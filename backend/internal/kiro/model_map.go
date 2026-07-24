package kiro

import "strings"

// MapModel maps an Anthropic model name to a Kiro upstream model id.
// Mirrors kiro-rs anthropic::converter::map_model.
//
//   - sonnet 4.6/4-6            => claude-sonnet-4.6
//   - other sonnet             => claude-sonnet-4.5
//   - opus 4.8/4-8             => claude-opus-4.8
//   - opus 4.7/4-7             => claude-opus-4.7
//   - opus 4.5/4-5             => claude-opus-4.5
//   - other opus               => claude-opus-4.6
//   - any haiku                => claude-haiku-4.5
//   - gpt-5*                   => passthrough (as-is)
//
// Returns ("", false) for unsupported models.
func MapModel(model string) (string, bool) {
	lower := strings.ToLower(model)
	base := strings.TrimSuffix(lower, "-thinking")

	// GPT-5.x family is passed through unchanged.
	if strings.HasPrefix(base, "gpt-5") {
		return lower, true
	}

	switch {
	case strings.Contains(base, "sonnet"):
		if strings.Contains(base, "4-6") || strings.Contains(base, "4.6") {
			return "claude-sonnet-4.6", true
		}
		return "claude-sonnet-4.5", true
	case strings.Contains(base, "opus"):
		switch {
		case strings.Contains(base, "4-8") || strings.Contains(base, "4.8"):
			return "claude-opus-4.8", true
		case strings.Contains(base, "4-7") || strings.Contains(base, "4.7"):
			return "claude-opus-4.7", true
		case strings.Contains(base, "4-5") || strings.Contains(base, "4.5"):
			return "claude-opus-4.5", true
		default:
			return "claude-opus-4.6", true
		}
	case strings.Contains(base, "haiku"):
		return "claude-haiku-4.5", true
	default:
		return "", false
	}
}

// ContextWindowSize returns the context window for a model, reusing MapModel.
// Kiro upgraded Opus/Sonnet 4.6+ to 1M context.
func ContextWindowSize(model string) int {
	mapped, ok := MapModel(model)
	if !ok {
		return 200_000
	}
	switch mapped {
	case "claude-sonnet-4.6", "claude-opus-4.6", "claude-opus-4.7", "claude-opus-4.8":
		return 1_000_000
	default:
		return 200_000
	}
}

// StripThinkingSuffix removes a trailing -thinking suffix from a model name.
func StripThinkingSuffix(model string) string {
	return strings.TrimSuffix(model, "-thinking")
}
