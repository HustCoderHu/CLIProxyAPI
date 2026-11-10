package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// qoderCatalogFixture mirrors the upstream {"chat": [...]} shape with the
// entry variety observed in qoder_catalog_cn.json / qoder_catalog_intl.json.
const qoderCatalogFixture = `{
  "chat": [
    {
      "key": "auto",
      "display_name": "Auto",
      "enable": true,
      "is_vl": true,
      "is_reasoning": false,
      "max_input_tokens": 200000
    },
    {
      "key": "qmodel_38max",
      "display_name": "Qwen3.8-Max",
      "enable": true,
      "is_vl": true,
      "is_reasoning": true,
      "max_input_tokens": 180000,
      "thinking_config": {
        "disabled": {"description": "Disable thinking"},
        "enabled": {
          "description": "Enable thinking",
          "efforts": {"xhigh": {}, "high": {}, "low": {}, "medium": {}},
          "is_default": true
        }
      }
    },
    {
      "key": "qmodel",
      "display_name": "Qwen3.7-Plus",
      "enable": true,
      "is_vl": true,
      "is_reasoning": false,
      "max_input_tokens": 1000000,
      "thinking_config": {
        "disabled": {},
        "enabled": {"description": "Enable thinking", "is_default": true}
      }
    },
    {
      "key": "kmodel",
      "display_name": "Kimi-K2.8-Preview",
      "enable": true,
      "is_vl": true,
      "is_reasoning": false,
      "thinking_config": {
        "enabled": {
          "efforts": {"high": {}, "low": {}, "max": {}}
        }
      }
    },
    {
      "key": "mmodel",
      "display_name": "MiniMax-M2.7",
      "enable": true,
      "is_vl": false,
      "is_reasoning": false,
      "max_input_tokens": 180000
    },
    {
      "key": "disabled-model",
      "display_name": "Disabled",
      "enable": false,
      "is_vl": true,
      "is_reasoning": true,
      "max_input_tokens": 1
    },
    {
      "display_name": "no-key-entry",
      "enable": true
    },
    {
      "key": "no-enable-field",
      "display_name": "No Enable Field"
    },
    {
      "key": "empty-disable-value",
      "display_name": "Empty Disable Value",
      "enable": true,
      "thinking_config": {
        "disabled": null,
        "enabled": {"efforts": {"medium": {}}}
      }
    }
  ]
}`

func TestQoderParseModelListFiltersDisabledAndMapsThinking(t *testing.T) {
	catalog, ok := ParseQoderModelList([]byte(qoderCatalogFixture))
	if !ok {
		t.Fatalf("ParseQoderModelList returned ok=false for a valid payload")
	}
	byKey := make(map[string]QoderModelEntry, len(catalog.Entries))
	order := make([]string, 0, len(catalog.Entries))
	for _, entry := range catalog.Entries {
		byKey[entry.Key] = entry
		order = append(order, entry.Key)
	}

	if _, exists := byKey["disabled-model"]; exists {
		t.Fatalf("enable=false entry must be filtered out: %v", order)
	}
	if _, exists := byKey["no-key-entry"]; exists {
		t.Fatalf("entry without a key must be skipped: %v", order)
	}
	if !slices.IsSorted(order) {
		t.Fatalf("entries must be sorted by key, got %v", order)
	}
	want := []string{"auto", "empty-disable-value", "kmodel", "mmodel", "no-enable-field", "qmodel", "qmodel_38max"}
	if !slices.Equal(order, want) {
		t.Fatalf("unexpected catalog keys:\n got %v\nwant %v", order, want)
	}

	if entry := byKey["no-enable-field"]; !entry.Enable {
		t.Fatalf("missing enable must default to true")
	}
	if entry := byKey["auto"]; entry.Thinking != nil {
		t.Fatalf("entry without thinking_config must have nil Thinking, got %+v", entry.Thinking)
	}
	if entry := byKey["auto"]; entry.DisplayName != "Auto" || !entry.IsVL || entry.MaxInputTokens != 200000 {
		t.Fatalf("auto entry decoded incorrectly: %+v", entry)
	}

	entry := byKey["qmodel_38max"]
	if entry.Thinking == nil {
		t.Fatalf("qmodel_38max must expose thinking support")
	}
	if !slices.Equal(entry.Thinking.Levels, []string{"high", "low", "medium", "xhigh"}) {
		t.Fatalf("levels must be sorted ascending, got %v", entry.Thinking.Levels)
	}
	if !entry.Thinking.ZeroAllowed {
		t.Fatalf("qmodel_38max has a disabled block, want ZeroAllowed")
	}

	// qmodel: enabled exists without an efforts table, disabled is present.
	// The documented expression advertises only "high" plus zero_allowed.
	entry = byKey["qmodel"]
	if entry.Thinking == nil {
		t.Fatalf("qmodel must expose thinking support")
	}
	if !slices.Equal(entry.Thinking.Levels, []string{"high"}) {
		t.Fatalf("qmodel without efforts must advertise [\"high\"], got %v", entry.Thinking.Levels)
	}
	if !entry.Thinking.ZeroAllowed {
		t.Fatalf("qmodel has a disabled block, want ZeroAllowed")
	}

	// kmodel: no disabled block, so thinking cannot be turned off.
	entry = byKey["kmodel"]
	if entry.Thinking == nil || entry.Thinking.ZeroAllowed {
		t.Fatalf("kmodel has no disabled block, want ZeroAllowed=false: %+v", entry.Thinking)
	}
	if !slices.Equal(entry.Thinking.Levels, []string{"high", "low", "max"}) {
		t.Fatalf("kmodel levels decoded incorrectly: %v", entry.Thinking.Levels)
	}

	// A null disabled value still counts as a present key: the upstream always
	// encodes it as an object, so presence is the contract.
	entry = byKey["empty-disable-value"]
	if entry.Thinking == nil || !entry.Thinking.ZeroAllowed {
		t.Fatalf("present disabled key must set ZeroAllowed even when null: %+v", entry.Thinking)
	}

	// mmodel carries no thinking_config in the real catalogs and must not gain
	// an implicit one.
	if entry := byKey["mmodel"]; entry.Thinking != nil {
		t.Fatalf("mmodel must keep Thinking nil, got %+v", entry.Thinking)
	}
}

func TestQoderParseModelListRejectsMalformedPayloads(t *testing.T) {
	cases := map[string]string{
		"invalid json":  `{"chat": [`,
		"array top":     `[]`,
		"chat missing":  `{"models": []}`,
		"chat not list": `{"chat": {"key": "x"}}`,
		"chat null":     `{"chat": null}`,
		"empty":         ``,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			if _, ok := ParseQoderModelList([]byte(payload)); ok {
				t.Fatalf("payload %q must be rejected", payload)
			}
		})
	}
}

func TestQoderParseModelListAcceptsEmptyCatalog(t *testing.T) {
	catalog, ok := ParseQoderModelList([]byte(`{"chat": []}`))
	if !ok {
		t.Fatalf("empty catalog must parse successfully")
	}
	if catalog == nil || len(catalog.Entries) != 0 {
		t.Fatalf("empty catalog must produce zero entries, got %+v", catalog)
	}
}

// TestQoderParseModelListMergesAppScene covers the chat+app union: Qoder may
// launch a model in the desktop "app" scene before it reaches "chat", so both
// scenes must be published with chat winning on duplicate keys.
func TestQoderParseModelListMergesAppScene(t *testing.T) {
	t.Run("union keeps stable key order", func(t *testing.T) {
		payload := `{
		  "chat": [{"key": "a", "display_name": "A", "enable": true}],
		  "app": [{"key": "b", "display_name": "B", "enable": true}]
		}`
		catalog, ok := ParseQoderModelList([]byte(payload))
		if !ok {
			t.Fatalf("chat+app payload must parse")
		}
		keys := make([]string, 0, len(catalog.Entries))
		for _, entry := range catalog.Entries {
			keys = append(keys, entry.Key)
		}
		if !slices.Equal(keys, []string{"a", "b"}) {
			t.Fatalf("unexpected merged keys: %v", keys)
		}
	})

	t.Run("chat wins on duplicate key", func(t *testing.T) {
		payload := `{
		  "chat": [{"key": "shared", "display_name": "Chat Authority", "enable": true}],
		  "app": [{"key": "shared", "display_name": "App Name", "enable": false}]
		}`
		catalog, ok := ParseQoderModelList([]byte(payload))
		if !ok {
			t.Fatalf("duplicate key payload must parse")
		}
		if len(catalog.Entries) != 1 {
			t.Fatalf("duplicate keys must collapse to one entry, got %+v", catalog.Entries)
		}
		entry := catalog.Entries[0]
		if entry.Key != "shared" || entry.DisplayName != "Chat Authority" || !entry.Enable {
			t.Fatalf("chat entry must be authoritative, got %+v", entry)
		}
	})

	t.Run("app-only payload parses", func(t *testing.T) {
		payload := `{
		  "app": [{
		    "key": "lmodel",
		    "display_name": "QoDuck",
		    "enable": true,
		    "is_vl": true,
		    "is_reasoning": true,
		    "max_input_tokens": 180000
		  }]
		}`
		catalog, ok := ParseQoderModelList([]byte(payload))
		if !ok {
			t.Fatalf("app-only payload must parse")
		}
		if len(catalog.Entries) != 1 {
			t.Fatalf("app-only payload must yield one entry, got %+v", catalog.Entries)
		}
		entry := catalog.Entries[0]
		if entry.Key != "lmodel" || entry.DisplayName != "QoDuck" || !entry.IsVL || !entry.IsReasoning {
			t.Fatalf("app entry decoded incorrectly: %+v", entry)
		}
	})

	t.Run("unrelated scenes ignored", func(t *testing.T) {
		payload := `{
		  "chat": [{"key": "a", "display_name": "A", "enable": true}],
		  "app": [],
		  "inline": [{"key": "inline-only", "display_name": "Inline", "enable": true}],
		  "quest": [{"key": "quest-only", "display_name": "Quest", "enable": true}]
		}`
		catalog, ok := ParseQoderModelList([]byte(payload))
		if !ok {
			t.Fatalf("payload with ignored scenes must parse")
		}
		keys := make([]string, 0, len(catalog.Entries))
		for _, entry := range catalog.Entries {
			keys = append(keys, entry.Key)
		}
		if !slices.Equal(keys, []string{"a"}) {
			t.Fatalf("only conversational scenes may be published, got %v", keys)
		}
	})
}

func TestQoderPersistedCatalogRoundTrip(t *testing.T) {
	catalog, ok := ParseQoderModelList([]byte(qoderCatalogFixture))
	if !ok {
		t.Fatalf("fixture must parse")
	}
	value := BuildPersistedQoderCatalog("https://gateway.qoder.com.cn/", catalog)
	if value == nil {
		t.Fatalf("BuildPersistedQoderCatalog returned nil")
	}
	metadata := map[string]any{QoderPersistedCatalogMetaKey: value}

	restored, ok := ParsePersistedQoderCatalog(metadata, "https://gateway.qoder.com.cn")
	if !ok {
		t.Fatalf("ParsePersistedQoderCatalog rejected its own snapshot")
	}
	if len(restored.Entries) != len(catalog.Entries) {
		t.Fatalf("entry count mismatch: got %d want %d", len(restored.Entries), len(catalog.Entries))
	}
	for i := range catalog.Entries {
		want, got := catalog.Entries[i], restored.Entries[i]
		if want.Key != got.Key || want.DisplayName != got.DisplayName || want.MaxInputTokens != got.MaxInputTokens ||
			want.IsVL != got.IsVL || want.IsReasoning != got.IsReasoning {
			t.Fatalf("entry %d mismatch:\n got %+v\nwant %+v", i, got, want)
		}
		if (want.Thinking == nil) != (got.Thinking == nil) {
			t.Fatalf("entry %s thinking presence mismatch", want.Key)
		}
		if want.Thinking != nil {
			if !slices.Equal(want.Thinking.Levels, got.Thinking.Levels) || want.Thinking.ZeroAllowed != got.Thinking.ZeroAllowed {
				t.Fatalf("entry %s thinking mismatch:\n got %+v\nwant %+v", want.Key, got.Thinking, want.Thinking)
			}
		}
	}

	if _, ok := ParsePersistedQoderCatalog(metadata, "https://api3.qoder.sh"); ok {
		t.Fatalf("snapshot must be rejected for a different endpoint")
	}
}

func TestQoderPersistedCatalogRejectsWrongShape(t *testing.T) {
	if _, ok := ParsePersistedQoderCatalog(nil, ""); ok {
		t.Fatalf("nil metadata must be rejected")
	}
	if _, ok := ParsePersistedQoderCatalog(map[string]any{QoderPersistedCatalogMetaKey: "nope"}, ""); ok {
		t.Fatalf("non-map snapshot must be rejected")
	}
	if _, ok := ParsePersistedQoderCatalog(map[string]any{QoderPersistedCatalogMetaKey: map[string]any{"endpoint": ""}}, ""); ok {
		t.Fatalf("snapshot without models must be rejected")
	}
}

// qoderCatalogServer serves a fixed catalog and records the request shape.
type qoderCatalogServer struct {
	*httptest.Server
	status  int
	body    string
	lastReq *http.Request
	calls   int
}

func newQoderCatalogServer(t *testing.T, status int, body string) *qoderCatalogServer {
	t.Helper()
	srv := &qoderCatalogServer{status: status, body: body}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		srv.calls++
		srv.lastReq = r.Clone(context.Background())
		w.WriteHeader(srv.status)
		_, _ = w.Write([]byte(srv.body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestQoderFetchModelsSignsAndParses(t *testing.T) {
	srv := newQoderCatalogServer(t, http.StatusOK, qoderCatalogFixture)
	catalog, status := FetchQoderModels(context.Background(), QoderModelsRequest{
		BaseURLs:         []string{srv.URL + "/"},
		UID:              "uid-fixture-001",
		Nickname:         "alice",
		AccessToken:      "drt-fixture-access-token",
		RefreshToken:     "drt-fixture-refresh-token",
		UserType:         "personal_professional_trial",
		OrganizationID:   "1234567890123456789",
		OrganizationName: "Acme",
	})
	if status != QoderProbeStatusSuccess {
		t.Fatalf("want success, got status %v", status)
	}
	if catalog == nil || len(catalog.Entries) == 0 {
		t.Fatalf("catalog is empty: %+v", catalog)
	}
	req := srv.lastReq
	if req == nil {
		t.Fatalf("upstream was not called")
	}
	if req.Method != http.MethodGet {
		t.Fatalf("model list must be GET, got %s", req.Method)
	}
	if req.URL.Path != "/algo/api/v2/model/list" {
		t.Fatalf("unexpected path %q", req.URL.Path)
	}
	if req.URL.RawQuery != "Encode=1" {
		t.Fatalf("unexpected query %q", req.URL.RawQuery)
	}
	for _, header := range []string{"authorization", "cosy-key", "cosy-date", "cosy-user", "cosy-version"} {
		if strings.TrimSpace(req.Header.Get(header)) == "" {
			t.Fatalf("missing COSY header %q", header)
		}
	}
	if got := req.Header.Get("accept"); got != "application/json" {
		t.Fatalf("non-SSE probe must ask for application/json, got %q", got)
	}
	if got := req.Header.Get("cosy-machinehostname"); got != "" {
		t.Fatalf("non-SSE probe must not send cosy-machinehostname, got %q", got)
	}
	if got := req.Header.Get("x-model-key"); got != "" {
		t.Fatalf("catalog probe must not send x-model-key, got %q", got)
	}
	if got := req.Header.Get("cosy-user"); got != "uid-fixture-001" {
		t.Fatalf("cosy-user must carry the uid, got %q", got)
	}
	if !strings.HasPrefix(req.Header.Get("authorization"), "Bearer COSY.") {
		t.Fatalf("authorization must be a COSY bearer, got %q", req.Header.Get("authorization"))
	}
}

func TestQoderFetchModelsClassifiesStatus(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   QoderProbeStatus
	}{
		{name: "unauthorized", status: http.StatusUnauthorized, want: QoderProbeStatusAuthError},
		{name: "forbidden", status: http.StatusForbidden, want: QoderProbeStatusAuthError},
		{name: "server error", status: http.StatusInternalServerError, want: QoderProbeStatusTransientError},
		{name: "bad request", status: http.StatusBadRequest, want: QoderProbeStatusTransientError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newQoderCatalogServer(t, tc.status, `{}`)
			if _, status := FetchQoderModels(context.Background(), QoderModelsRequest{
				BaseURLs:    []string{srv.URL},
				UID:         "uid",
				AccessToken: "token",
			}); status != tc.want {
				t.Fatalf("status %d classified as %v, want %v", tc.status, status, tc.want)
			}
		})
	}
}

func TestQoderFetchModelsRequiresAccessToken(t *testing.T) {
	if _, status := FetchQoderModels(context.Background(), QoderModelsRequest{
		BaseURLs: []string{"https://gateway.qoder.com.cn"},
		UID:      "uid",
	}); status != QoderProbeStatusAuthError {
		t.Fatalf("missing token must be an auth error, got %v", status)
	}
}

func TestQoderFetchModelsFailsOverBetweenHosts(t *testing.T) {
	bad := newQoderCatalogServer(t, http.StatusBadGateway, `{}`)
	good := newQoderCatalogServer(t, http.StatusOK, `{"chat":[{"key":"qmodel","enable":true}]}`)
	catalog, status := FetchQoderModels(context.Background(), QoderModelsRequest{
		BaseURLs:    []string{bad.URL, good.URL},
		UID:         "uid",
		AccessToken: "token",
	})
	if status != QoderProbeStatusSuccess || catalog == nil || len(catalog.Entries) != 1 {
		t.Fatalf("must fail over to the second host, got status %v catalog %+v", status, catalog)
	}
	if bad.calls == 0 || good.calls == 0 {
		t.Fatalf("both hosts must be attempted, got bad=%d good=%d", bad.calls, good.calls)
	}
	if got := catalog.Entries[0].Key; got != "qmodel" {
		t.Fatalf("unexpected model key %q", got)
	}
}

func TestQoderFetchModelsStopsOnAuthError(t *testing.T) {
	denied := newQoderCatalogServer(t, http.StatusForbidden, `{}`)
	other := newQoderCatalogServer(t, http.StatusOK, qoderCatalogFixture)
	if _, status := FetchQoderModels(context.Background(), QoderModelsRequest{
		BaseURLs:    []string{denied.URL, other.URL},
		UID:         "uid",
		AccessToken: "token",
	}); status != QoderProbeStatusAuthError {
		t.Fatalf("want auth error, got %v", status)
	}
	if other.calls != 0 {
		t.Fatalf("credential rejection must not be retried on another host")
	}
}

func TestQoderFetchModelsRejectsMalformedUpstream(t *testing.T) {
	srv := newQoderCatalogServer(t, http.StatusOK, `{"chat": "nope"}`)
	if _, status := FetchQoderModels(context.Background(), QoderModelsRequest{
		BaseURLs:    []string{srv.URL},
		UID:         "uid",
		AccessToken: "token",
	}); status == QoderProbeStatusSuccess {
		t.Fatalf("malformed payload must not be reported as success")
	}
}

// TestQoderCatalogFixtureShape locks the normalized projection against the
// thinking mapping table documented in research/03 so a refactor cannot silently
// reintroduce min/max semantics.
func TestQoderThinkingNeverSetsBudgetFields(t *testing.T) {
	catalog, ok := ParseQoderModelList([]byte(qoderCatalogFixture))
	if !ok {
		t.Fatalf("fixture must parse")
	}
	for _, entry := range catalog.Entries {
		if entry.Thinking == nil {
			continue
		}
		if len(entry.Thinking.Levels) == 0 {
			t.Fatalf("%s has thinking support but no levels", entry.Key)
		}
		encoded, errMarshal := json.Marshal(entry.Thinking)
		if errMarshal != nil {
			t.Fatalf("marshal thinking for %s: %v", entry.Key, errMarshal)
		}
		if strings.Contains(string(encoded), "Min") || strings.Contains(string(encoded), "Max") ||
			strings.Contains(string(encoded), "Dynamic") || strings.Contains(string(encoded), "DisableType") {
			t.Fatalf("%s thinking config exposes budget fields: %s", entry.Key, encoded)
		}
	}
}
