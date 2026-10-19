package helps

import (
	_ "embed"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
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

// UpdateOpenCodeTools 允许在运行时动态热重载工具 Schema
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

// ResetOpenCodeTools 重置工具定义回内置嵌入数据，供测试使用
func ResetOpenCodeTools() {
	openCodeToolsMu.Lock()
	defer openCodeToolsMu.Unlock()
	openCodeToolsMap = make(map[string]openCodeToolDef)
	_ = json.Unmarshal(openCodeToolsJSON, &openCodeToolsMap)
}

// GetOpenCodeTools 返回当前工具定义的副本
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
	initOpenCodeTools()
	toolsResult := gjson.GetBytes(payload, "tools")

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
	// 1. Replace existing core tools with canonical schema if present
	for _, name := range openCodeCoreTools {
		if idx, ok := present[name]; ok {
			if def, known := openCodeToolsMap[name]; known {
				items[idx] = formatOpenAIFunctionTool(name, def)
				changed = true
			}
		}
	}

	// 2. Append missing core tools
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
		return payload
	}

	updated, err := sjson.SetBytes(payload, "tools", items)
	if err != nil {
		return payload
	}
	return updated
}

// NormalizeOpenCodeToolCallsInChatCompletion maps upstream tool calls back to
// Claude Code conventions (e.g. shell -> Bash, read -> Read with path -> file_path).
func NormalizeOpenCodeToolCallsInChatCompletion(payload []byte) []byte {
	choices := gjson.GetBytes(payload, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return payload
	}
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
				payload, _ = sjson.SetBytes(payload, pathPrefix+".function.name", "Bash")
			case "read":
				payload, _ = sjson.SetBytes(payload, pathPrefix+".function.name", "Read")
				argsRaw := tc.Get("function.arguments").String()
				if gjson.Get(argsRaw, "path").Exists() && !gjson.Get(argsRaw, "file_path").Exists() {
					if updatedArgs, err := sjson.Set(argsRaw, "file_path", gjson.Get(argsRaw, "path").Value()); err == nil {
						payload, _ = sjson.SetBytes(payload, pathPrefix+".function.arguments", updatedArgs)
					}
				}
			case "write":
				payload, _ = sjson.SetBytes(payload, pathPrefix+".function.name", "Write")
				argsRaw := tc.Get("function.arguments").String()
				if gjson.Get(argsRaw, "path").Exists() && !gjson.Get(argsRaw, "file_path").Exists() {
					if updatedArgs, err := sjson.Set(argsRaw, "file_path", gjson.Get(argsRaw, "path").Value()); err == nil {
						payload, _ = sjson.SetBytes(payload, pathPrefix+".function.arguments", updatedArgs)
					}
				}
			case "edit":
				payload, _ = sjson.SetBytes(payload, pathPrefix+".function.name", "Edit")
				argsRaw := tc.Get("function.arguments").String()
				if gjson.Get(argsRaw, "path").Exists() && !gjson.Get(argsRaw, "file_path").Exists() {
					if updatedArgs, err := sjson.Set(argsRaw, "file_path", gjson.Get(argsRaw, "path").Value()); err == nil {
						payload, _ = sjson.SetBytes(payload, pathPrefix+".function.arguments", updatedArgs)
					}
				}
			case "glob":
				payload, _ = sjson.SetBytes(payload, pathPrefix+".function.name", "Glob")
			case "grep":
				payload, _ = sjson.SetBytes(payload, pathPrefix+".function.name", "Grep")
			}
		}
	}
	return payload
}

// NormalizeOpenCodeToolCallChunk normalizes tool_call names in SSE chunk deltas.
func NormalizeOpenCodeToolCallChunk(chunk []byte) []byte {
	choices := gjson.GetBytes(chunk, "choices")
	if !choices.Exists() || !choices.IsArray() {
		return chunk
	}
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
				chunk, _ = sjson.SetBytes(chunk, pathPrefix+".function.name", "Bash")
			case "read":
				chunk, _ = sjson.SetBytes(chunk, pathPrefix+".function.name", "Read")
			case "write":
				chunk, _ = sjson.SetBytes(chunk, pathPrefix+".function.name", "Write")
			case "edit":
				chunk, _ = sjson.SetBytes(chunk, pathPrefix+".function.name", "Edit")
			case "glob":
				chunk, _ = sjson.SetBytes(chunk, pathPrefix+".function.name", "Glob")
			case "grep":
				chunk, _ = sjson.SetBytes(chunk, pathPrefix+".function.name", "Grep")
			}
		}
	}
	return chunk
}
