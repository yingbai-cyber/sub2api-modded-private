package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

// mkReq builds a MessagesRequest from a JSON body for readable test fixtures.
func mkReq(t *testing.T, body string) *MessagesRequest {
	t.Helper()
	var req MessagesRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("unmarshal req: %v", err)
	}
	return &req
}

func TestConvertSimpleTextMessage(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4",
		"max_tokens":100,
		"messages":[{"role":"user","content":"Hello"}]
	}`)
	res, err := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	cm := res.ConversationState.CurrentMessage.UserInputMessage
	if cm.Content != "Hello" {
		t.Errorf("content = %q", cm.Content)
	}
	if cm.ModelID != "claude-sonnet-4.5" {
		t.Errorf("modelId = %q", cm.ModelID)
	}
	if len(res.ConversationState.History) != 0 {
		t.Errorf("expected no history, got %d", len(res.ConversationState.History))
	}
}

func TestConvertSystemPromptSynthesized(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"system":"You are helpful",
		"messages":[{"role":"user","content":"Hi"}]
	}`)
	res, err := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	h := res.ConversationState.History
	if len(h) != 2 {
		t.Fatalf("expected system user+assistant pair, got %d", len(h))
	}
	if h[0].UserInputMessage == nil || !strings.Contains(h[0].UserInputMessage.Content, "You are helpful") {
		t.Error("system content missing from first history user message")
	}
	if !strings.Contains(h[0].UserInputMessage.Content, systemChunkedPolicy) {
		t.Error("chunked policy should be appended to system content")
	}
	if h[1].AssistantResponseMessage == nil || h[1].AssistantResponseMessage.Content != "I will follow these instructions." {
		t.Error("system pair assistant reply mismatch")
	}
}

func TestConvertSystemArrayForm(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"system":[{"text":"A"},{"text":"B"}],
		"messages":[{"role":"user","content":"Hi"}]
	}`)
	res, _ := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if !strings.Contains(res.ConversationState.History[0].UserInputMessage.Content, "A\nB") {
		t.Error("system array blocks should be joined with newline")
	}
}

func TestConvertHistoryPairing(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"messages":[
			{"role":"user","content":"first"},
			{"role":"assistant","content":"reply"},
			{"role":"user","content":"second"}
		]
	}`)
	res, _ := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	h := res.ConversationState.History
	if len(h) != 2 {
		t.Fatalf("expected 2 history msgs (first user, reply assistant), got %d", len(h))
	}
	if h[0].UserInputMessage == nil || h[0].UserInputMessage.Content != "first" {
		t.Error("first history user message mismatch")
	}
	if h[1].AssistantResponseMessage == nil || h[1].AssistantResponseMessage.Content != "reply" {
		t.Error("history assistant message mismatch")
	}
	// current message is "second"
	if res.ConversationState.CurrentMessage.UserInputMessage.Content != "second" {
		t.Error("current message should be the last user turn")
	}
}

func TestConvertPrefillDropped(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"messages":[
			{"role":"user","content":"question"},
			{"role":"assistant","content":"prefill start"}
		]
	}`)
	res, err := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// After dropping the trailing assistant prefill, current message is "question".
	if res.ConversationState.CurrentMessage.UserInputMessage.Content != "question" {
		t.Errorf("prefill should be dropped; current = %q",
			res.ConversationState.CurrentMessage.UserInputMessage.Content)
	}
}

func TestConvertToolPairingAndOrphanRemoval(t *testing.T) {
	// assistant calls tool t1; the final user turn provides its result.
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"messages":[
			{"role":"user","content":"use a tool"},
			{"role":"assistant","content":[
				{"type":"tool_use","id":"t1","name":"read_file","input":{"p":"/x"}},
				{"type":"tool_use","id":"orphan","name":"ghost","input":{}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"t1","content":"file body"}
			]}
		]
	}`)
	res, err := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// The orphan tool_use (no matching result) must be removed from history.
	for _, h := range res.ConversationState.History {
		if h.AssistantResponseMessage != nil {
			for _, tu := range h.AssistantResponseMessage.ToolUses {
				if tu.ToolUseID == "orphan" {
					t.Error("orphaned tool_use should have been removed")
				}
			}
		}
	}
	// The valid tool_result should appear in the current message context.
	cm := res.ConversationState.CurrentMessage.UserInputMessage
	if len(cm.UserInputMessageContext.ToolResults) != 1 {
		t.Fatalf("expected 1 validated tool result, got %d", len(cm.UserInputMessageContext.ToolResults))
	}
	if cm.UserInputMessageContext.ToolResults[0].ToolUseID != "t1" {
		t.Error("wrong tool result id preserved")
	}
}

func TestConvertToolNameShortening(t *testing.T) {
	longName := strings.Repeat("x", 80)
	req := &MessagesRequest{
		Model:     "claude-sonnet-4",
		MaxTokens: 100,
		Tools: []AnthropicTool{
			{Name: longName, Description: "d", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []AnthropicMsg{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	res, err := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if err != nil {
		t.Fatal(err)
	}
	tools := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools
	if len(tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(tools))
	}
	if len(tools[0].ToolSpecification.Name) > toolNameMaxLen {
		t.Errorf("tool name not shortened: len=%d", len(tools[0].ToolSpecification.Name))
	}
	if len(res.ToolNameMap) != 1 {
		t.Error("tool name map should record the shortened mapping")
	}
}

func TestConvertWriteToolSuffix(t *testing.T) {
	req := &MessagesRequest{
		Model:     "claude-sonnet-4",
		MaxTokens: 100,
		Tools: []AnthropicTool{
			{Name: "Write", Description: "writes files", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		Messages: []AnthropicMsg{{Role: "user", Content: json.RawMessage(`"hi"`)}},
	}
	res, _ := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	desc := res.ConversationState.CurrentMessage.UserInputMessage.UserInputMessageContext.Tools[0].ToolSpecification.Description
	if !strings.Contains(desc, "150 lines") {
		t.Error("Write tool should get the chunked-write suffix")
	}
}

func TestConvertThinkingPrefixInjected(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-opus-4-7","max_tokens":100,
		"system":"base",
		"thinking":{"type":"enabled","budget_tokens":2048},
		"messages":[{"role":"user","content":"hi"}]
	}`)
	res, _ := ConvertRequest(req, "claude-opus-4.7", ConvertOptions{SuppressThinkingXML: false})
	sys := res.ConversationState.History[0].UserInputMessage.Content
	if !strings.Contains(sys, "<thinking_mode>enabled</thinking_mode>") {
		t.Errorf("thinking prefix not injected: %q", sys)
	}
	if !strings.Contains(sys, "<max_thinking_length>2048</max_thinking_length>") {
		t.Error("budget tokens not in prefix")
	}
}

func TestConvertThinkingSuppressed(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-opus-4-7","max_tokens":100,
		"system":"base",
		"thinking":{"type":"enabled","budget_tokens":2048},
		"messages":[{"role":"user","content":"hi"}]
	}`)
	res, _ := ConvertRequest(req, "claude-opus-4.7", ConvertOptions{SuppressThinkingXML: true})
	sys := res.ConversationState.History[0].UserInputMessage.Content
	if strings.Contains(sys, "<thinking_mode>") {
		t.Error("thinking prefix should be suppressed when native effort active")
	}
}

func TestConvertAssistantThinkingBlock(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"messages":[
			{"role":"user","content":"q"},
			{"role":"assistant","content":[
				{"type":"thinking","thinking":"let me think"},
				{"type":"text","text":"answer"}
			]},
			{"role":"user","content":"next"}
		]
	}`)
	res, _ := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	var found bool
	for _, h := range res.ConversationState.History {
		if h.AssistantResponseMessage != nil && strings.Contains(h.AssistantResponseMessage.Content, "<thinking>let me think</thinking>") {
			found = true
			if !strings.Contains(h.AssistantResponseMessage.Content, "answer") {
				t.Error("assistant text should follow thinking block")
			}
		}
	}
	if !found {
		t.Error("assistant thinking block not converted to <thinking> tag")
	}
}

func TestConvertEmptyMessagesError(t *testing.T) {
	req := &MessagesRequest{Model: "claude-sonnet-4", MaxTokens: 100}
	if _, err := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{}); err == nil {
		t.Error("empty messages should error")
	}
}

func TestConvertSessionIDFromMetadata(t *testing.T) {
	req := mkReq(t, `{
		"model":"claude-sonnet-4","max_tokens":100,
		"metadata":{"user_id":"user_abc_account__session_0b4445e1-f5be-49e1-87ce-62bbc28ad705"},
		"messages":[{"role":"user","content":"hi"}]
	}`)
	res, _ := ConvertRequest(req, "claude-sonnet-4.5", ConvertOptions{})
	if res.ConversationState.ConversationID != "0b4445e1-f5be-49e1-87ce-62bbc28ad705" {
		t.Errorf("conversationId = %q; want extracted session UUID", res.ConversationState.ConversationID)
	}
}

func TestNormalizeJSONSchemaFixesMalformed(t *testing.T) {
	// required: null and missing type should be normalized.
	out := normalizeJSONSchema(json.RawMessage(`{"required":null,"properties":null}`))
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatal(err)
	}
	if string(obj["type"]) != `"object"` {
		t.Errorf("type = %s; want object", obj["type"])
	}
	if string(obj["required"]) != `[]` {
		t.Errorf("required = %s; want []", obj["required"])
	}
	if !isJSONObject(obj["properties"]) {
		t.Errorf("properties = %s; want object", obj["properties"])
	}
}
