// Package executor provides per-provider runtime executors.
package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"

	codebuddyauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/codebuddycn"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// CodeBuddyCNBaseURL is the Tencent CodeBuddy CN OpenAI-compatible gateway
// chat-completions endpoint. The executor derives its base URL from the
// synthesized auth attributes; this constant documents the canonical endpoint.
const CodeBuddyCNBaseURL = "https://copilot.tencent.com/v2/chat/completions"

// CodeBuddyCNExecutor talks to the CodeBuddy CN (Tencent) OpenAI-compatible gateway.
//
// CodeBuddy CN rejects non-stream chat requests (HTTP 400, code 11101). To keep the
// OpenAI-compatible executor path intact we force streaming at the executor boundary
// and let the HTTP handler aggregate the SSE back into a JSON response for
// non-streaming clients.
//
// Reasoning is opt-in: reasoning_summary:"auto" is only added when the client
// explicitly sets reasoning_effort. "none"/"off" drops the field entirely (the
// gateway has no "none" value). Plain requests are left untouched — forcing
// reasoning on plain requests trips CodeBuddy's content filter.
//
// Agent system prompts are replaced with a neutral prompt because Tencent's
// content filter flags CLI agent system prompts (e.g. "You are Claude Code…") as
// sensitive content and rejects the whole request.
type CodeBuddyCNExecutor struct {
	*OpenAICompatExecutor
}

// NewCodeBuddyCNExecutor constructs a CodeBuddy CN executor.
func NewCodeBuddyCNExecutor(cfg *config.Config) *CodeBuddyCNExecutor {
	e := NewOpenAICompatExecutor(constant.CodeBuddyCN, cfg)
	e.httpClientFactory = helps.NewCodeBuddyCNHTTPClient
	e.outgoingTransforms = applyCodeBuddyCNOutgoingTransforms
	return &CodeBuddyCNExecutor{
		OpenAICompatExecutor: e,
	}
}

// Identifier returns the executor identifier.
func (e *CodeBuddyCNExecutor) Identifier() string { return constant.CodeBuddyCN }

// UsesConfig reports whether the executor is bound to the provided config
// snapshot. The embedded OpenAICompatExecutor pointer may be nil, so the check
// cannot rely on method promotion alone.
func (e *CodeBuddyCNExecutor) UsesConfig(cfg *config.Config) bool {
	if e == nil || e.OpenAICompatExecutor == nil {
		return false
	}
	return e.OpenAICompatExecutor.UsesConfig(cfg)
}

// Execute forces streaming upstream and aggregates the SSE chunks back into a
// single OpenAI JSON response for non-streaming clients. CodeBuddy CN rejects
// non-stream requests (HTTP 400 code 11101), so the non-streaming path must
// drive a streamed upstream request and fold the chunks into a chat.completion.
func (e *CodeBuddyCNExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	auth = prepareCodeBuddyCNAuth(auth)
	opts.Stream = true
	streamResult, err := e.OpenAICompatExecutor.ExecuteStream(ctx, auth, req, opts)
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
	resp = cliproxyexecutor.Response{
		Payload: aggregateCodeBuddyCNChunks(buffer.Bytes()),
		Headers: streamResult.Headers,
	}
	return resp, nil
}

// ExecuteStream forces streaming and delegates to the OpenAI-compatible executor.
func (e *CodeBuddyCNExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	opts.Stream = true
	return e.OpenAICompatExecutor.ExecuteStream(ctx, prepareCodeBuddyCNAuth(auth), req, opts)
}

// PrepareRequest injects CodeBuddy OAuth or API-key credentials into ad-hoc requests.
func (e *CodeBuddyCNExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	return e.OpenAICompatExecutor.PrepareRequest(req, prepareCodeBuddyCNAuth(auth))
}

// HttpRequest executes an ad-hoc CodeBuddy request with normalized credentials.
func (e *CodeBuddyCNExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	return e.OpenAICompatExecutor.HttpRequest(ctx, prepareCodeBuddyCNAuth(auth), req)
}

// Refresh rotates CodeBuddy OAuth credentials using the stored refresh token.
func (e *CodeBuddyCNExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.OpenAICompatExecutor.cfg, auth); handled {
		return refreshed, err
	}
	if auth == nil {
		return nil, fmt.Errorf("codebuddy-cn executor: auth is nil")
	}
	refreshToken := codeBuddyCNMetadataString(auth, "refresh_token")
	if refreshToken == "" {
		return auth, nil
	}
	token, err := codebuddyauth.NewClientWithProxyURL(e.OpenAICompatExecutor.cfg, auth.ProxyURL).Refresh(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = constant.CodeBuddyCN
	auth.Metadata["auth_kind"] = cliproxyauth.AuthKindOAuth
	auth.Metadata["access_token"] = token.AccessToken
	if strings.TrimSpace(token.RefreshToken) != "" {
		auth.Metadata["refresh_token"] = token.RefreshToken
	}
	if strings.TrimSpace(token.TokenType) != "" {
		auth.Metadata["token_type"] = token.TokenType
	}
	if token.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = token.ExpiresIn
	}
	if !token.ExpiresAt.IsZero() {
		auth.Metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)

	// Sync back to workbuddy2api nested structure if present, preserving bidirectional compatibility.
	if authObj, ok := auth.Metadata["auth"].(map[string]any); ok && authObj != nil {
		authObj["accessToken"] = token.AccessToken
		if strings.TrimSpace(token.RefreshToken) != "" {
			authObj["refreshToken"] = token.RefreshToken
		}
		if !token.ExpiresAt.IsZero() {
			authObj["expiresAt"] = token.ExpiresAt.Unix()
		}
	}
	if _, ok := auth.Metadata["accessToken"]; ok {
		auth.Metadata["accessToken"] = token.AccessToken
		if strings.TrimSpace(token.RefreshToken) != "" {
			auth.Metadata["refreshToken"] = token.RefreshToken
		}
		if !token.ExpiresAt.IsZero() {
			auth.Metadata["expiresAt"] = token.ExpiresAt.Unix()
		}
	}
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	auth.Attributes[cliproxyauth.AttributeAuthKind] = cliproxyauth.AuthKindOAuth
	if strings.TrimSpace(auth.Attributes["base_url"]) == "" {
		auth.Attributes["base_url"] = codebuddyauth.APIBaseURL
	}
	return auth, nil
}

func prepareCodeBuddyCNAuth(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	prepared := auth.Clone()
	if prepared.Attributes == nil {
		prepared.Attributes = make(map[string]string)
	}
	if strings.TrimSpace(prepared.Attributes["base_url"]) == "" {
		baseURL := codeBuddyCNMetadataString(prepared, "base_url")
		if baseURL == "" {
			baseURL = codebuddyauth.APIBaseURL
		}
		prepared.Attributes["base_url"] = baseURL
	}
	if strings.TrimSpace(prepared.Attributes["api_key"]) == "" {
		prepared.Attributes["api_key"] = codeBuddyCNMetadataString(prepared, "access_token")
	}
	defaults := map[string]string{
		"User-Agent":          "CLI/2.108.1 CodeBuddy/2.108.1",
		"X-Product":           "SaaS",
		"X-IDE-Type":          "CLI",
		"X-IDE-Name":          "CLI",
		"X-Requested-With":    "XMLHttpRequest",
		"X-Codebuddy-Request": "1",
	}
	for name, value := range defaults {
		if !codeBuddyCNHasCustomHeader(prepared.Attributes, name) {
			prepared.Attributes["header:"+name] = value
		}
	}
	return prepared
}

func codeBuddyCNMetadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

func codeBuddyCNHasCustomHeader(attrs map[string]string, name string) bool {
	for key := range attrs {
		if strings.HasPrefix(key, "header:") && strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(key, "header:")), name) {
			return true
		}
	}
	return false
}

// applyCodeBuddyCNOutgoingTransforms mutates the final OpenAI upstream document:
// forcing stream, mapping reasoning_effort to reasoning_summary, and
// neutralizing agent system prompts.
func applyCodeBuddyCNOutgoingTransforms(ctx context.Context, auth *cliproxyauth.Auth, baseModel string, opts cliproxyexecutor.Options, doc *ojson.Doc) error {
	if doc == nil {
		return nil
	}
	if err := doc.Set("stream", true); err != nil {
		return err
	}
	applyCodeBuddyCNReasoning(doc)
	applyCodeBuddyCNBackfillReasoning(doc)
	applyCodeBuddyCNAgentSystemPrompt(doc)
	return nil
}

// applyCodeBuddyCNReasoning configures reasoning/thinking parameters for CodeBuddy.
// For DeepSeek models, upstream requires thinking.type: "enabled" and reasoning_effort
// (e.g. "high") to activate reasoning/thinking.
// For other models, reasoning_effort is mapped to reasoning_summary: "auto".
func applyCodeBuddyCNReasoning(doc *ojson.Doc) {
	model := strings.ToLower(strings.TrimSpace(doc.Get("model").String()))
	isDeepSeek := strings.HasPrefix(model, "deepseek")

	reasoningEffort := doc.Get("reasoning_effort")
	hasEffort := reasoningEffort.Exists()
	eff := ""
	if hasEffort {
		eff = strings.ToLower(strings.TrimSpace(reasoningEffort.String()))
	}

	thinkingType := strings.ToLower(strings.TrimSpace(doc.Get("thinking.type").String()))

	if isDeepSeek {
		if eff == "none" || eff == "off" || thinkingType == "disabled" {
			_ = doc.Delete("reasoning_effort")
			_ = doc.Delete("reasoning_summary")
			_ = doc.Set("thinking.type", "disabled")
			return
		}
		_ = doc.Set("thinking.type", "enabled")
		if !hasEffort || eff == "" {
			_ = doc.Set("reasoning_effort", "high")
		}
		return
	}

	if !hasEffort {
		return
	}
	switch eff {
	case "none", "off", "":
		_ = doc.Delete("reasoning_effort")
		_ = doc.Delete("reasoning_summary")
	default:
		_ = doc.Delete("reasoning_effort")
		_ = doc.Set("reasoning_summary", "auto")
	}
}

// applyCodeBuddyCNBackfillReasoning ensures multi-turn assistant messages carry reasoning_content
// when reasoning traces exist, adhering to DeepSeek upstream requirements.
func applyCodeBuddyCNBackfillReasoning(doc *ojson.Doc) {
	model := strings.ToLower(strings.TrimSpace(doc.Get("model").String()))
	if !strings.HasPrefix(model, "deepseek") {
		return
	}
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	hasTrace := false
	for _, msg := range messages.Array() {
		if msg.Get("role").String() != "assistant" {
			continue
		}
		if reasoning := msg.Get("reasoning"); reasoning.Exists() && reasoning.String() != "" {
			hasTrace = true
			break
		}
		if msg.Get("reasoning_content").Exists() {
			hasTrace = true
			break
		}
	}
	if !hasTrace {
		return
	}
	changed := false
	out := make([]json.RawMessage, 0, len(messages.Array()))
	for _, item := range messages.Array() {
		if item.Get("role").String() == "assistant" && !item.Get("reasoning_content").Exists() {
			entry := make(map[string]any)
			_ = json.Unmarshal([]byte(item.Raw), &entry)
			entry["reasoning_content"] = item.Get("reasoning").String()
			marshaled, err := json.Marshal(entry)
			if err != nil {
				return
			}
			out = append(out, marshaled)
			changed = true
			continue
		}
		out = append(out, json.RawMessage(item.Raw))
	}
	if !changed {
		return
	}
	marshaled, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = doc.SetRawBytes("messages", marshaled)
}

// agentSystemPromptPattern matches CLI agent system prompts that CodeBuddy's
// content filter rejects as sensitive content.
var agentSystemPromptPattern = regexp.MustCompile(`(?i)you are claude code|claude\.?code.+official.+cli|anthropic.+official.+cli|you are (?:cursor|windsurf|cline|aider|continue|copilot|cody)|you are an? (?:ai )?(?:coding |code )?agent|cc_entrypoint\s*=\s*(?:cli|vscode|jetbrains|gui)|claude\.?code.+issues|give feedback.+claude\.?code|you are .{0,30}(?:powerful )?ai agent|orchestration capabilities|OhMyOpenCode|<agent-identity>|<Role>|<Behavior_Instructions>|you are an expert coding assistant|coding agent harness|operating inside pi\b`)

// neutralSystemPrompt replaces detected agent system prompts.
const neutralSystemPrompt = "You are a helpful AI assistant that helps with software engineering tasks."

// applyCodeBuddyCNAgentSystemPrompt replaces agent system prompts in either the
// top-level Anthropic `system` field or the openai `messages` array system role
// with a neutral prompt, leaving legitimate user system prompts untouched.
func applyCodeBuddyCNAgentSystemPrompt(doc *ojson.Doc) {
	if !agentSystemPromptPattern.MatchString(doc.String()) {
		return
	}
	replaceSystemField(doc)
	replaceSystemMessages(doc)
}

// replaceSystemField handles the Anthropic-style top-level `system` field.
func replaceSystemField(doc *ojson.Doc) {
	system := doc.Get("system")
	if !system.Exists() {
		return
	}
	text := systemContentText(system)
	if text == "" || !agentSystemPromptPattern.MatchString(text) {
		return
	}
	_ = doc.Set("system", neutralSystemPrompt)
}

// replaceSystemMessages handles OpenAI-style system messages in the `messages` array.
func replaceSystemMessages(doc *ojson.Doc) {
	messages := doc.Get("messages")
	if !messages.Exists() || !messages.IsArray() {
		return
	}
	changed := false
	out := make([]json.RawMessage, 0, len(messages.Array()))
	for _, item := range messages.Array() {
		if item.Get("role").String() == "system" {
			text := systemContentText(item.Get("content"))
			if text != "" && agentSystemPromptPattern.MatchString(text) {
				entry := map[string]any{
					"role":    "system",
					"content": neutralSystemPrompt,
				}
				marshaled, err := json.Marshal(entry)
				if err != nil {
					return
				}
				out = append(out, marshaled)
				changed = true
				continue
			}
		}
		out = append(out, json.RawMessage(item.Raw))
	}
	if !changed {
		return
	}
	marshaled, err := json.Marshal(out)
	if err != nil {
		return
	}
	_ = doc.SetRawBytes("messages", marshaled)
}

// systemContentText flattens a system content (string or typed blocks) to text.
func systemContentText(res ojson.Result) string {
	if res.Type == ojson.String {
		return res.String()
	}
	if res.Type == ojson.JSON {
		var parts []string
		for _, block := range res.Array() {
			if t := strings.TrimSpace(block.Get("text").String()); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// aggregateCodeBuddyCNChunks folds a sequence of OpenAI chat.completion.chunk
// JSON objects (one per line) into a single chat.completion JSON object. The
// OpenAI→OpenAI stream translator strips the SSE "data:" prefix, so the input
// here is raw JSON chunks. We accumulate content/reasoning deltas per choice and
// tool-call fragments, then emit a non-streaming response with the final usage.
func aggregateCodeBuddyCNChunks(raw []byte) []byte {
	type toolCall struct {
		index     int64
		id        string
		name      string
		arguments strings.Builder
	}

	var id, model string
	var created int64
	var usageRaw string
	var finishReason string
	// choice state keyed by index
	type choiceState struct {
		content         strings.Builder
		reasoning       strings.Builder
		toolCalls       []*toolCall
		toolCallByIndex map[int64]*toolCall
	}
	choices := map[int64]*choiceState{}
	var maxIndex int64 = -1

	for _, line := range bytes.Split(raw, []byte("\n")) {
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		if bytes.Equal(trimmed, []byte("[DONE]")) {
			continue
		}
		if !json.Valid(trimmed) {
			continue
		}
		root := ojson.ParseBytes(trimmed)
		if id == "" {
			id = root.Get("id").String()
		}
		if model == "" {
			model = root.Get("model").String()
		}
		if created == 0 {
			created = root.Get("created").Int()
		}
		if u := root.Get("usage"); u.Exists() && u.Raw != "null" {
			usageRaw = u.Raw
		}

		choicesResult := root.Get("choices")
		if !choicesResult.IsArray() {
			continue
		}
		for _, choice := range choicesResult.Array() {
			idx := choice.Get("index").Int()
			if idx > maxIndex {
				maxIndex = idx
			}
			st, ok := choices[idx]
			if !ok {
				st = &choiceState{toolCallByIndex: map[int64]*toolCall{}}
				choices[idx] = st
			}
			if fr := choice.Get("finish_reason").String(); fr != "" && fr != "null" {
				finishReason = fr
			}
			delta := choice.Get("delta")
			if c := delta.Get("content").String(); c != "" {
				st.content.WriteString(c)
			}
			if c := delta.Get("reasoning_content").String(); c != "" {
				st.reasoning.WriteString(c)
			}
			for _, tc := range delta.Get("tool_calls").Array() {
				tcIndex := tc.Get("index").Int()
				call := st.toolCallByIndex[tcIndex]
				if call == nil {
					call = &toolCall{index: tcIndex}
					st.toolCallByIndex[tcIndex] = call
					st.toolCalls = append(st.toolCalls, call)
				}
				if v := tc.Get("id").String(); v != "" {
					call.id = v
				}
				if v := tc.Get("function.name").String(); v != "" {
					call.name = v
				}
				if v := tc.Get("function.arguments").String(); v != "" {
					call.arguments.WriteString(v)
				}
			}
		}
	}

	out := ojson.NewDoc([]byte(`{}`))
	_ = out.Set("id", id)
	_ = out.Set("object", "chat.completion")
	_ = out.Set("model", model)
	_ = out.Set("created", created)

	choiceList := make([][]byte, 0, maxIndex+1)
	for i := int64(0); i <= maxIndex; i++ {
		st := choices[i]
		if st == nil {
			continue
		}
		msg := ojson.NewDoc([]byte(`{"role":"assistant"}`))
		_ = msg.Set("content", st.content.String())
		if st.reasoning.Len() > 0 {
			_ = msg.Set("reasoning_content", st.reasoning.String())
		}
		if len(st.toolCalls) > 0 {
			tcs := make([][]byte, 0, len(st.toolCalls))
			for _, call := range st.toolCalls {
				item := ojson.NewDoc([]byte(`{"type":"function"}`))
				_ = item.Set("id", call.id)
				_ = item.Set("function.name", call.name)
				_ = item.Set("function.arguments", call.arguments.String())
				tcs = append(tcs, item.Bytes())
			}
			_ = msg.SetRawBytes("tool_calls", mustMarshalJSON(tcs))
		}
		choice := ojson.NewDoc([]byte(`{"index":0}`))
		_ = choice.Set("index", i)
		_ = choice.SetRawBytes("message", msg.Bytes())
		if finishReason != "" {
			_ = choice.Set("finish_reason", finishReason)
		}
		choiceList = append(choiceList, choice.Bytes())
	}
	_ = out.SetRawBytes("choices", mustMarshalJSON(choiceList))
	if usageRaw != "" {
		_ = out.SetRawBytes("usage", []byte(usageRaw))
	}
	return out.Bytes()
}

// mustMarshalJSON marshals a slice of raw-JSON byte slices into a JSON array.
// It joins the raw fragments so each element is embedded as an object rather
// than a base64 string.
func mustMarshalJSON(parts [][]byte) []byte {
	if len(parts) == 0 {
		return []byte("[]")
	}
	var b bytes.Buffer
	b.WriteByte('[')
	for i, part := range parts {
		if i > 0 {
			b.WriteByte(',')
		}
		b.Write(part)
	}
	b.WriteByte(']')
	return b.Bytes()
}
