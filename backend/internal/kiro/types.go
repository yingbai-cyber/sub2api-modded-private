package kiro

import "encoding/json"

// This file defines the CodeWhisperer / Kiro upstream request JSON structures,
// mirroring kiro-rs kiro::model::requests::*. JSON tags use camelCase to match
// the upstream wire format.

// KiroRequest is the top-level request body sent to generateAssistantResponse.
type KiroRequest struct {
	ConversationState ConversationState `json:"conversationState"`
	ProfileArn        string            `json:"profileArn,omitempty"`
	// AdditionalModelRequestFields carries native effort (output_config).
	AdditionalModelRequestFields *AdditionalModelRequestFields `json:"additionalModelRequestFields,omitempty"`
}

// AdditionalModelRequestFields wraps output_config (sibling of conversationState).
type AdditionalModelRequestFields struct {
	OutputConfig AdditionalOutputConfig `json:"output_config"`
}

// AdditionalOutputConfig carries the effort tier (low/medium/high/xhigh/max).
type AdditionalOutputConfig struct {
	Effort string `json:"effort"`
}

// ConversationState is the core request structure holding the current message
// and history. Mirrors kiro-rs ConversationState.
type ConversationState struct {
	AgentContinuationID *string        `json:"agentContinuationId,omitempty"`
	AgentTaskType       *string        `json:"agentTaskType,omitempty"`
	ChatTriggerType     *string        `json:"chatTriggerType,omitempty"`
	CurrentMessage      CurrentMessage `json:"currentMessage"`
	ConversationID      string         `json:"conversationId"`
	History             []Message      `json:"history,omitempty"`
}

// CurrentMessage wraps the user input message.
type CurrentMessage struct {
	UserInputMessage UserInputMessage `json:"userInputMessage"`
}

// UserInputMessage is the current user turn.
type UserInputMessage struct {
	UserInputMessageContext UserInputMessageContext `json:"userInputMessageContext"`
	Content                 string                  `json:"content"`
	ModelID                 string                  `json:"modelId"`
	Images                  []KiroImage             `json:"images,omitempty"`
	Origin                  *string                 `json:"origin,omitempty"`
}

// UserInputMessageContext carries tool definitions and tool results.
type UserInputMessageContext struct {
	ToolResults []ToolResult `json:"toolResults,omitempty"`
	Tools       []Tool       `json:"tools,omitempty"`
}

// KiroImage is an image attachment (base64 bytes).
type KiroImage struct {
	Format string          `json:"format"`
	Source KiroImageSource `json:"source"`
}

// KiroImageSource wraps base64-encoded image bytes.
type KiroImageSource struct {
	Bytes string `json:"bytes"`
}

// Message is a history entry: either a user or assistant message. It
// serializes untagged (mirrors kiro-rs #[serde(untagged)] Message enum): the
// non-nil field determines the shape on the wire.
type Message struct {
	UserInputMessage         *UserMessage      `json:"userInputMessage,omitempty"`
	AssistantResponseMessage *AssistantMessage `json:"assistantResponseMessage,omitempty"`
}

// NewUserHistory builds a history user message.
func NewUserHistory(content, modelID string) Message {
	origin := "AI_EDITOR"
	return Message{UserInputMessage: &UserMessage{
		Content: content, ModelID: modelID, Origin: &origin,
	}}
}

// NewAssistantHistory builds a history assistant message.
func NewAssistantHistory(content string, toolUses []ToolUseEntry) Message {
	m := &AssistantMessage{Content: content}
	if len(toolUses) > 0 {
		m.ToolUses = toolUses
	}
	return Message{AssistantResponseMessage: m}
}

// UserMessage is a history user message.
type UserMessage struct {
	Content                 string                   `json:"content"`
	ModelID                 string                   `json:"modelId"`
	Origin                  *string                  `json:"origin,omitempty"`
	Images                  []KiroImage              `json:"images,omitempty"`
	UserInputMessageContext *UserInputMessageContext `json:"userInputMessageContext,omitempty"`
}

// AssistantMessage is a history assistant message.
type AssistantMessage struct {
	Content  string         `json:"content"`
	ToolUses []ToolUseEntry `json:"toolUses,omitempty"`
}

// Tool is a tool definition available to the model.
type Tool struct {
	ToolSpecification ToolSpecification `json:"toolSpecification"`
}

// ToolSpecification defines a tool's name, description and input schema.
type ToolSpecification struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema InputSchema `json:"inputSchema"`
}

// InputSchema wraps a JSON Schema definition under a "json" key.
type InputSchema struct {
	JSON json.RawMessage `json:"json"`
}

// ToolResult is a tool execution result returned to the model.
type ToolResult struct {
	ToolUseID string            `json:"toolUseId"`
	Content   []json.RawMessage `json:"content"`
	Status    *string           `json:"status,omitempty"`
	IsError   bool              `json:"isError,omitempty"`
}

// ToolUseEntry records a tool call in assistant history.
type ToolUseEntry struct {
	ToolUseID string          `json:"toolUseId"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
}
