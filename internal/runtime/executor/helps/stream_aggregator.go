package helps

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// AggregateOpenAIStreamChunks folds a sequence of OpenAI chat.completion.chunk
// JSON objects (one per line) into a single chat.completion JSON object. The
// OpenAI stream translator strips the SSE "data:" prefix, so the input here is
// raw JSON chunks. Content/reasoning deltas and tool-call fragments are
// accumulated per choice, then emitted as a non-streaming response carrying the
// final usage.
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
	// Choice state keyed by index.
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
			_ = msg.SetRawBytes("tool_calls", marshalRawJSONArray(tcs))
		}
		choice := ojson.NewDoc([]byte(`{"index":0}`))
		_ = choice.Set("index", i)
		_ = choice.SetRawBytes("message", msg.Bytes())
		if finishReason != "" {
			_ = choice.Set("finish_reason", finishReason)
		}
		choiceList = append(choiceList, choice.Bytes())
	}
	_ = out.SetRawBytes("choices", marshalRawJSONArray(choiceList))
	if usageRaw != "" {
		_ = out.SetRawBytes("usage", []byte(usageRaw))
	}
	return out.Bytes()
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
