package cliproxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"golang.org/x/sync/singleflight"
)

func resetQoderCatalogCache() {
	qoderCatalogMu.Lock()
	qoderCatalogCache = make(map[string]qoderCatalogCacheEntry)
	qoderAuthFailureCache = make(map[string]qoderFailureState)
	qoderCatalogMu.Unlock()
	qoderProbeSequence.Store(0)
	qoderCatalogGroup = singleflight.Group{}
}

// qoderTestSleep yields so the publication goroutine can make progress without
// depending on wall-clock timer resolution.
func qoderTestSleep() {
	runtime.Gosched()
	time.Sleep(time.Millisecond)
}

func TestQoderModelInfoFromEntryProjection(t *testing.T) {
	entry := sdkauth.QoderModelEntry{
		Key:            "qmodel_38max",
		DisplayName:    "Qwen3.8-Max",
		MaxInputTokens: 180000,
		IsVL:           true,
		IsReasoning:    true,
		Enable:         true,
		Thinking: &sdkauth.QoderThinkingConfig{
			Levels:      []string{"low", "medium", "xhigh"},
			ZeroAllowed: true,
		},
	}
	info := qoderModelInfoFromEntry(entry, "qoder-intl", nil)
	if info == nil {
		t.Fatalf("projection returned nil")
	}
	if info.ID != "qmodel_38max" || info.DisplayName != "Qwen3.8-Max" {
		t.Fatalf("unexpected identity: %+v", info)
	}
	if info.Type != "qoder-intl" {
		t.Fatalf("Type must be the provider key, got %q", info.Type)
	}
	if info.OwnedBy != "qoder" {
		t.Fatalf("OwnedBy must be %q, got %q", "qoder", info.OwnedBy)
	}
	if info.ContextLength != 180000 || info.InputTokenLimit != 180000 {
		t.Fatalf("context fields must mirror max_input_tokens: %+v", info)
	}
	if !slices.Contains(info.SupportedInputModalities, "image") {
		t.Fatalf("is_vl entry must advertise image input: %v", info.SupportedInputModalities)
	}
	if info.Thinking == nil {
		t.Fatalf("thinking_config present must yield Thinking support")
	}
	if !slices.Equal(info.Thinking.Levels, entry.Thinking.Levels) || !info.Thinking.ZeroAllowed {
		t.Fatalf("thinking mapping mismatch: %+v", info.Thinking)
	}
	assertQoderThinkingHasNoBudget(t, info)
}

func TestQoderModelInfoNilThinkingAndFallbacks(t *testing.T) {
	info := qoderModelInfoFromEntry(sdkauth.QoderModelEntry{
		Key:            "no-seed-model",
		MaxInputTokens: 0,
	}, "qoder", nil)
	if info == nil {
		t.Fatalf("projection returned nil")
	}
	if info.Thinking != nil {
		t.Fatalf("entry without thinking_config must produce nil Thinking, got %+v", info.Thinking)
	}
	if info.ContextLength != sdkauth.QoderDefaultMaxInputTokens() || info.InputTokenLimit != sdkauth.QoderDefaultMaxInputTokens() {
		t.Fatalf("missing max_input_tokens must fall back to %d, got %+v", sdkauth.QoderDefaultMaxInputTokens(), info)
	}
	if slices.Contains(info.SupportedInputModalities, "image") {
		t.Fatalf("non-VL entry must not advertise image input: %v", info.SupportedInputModalities)
	}
	if info.DisplayName != "no-seed-model" {
		t.Fatalf("missing display_name must fall back to the key, got %q", info.DisplayName)
	}
}

// assertQoderThinkingHasNoBudget enforces the level-only contract: a Qoder model
// must never be classified as budget-capable, or effort requests would be
// clamped into token budgets.
func assertQoderThinkingHasNoBudget(t *testing.T, info *ModelInfo) {
	t.Helper()
	if info.Thinking == nil {
		return
	}
	if info.Thinking.Min != 0 || info.Thinking.Max != 0 {
		t.Fatalf("%s must not set thinking Min/Max, got %+v", info.ID, info.Thinking)
	}
	if info.Thinking.DynamicAllowed {
		t.Fatalf("%s must not allow dynamic budgets", info.ID)
	}
	if info.Thinking.DisableType != "" {
		t.Fatalf("%s must not set DisableType", info.ID)
	}
}

func TestQoderQoderModelsFromCatalogFiltersDisabled(t *testing.T) {
	catalog := &sdkauth.QoderCatalog{Entries: []sdkauth.QoderModelEntry{
		{Key: "enabled", Enable: true},
		{Key: "disabled", Enable: false},
		{Key: "  ", Enable: true},
	}}
	models := qoderModelsFromCatalog(catalog, "qoder")
	if len(models) != 1 || models[0].ID != "enabled" {
		t.Fatalf("enable=false and empty keys must be dropped, got %+v", models)
	}
}

// TestQoderCatalogSeedMerge pins the live-over-seed overlay contract. The live
// catalog decides membership and overrides only the fields it actually carries;
// the real /algo/api/v2/model/list response has no thinking_config at all, so a
// nil live Thinking must keep the seed's levels instead of erasing them.
func TestQoderCatalogSeedMerge(t *testing.T) {
	cases := []struct {
		name         string
		entry        sdkauth.QoderModelEntry
		wantThinking *registry.ThinkingSupport
		wantDisplay  string
		wantContext  int
	}{
		{
			name:         "missing thinking_config keeps seed levels",
			entry:        sdkauth.QoderModelEntry{Key: "qmodel_38max", Enable: true},
			wantThinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "medium", "xhigh"}},
			wantDisplay:  "Qwen3.8-Max",
			wantContext:  180000,
		},
		{
			name:         "model absent from the seed gets no invented thinking",
			entry:        sdkauth.QoderModelEntry{Key: "live-only-model", Enable: true},
			wantThinking: nil,
			wantDisplay:  "live-only-model",
			wantContext:  sdkauth.QoderDefaultMaxInputTokens(),
		},
		{
			name: "live thinking_config wins over the seed",
			entry: sdkauth.QoderModelEntry{
				Key:    "dmodel",
				Enable: true,
				Thinking: &sdkauth.QoderThinkingConfig{
					Levels:      []string{"low"},
					ZeroAllowed: false,
				},
			},
			wantThinking: &registry.ThinkingSupport{Levels: []string{"low"}},
			wantDisplay:  "DeepSeek-V4-Pro",
			wantContext:  96000,
		},
		{
			name:         "empty live display_name and zero tokens preserve seed values",
			entry:        sdkauth.QoderModelEntry{Key: "qmodel_38max", Enable: true, DisplayName: "", MaxInputTokens: 0},
			wantThinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "medium", "xhigh"}},
			wantDisplay:  "Qwen3.8-Max",
			wantContext:  180000,
		},
		{
			name: "parser display_name fallback still preserves the seed name",
			entry: sdkauth.QoderModelEntry{
				Key:            "qmodel_38max",
				DisplayName:    "qmodel_38max",
				MaxInputTokens: 0,
				Enable:         true,
			},
			wantThinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "medium", "xhigh"}},
			wantDisplay:  "Qwen3.8-Max",
			wantContext:  180000,
		},
		{
			name: "live display_name and tokens override the seed",
			entry: sdkauth.QoderModelEntry{
				Key:            "qmodel_38max",
				DisplayName:    "Qwen 3.8 Max (live)",
				MaxInputTokens: 200000,
				IsVL:           true,
				Enable:         true,
			},
			wantThinking: &registry.ThinkingSupport{ZeroAllowed: true, Levels: []string{"low", "medium", "xhigh"}},
			wantDisplay:  "Qwen 3.8 Max (live)",
			wantContext:  200000,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			models := qoderModelsFromCatalog(&sdkauth.QoderCatalog{Entries: []sdkauth.QoderModelEntry{tc.entry}}, "qoder")
			if len(models) != 1 {
				t.Fatalf("want exactly one published model, got %d", len(models))
			}
			info := models[0]
			if info.Thinking == nil != (tc.wantThinking == nil) {
				t.Fatalf("thinking presence mismatch: got %+v", info.Thinking)
			}
			if tc.wantThinking != nil {
				if !slices.Equal(info.Thinking.Levels, tc.wantThinking.Levels) ||
					info.Thinking.ZeroAllowed != tc.wantThinking.ZeroAllowed {
					t.Fatalf("thinking mismatch:\n got %+v\nwant %+v", info.Thinking, tc.wantThinking)
				}
				assertQoderThinkingHasNoBudget(t, info)
			}
			if info.DisplayName != tc.wantDisplay {
				t.Fatalf("display name = %q, want %q", info.DisplayName, tc.wantDisplay)
			}
			if info.ContextLength != tc.wantContext || info.InputTokenLimit != tc.wantContext {
				t.Fatalf("context = %d/%d, want %d", info.ContextLength, info.InputTokenLimit, tc.wantContext)
			}
		})
	}
}

// TestQoderCatalogSeedDoesNotAddMembership guards against the seed leaking models
// the live probe did not return.
func TestQoderCatalogSeedDoesNotAddMembership(t *testing.T) {
	models := qoderModelsFromCatalog(&sdkauth.QoderCatalog{Entries: []sdkauth.QoderModelEntry{
		{Key: "qmodel_38max", Enable: true},
	}}, "qoder")
	if len(models) != 1 || models[0].ID != "qmodel_38max" {
		t.Fatalf("only live entries may be published, got %+v", modelIDs(models))
	}
	seed := registry.GetQoderModels("qoder")
	if len(seed) < 2 {
		t.Fatalf("CN seed must know more models than the live fixture, got %d", len(seed))
	}
	for _, model := range models {
		if model.ID != "qmodel_38max" {
			t.Fatalf("seed-only model %q must not be published", model.ID)
		}
	}
}

// TestQoderCatalogSkipsReservedAutoModel pins that a live catalog entry named
// "auto" is never published. The framework resolves that name before the
// executor runs (internal/util/provider.go ResolveAutoModel), so publishing a
// Qoder model for it would be misleading; every other live entry must survive.
func TestQoderCatalogSkipsReservedAutoModel(t *testing.T) {
	models := qoderModelsFromCatalog(&sdkauth.QoderCatalog{Entries: []sdkauth.QoderModelEntry{
		{Key: "auto", DisplayName: "Auto", Enable: true, MaxInputTokens: 200000},
		{Key: "qmodel_38max", Enable: true, MaxInputTokens: 180000},
	}}, "qoder")
	if len(models) != 1 {
		t.Fatalf("only non-reserved live entries may be published, got %v", modelIDs(models))
	}
	if models[0].ID != "qmodel_38max" {
		t.Fatalf("live model next to the reserved key must survive, got %v", modelIDs(models))
	}
	if slices.Contains(modelIDs(models), "auto") {
		t.Fatalf("framework-reserved model %q must not be published", "auto")
	}
}

// TestQoderCatalogUsesProviderSpecificSeed checks CN and INTL resolve their own
// seed: dmodel exists only in the CN seed, so INTL must not inherit its levels.
func TestQoderCatalogUsesProviderSpecificSeed(t *testing.T) {
	live := &sdkauth.QoderCatalog{Entries: []sdkauth.QoderModelEntry{
		{Key: "dmodel", Enable: true},
		{Key: "qmodel_38max", Enable: true},
	}}
	cn := qoderModelsFromCatalog(live, "qoder")
	intl := qoderModelsFromCatalog(live, "qoder-intl")
	if len(cn) != 2 || len(intl) != 2 {
		t.Fatalf("unexpected publication sizes: cn=%d intl=%d", len(cn), len(intl))
	}
	byID := func(models []*ModelInfo) map[string]*ModelInfo {
		out := make(map[string]*ModelInfo, len(models))
		for _, model := range models {
			out[model.ID] = model
		}
		return out
	}
	cnByID, intlByID := byID(cn), byID(intl)
	if info := cnByID["dmodel"]; info.Thinking == nil || !slices.Equal(info.Thinking.Levels, []string{"high", "max"}) {
		t.Fatalf("CN dmodel must inherit the CN seed levels, got %+v", info.Thinking)
	}
	if info := intlByID["dmodel"]; info.Thinking != nil {
		t.Fatalf("INTL dmodel is not in the INTL seed and must stay nil, got %+v", info.Thinking)
	}
	if info := intlByID["qmodel_38max"]; info.Thinking == nil || !slices.Equal(info.Thinking.Levels, []string{"low", "medium", "xhigh"}) {
		t.Fatalf("INTL qmodel_38max must inherit the INTL seed levels, got %+v", info.Thinking)
	}
	for _, model := range intl {
		if model.Type != "qoder-intl" {
			t.Fatalf("INTL publication must keep the INTL provider type, got %q", model.Type)
		}
	}
}

func modelIDs(models []*ModelInfo) []string {
	ids := make([]string, 0, len(models))
	for _, model := range models {
		if model != nil {
			ids = append(ids, model.ID)
		}
	}
	return ids
}

func TestQoderProviderSupported(t *testing.T) {
	for _, provider := range []string{"qoder", "QODER", "qoder-intl", " Qoder-INTL "} {
		if !qoderProviderSupported(provider) {
			t.Fatalf("%q must be supported", provider)
		}
	}
	for _, provider := range []string{"", "antigravity", "qoder-cn"} {
		if qoderProviderSupported(provider) {
			t.Fatalf("%q must not be served by the qoder catalog path", provider)
		}
	}
}

func TestQoderGatewayBaseURLsPerProvider(t *testing.T) {
	cn := qoderGatewayBaseURLs(&coreauth.Auth{Provider: "qoder"})
	if !slices.Equal(cn, []string{"https://gateway.qoder.com.cn"}) {
		t.Fatalf("unexpected CN gateways: %v", cn)
	}
	intl := qoderGatewayBaseURLs(&coreauth.Auth{Provider: "qoder-intl"})
	if len(intl) != 3 || intl[0] != "https://api3.qoder.sh" {
		t.Fatalf("unexpected INTL gateways: %v", intl)
	}
	override := qoderGatewayBaseURLs(&coreauth.Auth{
		Provider:   "qoder",
		Attributes: map[string]string{"base_url": "https://custom.example/"},
	})
	if !slices.Equal(override, []string{"https://custom.example"}) {
		t.Fatalf("attribute override must win, got %v", override)
	}
}

func TestQoderCatalogKeyIncludesProvider(t *testing.T) {
	cn := qoderCatalogKeyForRoute(&coreauth.Auth{ID: "acct", Provider: "qoder"}, "qoder", "host")
	intl := qoderCatalogKeyForRoute(&coreauth.Auth{ID: "acct", Provider: "qoder-intl"}, "qoder-intl", "host")
	if cn == intl {
		t.Fatalf("provider must be part of the cache key, both were %q", cn)
	}
	if cn == "" || intl == "" {
		t.Fatalf("keys must not be empty")
	}
}

func TestQoderFailureBackoffGrowsAndCaps(t *testing.T) {
	now := qoderNowFunc()
	state := qoderFailureState{}
	for i := 1; i <= 5; i++ {
		state = nextQoderFailure(state, now, func(int64) int64 { return 0 })
		if state.failures != uint8(i) {
			t.Fatalf("attempt %d: failures = %d", i, state.failures)
		}
	}
	// At the cap the window is 30 minutes, so the minimum delay is 15 minutes.
	if got := state.nextRetry.Sub(now); got < 15*60*1e9 || got > 30*60*1e9 {
		t.Fatalf("capped delay out of range: %v", got)
	}
}

// ---------------------------------------------------------------------------
// End-to-end publication through a mock upstream
// ---------------------------------------------------------------------------

// qoderMockUpstream serves a catalog and lets a test swap the response body.
type qoderMockUpstream struct {
	server *httptest.Server
	body   atomic.Value
	status atomic.Int32
	calls  atomic.Int32
}

func newQoderMockUpstream(t *testing.T, body string) *qoderMockUpstream {
	t.Helper()
	upstream := &qoderMockUpstream{}
	upstream.body.Store(body)
	upstream.status.Store(http.StatusOK)
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.calls.Add(1)
		w.WriteHeader(int(upstream.status.Load()))
		_, _ = w.Write([]byte(upstream.body.Load().(string)))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func newQoderTestAuth(id, provider, endpoint string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:         id,
		Provider:   provider,
		Attributes: map[string]string{"base_url": endpoint},
		Metadata: map[string]any{
			"access_token": "drt-test-token",
			"uid":          "uid-" + id,
			"user_type":    "personal_professional_trial",
		},
	}
}

// waitForQoderClientModel polls until the registry publishes modelID for clientID.
func waitForQoderClientModel(t *testing.T, clientID, modelID string) bool {
	t.Helper()
	for i := 0; i < 400; i++ {
		if GlobalModelRegistry().ClientSupportsModel(clientID, modelID) {
			return true
		}
		qoderTestSleep()
	}
	return false
}

func TestQoderCatalogPublication(t *testing.T) {
	resetQoderCatalogCache()
	t.Cleanup(resetQoderCatalogCache)
	const catalogFixture = `{"chat":[
		{"key":"qmodel_38max","display_name":"Qwen3.8-Max","enable":true,"is_vl":true,"max_input_tokens":180000,
		 "thinking_config":{"disabled":{},"enabled":{"efforts":{"low":{},"medium":{},"xhigh":{}}}}},
		{"key":"mmodel","display_name":"MiniMax","enable":true,"is_vl":false,"max_input_tokens":1000},
		{"key":"secret-model","display_name":"Secret","enable":false}
	]}`
	upstream := newQoderMockUpstream(t, catalogFixture)

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := newQoderTestAuth("qoder-publication", "qoder", upstream.server.URL)
	var errRegister error
	auth, errRegister = manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	if !svc.registerModelsForAuthWithCache(context.Background(), auth, nil) {
		t.Fatalf("registration reported a superseded fence")
	}
	svc.WaitQoderProbes()

	if !waitForQoderClientModel(t, auth.ID, "qmodel_38max") {
		t.Fatalf("qmodel_38max was not published; upstream calls=%d", upstream.calls.Load())
	}
	if !waitForQoderClientModel(t, auth.ID, "mmodel") {
		t.Fatalf("mmodel was not published")
	}
	if GlobalModelRegistry().ClientSupportsModel(auth.ID, "secret-model") {
		t.Fatalf("enable=false model must never be published")
	}

	models := GlobalModelRegistry().GetModelsForClient(auth.ID)
	byID := make(map[string]*ModelInfo, len(models))
	for _, model := range models {
		byID[model.ID] = model
	}
	info := byID["qmodel_38max"]
	if info == nil {
		t.Fatalf("missing published info for qmodel_38max")
	}
	if info.Type != "qoder" || info.OwnedBy != "qoder" {
		t.Fatalf("published metadata mismatch: %+v", info)
	}
	if info.Thinking == nil || !slices.Equal(info.Thinking.Levels, []string{"low", "medium", "xhigh"}) {
		t.Fatalf("thinking levels were not projected: %+v", info.Thinking)
	}
	assertQoderThinkingHasNoBudget(t, info)
	if mmodel := byID["mmodel"]; mmodel != nil && mmodel.Thinking != nil {
		t.Fatalf("mmodel must stay without thinking support, got %+v", mmodel.Thinking)
	}
}

func TestQoderCatalogPublicationAppliesPrefixesAndExclusions(t *testing.T) {
	resetQoderCatalogCache()
	t.Cleanup(resetQoderCatalogCache)
	const catalogFixture = `{"chat":[
		{"key":"qmodel_38max","display_name":"Qwen3.8-Max","enable":true,"max_input_tokens":180000},
		{"key":"qfmodel","display_name":"Qwen3.8-Flash","enable":true,"max_input_tokens":180000}
	]}`
	upstream := newQoderMockUpstream(t, catalogFixture)

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{
		cfg:         &config.Config{OAuthExcludedModels: map[string][]string{"qoder": {"qfmodel"}}},
		coreManager: manager,
	}
	auth := newQoderTestAuth("qoder-prefix", "qoder", upstream.server.URL)
	auth.Prefix = "cn"
	var errRegister error
	auth, errRegister = manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	if !svc.registerModelsForAuthWithCache(context.Background(), auth, nil) {
		t.Fatalf("registration reported a superseded fence")
	}
	svc.WaitQoderProbes()

	if !waitForQoderClientModel(t, auth.ID, "cn/qmodel_38max") {
		t.Fatalf("prefixed model was not published")
	}
	if GlobalModelRegistry().ClientSupportsModel(auth.ID, "cn/qfmodel") {
		t.Fatalf("excluded model must not be published")
	}
}

func TestQoderCatalogPersistedColdStartSeedsModels(t *testing.T) {
	resetQoderCatalogCache()
	t.Cleanup(resetQoderCatalogCache)
	upstream := newQoderMockUpstream(t, `{"chat":[{"key":"qmodel_38max","enable":true}]}`)

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	auth := newQoderTestAuth("qoder-coldstart", "qoder", upstream.server.URL)
	// Seed the metadata snapshot exactly as a previous run would have.
	auth.Metadata[sdkauth.QoderPersistedCatalogMetaKey] = map[string]any{
		"endpoint": upstream.server.URL,
		"models": []any{
			map[string]any{
				"key":          "persisted-model",
				"display_name": "Persisted",
				"enable":       true,
				"thinking":     map[string]any{"levels": []any{"high"}, "zero_allowed": false},
			},
		},
	}
	var errRegister error
	auth, errRegister = manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	cached := svc.cachedQoderCatalog(auth)
	if cached == nil || len(cached.Entries) != 1 || cached.Entries[0].Key != "persisted-model" {
		t.Fatalf("persisted snapshot was not seeded: %+v", cached)
	}

	svc.registerModelsForAuthWithCache(context.Background(), auth, nil)
	if !waitForQoderClientModel(t, auth.ID, "persisted-model") {
		models := GlobalModelRegistry().GetModelsForClient(auth.ID)
		t.Fatalf("cold start must publish the persisted catalog before probing, got %v", models)
	}
	svc.WaitQoderProbes()
}

func TestQoderCatalogRejectedWhenHomeEnabled(t *testing.T) {
	resetQoderCatalogCache()
	t.Cleanup(resetQoderCatalogCache)
	upstream := newQoderMockUpstream(t, `{"chat":[{"key":"qmodel","enable":true}]}`)

	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{Home: internalconfig.HomeConfig{Enabled: true}}, coreManager: manager}
	auth := newQoderTestAuth("qoder-home", "qoder", upstream.server.URL)
	var errRegister error
	auth, errRegister = manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })

	if catalog := svc.cachedQoderCatalog(auth); catalog != nil {
		t.Fatalf("Home mode must not seed a catalog: %+v", catalog)
	}
	svc.asyncProbeQoderCatalog(context.Background(), auth, "qoder")
	svc.WaitQoderProbes()
	if calls := upstream.calls.Load(); calls != 0 {
		t.Fatalf("Home mode must not probe the upstream, got %d calls", calls)
	}
}

func TestQoderRegistryStaticSeeds(t *testing.T) {
	cn := registry.GetQoderModels("qoder")
	if len(cn) != 13 {
		t.Fatalf("CN seed must contain 13 models, got %d", len(cn))
	}
	for _, model := range cn {
		if model.Type != "qoder" || model.OwnedBy != "qoder" {
			t.Fatalf("CN seed metadata mismatch: %+v", model)
		}
		// "auto" is reserved by the framework (internal/util/provider.go
		// ResolveAutoModel) and must never be seeded as a Qoder model.
		if model.ID == "auto" {
			t.Fatalf("CN seed must not carry the framework-reserved name %q", model.ID)
		}
		assertQoderThinkingHasNoBudget(t, model)
	}
	intl := registry.GetQoderModels("qoder-intl")
	if len(intl) != 2 {
		t.Fatalf("INTL seed must contain only the 2 enabled models, got %d", len(intl))
	}
	for _, model := range intl {
		if model.Type != "qoder-intl" {
			t.Fatalf("INTL seed metadata mismatch: %+v", model)
		}
		if model.ID != "qmodel_38max" && model.ID != "qfmodel" {
			t.Fatalf("INTL seed must only carry enable=true models, got %q", model.ID)
		}
		assertQoderThinkingHasNoBudget(t, model)
	}
	if registry.GetQoderModels("nope") != nil {
		t.Fatalf("unknown provider must yield nil")
	}

	if got := registry.GetStaticModelDefinitionsByChannel("qoder"); len(got) != 13 {
		t.Fatalf("management channel lookup for qoder returned %d models", len(got))
	}
	if got := registry.GetStaticModelDefinitionsByChannel("qoder-intl"); len(got) != 2 {
		t.Fatalf("management channel lookup for qoder-intl returned %d models", len(got))
	}
	if found := registry.LookupStaticModelInfo("qmodel_38max"); found == nil {
		t.Fatalf("LookupStaticModelInfo must find a qoder seed")
	}
}

// TestQoderSeedCatalogJSONShape asserts the management endpoint can serialize
// the seeds without leaking internal fields.
func TestQoderSeedCatalogJSONShape(t *testing.T) {
	data, errMarshal := json.Marshal(registry.GetQoderModels("qoder-intl"))
	if errMarshal != nil {
		t.Fatalf("marshal seeds: %v", errMarshal)
	}
	if !strings.Contains(string(data), `"id":"qmodel_38max"`) {
		t.Fatalf("unexpected seed JSON: %s", data)
	}
	if strings.Contains(string(data), `"min":`) || strings.Contains(string(data), `"max":`) {
		t.Fatalf("seed JSON must not carry budget fields: %s", data)
	}
}
