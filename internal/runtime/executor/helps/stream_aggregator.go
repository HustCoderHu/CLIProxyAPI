package helps

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AggregateOpenAIStreamChunks folds a sequence of OpenAI chat.completion.chunk
// JSON objects (one per line) into a single chat.completion JSON object. The
// OpenAI stream translator strips the SSE "data:" prefix, so the input here is
// raw JSON chunks. We accumulate content/reasoning deltas per choice and
// tool-call fragments, then emit a non-streaming response with the final usage.
func AggregateOpenAIStreamChunks(raw []byte) []byte {
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
		root := gjson.ParseBytes(trimmed)
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
		choicesResult.ForEach(func(_, choice gjson.Result) bool {
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
			delta.Get("tool_calls").ForEach(func(_, tc gjson.Result) bool {
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
				return true
			})
			return true
		})
	}

	out := []byte(`{"id":"","object":"chat.completion","created":0,"model":"","choices":[]}`)
	out, _ = sjson.SetBytes(out, "id", id)
	out, _ = sjson.SetBytes(out, "model", model)
	out, _ = sjson.SetBytes(out, "created", created)

	var choiceList [][]byte
	for i := int64(0); i <= maxIndex; i++ {
		st := choices[i]
		if st == nil {
			continue
		}
		msg := []byte(`{"role":"assistant","content":""}`)
		msg, _ = sjson.SetBytes(msg, "content", st.content.String())
		if st.reasoning.Len() > 0 {
			msg, _ = sjson.SetBytes(msg, "reasoning_content", st.reasoning.String())
		}
		if len(st.toolCalls) > 0 {
			var tcs [][]byte
			for _, call := range st.toolCalls {
				item := []byte(`{"id":"","type":"function","function":{"name":"","arguments":""}}`)
				item, _ = sjson.SetBytes(item, "id", call.id)
				item, _ = sjson.SetBytes(item, "function.name", call.name)
				item, _ = sjson.SetBytes(item, "function.arguments", call.arguments.String())
				tcs = append(tcs, item)
			}
			msg, _ = sjson.SetBytes(msg, "tool_calls", json.RawMessage(marshalRawJSONArray(tcs)))
		}
		choice := []byte(`{"index":0,"message":{},"finish_reason":""}`)
		choice, _ = sjson.SetBytes(choice, "index", i)
		choice, _ = sjson.SetRawBytes(choice, "message", msg)
		if finishReason != "" {
			choice, _ = sjson.SetBytes(choice, "finish_reason", finishReason)
		}
		choiceList = append(choiceList, choice)
	}
	out, _ = sjson.SetBytes(out, "choices", json.RawMessage(marshalRawJSONArray(choiceList)))
	if usageRaw != "" {
		out, _ = sjson.SetRawBytes(out, "usage", []byte(usageRaw))
	}
	return out
}

func marshalRawJSONArray(parts [][]byte) []byte {
	if len(parts) == 0 {
		return []byte("[]")
	}
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, part := range parts {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(part)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}
