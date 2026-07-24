package kiro

import "encoding/json"

// This file ports kiro-rs anthropic::stream SSE state management. It guarantees
// the emitted SSE event sequence satisfies the Anthropic Messages streaming
// contract:
//  1. message_start appears exactly once
//  2. content blocks go start -> delta* -> stop
//  3. message_delta appears once, after all content_block_stop
//  4. message_stop is last

// SseEvent is a single Server-Sent Event (name + JSON data object).
type SseEvent struct {
	Event string
	Data  map[string]any
}

// NewSseEvent builds an SseEvent.
func NewSseEvent(event string, data map[string]any) SseEvent {
	return SseEvent{Event: event, Data: data}
}

// ToSSEString renders the event in SSE wire format.
func (e SseEvent) ToSSEString() string {
	b, err := json.Marshal(e.Data)
	if err != nil {
		b = []byte("{}")
	}
	return "event: " + e.Event + "\ndata: " + string(b) + "\n\n"
}

// blockState tracks a single content block's lifecycle.
type blockState struct {
	blockType string
	started   bool
	stopped   bool
}

// SseStateManager enforces the Anthropic streaming event contract.
type SseStateManager struct {
	messageStarted   bool
	messageDeltaSent bool
	activeBlocks     map[int]*blockState
	messageEnded     bool
	nextIndex        int
	stopReason       string
	hasStopReason    bool
	hasToolUse       bool
}

// NewSseStateManager builds an empty state manager.
func NewSseStateManager() *SseStateManager {
	return &SseStateManager{activeBlocks: map[int]*blockState{}}
}

// isBlockOpenOfType reports whether a block is started, not stopped, of a type.
func (m *SseStateManager) isBlockOpenOfType(index int, expected string) bool {
	b, ok := m.activeBlocks[index]
	return ok && b.started && !b.stopped && b.blockType == expected
}

// nextBlockIndex allocates and returns the next block index.
func (m *SseStateManager) nextBlockIndex() int {
	idx := m.nextIndex
	m.nextIndex++
	return idx
}

// setHasToolUse records that a tool_use block occurred.
func (m *SseStateManager) setHasToolUse(has bool) { m.hasToolUse = has }

// setStopReason sets an explicit stop reason.
func (m *SseStateManager) setStopReason(reason string) {
	m.stopReason = reason
	m.hasStopReason = true
}

// hasNonThinkingBlocks reports whether any non-thinking block exists.
func (m *SseStateManager) hasNonThinkingBlocks() bool {
	for _, b := range m.activeBlocks {
		if b.blockType != "thinking" {
			return true
		}
	}
	return false
}

// getStopReason resolves the final stop reason.
func (m *SseStateManager) getStopReason() string {
	if m.hasStopReason {
		return m.stopReason
	}
	if m.hasToolUse {
		return "tool_use"
	}
	return "end_turn"
}

// handleMessageStart emits message_start once.
func (m *SseStateManager) handleMessageStart(event map[string]any) (SseEvent, bool) {
	if m.messageStarted {
		return SseEvent{}, false
	}
	m.messageStarted = true
	return NewSseEvent("message_start", event), true
}

// handleContentBlockStart emits content_block_start, auto-closing open text
// blocks first when starting a tool_use block.
func (m *SseStateManager) handleContentBlockStart(index int, blockType string, data map[string]any) []SseEvent {
	var events []SseEvent

	if blockType == "tool_use" {
		m.hasToolUse = true
		for bi, b := range m.activeBlocks {
			if b.blockType == "text" && b.started && !b.stopped {
				events = append(events, NewSseEvent("content_block_stop", map[string]any{
					"type": "content_block_stop", "index": bi,
				}))
				b.stopped = true
			}
		}
	}

	if b, ok := m.activeBlocks[index]; ok {
		if b.started {
			return events
		}
		b.started = true
	} else {
		m.activeBlocks[index] = &blockState{blockType: blockType, started: true}
	}

	events = append(events, NewSseEvent("content_block_start", data))
	return events
}

// handleContentBlockDelta emits a content_block_delta if the block is open.
func (m *SseStateManager) handleContentBlockDelta(index int, data map[string]any) (SseEvent, bool) {
	b, ok := m.activeBlocks[index]
	if !ok || !b.started || b.stopped {
		return SseEvent{}, false
	}
	return NewSseEvent("content_block_delta", data), true
}

// handleContentBlockStop emits content_block_stop once per block.
func (m *SseStateManager) handleContentBlockStop(index int) (SseEvent, bool) {
	b, ok := m.activeBlocks[index]
	if !ok || b.stopped {
		return SseEvent{}, false
	}
	b.stopped = true
	return NewSseEvent("content_block_stop", map[string]any{
		"type": "content_block_stop", "index": index,
	}), true
}

// generateFinalEvents closes open blocks then emits message_delta + message_stop.
func (m *SseStateManager) generateFinalEvents(inputTokens, outputTokens int, credits float64) []SseEvent {
	var events []SseEvent
	for idx, b := range m.activeBlocks {
		if b.started && !b.stopped {
			events = append(events, NewSseEvent("content_block_stop", map[string]any{
				"type": "content_block_stop", "index": idx,
			}))
			b.stopped = true
		}
	}
	if !m.messageDeltaSent {
		m.messageDeltaSent = true
		events = append(events, NewSseEvent("message_delta", map[string]any{
			"type": "message_delta",
			"delta": map[string]any{
				"stop_reason":   m.getStopReason(),
				"stop_sequence": nil,
			},
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": outputTokens,
				"kiro_credits":  credits,
			},
		}))
	}
	if !m.messageEnded {
		m.messageEnded = true
		events = append(events, NewSseEvent("message_stop", map[string]any{"type": "message_stop"}))
	}
	return events
}
