package helps

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
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
	openCodeToolsMu   sync.RWMutex
	openCodeToolsMap  map[string]openCodeToolDef
	openCodeCoreTools = []string{"shell", "read", "write", "edit", "glob", "grep"}
)

func initOpenCodeTools() {
	openCodeToolsOnce.Do(func() {
		loadOpenCodeTools()
	})
}

func loadOpenCodeTools() {
	openCodeToolsMu.Lock()
	defer openCodeToolsMu.Unlock()
	openCodeToolsMap = make(map[string]openCodeToolDef)

	const externalFile = "opencode_tools.json"
	if data, err := os.ReadFile(externalFile); err == nil && len(data) > 0 {
		var custom map[string]openCodeToolDef
		if errUnmarshal := json.Unmarshal(data, &custom); errUnmarshal == nil && len(custom) > 0 {
			openCodeToolsMap = custom
			return
		}
	}

	_ = json.Unmarshal(openCodeToolsJSON, &openCodeToolsMap)
}

// UpdateOpenCodeTools hot-reloads the tool schemas at runtime.
func UpdateOpenCodeTools(data []byte) error {
	var newTools map[string]openCodeToolDef
	if err := json.Unmarshal(data, &newTools); err != nil {
		return err
	}
	if len(newTools) == 0 {
		return errors.New("empty tool definitions")
	}
	initOpenCodeTools()
	openCodeToolsMu.Lock()
	defer openCodeToolsMu.Unlock()
	openCodeToolsMap = newTools
	return nil
}

// ResetOpenCodeTools restores the embedded tool definitions. Test use only.
func ResetOpenCodeTools() {
	openCodeToolsMu.Lock()
	defer openCodeToolsMu.Unlock()
	openCodeToolsMap = make(map[string]openCodeToolDef)
	_ = json.Unmarshal(openCodeToolsJSON, &openCodeToolsMap)
}

// GetOpenCodeTools returns a copy of the current tool definitions.
func GetOpenCodeTools() map[string]openCodeToolDef {
	initOpenCodeTools()
	openCodeToolsMu.RLock()
	defer openCodeToolsMu.RUnlock()
	res := make(map[string]openCodeToolDef, len(openCodeToolsMap))
	for k, v := range openCodeToolsMap {
		res[k] = v
	}
	return res
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

// EnsureOpenCodeAnonymousTools ensures all 6 core tools are present in payload
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
	openCodeToolsMu.RLock()
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
	openCodeToolsMu.RUnlock()

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
				renameOpenCodeToolArgPathToFilePath(doc, pathPrefix, tc)
				changed = true
			case "write":
				_ = doc.Set(pathPrefix+".function.name", "Write")
				renameOpenCodeToolArgPathToFilePath(doc, pathPrefix, tc)
				changed = true
			case "edit":
				_ = doc.Set(pathPrefix+".function.name", "Edit")
				renameOpenCodeToolArgPathToFilePath(doc, pathPrefix, tc)
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

// renameOpenCodeToolArgPathToFilePath copies a tool call's "path" argument into
// "file_path" when the latter is absent, matching Claude Code's Read/Write/Edit
// argument schema.
func renameOpenCodeToolArgPathToFilePath(doc *ojson.Doc, pathPrefix string, tc ojson.Result) {
	argsRaw := tc.Get("function.arguments").String()
	argsPath := ojson.Get(argsRaw, "path")
	if !argsPath.Exists() || ojson.Get(argsRaw, "file_path").Exists() {
		return
	}
	argsDoc := ojson.NewDocString(argsRaw)
	if err := argsDoc.Set("file_path", argsPath.Value()); err != nil {
		return
	}
	_ = doc.SetRawBytes(pathPrefix+".function.arguments", argsDoc.Bytes())
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
			case "write":
				_ = doc.Set(pathPrefix+".function.name", "Write")
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
