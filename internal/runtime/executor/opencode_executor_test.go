package executor

import (
	"context"
	"net/http"
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestNewOpenCodeExecutor(t *testing.T) {
	cfg := &config.Config{}
	exec := NewOpenCodeExecutor(cfg)
	if exec == nil {
		t.Fatal("expected non-nil executor")
	}
	if exec.Identifier() != constant.OpenCode {
		t.Fatalf("expected identifier %q, got %q", constant.OpenCode, exec.Identifier())
	}

	execCustom := NewOpenCodeExecutorWithProvider("my-opencode", cfg)
	if execCustom == nil {
		t.Fatal("expected non-nil custom executor")
	}
	if execCustom.Identifier() != "my-opencode" {
		t.Fatalf("expected identifier 'my-opencode', got %q", execCustom.Identifier())
	}
}

func TestOpenCodeExecutor_PrepareOpenCodeAuth(t *testing.T) {
	cfg := &config.Config{}
	exec := NewOpenCodeExecutor(cfg)

	// Test case 1: Base URL with /chat/completions trimmed, api_key empty normalized to public
	auth := &cliproxyauth.Auth{
		ID:       "auth-test-1",
		Provider: constant.OpenCode,
		Attributes: map[string]string{
			"base_url":                  "https://opencode.ai/inference/openai/v1/chat/completions/",
			"api_key":                   "sk-placeholder-999",
			"header:x-opencode-request": "must-be-removed",
		},
	}

	clientHeaders := make(http.Header)
	clientHeaders.Set("x-claude-code-session-id", "test-claude-session-123")

	prepared := exec.prepareOpenCodeAuth(context.Background(), auth, "deepseek-v4.1-flash-free", clientHeaders)
	if prepared == nil {
		t.Fatal("expected non-nil prepared auth")
	}

	// 1. Base URL trimmed
	expectedURL := "https://opencode.ai/inference/openai/v1"
	if prepared.Attributes["base_url"] != expectedURL {
		t.Errorf("expected base_url %q, got %q", expectedURL, prepared.Attributes["base_url"])
	}

	// 2. API key normalized to public
	if prepared.Attributes["api_key"] != "public" {
		t.Errorf("expected api_key 'public', got %q", prepared.Attributes["api_key"])
	}

	// 3. User-Agent
	if prepared.Attributes["header:User-Agent"] != OpenCodeDefaultUserAgent {
		t.Errorf("expected User-Agent %q, got %q", OpenCodeDefaultUserAgent, prepared.Attributes["header:User-Agent"])
	}

	// 4. Client
	if prepared.Attributes["header:x-opencode-client"] != OpenCodeDefaultClient {
		t.Errorf("expected x-opencode-client %q, got %q", OpenCodeDefaultClient, prepared.Attributes["header:x-opencode-client"])
	}

	// 5. Session headers
	ses := prepared.Attributes["header:x-opencode-session"]
	sesPattern := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	if !sesPattern.MatchString(ses) {
		t.Errorf("x-opencode-session %q does not match pattern", ses)
	}
	if prepared.Attributes["header:x-opencode-session-id"] != ses {
		t.Errorf("x-opencode-session-id != session")
	}
	if prepared.Attributes["header:x-session-affinity"] != ses {
		t.Errorf("x-session-affinity != session")
	}

	// 6. Project
	prj := prepared.Attributes["header:x-opencode-project"]
	prjPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	if !prjPattern.MatchString(prj) {
		t.Errorf("x-opencode-project %q is not 40-hex", prj)
	}

	// 7. Forbidden header stripped
	if _, exists := prepared.Attributes["header:x-opencode-request"]; exists {
		t.Errorf("header:x-opencode-request was not stripped")
	}
}

func TestOpenCodeExecutor_PrepareRequest(t *testing.T) {
	cfg := &config.Config{}
	exec := NewOpenCodeExecutor(cfg)

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
	if err != nil {
		t.Fatalf("failed to create request: %v", err)
	}

	// Set forbidden header to test stripping
	req.Header.Set("x-opencode-request", "forbidden-tracking-id")
	// Set placeholder token to test normalization
	req.Header.Set("Authorization", "Bearer sk-placeholder-12345")

	auth := &cliproxyauth.Auth{
		ID:       constant.OpenCode,
		Provider: constant.OpenCode,
	}

	if err := exec.PrepareRequest(req, auth); err != nil {
		t.Fatalf("PrepareRequest failed: %v", err)
	}

	// 1. User-Agent
	ua := req.Header.Get("User-Agent")
	if ua != OpenCodeDefaultUserAgent {
		t.Errorf("expected User-Agent %q, got %q", OpenCodeDefaultUserAgent, ua)
	}

	// 2. Client ID
	client := req.Header.Get("x-opencode-client")
	if client != OpenCodeDefaultClient {
		t.Errorf("expected x-opencode-client %q, got %q", OpenCodeDefaultClient, client)
	}

	// 3. Authorization normalized to Bearer public
	authHdr := req.Header.Get("Authorization")
	if authHdr != "Bearer public" {
		t.Errorf("expected Authorization 'Bearer public', got %q", authHdr)
	}

	// 4. Session headers paired and valid
	sesPattern := regexp.MustCompile(`^ses_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	ses := req.Header.Get("x-opencode-session")
	if !sesPattern.MatchString(ses) {
		t.Errorf("x-opencode-session %q does not match pattern", ses)
	}
	if sesId := req.Header.Get("x-opencode-session-id"); sesId != ses {
		t.Errorf("x-opencode-session-id %q != x-opencode-session %q", sesId, ses)
	}
	if sesAff := req.Header.Get("x-session-affinity"); sesAff != ses {
		t.Errorf("x-session-affinity %q != x-opencode-session %q", sesAff, ses)
	}

	// 5. Bare 40-hex project ID
	prjPattern := regexp.MustCompile(`^[0-9a-f]{40}$`)
	prj := req.Header.Get("x-opencode-project")
	if !prjPattern.MatchString(prj) {
		t.Errorf("x-opencode-project %q is not 40-hex", prj)
	}

	// 6. Forbidden header stripped
	if req.Header.Get("x-opencode-request") != "" {
		t.Errorf("x-opencode-request was not stripped")
	}
}

func TestApplyOpenCodeOutgoingTransforms(t *testing.T) {
	body := []byte(`{"model":"x-preview-f-free","stream":false,"messages":[{"role":"user","content":"hello"}]}`)
	opts := cliproxyexecutor.Options{}

	transformed := applyOpenCodeOutgoingTransforms(context.Background(), nil, "x-preview-f-free", opts, body)

	// Verify stream forced to true
	if !gjson.GetBytes(transformed, "stream").Bool() {
		t.Errorf("expected stream: true, got %s", gjson.GetBytes(transformed, "stream").String())
	}

	// Verify stream_options.include_usage: true
	if !gjson.GetBytes(transformed, "stream_options.include_usage").Bool() {
		t.Errorf("expected stream_options.include_usage: true")
	}

	// Verify tools injected
	tools := gjson.GetBytes(transformed, "tools").Array()
	if len(tools) != 5 {
		t.Errorf("expected 5 tools injected, got %d", len(tools))
	}
}
