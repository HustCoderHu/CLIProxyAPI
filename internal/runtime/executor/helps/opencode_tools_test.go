package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEnsureOpenCodeAnonymousTools_Empty(t *testing.T) {
	input := []byte(`{"model":"x-preview-f-free","messages":[{"role":"user","content":"hi"}]}`)
	output := EnsureOpenCodeAnonymousTools(input)

	tools := gjson.GetBytes(output, "tools")
	if !tools.Exists() || !tools.IsArray() {
		t.Fatalf("tools array missing in output: %s", output)
	}
	if len(tools.Array()) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools.Array()))
	}

	names := make(map[string]bool)
	for _, tool := range tools.Array() {
		name := tool.Get("function.name").String()
		names[name] = true
		desc := tool.Get("function.description").String()
		if desc == "" {
			t.Fatalf("tool %q missing description", name)
		}
	}
	for _, expected := range []string{"shell", "read", "edit", "glob", "grep"} {
		if !names[expected] {
			t.Errorf("missing core tool: %s", expected)
		}
	}
}

func TestEnsureOpenCodeAnonymousTools_PreserveClaudeTools(t *testing.T) {
	input := []byte(`{"model":"x-preview-f-free","tools":[{"type":"function","function":{"name":"Bash","description":"run cmd","parameters":{"type":"object"}}}]}`)
	output := EnsureOpenCodeAnonymousTools(input)

	tools := gjson.GetBytes(output, "tools").Array()
	// Should have Bash + 5 core tools = 6 tools
	if len(tools) != 6 {
		t.Fatalf("expected 6 tools (1 custom + 5 core), got %d: %s", len(tools), output)
	}

	names := make(map[string]bool)
	for _, tool := range tools {
		names[tool.Get("function.name").String()] = true
	}
	if !names["Bash"] {
		t.Errorf("expected custom tool 'Bash' to be preserved")
	}
	for _, expected := range []string{"shell", "read", "edit", "glob", "grep"} {
		if !names[expected] {
			t.Errorf("missing core tool: %s", expected)
		}
	}
}

func TestEnsureOpenCodeAnonymousTools_OverrideStubs(t *testing.T) {
	input := []byte(`{"model":"x-preview-f-free","tools":[{"type":"function","function":{"name":"read","description":"dummy stub","parameters":{"type":"object"}}}]}`)
	output := EnsureOpenCodeAnonymousTools(input)

	tools := gjson.GetBytes(output, "tools").Array()
	if len(tools) != 5 {
		t.Fatalf("expected 5 tools, got %d", len(tools))
	}
	for _, tool := range tools {
		if tool.Get("function.name").String() == "read" {
			desc := tool.Get("function.description").String()
			if desc == "dummy stub" {
				t.Fatalf("stub was not replaced with canonical schema")
			}
		}
	}
}

func TestNormalizeOpenCodeToolCallsInChatCompletion(t *testing.T) {
	raw := []byte(`{
		"id": "chatcmpl-1",
		"choices": [{
			"index": 0,
			"message": {
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_1",
						"type": "function",
						"function": {
							"name": "shell",
							"arguments": "{\"command\":\"ls -la\"}"
						}
					},
					{
						"id": "call_2",
						"type": "function",
						"function": {
							"name": "read",
							"arguments": "{\"path\":\"main.go\",\"offset\":1}"
						}
					}
				]
			}
		}]
	}`)

	normalized := NormalizeOpenCodeToolCallsInChatCompletion(raw)

	tc0Name := gjson.GetBytes(normalized, "choices.0.message.tool_calls.0.function.name").String()
	if tc0Name != "Bash" {
		t.Fatalf("expected shell -> Bash, got %s", tc0Name)
	}

	tc1Name := gjson.GetBytes(normalized, "choices.0.message.tool_calls.1.function.name").String()
	if tc1Name != "Read" {
		t.Fatalf("expected read -> Read, got %s", tc1Name)
	}

	tc1Args := gjson.GetBytes(normalized, "choices.0.message.tool_calls.1.function.arguments").String()
	if gjson.Get(tc1Args, "file_path").String() != "main.go" {
		t.Fatalf("expected path to be mapped to file_path in arguments, got %s", tc1Args)
	}
}

func TestNormalizeOpenCodeToolCallChunk(t *testing.T) {
	chunk := []byte(`{
		"choices": [{
			"index": 0,
			"delta": {
				"tool_calls": [{
					"index": 0,
					"function": {
						"name": "shell"
					}
				}]
			}
		}]
	}`)

	normalized := NormalizeOpenCodeToolCallChunk(chunk)
	name := gjson.GetBytes(normalized, "choices.0.delta.tool_calls.0.function.name").String()
	if name != "Bash" {
		t.Fatalf("expected shell -> Bash in chunk, got %s", name)
	}
}
