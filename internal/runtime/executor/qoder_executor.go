// Package executor provides per-provider runtime executors.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"

	qoderauthsvc "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/qoder"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

const (
	// qoderChatPath is the upstream inference endpoint. The gateway signs the
	// path only (query excluded), and the AgentId query parameter must mirror the
	// body's agent_id field.
	qoderChatPath = "/algo/api/v2/service/pro/sse/agent_chat_generation" +
		"?FetchKeys=llm_model_result&AgentId=agent_common&Encode=1"

	// qoderCNGatewayBaseURL is the single China-mainland gateway host.
	qoderCNGatewayBaseURL = "https://gateway.qoder.com.cn"

	// qoderINTLGatewayBaseURL is the first international gateway candidate.
	qoderINTLGatewayBaseURL = "https://api3.qoder.sh"
)

// qoderCNGatewayHosts is the CN gateway list: one host, no failover.
var qoderCNGatewayHosts = []string{qoderCNGatewayBaseURL}

// qoderINTLGatewayHosts is the INTL failover order. Failover only happens on
// transport-layer failures: an HTTP status from api3 is a real answer, not a
// reason to ask api2. The COSY signature covers the path only, so switching
// hosts does not invalidate it.
var qoderINTLGatewayHosts = []string{
	qoderINTLGatewayBaseURL,
	"https://api2.qoder.sh",
	"https://api1.qoder.sh",
}

// Upstream quota/queue signal markers. The gateway answers the HTTP handshake
// with 200 and delivers these inside the first SSE envelope, so they are matched
// against the envelope text.
const (
	qoderQueueCodeMarker    = "10605"
	qoderQueueFlagMarker    = "isQueued"
	qoderResourceLimitMark  = "RESOURCE_LIMIT"
	qoderQuotaExhaustedCode = "112"
	qoderQuotaDailyCode     = "110"
	qoderQuotaPricingMarker = "pricingUrl"
	qoderQuotaDailyMarker   = "Billing daily count exceeded"
)

// qoderContentPolicyMarkers are the upstream safety-refusal signals. A content
// rejection is a deterministic fault of the request itself: replaying it on
// another credential produces the same refusal, so it must never cool the
// credential. The list mirrors the reference implementation exactly
// (qoder_proxy.py:6855-6861); "safety" is deliberately absent because it matches
// unrelated text.
var qoderContentPolicyMarkers = []string{
	"DataInspectionFailed",
	"inappropriate content",
	"input text data may contain",
	"ContentFilter",
	"SensitiveContent",
}

// qoderRefreshClientFactory builds the credential-refresh client. It is a
// variable so tests can point the refresh at an httptest server; production
// always uses the real proxy-aware constructor.
var qoderRefreshClientFactory = qoderauthsvc.NewClientWithProxyURL

// qoderRetryAfterPattern extracts retryAfterSeconds from an envelope payload.
// Backslashes are stripped before matching, so both the raw and the escaped
// JSON string forms resolve.
var qoderRetryAfterPattern = regexp.MustCompile(`(?i)retryafterseconds"?\s*:\s*(\d+)`)

// Retry budgets (design.md 5.2 / 5.3).
//
// The gateway answers the HTTP handshake with 200 and reports admission queueing
// inside the first SSE envelope, so every retry decision happens before the
// stream is handed to the caller. The budgets are package variables so tests can
// shrink them instead of waiting on a wall clock.
var (
	// qoderQueueMaxAttempts caps the upstream calls made while the model reports
	// a full admission queue. Reaching it hands the queue error to the client
	// rather than cooling the credential.
	qoderQueueMaxAttempts = 3
	// qoderQueueSingleWaitCap bounds a single wait, whatever the upstream asked.
	qoderQueueSingleWaitCap = 180 * time.Second
	// qoderQueueTotalBudget bounds the sum of all waits in one execution.
	qoderQueueTotalBudget = 600 * time.Second
	// qoderQueueDefaultWait applies when the queue signal carries no hint.
	qoderQueueDefaultWait = 30 * time.Second
	// qoderQueueJitterMax bounds the random extra wait added to a queue delay.
	qoderQueueJitterMax = 2 * time.Second
	// qoderTransientRetries is how often a transport/5xx/418 failure is replayed
	// on the same credential before it becomes a credential rotation.
	qoderTransientRetries = 2
	// qoderTransientBackoff is the delay before transient retry 1 and 2.
	qoderTransientBackoff = []time.Duration{time.Second, 2 * time.Second}
)

// qoderSleepFunc waits for d unless ctx is cancelled. Tests replace it so the
// budget assertions never touch wall-clock time. Production uses a timer:
// AGENTS.md forbids post-connect http.Client timeouts, not request-scoped waits.
var qoderSleepFunc = func(ctx context.Context, d time.Duration) error {
	if errCtx := ctx.Err(); errCtx != nil {
		return errCtx
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// qoderJitterFunc returns the extra wait added to a queue delay.
var qoderJitterFunc = func() time.Duration {
	if qoderQueueJitterMax <= 0 {
		return 0
	}
	return time.Duration(rand.Int64N(int64(qoderQueueJitterMax) + 1))
}

// qoderNewHTTPClientFunc builds the upstream client. It is a variable so tests
// can capture the client and assert no wall-clock timeout is ever configured.
var qoderNewHTTPClientFunc = helps.NewProxyAwareHTTPClient

// QoderExecutor talks to the Qoder (CN or INTL) agent gateway.
//
// The upstream protocol is not OpenAI-compatible on the wire: the request body
// is custom-Base64 encoded and carries a COSY signature over those encoded
// bytes, and the response is an SSE envelope wrapping inner OpenAI chunks as
// JSON strings. Both steps are protocol-specific, so the OpenAI-compatible
// executor cannot be reused for execution; only CountTokens is inherited.
//
// The response format on the wire is always streamed. Execute forces streaming
// and folds the chunks back into a single chat.completion for non-streaming
// clients.
type QoderExecutor struct {
	*OpenAICompatExecutor

	provider string
	cfg      *config.Config
}

// NewQoderExecutor constructs a Qoder executor bound to one realm.
func NewQoderExecutor(cfg *config.Config, provider string) *QoderExecutor {
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = constant.QoderCN
	}
	return &QoderExecutor{
		OpenAICompatExecutor: NewOpenAICompatExecutor(provider, cfg),
		provider:             provider,
		cfg:                  cfg,
	}
}

// Identifier returns the provider key this executor is bound to.
func (e *QoderExecutor) Identifier() string {
	if e == nil {
		return constant.QoderCN
	}
	if e.provider != "" {
		return e.provider
	}
	return constant.QoderCN
}

// UsesConfig reports whether the executor is bound to the provided config
// snapshot. Method promotion does not dispatch through embedding, so this is
// written out explicitly.
func (e *QoderExecutor) UsesConfig(cfg *config.Config) bool {
	return e != nil && e.cfg == cfg
}

// SupportsApplyPatch reports that Qoder has no apply_patch bridge. The embedded
// OpenAI-compatible executor reports true for any non-nil receiver, so this must
// be overridden.
func (e *QoderExecutor) SupportsApplyPatch() bool { return false }

// ForAPIKey binds an API-key-scoped configuration view. Value receivers keep the
// shared transport state intact.
func (e QoderExecutor) ForAPIKey() cliproxyauth.ProviderExecutor {
	e.cfg = e.cfg.ForAPIKey()
	if e.OpenAICompatExecutor != nil {
		e.OpenAICompatExecutor = e.OpenAICompatExecutor.ForAPIKey().(*OpenAICompatExecutor)
	}
	return &e
}

// Execute drives the always-streaming upstream and folds the result into a
// single non-streaming response in the client's format.
func (e *QoderExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	// The chunk stream must stay in OpenAI format so it can be folded; the folded
	// payload is translated to the client format afterwards.
	opts.Stream = true
	opts.ResponseFormat = sdktranslator.FromString("openai")

	streamResult, streamReq, errStream := e.executeQoderStream(ctx, auth, req, opts)
	if errStream != nil {
		return resp, errStream
	}
	if streamResult == nil {
		return resp, nil
	}

	var buffer bytes.Buffer
	for {
		select {
		case <-ctx.Done():
			return resp, ctx.Err()
		case chunk, ok := <-streamResult.Chunks:
			if !ok {
				aggregated := helps.AggregateOpenAIStreamChunks(buffer.Bytes())
				out := aggregated
				if responseFormat != sdktranslator.FormatOpenAI {
					var param any
					out = sdktranslator.TranslateNonStream(ctx, sdktranslator.FromString("openai"), responseFormat, req.Model, streamReq.originalPayload, streamReq.translated, aggregated, &param)
					if len(out) == 0 {
						return resp, statusErr{code: http.StatusBadGateway, msg: "qoder executor: empty aggregated response"}
					}
				}
				return cliproxyexecutor.Response{Payload: out, Headers: streamResult.Headers}, nil
			}
			if chunk.Err != nil {
				return resp, chunk.Err
			}
			if len(chunk.Payload) > 0 {
				_, _ = buffer.Write(chunk.Payload)
				_, _ = buffer.Write([]byte("\n"))
			}
		}
	}
}

// ExecuteStream runs the upstream request and returns the decoded chunk stream.
func (e *QoderExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	opts.Stream = true
	res, _, err := e.executeQoderStream(ctx, auth, req, opts)
	return res, err
}

// qoderStreamRequest carries the request bytes the caller needs for the final
// non-streaming translation.
type qoderStreamRequest struct {
	originalPayload []byte
	translated      []byte
}

// qoderAttemptInput holds everything one upstream attempt needs.
//
// It is built exactly once per client request. Retries reuse the same encoded
// body: rebuilding would regenerate request_id/request_set_id and session_id,
// which are the upstream billing and quota counters, so a retry of what the
// client sees as one request must not look like a second billable request.
type qoderAttemptInput struct {
	encoded    string
	translated []byte
	modelKey   string
	session    *helps.CosySession
	auth       *cliproxyauth.Auth
	headers    http.Header
	reporter   *helps.UsageReporter
}

// qoderOpenedStream is a stream that passed the first-frame gate.
type qoderOpenedStream struct {
	resp    *http.Response
	scanner *bufio.Scanner
	first   qoderPeekedFrame
}

// qoderTransportError marks a failure below HTTP: no response was received.
// Only these advance the INTL gateway host list; an HTTP status -- even a 5xx --
// is a real answer from a reachable host and must not move to the next one.
type qoderTransportError struct {
	err error
}

// Error implements error.
func (e *qoderTransportError) Error() string {
	if e == nil || e.err == nil {
		return "qoder executor: upstream transport failure"
	}
	return e.err.Error()
}

// Unwrap exposes the underlying transport error.
func (e *qoderTransportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// executeQoderStream performs the shared request build, first-frame peek and
// stream forwarding used by both Execute and ExecuteStream.
func (e *QoderExecutor) executeQoderStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, _ qoderStreamRequest, err error) {
	var request qoderStreamRequest

	ctx = helps.EnsureSessionContext(ctx, opts, req.Payload)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	auth = qoderPreparedAuth(auth)
	if auth == nil {
		return nil, request, statusErr{code: http.StatusUnauthorized, msg: "qoder executor: auth is nil"}
	}
	if _, _, errCreds := qoderCredentials(auth); errCreds != nil {
		return nil, request, errCreds
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")

	request.originalPayload = req.Payload
	if len(opts.OriginalRequest) > 0 {
		request.originalPayload = opts.OriginalRequest
	}

	isCompat := helps.APIKeyModelIsCompat(req)
	targetModel := resolveResponsesSignatureTargetModel(baseModel, req, opts)
	originalTranslated, translated, updatesChanged, errTrans := helps.TranslateRequestPairWithAPIKeyModelCompatibilityAndUpdateIntent(
		ctx, opts.Headers, e.cfg, from, to, targetModel, request.originalPayload, req.Payload, true, isCompat)
	if errTrans != nil {
		return nil, request, errTrans
	}
	originalTranslated = helps.SetStringIfDifferent(originalTranslated, "model", baseModel)

	doc := ojson.NewDoc(translated)
	helps.SetStringIfDifferentDoc(doc, "model", baseModel)

	if errThink := helps.ApplyRequestThinkingDoc(doc, req, opts, from.String(), to.String(), e.Identifier(), updatesChanged); errThink != nil {
		return nil, request, errThink
	}

	// The body builder replaces the whole document with the Qoder envelope, so it
	// receives the translated payload separately instead of reading the document.
	meta := qoderCatalogMeta(req, baseModel)
	userType := qoderMetadataString(auth, "user_type")
	sessionID := util.SessionIDFromContext(ctx)
	if errBody := helps.BuildQoderBodyDoc(doc, doc.Bytes(), meta, userType, sessionID); errBody != nil {
		return nil, request, statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("qoder executor: build request body: %v", errBody)}
	}

	// Payload configuration is the final semantic barrier: it runs on the finished
	// Qoder envelope, and nothing below may change business payload fields.
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	helps.ApplyPayloadConfigWithRequestDoc(e.cfg, baseModel, to.String(), from.String(), "", doc, originalTranslated, requestedModel, requestPath, opts.Headers)

	request.translated = doc.Bytes()
	reporter.SetTranslatedReasoningEffort(request.translated, to.String())

	// The COSY signature covers the ENCODED body, so encoding must follow the last
	// payload mutation and precede header construction.
	encoded := helps.QoderEncode(request.translated)

	uid, accessToken, errCreds := qoderCredentials(auth)
	if errCreds != nil {
		return nil, request, errCreds
	}
	session, errSession := helps.NewCosySession(
		uid,
		qoderMetadataString(auth, "nickname"),
		accessToken,
		qoderMetadataString(auth, "refresh_token"),
		userType,
		qoderMetadataString(auth, "organization_id"),
		qoderMetadataString(auth, "organization_name"),
	)
	if errSession != nil {
		return nil, request, statusErr{code: http.StatusInternalServerError, msg: fmt.Sprintf("qoder executor: build cosy session: %v", errSession)}
	}

	attempt := &qoderAttemptInput{
		encoded:    encoded,
		translated: request.translated,
		modelKey:   meta.Key,
		session:    session,
		auth:       auth,
		headers:    opts.Headers,
		reporter:   reporter,
	}

	// The COSY signature covers the path only, so a host swap keeps it valid.
	// Headers are therefore built per attempt, not once per request.
	opened, errOpen := e.openQoderStream(ctx, baseModel, attempt)
	if errOpen != nil {
		return nil, request, errOpen
	}
	httpResp := opened.resp
	scanner := opened.scanner
	first := opened.first

	claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, request.originalPayload)
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("qoder executor: close response body error: %v", errClose)
			}
		}()

		var streamUsage helps.StreamUsageBuffer
		var param any
		var terminalSent bool
		defer func() {
			if terminalSent {
				streamUsage.Publish(ctx, reporter)
				reporter.EnsurePublished(ctx)
			}
		}()

		sendPayload := func(payload []byte) bool {
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, request.originalPayload, request.translated, payload, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		publishFailure := func(streamErr error) {
			helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
			if !streamUsage.PublishFailure(ctx, reporter, streamErr) {
				reporter.PublishFailure(ctx, streamErr)
			}
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
		}
		processInner := func(inner []byte) bool {
			streamUsage.ObserveOpenAIStream(inner)
			helps.ObserveChatTokenEvent(reporter, inner)
			payload := inner
			chunkDoc := ojson.NewDoc(inner)
			if changed, drop := helps.CleanQoderChunk(chunkDoc); drop {
				return true
			} else if changed {
				payload = chunkDoc.Bytes()
			}
			return sendPayload(append([]byte("data: "), payload...))
		}

		if first.done {
			terminalSent = true
			_ = sendPayload([]byte("data: [DONE]"))
			return
		}
		if len(first.inner) > 0 {
			if !processInner(first.inner) {
				return
			}
		}

		for scanner.Scan() {
			if ctx.Err() != nil {
				return
			}
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			inner, _, done, errFrame := helps.DecodeQoderStreamFrame(line)
			if errFrame != nil {
				publishFailure(qoderFrameError(errFrame, line))
				return
			}
			if done {
				terminalSent = true
				_ = sendPayload([]byte("data: [DONE]"))
				return
			}
			if len(inner) == 0 {
				continue
			}
			if !processInner(inner) {
				return
			}
		}

		if ctx.Err() != nil {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			publishFailure(statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("qoder executor: read upstream stream: %v", errScan)})
			return
		}
		// A clean EOF without the terminal frame means the upstream stopped
		// mid-generation. The request was already accepted upstream, so this is a
		// truncated stream rather than a credential fault.
		truncated := statusErr{code: http.StatusBadGateway, msg: "qoder executor: upstream stream closed before [DONE]", streamTruncated: true}
		helps.RecordAPIResponseError(ctx, e.cfg, truncated)
		select {
		case out <- cliproxyexecutor.StreamChunk{Err: truncated}:
		case <-ctx.Done():
		}
	}()

	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, request, nil
}

// openQoderStream drives the attempt loop: admission-queue waits, transient
// retries and INTL host failover all happen here, before any byte reaches the
// client.
//
// Every retry replays the same encoded body. request_id, request_set_id and
// session_id drive upstream billing and quota accounting, so regenerating them
// for a retry would turn one client request into several billable ones.
func (e *QoderExecutor) openQoderStream(ctx context.Context, baseModel string, in *qoderAttemptInput) (*qoderOpenedStream, error) {
	hosts := qoderGatewayHosts(in.auth, e.Identifier())
	hostIdx := 0
	queueRounds := 0
	transientRounds := 0
	totalWaited := time.Duration(0)

	for {
		url := hosts[hostIdx] + qoderChatPath
		opened, errAttempt := e.doQoderAttempt(ctx, baseModel, in, url)
		if errAttempt == nil {
			return opened, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}

		// Queueing is its own budget: the model is busy, the credential is
		// healthy, so it never consumes the transient retry allowance.
		var queued *qoderQueuedError
		if errors.As(errAttempt, &queued) {
			queueRounds++
			if queueRounds >= qoderQueueMaxAttempts {
				return nil, qoderQueueBudgetExhausted(queued, queueRounds, totalWaited)
			}
			wait := qoderQueueWait(queued.retryAfter)
			if totalWaited+wait > qoderQueueTotalBudget {
				return nil, qoderQueueBudgetExhausted(queued, queueRounds, totalWaited)
			}
			log.WithFields(log.Fields{
				"provider": e.Identifier(),
				"model":    baseModel,
				"round":    queueRounds,
				"wait":     wait.String(),
			}).Debug("qoder executor: upstream queue full, waiting before retry")
			if errSleep := qoderSleepFunc(ctx, wait); errSleep != nil {
				return nil, errSleep
			}
			totalWaited += wait
			continue
		}

		if !qoderIsTransientFailure(errAttempt) {
			return nil, errAttempt
		}

		// A transport failure means no HTTP answer arrived: the host may be
		// unreachable, so INTL advances to the next candidate. An HTTP status,
		// even a 5xx, is a real answer from a live host and keeps it.
		var transportErr *qoderTransportError
		if errors.As(errAttempt, &transportErr) && hostIdx+1 < len(hosts) {
			hostIdx++
			// A host swap is itself the retry: it does not consume the same-host
			// allowance and needs no backoff, since the failed host is not the
			// one about to be called.
			log.WithFields(log.Fields{
				"provider": e.Identifier(),
				"model":    baseModel,
				"host":     hosts[hostIdx],
			}).Debug("qoder executor: transport failure, failing over to the next gateway host")
			continue
		}

		if transientRounds >= qoderTransientRetries {
			// The retry allowance is spent. A bare transport error carries no
			// status, so it is promoted to a 502 here; every retry already used the
			// same credential, which leaves the conductor free to rotate.
			if errors.As(errAttempt, &transportErr) {
				return nil, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("qoder executor: upstream request failed after %d attempts: %v", transientRounds+1, transportErr)}
			}
			return nil, errAttempt
		}
		wait := qoderTransientBackoffDelay(transientRounds)
		transientRounds++
		log.WithFields(log.Fields{
			"provider": e.Identifier(),
			"model":    baseModel,
			"round":    transientRounds,
			"wait":     wait.String(),
			"host":     hosts[hostIdx],
		}).Debug("qoder executor: transient upstream failure, retrying")
		if errSleep := qoderSleepFunc(ctx, wait); errSleep != nil {
			return nil, errSleep
		}
	}
}

// doQoderAttempt performs one signed upstream call and peeks the first frame.
func (e *QoderExecutor) doQoderAttempt(ctx context.Context, baseModel string, in *qoderAttemptInput, url string) (*qoderOpenedStream, error) {
	// The COSY signature covers the path only, so the header set is rebuilt per
	// attempt: only the target host changes across a failover.
	cosyHeaders := in.session.Headers(in.encoded, url, in.modelKey, true)

	httpReq, errNew := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(in.encoded))
	if errNew != nil {
		return nil, errNew
	}
	for name, value := range cosyHeaders {
		httpReq.Header.Set(name, value)
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, in.auth.Attributes, in.headers)

	var authID, authLabel, authType, authValue string
	if in.auth != nil {
		authID = in.auth.ID
		authLabel = in.auth.Label
		authType, authValue = in.auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      []byte(in.encoded),
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	// No client timeout: the connection may stay open for a long generation, and
	// AGENTS.md forbids post-connect timeouts. Waits are request-scoped timers.
	httpClient := qoderNewHTTPClientFunc(ctx, e.cfg, in.auth, 0)
	httpClient = in.reporter.TrackHTTPClientRoundTripOnly(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, &qoderTransportError{err: errDo}
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		body, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("qoder executor: close response body error: %v", errClose)
		}
		log.WithFields(log.Fields{
			"provider": e.Identifier(),
			"model":    baseModel,
			"status":   httpResp.StatusCode,
		}).Debugf("qoder executor: upstream rejected the request: %s", helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return nil, qoderClassifyUpstreamError(httpResp.StatusCode, string(body))
	}

	scanner := bufio.NewScanner(httpResp.Body)
	scanner.Buffer(nil, helps.StreamScannerBuffer)

	// The upstream always answers the handshake with HTTP 200 and delivers
	// business failures (queueing, quota, provider faults) inside the first
	// envelope. Peek it synchronously so the caller can still retry or select
	// another credential instead of having a 200 already handed to the client.
	first, errFirst := qoderPeekFirstFrame(ctx, e, scanner, in.translated)
	if errFirst != nil {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("qoder executor: close response body error: %v", errClose)
		}
		helps.RecordAPIResponseError(ctx, e.cfg, errFirst)
		return nil, errFirst
	}
	return &qoderOpenedStream{resp: httpResp, scanner: scanner, first: first}, nil
}

// qoderQueueWait converts an upstream hint into the actual wait.
//
// The hint defaults to qoderQueueDefaultWait when absent and is clamped to the
// per-round cap after the jitter is added, so no single round can exceed the
// documented maximum.
func qoderQueueWait(hint time.Duration) time.Duration {
	if hint <= 0 {
		hint = qoderQueueDefaultWait
	}
	wait := hint + qoderJitterFunc()
	if qoderQueueSingleWaitCap > 0 && wait > qoderQueueSingleWaitCap {
		wait = qoderQueueSingleWaitCap
	}
	return wait
}

// qoderTransientBackoffDelay returns the delay before the next transient retry.
func qoderTransientBackoffDelay(round int) time.Duration {
	if round < 0 {
		round = 0
	}
	if round >= len(qoderTransientBackoff) {
		return 0
	}
	return qoderTransientBackoff[round]
}

// qoderQueueBudgetExhausted reports a queue that never cleared within the
// executor budget. The credential is not at fault, so the error stays
// request-scoped and the conductor leaves the account available.
func qoderQueueBudgetExhausted(queued *qoderQueuedError, rounds int, waited time.Duration) error {
	exhausted := &qoderQueuedError{
		status:     http.StatusServiceUnavailable,
		retryAfter: queued.retryAfter,
		body: fmt.Sprintf("%s (queue budget exhausted after %d attempts / %s)",
			queued.body, rounds, waited.String()),
	}
	return exhausted
}

// qoderIsTransientFailure reports failures worth replaying on the same
// credential: transport errors, 418 and 5xx.
//
// A stream that was already accepted (HTTP 200) and then closed before its first
// frame is excluded. The upstream has the request at that point, so replaying it
// risks a second billable generation for what the client sees as one request;
// the caller reports it as a truncated stream instead.
func qoderIsTransientFailure(err error) bool {
	if err == nil {
		return false
	}
	var truncated interface{ IsStreamTruncated() bool }
	if errors.As(err, &truncated) && truncated.IsStreamTruncated() {
		return false
	}
	var transportErr *qoderTransportError
	if errors.As(err, &transportErr) {
		return true
	}
	var statusCoder interface{ StatusCode() int }
	if errors.As(err, &statusCoder) {
		code := statusCoder.StatusCode()
		return code == http.StatusTeapot || (code >= 500 && code <= 599)
	}
	return false
}

// qoderGatewayHosts returns the candidate hosts for one credential, starting at
// the configured host.
//
// A per-credential base_url override that is not one of the realm's known hosts
// pins the request to that single host: silently failing over to a gateway the
// operator did not configure would send the client's traffic elsewhere.
func qoderGatewayHosts(auth *cliproxyauth.Auth, provider string) []string {
	candidates := qoderCNGatewayHosts
	if strings.EqualFold(strings.TrimSpace(provider), constant.QoderINTL) {
		candidates = qoderINTLGatewayHosts
	}
	base := qoderBaseURL(auth, provider)
	if len(candidates) == 0 {
		return []string{base}
	}
	start := -1
	for i, host := range candidates {
		if host == base {
			start = i
			break
		}
	}
	if start < 0 {
		return []string{base}
	}
	// Failover wraps once through the list and never cycles: the rotation is a
	// copy so the package-level order stays intact.
	ordered := make([]string, 0, len(candidates))
	ordered = append(ordered, candidates[start:]...)
	ordered = append(ordered, candidates[:start]...)
	return ordered
}

// qoderPeekedFrame is the first usable envelope of a stream.
type qoderPeekedFrame struct {
	inner []byte
	done  bool
}

// qoderPeekFirstFrame scans until the first frame that carries content or the
// terminal marker. Keepalive frames (headers only) are skipped.
func qoderPeekFirstFrame(ctx context.Context, e *QoderExecutor, scanner *bufio.Scanner, translated []byte) (qoderPeekedFrame, error) {
	for scanner.Scan() {
		if ctx.Err() != nil {
			return qoderPeekedFrame{}, ctx.Err()
		}
		line := scanner.Bytes()
		helps.AppendAPIResponseChunk(ctx, e.cfg, line)

		inner, _, done, errFrame := helps.DecodeQoderStreamFrame(line)
		if errFrame != nil {
			return qoderPeekedFrame{}, qoderFrameError(errFrame, line)
		}
		if done {
			return qoderPeekedFrame{done: true}, nil
		}
		if len(inner) > 0 {
			return qoderPeekedFrame{inner: inner}, nil
		}
	}
	if ctx.Err() != nil {
		return qoderPeekedFrame{}, ctx.Err()
	}
	if errScan := scanner.Err(); errScan != nil {
		return qoderPeekedFrame{}, statusErr{code: http.StatusBadGateway, msg: fmt.Sprintf("qoder executor: read upstream stream: %v", errScan)}
	}
	// No usable frame at all: the upstream closed an accepted stream.
	return qoderPeekedFrame{}, statusErr{code: http.StatusBadGateway, msg: "qoder executor: upstream stream closed before the first frame", streamTruncated: true}
}

// qoderFrameError classifies a failed envelope.
//
// It is given the RAW frame line rather than the QoderStreamStatusError: that
// error truncates its detail to 400 bytes when it is constructed, which would
// hide markers located later in the body (a long DataInspectionFailed payload,
// for instance). Classification must always see the full text.
func qoderFrameError(frameErr error, rawLine []byte) error {
	status := http.StatusBadGateway
	body := string(rawLine)
	if statusErrValue, ok := frameErr.(*helps.QoderStreamStatusError); ok && statusErrValue != nil {
		if statusErrValue.Status > 0 {
			status = statusErrValue.Status
		}
		if body == "" {
			body = statusErrValue.Body
		}
	}
	return qoderClassifyUpstreamError(status, body)
}

// qoderQueuedError reports an upstream admission queue rejection. The credential
// is healthy and the model is merely busy, so it is request-scoped: the auth
// manager must not cool the credential or replay on another one.
type qoderQueuedError struct {
	status     int
	body       string
	retryAfter time.Duration
}

// Error implements error.
func (e *qoderQueuedError) Error() string {
	if e == nil {
		return "qoder: upstream queue"
	}
	return fmt.Sprintf("qoder: upstream model queue is full (http %d): %s", e.status, e.body)
}

// StatusCode reports the HTTP status this error maps to.
func (e *qoderQueuedError) StatusCode() int {
	if e == nil || e.status <= 0 {
		return http.StatusServiceUnavailable
	}
	return e.status
}

// RetryAfter reports the upstream-suggested wait when one was advertised.
func (e *qoderQueuedError) RetryAfter() *time.Duration {
	if e == nil || e.retryAfter <= 0 {
		return nil
	}
	wait := e.retryAfter
	return &wait
}

// IsRequestScoped marks the failure as bound to this request only.
func (e *qoderQueuedError) IsRequestScoped() bool { return true }

// qoderClassifyUpstreamError maps an upstream failure onto the auth manager's
// error contracts.
//
// The gateway reports every business failure as HTTP 403, so the four distinct
// causes can only be told apart from the envelope text. Precedence matters:
//
//  1. Queueing (10605 / isQueued / RESOURCE_LIMIT) - the credential is healthy
//     and the model is merely busy, so the failure is request-scoped.
//  2. Content policy (qoderContentPolicyMarkers) - a deterministic refusal of
//     the request itself. Replaying it on another credential yields the same
//     refusal, so it must NOT cool the credential.
//  3. Quota exhaustion (112 / 110 / pricingUrl / daily count) - the credential
//     owns the limit, so it is credential-scoped.
//  4. Any other 403 - an authorization refusal, where a different credential
//     may well succeed.
//
// Classification runs on the full signal text; qoderTruncateBody only bounds
// the detail echoed back to the caller, so a marker late in the body is still
// matched.
func qoderClassifyUpstreamError(status int, body string) error {
	text := qoderSignalText(body)

	if qoderIsQueueSignal(text) {
		return &qoderQueuedError{status: http.StatusServiceUnavailable, body: qoderTruncateBody(text), retryAfter: qoderRetryAfter(text)}
	}

	switch {
	case status == http.StatusUnauthorized:
		return statusErr{code: http.StatusUnauthorized, msg: qoderTruncateBody(text), credentialScoped: true}
	case status == http.StatusForbidden:
		if qoderIsContentPolicySignal(text) {
			// Deterministic request fault: another credential would be refused too.
			return statusErr{code: http.StatusForbidden, msg: qoderTruncateBody(text)}
		}
		if qoderIsQuotaSignal(text) {
			// Quota belongs to the credential. The branch is kept separate from the
			// generic refusal below so the quota marker set stays the single place
			// that decides what counts as exhaustion. The cooldown length is owned
			// by the auth conductor: a credential-scoped 403 always cools for its
			// default window, so the 110/112 difference cannot be expressed here.
			return statusErr{code: http.StatusForbidden, msg: qoderTruncateBody(text), credentialScoped: true}
		}
		// Authorization refusal: a different credential may succeed.
		return statusErr{code: http.StatusForbidden, msg: qoderTruncateBody(text), credentialScoped: true}
	case status == http.StatusTooManyRequests:
		return statusErr{code: http.StatusTooManyRequests, msg: qoderTruncateBody(text), retryAfter: qoderRetryAfterPointer(text)}
	case status >= 400 && status < 500:
		return statusErr{code: status, msg: qoderTruncateBody(text)}
	default:
		// 5xx, 418 and anything else is reported as an upstream failure.
		return statusErr{code: http.StatusBadGateway, msg: qoderTruncateBody(text)}
	}
}

// qoderIsContentPolicySignal reports an upstream safety refusal.
func qoderIsContentPolicySignal(text string) bool {
	if text == "" {
		return false
	}
	for _, marker := range qoderContentPolicyMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

// qoderSignalText strips JSON escaping so markers match in both the raw and the
// stringified envelope forms.
func qoderSignalText(body string) string {
	if body == "" {
		return ""
	}
	return strings.ReplaceAll(body, `\`, "")
}

// qoderIsQueueSignal reports upstream admission queueing.
func qoderIsQueueSignal(text string) bool {
	return strings.Contains(text, qoderQueueCodeMarker) ||
		strings.Contains(text, qoderQueueFlagMarker) ||
		strings.Contains(text, qoderResourceLimitMark)
}

// qoderIsQuotaSignal reports credential quota exhaustion.
func qoderIsQuotaSignal(text string) bool {
	if strings.Contains(text, qoderQuotaPricingMarker) || strings.Contains(text, qoderQuotaDailyMarker) {
		return true
	}
	for _, code := range []string{qoderQuotaExhaustedCode, qoderQuotaDailyCode} {
		for _, form := range []string{`"code":"` + code + `"`, `"code":` + code} {
			if strings.Contains(text, form) {
				return true
			}
		}
	}
	return false
}

// qoderRetryAfter extracts the upstream retry hint in seconds.
func qoderRetryAfter(text string) time.Duration {
	match := qoderRetryAfterPattern.FindStringSubmatch(text)
	if len(match) < 2 {
		return 0
	}
	seconds, errAtoi := strconv.Atoi(match[1])
	if errAtoi != nil || seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

// qoderRetryAfterPointer is qoderRetryAfter in the pointer form statusErr takes.
func qoderRetryAfterPointer(text string) *time.Duration {
	wait := qoderRetryAfter(text)
	if wait <= 0 {
		return nil
	}
	return &wait
}

// qoderTruncateBody bounds the upstream detail echoed back to the caller.
func qoderTruncateBody(text string) string {
	const limit = 400
	if len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !isRuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}

// isRuneStart reports whether b begins a UTF-8 sequence.
func isRuneStart(b byte) bool { return b&0xC0 != 0x80 }

// qoderPreparedAuth returns a copy with normalized attributes, or nil.
func qoderPreparedAuth(auth *cliproxyauth.Auth) *cliproxyauth.Auth {
	if auth == nil {
		return nil
	}
	prepared := auth.Clone()
	if prepared.Attributes == nil {
		prepared.Attributes = make(map[string]string)
	}
	if strings.TrimSpace(prepared.Attributes["base_url"]) == "" {
		if baseURL := qoderMetadataString(prepared, "base_url"); baseURL != "" {
			prepared.Attributes["base_url"] = baseURL
		}
	}
	return prepared
}

// qoderCredentials returns the identity and access token required by COSY.
func qoderCredentials(auth *cliproxyauth.Auth) (string, string, error) {
	accessToken := qoderMetadataString(auth, "access_token")
	if accessToken == "" {
		accessToken = qoderMetadataString(auth, "token")
	}
	if accessToken == "" {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: "qoder executor: missing access token"}
	}
	uid := qoderMetadataString(auth, "uid")
	if uid == "" {
		// The device fingerprint is derived from the uid. Credentials written by an
		// older login flow may not carry one yet; the auth id is a stable
		// per-credential substitute until re-login.
		if auth != nil {
			uid = strings.TrimSpace(auth.ID)
		}
	}
	if uid == "" {
		return "", "", statusErr{code: http.StatusUnauthorized, msg: "qoder executor: missing account uid"}
	}
	return uid, accessToken, nil
}

// qoderBaseURL resolves the gateway host, honouring a per-credential override.
func qoderBaseURL(auth *cliproxyauth.Auth, provider string) string {
	if auth != nil && auth.Attributes != nil {
		if baseURL := strings.TrimSpace(auth.Attributes["base_url"]); baseURL != "" {
			return strings.TrimSuffix(baseURL, "/")
		}
	}
	if baseURL := qoderMetadataString(auth, "base_url"); baseURL != "" {
		return strings.TrimSuffix(baseURL, "/")
	}
	if strings.EqualFold(strings.TrimSpace(provider), constant.QoderINTL) {
		return qoderINTLGatewayBaseURL
	}
	return qoderCNGatewayBaseURL
}

// qoderMetadataString reads a trimmed string from auth metadata.
func qoderMetadataString(auth *cliproxyauth.Auth, key string) string {
	if auth == nil || auth.Metadata == nil {
		return ""
	}
	value, _ := auth.Metadata[key].(string)
	return strings.TrimSpace(value)
}

// qoderCatalogMeta projects the resolved model definition onto the body builder's
// inputs. The registry entry already carries the catalog mapping (thinking levels,
// display name, modalities); enable filtering happens at registration time, so it
// never reaches this function.
func qoderCatalogMeta(req cliproxyexecutor.Request, model string) helps.QoderCatalogMeta {
	meta := helps.QoderCatalogMeta{Key: model}
	info, ok := cliproxyauth.ResolvedModelInfo(req)
	if !ok || info == nil {
		return meta
	}
	if info.DisplayName != "" {
		meta.DisplayName = info.DisplayName
	}
	for _, modality := range info.SupportedInputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "image") {
			meta.IsVL = true
			break
		}
	}
	meta.IsReasoning = info.Thinking != nil
	meta.MaxInputTokens = info.InputTokenLimit
	if meta.MaxInputTokens == 0 {
		meta.MaxInputTokens = info.ContextLength
	}
	return meta
}

// HttpRequest delegates ad-hoc HTTP execution to the embedded executor.
func (e *QoderExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if e == nil || e.OpenAICompatExecutor == nil {
		return nil, fmt.Errorf("qoder executor: embedded executor is nil")
	}
	return e.OpenAICompatExecutor.HttpRequest(ctx, auth, req)
}

// Refresh rotates Qoder credentials.
//
// The upstream has three token families and the refresh token prefix selects
// the endpoint: drt- goes to the device-token refresh, jrt- to the job-token
// refresh, and a personal access token is only ever re-exchanged as a fallback
// after a job refresh fails. A credential with neither is already valid and is
// returned unchanged.
//
// This is the only rotation entry point: sdk/auth.Authenticator has no Refresh
// method, so the conductor reaches the provider through ProviderExecutor.
func (e *QoderExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, errHome := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, errHome
	}
	if auth == nil {
		return nil, fmt.Errorf("qoder executor: auth is nil")
	}
	refreshToken := qoderMetadataString(auth, "refresh_token")
	personalToken := qoderMetadataString(auth, "personal_token")
	if refreshToken == "" && personalToken == "" {
		return auth, nil
	}
	realm, ok := qoderauthsvc.RealmForProvider(e.Identifier())
	if !ok {
		realm = qoderauthsvc.RealmCN
	}
	client := qoderRefreshClientFactory(e.cfg, realm, auth.ProxyURL)
	token, errRefresh := client.Refresh(ctx, refreshToken, personalToken)
	if errRefresh != nil {
		return nil, fmt.Errorf("qoder executor: refresh credentials: %w", errRefresh)
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["type"] = realm.Provider
	auth.Metadata["access_token"] = token.AccessToken
	if strings.TrimSpace(token.RefreshToken) != "" {
		auth.Metadata["refresh_token"] = token.RefreshToken
	}
	if strings.TrimSpace(token.TokenType) != "" {
		auth.Metadata["token_type"] = token.TokenType
	}
	if token.ExpiresIn > 0 {
		auth.Metadata["expires_in"] = token.ExpiresIn
	}
	if !token.ExpiresAt.IsZero() {
		auth.Metadata["expired"] = token.ExpiresAt.UTC().Format(time.RFC3339)
	}
	auth.Metadata["last_refresh"] = time.Now().UTC().Format(time.RFC3339)
	// Refresh responses do not repeat the identity, so uid/user_type/
	// organization_* are intentionally left untouched.
	if auth.Attributes == nil {
		auth.Attributes = make(map[string]string)
	}
	if strings.TrimSpace(auth.Attributes["base_url"]) == "" {
		auth.Attributes["base_url"] = realm.GatewayBase
	}
	return auth, nil
}
