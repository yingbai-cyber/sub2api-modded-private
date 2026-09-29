package kiro

import (
	"strings"
	"testing"
)

// runStream drives a StreamContext over the given events and returns the
// ordered list of emitted SSE event names plus the concatenated text and
// thinking deltas, for contract assertions.
type streamOut struct {
	names    []string
	text     string
	thinking string
	toolJSON string
	events   []SseEvent
}

func runStream(model string, inputTokens int, thinking bool, toolNameMap map[string]string, evs []Event) streamOut {
	c := NewStreamContext(model, inputTokens, thinking, toolNameMap)
	var all []SseEvent
	all = append(all, c.GenerateInitialEvents()...)
	for i := range evs {
		all = append(all, c.ProcessKiroEvent(&evs[i])...)
	}
	all = append(all, c.GenerateFinalEvents()...)

	out := streamOut{events: all}
	for _, e := range all {
		out.names = append(out.names, e.Event)
		if e.Event != "content_block_delta" {
			continue
		}
		delta, _ := e.Data["delta"].(map[string]any)
		switch delta["type"] {
		case "text_delta":
			s, _ := delta["text"].(string)
			out.text += s
		case "thinking_delta":
			s, _ := delta["thinking"].(string)
			out.thinking += s
		case "input_json_delta":
			s, _ := delta["partial_json"].(string)
			out.toolJSON += s
		}
	}
	return out
}

func assistantEv(content string) Event {
	return Event{Kind: EventAssistantResponse, Assistant: &AssistantResponseEvent{Content: content}}
}

func reasoningEv(text, sig string) Event {
	return Event{Kind: EventReasoningContent, Reasoning: &ReasoningContentEvent{Text: text, Signature: sig}}
}

func toolEv(name, id, input string, stop bool) Event {
	return Event{Kind: EventToolUse, ToolUse: &ToolUseEvent{Name: name, ToolUseID: id, Input: input, Stop: stop}}
}

func TestStreamBasicText(t *testing.T) {
	out := runStream("claude-sonnet-4", 10, false, nil, []Event{
		assistantEv("Hello"),
		assistantEv(" world"),
	})
	if out.names[0] != "message_start" {
		t.Fatalf("first event = %q; want message_start", out.names[0])
	}
	if out.names[len(out.names)-1] != "message_stop" {
		t.Fatalf("last event = %q; want message_stop", out.names[len(out.names)-1])
	}
	if out.text != "Hello world" {
		t.Errorf("text = %q; want %q", out.text, "Hello world")
	}
	// message_start then content_block_start(text) since thinking disabled.
	if out.names[1] != "content_block_start" {
		t.Errorf("second event = %q; want content_block_start", out.names[1])
	}
	// exactly one message_delta before message_stop.
	if n := countName(out.names, "message_delta"); n != 1 {
		t.Errorf("message_delta count = %d; want 1", n)
	}
}

func TestStreamNativeThinking(t *testing.T) {
	// Kiro reasoningContentEvent text is an incremental fragment (kiro.rs
	// forwards it verbatim), and the signature arrives on a later event.
	out := runStream("claude-sonnet-4", 5, true, nil, []Event{
		reasoningEv("Let me", ""),
		reasoningEv(" think", "sig-1"),
		assistantEv("Answer"),
	})
	if out.thinking != "Let me think" {
		t.Errorf("thinking = %q; want %q", out.thinking, "Let me think")
	}
	if out.text != "Answer" {
		t.Errorf("text = %q; want %q", out.text, "Answer")
	}
	// thinking block must open and close before the text block opens.
	assertOrder(t, out.names, "content_block_start", "content_block_stop")
	// The upstream signature is sent as signature_delta before the block stops.
	if sig := thinkingSignatureBeforeStop(t, out.events); sig != "sig-1" {
		t.Errorf("signature_delta = %q; want sig-1", sig)
	}
}

func TestStreamNativeThinkingKeepsOverlappingFragments(t *testing.T) {
	// Regression: the old cumulative de-dup dropped " is" (a tail of the text
	// so far) and "The" (a prefix of it), truncating the thinking.
	out := runStream("claude-sonnet-4", 5, true, nil, []Event{
		reasoningEv("The plan is", ""),
		reasoningEv(" is", ""),
		reasoningEv(" fine. ", ""),
		reasoningEv("The", ""),
		assistantEv("ok"),
	})
	want := "The plan is is fine. The"
	if out.thinking != want {
		t.Errorf("thinking = %q; want %q", out.thinking, want)
	}
}

func TestStreamInlineThinkingGetsPlaceholderSignature(t *testing.T) {
	out := runStream("claude-sonnet-4", 5, true, nil, []Event{
		assistantEv("<thinking>\nreasoning here</thinking>\n\nFinal answer"),
	})
	if sig := thinkingSignatureBeforeStop(t, out.events); sig != thinkingSignaturePlaceholder {
		t.Errorf("signature_delta = %q; want placeholder", sig)
	}
}

func TestStreamNativeThinkingClosedAtEndHasSignature(t *testing.T) {
	// Native thinking with nothing after it is closed by GenerateFinalEvents,
	// which must still send signature_delta before the stop.
	out := runStream("claude-sonnet-4", 5, true, nil, []Event{
		reasoningEv("only thinking", ""),
	})
	if sig := thinkingSignatureBeforeStop(t, out.events); sig != thinkingSignaturePlaceholder {
		t.Errorf("signature_delta = %q; want placeholder", sig)
	}
	if sr := finalStopReason(out.events); sr != "max_tokens" {
		t.Errorf("stop_reason = %q; want max_tokens", sr)
	}
}

func countName(names []string, want string) int {
	n := 0
	for _, s := range names {
		if s == want {
			n++
		}
	}
	return n
}

func assertOrder(t *testing.T, names []string, first, second string) {
	t.Helper()
	fi, si := -1, -1
	for i, s := range names {
		if s == first && fi < 0 {
			fi = i
		}
		if s == second && fi >= 0 && si < 0 && i > fi {
			si = i
		}
	}
	if fi < 0 || si < 0 {
		t.Errorf("expected %q before %q in %v", first, second, names)
	}
}

// thinkingSignatureBeforeStop returns the signature_delta sent for the first
// thinking block, failing unless it comes before that block's
// content_block_stop.
func thinkingSignatureBeforeStop(t *testing.T, events []SseEvent) string {
	t.Helper()
	thinkingIdx := -1
	sig := ""
	for _, e := range events {
		idx, _ := e.Data["index"].(int)
		switch e.Event {
		case "content_block_start":
			cb, _ := e.Data["content_block"].(map[string]any)
			if cb["type"] == "thinking" && thinkingIdx < 0 {
				thinkingIdx = idx
			}
		case "content_block_delta":
			delta, _ := e.Data["delta"].(map[string]any)
			if thinkingIdx >= 0 && idx == thinkingIdx && delta["type"] == "signature_delta" {
				sig, _ = delta["signature"].(string)
			}
		case "content_block_stop":
			if thinkingIdx >= 0 && idx == thinkingIdx {
				if sig == "" {
					t.Fatal("thinking block stopped without a signature_delta")
				}
				return sig
			}
		}
	}
	t.Fatal("no closed thinking block found")
	return ""
}

func TestStreamInlineThinking(t *testing.T) {
	// Legacy inline <thinking> XML mixed into assistantResponse content.
	out := runStream("claude-sonnet-4", 5, true, nil, []Event{
		assistantEv("<thinking>\nreasoning here</thinking>\n\nFinal answer"),
	})
	if strings.TrimSpace(out.thinking) != "reasoning here" {
		t.Errorf("thinking = %q; want %q", out.thinking, "reasoning here")
	}
	if out.text != "Final answer" {
		t.Errorf("text = %q; want %q", out.text, "Final answer")
	}
}

func TestStreamToolUse(t *testing.T) {
	out := runStream("claude-sonnet-4", 5, false, map[string]string{"rf": "read_file"}, []Event{
		assistantEv("Let me read that."),
		toolEv("rf", "tool-1", `{"path":"/x"}`, true),
	})
	if out.toolJSON != `{"path":"/x"}` {
		t.Errorf("tool json = %q", out.toolJSON)
	}
	// Original tool name must be restored from the map.
	if !hasToolName(out.events, "read_file") {
		t.Error("expected restored tool name read_file")
	}
	// stop_reason should be tool_use.
	if sr := finalStopReason(out.events); sr != "tool_use" {
		t.Errorf("stop_reason = %q; want tool_use", sr)
	}
}

func TestStreamTextAfterToolUse(t *testing.T) {
	// Text arriving after a tool_use must reopen a text block (no swallowing).
	out := runStream("claude-sonnet-4", 5, false, nil, []Event{
		assistantEv("first"),
		toolEv("t", "id1", `{}`, true),
		assistantEv("second"),
	})
	if out.text != "firstsecond" {
		t.Errorf("text = %q; want %q", out.text, "firstsecond")
	}
	// There must be at least two text content_block_start events.
	if n := countTextBlockStarts(out.events); n < 2 {
		t.Errorf("text block starts = %d; want >=2", n)
	}
}

func hasToolName(events []SseEvent, want string) bool {
	for _, e := range events {
		if e.Event != "content_block_start" {
			continue
		}
		cb, _ := e.Data["content_block"].(map[string]any)
		if cb["type"] == "tool_use" && cb["name"] == want {
			return true
		}
	}
	return false
}

func countTextBlockStarts(events []SseEvent) int {
	n := 0
	for _, e := range events {
		if e.Event != "content_block_start" {
			continue
		}
		cb, _ := e.Data["content_block"].(map[string]any)
		if cb["type"] == "text" {
			n++
		}
	}
	return n
}

func finalStopReason(events []SseEvent) string {
	for _, e := range events {
		if e.Event != "message_delta" {
			continue
		}
		delta, _ := e.Data["delta"].(map[string]any)
		if sr, ok := delta["stop_reason"].(string); ok {
			return sr
		}
	}
	return ""
}
