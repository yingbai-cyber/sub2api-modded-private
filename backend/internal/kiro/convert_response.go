package kiro

import (
	"encoding/json"
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
	Credits      float64
	StopReason   string
	FatalError   string
	HasFatal     bool
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
}

// processEvent accumulates a single decoded event.
func (a *nonStreamAccumulator) processEvent(ev *Event) {
	switch ev.Kind {
	case EventAssistantResponse:
		a.textContent.WriteString(ev.Assistant.Content)
	case EventToolUse:
		a.hasToolUse = true
		buf, ok := a.toolJSONBuffers[ev.ToolUse.ToolUseID]
		if !ok {
			buf = &strings.Builder{}
			a.toolJSONBuffers[ev.ToolUse.ToolUseID] = buf
			a.toolOrder = append(a.toolOrder, ev.ToolUse.ToolUseID)
		}
		buf.WriteString(ev.ToolUse.Input)
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
// when no contextUsageEvent is present.
func BuildNonStreamResponse(r io.Reader, model string, thinkingEnabled bool, inputTokens int, toolNameMap map[string]string) (*NonStreamResult, error) {
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
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		acc.processEvent(&ev)
	}

	if acc.hasToolUse && acc.stopReason == "end_turn" {
		acc.stopReason = "tool_use"
	}

	// Assemble content blocks.
	var content []any
	rawText := acc.textContent.String()
	if acc.thinkingEnabled {
		thinking, hasThinking, remaining := ExtractThinkingFromCompleteText(rawText)
		if hasThinking {
			content = append(content, map[string]any{"type": "thinking", "thinking": thinking})
		}
		if remaining != "" {
			content = append(content, map[string]any{"type": "text", "text": remaining})
		}
	} else if rawText != "" {
		content = append(content, map[string]any{"type": "text", "text": rawText})
	}

	// Tool use blocks, in first-seen order.
	var toolInputConcat strings.Builder
	for _, id := range acc.toolOrder {
		buf := acc.toolJSONBuffers[id]
		toolInputConcat.WriteString(buf.String())
		toolInputConcat.WriteByte('\n')
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
	if outputTokens < 1 {
		outputTokens = 1
	}
	finalInput := inputTokens
	if acc.hasContextTokens {
		finalInput = acc.contextTokens
	}

	resp := map[string]any{
		"id":            "msg_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		"type":          "message",
		"role":          "assistant",
		"content":       content,
		"model":         model,
		"stop_reason":   acc.stopReason,
		"stop_sequence": nil,
		"usage": map[string]any{
			"input_tokens":  finalInput,
			"output_tokens": outputTokens,
			"kiro_credits":  acc.totalCredits,
		},
	}

	return &NonStreamResult{
		Response:     resp,
		InputTokens:  finalInput,
		OutputTokens: outputTokens,
		Credits:      acc.totalCredits,
		StopReason:   acc.stopReason,
		FatalError:   acc.fatalError,
		HasFatal:     acc.hasFatal,
	}, nil
}
