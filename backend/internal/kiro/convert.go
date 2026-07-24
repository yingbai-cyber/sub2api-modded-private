package kiro

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// This file converts an Anthropic MessagesRequest into a Kiro ConversationState,
// mirroring kiro-rs anthropic::converter.

// Tool description suffixes and system policy (mirrors kiro-rs constants).
const (
	writeToolDescriptionSuffix = "- IMPORTANT: If the content to write exceeds 150 lines, you MUST only write the first 50 lines using this tool, then use `Edit` tool to append the remaining content in chunks of no more than 50 lines each. If needed, leave a unique placeholder to help append content. Do NOT attempt to write all content at once."
	editToolDescriptionSuffix  = "- IMPORTANT: If the `new_string` content exceeds 50 lines, you MUST split it into multiple Edit calls, each replacing no more than 50 lines at a time. If used to append content, leave a unique placeholder to help append content. On the final chunk, do NOT include the placeholder."
	systemChunkedPolicy        = "When the Write or Edit tool has content size limits, always comply silently. Never suggest bypassing these limits via alternative tools. Never ask the user whether to switch approaches. Complete all chunked operations without commentary."
)

// toolNameMaxLen is the Kiro API tool-name length limit.
const toolNameMaxLen = 63

// ConvertOptions controls request conversion.
type ConvertOptions struct {
	// SuppressThinkingXML disables legacy <thinking_mode> injection (set when
	// native effort is in effect).
	SuppressThinkingXML bool
}

// ConversionResult is the converter output.
type ConversionResult struct {
	ConversationState ConversationState
	// ToolNameMap maps shortened tool names back to their originals (only
	// populated when over-long tool names were encountered).
	ToolNameMap map[string]string
}

// ConversionError describes why conversion failed.
type ConversionError struct{ Msg string }

func (e *ConversionError) Error() string { return e.Msg }

var (
	errEmptyMessages = &ConversionError{Msg: "消息列表为空"}
)

// ConvertRequest converts an Anthropic request into a Kiro ConversationState.
// mappedModel must be the already-mapped Kiro model id (see MapModel).
func ConvertRequest(req *MessagesRequest, mappedModel string, opts ConvertOptions) (*ConversionResult, error) {
	if len(req.Messages) == 0 {
		return nil, errEmptyMessages
	}

	// Prefill preprocessing: if the last message is an assistant prefill,
	// silently drop everything after the last user message.
	messages := req.Messages
	if last := messages[len(messages)-1]; last.Role != "user" {
		lastUser := -1
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role == "user" {
				lastUser = i
				break
			}
		}
		if lastUser < 0 {
			return nil, errEmptyMessages
		}
		messages = messages[:lastUser+1]
	}

	// conversationId: prefer session UUID extracted from metadata.user_id.
	conversationID := ""
	if req.Metadata != nil {
		conversationID = extractSessionID(req.Metadata.UserID)
	}
	if conversationID == "" {
		conversationID = uuid.NewString()
	}
	agentContinuationID := uuid.NewString()

	// Last message becomes the current message (guaranteed user after prefill).
	last := messages[len(messages)-1]
	textContent, images, toolResults := processMessageContent(last.Content)

	// Convert tool definitions (shortening over-long names).
	toolNameMap := map[string]string{}
	tools := convertTools(req.Tools, toolNameMap)

	// Build history (needed before tool pairing to collect history tool names).
	history := buildHistory(req, messages, mappedModel, toolNameMap, opts)

	// Validate/filter tool_use/tool_result pairing.
	validatedResults, orphanedToolUseIDs := validateToolPairing(history, toolResults)
	removeOrphanedToolUses(history, orphanedToolUseIDs)
	history = removeEmptyAssistantMessages(history)

	// Placeholder tools for history-referenced tools missing from tools list
	// (case-insensitive match, mirrors kiro-rs).
	existing := map[string]struct{}{}
	for _, t := range tools {
		existing[strings.ToLower(t.ToolSpecification.Name)] = struct{}{}
	}
	for _, name := range collectHistoryToolNames(history) {
		if _, ok := existing[strings.ToLower(name)]; !ok {
			tools = append(tools, createPlaceholderTool(name))
			existing[strings.ToLower(name)] = struct{}{}
		}
	}

	// Build current message context.
	ctx := UserInputMessageContext{}
	if len(tools) > 0 {
		ctx.Tools = tools
	}
	if len(validatedResults) > 0 {
		ctx.ToolResults = validatedResults
	}

	origin := "AI_EDITOR"
	userInput := UserInputMessage{
		UserInputMessageContext: ctx,
		Content:                 textContent,
		ModelID:                 mappedModel,
		Origin:                  &origin,
	}
	if len(images) > 0 {
		userInput.Images = images
	}

	taskType := "vibe"
	trigger := "MANUAL"
	state := ConversationState{
		AgentContinuationID: &agentContinuationID,
		AgentTaskType:       &taskType,
		ChatTriggerType:     &trigger,
		ConversationID:      conversationID,
		CurrentMessage:      CurrentMessage{UserInputMessage: userInput},
		History:             history,
	}

	return &ConversionResult{ConversationState: state, ToolNameMap: toolNameMap}, nil
}

// processMessageContent extracts text, images and tool results from a message
// content field (string or ContentBlock array).
func processMessageContent(raw json.RawMessage) (text string, images []KiroImage, toolResults []ToolResult) {
	blocks, bareText, isText := decodeContentBlocks(raw)
	if isText {
		return bareText, nil, nil
	}

	var textParts []string
	for i := range blocks {
		b := &blocks[i]
		switch b.Type {
		case "text":
			if b.Text != nil {
				textParts = append(textParts, *b.Text)
			}
		case "image":
			if b.Source != nil {
				if format, ok := imageFormat(b.Source.MediaType); ok {
					images = append(images, KiroImage{
						Format: format,
						Source: KiroImageSource{Bytes: b.Source.Data},
					})
				}
			}
		case "tool_result":
			if b.ToolUseID != nil {
				resultContent := extractToolResultContent(b.Content)
				isErr := b.IsError != nil && *b.IsError
				toolResults = append(toolResults, newToolResult(*b.ToolUseID, resultContent, isErr))
			}
		}
	}
	return strings.Join(textParts, "\n"), images, toolResults
}

// imageFormat maps an Anthropic media_type to a Kiro image format.
func imageFormat(mediaType string) (string, bool) {
	switch mediaType {
	case "image/jpeg":
		return "jpeg", true
	case "image/png":
		return "png", true
	case "image/gif":
		return "gif", true
	case "image/webp":
		return "webp", true
	default:
		return "", false
	}
}

// extractToolResultContent flattens a tool_result content field to text.
func extractToolResultContent(raw json.RawMessage) string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return ""
	}
	if trimmed[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) == nil {
			return s
		}
	}
	if trimmed[0] == '[' {
		var arr []map[string]json.RawMessage
		if json.Unmarshal(raw, &arr) == nil {
			var parts []string
			for _, item := range arr {
				if tv, ok := item["text"]; ok {
					var s string
					if json.Unmarshal(tv, &s) == nil {
						parts = append(parts, s)
					}
				}
			}
			return strings.Join(parts, "\n")
		}
	}
	return trimmed
}

// newToolResult builds a ToolResult with a single {"text": ...} content entry.
func newToolResult(toolUseID, content string, isError bool) ToolResult {
	status := "success"
	if isError {
		status = "error"
	}
	textJSON, _ := json.Marshal(map[string]string{"text": content})
	return ToolResult{
		ToolUseID: toolUseID,
		Content:   []json.RawMessage{textJSON},
		Status:    &status,
		IsError:   isError,
	}
}

// convertTools converts Anthropic tool defs into Kiro tools, applying custom
// description suffixes, length caps, schema normalization and name shortening.
func convertTools(tools []AnthropicTool, toolNameMap map[string]string) []Tool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]Tool, 0, len(tools))
	for i := range tools {
		t := &tools[i]
		description := t.Description
		switch t.Name {
		case "Write":
			description += "\n" + writeToolDescriptionSuffix
		case "Edit":
			description += "\n" + editToolDescriptionSuffix
		}
		description = truncateRunes(description, 10000)

		out = append(out, Tool{
			ToolSpecification: ToolSpecification{
				Name:        mapToolName(t.Name, toolNameMap),
				Description: description,
				InputSchema: InputSchema{JSON: normalizeJSONSchema(t.InputSchema)},
			},
		})
	}
	return out
}

// mapToolName shortens over-long names deterministically and records the map.
func mapToolName(name string, toolNameMap map[string]string) string {
	if len(name) <= toolNameMaxLen {
		return name
	}
	short := shortenToolName(name)
	toolNameMap[short] = name
	return short
}

// shortenToolName builds a deterministic short name: prefix + "_" + 8 hex.
func shortenToolName(name string) string {
	sum := sha256.Sum256([]byte(name))
	hashSuffix := hex.EncodeToString(sum[:])[:8]
	prefixMax := toolNameMaxLen - 1 - 8 // 54
	prefix := truncateRunes(name, prefixMax)
	return prefix + "_" + hashSuffix
}

// createPlaceholderTool builds a placeholder tool for a history-referenced tool.
func createPlaceholderTool(name string) Tool {
	schema := json.RawMessage(`{"$schema":"http://json-schema.org/draft-07/schema#","type":"object","properties":{},"required":[],"additionalProperties":true}`)
	return Tool{
		ToolSpecification: ToolSpecification{
			Name:        name,
			Description: "Tool used in conversation history",
			InputSchema: InputSchema{JSON: schema},
		},
	}
}

// normalizeJSONSchema fixes common malformed MCP tool schemas (mirrors kiro-rs).
func normalizeJSONSchema(raw json.RawMessage) json.RawMessage {
	fallback := json.RawMessage(`{"type":"object","properties":{},"required":[],"additionalProperties":true}`)
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return fallback
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return fallback
	}

	// type: must be a non-empty string.
	if v, ok := obj["type"]; !ok || !isNonEmptyJSONString(v) {
		obj["type"] = json.RawMessage(`"object"`)
	}
	// properties: must be an object.
	if v, ok := obj["properties"]; !ok || !isJSONObject(v) {
		obj["properties"] = json.RawMessage(`{}`)
	}
	// required: must be an array of strings.
	obj["required"] = normalizeRequired(obj["required"])
	// additionalProperties: bool or object, else true.
	if v, ok := obj["additionalProperties"]; !ok || !(isJSONBool(v) || isJSONObject(v)) {
		obj["additionalProperties"] = json.RawMessage(`true`)
	}

	out, err := json.Marshal(obj)
	if err != nil {
		return fallback
	}
	return out
}

// normalizeRequired keeps only string entries; returns [] otherwise.
func normalizeRequired(raw json.RawMessage) json.RawMessage {
	empty := json.RawMessage(`[]`)
	if len(raw) == 0 {
		return empty
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return empty
	}
	var strs []string
	for _, item := range arr {
		var s string
		if json.Unmarshal(item, &s) == nil {
			strs = append(strs, s)
		}
	}
	out, err := json.Marshal(strs)
	if err != nil || strs == nil {
		return empty
	}
	return out
}

// buildHistory builds the Kiro history from Anthropic messages, synthesizing a
// system turn, injecting thinking prefixes, and merging consecutive same-role
// messages. The last message is excluded (it becomes currentMessage).
func buildHistory(req *MessagesRequest, messages []AnthropicMsg, modelID string, toolNameMap map[string]string, opts ConvertOptions) []Message {
	var history []Message

	thinkingPrefix := ""
	if !opts.SuppressThinkingXML {
		thinkingPrefix = generateThinkingPrefix(req)
	}

	// 1. System message synthesis.
	systemContent := req.System.Text()
	if systemContent != "" {
		systemContent = systemContent + "\n" + systemChunkedPolicy
		final := systemContent
		if thinkingPrefix != "" && !hasThinkingTags(systemContent) {
			final = thinkingPrefix + "\n" + systemContent
		}
		history = append(history, NewUserHistory(final, modelID))
		history = append(history, NewAssistantHistory("I will follow these instructions.", nil))
	} else if thinkingPrefix != "" {
		history = append(history, NewUserHistory(thinkingPrefix, modelID))
		history = append(history, NewAssistantHistory("I will follow these instructions.", nil))
	}

	// 2. Regular history (all but the last message), merging same-role runs.
	end := len(messages) - 1
	var userBuf, assistantBuf []AnthropicMsg
	for i := 0; i < end; i++ {
		msg := messages[i]
		switch msg.Role {
		case "user":
			if len(assistantBuf) > 0 {
				history = append(history, mergeAssistantMessages(assistantBuf, toolNameMap))
				assistantBuf = nil
			}
			userBuf = append(userBuf, msg)
		case "assistant":
			if len(userBuf) > 0 {
				history = append(history, mergeUserMessages(userBuf, modelID))
				userBuf = nil
			}
			assistantBuf = append(assistantBuf, msg)
		}
	}
	if len(assistantBuf) > 0 {
		history = append(history, mergeAssistantMessages(assistantBuf, toolNameMap))
	}
	if len(userBuf) > 0 {
		history = append(history, mergeUserMessages(userBuf, modelID))
		// Auto-pair a trailing lone user message with an "OK" assistant.
		history = append(history, NewAssistantHistory("OK", nil))
	}
	return history
}

// mergeUserMessages merges consecutive user messages into one history entry.
func mergeUserMessages(messages []AnthropicMsg, modelID string) Message {
	var contentParts []string
	var allImages []KiroImage
	var allToolResults []ToolResult
	for i := range messages {
		text, images, toolResults := processMessageContent(messages[i].Content)
		if text != "" {
			contentParts = append(contentParts, text)
		}
		allImages = append(allImages, images...)
		allToolResults = append(allToolResults, toolResults...)
	}
	origin := "AI_EDITOR"
	um := &UserMessage{
		Content: strings.Join(contentParts, "\n"),
		ModelID: modelID,
		Origin:  &origin,
	}
	if len(allImages) > 0 {
		um.Images = allImages
	}
	if len(allToolResults) > 0 {
		um.UserInputMessageContext = &UserInputMessageContext{ToolResults: allToolResults}
	}
	return Message{UserInputMessage: um}
}

// convertAssistantMessage converts one assistant message, combining thinking
// and text and collecting tool_use entries.
func convertAssistantMessage(msg AnthropicMsg, toolNameMap map[string]string) *AssistantMessage {
	var thinkingContent, textContent strings.Builder
	var toolUses []ToolUseEntry

	blocks, bareText, isText := decodeContentBlocks(msg.Content)
	if isText {
		textContent.WriteString(bareText)
	} else {
		for i := range blocks {
			b := &blocks[i]
			switch b.Type {
			case "thinking":
				if b.Thinking != nil {
					thinkingContent.WriteString(*b.Thinking)
				}
			case "text":
				if b.Text != nil {
					textContent.WriteString(*b.Text)
				}
			case "tool_use":
				if b.ID != nil && b.Name != nil {
					input := b.Input
					if len(input) == 0 {
						input = json.RawMessage(`{}`)
					}
					toolUses = append(toolUses, ToolUseEntry{
						ToolUseID: *b.ID,
						Name:      mapToolName(*b.Name, toolNameMap),
						Input:     input,
					})
				}
			}
		}
	}

	think := thinkingContent.String()
	text := textContent.String()
	var finalContent string
	switch {
	case think != "" && text != "":
		finalContent = "<thinking>" + think + "</thinking>\n\n" + text
	case think != "":
		finalContent = "<thinking>" + think + "</thinking>"
	case text == "" && len(toolUses) > 0:
		finalContent = " "
	default:
		finalContent = text
	}

	am := &AssistantMessage{Content: finalContent}
	if len(toolUses) > 0 {
		am.ToolUses = toolUses
	}
	return am
}

// mergeAssistantMessages merges consecutive assistant messages into one.
func mergeAssistantMessages(messages []AnthropicMsg, toolNameMap map[string]string) Message {
	if len(messages) == 1 {
		return Message{AssistantResponseMessage: convertAssistantMessage(messages[0], toolNameMap)}
	}
	var allToolUses []ToolUseEntry
	var contentParts []string
	for i := range messages {
		am := convertAssistantMessage(messages[i], toolNameMap)
		if strings.TrimSpace(am.Content) != "" {
			contentParts = append(contentParts, am.Content)
		}
		allToolUses = append(allToolUses, am.ToolUses...)
	}
	content := strings.Join(contentParts, "\n\n")
	if content == "" && len(allToolUses) > 0 {
		content = " "
	}
	am := &AssistantMessage{Content: content}
	if len(allToolUses) > 0 {
		am.ToolUses = allToolUses
	}
	return Message{AssistantResponseMessage: am}
}

// generateThinkingPrefix builds the legacy XML thinking prefix, if applicable.
func generateThinkingPrefix(req *MessagesRequest) string {
	if req.Thinking == nil {
		return ""
	}
	switch req.Thinking.Type {
	case "enabled":
		return "<thinking_mode>enabled</thinking_mode><max_thinking_length>" +
			itoa(req.Thinking.BudgetTokens) + "</max_thinking_length>"
	case "adaptive":
		effort := "high"
		if req.OutputConfig != nil && req.OutputConfig.Effort != "" {
			effort = req.OutputConfig.Effort
		}
		return "<thinking_mode>adaptive</thinking_mode><thinking_effort>" + effort + "</thinking_effort>"
	default:
		return ""
	}
}

// hasThinkingTags reports whether content already carries thinking tags.
func hasThinkingTags(content string) bool {
	return strings.Contains(content, "<thinking_mode>") || strings.Contains(content, "<max_thinking_length>")
}

// validateToolPairing filters current-message tool_results to those pairing
// with an unpaired history tool_use, and returns the set of orphaned tool_use
// ids (tool_use with no tool_result anywhere) to strip from history.
func validateToolPairing(history []Message, toolResults []ToolResult) ([]ToolResult, map[string]struct{}) {
	allToolUseIDs := map[string]struct{}{}
	historyResultIDs := map[string]struct{}{}
	for i := range history {
		msg := &history[i]
		if msg.AssistantResponseMessage != nil {
			for _, tu := range msg.AssistantResponseMessage.ToolUses {
				allToolUseIDs[tu.ToolUseID] = struct{}{}
			}
		}
		if msg.UserInputMessage != nil && msg.UserInputMessage.UserInputMessageContext != nil {
			for _, r := range msg.UserInputMessage.UserInputMessageContext.ToolResults {
				historyResultIDs[r.ToolUseID] = struct{}{}
			}
		}
	}

	// Unpaired = all tool_use ids not already paired in history.
	unpaired := map[string]struct{}{}
	for id := range allToolUseIDs {
		if _, paired := historyResultIDs[id]; !paired {
			unpaired[id] = struct{}{}
		}
	}

	var filtered []ToolResult
	for _, r := range toolResults {
		if _, ok := unpaired[r.ToolUseID]; ok {
			filtered = append(filtered, r)
			delete(unpaired, r.ToolUseID)
		}
		// else: duplicate or orphaned tool_result — dropped.
	}
	// Remaining unpaired ids are orphaned tool_uses to remove from history.
	return filtered, unpaired
}

// removeOrphanedToolUses strips tool_use entries whose ids are orphaned.
func removeOrphanedToolUses(history []Message, orphaned map[string]struct{}) {
	if len(orphaned) == 0 {
		return
	}
	for i := range history {
		am := history[i].AssistantResponseMessage
		if am == nil || len(am.ToolUses) == 0 {
			continue
		}
		kept := am.ToolUses[:0]
		for _, tu := range am.ToolUses {
			if _, bad := orphaned[tu.ToolUseID]; !bad {
				kept = append(kept, tu)
			}
		}
		if len(kept) == 0 {
			am.ToolUses = nil
		} else {
			am.ToolUses = kept
		}
	}
}

// removeEmptyAssistantMessages drops assistant messages that became empty (no
// content, no tool_uses) plus their paired preceding user message.
func removeEmptyAssistantMessages(history []Message) []Message {
	remove := map[int]struct{}{}
	for i := range history {
		am := history[i].AssistantResponseMessage
		if am == nil {
			continue
		}
		if strings.TrimSpace(am.Content) == "" && len(am.ToolUses) == 0 {
			remove[i] = struct{}{}
			if i > 0 && history[i-1].UserInputMessage != nil {
				remove[i-1] = struct{}{}
			}
		}
	}
	if len(remove) == 0 {
		return history
	}
	out := history[:0]
	for i := range history {
		if _, drop := remove[i]; !drop {
			out = append(out, history[i])
		}
	}
	return out
}

// collectHistoryToolNames returns the distinct tool names used in history.
func collectHistoryToolNames(history []Message) []string {
	var names []string
	seen := map[string]struct{}{}
	for i := range history {
		am := history[i].AssistantResponseMessage
		if am == nil {
			continue
		}
		for _, tu := range am.ToolUses {
			if _, ok := seen[tu.Name]; !ok {
				seen[tu.Name] = struct{}{}
				names = append(names, tu.Name)
			}
		}
	}
	return names
}

// extractSessionID extracts a session UUID from metadata.user_id. Supports a
// JSON object with "session_id" or the string form "..session_<uuid>".
func extractSessionID(userID string) string {
	if userID == "" {
		return ""
	}
	// JSON form.
	var obj map[string]json.RawMessage
	if json.Unmarshal([]byte(userID), &obj) == nil {
		if v, ok := obj["session_id"]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && isValidUUID(s) {
				return s
			}
		}
	}
	// String form: content after "session_".
	if idx := strings.Index(userID, "session_"); idx >= 0 {
		part := userID[idx+8:]
		if len(part) >= 36 {
			candidate := part[:36]
			if isValidUUID(candidate) {
				return candidate
			}
		}
	}
	return ""
}

// isValidUUID does a light UUID shape check (36 chars, 4 dashes).
func isValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	dashes := 0
	for _, c := range s {
		if c == '-' {
			dashes++
		}
	}
	return dashes == 4
}

// truncateRunes truncates s to at most n runes (UTF-8 safe).
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i]
		}
		count++
	}
	return s
}

func isNonEmptyJSONString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil && s != ""
}

func isJSONObject(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return len(t) > 0 && t[0] == '{'
}

func isJSONBool(raw json.RawMessage) bool {
	t := strings.TrimSpace(string(raw))
	return t == "true" || t == "false"
}

// itoa converts an int to its decimal string.
func itoa(n int) string {
	return strconv.Itoa(n)
}
