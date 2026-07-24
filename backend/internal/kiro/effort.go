package kiro

import "strings"

// This file implements Kiro's native effort decision logic, mirroring kiro-rs
// effort.rs. It decides whether a request should send a native effort tier
// (low/medium/high/xhigh/max), fall back to legacy XML thinking injection, or
// suppress both.

// EffortLevel is a native effort tier.
type EffortLevel int

const (
	EffortLow EffortLevel = iota
	EffortMedium
	EffortHigh
	EffortXhigh
	EffortMax
)

// ParseEffortLevel parses a wire string into an EffortLevel.
func ParseEffortLevel(s string) (EffortLevel, bool) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "low":
		return EffortLow, true
	case "medium":
		return EffortMedium, true
	case "high":
		return EffortHigh, true
	case "xhigh":
		return EffortXhigh, true
	case "max":
		return EffortMax, true
	default:
		return 0, false
	}
}

// String returns the wire representation.
func (e EffortLevel) String() string {
	switch e {
	case EffortLow:
		return "low"
	case EffortMedium:
		return "medium"
	case EffortHigh:
		return "high"
	case EffortXhigh:
		return "xhigh"
	case EffortMax:
		return "max"
	default:
		return "high"
	}
}

// clampMax limits e to at most max.
func (e EffortLevel) clampMax(max EffortLevel) EffortLevel {
	if e > max {
		return max
	}
	return e
}

// EffortMode is the effort policy mode.
type EffortMode int

const (
	ModeInherit EffortMode = iota
	ModeUser
	ModeForce
	ModeOff
)

// GlobalEffortConfig is the global effort default configuration.
type GlobalEffortConfig struct {
	Enabled       bool
	Mode          EffortMode
	DefaultEffort EffortLevel
	MaxEffort     EffortLevel
	ForceEffort   EffortLevel
}

// DefaultGlobalEffortConfig mirrors kiro-rs GlobalEffortConfig::default.
func DefaultGlobalEffortConfig() GlobalEffortConfig {
	return GlobalEffortConfig{
		Enabled:       true,
		Mode:          ModeUser,
		DefaultEffort: EffortMedium,
		MaxEffort:     EffortMax,
		ForceEffort:   EffortHigh,
	}
}

// EffortContext is the decision input.
type EffortContext struct {
	SupportedEfforts  []EffortLevel
	Global            GlobalEffortConfig
	HasThinkingIntent bool
	ExplicitEffort    string // empty => none
	HasExplicitEffort bool
}

// EffortResult is the decision outcome.
type EffortResult int

const (
	// DecideLegacy: no native effort, use legacy XML thinking injection.
	DecideLegacy EffortResult = iota
	// DecideNative: send native effort at Level.
	DecideNative
	// DecideSuppressAll: no native effort AND no legacy XML (safe reject).
	DecideSuppressAll
)

// EffortDecision is the resolved decision.
type EffortDecision struct {
	Result EffortResult
	Level  EffortLevel
}

// SuppressesLegacyXML reports whether legacy XML thinking injection is suppressed.
func (d EffortDecision) SuppressesLegacyXML() bool {
	return d.Result == DecideNative || d.Result == DecideSuppressAll
}

// ResolveEffort resolves the effort decision (mirrors kiro-rs resolve_effort).
func ResolveEffort(ctx *EffortContext) EffortDecision {
	if len(ctx.SupportedEfforts) == 0 || !ctx.Global.Enabled {
		return EffortDecision{Result: DecideLegacy}
	}

	mode := ctx.Global.Mode
	if mode == ModeInherit {
		mode = ModeUser
	}
	if mode == ModeOff {
		return EffortDecision{Result: DecideLegacy}
	}

	maxEffort := ctx.Global.MaxEffort
	defaultEffort := ctx.Global.DefaultEffort
	forceEffort := ctx.Global.ForceEffort

	switch mode {
	case ModeForce:
		if ctx.HasThinkingIntent || ctx.HasExplicitEffort {
			lvl := clampToSupported(forceEffort.clampMax(maxEffort), ctx.SupportedEfforts)
			return EffortDecision{Result: DecideNative, Level: lvl}
		}
		return EffortDecision{Result: DecideLegacy}
	default: // ModeUser
		return resolveUserMode(ctx, maxEffort, defaultEffort)
	}
}

func resolveUserMode(ctx *EffortContext, maxEffort, defaultEffort EffortLevel) EffortDecision {
	if ctx.HasExplicitEffort {
		lvl, ok := ParseEffortLevel(ctx.ExplicitEffort)
		if !ok {
			// Invalid explicit effort => safe reject (no native, no legacy).
			return EffortDecision{Result: DecideSuppressAll}
		}
		final := clampToSupported(lvl.clampMax(maxEffort), ctx.SupportedEfforts)
		return EffortDecision{Result: DecideNative, Level: final}
	}
	if ctx.HasThinkingIntent {
		final := clampToSupported(defaultEffort.clampMax(maxEffort), ctx.SupportedEfforts)
		return EffortDecision{Result: DecideNative, Level: final}
	}
	return EffortDecision{Result: DecideLegacy}
}

// clampToSupported maps level into the model's supported set (mirrors kiro-rs).
func clampToSupported(level EffortLevel, supported []EffortLevel) EffortLevel {
	if containsEffort(supported, level) {
		return level
	}
	// xhigh but unsupported while max is supported => map up to max.
	if containsEffort(supported, EffortMax) {
		return EffortMax
	}
	best := supported[0]
	for _, s := range supported {
		if s <= level && s >= best {
			best = s
		}
	}
	return best
}

func containsEffort(set []EffortLevel, e EffortLevel) bool {
	for _, s := range set {
		if s == e {
			return true
		}
	}
	return false
}

// FallbackSupportedEfforts returns the model's supported effort tiers based on
// the built-in table (mirrors kiro-rs fallback_supported_efforts).
func FallbackSupportedEfforts(modelID string) []EffortLevel {
	base := strings.TrimSuffix(strings.ToLower(modelID), "-thinking")
	switch {
	case strings.Contains(base, "opus"):
		switch {
		case strings.Contains(base, "4.8"), strings.Contains(base, "4-8"),
			strings.Contains(base, "4.7"), strings.Contains(base, "4-7"):
			return []EffortLevel{EffortLow, EffortMedium, EffortHigh, EffortXhigh, EffortMax}
		case strings.Contains(base, "4.6"), strings.Contains(base, "4-6"):
			return []EffortLevel{EffortLow, EffortMedium, EffortHigh, EffortMax}
		default:
			return nil
		}
	case strings.Contains(base, "sonnet"):
		if strings.Contains(base, "4.6") || strings.Contains(base, "4-6") {
			return []EffortLevel{EffortLow, EffortMedium, EffortHigh, EffortMax}
		}
		return nil
	default:
		return nil
	}
}

// HasThinkingIntent reports whether the request carries thinking intent
// (mirrors kiro-rs has_thinking_intent).
func HasThinkingIntent(req *MessagesRequest) bool {
	if strings.Contains(strings.ToLower(req.Model), "-thinking") {
		return true
	}
	if req.Thinking.IsEnabled() {
		return true
	}
	return req.OutputConfig != nil
}
