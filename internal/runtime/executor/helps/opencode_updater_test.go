package helps

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestBuildUpdaterHTTPClient_Proxy(t *testing.T) {
	// 显式配置 HTTP 代理
	clientProxy := buildUpdaterHTTPClient("http://127.0.0.1:8080", "", 5*time.Second)
	if clientProxy.Transport == nil {
		t.Fatal("expected non-nil transport for http proxy")
	}
	if clientProxy.Timeout != 5*time.Second {
		t.Fatalf("expected timeout 5s, got %v", clientProxy.Timeout)
	}

	// 显式 Direct 模式
	clientDirect := buildUpdaterHTTPClient("direct", "", 3*time.Second)
	if clientDirect.Transport == nil {
		t.Fatal("expected non-nil transport for direct mode")
	}

	// 默认继承环境变量
	clientEnv := buildUpdaterHTTPClient("", "", 0)
	if clientEnv.Transport == nil {
		t.Fatal("expected non-nil transport for env inheritance")
	}
	if clientEnv.Timeout != 10*time.Second {
		t.Fatalf("expected default timeout 10s, got %v", clientEnv.Timeout)
	}
}

func TestVersionComparison(t *testing.T) {
	tests := []struct {
		v1       string
		v2       string
		expected int
	}{
		{"2.0.32", "2.0.32", 0},
		{"2.0.33", "2.0.32", 1},
		{"2.0.31", "2.0.32", -1},
		{"2.0.9", "2.0.10", -1},
		{"2.0.10", "2.0.9", 1},
		{"2.1.0", "2.0.32", 1},
		{"3.0.0", "2.9.9", 1},
		{"v2.0.33", "2.0.32", 1},
		{"V2.0.33", "v2.0.33", 0},
		{"2.0.32-beta", "2.0.32", 0},
	}

	for _, tt := range tests {
		res := CompareSemver(tt.v1, tt.v2)
		if res != tt.expected {
			t.Errorf("CompareSemver(%q, %q) = %d; expected %d", tt.v1, tt.v2, res, tt.expected)
		}
	}
}

func TestParseOfficialUpdateResponse(t *testing.T) {
	mockJSON := `{"channel":"latest","name":"cli","distribution":"npm","version":"2.0.35"}`
	mockClient := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if !strings.Contains(r.URL.String(), "opencode.ai/update") {
				t.Errorf("unexpected URL: %s", r.URL.String())
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(mockJSON)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	v, err := fetchVersionFromOfficial(context.Background(), mockClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "2.0.35" {
		t.Fatalf("expected 2.0.35, got %s", v)
	}
}

func TestParseGitHubMatchingRefs(t *testing.T) {
	mockRefsJSON := `[
		{"ref": "refs/tags/v2.0.0"},
		{"ref": "refs/tags/v2.0.9"},
		{"ref": "refs/tags/v2.0.34"},
		{"ref": "refs/tags/v2.0.10"},
		{"ref": "refs/tags/vscode-v1.0.0"}
	]`

	var capturedAuth, capturedUA string
	mockClient := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			capturedAuth = r.Header.Get("Authorization")
			capturedUA = r.Header.Get("User-Agent")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(mockRefsJSON)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	v, err := fetchVersionFromGitHub(context.Background(), mockClient, "ghp_secret_token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "2.0.34" {
		t.Fatalf("expected 2.0.34, got %s", v)
	}
	if capturedAuth != "Bearer ghp_secret_token" {
		t.Fatalf("expected Authorization header with token, got %q", capturedAuth)
	}
	if capturedUA != "CLIProxyAPI" {
		t.Fatalf("expected User-Agent CLIProxyAPI, got %q", capturedUA)
	}
}

func TestParseNPMResponse(t *testing.T) {
	mockNPMJSON := `{"name":"@opencode/cli","version":"2.0.33"}`
	mockClient := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString(mockNPMJSON)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	v, err := fetchVersionFromNPM(context.Background(), mockClient)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != "2.0.33" {
		t.Fatalf("expected 2.0.33, got %s", v)
	}
}

func TestFetchLatestOpenCodeVersion_FallbackChain(t *testing.T) {
	// Official 失败(500), GitHub 失败(403), NPM 成功(2.0.36)
	mockClient := &http.Client{
		Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			urlStr := r.URL.String()
			if strings.Contains(urlStr, "opencode.ai/update") {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Body:       io.NopCloser(bytes.NewBufferString("server error")),
					Header:     make(http.Header),
				}, nil
			}
			if strings.Contains(urlStr, "api.github.com") {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Body:       io.NopCloser(bytes.NewBufferString("rate limited")),
					Header:     make(http.Header),
				}, nil
			}
			if strings.Contains(urlStr, "registry.npmjs.org") {
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewBufferString(`{"version":"2.0.36"}`)),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Body:       io.NopCloser(bytes.NewBufferString("not found")),
				Header:     make(http.Header),
			}, nil
		}),
	}

	v, err := FetchLatestOpenCodeVersion(context.Background(), mockClient, "")
	if err != nil {
		t.Fatalf("unexpected error in fallback chain: %v", err)
	}
	if v != "2.0.36" {
		t.Fatalf("expected fallback to NPM 2.0.36, got %s", v)
	}
}

func TestCurrentOpenCodeUserAgent_And_SetVersion(t *testing.T) {
	ResetOpenCodeStateForTest()
	defer ResetOpenCodeStateForTest()

	if CurrentOpenCodeVersion() != OpenCodeBaselineVersion {
		t.Fatalf("expected baseline version %s, got %s", OpenCodeBaselineVersion, CurrentOpenCodeVersion())
	}
	if CurrentOpenCodeUserAgent() != OpenCodeBaselineUserAgent {
		t.Fatalf("expected baseline UA %s, got %s", OpenCodeBaselineUserAgent, CurrentOpenCodeUserAgent())
	}

	SetOpenCodeVersion("2.0.39")
	if CurrentOpenCodeVersion() != "2.0.39" {
		t.Fatalf("expected version 2.0.39, got %s", CurrentOpenCodeVersion())
	}
	expectedUA := "opencode/latest/2.0.39/cli"
	if CurrentOpenCodeUserAgent() != expectedUA {
		t.Fatalf("expected UA %s, got %s", expectedUA, CurrentOpenCodeUserAgent())
	}
}

func TestUpdateOpenCodeTools_DynamicReload(t *testing.T) {
	ResetOpenCodeTools()
	defer ResetOpenCodeTools()

	tools := GetOpenCodeTools()
	if len(tools) != 6 {
		t.Fatalf("expected 6 default tools, got %d", len(tools))
	}

	customJSON := `{
		"shell": {
			"description": "custom dynamic shell description",
			"parameters": {"type": "object"}
		}
	}`

	if err := UpdateOpenCodeTools([]byte(customJSON)); err != nil {
		t.Fatalf("unexpected error updating tools: %v", err)
	}

	updatedTools := GetOpenCodeTools()
	if updatedTools["shell"].Description != "custom dynamic shell description" {
		t.Fatalf("tools not updated dynamically: %+v", updatedTools["shell"])
	}

	// 错误格式测试
	if err := UpdateOpenCodeTools([]byte(`invalid json`)); err == nil {
		t.Fatal("expected error on invalid json, got nil")
	}
}

func TestStartOpenCodeUpdater_Smoke(t *testing.T) {
	ResetOpenCodeStateForTest()
	defer ResetOpenCodeStateForTest()

	cfg := &config.Config{
		GitHubToken: "test_token",
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	StartOpenCodeUpdater(ctx, cfg)
	// sync.Once 保证多次调用安全
	StartOpenCodeUpdater(ctx, cfg)
}
