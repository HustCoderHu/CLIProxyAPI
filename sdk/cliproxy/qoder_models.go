package cliproxy

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maps"
	"math/rand/v2"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/sync/singleflight"
)

const (
	qoderPersistedCatalogKey   = sdkauth.QoderPersistedCatalogMetaKey
	qoderCatalogCacheTTL       = registry.ModelsRefreshInterval
	qoderCatalogRetryBase      = time.Minute
	qoderCatalogRetryMax       = 30 * time.Minute
	qoderCatalogScanInterval   = time.Minute
	qoderCatalogGatewayHostCN  = "https://gateway.qoder.com.cn"
	qoderCatalogGatewayINTL1st = "https://api3.qoder.sh"
)

// qoderCatalogGatewayHosts lists the gateway candidates per provider. The COSY
// signature covers only the request path, so failing over between hosts keeps
// the same signed body valid. The INTL fallbacks mirror the reference
// implementation's gateway_candidates list.
var qoderCatalogGatewayHosts = map[string][]string{
	constant.QoderCN:   {qoderCatalogGatewayHostCN},
	constant.QoderINTL: {qoderCatalogGatewayINTL1st, "https://api2.qoder.sh", "https://api1.qoder.sh"},
}

// qoderCatalogCacheEntry tracks one account's catalog through probe, publication
// and persistence. Each revision field fences one stage so no global lock has to
// be held across network or registry calls.
type qoderCatalogCacheEntry struct {
	catalog           *sdkauth.QoderCatalog
	expiresAt         time.Time
	lastUsed          time.Time
	probeRevision     uint64
	appliedRevision   uint64
	appliedEpoch      uint64
	persistedRevision uint64
	pending           bool
}

// qoderFailureState keeps probe attempt history beyond nextRetry; clearing it at
// retry time would turn exponential backoff back into a fixed interval.
type qoderFailureState struct {
	failures    uint8
	lastFailure time.Time
	nextRetry   time.Time
}

func nextQoderFailure(previous qoderFailureState, now time.Time, randomInt64N func(int64) int64) qoderFailureState {
	if now.Sub(previous.lastFailure) > 2*qoderCatalogCacheTTL {
		previous.failures = 0
	}
	// Five failures reach the capped window: 2, 4, 8, 16, 30 minutes.
	failures := min(previous.failures, 4) + 1
	window := min(2*qoderCatalogRetryBase<<(failures-1), qoderCatalogRetryMax)
	// Equal jitter retains a minimum delay, including at the cap (15-30 minutes).
	half := window / 2
	delay := half + time.Duration(randomInt64N(int64(half)+1))
	return qoderFailureState{failures: failures, lastFailure: now, nextRetry: now.Add(delay)}
}

var (
	qoderNowFunc          = time.Now
	qoderCatalogMu        sync.RWMutex
	qoderCatalogCache     = make(map[string]qoderCatalogCacheEntry)
	qoderAuthFailureCache = make(map[string]qoderFailureState)
	qoderCatalogGroup     singleflight.Group
	qoderProbeSequence    atomic.Uint64
	qoderProbeSlots       = make(chan struct{}, modelRegistrationMaxWorkersPerCategory)
)

func (c qoderCatalogCacheEntry) cloneCatalog() *sdkauth.QoderCatalog {
	return cloneQoderCatalog(c.catalog)
}

func cloneQoderCatalog(catalog *sdkauth.QoderCatalog) *sdkauth.QoderCatalog {
	if catalog == nil {
		return nil
	}
	entries := make([]sdkauth.QoderModelEntry, len(catalog.Entries))
	for i, entry := range catalog.Entries {
		cloned := entry
		if entry.Thinking != nil {
			thinking := *entry.Thinking
			thinking.Levels = append([]string(nil), entry.Thinking.Levels...)
			cloned.Thinking = &thinking
		}
		entries[i] = cloned
	}
	return &sdkauth.QoderCatalog{Entries: entries, Revision: catalog.Revision}
}

// qoderCatalogKeyForRoute isolates catalogs by account, registration epoch,
// provider and gateway. The provider must be part of the key: CN and INTL can
// share an account id and would otherwise collide. The access token is
// deliberately excluded so a routine token refresh keeps the last known catalog;
// token rotation only resets the failure backoff, which is keyed separately.
func qoderCatalogKeyForRoute(auth *coreauth.Auth, provider, endpoint string) string {
	if auth == nil {
		return ""
	}
	identity := auth.ID
	if identity == "" {
		token, _ := auth.Metadata["access_token"].(string)
		identity = fmt.Sprintf("%x", sha256.Sum256([]byte(token)))
	}
	return fmt.Sprintf("%q/%d/%q/%q", identity, auth.RegistrationEpoch, provider, endpoint)
}

func qoderFailureKey(routeKey, accessToken string) string {
	return routeKey + fmt.Sprintf("/%x", sha256.Sum256([]byte(strings.TrimSpace(accessToken))))
}

func qoderProviderKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(auth.Provider))
}

// qoderProviderSupported reports whether a provider is served by this catalog
// path.
func qoderProviderSupported(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case constant.QoderCN, constant.QoderINTL:
		return true
	default:
		return false
	}
}

// qoderGatewayBaseURLs resolves the gateway candidates, honouring a per-credential
// base_url override placed on attributes by the auth synthesizer.
func qoderGatewayBaseURLs(auth *coreauth.Auth) []string {
	if auth != nil {
		if baseURL := strings.TrimRight(strings.TrimSpace(auth.Attributes["base_url"]), "/"); baseURL != "" {
			return []string{baseURL}
		}
		if auth.Metadata != nil {
			if baseURL, ok := auth.Metadata["base_url"].(string); ok {
				if trimmed := strings.TrimRight(strings.TrimSpace(baseURL), "/"); trimmed != "" {
					return []string{trimmed}
				}
			}
		}
	}
	if candidates := qoderCatalogGatewayHosts[qoderProviderKey(auth)]; len(candidates) > 0 {
		return candidates
	}
	return qoderCatalogGatewayHosts[constant.QoderCN]
}

func (s *Service) qoderModelFetchProxyURL(auth *coreauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if s != nil {
		s.cfgMu.RLock()
		defer s.cfgMu.RUnlock()
		if s.cfg != nil {
			return strings.TrimSpace(s.cfg.ProxyURL)
		}
	}
	return ""
}

func (s *Service) qoderCatalogKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	return qoderCatalogKeyForRoute(auth, qoderProviderKey(auth), qoderGatewayBaseURLs(auth)[0])
}

func (s *Service) qoderHomeEnabled() bool {
	if s == nil {
		return false
	}
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg != nil && s.cfg.Home.Enabled
}

func (s *Service) cachedQoderCatalog(auth *coreauth.Auth) *sdkauth.QoderCatalog {
	if auth == nil {
		return nil
	}
	endpoint := qoderGatewayBaseURLs(auth)[0]
	key := qoderCatalogKeyForRoute(auth, qoderProviderKey(auth), endpoint)
	return s.cachedQoderCatalogForKey(auth, endpoint, key)
}

func (s *Service) cachedQoderCatalogForKey(auth *coreauth.Auth, endpoint, key string) *sdkauth.QoderCatalog {
	qoderCatalogMu.RLock()
	entry := qoderCatalogCache[key]
	qoderCatalogMu.RUnlock()
	if entry.catalog != nil {
		return entry.cloneCatalog()
	}
	persisted, ok := s.persistedQoderCatalogForAuth(auth, endpoint)
	if !ok {
		return entry.cloneCatalog()
	}
	qoderCatalogMu.Lock()
	defer qoderCatalogMu.Unlock()
	entry = seedQoderCatalogCacheLocked(key, persisted)
	return entry.cloneCatalog()
}

// seedQoderCatalogCacheLocked installs a persisted catalog under a fresh
// revision so later probe and publication fences compare like with like.
func seedQoderCatalogCacheLocked(cacheKey string, persisted *sdkauth.QoderCatalog) qoderCatalogCacheEntry {
	entry := qoderCatalogCache[cacheKey]
	if entry.catalog != nil {
		return entry
	}
	seeded := cloneQoderCatalog(persisted)
	seededRevision := qoderProbeSequence.Add(1)
	seeded.Revision = seededRevision
	entry.catalog = seeded
	entry.probeRevision = seededRevision
	entry.persistedRevision = seededRevision
	entry.lastUsed = qoderNowFunc()
	qoderCatalogCache[cacheKey] = entry
	return entry
}

func (s *Service) persistedQoderCatalogForAuth(auth *coreauth.Auth, endpoint string) (*sdkauth.QoderCatalog, bool) {
	if auth == nil {
		return nil, false
	}
	var current *coreauth.Auth
	if s != nil && s.coreManager != nil && auth.ID != "" {
		var exists bool
		current, exists = s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || current.Disabled || current.RegistrationEpoch != auth.RegistrationEpoch {
			return nil, false
		}
	}
	if catalog, ok := sdkauth.ParsePersistedQoderCatalog(auth.Metadata, endpoint); ok {
		return catalog, true
	}
	if current != nil {
		return sdkauth.ParsePersistedQoderCatalog(current.Metadata, endpoint)
	}
	return nil, false
}

// qoderCatalogProbeRequest carries every field COSY needs. Missing identity
// fields fall back to stable substitutes so a credential created before the
// login flow filled them stays usable.
func qoderCatalogProbeRequest(auth *coreauth.Auth, baseURLs []string, proxyURL string) sdkauth.QoderModelsRequest {
	request := sdkauth.QoderModelsRequest{
		BaseURLs:         baseURLs,
		ProxyURL:         proxyURL,
		UID:              qoderCatalogAuthString(auth, "uid"),
		Nickname:         qoderCatalogAuthString(auth, "nickname"),
		AccessToken:      qoderCatalogAuthString(auth, "access_token"),
		RefreshToken:     qoderCatalogAuthString(auth, "refresh_token"),
		UserType:         qoderCatalogAuthString(auth, "user_type"),
		OrganizationID:   qoderCatalogAuthString(auth, "organization_id"),
		OrganizationName: qoderCatalogAuthString(auth, "organization_name"),
	}
	if request.UID == "" && auth != nil {
		// The COSY device fingerprint is derived from the uid. A credential
		// created before the login flow recorded one would otherwise produce an
		// unstable fingerprint; the auth id is stable per credential.
		request.UID = strings.TrimSpace(auth.ID)
	}
	return request
}

func qoderCatalogAuthString(auth *coreauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func (s *Service) fetchQoderModelCatalog(ctx context.Context, auth *coreauth.Auth) (string, *sdkauth.QoderCatalog) {
	if auth == nil || auth.Metadata == nil || s.qoderHomeEnabled() || !qoderProviderSupported(auth.Provider) {
		return "", nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	baseURLs := qoderGatewayBaseURLs(auth)
	proxyURL := s.qoderModelFetchProxyURL(auth)
	provider := qoderProviderKey(auth)
	cacheKey := qoderCatalogKeyForRoute(auth, provider, baseURLs[0])
	accessToken := qoderCatalogAuthString(auth, "access_token")
	if accessToken == "" || ctx.Err() != nil {
		return cacheKey, s.cachedQoderCatalogForKey(auth, baseURLs[0], cacheKey)
	}
	failureKey := qoderFailureKey(cacheKey, accessToken)
	lookup := func() (*sdkauth.QoderCatalog, bool) {
		persisted, hasPersisted := s.persistedQoderCatalogForAuth(auth, baseURLs[0])
		qoderCatalogMu.Lock()
		defer qoderCatalogMu.Unlock()
		entry, exists := qoderCatalogCache[cacheKey]
		if entry.catalog == nil && hasPersisted {
			entry = seedQoderCatalogCacheLocked(cacheKey, persisted)
			exists = true
		}
		now := qoderNowFunc()
		if exists {
			entry.lastUsed = now
			qoderCatalogCache[cacheKey] = entry
		}
		return entry.cloneCatalog(), now.Before(entry.expiresAt) || now.Before(qoderAuthFailureCache[failureKey].nextRetry)
	}
	if catalog, cached := lookup(); cached {
		return cacheKey, catalog
	}
	// The initiating caller owns the shared HTTP operation. Followers may cancel
	// their own wait without cancelling that operation or blocking a worker.
	resultCh := qoderCatalogGroup.DoChan(failureKey, func() (any, error) {
		if catalog, cached := lookup(); cached {
			return catalog, nil
		}
		probeCtx, finish, active := s.beginQoderAccountProbe(ctx, auth, cacheKey, failureKey, accessToken)
		if !active {
			return s.cachedQoderCatalogForKey(auth, baseURLs[0], cacheKey), nil
		}
		defer finish()
		revision := qoderProbeSequence.Add(1)
		qoderCatalogMu.Lock()
		entry := qoderCatalogCache[cacheKey]
		entry.probeRevision, entry.lastUsed, entry.pending = revision, qoderNowFunc(), true
		qoderCatalogCache[cacheKey] = entry
		qoderCatalogMu.Unlock()
		defer func() {
			qoderCatalogMu.Lock()
			current := qoderCatalogCache[cacheKey]
			if current.probeRevision == revision {
				current.pending = false
				qoderCatalogCache[cacheKey] = current
			}
			qoderCatalogMu.Unlock()
		}()
		select {
		case qoderProbeSlots <- struct{}{}:
		case <-probeCtx.Done():
			catalog, _ := lookup()
			return catalog, nil
		}
		defer func() { <-qoderProbeSlots }()
		catalog, status := s.probeQoderModelCatalog(probeCtx, auth, baseURLs, proxyURL)
		qoderCatalogMu.Lock()
		defer qoderCatalogMu.Unlock()
		current := qoderCatalogCache[cacheKey]
		if probeCtx.Err() != nil || current.probeRevision != revision {
			return current.cloneCatalog(), nil
		}
		now := qoderNowFunc()
		for key, other := range qoderCatalogCache {
			if key != cacheKey && !other.pending && now.Sub(other.lastUsed) > 2*qoderCatalogCacheTTL {
				delete(qoderCatalogCache, key)
			}
		}
		for key, failure := range qoderAuthFailureCache {
			if now.Sub(failure.lastFailure) > 2*qoderCatalogCacheTTL {
				delete(qoderAuthFailureCache, key)
			}
		}
		if status == sdkauth.QoderProbeStatusSuccess && catalog != nil {
			catalog.Revision = revision
			current.catalog, current.expiresAt, current.lastUsed = cloneQoderCatalog(catalog), now.Add(qoderCatalogCacheTTL), now
			qoderCatalogCache[cacheKey] = current
			delete(qoderAuthFailureCache, failureKey)
			return cloneQoderCatalog(catalog), nil
		}
		// Failures preserve the last successful catalog and grow the backoff.
		qoderAuthFailureCache[failureKey] = nextQoderFailure(qoderAuthFailureCache[failureKey], now, rand.Int64N)
		return current.cloneCatalog(), nil
	})
	select {
	case <-ctx.Done():
		catalog, _ := lookup()
		return cacheKey, catalog
	case result := <-resultCh:
		if result.Err != nil || result.Val == nil {
			catalog, _ := lookup()
			return cacheKey, catalog
		}
		return cacheKey, result.Val.(*sdkauth.QoderCatalog)
	}
}

func (s *Service) probeQoderModelCatalog(ctx context.Context, auth *coreauth.Auth, baseURLs []string, proxyURL string) (*sdkauth.QoderCatalog, sdkauth.QoderProbeStatus) {
	return sdkauth.FetchQoderModels(ctx, qoderCatalogProbeRequest(auth, baseURLs, proxyURL))
}

// qoderSeedModelIndex indexes the static seed by model ID. The seed only supplies
// metadata the live catalog omits (notably thinking_config, which the real
// /algo/api/v2/model/list response never carries); membership always comes from
// the live catalog.
func qoderSeedModelIndex(provider string) map[string]*ModelInfo {
	seeded := registry.GetQoderModels(provider)
	if len(seeded) == 0 {
		return nil
	}
	index := make(map[string]*ModelInfo, len(seeded))
	for _, model := range seeded {
		if model == nil || model.ID == "" {
			continue
		}
		index[model.ID] = model
	}
	return index
}

// qoderModelInfoFromEntry projects one catalog entry onto a registry model,
// overlaying it on the static seed entry for the same model ID.
//
// The live catalog is an incremental overlay: a field is taken from the live
// entry only when it is actually present, so a missing value never erases the
// seed. The reference implementation solves the same problem in merge_catalog by
// skipping null dynamic values (qoder_proxy.py:2729-2778).
//
// Thinking is level-only by design: the upstream catalog carries no token
// budget, so Min/Max/DynamicAllowed must never be set. Setting them would turn a
// pure level model into a budget model and clamp effort requests into budgets.
// seed may be nil when the live catalog lists a model the seed does not know.
func qoderModelInfoFromEntry(entry sdkauth.QoderModelEntry, provider string, seed *ModelInfo) *ModelInfo {
	modelID := strings.TrimSpace(entry.Key)
	if modelID == "" {
		return nil
	}
	contextLength := entry.MaxInputTokens
	if contextLength <= 0 && seed != nil {
		contextLength = seed.ContextLength
		if contextLength <= 0 {
			contextLength = seed.InputTokenLimit
		}
	}
	if contextLength <= 0 {
		contextLength = sdkauth.QoderDefaultMaxInputTokens()
	}
	// The parser falls back to the key when display_name is absent, which carries
	// no display information, so treat that case as "not provided".
	displayName := strings.TrimSpace(entry.DisplayName)
	if displayName == "" || displayName == modelID {
		displayName = ""
		if seed != nil {
			displayName = strings.TrimSpace(seed.DisplayName)
		}
		if displayName == "" {
			displayName = modelID
		}
	}
	info := &ModelInfo{
		ID:              modelID,
		Object:          "model",
		OwnedBy:         constant.QoderCN,
		Type:            provider,
		DisplayName:     displayName,
		ContextLength:   contextLength,
		InputTokenLimit: contextLength,
	}
	if entry.IsVL {
		info.SupportedInputModalities = []string{"text", "image"}
	} else if seed != nil && len(seed.SupportedInputModalities) > 0 {
		// The parser cannot distinguish an absent is_vl from an explicit false,
		// so a missing live value must not erase a seed that does advertise image
		// input.
		info.SupportedInputModalities = append([]string(nil), seed.SupportedInputModalities...)
	} else {
		info.SupportedInputModalities = []string{"text"}
	}
	switch {
	case entry.Thinking != nil:
		info.Thinking = &registry.ThinkingSupport{
			ZeroAllowed: entry.Thinking.ZeroAllowed,
			Levels:      append([]string(nil), entry.Thinking.Levels...),
		}
	case seed != nil && seed.Thinking != nil:
		// Project only the level-only fields so a seed that ever carries budget
		// fields cannot change the model capability class.
		info.Thinking = &registry.ThinkingSupport{
			ZeroAllowed: seed.Thinking.ZeroAllowed,
			Levels:      append([]string(nil), seed.Thinking.Levels...),
		}
	}
	return info
}

// qoderIsReservedCatalogKey reports whether a live catalog key collides with a
// framework-reserved model name. "auto" is resolved before any executor runs
// (internal/util/provider.go ResolveAutoModel), so publishing a Qoder entry for
// it is misleading and squats on a reserved name.
func qoderIsReservedCatalogKey(key string) bool {
	return strings.TrimSpace(key) == "auto"
}

func qoderModelsFromCatalog(catalog *sdkauth.QoderCatalog, provider string) []*ModelInfo {
	if catalog == nil {
		return nil
	}
	seed := qoderSeedModelIndex(provider)
	models := make([]*ModelInfo, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		if !entry.Enable {
			continue
		}
		if qoderIsReservedCatalogKey(entry.Key) {
			continue
		}
		if model := qoderModelInfoFromEntry(entry, provider, seed[strings.TrimSpace(entry.Key)]); model != nil {
			models = append(models, model)
		}
	}
	return models
}

// qoderModelsForCatalog applies the same registration modifiers as every other
// provider so exclusions, aliases, plugin models, settings and prefixes behave
// identically. The live catalog decides membership; the static seed only fills
// metadata the live response omits.
func (s *Service) qoderModelsForCatalog(auth *coreauth.Auth, catalog *sdkauth.QoderCatalog, cfg *config.Config) []*ModelInfo {
	if cfg == nil {
		cfg = &config.Config{}
	}
	provider := qoderProviderKey(auth)
	models := qoderModelsFromCatalog(catalog, provider)
	excluded := cfg.OAuthExcludedModels[coreauth.OAuthModelAliasChannel(provider, auth.AuthKind())]
	if value := strings.TrimSpace(auth.Attributes["excluded_models"]); value != "" {
		excluded = strings.Split(value, ",")
	}
	models = applyExcludedModels(models, excluded)
	models = applyOAuthModelAliasForAuth(cfg, provider, auth.AuthKind(), auth.Attributes, models)
	models = s.appendPluginModels(provider, models, excluded)
	models = applyOAuthSettingsForAuth(cfg, provider, auth.AuthKind(), models)
	return applyModelPrefixes(models, auth.Prefix, cfg.ForceModelPrefix)
}

func (s *Service) asyncProbeQoderCatalog(ctx context.Context, auth *coreauth.Auth, providerKey string) {
	s.queueQoderModelRefresh(ctx, auth, providerKey)
}

func (s *Service) probeAndRegisterQoderModels(ctx context.Context, auth *coreauth.Auth, providerKey string, expectedEpoch, expectedRegEpoch uint64) (string, *sdkauth.QoderCatalog) {
	expectedKey, catalog := s.fetchQoderModelCatalog(ctx, auth)
	s.applyQoderCatalog(ctx, auth, providerKey, catalog, expectedKey, expectedEpoch, expectedRegEpoch)
	return expectedKey, catalog
}

// qoderAuthModelSettingsEqual compares the actual model settings so exclusions
// and account aliases cannot be replayed stale. Request results change
// Generation, not these inputs, so Generation is excluded.
func qoderAuthModelSettingsEqual(a, b *coreauth.Auth) bool {
	return a != nil && b != nil && a.Provider == b.Provider && a.Prefix == b.Prefix &&
		a.RegistrationEpoch == b.RegistrationEpoch && a.Disabled == b.Disabled &&
		(a.Status == coreauth.StatusDisabled) == (b.Status == coreauth.StatusDisabled) &&
		a.AuthKind() == b.AuthKind() && maps.Equal(a.Attributes, b.Attributes)
}

// applyQoderCatalog publishes a catalog through the fenced replacement path.
// Publication is skipped unless the catalog is still the newest one the cache
// knows about, so a slow caller can never overwrite a newer catalog.
func (s *Service) applyQoderCatalog(ctx context.Context, auth *coreauth.Auth, providerKey string, catalog *sdkauth.QoderCatalog, expectedKey string, expectedEpoch, expectedRegEpoch uint64) {
	if auth == nil || catalog == nil || expectedKey == "" {
		return
	}
	if ctx.Err() != nil || s.qoderHomeEnabled() || !qoderProviderSupported(auth.Provider) {
		return
	}
	if s.qoderCatalogKey(auth) != expectedKey {
		return
	}
	if s.coreManager != nil {
		current, exists := s.coreManager.GetByID(auth.ID)
		if !exists || current == nil || current.Disabled || current.Provider != auth.Provider ||
			current.RegistrationEpoch != expectedEpoch || !qoderAuthModelSettingsEqual(current, auth) ||
			s.qoderCatalogKey(current) != expectedKey {
			return
		}
	}

	// Keep the config snapshot stable through publication. Registry CAS must
	// remain strict: an epoch from another publisher may use newer auth settings.
	s.cfgMu.RLock()
	models := s.qoderModelsForCatalog(auth, catalog, s.cfg)
	// Fence the cache-to-registry publication as well as the network result.
	qoderCatalogMu.Lock()
	entry := qoderCatalogCache[expectedKey]
	if entry.catalog == nil || entry.catalog.Revision != catalog.Revision || entry.appliedRevision == catalog.Revision {
		qoderCatalogMu.Unlock()
		s.cfgMu.RUnlock()
		return
	}
	reg := registry.GetGlobalRegistry()
	appliedEpoch, updated := reg.ReplaceClientModels(auth.ID, providerKey, expectedRegEpoch, models)
	if updated {
		entry.appliedRevision = catalog.Revision
		entry.appliedEpoch = appliedEpoch
		qoderCatalogCache[expectedKey] = entry
	}
	qoderCatalogMu.Unlock()
	s.cfgMu.RUnlock()
	if updated && s.coreManager != nil {
		s.coreManager.ReconcileRegistryModelStates(ctx, auth.ID)
		s.coreManager.RefreshSchedulerEntry(auth.ID)
	}
}

// persistQoderCatalogHints stores the last successful catalog in auth metadata so
// a cold start can publish models before the first probe completes. It reports
// whether the write was preempted by a foreground credential change, which the
// caller answers by queueing the request again.
func (s *Service) persistQoderCatalogHints(ctx context.Context, auth *coreauth.Auth, expectedKey, endpoint string, catalog *sdkauth.QoderCatalog) bool {
	if s == nil || s.coreManager == nil || auth == nil || auth.ID == "" || expectedKey == "" || catalog == nil || catalog.Revision == 0 {
		return false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return false
	}
	qoderCatalogMu.RLock()
	entry := qoderCatalogCache[expectedKey]
	stale := entry.catalog == nil || entry.catalog.Revision != catalog.Revision || entry.persistedRevision >= catalog.Revision
	qoderCatalogMu.RUnlock()
	if stale {
		return false
	}
	value := sdkauth.BuildPersistedQoderCatalog(endpoint, catalog)
	if value == nil {
		return false
	}
	persisted, preempted := s.coreManager.PersistMetadataEntry(ctx, auth.ID, auth.RegistrationEpoch, qoderPersistedCatalogKey, value, func(existing *coreauth.Auth) bool {
		if existing == nil || existing.Disabled || existing.Provider != auth.Provider || existing.RegistrationEpoch != auth.RegistrationEpoch || s.qoderCatalogKey(existing) != expectedKey {
			return false
		}
		qoderCatalogMu.RLock()
		currentEntry := qoderCatalogCache[expectedKey]
		valid := currentEntry.catalog != nil && currentEntry.catalog.Revision == catalog.Revision && currentEntry.persistedRevision < catalog.Revision
		qoderCatalogMu.RUnlock()
		return valid
	})
	if !persisted {
		return preempted
	}
	qoderCatalogMu.Lock()
	current := qoderCatalogCache[expectedKey]
	if current.catalog != nil && current.catalog.Revision == catalog.Revision && current.persistedRevision < catalog.Revision {
		current.persistedRevision = catalog.Revision
		qoderCatalogCache[expectedKey] = current
	}
	qoderCatalogMu.Unlock()
	return false
}

// refreshQoderCatalogs schedules account refreshes without waiting for the batch.
// One unfinished request must not block other accounts' retry or expiry
// deadlines. Network concurrency remains bounded by qoderProbeSlots.
func (s *Service) refreshQoderCatalogs(ctx context.Context) {
	if s == nil || s.coreManager == nil || s.qoderHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// A version change also invalidates connected probes. Cancel them before
	// queuing successors so a stalled old request cannot hold its account worker.
	s.cancelStaleQoderProbes("")
	for _, auth := range s.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || auth.Disabled || !qoderProviderSupported(auth.Provider) {
			continue
		}
		snapshot := auth.Clone()
		key := s.qoderCatalogKey(snapshot)
		failureKey := qoderFailureKey(key, qoderCatalogAuthString(snapshot, "access_token"))
		epoch := GlobalModelRegistry().ClientRegistrationEpoch(snapshot.ID)
		qoderCatalogMu.RLock()
		entry := qoderCatalogCache[key]
		registered := entry.catalog != nil && entry.appliedRevision == entry.catalog.Revision && entry.appliedEpoch == epoch
		persisted := entry.catalog == nil || entry.persistedRevision == entry.catalog.Revision
		fresh := (qoderNowFunc().Before(entry.expiresAt) && registered && persisted) || qoderNowFunc().Before(qoderAuthFailureCache[failureKey].nextRetry)
		qoderCatalogMu.RUnlock()
		if fresh {
			continue
		}
		s.scheduleQoderCatalogRefresh(ctx, snapshot)
	}
}

func (s *Service) scheduleQoderCatalogRefresh(ctx context.Context, auth *coreauth.Auth) {
	s.queueQoderModelRefresh(ctx, auth, qoderProviderKey(auth))
}

// nextQoderCatalogRefreshDelay preserves retry jitter in actual scheduling,
// rather than rounding every account's deadline up to the same minute tick.
// The upper bound still discovers accounts/config changes that bypass a wakeup.
func (s *Service) nextQoderCatalogRefreshDelay() time.Duration {
	now := qoderNowFunc()
	next := now.Add(qoderCatalogScanInterval)
	if s == nil || s.coreManager == nil || s.qoderHomeEnabled() {
		return qoderCatalogScanInterval
	}
	for _, auth := range s.coreManager.List() {
		if auth == nil || auth.Disabled || !qoderProviderSupported(auth.Provider) {
			continue
		}
		key := s.qoderCatalogKey(auth)
		failureKey := qoderFailureKey(key, qoderCatalogAuthString(auth, "access_token"))
		qoderCatalogMu.RLock()
		retry := qoderAuthFailureCache[failureKey].nextRetry
		expiry := qoderCatalogCache[key].expiresAt
		qoderCatalogMu.RUnlock()
		for _, deadline := range []time.Time{retry, expiry} {
			if deadline.After(now) && deadline.Before(next) {
				next = deadline
			}
		}
	}
	return next.Sub(now)
}

func (s *Service) runQoderModelRefresh(ctx context.Context) {
	wake := s.qoderModelRefreshWake()
	delay := s.nextQoderCatalogRefreshDelay()
	deadline := qoderNowFunc().Add(delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-wake:
			// Wakeups may advance a timer, never postpone an already due retry.
			delay = s.nextQoderCatalogRefreshDelay()
			earlier := qoderNowFunc().Add(delay)
			if earlier.Before(deadline) {
				deadline = earlier
				timer.Reset(delay)
			}
			continue
		case <-timer.C:
			s.refreshQoderCatalogs(ctx)
		}
		delay = s.nextQoderCatalogRefreshDelay()
		deadline = qoderNowFunc().Add(delay)
		timer.Reset(delay)
	}
}

// WaitQoderProbes waits for in-flight asynchronous Qoder catalog probes.
func (s *Service) WaitQoderProbes() {
	if s == nil {
		return
	}
	s.qoderProbeWg.Wait()
}
