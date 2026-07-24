package kiro

import (
	"bytes"
	"testing"
)

// buildStream encodes a sequence of (eventType,payload) frames into raw bytes.
func buildStream(t *testing.T, frames [][2]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	for _, f := range frames {
		_, _ = raw.Write(encodeFrame(t, eventHeaders(f[0]), []byte(f[1])))
	}
	return raw.Bytes()
}

func TestNonStreamBasicText(t *testing.T) {
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"Hello "}`},
		{"assistantResponseEvent", `{"content":"world"}`},
		{"meteringEvent", `{"usage":0.5}`},
	})
	res, err := BuildNonStreamResponse(bytes.NewReader(raw), "claude-sonnet-4", false, 12, nil)
	if err != nil {
		t.Fatalf("BuildNonStreamResponse: %v", err)
	}
	content, _ := res.Response["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content blocks = %d; want 1", len(content))
	}
	block, _ := content[0].(map[string]any)
	if block["type"] != "text" || block["text"] != "Hello world" {
		t.Errorf("block = %v", block)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("stop_reason = %q; want end_turn", res.StopReason)
	}
	if res.Credits != 0.5 {
		t.Errorf("credits = %v; want 0.5", res.Credits)
	}
	id, _ := res.Response["id"].(string)
	if len(id) < 5 || id[:4] != "msg_" {
		t.Errorf("id = %q; want msg_ prefix", id)
	}
}

func TestNonStreamToolUse(t *testing.T) {
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"calling"}`},
		{"toolUseEvent", `{"name":"rf","toolUseId":"t1","input":"{\"path\":","stop":false}`},
		{"toolUseEvent", `{"name":"rf","toolUseId":"t1","input":"\"/x\"}","stop":true}`},
	})
	res, err := BuildNonStreamResponse(bytes.NewReader(raw), "claude-sonnet-4", false, 5, map[string]string{"rf": "read_file"})
	if err != nil {
		t.Fatalf("BuildNonStreamResponse: %v", err)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q; want tool_use", res.StopReason)
	}
	content, _ := res.Response["content"].([]any)
	// text + tool_use
	if len(content) != 2 {
		t.Fatalf("content blocks = %d; want 2", len(content))
	}
	tool, _ := content[1].(map[string]any)
	if tool["type"] != "tool_use" || tool["name"] != "read_file" {
		t.Errorf("tool block = %v", tool)
	}
	input, _ := tool["input"].([]byte)
	_ = input
}

func TestNonStreamThinkingExtraction(t *testing.T) {
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"<thinking>\nmy reasoning</thinking>\n\nthe answer"}`},
	})
	res, err := BuildNonStreamResponse(bytes.NewReader(raw), "claude-sonnet-4", true, 5, nil)
	if err != nil {
		t.Fatalf("BuildNonStreamResponse: %v", err)
	}
	content, _ := res.Response["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content blocks = %d; want 2 (thinking+text)", len(content))
	}
	th, _ := content[0].(map[string]any)
	if th["type"] != "thinking" || th["thinking"] != "my reasoning" {
		t.Errorf("thinking block = %v", th)
	}
	txt, _ := content[1].(map[string]any)
	if txt["type"] != "text" || txt["text"] != "the answer" {
		t.Errorf("text block = %v", txt)
	}
}

func TestNonStreamContextUsageTokens(t *testing.T) {
	// contextUsageEvent overrides the estimated input tokens.
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"hi"}`},
		{"contextUsageEvent", `{"contextUsagePercentage":10.0}`},
	})
	res, err := BuildNonStreamResponse(bytes.NewReader(raw), "claude-sonnet-4", false, 999, nil)
	if err != nil {
		t.Fatalf("BuildNonStreamResponse: %v", err)
	}
	// 10% of the model context window, not the 999 estimate.
	window := ContextWindowSize("claude-sonnet-4")
	want := window / 10
	if res.InputTokens != want {
		t.Errorf("input_tokens = %d; want %d", res.InputTokens, want)
	}
}
