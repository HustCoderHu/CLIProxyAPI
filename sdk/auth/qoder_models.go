package auth

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// QoderModelsPath is the upstream per-account model catalog endpoint. The
	// query parameters are copied verbatim from the reference implementation
	// (qoder_proxy.py:332); the path alone is covered by the COSY signature.
	QoderModelsPath = "/algo/api/v2/model/list?Encode=1"
	// QoderPersistedCatalogMetaKey is the auth metadata key holding the last
	// successful catalog snapshot.
	QoderPersistedCatalogMetaKey = "qoder_model_catalog_hints"

	qoderMaxCatalogEntries = 1024
	qoderMaxModelKeyBytes  = 256
	qoderMaxCatalogBytes   = 8 << 20
)

// qoderDefaultMaxInputTokens mirrors the request-side fallback used by the body
// builder when the catalog omits max_input_tokens (qoder_proxy.py:6294).
const qoderDefaultMaxInputTokens = 180000

// qoderNoEffortLevels is the documented expression for a model whose
// thinking_config.enabled carries no efforts table (qmodel / qmodel_latest).
// The upstream accepts no per-level effort for those models, so only "high" is
// advertised; combined with zero_allowed this keeps "none" usable while
// rejecting unsupported levels instead of silently leaking them upstream.
// See .trellis/tasks/11-06-qoder-provider/research/03-thinking-mapping.md.
var qoderNoEffortLevels = []string{"high"}

// QoderThinkingConfig is the normalized thinking capability of one catalog entry.
type QoderThinkingConfig struct {
	// Levels are the accepted reasoning effort names, sorted ascending.
	Levels []string
	// ZeroAllowed reports whether the upstream exposes a "disabled" block,
	// which is the only way this catalog expresses "thinking off".
	ZeroAllowed bool
}

// QoderModelEntry is one normalized entry of the upstream Qoder model catalog.
type QoderModelEntry struct {
	Key            string
	DisplayName    string
	MaxInputTokens int
	IsVL           bool
	IsReasoning    bool
	Enable         bool
	// Thinking is nil when the entry carries no thinking_config at all.
	Thinking *QoderThinkingConfig
}

// QoderCatalog is the normalized, enable-filtered model catalog of one account.
type QoderCatalog struct {
	// Entries are sorted by key for stable publication.
	Entries []QoderModelEntry
	// Revision is runtime fencing metadata assigned by the probe layer. It is
	// never persisted and never sent upstream.
	Revision uint64 `json:"-"`
}

// QoderProbeStatus classifies the outcome of a Qoder catalog probe.
type QoderProbeStatus int

const (
	// QoderProbeStatusSuccess means the catalog was fetched and parsed.
	QoderProbeStatusSuccess QoderProbeStatus = iota
	// QoderProbeStatusAuthError means the credential was rejected.
	QoderProbeStatusAuthError
	// QoderProbeStatusTransientError means a retry may succeed.
	QoderProbeStatusTransientError
)

// QoderModelsRequest configures a single-shot catalog probe.
type QoderModelsRequest struct {
	// BaseURLs are the gateway candidates in preference order.
	BaseURLs []string
	// ProxyURL optionally routes the probe through a proxy.
	ProxyURL string

	// COSY identity fields. They must match the credential used for inference.
	UID              string
	Nickname         string
	AccessToken      string
	RefreshToken     string
	UserType         string
	OrganizationID   string
	OrganizationName string
}

// FetchQoderModels performs the COSY-signed model catalog request against the
// first reachable gateway candidate and returns the enable-filtered catalog.
//
// The request is a GET that still carries a body: the upstream verifies the
// COSY signature against the encoded body, so a bodyless GET is rejected with
// 403 (qoder_proxy.py:3039-3060).
func FetchQoderModels(ctx context.Context, req QoderModelsRequest) (*QoderCatalog, QoderProbeStatus) {
	baseURLs := qoderCleanBaseURLs(req.BaseURLs)
	if len(baseURLs) == 0 {
		return nil, QoderProbeStatusTransientError
	}
	accessToken := strings.TrimSpace(req.AccessToken)
	if accessToken == "" {
		return nil, QoderProbeStatusAuthError
	}
	if ctx == nil {
		ctx = context.Background()
	}
	session, errSession := helps.NewCosySession(
		strings.TrimSpace(req.UID), req.Nickname, accessToken, req.RefreshToken,
		req.UserType, req.OrganizationID, req.OrganizationName,
	)
	if errSession != nil {
		log.Warnf("qoder model catalog: build cosy session failed: %v", errSession)
		return nil, QoderProbeStatusTransientError
	}
	encodedBody := helps.QoderEncode([]byte("{}"))

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	transport, _, errProxy := proxyutil.BuildHTTPTransport(strings.TrimSpace(req.ProxyURL))
	if errProxy != nil {
		return nil, QoderProbeStatusTransientError
	}
	if transport != nil {
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}

	// The signature only covers the request path, so switching hosts keeps the
	// same body and headers valid.
	status := QoderProbeStatusTransientError
	for _, baseURL := range baseURLs {
		if ctx.Err() != nil {
			return nil, QoderProbeStatusTransientError
		}
		rawURL := baseURL + QoderModelsPath
		doc, statusCode := qoderDoCatalogRequest(ctx, client, rawURL, encodedBody, session)
		if doc != nil {
			return doc, QoderProbeStatusSuccess
		}
		status = statusCode
		if statusCode == QoderProbeStatusAuthError {
			// A rejected credential does not improve on another host.
			return nil, status
		}
	}
	return nil, status
}

func qoderDoCatalogRequest(ctx context.Context, client *http.Client, rawURL, encodedBody string, session *helps.CosySession) (*QoderCatalog, QoderProbeStatus) {
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, strings.NewReader(encodedBody))
	if errReq != nil {
		return nil, QoderProbeStatusTransientError
	}
	for key, value := range session.Headers(encodedBody, rawURL, "", false) {
		httpReq.Header.Set(key, value)
	}
	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return nil, QoderProbeStatusTransientError
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("qoder model catalog: response close failed")
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, QoderProbeStatusAuthError
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, QoderProbeStatusTransientError
	}
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, qoderMaxCatalogBytes+1))
	if errRead != nil || len(data) > qoderMaxCatalogBytes {
		return nil, QoderProbeStatusTransientError
	}
	catalog, ok := ParseQoderModelList(data)
	if !ok {
		return nil, QoderProbeStatusTransientError
	}
	return catalog, QoderProbeStatusSuccess
}

func qoderCleanBaseURLs(candidates []string) []string {
	urls := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		trimmed := strings.TrimRight(strings.TrimSpace(candidate), "/")
		if trimmed == "" {
			continue
		}
		if _, exists := seen[trimmed]; exists {
			continue
		}
		seen[trimmed] = struct{}{}
		urls = append(urls, trimmed)
	}
	return urls
}

// ParseQoderModelList decodes the {"chat": [...]} catalog payload.
//
// Entries without a key are skipped, a missing "enable" means enabled, and an
// entry with enable == false is dropped: the international catalog disables 15
// of 17 models per account, so publishing them would advertise unusable models.
// It returns ok = false only for malformed payloads.
func ParseQoderModelList(body []byte) (*QoderCatalog, bool) {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return nil, false
	}
	top := ojson.ParseBytes(body)
	if !top.IsObject() {
		return nil, false
	}
	chat := top.Get("chat")
	if !chat.Exists() || chat.Type == ojson.Null {
		return nil, false
	}
	if !chat.IsArray() {
		return nil, false
	}
	items := chat.Array()
	if len(items) > qoderMaxCatalogEntries {
		return nil, false
	}
	entries := make([]QoderModelEntry, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		entry, ok := parseQoderCatalogEntry(item)
		if !ok {
			continue
		}
		if _, exists := seen[entry.Key]; exists {
			continue
		}
		seen[entry.Key] = struct{}{}
		if !entry.Enable {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return &QoderCatalog{Entries: entries}, true
}

// parseQoderCatalogEntry normalizes one catalog item. It returns ok = false for
// entries the catalog must skip (missing or oversized key).
func parseQoderCatalogEntry(item ojson.Result) (QoderModelEntry, bool) {
	if !item.IsObject() {
		return QoderModelEntry{}, false
	}
	key := strings.TrimSpace(item.Get("key").String())
	if key == "" || len(key) > qoderMaxModelKeyBytes {
		return QoderModelEntry{}, false
	}
	entry := QoderModelEntry{
		Key:            key,
		DisplayName:    strings.TrimSpace(item.Get("display_name").String()),
		MaxInputTokens: int(item.Get("max_input_tokens").Int()),
		IsVL:           item.Get("is_vl").Bool(),
		IsReasoning:    item.Get("is_reasoning").Bool(),
		// A missing "enable" is treated as enabled: the field is an optional
		// per-account availability marker, not a required flag.
		Enable: true,
	}
	if entry.DisplayName == "" {
		entry.DisplayName = key
	}
	if enable := item.Get("enable"); enable.Exists() && enable.IsBool() {
		entry.Enable = enable.Bool()
	}
	if entry.MaxInputTokens < 0 {
		entry.MaxInputTokens = 0
	}
	entry.Thinking = parseQoderThinkingConfig(item.Get("thinking_config"))
	return entry, true
}

// parseQoderThinkingConfig maps thinking_config onto the normalized capability.
//
// The presence of thinking_config decides whether the model supports reasoning
// control at all; is_reasoning is not a valid signal (several models report
// false while exposing an efforts table). A missing disabled block means the
// model cannot be switched off. "disabled" is always an object, so the test is
// key presence, never truthiness.
func parseQoderThinkingConfig(tc ojson.Result) *QoderThinkingConfig {
	if !tc.Exists() || !tc.IsObject() {
		return nil
	}
	config := &QoderThinkingConfig{}
	if tc.Get("disabled").Exists() {
		config.ZeroAllowed = true
	}
	efforts := tc.Get("enabled").Get("efforts")
	if efforts.Exists() && efforts.IsObject() {
		levels := make([]string, 0, len(efforts.Map()))
		for level := range efforts.Map() {
			trimmed := strings.ToLower(strings.TrimSpace(level))
			if trimmed == "" {
				continue
			}
			levels = append(levels, trimmed)
		}
		sort.Strings(levels)
		config.Levels = slices.Compact(levels)
	}
	if len(config.Levels) == 0 {
		// enabled exists but carries no efforts table: see qoderNoEffortLevels.
		config.Levels = slices.Clone(qoderNoEffortLevels)
	}
	return config
}

// QoderDefaultMaxInputTokens returns the context fallback used when the catalog
// omits max_input_tokens.
func QoderDefaultMaxInputTokens() int {
	return qoderDefaultMaxInputTokens
}

// NormalizeQoderModelKey lowercases and trims a catalog model key so per-client
// registrations compare stably.
func NormalizeQoderModelKey(modelID string) string {
	trimmed := strings.ToLower(strings.TrimSpace(modelID))
	if len(trimmed) > qoderMaxModelKeyBytes {
		return ""
	}
	return trimmed
}

// BuildPersistedQoderCatalog serializes a catalog for storage in auth metadata.
// The endpoint binds the snapshot to the gateway it was fetched from so a realm
// or host change cannot seed a stale catalog.
func BuildPersistedQoderCatalog(endpoint string, catalog *QoderCatalog) map[string]any {
	if catalog == nil {
		return nil
	}
	entries := make([]any, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		item := map[string]any{
			"key":          entry.Key,
			"display_name": entry.DisplayName,
			"enable":       entry.Enable,
		}
		if entry.MaxInputTokens > 0 {
			item["max_input_tokens"] = entry.MaxInputTokens
		}
		if entry.IsVL {
			item["is_vl"] = true
		}
		if entry.IsReasoning {
			item["is_reasoning"] = true
		}
		if entry.Thinking != nil {
			thinking := map[string]any{
				"zero_allowed": entry.Thinking.ZeroAllowed,
			}
			levels := make([]any, 0, len(entry.Thinking.Levels))
			for _, level := range entry.Thinking.Levels {
				levels = append(levels, level)
			}
			thinking["levels"] = levels
			item["thinking"] = thinking
		}
		entries = append(entries, item)
	}
	value := map[string]any{"models": entries}
	if endpoint = sanitizeQoderPersistedEndpoint(endpoint); endpoint != "" {
		value["endpoint"] = endpoint
	}
	return value
}

// ParsePersistedQoderCatalog extracts a catalog snapshot from auth metadata.
// A snapshot recorded for a different endpoint is rejected.
func ParsePersistedQoderCatalog(metadata map[string]any, expectedEndpoint string) (*QoderCatalog, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, ok := metadata[QoderPersistedCatalogMetaKey]
	if !ok || raw == nil {
		return nil, false
	}
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	persistedEndpoint, _ := rawMap["endpoint"].(string)
	if sanitizeQoderPersistedEndpoint(persistedEndpoint) != sanitizeQoderPersistedEndpoint(expectedEndpoint) {
		return nil, false
	}
	rawModels, ok := rawMap["models"]
	if !ok || rawModels == nil {
		return nil, false
	}
	items, ok := anySlice(rawModels)
	if !ok || len(items) > qoderMaxCatalogEntries {
		return nil, false
	}
	entries := make([]QoderModelEntry, 0, len(items))
	for _, item := range items {
		entryMap, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		entry, ok := persistedQoderEntry(entryMap)
		if !ok {
			continue
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Key < entries[j].Key })
	return &QoderCatalog{Entries: entries}, true
}

func persistedQoderEntry(item map[string]any) (QoderModelEntry, bool) {
	key, _ := item["key"].(string)
	key = strings.TrimSpace(key)
	if key == "" {
		return QoderModelEntry{}, false
	}
	entry := QoderModelEntry{
		Key:         key,
		DisplayName: strings.TrimSpace(anyString(item["display_name"])),
		IsVL:        anyBool(item["is_vl"]),
		IsReasoning: anyBool(item["is_reasoning"]),
		Enable:      true,
	}
	if raw, ok := item["enable"]; ok {
		entry.Enable = anyBool(raw)
	}
	if entry.DisplayName == "" {
		entry.DisplayName = key
	}
	switch tokens := item["max_input_tokens"].(type) {
	case int:
		entry.MaxInputTokens = tokens
	case int64:
		entry.MaxInputTokens = int(tokens)
	case float64:
		entry.MaxInputTokens = int(tokens)
	}
	if entry.MaxInputTokens < 0 {
		entry.MaxInputTokens = 0
	}
	if thinking, ok := item["thinking"].(map[string]any); ok {
		config := &QoderThinkingConfig{ZeroAllowed: anyBool(thinking["zero_allowed"])}
		if levels, ok := anySlice(thinking["levels"]); ok {
			for _, level := range levels {
				trimmed := strings.ToLower(strings.TrimSpace(anyString(level)))
				if trimmed != "" {
					config.Levels = append(config.Levels, trimmed)
				}
			}
		}
		sort.Strings(config.Levels)
		config.Levels = slices.Compact(config.Levels)
		if len(config.Levels) == 0 {
			config.Levels = slices.Clone(qoderNoEffortLevels)
		}
		entry.Thinking = config
	}
	return entry, true
}

func anySlice(raw any) ([]any, bool) {
	switch items := raw.(type) {
	case []any:
		return items, true
	case []string:
		out := make([]any, len(items))
		for i, item := range items {
			out[i] = item
		}
		return out, true
	default:
		return nil, false
	}
}

func anyString(raw any) string {
	value, _ := raw.(string)
	return value
}

func anyBool(raw any) bool {
	value, _ := raw.(bool)
	return value
}

func sanitizeQoderPersistedEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return ""
	}
	if parsed, errParse := url.Parse(trimmed); errParse == nil && parsed.User != nil {
		parsed.User = nil
		return strings.TrimRight(parsed.String(), "/")
	}
	return trimmed
}
