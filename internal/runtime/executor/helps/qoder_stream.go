package helps

import (
	"bytes"
	"fmt"
	"strconv"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

// QoderStreamStatusError reports a non-200 statusCodeValue carried inside an
// upstream SSE envelope. The gateway answers the HTTP handshake with 200 and
// delivers business failures (403 queueing, quota exhaustion, provider faults)
// inside the first frame, so the status only exists here.
type QoderStreamStatusError struct {
	Status int
	Body   string
}

// Error implements error.
func (e *QoderStreamStatusError) Error() string {
	if e == nil {
		return "qoder stream: status error"
	}
	return fmt.Sprintf("qoder stream: upstream status %d: %s", e.Status, e.Body)
}

// qoderStatusErrorBodyLimit bounds the error detail echoed back to the caller.
const qoderStatusErrorBodyLimit = 400

// qoderNoiseDeltaKeys are the empty placeholders the gateway pads deltas with.
var qoderNoiseDeltaKeys = []string{"extra_fields", "refusal", "reasoning_content"}

// DecodeQoderStreamFrame decodes one upstream SSE data payload.
//
// The upstream frame shape is
//
//	data:{"headers":{...},"body":"<inner OpenAI chunk JSON string>","statusCodeValue":200}
//	data:{"body":"[DONE]"}
//
// inner is the inner chunk JSON, or nil when the frame carries nothing usable.
// status is the envelope statusCodeValue, or 0 when the envelope omits it. done
// reports that the inner body was "[DONE]". err is non-nil only for a non-200
// status or an unparseable status value.
func DecodeQoderStreamFrame(data []byte) (inner []byte, status int, done bool, err error) {
	frame := qoderStripDataPrefix(data)
	if len(frame) == 0 || frame[0] != '{' || !ojson.ValidBytes(frame) {
		return nil, 0, false, nil
	}

	status, errStatus := qoderFrameStatus(frame)
	if errStatus != nil {
		return nil, status, false, errStatus
	}

	body := ojson.GetBytesNoCopy(frame, "body")
	if body.Type != ojson.String {
		// Frames without a body carry only headers or event metadata.
		return nil, status, false, nil
	}
	content := []byte(body.String())
	if len(content) == 0 {
		return nil, status, false, nil
	}
	if bytes.Equal(content, []byte("[DONE]")) {
		return nil, status, true, nil
	}
	if !ojson.ValidBytes(content) {
		// The reference implementation skips undecodable inner bodies; the
		// envelope status is the authoritative error channel.
		return nil, status, false, nil
	}
	return content, status, false, nil
}

// qoderStripDataPrefix trims SSE framing from line. Comment lines, the "data:"
// prefix (repeated, as the reference implementation handles multi-prefixed
// lines) and surrounding whitespace are removed.
func qoderStripDataPrefix(line []byte) []byte {
	trimmed := bytes.TrimSpace(line)
	if len(trimmed) == 0 || trimmed[0] == ':' {
		return nil
	}
	for bytes.HasPrefix(trimmed, []byte("data:")) {
		trimmed = bytes.TrimSpace(trimmed[len("data:"):])
	}
	if len(trimmed) == 0 || trimmed[0] == ':' {
		return nil
	}
	return trimmed
}

// qoderFrameStatus reads statusCodeValue, which the gateway sends as either a
// number or a string. A missing or null value yields 0, meaning "no status".
// A non-empty status other than 200 becomes a QoderStreamStatusError.
func qoderFrameStatus(frame []byte) (int, error) {
	value := ojson.GetBytesNoCopy(frame, "statusCodeValue")
	if !value.Exists() || value.Type == ojson.Null {
		return 0, nil
	}

	status := 0
	switch value.Type {
	case ojson.Number:
		status = int(value.Int())
	case ojson.String:
		parsed, errParse := strconv.Atoi(value.String())
		if errParse != nil {
			// The reference implementation maps an unparseable status to 502
			// rather than guessing.
			return 502, qoderStatusError(frame, 502)
		}
		status = parsed
	default:
		return 502, qoderStatusError(frame, 502)
	}

	if status == 0 || status == 200 {
		return status, nil
	}
	return status, qoderStatusError(frame, status)
}

// qoderStatusError builds the error for a failed frame, echoing the raw envelope
// so the caller can classify the failure.
func qoderStatusError(frame []byte, status int) error {
	return &QoderStreamStatusError{Status: status, Body: qoderTruncateUTF8(string(frame), qoderStatusErrorBodyLimit)}
}

// qoderTruncateUTF8 cuts s to at most limit bytes without splitting a rune.
func qoderTruncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// CleanQoderChunk trims the empty noise the gateway pads deltas with. The
// document is edited in place; changed reports whether anything was removed so
// the caller can skip re-serialization, and drop reports that the whole chunk
// should be discarded.
//
// Usage cache counters are normalized at the same time: the upstream switches
// the field it reports by model family, so a client reading the standard
// prompt_tokens_details.cached_tokens would otherwise always see zero.
func CleanQoderChunk(doc *ojson.Doc) (changed bool, drop bool) {
	if doc == nil || doc.Len() == 0 {
		return false, false
	}

	changed = qoderNormalizeUsageCache(doc)

	choices := doc.Get("choices")
	if !choices.IsArray() {
		return changed, false
	}
	// The reference implementation returns on the first empty delta, which for
	// its single-choice upstream is the same thing. Deciding across all choices
	// avoids discarding a sibling choice's content when a client asks for n > 1.
	// A chunk with no choices at all (a usage-only tail) is always kept.
	choiceList := choices.Array()
	keepChunk := len(choiceList) == 0
	for choiceIdx := range choiceList {
		choicePath := "choices." + strconv.Itoa(choiceIdx)
		delta := doc.Get(choicePath + ".delta")
		if !delta.IsObject() {
			// A chunk without a delta (usage tail, role announcement) is never
			// dropped here.
			keepChunk = true
			continue
		}
		deltaPath := choicePath + ".delta"

		if functionCall := doc.Get(deltaPath + ".function_call"); functionCall.Exists() && qoderEmptyFunctionCall(functionCall) {
			if errDelete := doc.Delete(deltaPath + ".function_call"); errDelete == nil {
				changed = true
			}
		}
		if toolCalls := doc.Get(deltaPath + ".tool_calls"); toolCalls.IsArray() && len(toolCalls.Array()) == 0 {
			if errDelete := doc.Delete(deltaPath + ".tool_calls"); errDelete == nil {
				changed = true
			}
		}
		for _, key := range qoderNoiseDeltaKeys {
			value := doc.Get(deltaPath + "." + key)
			if !value.Exists() || !qoderEmptyJSONValue(value) {
				continue
			}
			if errDelete := doc.Delete(deltaPath + "." + key); errDelete == nil {
				changed = true
			}
		}

		if len(doc.Get(deltaPath).Map()) > 0 {
			keepChunk = true
			continue
		}
		if finishReason := doc.Get(choicePath + ".finish_reason"); finishReason.Type == ojson.String && finishReason.String() != "" {
			keepChunk = true
		}
	}
	return changed, !keepChunk
}

// qoderEmptyFunctionCall reports whether a function_call placeholder carries no
// tool name. A non-object value cannot name a tool either.
func qoderEmptyFunctionCall(functionCall ojson.Result) bool {
	if !functionCall.IsObject() {
		return true
	}
	return functionCall.Get("name").String() == ""
}

// qoderEmptyJSONValue mirrors Python truthiness: an empty string, null, zero,
// false, an empty object or an empty array counts as empty.
func qoderEmptyJSONValue(value ojson.Result) bool {
	switch {
	case value.Type == ojson.Null:
		return true
	case value.Type == ojson.False:
		return true
	case value.Type == ojson.True:
		return false
	case value.Type == ojson.Number:
		return value.Num == 0
	case value.Type == ojson.String:
		return value.String() == ""
	case value.IsObject():
		return len(value.Map()) == 0
	case value.IsArray():
		return len(value.Array()) == 0
	}
	return false
}

// qoderUsageCacheFallbacks are the upstream-specific counters that carry the
// same number as the OpenAI-standard prompt_tokens_details.cached_tokens.
var qoderUsageCacheFallbacks = []string{
	"cached_tokens",
	"prompt_cache_hit_tokens",
	"completion_tokens_details.cached_tokens",
}

// qoderNormalizeUsageCache writes the upstream cache counter into the standard
// prompt_tokens_details.cached_tokens slot. The original fields are left in
// place so clients already adapted to the upstream shape keep working.
func qoderNormalizeUsageCache(doc *ojson.Doc) bool {
	usage := doc.Get("usage")
	if !usage.IsObject() {
		return false
	}

	cached := usage.Get("prompt_tokens_details.cached_tokens").Int()
	if cached <= 0 {
		for _, path := range qoderUsageCacheFallbacks {
			if value := usage.Get(path).Int(); value > 0 {
				cached = value
				break
			}
		}
	}
	if cached <= 0 {
		return false
	}
	if usage.Get("prompt_tokens_details.cached_tokens").Int() == cached {
		// Already reported in the standard slot.
		return false
	}
	if errSet := doc.Set("usage.prompt_tokens_details.cached_tokens", cached); errSet != nil {
		return false
	}
	return true
}
