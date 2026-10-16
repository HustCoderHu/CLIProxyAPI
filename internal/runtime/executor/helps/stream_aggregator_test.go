package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestAggregateOpenAIStreamChunks(t *testing.T) {
	raw := []byte(`{"id":"cmb-1","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":"hello","reasoning_content":""},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-1","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"role":"assistant","content":" world","reasoning_content":""},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-1","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":2,"total_tokens":4}}` + "\n")

	got := AggregateOpenAIStreamChunks(raw)

	if obj := gjson.GetBytes(got, "object").String(); obj != "chat.completion" {
		t.Fatalf("object=%q, want chat.completion (body=%s)", obj, got)
	}
	if content := gjson.GetBytes(got, "choices.0.message.content").String(); content != "hello world" {
		t.Fatalf("content=%q, want %q (body=%s)", content, "hello world", got)
	}
	if fr := gjson.GetBytes(got, "choices.0.finish_reason").String(); fr != "stop" {
		t.Fatalf("finish_reason=%q, want stop (body=%s)", fr, got)
	}
	if u := gjson.GetBytes(got, "usage.total_tokens").Int(); u != 4 {
		t.Fatalf("usage.total_tokens=%d, want 4 (body=%s)", u, got)
	}
}

func TestAggregateOpenAIStreamChunksToolCalls(t *testing.T) {
	raw := []byte(`{"id":"cmb-2","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"get_weather","arguments":"{\"city\":"}}]},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-2","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"SF\"}"}}]},"finish_reason":""}],"usage":null}` + "\n" +
		`{"id":"cmb-2","model":"glm-5.2","object":"chat.completion.chunk","created":123,"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":null}` + "\n")

	got := AggregateOpenAIStreamChunks(raw)

	name := gjson.GetBytes(got, "choices.0.message.tool_calls.0.function.name").String()
	if name != "get_weather" {
		t.Fatalf("tool name=%q, want get_weather (body=%s)", name, got)
	}
	args := gjson.GetBytes(got, "choices.0.message.tool_calls.0.function.arguments").String()
	if args != `{"city":"SF"}` {
		t.Fatalf("tool args=%q, want merged JSON (body=%s)", args, got)
	}
	if fr := gjson.GetBytes(got, "choices.0.finish_reason").String(); fr != "tool_calls" {
		t.Fatalf("finish_reason=%q, want tool_calls (body=%s)", fr, got)
	}
}
