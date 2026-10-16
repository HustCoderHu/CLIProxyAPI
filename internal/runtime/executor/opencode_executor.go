// Package executor provides per-provider runtime executors.
package executor

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	// OpenCodeDefaultUserAgent is the official CLI User-Agent expected by OpenCode's free-tier gate.
	OpenCodeDefaultUserAgent = "opencode/latest/2.0.29/cli"

	// OpenCodeDefaultClient is the default client identifier.
	OpenCodeDefaultClient = "cli"

	// OpenCodeDefaultBaseURL is the fallback base URL for OpenCode inference.
	OpenCodeDefaultBaseURL = "https://opencode.ai/zen/v1"
)

// OpenCodeExecutor handles OpenCode inference requests with free-tier gate compliance.
type OpenCodeExecutor struct {
	*OpenAICompatExecutor
	provider string
}

// NewOpenCodeExecutor creates a new OpenCode executor.
func NewOpenCodeExecutor(cfg *config.Config) *OpenCodeExecutor {
	return NewOpenCodeExecutorWithProvider(constant.OpenCode, cfg)
}

// NewOpenCodeExecutorWithProvider creates a new OpenCode executor with a custom provider identifier.
func NewOpenCodeExecutorWithProvider(provider string, cfg *config.Config) *OpenCodeExecutor {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = constant.OpenCode
	}
	e := NewOpenAICompatExecutor(provider, cfg)
	e.outgoingTransforms = applyOpenCodeOutgoingTransforms
	return &OpenCodeExecutor{
		OpenAICompatExecutor: e,
		provider:             provider,
	}
}

// Identifier returns the OpenCode provider key.
func (e *OpenCodeExecutor) Identifier() string {
	if e.provider != "" {
		return e.provider
	}
	return constant.OpenCode
}

// Execute forces upstream streaming, aggregates SSE chunks, and normalizes tool calls back to Claude Code.
func (e *OpenCodeExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	preparedAuth := e.prepareOpenCodeAuth(ctx, auth, req.Model, opts.Headers)
	log.Debugf("opencode executor: executing %s request: model=%s, session_id=%s, base_url=%s",
		e.Identifier(), req.Model, preparedAuth.Attributes["header:x-opencode-session"], preparedAuth.Attributes["base_url"])

	opts.Stream = true
	streamResult, err := e.OpenAICompatExecutor.ExecuteStream(ctx, preparedAuth, req, opts)
	if err != nil {
		return resp, err
	}
	if streamResult == nil {
		return resp, nil
	}
	var buffer bytes.Buffer
	for chunk := range streamResult.Chunks {
		if chunk.Err != nil {
			return resp, chunk.Err
		}
		if len(chunk.Payload) > 0 {
			_, _ = buffer.Write(chunk.Payload)
			_, _ = buffer.Write([]byte("\n"))
		}
	}
	aggregated := helps.AggregateOpenAIStreamChunks(buffer.Bytes())
	normalized := helps.NormalizeOpenCodeToolCallsInChatCompletion(aggregated)

	resp = cliproxyexecutor.Response{
		Payload: normalized,
		Headers: streamResult.Headers,
	}
	return resp, nil
}

// ExecuteStream forces streaming and normalizes SSE chunks on-the-fly.
func (e *OpenCodeExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	preparedAuth := e.prepareOpenCodeAuth(ctx, auth, req.Model, opts.Headers)
	log.Debugf("opencode executor: executing %s stream request: model=%s, session_id=%s, base_url=%s",
		e.Identifier(), req.Model, preparedAuth.Attributes["header:x-opencode-session"], preparedAuth.Attributes["base_url"])

	opts.Stream = true
	res, err := e.OpenAICompatExecutor.ExecuteStream(ctx, preparedAuth, req, opts)
	if err != nil || res == nil {
		return res, err
	}
	inChunks := res.Chunks
	if inChunks == nil {
		return res, nil
	}
	outChunks := make(chan cliproxyexecutor.StreamChunk, 16)
	go func() {
		defer close(outChunks)
		for chunk := range inChunks {
			if chunk.Err == nil && len(chunk.Payload) > 0 {
				chunk.Payload = helps.NormalizeOpenCodeToolCallChunk(chunk.Payload)
			}
			outChunks <- chunk
		}
	}()
	res.Chunks = outChunks
	return res, nil
}

// PrepareRequest configures headers for OpenCode free-tier gate compliance.
func (e *OpenCodeExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	var clientHeaders http.Header
	if req.Header != nil {
		clientHeaders = req.Header.Clone()
	}
	preparedAuth := e.prepareOpenCodeAuth(req.Context(), auth, "", clientHeaders)
	if err := e.OpenAICompatExecutor.PrepareRequest(req, preparedAuth); err != nil {
		return err
	}
	req.Header.Del("x-opencode-request")
	return nil
}

// HttpRequest executes an ad-hoc OpenCode request with normalized credentials.
func (e *OpenCodeExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("opencode executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := e.newHTTPClient(ctx, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenCodeExecutor) prepareOpenCodeAuth(ctx context.Context, auth *cliproxyauth.Auth, model string, clientHeaders http.Header) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	prepared := auth.Clone()
	if prepared.Attributes == nil {
		prepared.Attributes = make(map[string]string)
	}

	// 1. Base URL normalization: default to https://opencode.ai/zen/v1, and trim trailing /chat/completions and slashes.
	baseURL := strings.TrimSpace(prepared.Attributes["base_url"])
	if baseURL == "" {
		baseURL = OpenCodeDefaultBaseURL
	}
	baseURL = strings.TrimRight(baseURL, "/")
	baseURL = strings.TrimSuffix(baseURL, "/chat/completions")
	baseURL = strings.TrimRight(baseURL, "/")
	prepared.Attributes["base_url"] = baseURL

	// 2. Normalize Authorization: for free-tier models or placeholder keys, force "public"
	apiKey := strings.TrimSpace(prepared.Attributes["api_key"])
	if apiKey == "" && clientHeaders != nil {
		apiKey = strings.TrimSpace(clientHeaders.Get("Authorization"))
	}
	token := strings.TrimPrefix(apiKey, "Bearer ")
	token = strings.TrimSpace(token)
	modelLower := strings.ToLower(model)
	isFreeModel := strings.HasSuffix(modelLower, "-free") || modelLower == "big-pickle"
	if isFreeModel || token == "" || token == "public" || strings.HasPrefix(token, "sk-placeholder") || strings.HasPrefix(token, "dummy") || strings.EqualFold(token, "none") {
		prepared.Attributes["api_key"] = "public"
	}

	// 3. Official descending session ID with affinity
	var sessionSignal string
	if clientHeaders != nil {
		sessionSignal = clientHeaders.Get("x-opencode-session")
		if sessionSignal == "" {
			sessionSignal = clientHeaders.Get("x-session-affinity")
		}
		if sessionSignal == "" {
			sessionSignal = clientHeaders.Get("x-opencode-session-id")
		}
		if sessionSignal == "" {
			sessionSignal = clientHeaders.Get("x-session-id")
		}
		if sessionSignal == "" {
			sessionSignal = clientHeaders.Get("x-claude-code-session-id")
		}
	}
	if sessionSignal == "" && ctx != nil {
		sessionSignal = util.SessionIDFromContext(ctx)
	}
	canonicalSession := helps.CanonicalOpenCodeSessionID(sessionSignal)

	// 4. Set bare 40-hex project ID
	var projectSignal string
	if clientHeaders != nil {
		projectSignal = clientHeaders.Get("x-opencode-project")
	}
	if projectSignal == "" {
		projectSignal = auth.ID
	}
	projectID := helps.OpenCodeProjectID(projectSignal)

	// 5. Inject required headers into prepared.Attributes for OpenAICompatExecutor
	prepared.Attributes["header:User-Agent"] = OpenCodeDefaultUserAgent
	prepared.Attributes["header:x-opencode-client"] = OpenCodeDefaultClient
	prepared.Attributes["header:x-opencode-session"] = canonicalSession
	prepared.Attributes["header:x-opencode-session-id"] = canonicalSession
	prepared.Attributes["header:x-session-affinity"] = canonicalSession
	prepared.Attributes["header:X-Session-Id"] = canonicalSession
	prepared.Attributes["header:x-opencode-project"] = projectID

	// 6. Strip forbidden tracking header
	for k := range prepared.Attributes {
		if strings.HasPrefix(k, "header:") && strings.EqualFold(strings.TrimPrefix(k, "header:"), "x-opencode-request") {
			delete(prepared.Attributes, k)
		}
	}

	return prepared
}

func applyOpenCodeOutgoingTransforms(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, opts cliproxyexecutor.Options, translated []byte) []byte {
	if len(translated) == 0 {
		return translated
	}
	// Force stream: true
	translated, _ = sjson.SetBytes(translated, "stream", true)
	// Ensure stream_options.include_usage: true
	translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	// Inject & merge canonical OpenCode agent tools (preserving Claude Code tools)
	translated = helps.EnsureOpenCodeAnonymousTools(translated)
	return translated
}
