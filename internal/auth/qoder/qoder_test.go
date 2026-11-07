package qoder

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// parseResult decodes a fixture body the same way the client does.
func parseResult(t *testing.T, raw []byte) ojson.Result {
	t.Helper()
	return ojson.ParseBytes(raw)
}

// newTestClient binds a client to a test server by rewriting the realm base URL.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	realm := RealmCN
	realm.OpenAPIBase = server.URL
	realm.WebsiteBase = server.URL
	client := newClient(&http.Client{Timeout: 10 * time.Second}, realm)
	return client, server
}

func TestRefreshRoute(t *testing.T) {
	tests := []struct {
		name          string
		refreshToken  string
		personalToken string
		want          RefreshKind
	}{
		{name: "device token", refreshToken: "drt-abc", want: RefreshDevice},
		{name: "device token wins over PAT", refreshToken: "drt-abc", personalToken: "pt-xyz", want: RefreshDevice},
		{name: "job token", refreshToken: "jrt-abc", want: RefreshJob},
		{name: "unknown prefix falls back to job", refreshToken: "legacy-token", want: RefreshJob},
		{name: "pat only", personalToken: "pt-xyz", want: RefreshPAT},
		{name: "nothing", want: RefreshNone},
		{name: "whitespace only", refreshToken: "  ", personalToken: "  ", want: RefreshNone},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := RefreshRoute(test.refreshToken, test.personalToken); got != test.want {
				t.Fatalf("RefreshRoute(%q, %q) = %v, want %v", test.refreshToken, test.personalToken, got, test.want)
			}
		})
	}
}

// TestParseTokenDocumentExpiresInMilliseconds is the single most important
// conversion in the package: the upstream reports milliseconds, and a missed
// division puts expiry ~80 years out so the refresh loop never fires.
func TestParseTokenDocumentExpiresInMilliseconds(t *testing.T) {
	raw := []byte(`{"token":"dt-1","refresh_token":"drt-2","token_type":"Bearer","expires_in":3600000}`)
	data := parseTokenDocument(parseResult(t, raw), "dt-1")
	if data.ExpiresIn != 3600 {
		t.Fatalf("ExpiresIn = %d, want 3600 seconds", data.ExpiresIn)
	}
	if data.TokenType != "Bearer" {
		t.Fatalf("TokenType = %q", data.TokenType)
	}
	if data.RefreshToken != "drt-2" {
		t.Fatalf("RefreshToken = %q", data.RefreshToken)
	}
	remaining := time.Until(data.ExpiresAt)
	if remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("ExpiresAt = %v (in %v), want about one hour", data.ExpiresAt, remaining)
	}
}

func TestParseTokenDocumentExpiresInSubSecondClamps(t *testing.T) {
	// The reference implementation always divides by 1000, so a sub-second value
	// collapses to zero. Clamping to one second keeps the credential usable
	// instead of reporting an already-expired token.
	raw := []byte(`{"token":"dt-1","expires_in":800}`)
	data := parseTokenDocument(parseResult(t, raw), "dt-1")
	if data.ExpiresIn != 1 {
		t.Fatalf("ExpiresIn = %d, want the clamped minimum of 1 second", data.ExpiresIn)
	}
}

func TestParseTokenDocumentDefaultsTokenType(t *testing.T) {
	raw := []byte(`{"token":"dt-1"}`)
	data := parseTokenDocument(parseResult(t, raw), "dt-1")
	if data.TokenType != "Bearer" {
		t.Fatalf("TokenType = %q, want Bearer", data.TokenType)
	}
	if !data.ExpiresAt.IsZero() {
		t.Fatalf("ExpiresAt = %v, want zero when no expiry is reported", data.ExpiresAt)
	}
}

func TestDeviceFlowPendingOnNotFoundAndAccepted(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusAccepted} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			polls := 0
			client, server := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/v1/deviceToken/poll" {
					t.Errorf("unexpected path %q", r.URL.Path)
				}
				if got := r.URL.Query().Get("verifier"); got != "verifier-value" {
					t.Errorf("verifier = %q", got)
				}
				if got := r.URL.Query().Get("challenge_method"); got != "S256" {
					t.Errorf("challenge_method = %q", got)
				}
				polls++
				if polls == 1 {
					w.WriteHeader(status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"token":"dt-ok","refresh_token":"drt-ok","expires_in":2592000000}`)
			})
			_ = server

			flow := &DeviceFlow{State: "qd-1", Interval: time.Millisecond, ExpiresIn: 30, nonce: "nonce-value", verifier: "verifier-value"}
			token, errTry := client.WaitForAuthorization(context.Background(), flow)
			if errTry != nil {
				t.Fatalf("WaitForAuthorization() error = %v", errTry)
			}
			if token.AccessToken != "dt-ok" || token.RefreshToken != "drt-ok" {
				t.Fatalf("token = %+v", token)
			}
			if token.ExpiresIn != 2592000 {
				t.Fatalf("ExpiresIn = %d, want 2592000 seconds (30 days)", token.ExpiresIn)
			}
			if polls < 2 {
				t.Fatalf("poll count = %d, want at least 2", polls)
			}
		})
	}
}

func TestDeviceFlowStartBuildsRealmURL(t *testing.T) {
	tests := []struct {
		name          string
		realm         RealmConfig
		wantRedirect  bool
		wantNonceDash bool
	}{
		{name: "cn", realm: RealmCN, wantRedirect: true, wantNonceDash: true},
		{name: "intl", realm: RealmINTL, wantRedirect: false, wantNonceDash: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(&http.Client{}, test.realm)
			flow, errStart := client.StartDeviceFlow(context.Background())
			if errStart != nil {
				t.Fatalf("StartDeviceFlow() error = %v", errStart)
			}
			if !strings.HasPrefix(flow.AuthURL, test.realm.WebsiteBase+"/device/selectAccounts?") {
				t.Fatalf("AuthURL = %q", flow.AuthURL)
			}
			if gotRedirect := strings.Contains(flow.AuthURL, "redirect_uri="); gotRedirect != test.wantRedirect {
				t.Fatalf("redirect_uri present = %v, want %v (url %q)", gotRedirect, test.wantRedirect, flow.AuthURL)
			}
			if !strings.Contains(flow.AuthURL, "challenge_method=S256") || !strings.Contains(flow.AuthURL, "client_id=") {
				t.Fatalf("AuthURL missing required parameters: %q", flow.AuthURL)
			}
			if test.wantNonceDash && !strings.Contains(flow.nonce, "-") {
				t.Fatalf("nonce = %q, want hyphenated UUID", flow.nonce)
			}
			if !test.wantNonceDash && (len(flow.nonce) != 32 || strings.Contains(flow.nonce, "-")) {
				t.Fatalf("nonce = %q, want 32-character hex", flow.nonce)
			}
			if flow.verifier == "" {
				t.Fatal("verifier is empty")
			}
		})
	}
}

func TestExchangePAT(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/jobToken/exchange" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"personal_token":"pt-abc"`) {
			t.Errorf("body = %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"jt-1","refresh_token":"jrt-1","expires_in":86400000}`)
	})
	token, errExchange := client.ExchangePAT(context.Background(), "pt-abc")
	if errExchange != nil {
		t.Fatalf("ExchangePAT() error = %v", errExchange)
	}
	if token.AccessToken != "jt-1" || token.RefreshToken != "jrt-1" {
		t.Fatalf("token = %+v", token)
	}
	if token.ExpiresIn != 86400 {
		t.Fatalf("ExpiresIn = %d, want 86400", token.ExpiresIn)
	}
}

func TestExchangePATRejectsBadPrefix(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("upstream must not be called for a malformed personal token")
	})
	if _, errExchange := client.ExchangePAT(context.Background(), "drt-nope"); errExchange == nil {
		t.Fatal("ExchangePAT() must reject tokens without the pt- prefix")
	}
}

func TestExchangePATError(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"message":"bad personal token"}`)
	})
	_, errExchange := client.ExchangePAT(context.Background(), "pt-bad")
	if errExchange == nil || !strings.Contains(errExchange.Error(), "401") {
		t.Fatalf("ExchangePAT() error = %v, want a 401 failure", errExchange)
	}
}

func TestRefreshRoutesToDeviceEndpoint(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/deviceToken/refresh" {
			t.Errorf("path = %q, want the device refresh endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"dt-new","refresh_token":"drt-new","expires_in":600000}`)
	})
	token, errRefresh := client.Refresh(context.Background(), "drt-old", "")
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if token.AccessToken != "dt-new" || token.RefreshToken != "drt-new" {
		t.Fatalf("token = %+v", token)
	}
}

func TestRefreshRoutesToJobEndpoint(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/jobToken/refresh" {
			t.Errorf("path = %q, want the job refresh endpoint", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"jt-new","refresh_token":"jrt-new"}`)
	})
	token, errRefresh := client.Refresh(context.Background(), "jrt-old", "")
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if token.AccessToken != "jt-new" {
		t.Fatalf("token = %+v", token)
	}
}

// TestRefreshJobFallsBackToPAT verifies that a rejected job refresh token falls
// back to the personal token, while a device refresh token never does.
func TestRefreshJobFallsBackToPAT(t *testing.T) {
	var sawExchange bool
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/jobToken/refresh":
			w.WriteHeader(http.StatusUnauthorized)
		case "/api/v1/jobToken/exchange":
			sawExchange = true
			_, _ = io.WriteString(w, `{"token":"jt-from-pat"}`)
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	token, errRefresh := client.Refresh(context.Background(), "jrt-expired", "pt-live")
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if !sawExchange {
		t.Fatal("expected a PAT exchange fallback")
	}
	if token.AccessToken != "jt-from-pat" {
		t.Fatalf("token = %+v", token)
	}
}

func TestRefreshDeviceDoesNotFallBackToPAT(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/jobToken/exchange" {
			t.Error("an active OAuth session must not be replaced by a PAT")
		}
		w.WriteHeader(http.StatusUnauthorized)
	})
	if _, errRefresh := client.Refresh(context.Background(), "drt-dead", "pt-live"); errRefresh == nil {
		t.Fatal("Refresh() must surface the device refresh failure")
	}
}

func TestRefreshWithoutCredentials(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("no request expected")
	})
	if _, errRefresh := client.Refresh(context.Background(), "", ""); errRefresh == nil {
		t.Fatal("Refresh() with no credential must fail")
	}
}

func TestRefreshErrorPropagates(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"message":"boom"}`)
	})
	_, errRefresh := client.Refresh(context.Background(), "drt-x", "")
	if errRefresh == nil || !strings.Contains(errRefresh.Error(), "500") {
		t.Fatalf("Refresh() error = %v, want a 500 failure", errRefresh)
	}
	// The conductor classifies refresh failures by status code, so the error
	// must expose it rather than relying on text parsing.
	var statusErr *StatusError
	if !errors.As(errRefresh, &statusErr) || statusErr.StatusCode() != http.StatusInternalServerError {
		t.Fatalf("Refresh() error = %v, want a StatusError carrying 500", errRefresh)
	}
}

func TestIsSessionDead(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{body: `{"code":"TOKEN_EXPIRE"}`, want: true},
		{body: `{"message":"error 12153"}`, want: true},
		{body: `{"message":"Offline user session not found"}`, want: true},
		{body: `{"message":"temporary outage"}`, want: false},
		{body: "", want: false},
	}
	for _, test := range tests {
		if got := IsSessionDead(test.body); got != test.want {
			t.Fatalf("IsSessionDead(%q) = %v, want %v", test.body, got, test.want)
		}
	}
}

func TestFetchUserInfo(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/userinfo" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer dt-1" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"id":"uid-1","name":"alice","user_type":"personal_pro","organization_id":"1234567890123456789","organization_name":"Acme"}`)
	})
	info, errInfo := client.FetchUserInfo(context.Background(), "dt-1")
	if errInfo != nil {
		t.Fatalf("FetchUserInfo() error = %v", errInfo)
	}
	if info.ID != "uid-1" || info.Name != "alice" || info.UserType != "personal_pro" {
		t.Fatalf("info = %+v", info)
	}
	// The organization id must survive as an exact string: the auth-file read
	// path has no UseNumber and a large integer would lose precision.
	if info.OrganizationID != "1234567890123456789" {
		t.Fatalf("OrganizationID = %q", info.OrganizationID)
	}
	if info.OrganizationName != "Acme" {
		t.Fatalf("OrganizationName = %q", info.OrganizationName)
	}
}

func TestFetchUserInfoDefaultsUserType(t *testing.T) {
	client, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"id":"uid-1"}`)
	})
	info, errInfo := client.FetchUserInfo(context.Background(), "dt-1")
	if errInfo != nil {
		t.Fatalf("FetchUserInfo() error = %v", errInfo)
	}
	if info.UserType != DefaultUserType {
		t.Fatalf("UserType = %q, want %q", info.UserType, DefaultUserType)
	}
}

func TestRealmLookups(t *testing.T) {
	if realm, ok := RealmForProvider("qoder"); !ok || realm.Provider != "qoder" {
		t.Fatalf("RealmForProvider(qoder) = %+v, %v", realm, ok)
	}
	if realm, ok := RealmForProvider("qoder-intl"); !ok || realm.Provider != "qoder-intl" {
		t.Fatalf("RealmForProvider(qoder-intl) = %+v, %v", realm, ok)
	}
	if _, ok := RealmForProvider("nope"); ok {
		t.Fatal("RealmForProvider(nope) must not resolve")
	}
	if got := NormalizeRealm("intl"); got != "intl" {
		t.Fatalf("NormalizeRealm(intl) = %q", got)
	}
	if got := NormalizeRealm(""); got != "cn" {
		t.Fatalf("NormalizeRealm(\"\") = %q, want cn", got)
	}
	if got := NormalizeRealm("bogus"); got != "cn" {
		t.Fatalf("NormalizeRealm(bogus) = %q, want the cn default", got)
	}
}
