package kiro

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConversationStateSerialize(t *testing.T) {
	taskType := "vibe"
	trigger := "MANUAL"
	origin := "AI_EDITOR"
	state := ConversationState{
		AgentTaskType:   &taskType,
		ChatTriggerType: &trigger,
		ConversationID:  "conv-123",
		CurrentMessage: CurrentMessage{
			UserInputMessage: UserInputMessage{
				Content: "Hello",
				ModelID: "claude-sonnet-4.5",
				Origin:  &origin,
			},
		},
	}
	b, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	for _, want := range []string{
		`"conversationId":"conv-123"`,
		`"agentTaskType":"vibe"`,
		`"content":"Hello"`,
		`"modelId":"claude-sonnet-4.5"`,
		`"userInputMessage"`,
	} {
		if !strings.Contains(js, want) {
			t.Errorf("serialized state missing %q; got %s", want, js)
		}
	}
	// history omitted when empty
	if strings.Contains(js, `"history"`) {
		t.Errorf("empty history should be omitted; got %s", js)
	}
}

func TestHistoryUntaggedSerialize(t *testing.T) {
	history := []Message{
		NewUserHistory("Hello", "claude-sonnet-4.5"),
		NewAssistantHistory("Hi!", nil),
	}
	b, err := json.Marshal(history)
	if err != nil {
		t.Fatal(err)
	}
	js := string(b)
	if !strings.Contains(js, "userInputMessage") {
		t.Errorf("missing userInputMessage; got %s", js)
	}
	if !strings.Contains(js, "assistantResponseMessage") {
		t.Errorf("missing assistantResponseMessage; got %s", js)
	}
}

func TestToolResultSerialize(t *testing.T) {
	status := "success"
	tr := ToolResult{
		ToolUseID: "tool-789",
		Content:   []json.RawMessage{json.RawMessage(`{"text":"Done"}`)},
		Status:    &status,
	}
	b, _ := json.Marshal(tr)
	js := string(b)
	if !strings.Contains(js, `"toolUseId":"tool-789"`) {
		t.Errorf("missing toolUseId; got %s", js)
	}
	if !strings.Contains(js, `"status":"success"`) {
		t.Errorf("missing status; got %s", js)
	}
	// isError=false should be omitted
	if strings.Contains(js, "isError") {
		t.Errorf("isError=false should be omitted; got %s", js)
	}
}

func TestAdditionalModelRequestFieldsSerialize(t *testing.T) {
	f := AdditionalModelRequestFields{OutputConfig: AdditionalOutputConfig{Effort: "high"}}
	b, _ := json.Marshal(f)
	js := string(b)
	if !strings.Contains(js, `"output_config"`) || !strings.Contains(js, `"effort":"high"`) {
		t.Errorf("effort serialization wrong; got %s", js)
	}
}
