package kiro

import (
	"encoding/json"
	"io"
	"strings"
)

// This file ports the request-orchestration flow kiro-rs keeps in
// anthropic::handlers: it turns an inbound Anthropic Messages request into a
// serialized CodeWhisperer request body (resolving native effort, deciding
// thinking handling) and drives the upstream event stream into Anthropic SSE.
//
// It deliberately lives in the kiro package (not the sub2api service package)
// so the trunk adapter (forwardKiro) stays thin and this logic is unit-testable
// with no network or Account dependency.

// ErrUnsupportedModel indicates the requested model has no Kiro mapping.
var ErrUnsupportedModel = &ConversionError{Msg: "模型不支持"}

// PrepareOptions controls request preparation. The zero value is valid.
type PrepareOptions struct {
	// MappedModel is the sub2api account-level mapped model name. sub2api applies
	// its own model mapping upstream of Kiro's canonical MapModel; pass it here so
	// the CodeWhisperer conversationState uses the right modelId. Empty => the
	// request body's own model field is used.
	MappedModel string
	// ResponseModel is the model echoed back to the client (message_start.model
	// and non-stream response). Empty => the request body's own model field.
	ResponseModel string
	// GlobalEffort overrides the effort policy. Nil => DefaultGlobalEffortConfig().
	GlobalEffort *GlobalEffortConfig
	// SupportedEfforts overrides the model's supported effort tiers. Nil =>
	// FallbackSupportedEfforts(upstreamModel).
	SupportedEfforts []EffortLevel
	// CacheEmulationRatio (0~1) splits reported input tokens into input +
	// emulated cache_read_input_tokens in client-visible usage. 0 disables.
	CacheEmulationRatio float64
}

// PreparedRequest is the output of PrepareRequest: everything the caller needs
// to make the upstream call and drive the response conversion.
type PreparedRequest struct {
	// RequestBody is the serialized CodeWhisperer request (conversationState +
	// optional additionalModelRequestFields). profileArn is injected later by
	// the endpoint layer, not here.
	RequestBody string
	// UpstreamModel is the canonical Kiro model id sent to the upstream.
	UpstreamModel string
	// ResponseModel is the model echoed to the client.
	ResponseModel string
	// ThinkingEnabled controls thinking handling in the response converter. It
	// is true when the client requested thinking OR native effort is in effect
	// (native effort returns reasoningContentEvent which must not be dropped).
	ThinkingEnabled bool
	// ToolNameMap maps shortened tool names back to originals (may be empty).
	ToolNameMap map[string]string
	// InputTokens is the locally-estimated input token count (fallback used
	// when the upstream omits a contextUsageEvent).
	InputTokens int
	// Stream reports whether the client requested a streaming response.
	Stream bool
	// EffortNative reports whether a native effort tier was sent.
	EffortNative bool
	// EffortLevel is the resolved native effort tier (valid when EffortNative).
	EffortLevel EffortLevel
	// CacheEmulationRatio is carried from PrepareOptions (see there).
	CacheEmulationRatio float64
}

// PrepareRequest parses an inbound Anthropic Messages request body and builds
// the CodeWhisperer request, mirroring kiro-rs handle_messages orchestration:
// override thinking-from-model-name -> resolve effort -> convert (suppressing
// legacy XML when native effort is used) -> attach additionalModelRequestFields
// -> serialize -> decide thinking_enabled.
func PrepareRequest(rawBody []byte, opts PrepareOptions) (*PreparedRequest, error) {
	var req MessagesRequest
	if err := json.Unmarshal(rawBody, &req); err != nil {
		return nil, &ConversionError{Msg: "请求体解析失败: " + err.Error()}
	}
	if len(req.Messages) == 0 {
		return nil, errEmptyMessages
	}

	// Model-name "-thinking" suffix overrides the thinking/effort config.
	overrideThinkingFromModelName(&req)

	// Resolve the canonical upstream model from the (account-mapped) model.
	baseModel := opts.MappedModel
	if baseModel == "" {
		baseModel = req.Model
	}
	upstreamModel, ok := MapModel(baseModel)
	if !ok {
		return nil, &ConversionError{Msg: "模型不支持: " + baseModel}
	}

	responseModel := opts.ResponseModel
	if responseModel == "" {
		responseModel = req.Model
	}

	// Resolve the native-effort decision BEFORE conversion so we know whether
	// to suppress legacy <thinking_mode> XML injection.
	decision := resolveEffortDecision(&req, upstreamModel, opts)

	conv, err := ConvertRequest(&req, upstreamModel, ConvertOptions{
		SuppressThinkingXML: decision.SuppressesLegacyXML(),
	})
	if err != nil {
		return nil, err
	}

	kreq := KiroRequest{ConversationState: conv.ConversationState}
	if decision.Result == DecideNative {
		kreq.AdditionalModelRequestFields = &AdditionalModelRequestFields{
			OutputConfig: AdditionalOutputConfig{Effort: decision.Level.String()},
		}
	}

	bodyJSON, err := json.Marshal(kreq)
	if err != nil {
		return nil, &ConversionError{Msg: "序列化请求失败: " + err.Error()}
	}

	// thinking_enabled: client asked for thinking OR native effort is active.
	thinkingEnabled := req.Thinking.IsEnabled() || decision.Result == DecideNative

	return &PreparedRequest{
		RequestBody:         string(bodyJSON),
		UpstreamModel:       upstreamModel,
		ResponseModel:       responseModel,
		ThinkingEnabled:     thinkingEnabled,
		ToolNameMap:         conv.ToolNameMap,
		InputTokens:         CountInputTokens(&req),
		Stream:              req.Stream,
		EffortNative:        decision.Result == DecideNative,
		EffortLevel:         decision.Level,
		CacheEmulationRatio: opts.CacheEmulationRatio,
	}, nil
}

// NewStreamContext builds a StreamContext primed for this prepared request.
func (pr *PreparedRequest) NewStreamContext() *StreamContext {
	sc := NewStreamContext(pr.ResponseModel, pr.InputTokens, pr.ThinkingEnabled, pr.ToolNameMap)
	sc.CacheEmulationRatio = pr.CacheEmulationRatio
	return sc
}

// resolveEffortDecision mirrors kiro-rs resolve_effort_for_request. Go has no
// models cache, so supported efforts come from opts or the fallback table.
func resolveEffortDecision(req *MessagesRequest, upstreamModel string, opts PrepareOptions) EffortDecision {
	supported := opts.SupportedEfforts
	if supported == nil {
		supported = FallbackSupportedEfforts(upstreamModel)
	}
	if len(supported) == 0 {
		return EffortDecision{Result: DecideLegacy}
	}

	global := DefaultGlobalEffortConfig()
	if opts.GlobalEffort != nil {
		global = *opts.GlobalEffort
	}

	ctx := &EffortContext{
		SupportedEfforts:  supported,
		Global:            global,
		HasThinkingIntent: HasThinkingIntent(req),
	}
	if req.OutputConfig != nil {
		ctx.ExplicitEffort = req.OutputConfig.Effort
		ctx.HasExplicitEffort = true
	}
	return ResolveEffort(ctx)
}

// overrideThinkingFromModelName forces a thinking config when the model name
// carries a "thinking" token (mirrors kiro-rs override_thinking_from_model_name).
// Opus 4.6 uses adaptive thinking + high effort; others use enabled thinking.
func overrideThinkingFromModelName(req *MessagesRequest) {
	lower := strings.ToLower(req.Model)
	if !strings.Contains(lower, "thinking") {
		return
	}
	isOpus46 := strings.Contains(lower, "opus") &&
		(strings.Contains(lower, "4-6") || strings.Contains(lower, "4.6"))

	thinkingType := "enabled"
	if isOpus46 {
		thinkingType = "adaptive"
	}
	req.Thinking = &ThinkingConfig{Type: thinkingType, BudgetTokens: 20000}
	if isOpus46 {
		req.OutputConfig = &OutputConfig{Effort: "high"}
	}
}

// StreamOutcome reports the result of driving a streaming response.
type StreamOutcome struct {
	// InputTokens is the final input token count (contextUsageEvent value when
	// present, else the local estimate).
	InputTokens int
	// OutputTokens is the accumulated output token estimate.
	OutputTokens int
	// CacheReadTokens is the emulated cache_read_input_tokens portion already
	// subtracted from InputTokens (0 when cache emulation is disabled).
	CacheReadTokens int
	// Credits is the summed meteringEvent usage (billable Kiro credits).
	Credits float64
	// ClientDisconnected reports that the emit callback failed (client hung up);
	// upstream draining continued to capture full usage for billing.
	ClientDisconnected bool
	// FatalError is the upstream error message, when the stream ended fatally.
	FatalError string
	HasFatal   bool
}

// EmitFunc writes one already-rendered SSE event to the client. Returning an
// error signals the client disconnected; DriveStream then stops emitting but
// keeps draining upstream so usage/credits are captured for billing.
type EmitFunc func(SseEvent) error

// DriveStream decodes the upstream Kiro event-stream from r and drives ctx,
// invoking emit for every produced SSE event (initial, per-event, final). It
// mirrors kiro-rs create_sse_stream ordering: initial events, then per-chunk
// events, then final events — except a fatal upstream error terminates the
// stream immediately after the error event (no final events), matching kiro-rs.
//
// r is typically the ForwardResponse.Body (caller owns Close).
func DriveStream(ctx *StreamContext, r io.Reader, emit EmitFunc) (*StreamOutcome, error) {
	disconnected := false
	emitAll := func(events []SseEvent) {
		if disconnected {
			return
		}
		for _, ev := range events {
			if err := emit(ev); err != nil {
				disconnected = true
				return
			}
		}
	}

	emitAll(ctx.GenerateInitialEvents())

	dec := NewEventDecoder(r)
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Transport/decode error: close out the stream gracefully.
			emitAll(ctx.GenerateFinalEvents())
			return outcomeFrom(ctx, disconnected), nil
		}
		emitAll(ctx.ProcessKiroEvent(&ev))
		if ctx.HasFatalError {
			// Fatal upstream error: the error event was already produced; do not
			// emit final events (mirrors kiro-rs fatal short-circuit).
			return outcomeFrom(ctx, disconnected), nil
		}
	}

	emitAll(ctx.GenerateFinalEvents())
	return outcomeFrom(ctx, disconnected), nil
}

// outcomeFrom snapshots the final usage/credits from a StreamContext.
func outcomeFrom(ctx *StreamContext, disconnected bool) *StreamOutcome {
	inputTokens := ctx.InputTokens
	if ctx.hasContextTokens {
		inputTokens = ctx.ContextInputTokens
	}
	realInput, cacheRead := splitCacheTokens(inputTokens, ctx.CacheEmulationRatio)
	return &StreamOutcome{
		InputTokens:        realInput,
		OutputTokens:       ctx.OutputTokens,
		CacheReadTokens:    cacheRead,
		Credits:            ctx.TotalCredits,
		ClientDisconnected: disconnected,
		FatalError:         ctx.FatalError,
		HasFatal:           ctx.HasFatalError,
	}
}
