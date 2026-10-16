package helps

import (
	_ "embed"
	"encoding/json"
	"strconv"
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
)

//go:embed opencode_tools.json
var openCodeToolsJSON []byte

type openCodeToolDef struct {
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

var (
	openCodeToolsOnce sync.Once
	openCodeToolsMap  map[string]openCodeToolDef
	openCodeCoreTools = []string{"shell", "read", "edit", "glob", "grep"}
)

func initOpenCodeTools() {
	openCodeToolsOnce.Do(func() {
		openCodeToolsMap = make(map[string]openCodeToolDef)
		_ = json.Unmarshal(openCodeToolsJSON, &openCodeToolsMap)
	})
}

func formatOpenAIFunctionTool(name string, def openCodeToolDef) map[string]any {
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": def.Description,
			"parameters":  def.Parameters,
		},
	}
}

// EnsureOpenCodeAnonymousTools ensures all 5 core tools are present in payload
// with exact official schemas, while preserving any Claude Code / custom tools.
func EnsureOpenCodeAnonymousTools(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	EnsureOpenCodeAnonymousToolsDoc(doc)
	return doc.Bytes()
}

// EnsureOpenCodeAnonymousToolsDoc ensures all 5 core tools are present in the
// document with exact official schemas, while preserving any Claude Code or
// custom tools. It reports whether the document changed.
func EnsureOpenCodeAnonymousToolsDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	initOpenCodeTools()
	toolsResult := doc.Get("tools")

	var items []any
	present := make(map[string]int) // tool name -> slice index

	if toolsResult.Exists() && toolsResult.IsArray() {
		for _, res := range toolsResult.Array() {
			var m map[string]any
			if err := json.Unmarshal([]byte(res.Raw), &m); err != nil {
				continue
			}
			name := res.Get("function.name").String()
			if name == "" {
				name = res.Get("name").String()
			}
			if name != "" {
				present[name] = len(items)
			}
			items = append(items, m)
		}
	}

	changed := false
	// 1. Replace existing core tools with canonical schema if present.
	for _, name := range openCodeCoreTools {
		if idx, ok := present[name]; ok {
			if def, known := openCodeToolsMap[name]; known {
				items[idx] = formatOpenAIFunctionTool(name, def)
				changed = true
			}
		}
	}

	// 2. Append missing core tools.
	for _, name := range openCodeCoreTools {
		if _, ok := present[name]; !ok {
			if def, known := openCodeToolsMap[name]; known {
				items = append(items, formatOpenAIFunctionTool(name, def))
				changed = true
			}
		}
	}

	if !changed && toolsResult.Exists() {
		return false
	}
	if err := doc.Set("tools", items); err != nil {
		return false
	}
	return true
}

// NormalizeOpenCodeToolCallsInChatCompletion maps upstream tool calls back to
// Claude Code conventions (e.g. shell -> Bash, read -> Read with path -> file_path).
func NormalizeOpenCodeToolCallsInChatCompletion(payload []byte) []byte {
	doc := ojson.NewDoc(payload)
	NormalizeOpenCodeToolCallsInChatCompletionDoc(doc)
	return doc.Bytes()
}

// NormalizeOpenCodeToolCallsInChatCompletionDoc maps upstream tool calls in a
// chat.completion document back to Claude Code conventions. It reports whether
// the document changed.
func NormalizeOpenCodeToolCallsInChatCompletionDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	choices := doc.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return false
	}
	changed := false
	for choiceIdx, choice := range choices.Array() {
		toolCalls := choice.Get("message.tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() {
			continue
		}
		for tcIdx, tc := range toolCalls.Array() {
			name := tc.Get("function.name").String()
			pathPrefix := "choices." + strconv.Itoa(choiceIdx) + ".message.tool_calls." + strconv.Itoa(tcIdx)
			switch strings.ToLower(name) {
			case "shell":
				_ = doc.Set(pathPrefix+".function.name", "Bash")
				changed = true
			case "read":
				_ = doc.Set(pathPrefix+".function.name", "Read")
				argsRaw := tc.Get("function.arguments").String()
				if argsPath := ojson.Get(argsRaw, "path"); argsPath.Exists() && !ojson.Get(argsRaw, "file_path").Exists() {
					argsDoc := ojson.NewDocString(argsRaw)
					if err := argsDoc.Set("file_path", argsPath.Value()); err == nil {
						_ = doc.SetRawBytes(pathPrefix+".function.arguments", argsDoc.Bytes())
					}
				}
				changed = true
			case "edit":
				_ = doc.Set(pathPrefix+".function.name", "Edit")
				changed = true
			case "glob":
				_ = doc.Set(pathPrefix+".function.name", "Glob")
				changed = true
			case "grep":
				_ = doc.Set(pathPrefix+".function.name", "Grep")
				changed = true
			}
		}
	}
	return changed
}

// NormalizeOpenCodeToolCallChunk normalizes tool_call names in SSE chunk deltas.
func NormalizeOpenCodeToolCallChunk(chunk []byte) []byte {
	doc := ojson.NewDoc(chunk)
	NormalizeOpenCodeToolCallChunkDoc(doc)
	return doc.Bytes()
}

// NormalizeOpenCodeToolCallChunkDoc normalizes tool_call names in SSE chunk
// deltas. It reports whether the document changed.
func NormalizeOpenCodeToolCallChunkDoc(doc *ojson.Doc) bool {
	if doc == nil {
		return false
	}
	choices := doc.Get("choices")
	if !choices.Exists() || !choices.IsArray() {
		return false
	}
	changed := false
	for choiceIdx, choice := range choices.Array() {
		toolCalls := choice.Get("delta.tool_calls")
		if !toolCalls.Exists() || !toolCalls.IsArray() {
			continue
		}
		for tcIdx, tc := range toolCalls.Array() {
			name := tc.Get("function.name").String()
			if name == "" {
				continue
			}
			pathPrefix := "choices." + strconv.Itoa(choiceIdx) + ".delta.tool_calls." + strconv.Itoa(tcIdx)
			switch strings.ToLower(name) {
			case "shell":
				_ = doc.Set(pathPrefix+".function.name", "Bash")
				changed = true
			case "read":
				_ = doc.Set(pathPrefix+".function.name", "Read")
				changed = true
			case "edit":
				_ = doc.Set(pathPrefix+".function.name", "Edit")
				changed = true
			case "glob":
				_ = doc.Set(pathPrefix+".function.name", "Glob")
				changed = true
			case "grep":
				_ = doc.Set(pathPrefix+".function.name", "Grep")
				changed = true
			}
		}
	}
	return changed
}
