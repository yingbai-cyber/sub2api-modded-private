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
		MappedModel:   mappedModel,
		ResponseModel: originalModel,
	})
	if err != nil {
		// Unsupported model / malformed request: client error, do not fail over.
		return s.writeKiroClientError(c, parsed, http.StatusBadRequest, err.Error())
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

	resp, err := provider.Forward(ctx, &kiro.ForwardInput{
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
		return s.streamKiroNative(c, resp, pr, parsed, startTime)
	}
	return s.nonStreamKiroNative(c, resp, pr, parsed, startTime)
}

// streamKiroNative drives the upstream event-stream into Anthropic SSE. Once any
// byte is written to the client, failover is no longer possible; a fatal upstream
// error surfaces as an in-stream error event (handled inside DriveStream).
func (s *GatewayService) streamKiroNative(
	c *gin.Context,
	resp *kiro.ForwardResponse,
	pr *kiro.PreparedRequest,
	parsed *ParsedRequest,
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
	outcome, _ := kiro.DriveStream(sctx, resp.Body, func(ev kiro.SseEvent) error {
		if firstTokenMs == nil {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if _, werr := io.WriteString(c.Writer, ev.ToSSEString()); werr != nil {
			return werr // signals client disconnect; DriveStream keeps draining
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if outcome == nil {
		outcome = &kiro.StreamOutcome{}
	}

	duration := time.Since(startTime)
	logger.LegacyPrintf("service.gateway", "[Kiro] native model=%s stream endpoint=%s credits=%.6f duration_ms=%d",
		parsed.Model, resp.Endpoint, outcome.Credits, duration.Milliseconds())

	return &ForwardResult{
		Model:            parsed.Model,
		UpstreamModel:    pr.UpstreamModel,
		Stream:           true,
		Duration:         duration,
		FirstTokenMs:     firstTokenMs,
		ClientDisconnect: outcome.ClientDisconnected,
		Usage: ClaudeUsage{
			InputTokens:  outcome.InputTokens,
			OutputTokens: outcome.OutputTokens,
			KiroCredits:  outcome.Credits,
		},
	}, nil
}

// nonStreamKiroNative aggregates the upstream event-stream into a single
// Anthropic Messages JSON response.
func (s *GatewayService) nonStreamKiroNative(
	c *gin.Context,
	resp *kiro.ForwardResponse,
	pr *kiro.PreparedRequest,
	parsed *ParsedRequest,
	startTime time.Time,
) (*ForwardResult, error) {
	res, err := kiro.BuildNonStreamResponse(resp.Body, pr.ResponseModel, pr.ThinkingEnabled, pr.InputTokens, pr.ToolNameMap)
	if err != nil {
		return nil, fmt.Errorf("kiro: build non-stream response: %w", err)
	}

	out, err := json.Marshal(res.Response)
	if err != nil {
		return nil, fmt.Errorf("kiro: marshal response: %w", err)
	}
	c.Header("Content-Type", "application/json")
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write(out)

	duration := time.Since(startTime)
	logger.LegacyPrintf("service.gateway", "[Kiro] native model=%s non-stream endpoint=%s credits=%.6f duration_ms=%d",
		parsed.Model, resp.Endpoint, res.Credits, duration.Milliseconds())

	return &ForwardResult{
		Model:         parsed.Model,
		UpstreamModel: pr.UpstreamModel,
		Stream:        false,
		Duration:      duration,
		Usage: ClaudeUsage{
			InputTokens:  res.InputTokens,
			OutputTokens: res.OutputTokens,
			KiroCredits:  res.Credits,
		},
	}, nil
}

// writeKiroClientError writes a client-facing error and returns a successful
// ForwardResult (no failover): the request is malformed, other accounts cannot help.
func (s *GatewayService) writeKiroClientError(c *gin.Context, parsed *ParsedRequest, status int, msg string) (*ForwardResult, error) {
	c.Header("Content-Type", "application/json")
	c.Status(status)
	body, _ := json.Marshal(map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "invalid_request_error", "message": msg},
	})
	_, _ = c.Writer.Write(body)
	return &ForwardResult{Model: parsed.Model, Stream: parsed.Stream}, nil
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
		return s.writeKiroClientError(c, parsed, status, ue.Body)
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
