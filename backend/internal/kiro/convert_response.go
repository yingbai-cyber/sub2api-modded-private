package kiro

import (
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/google/uuid"
)

// This file builds a non-streaming Anthropic Messages response from the Kiro
// event stream, porting kiro-rs anthropic::handlers::handle_non_stream_request.

// NonStreamResult is the assembled non-streaming response plus usage metadata.
type NonStreamResult struct {
	Response     map[string]any
	InputTokens  int
	OutputTokens int
	// CacheReadTokens is the emulated cache_read_input_tokens portion already
	// subtracted from InputTokens (0 when cache emulation is disabled).
	CacheReadTokens int
	Credits         float64
	StopReason      string
	FatalError      string
	HasFatal        bool
}

// nonStreamAccumulator collects events for a non-streaming response.
type nonStreamAccumulator struct {
	model           string
	thinkingEnabled bool
	toolNameMap     map[string]string

	textContent      strings.Builder
	toolJSONBuffers  map[string]*strings.Builder
	toolNames        map[string]string
	toolOrder        []string
	hasToolUse       bool
	stopReason       string
	contextTokens    int
	hasContextTokens bool
	totalCredits     float64
	fatalError       string
	hasFatal         bool

	// Native reasoningContentEvent thinking (incremental fragments).
	nativeThinking   strings.Builder
	thinkingSig      string
	redactedThinking []string
}

// processEvent accumulates a single decoded event.
func (a *nonStreamAccumulator) processEvent(ev *Event) {
	switch ev.Kind {
	case EventAssistantResponse:
		_, _ = a.textContent.WriteString(ev.Assistant.Content)
	case EventToolUse:
		a.hasToolUse = true
		buf, ok := a.toolJSONBuffers[ev.ToolUse.ToolUseID]
		if !ok {
			buf = &strings.Builder{}
			a.toolJSONBuffers[ev.ToolUse.ToolUseID] = buf
			a.toolOrder = append(a.toolOrder, ev.ToolUse.ToolUseID)
		}
		_, _ = buf.WriteString(ev.ToolUse.Input)
		if ev.ToolUse.Name != "" {
			name := ev.ToolUse.Name
			if orig, ok := a.toolNameMap[name]; ok {
				name = orig
			}
			a.toolNames[ev.ToolUse.ToolUseID] = name
		}
	case EventContextUsage:
		window := ContextWindowSize(a.model)
		a.contextTokens = int(ev.Context.ContextUsagePercentage * float64(window) / 100.0)
		a.hasContextTokens = true
		if ev.Context.ContextUsagePercentage >= 100.0 {
			a.stopReason = "model_context_window_exceeded"
		}
	case EventException:
		if ev.ExceptionType == "ContentLengthExceededException" {
			a.stopReason = "max_tokens"
		}
	case EventError:
		a.fatalError = "上游错误: " + ev.ErrorCode + " - " + ev.ErrorMessage
		a.hasFatal = true
	case EventMetering:
		a.totalCredits += ev.Metering.Usage
	case EventReasoningContent:
		// Previously dropped: native thinking never reached non-stream replies.
		if ev.Reasoning.Text != "" {
			_, _ = a.nativeThinking.WriteString(ev.Reasoning.Text)
		}
		if ev.Reasoning.Signature != "" {
			a.thinkingSig = ev.Reasoning.Signature
		}
		if ev.Reasoning.RedactedContent != "" {
			a.redactedThinking = append(a.redactedThinking, ev.Reasoning.RedactedContent)
		}
	}
}

// tuInput resolves a tool's accumulated JSON input into a value.
func tuInput(buf *strings.Builder) json.RawMessage {
	s := strings.TrimSpace(buf.String())
	if s == "" {
		return json.RawMessage(`{}`)
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	return json.RawMessage(`{}`)
}

// BuildNonStreamResponse decodes the full event stream from r and assembles a
// non-streaming Anthropic response. inputTokens is the estimated fallback used
// when no contextUsageEvent is present. Cache emulation (when the prepared
// request carries a positive ratio) is applied by BuildNonStreamResponseFor.
func BuildNonStreamResponse(r io.Reader, model string, thinkingEnabled bool, inputTokens int, toolNameMap map[string]string) (*NonStreamResult, error) {
	return buildNonStreamResponse(r, model, thinkingEnabled, inputTokens, toolNameMap, 0)
}

// BuildNonStreamResponseFor is BuildNonStreamResponse driven by a
// PreparedRequest, applying its CacheEmulationRatio to the reported usage.
func BuildNonStreamResponseFor(r io.Reader, pr *PreparedRequest) (*NonStreamResult, error) {
	return buildNonStreamResponse(r, pr.ResponseModel, pr.ThinkingEnabled, pr.InputTokens, pr.ToolNameMap, pr.CacheEmulationRatio)
}

func buildNonStreamResponse(r io.Reader, model string, thinkingEnabled bool, inputTokens int, toolNameMap map[string]string, cacheEmulationRatio float64) (*NonStreamResult, error) {
	if toolNameMap == nil {
		toolNameMap = map[string]string{}
	}
	acc := &nonStreamAccumulator{
		model:           model,
		thinkingEnabled: thinkingEnabled,
		toolNameMap:     toolNameMap,
		toolJSONBuffers: map[string]*strings.Builder{},
		toolNames:       map[string]string{},
		stopReason:      "end_turn",
	}

	dec := NewEventDecoder(r)
	consecutiveDecodeErrors := 0
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			// Skip a malformed payload frame (kiro.rs does); anything else, or
			// too many in a row, is a real failure (incl. a mid-frame cut-off).
			var payloadErr *PayloadDecodeError
			if errors.As(err, &payloadErr) {
				consecutiveDecodeErrors++
				if consecutiveDecodeErrors < maxConsecutiveDecodeErrors {
					continue
				}
			}
			return nil, err
		}
		consecutiveDecodeErrors = 0
		acc.processEvent(&ev)
	}

	if acc.hasToolUse && acc.stopReason == "end_turn" {
		acc.stopReason = "tool_use"
	}

	// Assemble content blocks.
	var content []any
	rawText := acc.textContent.String()
	nativeThinking := acc.nativeThinking.String()
	if acc.thinkingEnabled {
		// Mirrors kiro.rs build_non_stream_content. Thinking blocks carry a
		// signature so thinking-mode clients accept them on the next turn.
		if nativeThinking != "" {
			sig := acc.thinkingSig
			if sig == "" {
				sig = thinkingSignaturePlaceholder
			}
			content = append(content, map[string]any{"type": "thinking", "thinking": nativeThinking, "signature": sig})
		} else {
			thinking, hasThinking, remaining := ExtractThinkingFromCompleteText(rawText)
			if hasThinking {
				content = append(content, map[string]any{"type": "thinking", "thinking": thinking, "signature": thinkingSignaturePlaceholder})
			}
			if remaining != "" {
				content = append(content, map[string]any{"type": "text", "text": remaining})
			}
		}
		for _, redacted := range acc.redactedThinking {
			content = append(content, map[string]any{"type": "redacted_thinking", "data": redacted})
		}
		if nativeThinking != "" && rawText != "" {
			content = append(content, map[string]any{"type": "text", "text": rawText})
		}
	} else if rawText != "" {
		content = append(content, map[string]any{"type": "text", "text": rawText})
	}

	// Tool use blocks, in first-seen order.
	var toolInputConcat strings.Builder
	for _, id := range acc.toolOrder {
		buf := acc.toolJSONBuffers[id]
		_, _ = toolInputConcat.WriteString(buf.String())
		_ = toolInputConcat.WriteByte('\n')
	}
	for _, id := range acc.toolOrder {
		content = append(content, map[string]any{
			"type":  "tool_use",
			"id":    id,
			"name":  acc.toolNames[id],
			"input": tuInput(acc.toolJSONBuffers[id]),
		})
	}

	outputTokens := estimateTokens(rawText) + estimateTokens(toolInputConcat.String())
	if nativeThinking != "" {
		outputTokens += estimateTokens(nativeThinking)
	}
	if outputTokens < 1 {
		outputTokens = 1
	}
	finalInput := inputTokens
	if acc.hasContextTokens {
		finalInput = acc.contextTokens
	}
	realInput, cacheRead := splitCacheTokens(finalInput, cacheEmulationRatio)

	// Kiro credits are deliberately omitted from the client-visible usage payload:
	// they are an internal cost metric for admins only. The consumed amount still
	// reaches the billing layer via NonStreamResult.Credits.
	usage := map[string]any{
		"input_tokens":  realInput,
		"output_tokens": outputTokens,
	}
	if cacheRead > 0 {
		usage["cache_read_input_tokens"] = cacheRead
	}

	resp := map[string]any{
		"id":            "msg_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   acc.stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	}

	return &NonStreamResult{
		Response:        resp,
		InputTokens:     realInput,
		OutputTokens:    outputTokens,
		CacheReadTokens: cacheRead,
		Credits:         acc.totalCredits,
		StopReason:      acc.stopReason,
		FatalError:      acc.fatalError,
		HasFatal:        acc.hasFatal,
	}, nil
}
