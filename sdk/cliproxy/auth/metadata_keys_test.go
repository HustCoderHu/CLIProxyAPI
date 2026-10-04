package auth

import (
	"context"
	"reflect"
	"testing"
)

func TestNormalizeCredentialMetadata(t *testing.T) {
	metadata := map[string]any{
		"api-key":               "legacy-key",
		"base-url":              "https://legacy.example",
		"disable-cooling":       true,
		"excluded-models":       []any{"legacy-model"},
		"fingerprint-profile":   "claude-code-cli",
		"model-aliases":         []any{map[string]any{"name": "upstream", "alias": "public"}},
		"proxy-url":             "http://legacy-proxy.example",
		"request-retry":         3,
		"request_retry":         0,
		"request-scoped-errors": []any{map[string]any{"status": 429}},
		"tool-prefix-disabled":  true,
		"provider_field":        "preserved",
	}

	NormalizeCredentialMetadata(metadata)

	want := map[string]any{
		"api_key":               "legacy-key",
		"base_url":              "https://legacy.example",
		"disable_cooling":       true,
		"excluded_models":       []any{"legacy-model"},
		"fingerprint_profile":   "claude-code-cli",
		"model_aliases":         []any{map[string]any{"name": "upstream", "alias": "public"}},
		"proxy_url":             "http://legacy-proxy.example",
		"request_retry":         0,
		"request_scoped_errors": []any{map[string]any{"status": 429}},
		"tool_prefix_disabled":  true,
		"provider_field":        "preserved",
	}
	if !reflect.DeepEqual(metadata, want) {
		t.Fatalf("NormalizeCredentialMetadata() = %#v, want %#v", metadata, want)
	}
}

func TestCanonicalCredentialMetadataKeyPreservesUnknownKeys(t *testing.T) {
	if got := CanonicalCredentialMetadataKey("provider-specific-key"); got != "provider-specific-key" {
		t.Fatalf("CanonicalCredentialMetadataKey() = %q, want provider-specific-key", got)
	}
}

func TestManagerRegisterNormalizesCredentialMetadata(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "legacy-auth",
		Provider: "codex",
		Metadata: map[string]any{
			"request-retry": 2,
			"request_retry": 0,
		},
	}

	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if got, ok := registered.Metadata["request_retry"]; !ok || got != 0 {
		t.Fatalf("registered request_retry = %#v, want 0", got)
	}
	if _, exists := registered.Metadata["request-retry"]; exists {
		t.Fatalf("registered metadata retained legacy key: %#v", registered.Metadata)
	}
}

func TestNormalizeCredentialMetadata_WorkBuddyNested(t *testing.T) {
	metadata := map[string]any{
		"account": map[string]any{
			"uid":          "38993a2b-faa6-416d-a71e-7d367aff7ff9",
			"enterpriseId": "",
			"nickname":     "hu-广州",
		},
		"auth": map[string]any{
			"accessToken":  "jwt-access-token-123",
			"refreshToken": "jwt-refresh-token-456",
			"expiresAt":    float64(1794637676),
			"domain":       "www.codebuddy.cn",
			"realm":        "cn",
		},
	}

	NormalizeCredentialMetadata(metadata)

	if got := metadata["type"]; got != "codebuddy-cn" {
		t.Errorf("type = %v, want codebuddy-cn", got)
	}
	if got := metadata["auth_kind"]; got != "oauth" {
		t.Errorf("auth_kind = %v, want oauth", got)
	}
	if got := metadata["access_token"]; got != "jwt-access-token-123" {
		t.Errorf("access_token = %v, want jwt-access-token-123", got)
	}
	if got := metadata["refresh_token"]; got != "jwt-refresh-token-456" {
		t.Errorf("refresh_token = %v, want jwt-refresh-token-456", got)
	}
	if got := metadata["base_url"]; got != "https://copilot.tencent.com/v2" {
		t.Errorf("base_url = %v, want https://copilot.tencent.com/v2", got)
	}
	if got := metadata["label"]; got != "hu-广州" {
		t.Errorf("label = %v, want hu-广州", got)
	}
	if got := metadata["email"]; got != "hu-广州" {
		t.Errorf("email = %v, want hu-广州", got)
	}
	if got := metadata["account_id"]; got != "38993a2b-faa6-416d-a71e-7d367aff7ff9" {
		t.Errorf("account_id = %v, want 38993a2b-faa6-416d-a71e-7d367aff7ff9", got)
	}
	if got, ok := metadata["expired"].(string); !ok || got == "" {
		t.Errorf("expired = %v, want non-empty RFC3339 string", got)
	}
}

func TestNormalizeCredentialMetadata_WorkBuddyGlobal(t *testing.T) {
	metadata := map[string]any{
		"auth": map[string]any{
			"accessToken":  "global-access-token",
			"refreshToken": "global-refresh-token",
			"domain":       "www.workbuddy.ai",
		},
	}

	NormalizeCredentialMetadata(metadata)

	if got := metadata["type"]; got != "codebuddy-cn" {
		t.Errorf("type = %v, want codebuddy-cn", got)
	}
	if got := metadata["base_url"]; got != "https://www.workbuddy.ai/v2" {
		t.Errorf("base_url = %v, want https://www.workbuddy.ai/v2", got)
	}
}
