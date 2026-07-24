package kiro

import (
	"encoding/json"
	"strings"
)

// This file defines the Anthropic Messages API input types that the converter
// consumes, mirroring kiro-rs anthropic::types. The wire format uses snake_case.
//
// Two fields are polymorphic on the wire and use custom unmarshalling:
//   - MessagesRequest.system : string | []{text}
//   - Message.content        : string | []ContentBlock (kept as RawMessage)

// MessagesRequest is the inbound Anthropic /v1/messages request body.
type MessagesRequest struct {
	Model        string          `json:"model"`
	MaxTokens    int             `json:"max_tokens"`
	Messages     []AnthropicMsg  `json:"messages"`
	Stream       bool            `json:"stream"`
	System       SystemPrompt    `json:"system"`
	Tools        []AnthropicTool `json:"tools"`
	ToolChoice   json.RawMessage `json:"tool_choice"`
	Thinking     *ThinkingConfig `json:"thinking"`
	OutputConfig *OutputConfig   `json:"output_config"`
	Metadata     *Metadata       `json:"metadata"`
}

// AnthropicMsg is a single message. Content is kept raw (string or array) and
// decoded on demand by the converter.
type AnthropicMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// ThinkingConfig is the Anthropic thinking control block.
type ThinkingConfig struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens"`
}

// IsEnabled reports whether thinking intent is present.
func (t *ThinkingConfig) IsEnabled() bool {
	return t != nil && (t.Type == "enabled" || t.Type == "adaptive")
}

// OutputConfig carries the native effort tier requested by the client.
type OutputConfig struct {
	Effort string `json:"effort"`
}

// Metadata carries Claude Code session info.
type Metadata struct {
	UserID string `json:"user_id"`
}

// AnthropicTool is an inbound tool definition (normal or web_search).
type AnthropicTool struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	MaxUses     *int            `json:"max_uses,omitempty"`
}

// ContentBlock is one item of a message content array.
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      *string         `json:"text,omitempty"`
	Thinking  *string         `json:"thinking,omitempty"`
	ToolUseID *string         `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	Name      *string         `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ID        *string         `json:"id,omitempty"`
	IsError   *bool           `json:"is_error,omitempty"`
	Source    *ImageSource    `json:"source,omitempty"`
}

// ImageSource is a base64 image payload.
type ImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// SystemPrompt handles the polymorphic system field (string | []{text}).
type SystemPrompt struct {
	Blocks []SystemMessage
}

// SystemMessage is a single system text block.
type SystemMessage struct {
	Text string `json:"text"`
}

// UnmarshalJSON accepts a bare string, an array of {text}, or null.
func (s *SystemPrompt) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	if trimmed[0] == '"' {
		var str string
		if err := json.Unmarshal(data, &str); err != nil {
			return err
		}
		if str != "" {
			s.Blocks = []SystemMessage{{Text: str}}
		}
		return nil
	}
	var arr []SystemMessage
	if err := json.Unmarshal(data, &arr); err != nil {
		return err
	}
	s.Blocks = arr
	return nil
}

// Text concatenates all system blocks with newlines.
func (s *SystemPrompt) Text() string {
	if len(s.Blocks) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Blocks))
	for _, b := range s.Blocks {
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n")
}

// decodeContentBlocks decodes a message content field into blocks. When the
// content is a bare string, it returns a single synthetic text block.
func decodeContentBlocks(raw json.RawMessage) (blocks []ContentBlock, bareText string, isText bool) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, "", true
	}
	if trimmed[0] == '"' {
		var str string
		if err := json.Unmarshal(raw, &str); err == nil {
			return nil, str, true
		}
	}
	_ = json.Unmarshal(raw, &blocks)
	return blocks, "", false
}
