package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/Wei-Shaw/sub2api/internal/kiro"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// forwardKiro dispatches a Kiro account request. Credentials carrying native
// Kiro auth (kiro_api_key / refresh_token / an explicit native auth_method) use
// the in-process native CodeWhisperer upstream path (internal/kiro). Legacy
// credentials that only carry base_url + api_key fall back to transparent
// passthrough to an external kiro-rs proxy.
func (s *GatewayService) forwardKiro(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	cred := kiro.ParseCredentials(account.ID, account.Credentials, account.Extra)
	if cred.UsesNativeUpstream() {
		return s.forwardKiroNative(ctx, c, account, parsed, startTime)
	}
	return s.forwardKiroLegacy(ctx, c, account, parsed, startTime)
}

// forwardKiroNative runs the native CodeWhisperer upstream path: resolve/refresh
// the bearer token, convert the request, call the upstream (ide->cli retry) and
// convert the response back to Anthropic SSE / JSON.
func (s *GatewayService) forwardKiroNative(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	// Resolve credential + effective bearer token (lazy refresh when expired).
	cred, token, err := s.kiroTokenProvider.Resolve(ctx, account)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[Kiro] token resolve failed (account=%s): %v", account.Name, err)
		// Credential resolution failure is account-scoped: fail over.
		return nil, &UpstreamFailoverError{
			StatusCode:   http.StatusUnauthorized,
			ResponseBody: []byte(err.Error()),
			Stage:        GatewayFailureStageAccountAuth,
		}
	}

	// Convert the inbound Anthropic request into a CodeWhisperer request.
	originalModel := parsed.Model
	mappedModel := account.GetMappedModel(originalModel)
	pr, err := kiro.PrepareRequest(parsed.Body.Bytes(), kiro.PrepareOptions{
		MappedModel:         mappedModel,
		ResponseModel:       originalModel,
		CacheEmulationRatio: account.GetKiroCacheEmulationRatio(),
	})
	if err != nil {
		// Unsupported model / malformed request: client error, do not fail over.
		return s.writeKiroClientError(c, http.StatusBadRequest, err.Error())
	}

	// Build a provider whose HTTP client routes through the gateway upstream
	// (proxy, per-account concurrency isolation, connection pool, TTFT trace).
	proxyURL := resolveAccountProxyURL(account)
	httpClient := &http.Client{Transport: &kiroUpstreamRoundTripper{
		upstream:    s.httpUpstream,
		proxyURL:    proxyURL,
		accountID:   account.ID,
		concurrency: account.Concurrency,
	}}
	provider := kiro.NewProvider(httpClient, kiro.NewEndpointRegistry(), nil)

	// Detach the upstream read from the client connection for streams, like
	// the Anthropic path: otherwise a client disconnect cancels the read and
	// the trailing meteringEvent is lost (output recorded with 0 credits). A
	// stalled upstream is still bounded by the stream idle timeout.
	upstreamCtx, releaseUpstreamCtx := detachStreamUpstreamContext(ctx, pr.Stream)
	defer releaseUpstreamCtx()

	resp, err := provider.Forward(upstreamCtx, &kiro.ForwardInput{
		Credentials: cred,
		Token:       token,
		MachineID:   kiro.GenerateMachineID(cred, ""),
		Config:      kiro.DefaultConfig(),
		RequestBody: pr.RequestBody,
		Model:       pr.UpstreamModel,
		ForceRefresh: func(ctx context.Context) (string, error) {
			return s.kiroTokenProvider.ForceRefresh(ctx, account, cred)
		},
	})
	if err != nil {
		return s.mapKiroUpstreamError(c, parsed, account, err)
	}
	defer func() { _ = resp.Body.Close() }()

	// Upstream accepted (2xx): release the serial lock early if configured.
	if parsed.OnUpstreamAccepted != nil {
		parsed.OnUpstreamAccepted()
	}

	if pr.Stream {
		return s.streamKiroNative(c, resp, pr, parsed, account, startTime)
	}
	return s.nonStreamKiroNative(c, resp, pr, parsed, account, startTime)
}

// streamKiroNative drives the upstream event-stream into Anthropic SSE. Once any
// byte is written to the client, failover is no longer possible; a fatal upstream
// error or an interrupted stream surfaces as an in-stream error event and is
// returned as an error (with the partial usage) so it is logged and counted.
func (s *GatewayService) streamKiroNative(
	c *gin.Context,
	resp *kiro.ForwardResponse,
	pr *kiro.PreparedRequest,
	parsed *ParsedRequest,
	account *Account,
	startTime time.Time,
) (*ForwardResult, error) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)

	flusher, _ := c.Writer.(http.Flusher)
	var firstTokenMs *int

	sctx := pr.NewStreamContext()
	outcome, _ := kiro.DriveStreamWithOptions(sctx, resp.Body, func(ev kiro.SseEvent) error {
		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if _, werr := io.WriteString(c.Writer, ev.ToSSEString()); werr != nil {
			return werr // signals client disconnect; the driver keeps draining
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	}, s.kiroDriveOptions())
	if outcome == nil {
		outcome = &kiro.StreamOutcome{}
	}

	duration := time.Since(startTime)
	logger.LegacyPrintf("service.gateway", "[Kiro] native model=%s upstream_model=%s native_effort=%s stream endpoint=%s credits=%.6f duration_ms=%d pings=%d skipped_frames=%d client_disconnected=%t",
		parsed.Model, pr.UpstreamModel, kiroNativeEffortLabel(pr), resp.Endpoint, outcome.Credits, duration.Milliseconds(),
		outcome.Pings, outcome.SkippedFrames, outcome.ClientDisconnected)

	result := &ForwardResult{
		Model:            parsed.Model,
		UpstreamModel:    pr.UpstreamModel,
		Stream:           true,
		Duration:         duration,
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: outcome.ClientDisconnected,
		Usage: ClaudeUsage{
			InputTokens:          outcome.InputTokens,
			OutputTokens:         outcome.OutputTokens,
			CacheReadInputTokens: outcome.CacheReadTokens,
			KiroCredits:          outcome.Credits,
		},
	}
	if !outcome.HasFatal && !outcome.Interrupted {
		return result, nil
	}
	return result, s.reportKiroStreamFailure(c, account, parsed, outcome)
}

// reportKiroStreamFailure logs an in-stream failure (fatal upstream error event
// or interrupted stream) and records it for Ops. The error event was already
// written to the client, so the response is marked committed to stop the
// handler from appending a second error frame. The returned error makes the
// handler log gateway.forward_failed while still billing the partial usage.
func (s *GatewayService) reportKiroStreamFailure(c *gin.Context, account *Account, parsed *ParsedRequest, outcome *kiro.StreamOutcome) error {
	reason := "upstream_error"
	detail := outcome.FatalError
	if outcome.Interrupted {
		reason = outcome.InterruptReason
		detail = outcome.InterruptDetail
	}
	logger.LegacyPrintf("service.gateway", "[Kiro] native stream failed account=%d(%s) model=%s reason=%s detail=%s credits=%.6f output_tokens=%d",
		account.ID, account.Name, parsed.Model, reason, truncateString(detail, 1000), outcome.Credits, outcome.OutputTokens)

	message := sanitizeUpstreamErrorMessage(truncateString(detail, 512))
	MarkOpsStreamFailure(c, "upstream_error", reason, message, http.StatusBadGateway)
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
		ProxyID:     opsUpstreamProxyID(account),
		ProxyName:   opsUpstreamProxyName(account),
		Platform:    account.Platform,
		AccountID:   account.ID,
		AccountName: account.Name,
		Kind:        "stream_error",
		Reason:      reason,
		Message:     message,
	})
	MarkResponseCommitted(c)
	return fmt.Errorf("kiro stream %s: %s", reason, message)
}

// nonStreamKiroNative aggregates the upstream event-stream into a single
// Anthropic Messages JSON response.
func (s *GatewayService) nonStreamKiroNative(
	c *gin.Context,
	resp *kiro.ForwardResponse,
	pr *kiro.PreparedRequest,
	parsed *ParsedRequest,
	account *Account,
	startTime time.Time,
) (*ForwardResult, error) {
	res, err := kiro.BuildNonStreamResponseFor(resp.Body, pr)
	if err != nil {
		return nil, fmt.Errorf("kiro: build non-stream response: %w", err)
	}
	if res.HasFatal {
		// An upstream error event mid-response used to be returned as a 200 with
		// whatever partial text had arrived. Report it as an error instead.
		logger.LegacyPrintf("service.gateway", "[Kiro] native non-stream failed account=%d(%s) model=%s detail=%s credits=%.6f",
			account.ID, account.Name, parsed.Model, truncateString(res.FatalError, 1000), res.Credits)
		message := sanitizeUpstreamErrorMessage(truncateString(res.FatalError, 512))
		setOpsUpstreamError(c, http.StatusBadGateway, message, "")
		MarkResponseCommitted(c)
		body, _ := json.Marshal(map[string]any{
			"type":  "error",
			"error": map[string]any{"type": "api_error", "message": message},
		})
		c.Data(http.StatusBadGateway, "application/json", body)
		return &ForwardResult{
			Model:         parsed.Model,
			UpstreamModel: pr.UpstreamModel,
			Stream:        false,
			Duration:      time.Since(startTime),
			Usage: ClaudeUsage{
				InputTokens:          res.InputTokens,
				OutputTokens:         res.OutputTokens,
				CacheReadInputTokens: res.CacheReadTokens,
				KiroCredits:          res.Credits,
			},
		}, fmt.Errorf("kiro non-stream upstream error: %s", message)
	}

	out, err := json.Marshal(res.Response)
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal response: %w", err)
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(out)

	duration := time.Since(startTime)
	logger.LegacyPrintf("service.gateway", "[Kiro] native model=%s upstream_model=%s native_effort=%s non-stream endpoint=%s credits=%.6f duration_ms=%d",
		parsed.Model, pr.UpstreamModel, kiroNativeEffortLabel(pr), resp.Endpoint, res.Credits, duration.Milliseconds())

	return &ForwardResult{
		Model:         parsed.Model,
		UpstreamModel: pr.UpstreamModel,
		Stream:        false,
		Duration:      duration,
		Usage: ClaudeUsage{
			InputTokens:          res.InputTokens,
			OutputTokens:         res.OutputTokens,
			CacheReadInputTokens: res.CacheReadTokens,
			KiroCredits:          res.Credits,
		},
	}, nil
}

// writeKiroClientError writes a client-facing error and returns it as an error
// (no failover: the request is malformed, other accounts cannot help). It used
// to return a successful ForwardResult, which recorded the failure as a 0-token
// success and kept it out of the error logs.
func (s *GatewayService) writeKiroClientError(c *gin.Context, status int, msg string) (*ForwardResult, error) {
	MarkResponseCommitted(c)
	c.Header("Content-Type", "application/json")
	c.Status(status)
	body, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "invalid_request_error", "message": msg},
	})
	_, _ = c.Writer.Write(body)
	return nil, fmt.Errorf("kiro client error %d: %s", status, truncateString(msg, 512))
}

// kiroDispositionAction is the gateway's reaction to a classified upstream
// disposition. It is computed by the pure classifyKiroDisposition so the
// decision table is unit-testable without a gin context or Account.
type kiroDispositionAction struct {
	// ClientError => write the upstream body back to the caller, no failover.
	ClientError bool
	// Failover => return an *UpstreamFailoverError to the scheduler.
	Failover bool
	// FailoverStatus is the status reported on the failover error.
	FailoverStatus int
	// AccountAuthStage marks the failure as credential-scoped (auth/quota),
	// letting ops classify it as a provider/account problem.
	AccountAuthStage bool
	// RetryableEligible marks throttle/transient failures that MAY retry on the
	// same account first (gated by pool-mode at the call site).
	RetryableEligible bool
}

// classifyKiroDisposition maps a provider Disposition (+ upstream status) to the
// gateway's reaction. Pure function: no side effects, no gin/Account dependency.
func classifyKiroDisposition(disp kiro.Disposition, status int) kiroDispositionAction {
	switch disp {
	case kiro.DispBadRequest, kiro.DispClientError:
		return kiroDispositionAction{ClientError: true}
	case kiro.DispThrottled:
		return kiroDispositionAction{Failover: true, FailoverStatus: kiroFailoverStatus(status, http.StatusTooManyRequests), RetryableEligible: true}
	case kiro.DispTransient:
		return kiroDispositionAction{Failover: true, FailoverStatus: kiroFailoverStatus(status, http.StatusBadGateway), RetryableEligible: true}
	case kiro.DispAuthFailure, kiro.DispQuotaExhausted:
		// Credential-scoped: other endpoints won't help; let ops mark the account.
		return kiroDispositionAction{Failover: true, FailoverStatus: kiroFailoverStatus(status, http.StatusBadGateway), AccountAuthStage: true}
	default: // DispUnknown
		return kiroDispositionAction{Failover: true, FailoverStatus: kiroFailoverStatus(status, http.StatusBadGateway)}
	}
}

// mapKiroUpstreamError translates a classified kiro.UpstreamError into the
// gateway's failover machinery. It runs BEFORE any response byte is written, so
// failover branches are always safe here.
func (s *GatewayService) mapKiroUpstreamError(
	c *gin.Context,
	parsed *ParsedRequest,
	account *Account,
	err error,
) (*ForwardResult, error) {
	var ue *kiro.UpstreamError
	if !errors.As(err, &ue) {
		// Unclassified: treat as transient and fail over.
		return nil, &UpstreamFailoverError{StatusCode: http.StatusBadGateway, ResponseBody: []byte(err.Error())}
	}

	logger.LegacyPrintf("service.gateway", "[Kiro] native upstream error account=%s endpoint=%s disp=%d status=%d",
		account.Name, ue.Endpoint, ue.Disposition, ue.Status)

	action := classifyKiroDisposition(ue.Disposition, ue.Status)
	if action.ClientError {
		status := ue.Status
		if status == 0 {
			status = http.StatusBadRequest
		}
		// Keep the upstream reason (e.g. "Invalid tool use format.") in the log
		// and Ops. It used to reach only the client, which made the instant
		// empty-reply 400s impossible to diagnose from the server side.
		upstreamMsg := sanitizeUpstreamErrorMessage(kiroUpstreamErrorMessage(ue.Body))
		logger.LegacyPrintf("service.gateway", "[Kiro] native upstream client error account=%d(%s) endpoint=%s status=%d model=%s body=%s",
			account.ID, account.Name, ue.Endpoint, status, parsed.Model, truncateString(ue.Body, 1000))
		setOpsUpstreamError(c, status, upstreamMsg, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: status,
			Kind:               "http_error",
			Message:            upstreamMsg,
		})
		return s.writeKiroClientError(c, status, ue.Body)
	}

	failover := &UpstreamFailoverError{
		StatusCode:   action.FailoverStatus,
		ResponseBody: []byte(ue.Body),
	}
	if action.AccountAuthStage {
		failover.Stage = GatewayFailureStageAccountAuth
	}
	if action.RetryableEligible {
		failover.RetryableOnSameAccount = account.IsPoolMode() && account.IsPoolModeRetryableStatus(ue.Status)
	}
	return nil, failover
}

// kiroUpstreamErrorMessage extracts a readable reason from a Kiro error body
// such as {"message":"Invalid tool use format.","reason":"REQUEST_BODY_INVALID"}.
func kiroUpstreamErrorMessage(body string) string {
	var kb struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(body), &kb)
	msg := strings.TrimSpace(kb.Message)
	if msg == "" {
		msg = strings.TrimSpace(extractUpstreamErrorMessage([]byte(body)))
	}
	reason := strings.TrimSpace(kb.Reason)
	switch {
	case msg != "" && reason != "":
		return msg + " (" + reason + ")"
	case msg != "":
		return msg
	case reason != "":
		return reason
	default:
		return truncateString(strings.TrimSpace(body), 512)
	}
}

// kiroDriveOptions maps the gateway stream settings onto the native Kiro stream
// driver so Kiro streams get the same keepalive pings and idle bound as the
// other stream paths (gateway.stream_keepalive_interval and
// gateway.stream_data_interval_timeout, default 10s / 180s).
func (s *GatewayService) kiroDriveOptions() kiro.DriveOptions {
	var opts kiro.DriveOptions
	if s.cfg == nil {
		return opts
	}
	if v := s.cfg.Gateway.StreamKeepaliveInterval; v > 0 {
		opts.KeepaliveInterval = time.Duration(v) * time.Second
	}
	if v := s.cfg.Gateway.StreamDataIntervalTimeout; v > 0 {
		opts.IdleTimeout = time.Duration(v) * time.Second
	}
	return opts
}

// kiroNativeEffortLabel reports the effort tier actually sent upstream as the
// native output_config.effort field, or "none" when no native effort was sent
// (legacy XML hint or no thinking). usage_logs.reasoning_effort only records
// what the client requested, so this is the one place that shows what applied.
func kiroNativeEffortLabel(pr *kiro.PreparedRequest) string {
	if pr == nil || !pr.EffortNative {
		return "none"
	}
	return pr.EffortLevel.String()
}

// kiroFailoverStatus returns the upstream status when set, else a fallback.
func kiroFailoverStatus(status, fallback int) int {
	if status == 0 {
		return fallback
	}
	return status
}

// kiroUpstreamRoundTripper adapts the gateway HTTPUpstream (proxy + per-account
// concurrency isolation + connection pool + TTFT trace) to http.RoundTripper so
// it can back the kiro.Provider's *http.Client.
type kiroUpstreamRoundTripper struct {
	upstream    HTTPUpstream
	proxyURL    string
	accountID   int64
	concurrency int
}

func (rt *kiroUpstreamRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt.upstream.Do(req, rt.proxyURL, rt.accountID, rt.concurrency)
}

// forwardKiroLegacy transparently proxies to an external kiro-rs endpoint
// (base_url + api_key passthrough). kiro-rs exposes a standard Anthropic Messages
// API and returns a kiro_credits field in usage.
func (s *GatewayService) forwardKiroLegacy(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	baseURL := strings.TrimSpace(account.GetCredential("base_url"))
	apiKey := strings.TrimSpace(account.GetCredential("api_key"))
	if baseURL == "" || apiKey == "" {
		return nil, fmt.Errorf("kiro account missing base_url or api_key")
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	body := parsed.Body
	originalModel := parsed.Model
	mappedModel := account.GetMappedModel(originalModel)
	if mappedModel != originalModel {
		body.Replace(s.replaceModelInBody(body.Bytes(), mappedModel))
		logger.LegacyPrintf("service.gateway", "[Kiro] Model mapping applied: %s -> %s (account=%s)", originalModel, mappedModel, account.Name)
	}

	upstreamURL := baseURL + "/v1/messages"

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, upstreamURL, bytes.NewReader(body.Bytes()))
	if err != nil {
		return nil, fmt.Errorf("kiro: create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("x-api-key", apiKey)

	if v := c.GetHeader("anthropic-version"); v != "" {
		req.Header.Set("anthropic-version", v)
	}
	if v := c.GetHeader("anthropic-beta"); v != "" {
		req.Header.Set("anthropic-beta", v)
	}

	proxyURL := ""
	if account.ProxyID != nil && account.Proxy != nil {
		proxyURL = account.Proxy.URL()
	}

	resp, err := s.httpUpstream.Do(req, proxyURL, account.ID, account.Concurrency)
	if err != nil {
		logger.LegacyPrintf("service.gateway", "[Kiro] request failed (account=%s): %v", account.Name, err)
		return nil, fmt.Errorf("kiro request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))

		c.Header("Content-Type", resp.Header.Get("Content-Type"))
		c.Status(resp.StatusCode)
		_, _ = c.Writer.Write(respBody)

		return &ForwardResult{
			Model:  parsed.Model,
			Stream: parsed.Stream,
		}, nil
	}

	var usage ClaudeUsage
	var firstTokenMs *int
	var clientDisconnect bool

	if parsed.Stream {
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		c.Status(http.StatusOK)

		streamRes := s.streamKiroResponse(c, resp, startTime, originalModel, mappedModel)
		usage = streamRes.usage
		firstTokenMs = streamRes.firstTokenMs
		clientDisconnect = streamRes.clientDisconnect
	} else {
		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, fmt.Errorf("kiro: read response: %w", err)
		}

		parsedUsage := parseClaudeUsageFromResponseBody(respBody)
		if parsedUsage != nil {
			usage = *parsedUsage
		}

		if originalModel != mappedModel {
			respBody = s.replaceModelInResponseBody(respBody, mappedModel, originalModel)
		}

		c.Header("Content-Type", resp.Header.Get("Content-Type"))
		c.Status(http.StatusOK)
		_, _ = c.Writer.Write(respBody)
	}

	duration := time.Since(startTime)
	logger.LegacyPrintf("service.gateway", "[Kiro] account=%s status=success duration_ms=%d credits=%.6f",
		account.Name, duration.Milliseconds(), usage.KiroCredits)

	return &ForwardResult{
		Model:            parsed.Model,
		Stream:           parsed.Stream,
		Duration:         duration,
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: clientDisconnect,
		Usage:            usage,
	}, nil
}

// kiroStreamResult is the legacy passthrough stream result.
type kiroStreamResult struct {
	usage            ClaudeUsage
	firstTokenMs     *int
	clientDisconnect bool
}

// streamKiroResponse proxies a kiro-rs SSE stream and extracts usage (incl.
// kiro_credits). When originalModel != mappedModel it rewrites the model name.
func (s *GatewayService) streamKiroResponse(c *gin.Context, resp *http.Response, startTime time.Time, originalModel, mappedModel string) *kiroStreamResult {
	usage := &ClaudeUsage{}
	var firstTokenMs *int
	clientDisconnected := false
	needModelReplace := originalModel != mappedModel

	flusher, _ := c.Writer.(http.Flusher)

	scanner := bufio.NewScanner(resp.Body)
	maxLineSize := defaultMaxLineSize
	if s.cfg != nil && s.cfg.Gateway.MaxLineSize > 0 {
		maxLineSize = s.cfg.Gateway.MaxLineSize
	}
	scanBuf := make([]byte, 64*1024)
	scanner.Buffer(scanBuf[:0], maxLineSize)

	for scanner.Scan() {
		line := scanner.Text()

		if firstTokenMs == nil && len(line) > 0 {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}

		extractKiroSSEUsage(line, usage)

		outputLine := line
		if needModelReplace && strings.HasPrefix(line, "data: ") {
			outputLine = replaceModelInSSELine(line, mappedModel, originalModel)
		}

		if _, err := fmt.Fprintf(c.Writer, "%s\n", outputLine); err != nil {
			clientDisconnected = true
			for scanner.Scan() {
				extractKiroSSEUsage(scanner.Text(), usage)
			}
			break
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	return &kiroStreamResult{
		usage:            *usage,
		firstTokenMs:     firstTokenMs,
		clientDisconnect: clientDisconnected,
	}
}

// replaceModelInSSELine rewrites the model field inside an SSE data line.
func replaceModelInSSELine(line, fromModel, toModel string) string {
	dataStr := strings.TrimPrefix(line, "data: ")
	var event map[string]any
	if json.Unmarshal([]byte(dataStr), &event) != nil {
		return line
	}

	changed := false
	if model, ok := event["model"].(string); ok && model == fromModel {
		event["model"] = toModel
		changed = true
	}
	if msg, ok := event["message"].(map[string]any); ok {
		if model, ok := msg["model"].(string); ok && model == fromModel {
			msg["model"] = toModel
			changed = true
		}
	}

	if !changed {
		return line
	}
	newData, err := json.Marshal(event)
	if err != nil {
		return line
	}
	return "data: " + string(newData)
}

// extractKiroSSEUsage extracts usage (incl. kiro_credits) from an SSE data line.
func extractKiroSSEUsage(line string, usage *ClaudeUsage) {
	if !strings.HasPrefix(line, "data: ") {
		return
	}
	dataStr := strings.TrimPrefix(line, "data: ")
	var event map[string]any
	if json.Unmarshal([]byte(dataStr), &event) != nil {
		return
	}
	u, ok := event["usage"].(map[string]any)
	if !ok {
		return
	}
	if v, ok := u["input_tokens"].(float64); ok && int(v) > 0 {
		usage.InputTokens = int(v)
	}
	if v, ok := u["output_tokens"].(float64); ok && int(v) > 0 {
		usage.OutputTokens = int(v)
	}
	if v, ok := u["cache_read_input_tokens"].(float64); ok && int(v) > 0 {
		usage.CacheReadInputTokens = int(v)
	}
	if v, ok := u["cache_creation_input_tokens"].(float64); ok && int(v) > 0 {
		usage.CacheCreationInputTokens = int(v)
	}
	if v, ok := u["kiro_credits"].(float64); ok && v > 0 {
		usage.KiroCredits = v
	}
}
