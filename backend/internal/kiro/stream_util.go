package kiro

import "unicode/utf8"

// computeCumulativeDelta returns the new suffix of chunk relative to previous.
// Kiro assistantResponseEvent / reasoningContentEvent typically return
// cumulative text rather than deltas; this recovers the incremental part.
// Mirrors kiro-rs compute_cumulative_delta.
func computeCumulativeDelta(chunk, previous string) string {
	if previous == "" {
		return chunk
	}
	if chunk == previous {
		return ""
	}
	if len(chunk) > len(previous) && hasPrefix(chunk, previous) {
		return chunk[len(previous):]
	}
	if len(previous) > len(chunk) && hasPrefix(previous, chunk) {
		return ""
	}
	overlap := longestSuffixPrefixOverlap(previous, chunk)
	// overlap == len(chunk) means chunk is wholly contained in previous's suffix
	// (e.g. previous="abcXYZ", chunk="XYZ"): there is no new text, and indexing
	// chunk[overlap] would panic with index out of range.
	if overlap >= len(chunk) {
		return ""
	}
	if overlap > 0 && utf8.RuneStart(chunk[overlap]) {
		return chunk[overlap:]
	}
	return chunk
}

// hasPrefix is a small strings.HasPrefix without importing strings here.
func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// longestSuffixPrefixOverlap finds the longest length where previous's suffix
// equals chunk's prefix.
func longestSuffixPrefixOverlap(previous, chunk string) int {
	maxLen := len(previous)
	if len(chunk) < maxLen {
		maxLen = len(chunk)
	}
	for l := maxLen; l >= 1; l-- {
		if previous[len(previous)-l:] == chunk[:l] {
			return l
		}
	}
	return 0
}

// estimateTokens is a rough token estimate: Chinese ~1.5 chars/token, other
// ~4 chars/token. Mirrors kiro-rs estimate_tokens (min 1).
func estimateTokens(text string) int {
	chineseCount := 0
	otherCount := 0
	for _, c := range text {
		if c >= '\u4E00' && c <= '\u9FFF' {
			chineseCount++
		} else {
			otherCount++
		}
	}
	chineseTokens := (chineseCount*2 + 2) / 3
	otherTokens := (otherCount + 3) / 4
	total := chineseTokens + otherTokens
	if total < 1 {
		return 1
	}
	return total
}
