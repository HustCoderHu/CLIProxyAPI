package helps

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// Request body constants.
//
// The official client builds its body from a 59 KB baseprompt.json template in
// which 74% of the bytes are agent tools this proxy never sends and 24% are a
// system prompt that clients replace. Only the ~1 KB protocol envelope is
// reproduced here; see research/01-baseprompt-fields.md for the field audit.
const (
	// qoderNeutralSystemPrompt is used when the client sends no system message.
	// The official 12 KB template embeds the maintainer's local paths, unrendered
	// placeholders and tools this proxy never sends, so it is not reproduced
	// (decision D8). Real clients always supply their own system prompt.
	qoderNeutralSystemPrompt = "You are a helpful assistant."

	// qoderDefaultMaxTokens mirrors the value inherited from the official
	// template. Omitting it changes upstream truncation behaviour, so it must
	// always be written.
	qoderDefaultMaxTokens = 32768

	// qoderDefaultMaxInputTokens is the reference fallback for models whose
	// catalog entry omits max_input_tokens.
	qoderDefaultMaxInputTokens = 180000

	// qoderNeutralBusinessName names the usage-panel conversation when the last
	// user prompt is empty.
	qoderNeutralBusinessName = "chat"

	// qoderBusinessNameLimit is how many leading characters of the last user
	// prompt become the usage-panel conversation name.
	qoderBusinessNameLimit = 30
)

// qoderEnvelopeFields are the protocol scalars inherited from the official
// template. None of them is derived from the request, and none has been
// validated by A/B testing, so they are kept verbatim (decision D7).
var qoderEnvelopeFields = []struct {
	path  string
	value any
}{
	{"chat_task", "FREE_INPUT"},
	{"code_language", ""},
	{"source", 1},
	{"version", "3"},
	{"chat_prompt", ""},
	{"session_type", "qodercli"},
	{"agent_id", "agent_common"},
	{"task_id", "common"},
	{"is_reply", true},
	{"is_retry", false},
	{"image_urls", nil},
}

// qoderDeepSeekModelKeys are the upstream catalog keys that route to DeepSeek
// models. DeepSeek keeps multi-turn consistency only when assistant turns carry
// reasoning_content, so these keys unlock that field.
var qoderDeepSeekModelKeys = map[string]struct{}{
	"dmodel":  {},
	"dfmodel": {},
}

// QoderCatalogMeta is the subset of an upstream catalog entry the request body
// needs. thinking_config and enable stay in the catalog layer: the former is
// mapped into registry.ModelInfo, the latter only decides whether a model is
// registered at all.
type QoderCatalogMeta struct {
	Key            string
	DisplayName    string
	IsVL           bool
	IsReasoning    bool
	MaxInputTokens int
}

// BuildQoderBodyDoc replaces the contents of doc with the upstream request body
// for payload, which must be an OpenAI chat-completions request.
//
// The builder owns the document: any previous contents are discarded, so calling
// it twice for the same input produces the same body. The result is plain JSON;
// the caller serializes it once and then feeds those exact bytes to QoderEncode,
// because the COSY signature covers the encoded body.
func BuildQoderBodyDoc(doc *ojson.Doc, payload []byte, meta QoderCatalogMeta, userType, sessionID string) error {
	if doc == nil {
		return errors.New("qoder body: nil document")
	}
	request := bytes.TrimSpace(payload)
	if len(request) == 0 {
		// An empty request object is the only sensible reading of no payload.
		request = []byte("{}")
	} else if !ojson.ValidBytes(request) {
		return errors.New("qoder body: payload is not valid JSON")
	}

	// ojson has no Clear and NewDoc(nil) yields an empty byte string rather than
	// "{}", so a fresh empty object is copied over the caller's value.
	*doc = *ojson.NewDocString("{}")

	modelName := ojson.GetBytesNoCopy(request, "model").String()
	keepReasoning := qoderIsDeepSeekModel(meta.Key, modelName)
	systemText, turns := qoderFlattenMessages(ojson.GetBytesNoCopy(request, "messages"), keepReasoning)
	if systemText == "" {
		systemText = qoderNeutralSystemPrompt
	}
	prompt := qoderLastUserPrompt(turns)

	// Envelope first, then the request-derived scalars, in the same order the
	// official template uses. That order is irrelevant to the protocol but makes
	// captured bodies diffable against the reference implementation.
	for _, field := range qoderEnvelopeFields {
		if err := qoderSetBodyField(doc, field.path, field.value); err != nil {
			return err
		}
	}

	requestID := qoderNewRequestID()
	conversationID := strings.TrimSpace(sessionID)
	recordID := conversationID
	if recordID == "" {
		// The usage panel groups a conversation by chat_record_id; without a
		// conversation identity the request id is the only stable choice.
		recordID = requestID
	}
	for _, field := range []struct {
		path  string
		value any
	}{
		{"request_id", requestID},
		{"request_set_id", qoderNewRequestID()},
		{"chat_record_id", recordID},
		{"stream", true},
		{"session_id", qoderNewRequestID()},
		{"aliyun_user_type", qoderUserType(userType)},
	} {
		if err := qoderSetBodyField(doc, field.path, field.value); err != nil {
			return err
		}
	}

	modelConfig := qoderModelConfig(meta)
	if err := qoderSetBodyField(doc, "model_config", modelConfig); err != nil {
		return err
	}
	if err := qoderSetBodyField(doc, "chat_context", qoderChatContext(prompt, modelConfig)); err != nil {
		return err
	}
	if err := qoderSetBodyField(doc, "parameters", qoderParameters(request)); err != nil {
		return err
	}

	messages := make([]map[string]any, 0, len(turns)+1)
	messages = append(messages, map[string]any{"role": "system", "content": systemText})
	messages = append(messages, turns...)
	if err := qoderSetBodyField(doc, "messages", messages); err != nil {
		return err
	}

	// A known conversation keeps its derived begin_at so every turn reports the
	// same grouping key; a standalone request uses the wall clock.
	beginAt := int64(0)
	if conversationID == "" {
		beginAt = cosyNowFunc().UnixMilli()
	}
	if err := qoderSetBodyField(doc, "business", qoderBusiness(prompt, meta.Key, conversationID, beginAt)); err != nil {
		return err
	}

	// Client tools pass through untouched. The official client template ships 14
	// agent tools that are always discarded, so an empty list is expected.
	tools := ojson.GetBytesNoCopy(request, "tools")
	if tools.IsArray() {
		if err := doc.SetRawBytes("tools", []byte(tools.Raw)); err != nil {
			return fmt.Errorf("qoder body: set tools: %w", err)
		}
	} else if err := qoderSetBodyField(doc, "tools", []any{}); err != nil {
		return err
	}
	return nil
}

// qoderSetBodyField sets one path and wraps the error with the path name.
func qoderSetBodyField(doc *ojson.Doc, path string, value any) error {
	if err := doc.Set(path, value); err != nil {
		return fmt.Errorf("qoder body: set %s: %w", path, err)
	}
	return nil
}

// qoderModelConfig renders the ten model_config keys the upstream requires. The
// same map is embedded in chat_context.extra.modelConfig.
func qoderModelConfig(meta QoderCatalogMeta) map[string]any {
	displayName := meta.DisplayName
	if displayName == "" {
		displayName = meta.Key
	}
	maxInputTokens := meta.MaxInputTokens
	if maxInputTokens <= 0 {
		maxInputTokens = qoderDefaultMaxInputTokens
	}
	return map[string]any{
		"key":              meta.Key,
		"display_name":     displayName,
		"model":            "",
		"format":           "openai",
		"is_vl":            meta.IsVL,
		"is_reasoning":     meta.IsReasoning,
		"api_key":          "",
		"url":              "",
		"source":           "system",
		"max_input_tokens": maxInputTokens,
	}
}

// qoderChatContext builds the highlight block. The two "type":"text"
// discriminators must be written explicitly: the reference implementation
// inherits them from the template, and an incomplete text node is rejected.
func qoderChatContext(prompt string, modelConfig map[string]any) map[string]any {
	return map[string]any{
		"chatPrompt": "",
		"text": map[string]any{
			"type": "text",
			"text": prompt,
		},
		"extra": map[string]any{
			"context": []any{},
			"originalContent": map[string]any{
				"type": "text",
				"text": prompt,
			},
			"modelConfig": modelConfig,
		},
		"features":  []any{},
		"imageUrls": nil,
	}
}

// qoderParameters builds the generation parameters. max_tokens always carries a
// value: the upstream truncation behaviour depends on the 32768 default that the
// reference implementation inherits from its template.
func qoderParameters(payload []byte) map[string]any {
	maxTokens := qoderMaxTokens(payload)
	parameters := map[string]any{"max_tokens": maxTokens}
	if effort := qoderRequestedEffort(payload); effort != "" {
		// The effort is forwarded verbatim. Level validation belongs to the
		// thinking pipeline, not to the body builder.
		parameters["reasoning_effort"] = effort
	}
	return parameters
}

// qoderMaxTokens applies the reference precedence: max_tokens, then
// max_completion_tokens, then the template default.
func qoderMaxTokens(payload []byte) int {
	for _, path := range []string{"max_tokens", "max_completion_tokens"} {
		if value := ojson.GetBytesNoCopy(payload, path).Int(); value > 0 {
			return int(value)
		}
	}
	return qoderDefaultMaxTokens
}

// qoderRequestedEffort extracts the raw client thinking level. The lookup order
// matches the reference implementation so request-body construction and usage
// accounting never disagree.
func qoderRequestedEffort(payload []byte) string {
	for _, path := range []string{"reasoning_effort", "reasoning.effort", "thinking.effort", "thinking.level"} {
		value := ojson.GetBytesNoCopy(payload, path)
		if value.Type == ojson.String && value.String() != "" {
			return value.String()
		}
	}
	return ""
}

// qoderBusiness builds the usage-panel conversation card. model_key is
// mandatory: without it the upstream silently bills the request against the
// "auto" model.
//
// Stability contract for business.id and business.begin_at: the usage panel
// groups turns by business.id and orders them by business.begin_at, so every
// turn of one conversation must report both unchanged. A per-turn id splits one
// agent turn into dozens of sub-minute conversations and burns the daily quota
// (upstream code 110). This proxy has no real conversation start time -- the
// reference gateway keeps one in its own session registry -- so when the caller
// knows the conversation it passes the id and beginAt is derived from that id
// alone (see qoderDerivedBeginAt). The absolute value is only a grouping key;
// stability and relative ordering are what matter.
//
// beginAt is used verbatim when positive. That is the "no conversation known"
// case, where the request stands alone and "now" is the correct answer.
func qoderBusiness(prompt, modelKey, conversationID string, beginAt int64) map[string]any {
	id := conversationID
	if id == "" {
		id = qoderNewRequestID()
	}
	if beginAt <= 0 {
		beginAt = qoderDerivedBeginAt(id)
	}
	return map[string]any{
		"id":        id,
		"name":      qoderBusinessName(prompt),
		"begin_at":  beginAt,
		"product":   "cli",
		"type":      "agent",
		"stage":     "start",
		"model_key": modelKey,
		"version":   cosyVersion,
	}
}

// Deterministic window for business.begin_at. The anchor is a fixed instant and
// the span covers the 30 days before it, so a conversation id always maps to the
// same millisecond regardless of when the request runs. Using the wall clock as
// the upper bound would make the value drift between turns and reintroduce the
// quota bug this derivation exists to avoid.
const (
	qoderBusinessBeginAtAnchorMs int64 = 1767225600000 // 2026-01-01T00:00:00Z
	qoderBusinessBeginAtSpanMs   int64 = 30 * 24 * 60 * 60 * 1000
)

// qoderDerivedBeginAt maps conversationID onto a fixed millisecond timestamp in
// the window [anchor-span, anchor]. The mapping depends on the id only, so two
// turns of one conversation agree byte for byte.
func qoderDerivedBeginAt(conversationID string) int64 {
	sum := sha256.Sum256([]byte(conversationID))
	offset := int64(binary.BigEndian.Uint64(sum[:8]) % uint64(qoderBusinessBeginAtSpanMs))
	return qoderBusinessBeginAtAnchorMs - offset
}

// qoderBusinessName truncates the prompt by characters, mirroring the reference
// behavior of slicing a Unicode string.
func qoderBusinessName(prompt string) string {
	if prompt == "" {
		return qoderNeutralBusinessName
	}
	runes := []rune(prompt)
	if len(runes) > qoderBusinessNameLimit {
		runes = runes[:qoderBusinessNameLimit]
	}
	return string(runes)
}

// qoderLastUserPrompt returns the content of the last non-empty user turn.
func qoderLastUserPrompt(turns []map[string]any) string {
	for i := len(turns) - 1; i >= 0; i-- {
		if turns[i]["role"] != "user" {
			continue
		}
		if content, _ := turns[i]["content"].(string); content != "" {
			return content
		}
	}
	return ""
}

// qoderIsDeepSeekModel reports whether the request targets a DeepSeek-family
// model, which is the only family that must keep reasoning_content on assistant
// turns. Both the upstream key and the client-supplied model name are checked:
// clients may send either the catalog key or the display name.
func qoderIsDeepSeekModel(modelKey, model string) bool {
	if _, ok := qoderDeepSeekModelKeys[strings.ToLower(strings.TrimSpace(modelKey))]; ok {
		return true
	}
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return false
	}
	if _, ok := qoderDeepSeekModelKeys[name]; ok {
		return true
	}
	return strings.Contains(name, "deepseek")
}

// qoderFlattenMessages converts the OpenAI messages array into the single system
// prompt plus the flattened turns the upstream accepts.
//
// Only the first non-empty system or developer text survives; later ones are
// dropped. system is returned empty when the client sent none, and the caller
// substitutes the neutral fallback.
func qoderFlattenMessages(messages ojson.Result, keepReasoning bool) (string, []map[string]any) {
	if !messages.IsArray() {
		return "", nil
	}
	elements := messages.Array()
	turns := make([]map[string]any, 0, len(elements))
	systemText := ""
	for _, message := range elements {
		if !message.IsObject() {
			continue
		}
		role := message.Get("role").String()
		if role == "developer" {
			// OpenAI's developer role is the upstream's system role.
			role = "system"
		}
		text := qoderFlattenContentText(message.Get("content"))
		switch role {
		case "":
			// A message without a role cannot be placed; drop it.
			continue
		case "system":
			if systemText == "" && text != "" {
				systemText = text
			}
		case "tool":
			// The upstream drops the whole assistant turn when a tool result
			// carries null content, so content is always a string.
			turns = append(turns, map[string]any{
				"role":         "tool",
				"tool_call_id": message.Get("tool_call_id").String(),
				"content":      text,
			})
		case "assistant":
			turn := map[string]any{"role": "assistant", "content": text}
			if calls := qoderFlattenToolCalls(message.Get("tool_calls")); len(calls) > 0 {
				turn["tool_calls"] = calls
			}
			qoderKeepReasoningContent(turn, message, keepReasoning)
			turns = append(turns, turn)
		case "user":
			turns = append(turns, map[string]any{"role": "user", "content": text})
		default:
			// Unknown roles degrade to user rather than disappearing.
			turns = append(turns, map[string]any{"role": "user", "content": text})
		}
	}
	return systemText, turns
}

// qoderKeepReasoningContent copies reasoning_content onto an assistant turn when
// the model needs it. A null value is treated as absent.
func qoderKeepReasoningContent(turn map[string]any, message ojson.Result, keepReasoning bool) {
	if !keepReasoning {
		return
	}
	reasoning := message.Get("reasoning_content")
	if !reasoning.Exists() || reasoning.Type == ojson.Null {
		return
	}
	turn["reasoning_content"] = reasoning.String()
}

// qoderFlattenToolCalls normalizes assistant tool_calls into the upstream shape.
// arguments always becomes a JSON string: a client that sent an object or an
// array would otherwise produce a nested JSON value the upstream rejects.
func qoderFlattenToolCalls(toolCalls ojson.Result) []map[string]any {
	if !toolCalls.IsArray() {
		return nil
	}
	elements := toolCalls.Array()
	calls := make([]map[string]any, 0, len(elements))
	for _, call := range elements {
		if !call.IsObject() {
			// A malformed entry cannot be normalized into a valid tool call.
			continue
		}
		function := call.Get("function")
		callType := call.Get("type").String()
		if callType == "" {
			callType = "function"
		}
		calls = append(calls, map[string]any{
			"id":   call.Get("id").String(),
			"type": callType,
			"function": map[string]any{
				"name":      function.Get("name").String(),
				"arguments": qoderToolCallArguments(function.Get("arguments")),
			},
		})
	}
	return calls
}

// qoderToolCallArguments renders tool call arguments as a string.
func qoderToolCallArguments(arguments ojson.Result) string {
	switch {
	case !arguments.Exists(), arguments.Type == ojson.Null:
		return ""
	case arguments.Type == ojson.String:
		return arguments.String()
	default:
		// Objects and arrays keep their exact bytes, which keeps the payload
		// byte-stable across turns; scalars use their JSON text form.
		return arguments.Raw
	}
}

// qoderFlattenContentText reduces a message content value to plain text.
// String contents pass through; arrays of parts are joined with newlines; image
// parts contribute nothing, because this port sends text only.
func qoderFlattenContentText(content ojson.Result) string {
	if !content.Exists() {
		return ""
	}
	switch {
	case content.Type == ojson.String:
		return content.String()
	case content.IsArray():
		parts := make([]string, 0, len(content.Array()))
		for _, piece := range content.Array() {
			if text, ok := qoderContentPartText(piece); ok && text != "" {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case content.IsObject():
		text, _ := qoderContentPartText(content)
		return text
	case content.Type == ojson.Null:
		return ""
	default:
		return content.String()
	}
}

// qoderContentPartText extracts the text of one structured content part.
func qoderContentPartText(piece ojson.Result) (string, bool) {
	if piece.Type == ojson.String {
		return piece.String(), true
	}
	if !piece.IsObject() {
		return "", false
	}
	switch piece.Get("type").String() {
	case "text", "input_text", "output_text", "summary_text":
		return piece.Get("text").String(), true
	}
	return "", false
}
