package kiro

// This file ports kiro-rs token::count_tokens input-token estimation. It is
// intentionally distinct from estimateTokens (stream_util.go), which mirrors
// kiro-rs estimate_tokens and is used for OUTPUT token accounting during
// streaming. CountInputTokens seeds message_start.usage.input_tokens as a
// local fallback when the upstream omits a contextUsageEvent.

// isNonWesternChar reports whether c falls OUTSIDE the Latin/ASCII ranges
// (mirrors kiro-rs is_non_western_char). Non-western runes weigh 4 char-units,
// western runes weigh 1.
func isNonWesternChar(c rune) bool {
	switch {
	case c >= 0x0000 && c <= 0x007F, // Basic ASCII
		c >= 0x0080 && c <= 0x00FF, // Latin-1 Supplement
		c >= 0x0100 && c <= 0x024F, // Latin Extended-A/B
		c >= 0x1E00 && c <= 0x1EFF, // Latin Extended Additional
		c >= 0x2C60 && c <= 0x2C7F, // Latin Extended-C
		c >= 0xA720 && c <= 0xA7FF, // Latin Extended-D
		c >= 0xAB30 && c <= 0xAB6F: // Latin Extended-E
		return false
	default:
		return true
	}
}

// countTokens estimates tokens for one text (mirrors kiro-rs count_tokens):
// char-units / 4, then an accumulation multiplier that decays with magnitude.
// The result is truncated toward zero (matching Rust `as u64`).
func countTokens(text string) int {
	var units float64
	for _, c := range text {
		if isNonWesternChar(c) {
			units += 4.0
		} else {
			units += 1.0
		}
	}
	tokens := units / 4.0
	var acc float64
	switch {
	case tokens < 100.0:
		acc = tokens * 1.5
	case tokens < 200.0:
		acc = tokens * 1.3
	case tokens < 300.0:
		acc = tokens * 1.25
	case tokens < 800.0:
		acc = tokens * 1.2
	default:
		acc = tokens * 1.0
	}
	if acc < 0 {
		return 0
	}
	return int(acc)
}

// CountInputTokens estimates the input tokens of an Anthropic request locally
// (mirrors kiro-rs count_all_tokens_local): sums system blocks, message text
// content and tool name/description/input_schema. Result is at least 1.
func CountInputTokens(req *MessagesRequest) int {
	total := 0

	// System prompt blocks.
	for _, b := range req.System.Blocks {
		total += countTokens(b.Text)
	}

	// Message content: bare string, or the "text" field of each content block.
	for i := range req.Messages {
		blocks, bare, isText := decodeContentBlocks(req.Messages[i].Content)
		if isText {
			total += countTokens(bare)
			continue
		}
		for j := range blocks {
			if blocks[j].Text != nil {
				total += countTokens(*blocks[j].Text)
			}
		}
	}

	// Tool definitions.
	for i := range req.Tools {
		total += countTokens(req.Tools[i].Name)
		total += countTokens(req.Tools[i].Description)
		if len(req.Tools[i].InputSchema) > 0 {
			total += countTokens(string(req.Tools[i].InputSchema))
		}
	}

	if total < 1 {
		return 1
	}
	return total
}
