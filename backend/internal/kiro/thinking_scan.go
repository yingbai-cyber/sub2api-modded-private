package kiro

import (
	"strings"
	"unicode/utf8"
)

// This file ports kiro-rs anthropic::stream tag-scanning helpers. The model may
// emit legacy <thinking>...</thinking> XML inline in assistantResponseEvent
// content; these helpers locate *real* tags (not quoted/backticked mentions)
// with cross-chunk safety.
//
// Go strings are UTF-8 byte sequences (like Rust String), so byte offsets and
// slicing map directly; findCharBoundary replaces Rust's is_char_boundary walk.

// quoteChars are wrapping characters that indicate a tag mention rather than a
// real tag (inline code, strings, punctuation).
const quoteChars = "`\"'\\#!@$%^&*()-_=+[]{};:<>,.?/"

// findCharBoundary returns the largest valid UTF-8 boundary <= target.
func findCharBoundary(s string, target int) int {
	if target >= len(s) {
		return len(s)
	}
	if target <= 0 {
		return 0
	}
	pos := target
	for pos > 0 && !utf8.RuneStart(s[pos]) {
		pos--
	}
	return pos
}

// isQuoteChar reports whether the byte at pos is a wrapping/quote character.
func isQuoteChar(buffer string, pos int) bool {
	if pos < 0 || pos >= len(buffer) {
		return false
	}
	return strings.IndexByte(quoteChars, buffer[pos]) >= 0
}

// findRealThinkingEndTag finds a real </thinking> tag: not quote-wrapped, and
// followed by "\n\n". Returns -1 if none (or if more data is needed).
func findRealThinkingEndTag(buffer string) int {
	const tag = "</thinking>"
	searchStart := 0
	for {
		rel := strings.Index(buffer[searchStart:], tag)
		if rel < 0 {
			return -1
		}
		abs := searchStart + rel

		hasQuoteBefore := abs > 0 && isQuoteChar(buffer, abs-1)
		afterPos := abs + len(tag)
		hasQuoteAfter := isQuoteChar(buffer, afterPos)
		if hasQuoteBefore || hasQuoteAfter {
			searchStart = abs + 1
			continue
		}

		after := buffer[afterPos:]
		if len(after) < 2 {
			// Not enough data to confirm the trailing \n\n; wait for more.
			return -1
		}
		if strings.HasPrefix(after, "\n\n") {
			return abs
		}
		searchStart = abs + 1
	}
}

// findRealThinkingEndTagAtBufferEnd finds a </thinking> tag followed only by
// whitespace (boundary scenario: tag then tool_use or end-of-stream).
func findRealThinkingEndTagAtBufferEnd(buffer string) int {
	const tag = "</thinking>"
	searchStart := 0
	for {
		rel := strings.Index(buffer[searchStart:], tag)
		if rel < 0 {
			return -1
		}
		abs := searchStart + rel

		hasQuoteBefore := abs > 0 && isQuoteChar(buffer, abs-1)
		afterPos := abs + len(tag)
		hasQuoteAfter := isQuoteChar(buffer, afterPos)
		if hasQuoteBefore || hasQuoteAfter {
			searchStart = abs + 1
			continue
		}
		if strings.TrimSpace(buffer[afterPos:]) == "" {
			return abs
		}
		searchStart = abs + 1
	}
}

// findRealThinkingStartTag finds a real <thinking> tag not quote-wrapped.
func findRealThinkingStartTag(buffer string) int {
	const tag = "<thinking>"
	searchStart := 0
	for {
		rel := strings.Index(buffer[searchStart:], tag)
		if rel < 0 {
			return -1
		}
		abs := searchStart + rel

		hasQuoteBefore := abs > 0 && isQuoteChar(buffer, abs-1)
		afterPos := abs + len(tag)
		hasQuoteAfter := isQuoteChar(buffer, afterPos)
		if !hasQuoteBefore && !hasQuoteAfter {
			return abs
		}
		searchStart = abs + 1
	}
}

// ExtractThinkingFromCompleteText extracts a thinking block from complete text
// (non-streaming). Returns (thinking, hasThinking, remainingText).
func ExtractThinkingFromCompleteText(text string) (string, bool, string) {
	startPos := findRealThinkingStartTag(text)
	if startPos < 0 {
		return "", false, text
	}
	before := text[:startPos]
	afterOpen := text[startPos+len("<thinking>"):]

	var thinkingRaw, textAfter string
	if endPos := findRealThinkingEndTag(afterOpen); endPos >= 0 {
		thinkingRaw = afterOpen[:endPos]
		textAfter = afterOpen[endPos+len("</thinking>\n\n"):]
	} else if endPos := findRealThinkingEndTagAtBufferEnd(afterOpen); endPos >= 0 {
		thinkingRaw = afterOpen[:endPos]
		textAfter = strings.TrimLeft(afterOpen[endPos+len("</thinking>"):], " \t\r\n")
	} else {
		return "", false, text
	}

	thinkingContent := strings.TrimPrefix(thinkingRaw, "\n")

	var remaining strings.Builder
	if strings.TrimSpace(before) != "" {
		remaining.WriteString(before)
	}
	remaining.WriteString(textAfter)

	if thinkingContent == "" {
		return "", false, remaining.String()
	}
	return thinkingContent, true, remaining.String()
}
