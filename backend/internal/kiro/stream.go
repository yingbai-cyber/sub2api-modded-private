package kiro

import (
	"strings"

	"github.com/google/uuid"
)

// StreamContext converts a sequence of decoded Kiro Events into Anthropic SSE
// events, porting kiro-rs anthropic::stream::StreamContext. It drives the
// legacy inline <thinking> state machine and native reasoningContentEvent
// handling, plus tool_use block management.

// StreamContext holds all streaming conversion state.
type StreamContext struct {
	State     *SseStateManager
	Model     string
	MessageID string

	InputTokens        int
	ContextInputTokens int
	hasContextTokens   bool
	OutputTokens       int

	toolBlockIndices map[string]int
	ToolNameMap      map[string]string

	thinkingEnabled       bool
	thinkingBuffer        string
	inThinkingBlock       bool
	thinkingExtracted     bool
	thinkingBlockIndex    int
	hasThinkingBlockIndex bool

	nativeThinkingText string
	thinkingSignature  string
	hasThinkingSig     bool

	textBlockIndex    int
	hasTextBlockIndex bool

	stripThinkingLeadingNewline bool

	// CacheEmulationRatio (0~1) splits client-visible input tokens into
	// input_tokens + emulated cache_read_input_tokens. 0 disables.
	CacheEmulationRatio float64

	TotalCredits float64

	FatalError    string
	HasFatalError bool
}

// NewStreamContext builds a StreamContext.
func NewStreamContext(model string, inputTokens int, thinkingEnabled bool, toolNameMap map[string]string) *StreamContext {
	if toolNameMap == nil {
		toolNameMap = map[string]string{}
	}
	return &StreamContext{
		State:            NewSseStateManager(),
		Model:            model,
		MessageID:        "msg_" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		InputTokens:      inputTokens,
		toolBlockIndices: map[string]int{},
		ToolNameMap:      toolNameMap,
		thinkingEnabled:  thinkingEnabled,
	}
}

// createMessageStartEvent builds the message_start payload. Cache emulation
// applies to the initial estimate too, so clients see a consistent split.
func (c *StreamContext) createMessageStartEvent() map[string]any {
	realInput, cacheRead := splitCacheTokens(c.InputTokens, c.CacheEmulationRatio)
	usage := map[string]any{
		"input_tokens":  realInput,
		"output_tokens": 1,
	}
	if cacheRead > 0 {
		usage["cache_read_input_tokens"] = cacheRead
	}
	return map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            c.MessageID,
			"type":          "message",
			"role":          "assistant",
			"content":       []any{},
			"model":         c.Model,
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage":         usage,
		},
	}
}

// GenerateInitialEvents emits message_start (+ initial text block when thinking
// is disabled). When thinking is enabled, blocks are created lazily in order.
func (c *StreamContext) GenerateInitialEvents() []SseEvent {
	var events []SseEvent
	if ev, ok := c.State.handleMessageStart(c.createMessageStartEvent()); ok {
		events = append(events, ev)
	}
	if c.thinkingEnabled {
		return events
	}
	idx := c.State.nextBlockIndex()
	c.textBlockIndex = idx
	c.hasTextBlockIndex = true
	events = append(events, c.State.handleContentBlockStart(idx, "text", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": map[string]any{"type": "text", "text": ""},
	})...)
	return events
}

// ProcessKiroEvent converts one decoded Kiro Event into SSE events.
func (c *StreamContext) ProcessKiroEvent(ev *Event) []SseEvent {
	switch ev.Kind {
	case EventAssistantResponse:
		return c.processAssistantResponse(ev.Assistant.Content)
	case EventReasoningContent:
		return c.processReasoningContent(ev.Reasoning)
	case EventToolUse:
		return c.processToolUse(ev.ToolUse)
	case EventContextUsage:
		window := ContextWindowSize(c.Model)
		actual := int(ev.Context.ContextUsagePercentage * float64(window) / 100.0)
		c.ContextInputTokens = actual
		c.hasContextTokens = true
		if ev.Context.ContextUsagePercentage >= 100.0 {
			c.State.setStopReason("model_context_window_exceeded")
		}
		return nil
	case EventError:
		msg := "上游错误: " + ev.ErrorCode + " - " + ev.ErrorMessage
		c.FatalError = msg
		c.HasFatalError = true
		return []SseEvent{errorSSE(msg)}
	case EventException:
		// ContentLengthExceededException is a normal context-limit, not fatal.
		if ev.ExceptionType == "ContentLengthExceededException" {
			c.State.setStopReason("max_tokens")
			return nil
		}
		msg := "上游异常: " + ev.ExceptionType
		c.FatalError = msg
		c.HasFatalError = true
		return []SseEvent{errorSSE(msg)}
	case EventMetering:
		c.TotalCredits += ev.Metering.Usage
		return nil
	default:
		return nil
	}
}

// errorSSE builds an Anthropic-style error SSE event.
func errorSSE(msg string) SseEvent {
	return NewSseEvent("error", map[string]any{
		"type": "error",
		"error": map[string]any{
			"type":    "api_error",
			"message": msg,
		},
	})
}

// processReasoningContent handles native reasoningContentEvent (thinking).
func (c *StreamContext) processReasoningContent(r *ReasoningContentEvent) []SseEvent {
	if !c.thinkingEnabled {
		return nil
	}
	var events []SseEvent

	if r.Signature != "" {
		c.thinkingSignature = r.Signature
		c.hasThinkingSig = true
	}

	if r.RedactedContent != "" {
		events = append(events, c.closeThinkingBlockIfOpen()...)
		redactedIndex := c.State.nextBlockIndex()
		events = append(events, c.State.handleContentBlockStart(redactedIndex, "redacted_thinking", map[string]any{
			"type":          "content_block_start",
			"index":         redactedIndex,
			"content_block": map[string]any{"type": "redacted_thinking", "data": r.RedactedContent},
		})...)
		if stop, ok := c.State.handleContentBlockStop(redactedIndex); ok {
			events = append(events, stop)
		}
		return events
	}

	if r.Text == "" {
		return events
	}

	delta := computeCumulativeDelta(r.Text, c.nativeThinkingText)
	if strings.HasPrefix(r.Text, c.nativeThinkingText) || strings.HasPrefix(c.nativeThinkingText, r.Text) {
		c.nativeThinkingText = r.Text
	} else {
		c.nativeThinkingText += delta
	}
	if delta == "" {
		return events
	}

	c.OutputTokens += estimateTokens(delta)
	events = append(events, c.ensureThinkingBlockStarted()...)
	if c.hasThinkingBlockIndex {
		events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, delta))
	}
	return events
}

// ensureThinkingBlockStarted opens a native thinking block if not already open.
func (c *StreamContext) ensureThinkingBlockStarted() []SseEvent {
	if c.hasThinkingBlockIndex && c.State.isBlockOpenOfType(c.thinkingBlockIndex, "thinking") {
		return nil
	}
	idx := c.State.nextBlockIndex()
	c.thinkingBlockIndex = idx
	c.hasThinkingBlockIndex = true

	contentBlock := map[string]any{"type": "thinking", "thinking": ""}
	if c.hasThinkingSig {
		contentBlock["signature"] = c.thinkingSignature
	}
	return c.State.handleContentBlockStart(idx, "thinking", map[string]any{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": contentBlock,
	})
}

// closeThinkingBlockIfOpen closes a native thinking block before text/tool_use.
func (c *StreamContext) closeThinkingBlockIfOpen() []SseEvent {
	var events []SseEvent
	if c.hasThinkingBlockIndex && !c.inThinkingBlock &&
		c.State.isBlockOpenOfType(c.thinkingBlockIndex, "thinking") {
		if stop, ok := c.State.handleContentBlockStop(c.thinkingBlockIndex); ok {
			events = append(events, stop)
		}
	}
	return events
}

// processAssistantResponse handles assistantResponseEvent text content.
func (c *StreamContext) processAssistantResponse(content string) []SseEvent {
	if content == "" {
		return nil
	}
	c.OutputTokens += estimateTokens(content)
	events := c.closeThinkingBlockIfOpen()
	if c.thinkingEnabled {
		events = append(events, c.processContentWithThinking(content)...)
		return events
	}
	events = append(events, c.createTextDeltaEvents(content)...)
	return events
}

// processContentWithThinking drives the inline <thinking> XML state machine,
// buffering across chunks to safely detect tags split across events.
func (c *StreamContext) processContentWithThinking(content string) []SseEvent {
	var events []SseEvent
	c.thinkingBuffer += content

	for {
		switch {
		case !c.inThinkingBlock && !c.thinkingExtracted:
			startPos := findRealThinkingStartTag(c.thinkingBuffer)
			if startPos >= 0 {
				before := c.thinkingBuffer[:startPos]
				if before != "" && strings.TrimSpace(before) != "" {
					events = append(events, c.createTextDeltaEvents(before)...)
				}
				c.inThinkingBlock = true
				c.stripThinkingLeadingNewline = true
				c.thinkingBuffer = c.thinkingBuffer[startPos+len("<thinking>"):]

				idx := c.State.nextBlockIndex()
				c.thinkingBlockIndex = idx
				c.hasThinkingBlockIndex = true
				events = append(events, c.State.handleContentBlockStart(idx, "thinking", map[string]any{
					"type":          "content_block_start",
					"index":         idx,
					"content_block": map[string]any{"type": "thinking", "thinking": ""},
				})...)
				continue
			}
			// No start tag: hold back a possible partial tag at the end.
			target := len(c.thinkingBuffer) - len("<thinking>")
			safeLen := findCharBoundary(c.thinkingBuffer, target)
			if safeLen > 0 {
				safe := c.thinkingBuffer[:safeLen]
				if safe != "" && strings.TrimSpace(safe) != "" {
					events = append(events, c.createTextDeltaEvents(safe)...)
					c.thinkingBuffer = c.thinkingBuffer[safeLen:]
				}
			}
			return events

		case c.inThinkingBlock:
			if c.stripThinkingLeadingNewline {
				if strings.HasPrefix(c.thinkingBuffer, "\n") {
					c.thinkingBuffer = c.thinkingBuffer[1:]
					c.stripThinkingLeadingNewline = false
				} else if c.thinkingBuffer != "" {
					c.stripThinkingLeadingNewline = false
				}
			}

			endPos := findRealThinkingEndTag(c.thinkingBuffer)
			if endPos >= 0 {
				thinkingContent := c.thinkingBuffer[:endPos]
				if thinkingContent != "" && c.hasThinkingBlockIndex {
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, thinkingContent))
				}
				c.inThinkingBlock = false
				c.thinkingExtracted = true
				if c.hasThinkingBlockIndex {
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, ""))
					if stop, ok := c.State.handleContentBlockStop(c.thinkingBlockIndex); ok {
						events = append(events, stop)
					}
				}
				c.thinkingBuffer = c.thinkingBuffer[endPos+len("</thinking>\n\n"):]
				continue
			}
			// No end tag: emit safe content, hold back a possible partial tag.
			target := len(c.thinkingBuffer) - len("</thinking>\n\n")
			safeLen := findCharBoundary(c.thinkingBuffer, target)
			if safeLen > 0 {
				safe := c.thinkingBuffer[:safeLen]
				if safe != "" && c.hasThinkingBlockIndex {
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, safe))
				}
				c.thinkingBuffer = c.thinkingBuffer[safeLen:]
			}
			return events

		default:
			// thinking already extracted: remaining is text.
			if c.thinkingBuffer != "" {
				remaining := c.thinkingBuffer
				c.thinkingBuffer = ""
				events = append(events, c.createTextDeltaEvents(remaining)...)
			}
			return events
		}
	}
}

// createTextDeltaEvents emits a text_delta, self-healing the text block if it
// was auto-closed by a preceding tool_use (avoids swallowing text).
func (c *StreamContext) createTextDeltaEvents(text string) []SseEvent {
	var events []SseEvent

	if c.hasTextBlockIndex && !c.State.isBlockOpenOfType(c.textBlockIndex, "text") {
		c.hasTextBlockIndex = false
	}

	var textIndex int
	if c.hasTextBlockIndex {
		textIndex = c.textBlockIndex
	} else {
		idx := c.State.nextBlockIndex()
		c.textBlockIndex = idx
		c.hasTextBlockIndex = true
		events = append(events, c.State.handleContentBlockStart(idx, "text", map[string]any{
			"type":          "content_block_start",
			"index":         idx,
			"content_block": map[string]any{"type": "text", "text": ""},
		})...)
		textIndex = idx
	}

	if delta, ok := c.State.handleContentBlockDelta(textIndex, map[string]any{
		"type":  "content_block_delta",
		"index": textIndex,
		"delta": map[string]any{"type": "text_delta", "text": text},
	}); ok {
		events = append(events, delta)
	}
	return events
}

// createThinkingDeltaEvent builds a thinking_delta event.
func (c *StreamContext) createThinkingDeltaEvent(index int, thinking string) SseEvent {
	return NewSseEvent("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": index,
		"delta": map[string]any{"type": "thinking_delta", "thinking": thinking},
	})
}

// processToolUse handles toolUseEvent, closing thinking/flushing pending text
// buffers first, then emitting the tool_use block start/delta/stop.
func (c *StreamContext) processToolUse(t *ToolUseEvent) []SseEvent {
	events := c.closeThinkingBlockIfOpen()
	c.State.setHasToolUse(true)

	// Boundary case: </thinking> lingering at buffer end (no trailing \n\n).
	if c.thinkingEnabled && c.inThinkingBlock {
		if endPos := findRealThinkingEndTagAtBufferEnd(c.thinkingBuffer); endPos >= 0 {
			thinkingContent := c.thinkingBuffer[:endPos]
			if thinkingContent != "" && c.hasThinkingBlockIndex {
				events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, thinkingContent))
			}
			c.inThinkingBlock = false
			c.thinkingExtracted = true
			if c.hasThinkingBlockIndex {
				events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, ""))
				if stop, ok := c.State.handleContentBlockStop(c.thinkingBlockIndex); ok {
					events = append(events, stop)
				}
			}
			afterPos := endPos + len("</thinking>")
			remaining := strings.TrimLeft(c.thinkingBuffer[afterPos:], " \t\r\n")
			c.thinkingBuffer = ""
			if remaining != "" {
				events = append(events, c.createTextDeltaEvents(remaining)...)
			}
		}
	}

	// Flush any pending pre-thinking probe text so tool_use doesn't swallow it.
	if c.thinkingEnabled && !c.inThinkingBlock && !c.thinkingExtracted && c.thinkingBuffer != "" {
		buffered := c.thinkingBuffer
		c.thinkingBuffer = ""
		events = append(events, c.createTextDeltaEvents(buffered)...)
	}

	// Allocate or reuse the tool block index.
	blockIndex, ok := c.toolBlockIndices[t.ToolUseID]
	if !ok {
		blockIndex = c.State.nextBlockIndex()
		c.toolBlockIndices[t.ToolUseID] = blockIndex
	}

	// Restore original (un-shortened) tool name if mapped.
	originalName := t.Name
	if orig, ok := c.ToolNameMap[t.Name]; ok {
		originalName = orig
	}

	events = append(events, c.State.handleContentBlockStart(blockIndex, "tool_use", map[string]any{
		"type":  "content_block_start",
		"index": blockIndex,
		"content_block": map[string]any{
			"type":  "tool_use",
			"id":    t.ToolUseID,
			"name":  originalName,
			"input": map[string]any{},
		},
	})...)

	if t.Input != "" {
		c.OutputTokens += (len(t.Input) + 3) / 4
		if delta, ok := c.State.handleContentBlockDelta(blockIndex, map[string]any{
			"type":  "content_block_delta",
			"index": blockIndex,
			"delta": map[string]any{"type": "input_json_delta", "partial_json": t.Input},
		}); ok {
			events = append(events, delta)
		}
	}

	if t.Stop {
		if stop, ok := c.State.handleContentBlockStop(blockIndex); ok {
			events = append(events, stop)
		}
	}
	return events
}

// GenerateFinalEvents flushes remaining buffers and emits the closing events.
func (c *StreamContext) GenerateFinalEvents() []SseEvent {
	var events []SseEvent

	if c.thinkingEnabled && c.thinkingBuffer != "" {
		if c.inThinkingBlock {
			if endPos := findRealThinkingEndTagAtBufferEnd(c.thinkingBuffer); endPos >= 0 {
				thinkingContent := c.thinkingBuffer[:endPos]
				if thinkingContent != "" && c.hasThinkingBlockIndex {
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, thinkingContent))
				}
				if c.hasThinkingBlockIndex {
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, ""))
					if stop, ok := c.State.handleContentBlockStop(c.thinkingBlockIndex); ok {
						events = append(events, stop)
					}
				}
				afterPos := endPos + len("</thinking>")
				remaining := strings.TrimLeft(c.thinkingBuffer[afterPos:], " \t\r\n")
				c.thinkingBuffer = ""
				c.inThinkingBlock = false
				c.thinkingExtracted = true
				if remaining != "" {
					events = append(events, c.createTextDeltaEvents(remaining)...)
				}
			} else {
				if c.hasThinkingBlockIndex {
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, c.thinkingBuffer))
					events = append(events, c.createThinkingDeltaEvent(c.thinkingBlockIndex, ""))
					if stop, ok := c.State.handleContentBlockStop(c.thinkingBlockIndex); ok {
						events = append(events, stop)
					}
				}
			}
		} else {
			events = append(events, c.createTextDeltaEvents(c.thinkingBuffer)...)
		}
		c.thinkingBuffer = ""
	}

	// If only a thinking block was produced (no text/tool_use), emit a filler
	// text block and set stop_reason=max_tokens.
	if c.thinkingEnabled && c.hasThinkingBlockIndex && !c.State.hasNonThinkingBlocks() {
		c.State.setStopReason("max_tokens")
		events = append(events, c.createTextDeltaEvents(" ")...)
	}

	finalInputTokens := c.InputTokens
	if c.hasContextTokens {
		finalInputTokens = c.ContextInputTokens
	}
	realInput, cacheRead := splitCacheTokens(finalInputTokens, c.CacheEmulationRatio)
	events = append(events, c.State.generateFinalEvents(realInput, cacheRead, c.OutputTokens, c.TotalCredits)...)
	return events
}
