package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

// TestRequestQoderTokenDeviceFlow verifies both realm routes return a device
// authorization payload pointing at the correct realm website. The handler only
// builds the URL locally, so no upstream call is involved.
func TestRequestQoderTokenDeviceFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		route      string
		wantHost   string
		wantClient string
	}{
		{name: "cn", route: "/qoder-auth-url", wantHost: "https://qoder.com.cn/device/selectAccounts?", wantClient: "1c5e33e1-364d-4ce6-b02c-acaa81274a5c"},
		{name: "intl", route: "/qoder-intl-auth-url", wantHost: "https://qoder.com/device/selectAccounts?", wantClient: "e883ade2-e6e3-4d6d-adf7-f92ceff5fdcb"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h := &Handler{cfg: &config.Config{}}
			router := gin.New()
			router.GET("/qoder-auth-url", h.RequestQoderToken)
			router.GET("/qoder-intl-auth-url", h.RequestQoderINTLToken)

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, test.route+"?is_webui=true", nil))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var payload struct {
				Status string `json:"status"`
				URL    string `json:"url"`
				State  string `json:"state"`
				Flow   string `json:"flow"`
			}
			if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &payload); errUnmarshal != nil {
				t.Fatalf("decode response: %v", errUnmarshal)
			}
			if payload.Status != "ok" || payload.Flow != "device" {
				t.Fatalf("payload = %+v", payload)
			}
			if !strings.HasPrefix(payload.URL, test.wantHost) {
				t.Fatalf("url = %q, want prefix %q", payload.URL, test.wantHost)
			}
			if !strings.Contains(payload.URL, "client_id="+test.wantClient) {
				t.Fatalf("url = %q, want client_id %s", payload.URL, test.wantClient)
			}
			if payload.State == "" {
				t.Fatal("state is empty")
			}
			// The poll goroutine is registered for this state; clean it up so the
			// session store does not keep waiting on a browser that never authorizes.
			CancelOAuthSession(payload.State)
		})
	}
}

// TestRequestQoderTokenPersonalTokenRequiresValidPrefix locks the synchronous
// PAT import path: a malformed token is rejected before any upstream call.
func TestRequestQoderTokenPersonalTokenRequiresValidPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{cfg: &config.Config{}}
	router := gin.New()
	router.GET("/qoder-auth-url", h.RequestQoderToken)

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/qoder-auth-url?personal_token=bad-token", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", recorder.Code, recorder.Body.String())
	}
}
