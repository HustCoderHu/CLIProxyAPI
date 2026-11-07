package helps

import (
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/MoeForge/moego/ojson"
)

func TestQoderStreamDecodeFrame(t *testing.T) {
	innerChunk := `{"id":"chatcmpl-1","choices":[{"delta":{"content":"hi"}}]}`
	envelope := `{"headers":{"content-type":"text/event-stream"},"body":` + qoderQuoteJSON(innerChunk) + `,"statusCodeValue":200}`

	cases := []struct {
		name       string
		frame      string
		wantInner  string
		wantStatus int
		wantDone   bool
	}{
		{
			name:       "plain json payload",
			frame:      envelope,
			wantInner:  innerChunk,
			wantStatus: 200,
		},
		{
			name:       "data prefix",
			frame:      "data: " + envelope,
			wantInner:  innerChunk,
			wantStatus: 200,
		},
		{
			name:       "double data prefix",
			frame:      "data:data:" + envelope,
			wantInner:  innerChunk,
			wantStatus: 200,
		},
		{
			name:       "leading blank line",
			frame:      "\n  " + envelope + "  \n",
			wantInner:  innerChunk,
			wantStatus: 200,
		},
		{
			name:       "status as string",
			frame:      `{"body":` + qoderQuoteJSON(innerChunk) + `,"statusCodeValue":"200"}`,
			wantInner:  innerChunk,
			wantStatus: 200,
		},
		{
			name:       "absent status",
			frame:      `{"body":` + qoderQuoteJSON(innerChunk) + `}`,
			wantInner:  innerChunk,
			wantStatus: 0,
		},
		{
			name:       "null status",
			frame:      `{"body":` + qoderQuoteJSON(innerChunk) + `,"statusCodeValue":null}`,
			wantInner:  innerChunk,
			wantStatus: 0,
		},
		{
			name:       "done frame",
			frame:      `{"body":"[DONE]"}`,
			wantInner:  "",
			wantStatus: 0,
			wantDone:   true,
		},
		{
			name:       "done frame with status",
			frame:      `{"body":"[DONE]","statusCodeValue":200}`,
			wantInner:  "",
			wantStatus: 200,
			wantDone:   true,
		},
		{
			name:       "missing body",
			frame:      `{"headers":{"a":"b"},"statusCodeValue":200}`,
			wantInner:  "",
			wantStatus: 200,
		},
		{
			name:       "body is not a string",
			frame:      `{"body":{"nested":true},"statusCodeValue":200}`,
			wantInner:  "",
			wantStatus: 200,
		},
		{
			name:       "empty body string",
			frame:      `{"body":"","statusCodeValue":200}`,
			wantInner:  "",
			wantStatus: 200,
		},
		{
			name:       "undecodable inner body",
			frame:      `{"body":"not json","statusCodeValue":200}`,
			wantInner:  "",
			wantStatus: 200,
		},
		{
			name:       "event line",
			frame:      `event:finish{"elapsed":120}`,
			wantInner:  "",
			wantStatus: 0,
		},
		{
			name:       "comment line",
			frame:      `: keep-alive`,
			wantInner:  "",
			wantStatus: 0,
		},
		{
			name:       "blank line",
			frame:      `   `,
			wantInner:  "",
			wantStatus: 0,
		},
		{
			name:       "non object payload",
			frame:      `[1,2,3]`,
			wantInner:  "",
			wantStatus: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, status, done, err := DecodeQoderStreamFrame([]byte(tc.frame))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if done != tc.wantDone {
				t.Fatalf("done = %v, want %v", done, tc.wantDone)
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if string(inner) != tc.wantInner {
				t.Fatalf("inner = %q, want %q", inner, tc.wantInner)
			}
		})
	}
}

func TestQoderStreamDecodeFrameStatusError(t *testing.T) {
	cases := []struct {
		name       string
		frame      string
		wantStatus int
		wantBody   string
	}{
		{
			name:       "numeric 500",
			frame:      `{"statusCodeValue":500,"body":"boom"}`,
			wantStatus: 500,
			wantBody:   "boom",
		},
		{
			name:       "string 403",
			frame:      `{"statusCodeValue":"403","body":"{\"code\":112}"}`,
			wantStatus: 403,
		},
		{
			name:       "queue status 10605",
			frame:      `{"statusCodeValue":10605,"body":"queued","retryAfterSeconds":3}`,
			wantStatus: 10605,
		},
		{
			name:       "unparseable status maps to 502",
			frame:      `{"statusCodeValue":"nope","body":"weird"}`,
			wantStatus: 502,
		},
		{
			name:       "boolean status maps to 502",
			frame:      `{"statusCodeValue":true,"body":"weird"}`,
			wantStatus: 502,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			inner, status, done, err := DecodeQoderStreamFrame([]byte(tc.frame))
			if err == nil {
				t.Fatalf("expected an error, got inner=%q status=%d done=%v", inner, status, done)
			}
			if done {
				t.Fatalf("done = true on an error frame")
			}
			if inner != nil {
				t.Fatalf("inner = %q, want nil", inner)
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			var statusErr *QoderStreamStatusError
			if !errors.As(err, &statusErr) {
				t.Fatalf("error type = %T, want *QoderStreamStatusError", err)
			}
			if statusErr.Status != tc.wantStatus {
				t.Fatalf("status error carries %d, want %d", statusErr.Status, tc.wantStatus)
			}
			if statusErr.Body == "" {
				t.Fatal("status error carries no detail")
			}
		})
	}
}

func TestQoderStreamDecodeFrameEmbeddedDoneIsNotFrameDone(t *testing.T) {
	// A chunk whose content literally contains the [DONE] marker is not a
	// terminal frame; only the inner body being exactly "[DONE]" ends the stream.
	inner := `{"choices":[{"delta":{"content":"[DONE]"}}]}`
	frame := `{"body":` + qoderQuoteJSON(inner) + `,"statusCodeValue":200}`
	got, status, done, err := DecodeQoderStreamFrame([]byte(frame))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if done {
		t.Fatal("done = true for a chunk containing the marker as content")
	}
	if status != 200 {
		t.Fatalf("status = %d, want 200", status)
	}
	if string(got) != inner {
		t.Fatalf("inner = %q, want %q", got, inner)
	}
}

func TestQoderStreamDecodeFrameUTF8(t *testing.T) {
	// Multi-byte characters must survive the string decode unchanged, at a
	// boundary where a byte-counting implementation would cut a rune.
	inner := `{"choices":[{"delta":{"content":"你好，世界 😀"}}]}`
	frame := `{"body":` + qoderQuoteJSON(inner) + `,"statusCodeValue":200}`
	got, _, _, err := DecodeQoderStreamFrame([]byte(frame))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != inner {
		t.Fatalf("inner round trip changed the payload:\n got %s\nwant %s", got, inner)
	}
	if !strings.Contains(string(got), "你好") {
		t.Fatalf("inner lost its non-ASCII text: %s", got)
	}
}

func TestQoderStreamCleanChunk(t *testing.T) {
	cases := []struct {
		name        string
		chunk       string
		wantChanged bool
		wantDrop    bool
		wantJSON    string
	}{
		{
			name:        "untouched chunk reports no change",
			chunk:       `{"id":"c1","choices":[{"delta":{"content":"hello"},"finish_reason":""}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"id":"c1","choices":[{"delta":{"content":"hello"},"finish_reason":""}]}`,
		},
		{
			name:        "empty delta is dropped",
			chunk:       `{"choices":[{"delta":{}}]}`,
			wantChanged: false,
			wantDrop:    true,
			wantJSON:    `{"choices":[{"delta":{}}]}`,
		},
		{
			name:        "delta emptied by cleanup is dropped",
			chunk:       `{"choices":[{"delta":{"reasoning_content":""}}]}`,
			wantChanged: true,
			wantDrop:    true,
			wantJSON:    `{"choices":[{"delta":{}}]}`,
		},
		{
			name:        "reasoning content empty string removed",
			chunk:       `{"choices":[{"delta":{"reasoning_content":"","content":"x"}}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"content":"x"}}]}`,
		},
		{
			name:        "reasoning content null removed",
			chunk:       `{"choices":[{"delta":{"reasoning_content":null,"content":"x"}}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"content":"x"}}]}`,
		},
		{
			name:        "reasoning content kept when present",
			chunk:       `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
		},
		{
			name:        "empty function call removed",
			chunk:       `{"choices":[{"delta":{"function_call":{},"content":"x"}}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"content":"x"}}]}`,
		},
		{
			name:        "nameless function call removed",
			chunk:       `{"choices":[{"delta":{"function_call":{"arguments":"{}"},"content":"x"}}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"content":"x"}}]}`,
		},
		{
			name:        "named function call kept",
			chunk:       `{"choices":[{"delta":{"function_call":{"name":"read"}}}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"function_call":{"name":"read"}}}]}`,
		},
		{
			name:        "empty tool calls removed",
			chunk:       `{"choices":[{"delta":{"tool_calls":[],"content":"x"}}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"content":"x"}}]}`,
		},
		{
			name:        "non empty tool calls kept",
			chunk:       `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1"}]}}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1"}]}}]}`,
		},
		{
			name:        "empty extra fields and refusal removed",
			chunk:       `{"choices":[{"delta":{"extra_fields":{},"refusal":"","content":"x"}}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"content":"x"}}]}`,
		},
		{
			name:        "non empty extra fields kept",
			chunk:       `{"choices":[{"delta":{"extra_fields":{"a":1}}}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{"extra_fields":{"a":1}}}]}`,
		},
		{
			name:        "delta with only finish reason kept",
			chunk:       `{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		},
		{
			name:        "delta emptied but finish reason set is kept",
			chunk:       `{"choices":[{"delta":{"reasoning_content":""},"finish_reason":"length"}]}`,
			wantChanged: true,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"delta":{},"finish_reason":"length"}]}`,
		},
		{
			name:        "choice without delta kept",
			chunk:       `{"choices":[{"finish_reason":"stop","index":0}]}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[{"finish_reason":"stop","index":0}]}`,
		},
		{
			name:        "usage only chunk kept",
			chunk:       `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
			wantChanged: false,
			wantDrop:    false,
			wantJSON:    `{"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.chunk)
			changed, drop := CleanQoderChunk(doc)
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v (document %s)", changed, tc.wantChanged, doc.Bytes())
			}
			if drop != tc.wantDrop {
				t.Errorf("drop = %v, want %v", drop, tc.wantDrop)
			}
			if got := string(doc.Bytes()); got != tc.wantJSON {
				t.Errorf("document = %s, want %s", got, tc.wantJSON)
			}
		})
	}
}

func TestQoderStreamCleanChunkMultiChoice(t *testing.T) {
	// Two choices: the first has content, the second is an empty delta. The
	// chunk must survive because the first choice carries output.
	chunk := `{"choices":[{"delta":{"content":"a"}},{"delta":{"refusal":""}}]}`
	doc := ojson.NewDocString(chunk)
	changed, drop := CleanQoderChunk(doc)
	if drop {
		t.Fatal("chunk with real content was dropped")
	}
	if !changed {
		t.Fatal("changed = false, want the empty refusal removed")
	}
	if doc.Get("choices.1.delta.refusal").Exists() {
		t.Fatalf("refusal survived: %s", doc.Bytes())
	}
	if doc.Get("choices.0.delta.content").String() != "a" {
		t.Fatalf("content was lost: %s", doc.Bytes())
	}
}

func TestQoderStreamStatusErrorTruncatesAtRuneBoundary(t *testing.T) {
	// A long non-ASCII body must not be cut in the middle of a rune.
	detail := strings.Repeat("错误", 300)
	frame := `{"statusCodeValue":500,"body":` + qoderQuoteJSON(detail) + `}`
	_, status, _, err := DecodeQoderStreamFrame([]byte(frame))
	if err == nil {
		t.Fatal("expected an error")
	}
	if status != 500 {
		t.Fatalf("status = %d, want 500", status)
	}
	var statusErr *QoderStreamStatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("error type = %T", err)
	}
	if len(statusErr.Body) > qoderStatusErrorBodyLimit {
		t.Fatalf("detail length = %d, want <= %d", len(statusErr.Body), qoderStatusErrorBodyLimit)
	}
	if !utf8.ValidString(statusErr.Body) {
		t.Fatal("detail is not valid UTF-8; the truncation split a rune")
	}
}

func TestQoderStreamCleanChunkNilAndEmpty(t *testing.T) {
	if changed, drop := CleanQoderChunk(nil); changed || drop {
		t.Fatalf("nil doc: changed=%v drop=%v", changed, drop)
	}
	doc := ojson.NewDoc(nil)
	if changed, drop := CleanQoderChunk(doc); changed || drop {
		t.Fatalf("empty doc: changed=%v drop=%v", changed, drop)
	}
}

func TestQoderStreamUsageCacheNormalization(t *testing.T) {
	cases := []struct {
		name        string
		chunk       string
		wantChanged bool
		wantCached  int64
		wantKeep    []string
	}{
		{
			name:        "deepseek prompt_cache_hit_tokens",
			chunk:       `{"choices":[],"usage":{"prompt_tokens":100,"prompt_cache_hit_tokens":40}}`,
			wantChanged: true,
			wantCached:  40,
			wantKeep:    []string{"usage.prompt_cache_hit_tokens"},
		},
		{
			name:        "qwen completion_tokens_details",
			chunk:       `{"choices":[],"usage":{"prompt_tokens":100,"completion_tokens_details":{"cached_tokens":25}}}`,
			wantChanged: true,
			wantCached:  25,
			wantKeep:    []string{"usage.completion_tokens_details.cached_tokens"},
		},
		{
			name:        "top level cached_tokens",
			chunk:       `{"choices":[],"usage":{"cached_tokens":7}}`,
			wantChanged: true,
			wantCached:  7,
			wantKeep:    []string{"usage.cached_tokens"},
		},
		{
			name:        "already standard",
			chunk:       `{"choices":[],"usage":{"prompt_tokens_details":{"cached_tokens":12}}}`,
			wantChanged: false,
			wantCached:  12,
		},
		{
			name:        "standard takes precedence over fallback",
			chunk:       `{"choices":[],"usage":{"cached_tokens":3,"prompt_tokens_details":{"cached_tokens":9}}}`,
			wantChanged: false,
			wantCached:  9,
			wantKeep:    []string{"usage.cached_tokens"},
		},
		{
			name:        "no cache data",
			chunk:       `{"choices":[],"usage":{"prompt_tokens":100}}`,
			wantChanged: false,
			wantCached:  0,
		},
		{
			name:        "zero fallback ignored",
			chunk:       `{"choices":[],"usage":{"prompt_cache_hit_tokens":0,"prompt_tokens_details":{"cached_tokens":0}}}`,
			wantChanged: false,
			wantCached:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc := ojson.NewDocString(tc.chunk)
			changed, drop := CleanQoderChunk(doc)
			if drop {
				t.Fatal("usage chunk was dropped")
			}
			if changed != tc.wantChanged {
				t.Errorf("changed = %v, want %v (%s)", changed, tc.wantChanged, doc.Bytes())
			}
			if got := doc.Get("usage.prompt_tokens_details.cached_tokens").Int(); got != tc.wantCached {
				t.Errorf("prompt_tokens_details.cached_tokens = %d, want %d", got, tc.wantCached)
			}
			// The upstream-specific fields must survive normalization.
			for _, path := range tc.wantKeep {
				if !doc.Get(path).Exists() {
					t.Errorf("%s was removed; original fields must be kept", path)
				}
			}
		})
	}
}

func TestQoderStreamCleanChunkDoesNotTouchNonObjectUsage(t *testing.T) {
	chunk := `{"choices":[{"delta":{"content":"x"}}],"usage":"oops"}`
	doc := ojson.NewDocString(chunk)
	changed, drop := CleanQoderChunk(doc)
	if changed || drop {
		t.Fatalf("changed=%v drop=%v, want both false", changed, drop)
	}
	if string(doc.Bytes()) != chunk {
		t.Fatalf("document changed: %s", doc.Bytes())
	}
}

func TestQoderStreamCleanChunkUTF8(t *testing.T) {
	// Chinese text and an emoji must survive the delete rewrites byte for byte.
	chunk := `{"choices":[{"delta":{"content":"你好，世界 😀","reasoning_content":"","tool_calls":[]}}]}`
	doc := ojson.NewDocString(chunk)
	changed, drop := CleanQoderChunk(doc)
	if drop {
		t.Fatal("chunk was dropped")
	}
	if !changed {
		t.Fatal("changed = false, want the noise fields removed")
	}
	content := doc.Get("choices.0.delta.content").String()
	if content != "你好，世界 😀" {
		t.Fatalf("content = %q, want the original multi-byte text", content)
	}
	// The raw bytes must still be valid JSON with the runes intact.
	if !ojson.ValidBytes(doc.Bytes()) {
		t.Fatalf("document is not valid JSON: %s", doc.Bytes())
	}
}

func TestQoderStreamEnvelopeEndToEnd(t *testing.T) {
	// One full frame through decode and clean, as the executor will do it.
	inner := `{"id":"c1","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"你好","reasoning_content":"","tool_calls":[]},"finish_reason":""}],"usage":{"prompt_cache_hit_tokens":31}}`
	frame := "data: " + `{"headers":{"x":"y"},"body":` + qoderQuoteJSON(inner) + `,"statusCodeValue":200}` + "\n\n"

	decoded, status, done, err := DecodeQoderStreamFrame([]byte(frame))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if status != 200 || done {
		t.Fatalf("status=%d done=%v", status, done)
	}

	doc := ojson.NewDoc(decoded)
	changed, drop := CleanQoderChunk(doc)
	if drop {
		t.Fatal("chunk was dropped")
	}
	if !changed {
		t.Fatal("changed = false, want the noise removed")
	}
	out := doc.Bytes()
	if doc.Get("choices.0.delta.content").String() != "你好" {
		t.Fatalf("content lost: %s", out)
	}
	if doc.Get("choices.0.delta.reasoning_content").Exists() {
		t.Fatalf("reasoning_content survived: %s", out)
	}
	if doc.Get("choices.0.delta.tool_calls").Exists() {
		t.Fatalf("empty tool_calls survived: %s", out)
	}
	if got := doc.Get("usage.prompt_tokens_details.cached_tokens").Int(); got != 31 {
		t.Fatalf("cached_tokens = %d, want 31", got)
	}
}

// qoderQuoteJSON renders s as a JSON string literal for embedding in a test
// envelope. It only handles the ASCII payloads the tests use.
func qoderQuoteJSON(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}
