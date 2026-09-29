package kiro

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
