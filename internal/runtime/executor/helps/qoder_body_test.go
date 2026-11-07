package helps

import (
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

// buildQoderBodyForTest runs the builder and parses the result so tests can
// assert on specific paths.
func buildQoderBodyForTest(t *testing.T, payload string, meta QoderCatalogMeta, userType, sessionID string) *ojson.Doc {
	t.Helper()
	doc := ojson.NewDocString("{}")
	if err := BuildQoderBodyDoc(doc, []byte(payload), meta, userType, sessionID); err != nil {
		t.Fatalf("BuildQoderBodyDoc: %v", err)
	}
	if !ojson.ValidBytes(doc.Bytes()) {
		t.Fatalf("built body is not valid JSON: %s", doc.Bytes())
	}
	return doc
}

func qoderTestMeta() QoderCatalogMeta {
	return QoderCatalogMeta{
		Key:            "qfmodel",
		DisplayName:    "Qwen3.8-Flash",
		IsVL:           true,
		IsReasoning:    true,
		MaxInputTokens: 180000,
	}
}

func TestQoderBodyEnvelopeFields(t *testing.T) {
	doc := buildQoderBodyForTest(t, `{"model":"qfmodel","messages":[{"role":"user","content":"hi"}]}`,
		qoderTestMeta(), "personal_professional_trial", "")

	cases := []struct {
		path     string
		wantType ojson.Type
		wantStr  string
		wantInt  int64
	}{
		{path: "chat_task", wantType: ojson.String, wantStr: "FREE_INPUT"},
		{path: "code_language", wantType: ojson.String, wantStr: ""},
		{path: "source", wantType: ojson.Number, wantInt: 1},
		{path: "version", wantType: ojson.String, wantStr: "3"},
		{path: "chat_prompt", wantType: ojson.String, wantStr: ""},
		{path: "session_type", wantType: ojson.String, wantStr: "qodercli"},
		{path: "agent_id", wantType: ojson.String, wantStr: "agent_common"},
		{path: "task_id", wantType: ojson.String, wantStr: "common"},
		{path: "stream", wantType: ojson.True},
		{path: "aliyun_user_type", wantType: ojson.String, wantStr: "personal_professional_trial"},
	}
	for _, tc := range cases {
		got := doc.Get(tc.path)
		if !got.Exists() {
			t.Errorf("%s: missing", tc.path)
			continue
		}
		if got.Type != tc.wantType {
			t.Errorf("%s: type = %v, want %v (raw %q)", tc.path, got.Type, tc.wantType, got.Raw)
			continue
		}
		if tc.wantType == ojson.String && got.String() != tc.wantStr {
			t.Errorf("%s = %q, want %q", tc.path, got.String(), tc.wantStr)
		}
		if tc.wantType == ojson.Number && got.Int() != tc.wantInt {
			t.Errorf("%s = %d, want %d", tc.path, got.Int(), tc.wantInt)
		}
	}

	// is_reply / is_retry / image_urls are three more envelope scalars.
	if !doc.Get("is_reply").Bool() {
		t.Errorf("is_reply = %q, want true", doc.Get("is_reply").Raw)
	}
	if doc.Get("is_retry").Bool() {
		t.Errorf("is_retry = %q, want false", doc.Get("is_retry").Raw)
	}
	imageURLs := doc.Get("image_urls")
	if !imageURLs.Exists() || imageURLs.Type != ojson.Null {
		t.Errorf("image_urls = %q (type %v), want null", imageURLs.Raw, imageURLs.Type)
	}

	// The four generated ids must be non-empty UUID strings, and distinct.
	ids := map[string]string{}
	for _, path := range []string{"request_id", "request_set_id", "chat_record_id", "session_id"} {
		value := doc.Get(path).String()
		if len(value) != 36 {
			t.Errorf("%s = %q, want a 36-character uuid", path, value)
		}
		ids[path] = value
	}
	for _, path := range []string{"request_set_id", "session_id"} {
		if ids[path] == ids["request_id"] {
			t.Errorf("%s must not reuse request_id", path)
		}
	}
}

func TestQoderBodyModelConfigKeys(t *testing.T) {
	meta := qoderTestMeta()
	doc := buildQoderBodyForTest(t, `{"model":"qfmodel","messages":[]}`, meta, "", "")

	got := doc.Get("model_config")
	if !got.IsObject() {
		t.Fatalf("model_config is not an object: %q", got.Raw)
	}

	want := map[string]bool{
		"key": true, "display_name": true, "model": true, "format": true,
		"is_vl": true, "is_reasoning": true, "api_key": true, "url": true,
		"source": true, "max_input_tokens": true,
	}
	keys := got.Map()
	if len(keys) != len(want) {
		t.Errorf("model_config has %d keys, want %d: %v", len(keys), len(want), got.Raw)
	}
	for key := range want {
		if _, ok := keys[key]; !ok {
			t.Errorf("model_config missing key %q", key)
		}
	}

	if keys["key"].String() != "qfmodel" {
		t.Errorf("model_config.key = %q", keys["key"].String())
	}
	if keys["display_name"].String() != "Qwen3.8-Flash" {
		t.Errorf("model_config.display_name = %q", keys["display_name"].String())
	}
	if keys["model"].String() != "" || keys["api_key"].String() != "" || keys["url"].String() != "" {
		t.Errorf("model_config shared blanks must be empty strings: %q", got.Raw)
	}
	if keys["format"].String() != "openai" || keys["source"].String() != "system" {
		t.Errorf("model_config format/source = %q/%q", keys["format"].String(), keys["source"].String())
	}
	if !keys["is_vl"].Bool() || !keys["is_reasoning"].Bool() {
		t.Errorf("model_config flags not carried: %q", got.Raw)
	}
	if keys["max_input_tokens"].Int() != 180000 {
		t.Errorf("model_config.max_input_tokens = %d", keys["max_input_tokens"].Int())
	}
}

func TestQoderBodyMaxInputTokensFallback(t *testing.T) {
	cases := []struct {
		name string
		meta QoderCatalogMeta
		want int64
	}{
		{name: "explicit", meta: QoderCatalogMeta{Key: "kmodel", MaxInputTokens: 262144}, want: 262144},
		{name: "zero falls back", meta: QoderCatalogMeta{Key: "auto"}, want: 180000},
		{name: "negative falls back", meta: QoderCatalogMeta{Key: "auto", MaxInputTokens: -1}, want: 180000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, `{"model":"m","messages":[]}`, tc.meta, "", "")
			if got := doc.Get("model_config.max_input_tokens").Int(); got != tc.want {
				t.Fatalf("max_input_tokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestQoderBodyChatContext(t *testing.T) {
	doc := buildQoderBodyForTest(t,
		`{"model":"qfmodel","messages":[{"role":"user","content":"explain this code"}]}`,
		qoderTestMeta(), "", "")

	if got := doc.Get("chat_context.chatPrompt").String(); got != "" {
		t.Errorf("chat_context.chatPrompt = %q, want empty", got)
	}
	// Both "type":"text" discriminators must be written explicitly.
	if got := doc.Get("chat_context.text.type").String(); got != "text" {
		t.Errorf("chat_context.text.type = %q, want text", got)
	}
	if got := doc.Get("chat_context.text.text").String(); got != "explain this code" {
		t.Errorf("chat_context.text.text = %q", got)
	}
	if got := doc.Get("chat_context.extra.originalContent.type").String(); got != "text" {
		t.Errorf("chat_context.extra.originalContent.type = %q, want text", got)
	}
	if got := doc.Get("chat_context.extra.originalContent.text").String(); got != "explain this code" {
		t.Errorf("chat_context.extra.originalContent.text = %q", got)
	}
	if !doc.Get("chat_context.extra.context").IsArray() || len(doc.Get("chat_context.extra.context").Array()) != 0 {
		t.Errorf("chat_context.extra.context = %q, want []", doc.Get("chat_context.extra.context").Raw)
	}
	if !doc.Get("chat_context.features").IsArray() || len(doc.Get("chat_context.features").Array()) != 0 {
		t.Errorf("chat_context.features = %q, want []", doc.Get("chat_context.features").Raw)
	}
	if got := doc.Get("chat_context.imageUrls"); got.Type != ojson.Null {
		t.Errorf("chat_context.imageUrls = %q, want null", got.Raw)
	}

	// extra.modelConfig is a byte-identical copy of model_config.
	embedded := doc.Get("chat_context.extra.modelConfig")
	topLevel := doc.Get("model_config")
	if embedded.Raw != topLevel.Raw {
		t.Fatalf("extra.modelConfig differs from model_config:\n got %s\nwant %s", embedded.Raw, topLevel.Raw)
	}
}

func TestQoderBodyMaxTokens(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    int64
	}{
		{name: "default", payload: `{"model":"m","messages":[]}`, want: 32768},
		{name: "max_tokens", payload: `{"model":"m","messages":[],"max_tokens":4096}`, want: 4096},
		{name: "max_completion_tokens", payload: `{"model":"m","messages":[],"max_completion_tokens":8192}`, want: 8192},
		{
			name:    "max_tokens wins",
			payload: `{"model":"m","messages":[],"max_tokens":1024,"max_completion_tokens":8192}`,
			want:    1024,
		},
		{
			name:    "zero max_tokens falls through",
			payload: `{"model":"m","messages":[],"max_tokens":0,"max_completion_tokens":8192}`,
			want:    8192,
		},
		{name: "zero both uses default", payload: `{"model":"m","messages":[],"max_tokens":0}`, want: 32768},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, tc.payload, qoderTestMeta(), "", "")
			if got := doc.Get("parameters.max_tokens").Int(); got != tc.want {
				t.Fatalf("parameters.max_tokens = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestQoderBodyReasoningEffort(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "absent", payload: `{"model":"m","messages":[]}`, want: ""},
		{name: "reasoning_effort", payload: `{"model":"m","messages":[],"reasoning_effort":"high"}`, want: "high"},
		{name: "reasoning.effort", payload: `{"model":"m","messages":[],"reasoning":{"effort":"medium"}}`, want: "medium"},
		{name: "thinking.effort", payload: `{"model":"m","messages":[],"thinking":{"effort":"low"}}`, want: "low"},
		{name: "thinking.level", payload: `{"model":"m","messages":[],"thinking":{"level":"max"}}`, want: "max"},
		{
			name:    "reasoning_effort wins",
			payload: `{"model":"m","messages":[],"reasoning_effort":"xhigh","reasoning":{"effort":"low"}}`,
			want:    "xhigh",
		},
		{name: "empty value is absent", payload: `{"model":"m","messages":[],"reasoning_effort":""}`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, tc.payload, qoderTestMeta(), "", "")
			got := doc.Get("parameters.reasoning_effort")
			if tc.want == "" {
				if got.Exists() {
					t.Fatalf("parameters.reasoning_effort = %q, want absent", got.Raw)
				}
				return
			}
			if got.String() != tc.want {
				t.Fatalf("parameters.reasoning_effort = %q, want %q", got.String(), tc.want)
			}
		})
	}
}

func TestQoderBodyBusiness(t *testing.T) {
	before := time.Now().Add(-time.Second)
	// Exactly 40 characters, so the expected 30-character prefix is obvious.
	doc := buildQoderBodyForTest(t, `{"model":"qfmodel","messages":[{"role":"user","content":"abcdefghij0123456789ABCDEFGHIJ0123456789"}]}`,
		qoderTestMeta(), "", "")
	after := time.Now().Add(time.Second)

	if got := doc.Get("business.model_key").String(); got != "qfmodel" {
		t.Errorf("business.model_key = %q, want qfmodel", got)
	}
	if got := doc.Get("business.version").String(); got != cosyVersion {
		t.Errorf("business.version = %q, want %q", got, cosyVersion)
	}
	if got := doc.Get("business.product").String(); got != "cli" {
		t.Errorf("business.product = %q", got)
	}
	if got := doc.Get("business.type").String(); got != "agent" {
		t.Errorf("business.type = %q", got)
	}
	if got := doc.Get("business.stage").String(); got != "start" {
		t.Errorf("business.stage = %q", got)
	}
	// 30 characters of the 40-character prompt.
	if got := doc.Get("business.name").String(); got != "abcdefghij0123456789ABCDEFGHIJ" {
		t.Errorf("business.name = %q, want the 30-character prompt prefix", got)
	}
	if id := doc.Get("business.id").String(); len(id) != 36 {
		t.Errorf("business.id = %q, want a uuid", id)
	}
	beginAt := doc.Get("business.begin_at").Int()
	if wantMin, wantMax := before.UnixMilli(), after.UnixMilli(); beginAt < wantMin || beginAt > wantMax {
		t.Errorf("business.begin_at = %d, want within [%d,%d] for a standalone request", beginAt, wantMin, wantMax)
	}
}

func TestQoderBodyBusinessNameFallbacks(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "no user turn", payload: `{"model":"m","messages":[{"role":"system","content":"sys"}]}`, want: "chat"},
		{name: "empty user content", payload: `{"model":"m","messages":[{"role":"user","content":""}]}`, want: "chat"},
		{name: "short prompt", payload: `{"model":"m","messages":[{"role":"user","content":"hi there"}]}`, want: "hi there"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, tc.payload, qoderTestMeta(), "", "")
			if got := doc.Get("business.name").String(); got != tc.want {
				t.Fatalf("business.name = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestQoderBodySystemPromptHandling(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "fallback when absent",
			payload: `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
			want:    "You are a helpful assistant.",
		},
		{
			name:    "client system replaces fallback",
			payload: `{"model":"m","messages":[{"role":"system","content":"custom system"},{"role":"user","content":"hi"}]}`,
			want:    "custom system",
		},
		{
			name:    "developer maps to system",
			payload: `{"model":"m","messages":[{"role":"developer","content":"dev system"},{"role":"user","content":"hi"}]}`,
			want:    "dev system",
		},
		{
			name: "only first system survives",
			payload: `{"model":"m","messages":[
				{"role":"system","content":"first"},
				{"role":"system","content":"second"},
				{"role":"user","content":"hi"}]}`,
			want: "first",
		},
		{
			name: "empty first system is skipped",
			payload: `{"model":"m","messages":[
				{"role":"system","content":""},
				{"role":"system","content":"second"},
				{"role":"user","content":"hi"}]}`,
			want: "second",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, tc.payload, qoderTestMeta(), "", "")
			messages := doc.Get("messages")
			if !messages.IsArray() {
				t.Fatalf("messages is not an array: %q", messages.Raw)
			}
			list := messages.Array()
			if len(list) == 0 {
				t.Fatal("messages is empty")
			}
			first := list[0]
			if first.Get("role").String() != "system" {
				t.Fatalf("messages.0.role = %q, want system", first.Get("role").String())
			}
			if got := first.Get("content").String(); got != tc.want {
				t.Fatalf("system content = %q, want %q", got, tc.want)
			}
			// Exactly one system message, always first.
			for idx, message := range list[1:] {
				if message.Get("role").String() == "system" {
					t.Fatalf("messages.%d is an extra system message", idx+1)
				}
			}
		})
	}
}

func TestQoderBodyMessageFlattening(t *testing.T) {
	payload := `{"model":"m","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":[{"type":"text","text":"line one"},{"type":"text","text":"line two"}]},
		{"role":"tool","tool_call_id":"call_1"},
		{"role":"wizard","content":"unknown role"},
		{"role":"assistant","content":"answer"}
	]}`
	doc := buildQoderBodyForTest(t, payload, qoderTestMeta(), "", "")
	list := doc.Get("messages").Array()
	if len(list) != 5 {
		t.Fatalf("messages has %d entries, want 5: %s", len(list), doc.Get("messages").Raw)
	}

	// Array content joins text parts with newlines.
	if got := list[1].Get("content").String(); got != "line one\nline two" {
		t.Errorf("user content = %q, want the joined text parts", got)
	}
	// A tool message without content keeps an empty string, never null.
	toolMessage := list[2]
	if toolMessage.Get("role").String() != "tool" {
		t.Errorf("messages.2.role = %q", toolMessage.Get("role").String())
	}
	if toolMessage.Get("tool_call_id").String() != "call_1" {
		t.Errorf("messages.2.tool_call_id = %q", toolMessage.Get("tool_call_id").String())
	}
	content := toolMessage.Get("content")
	if content.Type != ojson.String {
		t.Fatalf("tool content type = %v, want string (%q)", content.Type, content.Raw)
	}
	if content.String() != "" {
		t.Errorf("tool content = %q, want empty", content.String())
	}
	// Unknown roles degrade to user.
	if list[3].Get("role").String() != "user" {
		t.Errorf("messages.3.role = %q, want user", list[3].Get("role").String())
	}
	if list[3].Get("content").String() != "unknown role" {
		t.Errorf("messages.3.content = %q", list[3].Get("content").String())
	}
	if list[4].Get("role").String() != "assistant" {
		t.Errorf("messages.4.role = %q", list[4].Get("role").String())
	}
}

func TestQoderBodyToolCallsArgumentsStringified(t *testing.T) {
	payload := `{"model":"m","messages":[{"role":"assistant","content":"","tool_calls":[
		{"id":"a1","type":"function","function":{"name":"read","arguments":{"path":"/tmp/x"}}},
		{"id":"a2","function":{"name":"glob","arguments":"{\"pattern\":\"*.go\"}"}},
		{"id":"a3","type":"function","function":{"name":"list"}},
		{"id":"a4","type":"function","function":{"name":"multi","arguments":[1,2]}}
	]}]}`
	doc := buildQoderBodyForTest(t, payload, qoderTestMeta(), "", "")

	calls := doc.Get("messages.1.tool_calls").Array()
	if len(calls) != 4 {
		t.Fatalf("tool_calls has %d entries, want 4: %s", len(calls), doc.Get("messages.1.tool_calls").Raw)
	}

	wantArgs := map[string]string{
		"a1": `{"path":"/tmp/x"}`,
		"a2": `{"pattern":"*.go"}`,
		"a3": "",
		"a4": `[1,2]`,
	}
	for _, call := range calls {
		id := call.Get("id").String()
		if call.Get("type").String() != "function" {
			t.Errorf("%s: type = %q, want function", id, call.Get("type").String())
		}
		arguments := call.Get("function.arguments")
		if arguments.Type != ojson.String {
			t.Errorf("%s: arguments type = %v, want string (%q)", id, arguments.Type, arguments.Raw)
			continue
		}
		if want := wantArgs[id]; arguments.String() != want {
			t.Errorf("%s: arguments = %q, want %q", id, arguments.String(), want)
		}
	}
}

func TestQoderBodyReasoningContentGating(t *testing.T) {
	payload := `{"model":"%s","messages":[
		{"role":"user","content":"hi"},
		{"role":"assistant","content":"answer","reasoning_content":"thinking..."}
	]}`

	cases := []struct {
		name     string
		model    string
		meta     QoderCatalogMeta
		wantKept bool
	}{
		{name: "deepseek key", model: "dfmodel", meta: QoderCatalogMeta{Key: "dfmodel"}, wantKept: true},
		{name: "deepseek dmodel", model: "dmodel", meta: QoderCatalogMeta{Key: "dmodel"}, wantKept: true},
		{name: "deepseek name", model: "deepseek-v4-flash", meta: QoderCatalogMeta{Key: "somekey"}, wantKept: true},
		{name: "non deepseek", model: "qfmodel", meta: QoderCatalogMeta{Key: "qfmodel"}, wantKept: false},
		{name: "kimi", model: "kmodel", meta: QoderCatalogMeta{Key: "kmodel"}, wantKept: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := qoderSprintf(payload, tc.model)
			doc := buildQoderBodyForTest(t, body, tc.meta, "", "")
			got := doc.Get("messages.2.reasoning_content")
			if tc.wantKept {
				if got.String() != "thinking..." {
					t.Fatalf("reasoning_content = %q, want kept", got.Raw)
				}
				return
			}
			if got.Exists() {
				t.Fatalf("reasoning_content = %q, want dropped", got.Raw)
			}
		})
	}
}

func TestQoderBodyToolsPassthrough(t *testing.T) {
	cases := []struct {
		name     string
		payload  string
		wantRaw  string
		wantKind string
	}{
		{
			name:     "absent yields empty array",
			payload:  `{"model":"m","messages":[]}`,
			wantRaw:  "[]",
			wantKind: "empty",
		},
		{
			name:     "empty array stays empty",
			payload:  `{"model":"m","messages":[],"tools":[]}`,
			wantRaw:  "[]",
			wantKind: "empty",
		},
		{
			name:     "tools pass through",
			payload:  `{"model":"m","messages":[],"tools":[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]}`,
			wantRaw:  `[{"type":"function","function":{"name":"read","parameters":{"type":"object"}}}]`,
			wantKind: "one",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, tc.payload, qoderTestMeta(), "", "")
			tools := doc.Get("tools")
			if !tools.IsArray() {
				t.Fatalf("tools is not an array: %q", tools.Raw)
			}
			if tools.Raw != tc.wantRaw {
				t.Fatalf("tools = %s, want %s", tools.Raw, tc.wantRaw)
			}
		})
	}
}

func TestQoderBodySessionIDAndUserType(t *testing.T) {
	cases := []struct {
		name      string
		sessionID string
		userType  string
		wantRec   string
		wantUser  string
	}{
		{
			name:      "session id reused",
			sessionID: "session-abc",
			userType:  "personal_standard",
			wantRec:   "session-abc",
			wantUser:  "personal_standard",
		},
		{
			name:      "blank session id falls back to request id",
			sessionID: "   ",
			userType:  "",
			wantUser:  "personal_professional_trial",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := buildQoderBodyForTest(t, `{"model":"m","messages":[]}`, qoderTestMeta(), tc.userType, tc.sessionID)
			if got := doc.Get("aliyun_user_type").String(); got != tc.wantUser {
				t.Fatalf("aliyun_user_type = %q, want %q", got, tc.wantUser)
			}
			record := doc.Get("chat_record_id").String()
			if tc.wantRec != "" {
				if record != tc.wantRec {
					t.Fatalf("chat_record_id = %q, want %q", record, tc.wantRec)
				}
				// A known conversation id also groups the usage panel.
				if got := doc.Get("business.id").String(); got != tc.wantRec {
					t.Fatalf("business.id = %q, want the conversation id %q", got, tc.wantRec)
				}
				return
			}
			if record != doc.Get("request_id").String() {
				t.Fatalf("chat_record_id = %q, want the request id", record)
			}
			if len(record) != 36 {
				t.Fatalf("chat_record_id = %q, want a uuid", record)
			}
		})
	}
}

func TestQoderBodyRebuildIsStable(t *testing.T) {
	// Building twice on the same document must not accumulate fields, and two
	// builds of the same payload must agree on everything except the generated
	// identifiers.
	payload := []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"hi"}],"max_tokens":1234}`)
	doc := ojson.NewDocString(`{"leftover":true,"messages":[{"role":"user","content":"stale"}]}`)
	if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", ""); err != nil {
		t.Fatalf("first build: %v", err)
	}
	if doc.Get("leftover").Exists() {
		t.Fatalf("previous document contents survived: %s", doc.Bytes())
	}

	unstable := []string{
		"request_id", "request_set_id", "session_id", "chat_record_id", "business.id",
	}
	first := map[string]string{}
	for _, path := range unstable {
		first[path] = doc.Get(path).Raw
	}
	// With no conversation the request stands alone, so begin_at is wall clock
	// and legitimately changes between builds; it is asserted in
	// TestQoderBodyBusinessBeginAt instead.
	firstBeginAt := doc.Get("business.begin_at").Int()
	if firstBeginAt <= 0 {
		t.Fatalf("business.begin_at = %d, want a positive timestamp", firstBeginAt)
	}

	if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", ""); err != nil {
		t.Fatalf("second build: %v", err)
	}
	for _, path := range unstable {
		if got := doc.Get(path).Raw; got == first[path] {
			t.Errorf("%s did not change across builds: %q", path, got)
		}
	}
	if got := doc.Get("parameters.max_tokens").Int(); got != 1234 {
		t.Errorf("parameters.max_tokens = %d, want 1234", got)
	}
	if got := doc.Get("messages.1.content").String(); got != "hi" {
		t.Errorf("messages.1.content = %q, want hi", got)
	}
}

func TestQoderBodyRebuildKeepsConversationIdentity(t *testing.T) {
	// With a known conversation, id and begin_at are grouping keys: both must
	// survive a rebuild untouched while the per-request fields rotate.
	payload := []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"hi"}]}`)
	doc := ojson.NewDocString("{}")
	if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conv-stable"); err != nil {
		t.Fatalf("first build: %v", err)
	}
	firstID := doc.Get("business.id").String()
	firstBeginAt := doc.Get("business.begin_at").Int()
	firstRecordID := doc.Get("chat_record_id").String()
	firstRequestID := doc.Get("request_id").String()

	if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conv-stable"); err != nil {
		t.Fatalf("second build: %v", err)
	}
	if got := doc.Get("business.id").String(); got != firstID {
		t.Errorf("business.id = %q across builds, want %q", got, firstID)
	}
	if got := doc.Get("business.begin_at").Int(); got != firstBeginAt {
		t.Errorf("business.begin_at = %d across builds, want %d", got, firstBeginAt)
	}
	if got := doc.Get("chat_record_id").String(); got != firstRecordID {
		t.Errorf("chat_record_id = %q across builds, want %q", got, firstRecordID)
	}
	// Per-request identifiers must still rotate.
	if got := doc.Get("request_id").String(); got == firstRequestID {
		t.Errorf("request_id did not rotate across builds: %q", got)
	}
}

func TestQoderBodyBusinessBeginAt(t *testing.T) {
	payload := []byte(`{"model":"qfmodel","messages":[{"role":"user","content":"hi"}]}`)

	t.Run("stable across turns of one conversation", func(t *testing.T) {
		// Every turn of a conversation must report the same grouping key, or the
		// usage panel splits one agent turn into sub-minute conversations and
		// burns the daily quota (upstream code 110).
		doc := ojson.NewDocString("{}")
		if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conversation-1"); err != nil {
			t.Fatalf("first build: %v", err)
		}
		first := doc.Get("business.begin_at").Int()
		if first <= 0 {
			t.Fatalf("business.begin_at = %d, want a positive timestamp", first)
		}

		if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conversation-1"); err != nil {
			t.Fatalf("second build: %v", err)
		}
		if got := doc.Get("business.begin_at").Int(); got != first {
			t.Fatalf("business.begin_at = %d after rebuild, want %d unchanged", got, first)
		}
	})

	t.Run("stable across a wall clock jump", func(t *testing.T) {
		// The derivation must not read the clock at all: a conversation started
		// yesterday and resumed tomorrow keeps its begin_at.
		prevNow := cosyNowFunc
		t.Cleanup(func() { cosyNowFunc = prevNow })

		doc := ojson.NewDocString("{}")
		cosyNowFunc = func() time.Time { return time.Unix(1700000000, 0) }
		if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conversation-clock"); err != nil {
			t.Fatalf("first build: %v", err)
		}
		first := doc.Get("business.begin_at").Int()

		cosyNowFunc = func() time.Time { return time.Unix(1800000000, 0) }
		if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conversation-clock"); err != nil {
			t.Fatalf("second build: %v", err)
		}
		if got := doc.Get("business.begin_at").Int(); got != first {
			t.Fatalf("business.begin_at = %d after the clock moved, want %d", got, first)
		}
	})

	t.Run("different conversations differ", func(t *testing.T) {
		seen := map[int64]string{}
		for _, id := range []string{"conversation-a", "conversation-b", "conversation-c", "conversation-d"} {
			doc := ojson.NewDocString("{}")
			if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", id); err != nil {
				t.Fatalf("build %s: %v", id, err)
			}
			beginAt := doc.Get("business.begin_at").Int()
			if prev, dup := seen[beginAt]; dup {
				t.Fatalf("%s and %s share begin_at %d", id, prev, beginAt)
			}
			seen[beginAt] = id
		}
	})

	t.Run("derived value stays inside its window", func(t *testing.T) {
		doc := ojson.NewDocString("{}")
		if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", "conversation-window"); err != nil {
			t.Fatalf("build: %v", err)
		}
		beginAt := doc.Get("business.begin_at").Int()
		if lower := qoderBusinessBeginAtAnchorMs - qoderBusinessBeginAtSpanMs; beginAt < lower || beginAt > qoderBusinessBeginAtAnchorMs {
			t.Fatalf("business.begin_at = %d, want within [%d,%d]", beginAt, lower, qoderBusinessBeginAtAnchorMs)
		}
	})

	t.Run("no conversation uses the wall clock", func(t *testing.T) {
		before := time.Now().Add(-time.Second).UnixMilli()
		doc := ojson.NewDocString("{}")
		if err := BuildQoderBodyDoc(doc, payload, qoderTestMeta(), "", ""); err != nil {
			t.Fatalf("build: %v", err)
		}
		after := time.Now().Add(time.Second).UnixMilli()
		beginAt := doc.Get("business.begin_at").Int()
		if beginAt < before || beginAt > after {
			t.Fatalf("business.begin_at = %d, want within [%d,%d]", beginAt, before, after)
		}
	})
}

func TestQoderBodyRejectsInvalidPayload(t *testing.T) {
	doc := ojson.NewDocString("{}")
	if err := BuildQoderBodyDoc(doc, []byte("{not json"), qoderTestMeta(), "", ""); err == nil {
		t.Fatal("invalid payload was accepted")
	}
	if err := BuildQoderBodyDoc(nil, []byte("{}"), qoderTestMeta(), "", ""); err == nil {
		t.Fatal("nil document was accepted")
	}
}

func TestQoderBodyEmptyMessages(t *testing.T) {
	// A request with no messages must still produce a valid body with the system
	// fallback and no turns.
	doc := buildQoderBodyForTest(t, `{"model":"m"}`, qoderTestMeta(), "", "")
	list := doc.Get("messages").Array()
	if len(list) != 1 {
		t.Fatalf("messages has %d entries, want 1: %s", len(list), doc.Get("messages").Raw)
	}
	if list[0].Get("content").String() != qoderNeutralSystemPrompt {
		t.Fatalf("system content = %q, want the neutral fallback", list[0].Get("content").String())
	}
	if doc.Get("business.name").String() != qoderNeutralBusinessName {
		t.Fatalf("business.name = %q, want %q", doc.Get("business.name").String(), qoderNeutralBusinessName)
	}
}

// qoderSprintf substitutes the single %s placeholder a test table needs.
func qoderSprintf(format, value string) string {
	return strings.Replace(format, "%s", value, 1)
}

func TestQoderBodyTopLevelKeySet(t *testing.T) {
	// The official baseprompt.json has exactly these 23 top-level keys and the
	// builder overrides or rebuilds all of them, so a missing key means the
	// upstream would fall back to a template default that no longer exists.
	doc := buildQoderBodyForTest(t, `{"model":"m","messages":[{"role":"user","content":"hi"}]}`,
		qoderTestMeta(), "", "")

	want := []string{
		"request_id", "request_set_id", "chat_record_id", "stream", "chat_task",
		"chat_context", "image_urls", "is_reply", "is_retry", "session_id",
		"code_language", "source", "version", "chat_prompt", "parameters",
		"aliyun_user_type", "session_type", "agent_id", "task_id",
		"model_config", "messages", "tools", "business",
	}
	got := make(map[string]bool, len(want))
	root := ojson.ParseBytes(doc.Bytes())
	if !root.IsObject() {
		t.Fatalf("built body is not a JSON object: %s", doc.Bytes())
	}
	root.ForEach(func(key, _ ojson.Result) bool {
		got[key.String()] = true
		return true
	})
	if len(got) != len(want) {
		t.Errorf("top-level has %d keys, want %d: %v", len(got), len(want), keysOf(got))
	}
	for _, key := range want {
		if !got[key] {
			t.Errorf("missing top-level key %q", key)
		}
	}
}

// keysOf returns the map keys in a stable order for failure messages.
func keysOf(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func TestQoderBodyWithoutEffortsCatalog(t *testing.T) {
	// qmodel exposes thinking_config but no efforts table, so the client cannot
	// ask for a level. The builder must still produce a body, and must not
	// invent a reasoning_effort when the client sends none.
	meta := QoderCatalogMeta{Key: "qmodel", DisplayName: "Qmodel"}
	doc := buildQoderBodyForTest(t, `{"model":"qmodel","messages":[{"role":"user","content":"hi"}]}`, meta, "", "")
	if doc.Get("parameters.reasoning_effort").Exists() {
		t.Fatalf("reasoning_effort was invented: %s", doc.Get("parameters").Raw)
	}
	if got := doc.Get("model_config.key").String(); got != "qmodel" {
		t.Fatalf("model_config.key = %q", got)
	}
	// A client that does send a level still has it forwarded; validation is the
	// thinking pipeline's job, not the body builder's.
	doc = buildQoderBodyForTest(t, `{"model":"qmodel","messages":[],"reasoning_effort":"high"}`, meta, "", "")
	if got := doc.Get("parameters.reasoning_effort").String(); got != "high" {
		t.Fatalf("parameters.reasoning_effort = %q, want high", got)
	}
}
