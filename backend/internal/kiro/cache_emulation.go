package kiro

// Cache emulation ("假缓存"): the Kiro upstream never reports prompt-cache
// usage, so downstream clients always see cache_read_input_tokens = 0. When a
// positive emulation ratio is configured on the account, we split the final
// input token count into a "real" input portion and an emulated
// cache_read_input_tokens portion in every client-visible usage payload
// (message_start / message_delta / non-stream response).
//
// This is presentation-layer only: upstream credits consumption and the
// credits-based billing override are unaffected. It exists so token-billing
// consumers (e.g. Claude Code cost display) see cache behavior comparable to
// the real Anthropic API.

// splitCacheTokens splits input tokens into (realInput, cacheRead) using
// ratio (clamped to [0,1]). ratio<=0 disables emulation. When input > 0 the
// real input portion is kept at >= 1 so usage never reports zero input.
func splitCacheTokens(input int, ratio float64) (realInput, cacheRead int) {
	if input <= 0 || ratio <= 0 {
		return input, 0
	}
	if ratio > 1 {
		ratio = 1
	}
	cacheRead = int(float64(input) * ratio)
	realInput = input - cacheRead
	if realInput < 1 {
		realInput = 1
		cacheRead = input - 1
	}
	return realInput, cacheRead
}
