package kiro

import "encoding/json"

// This file defines the decoded event payloads emitted by the CodeWhisperer
// generateAssistantResponse stream, mirroring kiro-rs kiro::model::events::*.

// EventKind identifies the decoded event variant.
type EventKind int

const (
	// EventUnknown is an event type we do not specifically handle.
	EventUnknown EventKind = iota
	// EventAssistantResponse carries an assistant text delta.
	EventAssistantResponse
	// EventToolUse carries a (possibly partial) tool-call delta.
	EventToolUse
	// EventReasoningContent carries native thinking/reasoning text.
	EventReasoningContent
	// EventMetering carries credits usage.
	EventMetering
	// EventContextUsage carries context-window usage percentage.
	EventContextUsage
	// EventError is a service-side error message frame.
	EventError
	// EventException is a service-side exception frame.
	EventException
)

// Event is the unified decoded event. Exactly one payload field is populated
// according to Kind (mirrors kiro-rs Event enum).
type Event struct {
	Kind EventKind

	Assistant *AssistantResponseEvent
	ToolUse   *ToolUseEvent
	Reasoning *ReasoningContentEvent
	Metering  *MeteringEvent
	Context   *ContextUsageEvent

	// Error / Exception frames.
	ErrorCode     string
	ErrorMessage  string
	ExceptionType string
}

// AssistantResponseEvent is a streamed assistant text fragment.
type AssistantResponseEvent struct {
	Content string `json:"content"`
}

// ToolUseEvent is a streamed tool-call fragment. Input is a JSON-string that
// may arrive in partial chunks; Stop marks the final chunk.
type ToolUseEvent struct {
	Name      string `json:"name"`
	ToolUseID string `json:"toolUseId"`
	Input     string `json:"input"`
	Stop      bool   `json:"stop"`
}

// ReasoningContentEvent is native thinking content. Kiro CLI/runtime typically
// returns cumulative text, so downstream must compute deltas.
type ReasoningContentEvent struct {
	Text            string `json:"text"`
	Signature       string `json:"signature"`
	RedactedContent string `json:"redactedContent"`
}

// MeteringEvent carries credits usage for the request.
type MeteringEvent struct {
	Usage float64 `json:"usage"`
}

// ContextUsageEvent carries context-window usage percentage (0-100).
type ContextUsageEvent struct {
	ContextUsagePercentage float64 `json:"contextUsagePercentage"`
}

// eventTypeFromString maps the :event-type header to an EventKind.
func eventTypeFromString(s string) EventKind {
	switch s {
	case "assistantResponseEvent":
		return EventAssistantResponse
	case "toolUseEvent":
		return EventToolUse
	case "reasoningContentEvent":
		return EventReasoningContent
	case "meteringEvent":
		return EventMetering
	case "contextUsageEvent":
		return EventContextUsage
	default:
		return EventUnknown
	}
}

// decodeEventPayload parses the payload JSON for a known event kind.
func decodeEventPayload(kind EventKind, payload []byte) (Event, error) {
	ev := Event{Kind: kind}
	switch kind {
	case EventAssistantResponse:
		var p AssistantResponseEvent
		if err := json.Unmarshal(payload, &p); err != nil {
			return ev, err
		}
		ev.Assistant = &p
	case EventToolUse:
		var p ToolUseEvent
		if err := json.Unmarshal(payload, &p); err != nil {
			return ev, err
		}
		ev.ToolUse = &p
	case EventReasoningContent:
		var p ReasoningContentEvent
		if err := json.Unmarshal(payload, &p); err != nil {
			return ev, err
		}
		ev.Reasoning = &p
	case EventMetering:
		var p MeteringEvent
		if err := json.Unmarshal(payload, &p); err != nil {
			return ev, err
		}
		ev.Metering = &p
	case EventContextUsage:
		var p ContextUsageEvent
		if err := json.Unmarshal(payload, &p); err != nil {
			return ev, err
		}
		ev.Context = &p
	default:
		ev.Kind = EventUnknown
	}
	return ev, nil
}
