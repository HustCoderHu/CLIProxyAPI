package executor

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"

	qoderauthsvc "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/synthesizer"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// qoderPATFixture is the config API-key token used by the preparation tests.
const qoderPATFixture = "pt-fixture-personal-token"

// qoderJobTokenFixture is the job token the mocked exchange returns.
const qoderJobTokenFixture = "jt-fixture-exchanged-token"

// qoderPrepareTestAuth builds a credential with the given metadata.
func qoderPrepareTestAuth(metadata map[string]any) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "qoder-prepare-test",
		Provider: constant.QoderCN,
		Metadata: metadata,
	}
}

// qoderStubExchangeClient points the credential client at a local exchange
// endpoint. The realm openapi host is a package constant, so the factory seam is
// rewired instead.
func qoderStubExchangeClient(t *testing.T, baseURL string) {
	t.Helper()
	previous := qoderRefreshClientFactory
	qoderRefreshClientFactory = func(cfg *config.Config, realm qoderauthsvc.RealmConfig, proxyURL string) *qoderauthsvc.Client {
		realm.OpenAPIBase = baseURL
		return qoderauthsvc.NewClientWithProxyURL(cfg, realm, proxyURL)
	}
	t.Cleanup(func() { qoderRefreshClientFactory = previous })
}

// qoderExchangeServer answers the PAT exchange with a fixed token pair and
// records every request.
func qoderExchangeServer(t *testing.T, status int, body string) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var calls atomic.Int32
	captured := &atomic.Value{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		raw, _ := io.ReadAll(r.Body)
		captured.Store(struct {
			path string
			body []byte
		}{path: r.URL.Path, body: raw})
		w.Header().Set("Content-Type", "application/json")
		if status != 0 && status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
			return
		}
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)
	return server, &calls, captured
}

// qoderSuccessExchangeBody is the mocked job-token exchange response.
const qoderSuccessExchangeBody = `{"token":"jt-fixture-exchanged-token","refresh_token":"jrt-fixture-exchanged","token_type":"Bearer","expires_in":3600000}`

// TestQoderExecutorShouldPrepareRequestAuthMatrix locks the decision boundary:
// only a personal token that has not been exchanged yet is prepared, and an
// active OAuth session is never replaced by a personal token.
func TestQoderExecutorShouldPrepareRequestAuthMatrix(t *testing.T) {
	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)

	tests := []struct {
		name     string
		auth     *cliproxyauth.Auth
		expected bool
	}{
		{
			name:     "nil auth",
			auth:     nil,
			expected: false,
		},
		{
			name:     "oauth credential with personal token",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "oauth", "access_token": "dt-oauth", "personal_token": qoderPATFixture}),
			expected: false,
		},
		{
			name:     "pat placeholder written by the config synthesizer",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "pat", "access_token": qoderPATFixture, "personal_token": qoderPATFixture}),
			expected: true,
		},
		{
			name:     "pat with only a personal token",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "pat", "personal_token": qoderPATFixture}),
			expected: true,
		},
		{
			name:     "pat already exchanged with marker",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "pat", "access_token": qoderJobTokenFixture, "personal_token": qoderPATFixture, "pat_exchanged": true}),
			expected: false,
		},
		{
			name:     "pat imported through login flow",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "pat", "access_token": qoderJobTokenFixture, "personal_token": qoderPATFixture, "refresh_token": "jrt-fixture-exchanged"}),
			expected: false,
		},
		{
			name:     "pat with unproven access token",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "pat", "access_token": qoderJobTokenFixture, "personal_token": qoderPATFixture}),
			expected: true,
		},
		{
			name:     "credential without a personal token",
			auth:     qoderPrepareTestAuth(map[string]any{"auth_kind": "pat", "access_token": qoderJobTokenFixture}),
			expected: false,
		},
		{
			name:     "credential without metadata",
			auth:     &cliproxyauth.Auth{ID: "qoder-empty"},
			expected: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := e.ShouldPrepareRequestAuth(test.auth); got != test.expected {
				t.Fatalf("ShouldPrepareRequestAuth() = %v, want %v", got, test.expected)
			}
		})
	}
}

// TestQoderExecutorPrepareRequestAuthExchangesPAT covers the successful
// exchange: the job token replaces the placeholder, the identity is preserved
// and the marker makes the next preparation a no-op.
func TestQoderExecutorPrepareRequestAuthExchangesPAT(t *testing.T) {
	server, calls, captured := qoderExchangeServer(t, http.StatusOK, qoderSuccessExchangeBody)
	qoderStubExchangeClient(t, server.URL)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	auth := qoderPrepareTestAuth(map[string]any{
		"auth_kind":         "pat",
		"access_token":      qoderPATFixture,
		"personal_token":    qoderPATFixture,
		"uid":               "uid-fixture-001",
		"user_type":         "personal_professional_trial",
		"organization_id":   "1234567890123456789",
		"organization_name": "Acme",
		"token_type":        "Bearer",
	})

	if !e.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for the config-synthesized placeholder")
	}
	prepared, errPrepare := e.PrepareRequestAuth(context.Background(), auth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared == nil {
		t.Fatal("PrepareRequestAuth() returned nil auth")
	}
	if calls.Load() != 1 {
		t.Fatalf("exchange calls = %d, want 1", calls.Load())
	}

	record := captured.Load().(struct {
		path string
		body []byte
	})
	if record.path != "/api/v1/jobToken/exchange" {
		t.Fatalf("exchange path = %q, want /api/v1/jobToken/exchange", record.path)
	}
	if got := ojson.GetBytesNoCopy(record.body, "personal_token").String(); got != qoderPATFixture {
		t.Fatalf("exchange body personal_token = %q, want the configured PAT", got)
	}

	if got := prepared.Metadata["access_token"]; got != qoderJobTokenFixture {
		t.Fatalf("access_token = %v, want the exchanged job token", got)
	}
	if got := prepared.Metadata["personal_token"]; got != qoderPATFixture {
		t.Fatalf("personal_token = %v, want the configured PAT preserved", got)
	}
	if got := prepared.Metadata["refresh_token"]; got != "jrt-fixture-exchanged" {
		t.Fatalf("refresh_token = %v, want the exchanged refresh token", got)
	}
	if got := prepared.Metadata["expires_in"]; got != 3600 {
		t.Fatalf("expires_in = %v, want 3600 seconds (upstream reports milliseconds)", got)
	}
	if got := prepared.Metadata["token_type"]; got != "Bearer" {
		t.Fatalf("token_type = %v, want Bearer", got)
	}
	if _, ok := prepared.Metadata["expired"].(string); !ok {
		t.Fatalf("expired = %v, want an RFC3339 timestamp", prepared.Metadata["expired"])
	}
	if got := prepared.Metadata[qoderPATExtendedMarker]; got != true {
		t.Fatalf("pat_exchanged = %v, want true", got)
	}
	if got := prepared.Metadata["uid"]; got != "uid-fixture-001" {
		t.Fatalf("uid = %v, want the identity preserved", got)
	}
	if got := prepared.Metadata["user_type"]; got != "personal_professional_trial" {
		t.Fatalf("user_type = %v, want the identity preserved", got)
	}
	if got := prepared.Metadata["organization_id"]; got != "1234567890123456789" {
		t.Fatalf("organization_id = %v, want the identity preserved", got)
	}

	// The original credential must stay untouched: the manager persists the
	// returned clone, never a mutation of the shared snapshot.
	if got := auth.Metadata["access_token"]; got != qoderPATFixture {
		t.Fatalf("input auth access_token mutated to %v", got)
	}

	// The exchanged credential carries a usable COSY bearer.
	uid, token, errCreds := qoderCredentials(prepared)
	if errCreds != nil {
		t.Fatalf("qoderCredentials(prepared) error = %v", errCreds)
	}
	if token != qoderJobTokenFixture {
		t.Fatalf("COSY access token = %q, want the exchanged job token", token)
	}
	if uid != "uid-fixture-001" {
		t.Fatalf("COSY uid = %q", uid)
	}

	if e.ShouldPrepareRequestAuth(prepared) {
		t.Fatal("ShouldPrepareRequestAuth() = true after a successful exchange")
	}
}

// TestQoderExecutorPrepareRequestAuthSkipsWhenNotNeeded locks the no-op path:
// a credential the executor would not prepare is returned as nil, no exchange.
func TestQoderExecutorPrepareRequestAuthSkipsWhenNotNeeded(t *testing.T) {
	server, calls, _ := qoderExchangeServer(t, http.StatusOK, qoderSuccessExchangeBody)
	qoderStubExchangeClient(t, server.URL)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	oauth := qoderPrepareTestAuth(map[string]any{"auth_kind": "oauth", "access_token": "dt-oauth", "personal_token": qoderPATFixture})
	prepared, errPrepare := e.PrepareRequestAuth(context.Background(), oauth)
	if errPrepare != nil {
		t.Fatalf("PrepareRequestAuth() error = %v", errPrepare)
	}
	if prepared != nil {
		t.Fatalf("PrepareRequestAuth() = %#v, want nil for an OAuth credential", prepared)
	}
	if calls.Load() != 0 {
		t.Fatalf("exchange calls = %d, want 0", calls.Load())
	}
}

// TestQoderExecutorPrepareRequestAuthRejectsInvalidPAT locks the local guard: a
// configured api-key that is not a personal access token can never be exchanged,
// so it is rejected as a credential fault without any upstream call.
func TestQoderExecutorPrepareRequestAuthRejectsInvalidPAT(t *testing.T) {
	server, calls, _ := qoderExchangeServer(t, http.StatusOK, qoderSuccessExchangeBody)
	qoderStubExchangeClient(t, server.URL)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	auth := qoderPrepareTestAuth(map[string]any{
		"auth_kind":      "pat",
		"access_token":   "not-a-personal-token",
		"personal_token": "not-a-personal-token",
	})
	if !e.ShouldPrepareRequestAuth(auth) {
		t.Fatal("ShouldPrepareRequestAuth() = false for an unexchanged non-PAT value")
	}
	prepared, errPrepare := e.PrepareRequestAuth(context.Background(), auth)
	if errPrepare == nil {
		t.Fatal("PrepareRequestAuth() error = nil, want a rejection")
	}
	if prepared != nil {
		t.Fatalf("PrepareRequestAuth() auth = %#v, want nil", prepared)
	}
	if calls.Load() != 0 {
		t.Fatalf("exchange calls = %d, want 0 (a non-PAT value must never be sent upstream)", calls.Load())
	}
	var statusError interface{ StatusCode() int }
	if !errors.As(errPrepare, &statusError) || statusError.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("error %v does not map to 401", errPrepare)
	}
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(errPrepare, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("error %v is not credential-scoped", errPrepare)
	}
}

// TestQoderExecutorPrepareRequestAuthFailureMapping locks the error contract the
// conductor relies on: a rejected PAT cools the credential, an upstream failure
// is reported as a gateway error.
func TestQoderExecutorPrepareRequestAuthFailureMapping(t *testing.T) {
	tests := []struct {
		name             string
		status           int
		body             string
		wantStatus       int
		wantCredentialSc bool
	}{
		{
			name:             "unauthorized is credential scoped",
			status:           http.StatusUnauthorized,
			body:             `{"message":"invalid personal token"}`,
			wantStatus:       http.StatusUnauthorized,
			wantCredentialSc: true,
		},
		{
			name:             "forbidden is credential scoped",
			status:           http.StatusForbidden,
			body:             `{"code":"112","message":"quota exhausted"}`,
			wantStatus:       http.StatusForbidden,
			wantCredentialSc: true,
		},
		{
			name:       "server error becomes a gateway error",
			status:     http.StatusInternalServerError,
			body:       `{"message":"boom"}`,
			wantStatus: http.StatusBadGateway,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, calls, _ := qoderExchangeServer(t, test.status, test.body)
			qoderStubExchangeClient(t, server.URL)

			e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
			auth := qoderPrepareTestAuth(map[string]any{
				"auth_kind":      "pat",
				"access_token":   qoderPATFixture,
				"personal_token": qoderPATFixture,
			})
			prepared, errPrepare := e.PrepareRequestAuth(context.Background(), auth)
			if errPrepare == nil {
				t.Fatal("PrepareRequestAuth() error = nil, want a failure")
			}
			if prepared != nil {
				t.Fatalf("PrepareRequestAuth() auth = %#v, want nil on failure", prepared)
			}
			if calls.Load() != 1 {
				t.Fatalf("exchange calls = %d, want 1", calls.Load())
			}
			var statusError interface{ StatusCode() int }
			if !errors.As(errPrepare, &statusError) || statusError.StatusCode() != test.wantStatus {
				t.Fatalf("error %v does not map to %d", errPrepare, test.wantStatus)
			}
			var scoped interface{ IsCredentialScoped() bool }
			isScoped := errors.As(errPrepare, &scoped) && scoped.IsCredentialScoped()
			if isScoped != test.wantCredentialSc {
				t.Fatalf("credential-scoped = %v, want %v (error: %v)", isScoped, test.wantCredentialSc, errPrepare)
			}
			if strings.Contains(errPrepare.Error(), qoderPATFixture) {
				t.Fatalf("error message leaks the personal token: %v", errPrepare)
			}
		})
	}
}

// TestQoderExecutorConfigPATCredentialExchangesOnce is the end-to-end proof: a
// config-synthesized PAT credential reaches the exchange endpoint exactly once,
// then signs inference requests with the exchanged job token across a transient
// retry and a second request. Only the manager persistence can make the second
// request skip the exchange.
func TestQoderExecutorConfigPATCredentialExchangesOnce(t *testing.T) {
	qoderStubRetrySeams(t)

	exchangeServer, exchanges, exchangeCaptured := qoderExchangeServer(t, http.StatusOK, qoderSuccessExchangeBody)
	qoderStubExchangeClient(t, exchangeServer.URL)

	inferServer, inferCalls := qoderStartScriptedServer(t, []qoderScriptedAnswer{
		{status: http.StatusInternalServerError},
		qoderSuccessAnswer("first"),
		qoderSuccessAnswer("second"),
	})

	cfg := &config.Config{}
	auths, errSynthesize := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config: &config.Config{
			QoderKey: []config.QoderKey{{APIKey: qoderPATFixture, BaseURL: inferServer.URL}},
		},
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("synthesized auth count = %d, want 1", len(auths))
	}
	auth := auths[0]
	auth.Status = cliproxyauth.StatusActive

	manager := cliproxyauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(NewQoderExecutor(cfg, constant.QoderCN))
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "qfmodel"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })

	request := qoderTestRequest()
	opts := qoderTestOptions()
	opts.Stream = true

	stream, errStream := manager.ExecuteStream(context.Background(), []string{constant.QoderCN}, request, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	payloads, errDrain := qoderDrain(t, stream)
	if errDrain != nil {
		t.Fatalf("stream error = %v", errDrain)
	}
	if !strings.Contains(strings.Join(payloads, ""), "first") {
		t.Fatalf("stream did not carry the retried response: %v", payloads)
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchange calls = %d, want exactly 1", exchanges.Load())
	}
	if got := len(inferCalls.snapshot()); got != 2 {
		t.Fatalf("inference calls = %d, want 2 (initial 500 + transient retry)", got)
	}

	// Every inference attempt must carry a valid COSY signature over the bytes
	// that went on the wire.
	for _, record := range inferCalls.snapshot() {
		qoderAssertCosySignature(t, record)
	}

	// The manager must have persisted the exchange result.
	stored, ok := manager.GetByID(auth.ID)
	if !ok || stored == nil {
		t.Fatal("prepared credential disappeared from the manager")
	}
	if got := stored.Metadata["access_token"]; got != qoderJobTokenFixture {
		t.Fatalf("stored access_token = %v, want the exchanged job token", got)
	}
	if got := stored.Metadata["personal_token"]; got != qoderPATFixture {
		t.Fatalf("stored personal_token = %v, want the configured PAT preserved", got)
	}
	if got := stored.Metadata[qoderPATExtendedMarker]; got != true {
		t.Fatalf("stored pat_exchanged = %v, want true", got)
	}

	// A second request must reuse the stored exchange: the manager persists the
	// prepared credential, so preparation is not repeated.
	second, errSecond := manager.ExecuteStream(context.Background(), []string{constant.QoderCN}, request, opts)
	if errSecond != nil {
		t.Fatalf("second ExecuteStream() error = %v", errSecond)
	}
	secondPayloads, errSecondDrain := qoderDrain(t, second)
	if errSecondDrain != nil {
		t.Fatalf("second stream error = %v", errSecondDrain)
	}
	if !strings.Contains(strings.Join(secondPayloads, ""), "second") {
		t.Fatalf("second stream did not carry the response: %v", secondPayloads)
	}
	if exchanges.Load() != 1 {
		t.Fatalf("exchange calls after the second request = %d, want 1", exchanges.Load())
	}

	record := exchangeCaptured.Load().(struct {
		path string
		body []byte
	})
	if got := ojson.GetBytesNoCopy(record.body, "personal_token").String(); got != qoderPATFixture {
		t.Fatalf("exchange body personal_token = %q, want the configured PAT", got)
	}
}
