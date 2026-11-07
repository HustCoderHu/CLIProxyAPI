package executor

import (
	"bytes"
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"

	qoderauthsvc "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// qoderTestAPIKeyValue is the access token the fixture credentials carry.
const qoderTestAPIKeyValue = "drt-fixture-access-token"

// qoderQuoteJSON renders value as a JSON string literal, so an inner chunk can
// be embedded in the envelope's "body" field.
func qoderQuoteJSON(value string) string {
	doc := ojson.NewDocString("{}")
	if err := doc.Set("body", value); err != nil {
		panic(err)
	}
	return doc.Get("body").Raw
}

// qoderEnvelopeFrame wraps an inner OpenAI chunk in the upstream SSE envelope.
func qoderEnvelopeFrame(inner string) string {
	return `data:{"headers":{"content-type":"text/event-stream"},"body":` + qoderQuoteJSON(inner) + `,"statusCodeValue":200}`
}

// qoderTestAuth builds a credential carrying every field COSY needs.
func qoderTestAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "qoder-test-auth",
		Provider: constant.QoderCN,
		Attributes: map[string]string{
			"base_url": baseURL,
		},
		Metadata: map[string]any{
			"access_token":      qoderTestAPIKeyValue,
			"refresh_token":     "drt-fixture-refresh-token",
			"uid":               "uid-fixture-001",
			"nickname":          "alice",
			"user_type":         "personal_professional_trial",
			"organization_id":   "1234567890123456789",
			"organization_name": "Acme",
		},
	}
}

// qoderTestRequest is a minimal OpenAI chat-completions request.
func qoderTestRequest() cliproxyexecutor.Request {
	return cliproxyexecutor.Request{
		Model:   "qfmodel",
		Payload: []byte(`{"model":"qfmodel","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	}
}

// qoderTestOptions points the executor at the fixture server in OpenAI format.
func qoderTestOptions() cliproxyexecutor.Options {
	return cliproxyexecutor.Options{
		Stream:       true,
		SourceFormat: sdktranslator.FromString("openai"),
		Headers:      http.Header{},
	}
}

// qoderDrain collects every chunk payload and the terminal error.
func qoderDrain(t *testing.T, res *cliproxyexecutor.StreamResult) ([]string, error) {
	t.Helper()
	if res == nil {
		t.Fatal("ExecuteStream returned a nil result")
	}
	var payloads []string
	for chunk := range res.Chunks {
		if chunk.Err != nil {
			return payloads, chunk.Err
		}
		if len(chunk.Payload) > 0 {
			payloads = append(payloads, string(chunk.Payload))
		}
	}
	return payloads, nil
}

// qoderDecodedRequest is the verified upstream request.
type qoderDecodedRequest struct {
	doc *ojson.Doc
	// raw is the encoded request body exactly as it went on the wire.
	raw []byte
	// cosy holds the outbound headers.
	cosy http.Header
	// url is the full request target including the query string.
	url string
	// method is the HTTP method the executor used.
	method string
	inner  string
}

// qoderStartServer records the last request and answers with the given frames.
func qoderStartServer(t *testing.T, frames []string, status int) (*httptest.Server, *atomic.Int32, *atomic.Value) {
	t.Helper()
	var calls atomic.Int32
	captured := &atomic.Value{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		captured.Store(qoderDecodedRequest{
			raw:    body,
			cosy:   r.Header.Clone(),
			url:    r.URL.RequestURI(),
			method: r.Method,
		})
		if status != 0 && status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"code":"112","message":"quota"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range frames {
			_, _ = fmt.Fprint(w, frame)
			_, _ = fmt.Fprint(w, "\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, &calls, captured
}

// qoderDecodeCaptured decodes the captured upstream body and asserts the COSY
// headers and envelope shape are present.
func qoderDecodeCaptured(t *testing.T, captured *atomic.Value) qoderDecodedRequest {
	t.Helper()
	value := captured.Load()
	if value == nil {
		t.Fatal("upstream was never called")
	}
	record := value.(qoderDecodedRequest)

	decoded, errDecode := helps.QoderDecode(string(record.raw))
	if errDecode != nil {
		t.Fatalf("upstream body is not Qoder-encoded: %v", errDecode)
	}
	if !ojson.ValidBytes(decoded) {
		t.Fatalf("decoded upstream body is not JSON: %s", decoded)
	}
	record.doc = ojson.NewDoc(decoded)

	for _, name := range []string{"authorization", "cosy-key", "cosy-date", "x-model-key", "cosy-user"} {
		if record.cosy.Get(name) == "" {
			t.Fatalf("missing COSY header %q", name)
		}
	}
	if !strings.HasPrefix(record.cosy.Get("authorization"), "Bearer COSY.") {
		t.Fatalf("authorization header is not a COSY bearer: %q", record.cosy.Get("authorization"))
	}
	if got := record.cosy.Get("x-model-key"); got != "qfmodel" {
		t.Fatalf("x-model-key = %q, want qfmodel", got)
	}

	// The COSY signature must be reproducible from the bytes that actually went
	// on the wire, the advertised key/date and the SIGNED path (query removed and
	// the leading /algo stripped). A mismatch means the signature covers
	// different bytes than the request carries.
	auth := record.cosy.Get("authorization")
	parts := strings.Split(strings.TrimPrefix(auth, "Bearer COSY."), ".")
	if len(parts) != 2 {
		t.Fatalf("authorization is not <payload>.<signature>: %q", auth)
	}
	signedPath := record.url
	if idx := strings.Index(signedPath, "?"); idx >= 0 {
		signedPath = signedPath[:idx]
	}
	signedPath = strings.TrimPrefix(signedPath, "/algo")
	rawToSign := parts[0] + "\n" + record.cosy.Get("cosy-key") + "\n" + record.cosy.Get("cosy-date") + "\n" + string(record.raw) + "\n" + signedPath
	if want := fmt.Sprintf("%x", md5.Sum([]byte(rawToSign))); parts[1] != want {
		t.Fatalf("COSY signature mismatch: got %s, recomputed %s (signed path %q)", parts[1], want, signedPath)
	}
	return record
}

func TestQoderExecutorIdentifierAndCapabilities(t *testing.T) {
	cn := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	if got := cn.Identifier(); got != constant.QoderCN {
		t.Fatalf("Identifier() = %q, want %q", got, constant.QoderCN)
	}
	intl := NewQoderExecutor(&config.Config{}, constant.QoderINTL)
	if got := intl.Identifier(); got != constant.QoderINTL {
		t.Fatalf("Identifier() = %q, want %q", got, constant.QoderINTL)
	}
	if cn.SupportsApplyPatch() {
		t.Fatal("Qoder must not advertise apply_patch support")
	}
	cfgA := &config.Config{}
	cfgB := &config.Config{}
	bound := NewQoderExecutor(cfgA, constant.QoderCN)
	if !bound.UsesConfig(cfgA) {
		t.Fatal("UsesConfig should match the bound config")
	}
	if bound.UsesConfig(cfgB) {
		t.Fatal("UsesConfig should reject a different config")
	}
	scoped := bound.ForAPIKey()
	if scoped == nil || scoped.Identifier() != constant.QoderCN {
		t.Fatalf("ForAPIKey() returned %#v", scoped)
	}
}

func TestQoderExecutorStreamsDecodedEnvelopes(t *testing.T) {
	frames := []string{
		qoderEnvelopeFrame(`{"id":"chatcmpl-1","model":"qfmodel","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`),
		qoderEnvelopeFrame(`{"id":"chatcmpl-1","model":"qfmodel","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`),
		`data:{"body":"[DONE]"}`,
	}
	server, calls, captured := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errStream := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	payloads, errDrain := qoderDrain(t, res)
	if errDrain != nil {
		t.Fatalf("stream error = %v", errDrain)
	}
	if len(payloads) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d: %v", len(payloads), payloads)
	}
	if !strings.Contains(strings.Join(payloads, ""), "Hello") {
		t.Fatalf("stream did not carry the first delta: %v", payloads)
	}
	if !strings.Contains(strings.Join(payloads, ""), " world") {
		t.Fatalf("stream did not carry the second delta: %v", payloads)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}

	record := qoderDecodeCaptured(t, captured)
	// The literal is intentional: referencing qoderChatPath here would make the
	// test agree with a broken constant instead of catching it.
	if record.method != http.MethodPost {
		t.Fatalf("upstream method = %q, want POST", record.method)
	}
	if want := "/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"; record.url != want {
		t.Fatalf("upstream request target = %q, want %q", record.url, want)
	}
	if got := record.doc.Get("stream").Type; got != ojson.True {
		t.Fatalf("upstream stream flag = %v, want true", got)
	}
	if got := record.doc.Get("agent_id").String(); got != "agent_common" {
		t.Fatalf("agent_id = %q", got)
	}
	if got := record.doc.Get("model_config.key").String(); got != "qfmodel" {
		t.Fatalf("model_config.key = %q", got)
	}
	if got := record.doc.Get("business.model_key").String(); got != "qfmodel" {
		t.Fatalf("business.model_key = %q", got)
	}
}

func TestQoderExecutorExecuteAggregates(t *testing.T) {
	frames := []string{
		qoderEnvelopeFrame(`{"id":"chatcmpl-2","model":"qfmodel","choices":[{"index":0,"delta":{"role":"assistant","content":"Hello"}}]}`),
		qoderEnvelopeFrame(`{"id":"chatcmpl-2","model":"qfmodel","choices":[{"index":0,"delta":{"content":" world"},"finish_reason":"stop"}]}`),
		`data:{"body":"[DONE]"}`,
	}
	server, calls, _ := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	opts := qoderTestOptions()
	opts.Stream = false // Execute must force it back to true.
	resp, errExec := e.Execute(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if !ojson.ValidBytes(resp.Payload) {
		t.Fatalf("aggregated response is not JSON: %s", resp.Payload)
	}
	doc := ojson.NewDoc(resp.Payload)
	if got := doc.Get("object").String(); got != "chat.completion" {
		t.Fatalf("object = %q, want chat.completion", got)
	}
	if got := doc.Get("choices.0.message.content").String(); got != "Hello world" {
		t.Fatalf("content = %q, want %q", got, "Hello world")
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

// TestQoderExecutorExecuteForcesStreamingUpstream proves the upstream never sees
// a non-streaming request: the protocol is stream-only, and Execute folds it.
func TestQoderExecutorExecuteForcesStreamingUpstream(t *testing.T) {
	var sawStreamFlag atomic.Bool
	var sawAccept atomic.Value
	frames := []string{
		qoderEnvelopeFrame(`{"id":"chatcmpl-4","model":"qfmodel","choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":"stop"}]}`),
		`data:{"body":"[DONE]"}`,
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if decoded, errDecode := helps.QoderDecode(string(body)); errDecode == nil {
			sawStreamFlag.Store(ojson.GetBytesNoCopy(decoded, "stream").Type == ojson.True)
		}
		sawAccept.Store(r.Header.Get("accept"))
		w.Header().Set("Content-Type", "text/event-stream")
		for _, frame := range frames {
			_, _ = fmt.Fprint(w, frame, "\n\n")
		}
	}))
	t.Cleanup(server.Close)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	opts := qoderTestOptions()
	opts.Stream = false
	if _, errExec := e.Execute(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), opts); errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if !sawStreamFlag.Load() {
		t.Fatal("Execute sent a non-streaming upstream body")
	}
	if accept, _ := sawAccept.Load().(string); accept != "text/event-stream" {
		t.Fatalf("accept header = %q, want text/event-stream", accept)
	}
}

// TestQoderExecutorExecuteTranslatesToClientFormat checks the folded response is
// rendered in the client protocol rather than passed through as OpenAI JSON.
func TestQoderExecutorExecuteTranslatesToClientFormat(t *testing.T) {
	frames := []string{
		qoderEnvelopeFrame(`{"id":"chatcmpl-5","model":"qfmodel","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`),
		`data:{"body":"[DONE]"}`,
	}
	server, _, _ := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	opts := qoderTestOptions()
	opts.Stream = false
	opts.SourceFormat = sdktranslator.FromString("openai")
	opts.ResponseFormat = sdktranslator.FromString("openai")
	resp, errExec := e.Execute(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), opts)
	if errExec != nil {
		t.Fatalf("Execute() error = %v", errExec)
	}
	if got := ojson.GetBytesNoCopy(resp.Payload, "choices.0.message.content").String(); got != "hi" {
		t.Fatalf("content = %q, want hi", got)
	}
}

func TestQoderExecutorQuotaErrorIsCredentialScoped(t *testing.T) {
	frames := []string{
		`data:{"body":"{\"code\":\"112\",\"message\":\"quota exhausted\",\"pricingUrl\":\"https://x\"}","statusCodeValue":403}`,
	}
	server, _, _ := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected a quota error before the stream started")
	}
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(errExec, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("error %v does not implement IsCredentialScoped() == true", errExec)
	}
	var requestScoped interface{ IsRequestScoped() bool }
	if errors.As(errExec, &requestScoped) && requestScoped.IsRequestScoped() {
		t.Fatal("a quota error must not be request-scoped")
	}
}

func TestQoderExecutorQueueErrorIsRequestScoped(t *testing.T) {
	// The queue is retried with real waits, so the seams are stubbed to keep this
	// assertion off the wall clock.
	qoderStubRetrySeams(t)
	frames := []string{
		`data:{"body":"{\"code\":\"10605\",\"isQueued\":true,\"retryAfterSeconds\":30}","statusCodeValue":403}`,
	}
	server, _, _ := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected a queue error before the stream started")
	}
	var requestScoped interface{ IsRequestScoped() bool }
	if !errors.As(errExec, &requestScoped) || !requestScoped.IsRequestScoped() {
		t.Fatalf("error %v does not implement IsRequestScoped() == true", errExec)
	}
	var retryAfterProvider interface{ RetryAfter() *time.Duration }
	if !errors.As(errExec, &retryAfterProvider) {
		t.Fatal("queue error does not expose RetryAfter")
	}
	if wait := retryAfterProvider.RetryAfter(); wait == nil || *wait != 30*time.Second {
		t.Fatalf("RetryAfter() = %v, want 30s", wait)
	}
	var scoped interface{ IsCredentialScoped() bool }
	if errors.As(errExec, &scoped) && scoped.IsCredentialScoped() {
		t.Fatal("a queue error must not cool the credential")
	}
}

// qoderFirstFrameError runs one request whose first envelope carries body with
// the given statusCodeValue and returns the pre-stream error.
func qoderFirstFrameError(t *testing.T, status int, body string) error {
	t.Helper()
	frames := []string{
		`data:{"body":` + qoderQuoteJSON(body) + `,"statusCodeValue":` + fmt.Sprint(status) + `}`,
	}
	server, _, _ := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec != nil {
		return errExec
	}
	if res != nil {
		for range res.Chunks {
		}
	}
	t.Fatal("expected the first frame to fail the request before streaming")
	return nil
}

// TestQoderExecutorContentPolicyIsNotCredentialScoped proves a safety refusal
// does not cool the account: another credential would be refused identically.
func TestQoderExecutorContentPolicyIsNotCredentialScoped(t *testing.T) {
	errExec := qoderFirstFrameError(t, http.StatusForbidden, `{"code":"DataInspectionFailed","message":"input text data may contain inappropriate content"}`)
	var scoped interface{ IsCredentialScoped() bool }
	if errors.As(errExec, &scoped) && scoped.IsCredentialScoped() {
		t.Fatalf("a content-policy refusal must not cool the credential: %v", errExec)
	}
	var statusError interface{ StatusCode() int }
	if !errors.As(errExec, &statusError) || statusError.StatusCode() != http.StatusForbidden {
		t.Fatalf("error %v does not map to 403", errExec)
	}
}

// TestQoderExecutorContentPolicyMarkerAfterBodyLimitStillMatches guards the
// classification order: markers are matched against the full body, not the
// truncated copy echoed to the caller.
func TestQoderExecutorContentPolicyMarkerAfterBodyLimitStillMatches(t *testing.T) {
	padding := strings.Repeat("x", 2000)
	errExec := qoderFirstFrameError(t, http.StatusForbidden, `{"code":"err","message":"`+padding+` DataInspectionFailed"}`)
	var scoped interface{ IsCredentialScoped() bool }
	if errors.As(errExec, &scoped) && scoped.IsCredentialScoped() {
		t.Fatalf("a content-policy marker past the body limit must still be classified: %v", errExec)
	}
}

// TestQoderExecutorPlainForbiddenIsCredentialScoped keeps the fallback semantics:
// a 403 without queue/quota/policy markers is an authorization refusal.
func TestQoderExecutorPlainForbiddenIsCredentialScoped(t *testing.T) {
	errExec := qoderFirstFrameError(t, http.StatusForbidden, `{"message":"permission denied"}`)
	var scoped interface{ IsCredentialScoped() bool }
	if !errors.As(errExec, &scoped) || !scoped.IsCredentialScoped() {
		t.Fatalf("a plain 403 should remain credential-scoped: %v", errExec)
	}
}

func TestQoderExecutorServerErrorBeforeStream(t *testing.T) {
	// The 5xx is retried twice on the same credential, so the backoff seams are
	// stubbed to keep this assertion off the wall clock.
	qoderStubRetrySeams(t)
	server, _, _ := qoderStartServer(t, nil, http.StatusInternalServerError)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected an error for an upstream 500")
	}
	var statusError interface{ StatusCode() int }
	if !errors.As(errExec, &statusError) {
		t.Fatalf("error %v does not expose a status code", errExec)
	}
	if got := statusError.StatusCode(); got < 500 || got > 599 {
		t.Fatalf("StatusCode() = %d, want a 5xx", got)
	}
}

func TestQoderExecutorTruncatedStream(t *testing.T) {
	frames := []string{
		qoderEnvelopeFrame(`{"id":"chatcmpl-3","model":"qfmodel","choices":[{"index":0,"delta":{"content":"partial"}}]}`),
	}
	server, _, _ := qoderStartServer(t, frames, 0)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errStream := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	_, errDrain := qoderDrain(t, res)
	if errDrain == nil {
		t.Fatal("expected a truncation error for a stream without [DONE]")
	}
	var truncated interface{ IsStreamTruncated() bool }
	if !errors.As(errDrain, &truncated) || !truncated.IsStreamTruncated() {
		t.Fatalf("error %v does not report IsStreamTruncated()", errDrain)
	}
}

// qoderCallLog records every upstream call in arrival order.
type qoderCallLog struct {
	mu    sync.Mutex
	calls []qoderDecodedRequest
}

// append stores one call.
func (l *qoderCallLog) append(record qoderDecodedRequest) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, record)
}

// snapshot returns a copy of the recorded calls.
func (l *qoderCallLog) snapshot() []qoderDecodedRequest {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]qoderDecodedRequest, len(l.calls))
	copy(out, l.calls)
	return out
}

// qoderScriptedAnswer is one scripted upstream response.
type qoderScriptedAnswer struct {
	frames []string
	status int
}

// qoderStartScriptedServer answers the n-th call with the n-th answer. When the
// answers run out, the last one is repeated.
func qoderStartScriptedServer(t *testing.T, answers []qoderScriptedAnswer) (*httptest.Server, *qoderCallLog) {
	t.Helper()
	log := &qoderCallLog{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		log.append(qoderDecodedRequest{
			raw:    body,
			cosy:   r.Header.Clone(),
			url:    r.URL.RequestURI(),
			method: r.Method,
		})

		log.mu.Lock()
		index := len(log.calls) - 1
		log.mu.Unlock()
		if index >= len(answers) {
			index = len(answers) - 1
		}
		answer := answers[index]

		if answer.status != 0 && answer.status != http.StatusOK {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(answer.status)
			_, _ = w.Write([]byte(`{"code":"500","message":"upstream failure"}`))
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		for _, frame := range answer.frames {
			_, _ = fmt.Fprint(w, frame)
			_, _ = fmt.Fprint(w, "\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
	}))
	t.Cleanup(server.Close)
	return server, log
}

// qoderQueueAnswer is one scripted admission-queue rejection.
func qoderQueueAnswer(retryAfterSeconds int) qoderScriptedAnswer {
	inner := fmt.Sprintf(`{"code":"10605","isQueued":true,"retryAfterSeconds":%d}`, retryAfterSeconds)
	return qoderScriptedAnswer{frames: []string{
		`data:{"body":` + qoderQuoteJSON(inner) + `,"statusCodeValue":403}`,
	}}
}

// qoderSuccessAnswer is one scripted successful stream.
func qoderSuccessAnswer(text string) qoderScriptedAnswer {
	return qoderScriptedAnswer{frames: []string{
		qoderEnvelopeFrame(`{"id":"chatcmpl-r","model":"qfmodel","choices":[{"index":0,"delta":{"content":"` + text + `"},"finish_reason":"stop"}]}`),
		`data:{"body":"[DONE]"}`,
	}}
}

// qoderStubRetrySeams replaces the sleep and jitter seams so budget assertions
// never touch a wall clock. It returns the recorded waits.
func qoderStubRetrySeams(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	previousSleep := qoderSleepFunc
	previousJitter := qoderJitterFunc
	qoderSleepFunc = func(ctx context.Context, d time.Duration) error {
		if errCtx := ctx.Err(); errCtx != nil {
			return errCtx
		}
		waits = append(waits, d)
		return nil
	}
	qoderJitterFunc = func() time.Duration { return 0 }
	t.Cleanup(func() {
		qoderSleepFunc = previousSleep
		qoderJitterFunc = previousJitter
	})
	return &waits
}

// qoderAssertCosySignature recomputes the COSY signature from the bytes that
// actually went on the wire. The signed path excludes the query string and the
// leading /algo, and does not include the host, so a host failover must leave
// the signature valid.
func qoderAssertCosySignature(t *testing.T, record qoderDecodedRequest) {
	t.Helper()
	authorization := record.cosy.Get("authorization")
	parts := strings.Split(strings.TrimPrefix(authorization, "Bearer COSY."), ".")
	if len(parts) != 2 {
		t.Fatalf("authorization is not <payload>.<signature>: %q", authorization)
	}
	signedPath := record.url
	if idx := strings.Index(signedPath, "?"); idx >= 0 {
		signedPath = signedPath[:idx]
	}
	signedPath = strings.TrimPrefix(signedPath, "/algo")
	rawToSign := parts[0] + "\n" + record.cosy.Get("cosy-key") + "\n" + record.cosy.Get("cosy-date") + "\n" + string(record.raw) + "\n" + signedPath
	if want := fmt.Sprintf("%x", md5.Sum([]byte(rawToSign))); parts[1] != want {
		t.Fatalf("COSY signature mismatch: got %s, recomputed %s (signed path %q)", parts[1], want, signedPath)
	}
}

// TestQoderQueueRetriesWithIdenticalBody covers the admission-queue path: the
// executor waits and replays the SAME encoded body, so the upstream billing
// counters (request_id/request_set_id/session_id) stay one request.
func TestQoderQueueRetriesWithIdenticalBody(t *testing.T) {
	waits := qoderStubRetrySeams(t)
	server, calls := qoderStartScriptedServer(t, []qoderScriptedAnswer{
		qoderQueueAnswer(1),
		qoderSuccessAnswer("ok"),
	})

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errStream := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	payloads, errDrain := qoderDrain(t, res)
	if errDrain != nil {
		t.Fatalf("stream error = %v (a queue rejection must not cool the credential)", errDrain)
	}
	if !strings.Contains(strings.Join(payloads, ""), "ok") {
		t.Fatalf("stream did not carry the retried response: %v", payloads)
	}

	records := calls.snapshot()
	if len(records) != 2 {
		t.Fatalf("upstream calls = %d, want exactly 2", len(records))
	}
	if !bytes.Equal(records[0].raw, records[1].raw) {
		t.Fatal("the retried request body differs from the first one; retries must not rebuild the payload")
	}
	if len(*waits) != 1 {
		t.Fatalf("sleeps = %v, want exactly 1", *waits)
	}
	if got := (*waits)[0]; got != time.Second {
		t.Fatalf("queue wait = %v, want the advertised 1s (jitter stubbed to 0)", got)
	}
}

// TestQoderQueueBudgetExhaustedIsRequestScoped covers the bounded wait: the
// executor gives up after the attempt budget and hands back an error the
// conductor must not turn into a credential cooldown.
func TestQoderQueueBudgetExhaustedIsRequestScoped(t *testing.T) {
	waits := qoderStubRetrySeams(t)
	previousCap := qoderQueueSingleWaitCap
	qoderQueueSingleWaitCap = 5 * time.Second
	t.Cleanup(func() { qoderQueueSingleWaitCap = previousCap })

	// The advertised 60s exceeds the per-round cap, so each wait must clamp.
	server, calls := qoderStartScriptedServer(t, []qoderScriptedAnswer{qoderQueueAnswer(60)})

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected the queue budget to be exhausted")
	}

	var requestScoped interface{ IsRequestScoped() bool }
	if !errors.As(errExec, &requestScoped) || !requestScoped.IsRequestScoped() {
		t.Fatalf("error %v does not implement IsRequestScoped() == true", errExec)
	}
	var scoped interface{ IsCredentialScoped() bool }
	if errors.As(errExec, &scoped) && scoped.IsCredentialScoped() {
		t.Fatal("a queue timeout must not cool the credential")
	}

	if got, want := len(calls.snapshot()), qoderQueueMaxAttempts; got != want {
		t.Fatalf("upstream calls = %d, want the attempt budget of %d", got, want)
	}
	if len(*waits) == 0 {
		t.Fatal("no wait was recorded before the budget ran out")
	}
	for _, wait := range *waits {
		if wait > qoderQueueSingleWaitCap {
			t.Fatalf("wait %v exceeds the per-round cap %v", wait, qoderQueueSingleWaitCap)
		}
	}
}

// TestQoderQueueTotalBudgetStopsRetrying covers the aggregate budget: even with
// attempts left, a wait that would overflow the total is refused.
func TestQoderQueueTotalBudgetStopsRetrying(t *testing.T) {
	waits := qoderStubRetrySeams(t)
	previousCap := qoderQueueSingleWaitCap
	previousTotal := qoderQueueTotalBudget
	qoderQueueSingleWaitCap = 100 * time.Second
	qoderQueueTotalBudget = 150 * time.Second
	t.Cleanup(func() {
		qoderQueueSingleWaitCap = previousCap
		qoderQueueTotalBudget = previousTotal
	})

	server, calls := qoderStartScriptedServer(t, []qoderScriptedAnswer{qoderQueueAnswer(100)})

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected the total queue budget to be exhausted")
	}
	var requestScoped interface{ IsRequestScoped() bool }
	if !errors.As(errExec, &requestScoped) || !requestScoped.IsRequestScoped() {
		t.Fatalf("error %v does not implement IsRequestScoped() == true", errExec)
	}
	// 100s fits, the second 100s would total 200s > 150s, so only one wait and
	// two upstream calls happen.
	if got := len(calls.snapshot()); got != 2 {
		t.Fatalf("upstream calls = %d, want 2 (the second wait would overflow the total budget)", got)
	}
	if len(*waits) != 1 {
		t.Fatalf("sleeps = %v, want exactly 1", *waits)
	}
}

// TestQoderRetryTransientServerError covers the same-credential replay policy:
// 5xx is retried twice on the same host, then succeeds.
func TestQoderRetryTransientServerError(t *testing.T) {
	waits := qoderStubRetrySeams(t)
	server, calls := qoderStartScriptedServer(t, []qoderScriptedAnswer{
		{status: http.StatusInternalServerError},
		{status: http.StatusBadGateway},
		qoderSuccessAnswer("recovered"),
	})

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errStream := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	payloads, errDrain := qoderDrain(t, res)
	if errDrain != nil {
		t.Fatalf("stream error = %v", errDrain)
	}
	if !strings.Contains(strings.Join(payloads, ""), "recovered") {
		t.Fatalf("stream did not carry the retried response: %v", payloads)
	}
	if got := len(calls.snapshot()); got != 3 {
		t.Fatalf("upstream calls = %d, want 3 (initial + 2 transient retries)", got)
	}
	if len(*waits) != 2 || (*waits)[0] != time.Second || (*waits)[1] != 2*time.Second {
		t.Fatalf("backoff = %v, want [1s 2s]", *waits)
	}
}

// TestQoderRetryTransientServerErrorExhausted covers the give-up path: after the
// retry allowance the failure becomes a normal upstream error.
func TestQoderRetryTransientServerErrorExhausted(t *testing.T) {
	waits := qoderStubRetrySeams(t)
	server, calls := qoderStartScriptedServer(t, []qoderScriptedAnswer{
		{status: http.StatusInternalServerError},
	})

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected the transient retries to be exhausted")
	}
	if got := len(calls.snapshot()); got != 3 {
		t.Fatalf("upstream calls = %d, want 3 (initial + 2 transient retries)", got)
	}
	if len(*waits) != 2 {
		t.Fatalf("backoff = %v, want two waits", *waits)
	}
	var statusError interface{ StatusCode() int }
	if !errors.As(errExec, &statusError) || statusError.StatusCode() != http.StatusBadGateway {
		t.Fatalf("error %v does not map to 502", errExec)
	}
	// A plain 5xx carries no queue signal and stays credential-scoped so the
	// conductor rotates the account.
	var requestScoped interface{ IsRequestScoped() bool }
	if errors.As(errExec, &requestScoped) && requestScoped.IsRequestScoped() {
		t.Fatal("an exhausted 5xx must not stay request-scoped")
	}
}

// qoderFailoverTransport records the host of every attempt and optionally
// refuses the transport for a configured set of hosts.
type qoderFailoverTransport struct {
	mu sync.Mutex
	// refuse lists the hosts whose dial fails before any HTTP response exists.
	refuse map[string]bool
	hosts  []string
	calls  []qoderDecodedRequest
}

// RoundTrip implements http.RoundTripper.
func (f *qoderFailoverTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	record := qoderDecodedRequest{
		raw:    body,
		cosy:   req.Header.Clone(),
		url:    req.URL.RequestURI(),
		method: req.Method,
	}
	f.mu.Lock()
	f.hosts = append(f.hosts, req.URL.Host)
	f.calls = append(f.calls, record)
	refuse := f.refuse[req.URL.Host]
	f.mu.Unlock()

	if refuse {
		return nil, errors.New("dial tcp: connection refused")
	}

	payload := `data:{"body":` + qoderQuoteJSON(`{"id":"chatcmpl-f","model":"qfmodel","choices":[{"index":0,"delta":{"content":"failover"},"finish_reason":"stop"}]}`) + `,"statusCodeValue":200}` + "\n\n" +
		`data:{"body":"[DONE]"}` + "\n\n"
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
		Request:    req,
	}, nil
}

// snapshotHosts returns the recorded hosts and calls.
func (f *qoderFailoverTransport) snapshot() ([]string, []qoderDecodedRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hosts := make([]string, len(f.hosts))
	copy(hosts, f.hosts)
	calls := make([]qoderDecodedRequest, len(f.calls))
	copy(calls, f.calls)
	return hosts, calls
}

// TestQoderRetryINTLFailsOverToNextHost covers the international host list: a
// transport-level failure on api3 advances to api2, and the COSY signature stays
// valid because it covers the path, not the host.
func TestQoderRetryINTLFailsOverToNextHost(t *testing.T) {
	qoderStubRetrySeams(t)
	transport := &qoderFailoverTransport{refuse: map[string]bool{"api3.qoder.sh": true}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)

	auth := qoderTestAuth("")
	auth.Provider = constant.QoderINTL

	e := NewQoderExecutor(&config.Config{}, constant.QoderINTL)
	res, errStream := e.ExecuteStream(ctx, auth, qoderTestRequest(), qoderTestOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	payloads, errDrain := qoderDrain(t, res)
	if errDrain != nil {
		t.Fatalf("stream error = %v", errDrain)
	}
	if !strings.Contains(strings.Join(payloads, ""), "failover") {
		t.Fatalf("stream did not carry the failover response: %v", payloads)
	}

	hosts, calls := transport.snapshot()
	want := []string{"api3.qoder.sh", "api2.qoder.sh"}
	if len(hosts) != len(want) {
		t.Fatalf("hosts = %v, want %v", hosts, want)
	}
	for i := range want {
		if hosts[i] != want[i] {
			t.Fatalf("hosts = %v, want %v", hosts, want)
		}
	}
	if !bytes.Equal(calls[0].raw, calls[1].raw) {
		t.Fatal("the failover request body differs from the first one")
	}
	// The signed path is identical on both hosts, so the recomputation succeeds
	// for the second (api2) request as well.
	qoderAssertCosySignature(t, calls[1])
}

// TestQoderRetryCNNeverFailsOver locks the China realm: one host, so repeated
// transport failures stay on it instead of probing an unconfigured gateway.
func TestQoderRetryCNNeverFailsOver(t *testing.T) {
	qoderStubRetrySeams(t)
	transport := &qoderFailoverTransport{refuse: map[string]bool{"gateway.qoder.com.cn": true}}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errExec := e.ExecuteStream(ctx, qoderTestAuth(""), qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected the CN transport failures to end in an error")
	}

	hosts, _ := transport.snapshot()
	if len(hosts) != 3 {
		t.Fatalf("calls = %v, want 3 attempts on the single CN host", hosts)
	}
	for _, host := range hosts {
		if host != "gateway.qoder.com.cn" {
			t.Fatalf("CN must not fail over, but called %q", host)
		}
	}
}

// TestQoderRetryHTTPStatusDoesNotFailOver proves a reachable host answering 5xx
// is not treated as a transport failure: the host stays put.
func TestQoderRetryHTTPStatusDoesNotFailOver(t *testing.T) {
	qoderStubRetrySeams(t)
	transport := &qoderFailoverTransport{}
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", transport)

	auth := qoderTestAuth("")
	auth.Provider = constant.QoderINTL
	e := NewQoderExecutor(&config.Config{}, constant.QoderINTL)

	// api3 answers (a real failure), api2 would succeed -- but it must never be
	// asked, because the transport delivered a response.
	serverStatus := http.StatusInternalServerError
	previousFactory := qoderNewHTTPClientFunc
	qoderNewHTTPClientFunc = func(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
		client := previousFactory(ctx, cfg, auth, timeout)
		client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			transport.mu.Lock()
			transport.hosts = append(transport.hosts, req.URL.Host)
			transport.calls = append(transport.calls, qoderDecodedRequest{url: req.URL.RequestURI(), cosy: req.Header.Clone(), method: req.Method})
			transport.mu.Unlock()
			return &http.Response{
				StatusCode: serverStatus,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{"message":"boom"}`)),
				Request:    req,
			}, nil
		})
		return client
	}
	t.Cleanup(func() { qoderNewHTTPClientFunc = previousFactory })

	res, errExec := e.ExecuteStream(ctx, auth, qoderTestRequest(), qoderTestOptions())
	if errExec == nil {
		if res != nil {
			for range res.Chunks {
			}
		}
		t.Fatal("expected the 5xx retries to be exhausted")
	}
	hosts, _ := transport.snapshot()
	if len(hosts) != 3 {
		t.Fatalf("calls = %v, want 3 attempts against api3", hosts)
	}
	for _, host := range hosts {
		if host != "api3.qoder.sh" {
			t.Fatalf("an HTTP 5xx must not fail over, but called %q", host)
		}
	}
}

// TestQoderRetryNeverConfiguresClientTimeout proves the upstream client carries
// no wall-clock timeout: AGENTS.md forbids post-connect timeouts.
func TestQoderRetryNeverConfiguresClientTimeout(t *testing.T) {
	qoderStubRetrySeams(t)
	server, _ := qoderStartScriptedServer(t, []qoderScriptedAnswer{qoderSuccessAnswer("ok")})

	var timeouts []time.Duration
	previousFactory := qoderNewHTTPClientFunc
	qoderNewHTTPClientFunc = func(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
		client := previousFactory(ctx, cfg, auth, timeout)
		timeouts = append(timeouts, client.Timeout)
		return client
	}
	t.Cleanup(func() { qoderNewHTTPClientFunc = previousFactory })

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	res, errStream := e.ExecuteStream(context.Background(), qoderTestAuth(server.URL), qoderTestRequest(), qoderTestOptions())
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	if _, errDrain := qoderDrain(t, res); errDrain != nil {
		t.Fatalf("stream error = %v", errDrain)
	}
	if len(timeouts) == 0 {
		t.Fatal("the executor never built an HTTP client")
	}
	for _, timeout := range timeouts {
		if timeout != 0 {
			t.Fatalf("http.Client.Timeout = %v, want no timeout on an upstream connection", timeout)
		}
	}
}

// TestQoderExecutorRefreshNoCredential locks the no-op path: a credential with
// neither a refresh token nor a personal token is already valid and must be
// returned unchanged rather than reported as a failure.
func TestQoderExecutorRefreshNoCredential(t *testing.T) {
	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)

	plain := &cliproxyauth.Auth{ID: "qoder-plain", Provider: constant.QoderCN, Metadata: map[string]any{"access_token": "token"}}
	refreshed, errPlain := e.Refresh(context.Background(), plain)
	if errPlain != nil {
		t.Fatalf("Refresh() with no refresh token should be a no-op, got %v", errPlain)
	}
	if refreshed != plain {
		t.Fatal("Refresh() should return the same auth when there is nothing to rotate")
	}
}

// TestQoderExecutorRefreshRotatesDeviceToken covers the real rotation path: a
// drt- refresh token goes to the device-token endpoint and the rotated values
// land in metadata while the identity is preserved.
func TestQoderExecutorRefreshRotatesDeviceToken(t *testing.T) {
	var sawPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"token":"dt-rotated","refresh_token":"drt-rotated","token_type":"Bearer","expires_in":3600000}`)
	}))
	t.Cleanup(server.Close)

	e := NewQoderExecutor(&config.Config{}, constant.QoderCN)
	auth := &cliproxyauth.Auth{
		ID:       "qoder-rotate",
		Provider: constant.QoderCN,
		Metadata: map[string]any{
			"access_token":    "dt-old",
			"refresh_token":   "drt-old",
			"uid":             "uid-fixture-001",
			"user_type":       "personal_professional_trial",
			"organization_id": "1234567890123456789",
			"base_url":        server.URL,
		},
	}
	// The realm openapi host is a package constant, so the client factory is
	// pointed at the test server instead.
	previous := qoderRefreshClientFactory
	qoderRefreshClientFactory = func(cfg *config.Config, realm qoderauthsvc.RealmConfig, proxyURL string) *qoderauthsvc.Client {
		realm.OpenAPIBase = server.URL
		return qoderauthsvc.NewClientWithProxyURL(cfg, realm, proxyURL)
	}
	t.Cleanup(func() { qoderRefreshClientFactory = previous })

	refreshed, errRefresh := e.Refresh(context.Background(), auth)
	if errRefresh != nil {
		t.Fatalf("Refresh() error = %v", errRefresh)
	}
	if sawPath != "/api/v1/deviceToken/refresh" {
		t.Fatalf("refresh path = %q, want the device-token endpoint", sawPath)
	}
	if got, _ := refreshed.Metadata["access_token"].(string); got != "dt-rotated" {
		t.Fatalf("access_token = %q", got)
	}
	if got, _ := refreshed.Metadata["refresh_token"].(string); got != "drt-rotated" {
		t.Fatalf("refresh_token = %q", got)
	}
	if got, _ := refreshed.Metadata["uid"].(string); got != "uid-fixture-001" {
		t.Fatalf("uid = %q, want the identity preserved across rotation", got)
	}
	if got, _ := refreshed.Metadata["organization_id"].(string); got != "1234567890123456789" {
		t.Fatalf("organization_id = %q, want the identity preserved across rotation", got)
	}
	if _, has := refreshed.Metadata["last_refresh"]; !has {
		t.Fatal("last_refresh must be recorded after a successful rotation")
	}
}
