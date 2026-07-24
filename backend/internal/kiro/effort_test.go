package kiro

import "testing"

func supportedFull() []EffortLevel {
	return []EffortLevel{EffortLow, EffortMedium, EffortHigh, EffortMax}
}

func TestParseEffortLevel(t *testing.T) {
	if l, ok := ParseEffortLevel("HIGH"); !ok || l != EffortHigh {
		t.Errorf("HIGH => %v,%v", l, ok)
	}
	if _, ok := ParseEffortLevel("nope"); ok {
		t.Error("invalid should fail")
	}
}

func TestResolveUserExplicit(t *testing.T) {
	ctx := &EffortContext{
		SupportedEfforts:  supportedFull(),
		Global:            DefaultGlobalEffortConfig(),
		HasThinkingIntent: true,
		ExplicitEffort:    "max",
		HasExplicitEffort: true,
	}
	d := ResolveEffort(ctx)
	if d.Result != DecideNative || d.Level != EffortMax {
		t.Errorf("got %+v; want native/max", d)
	}
}

func TestResolveInvalidExplicitSuppresses(t *testing.T) {
	ctx := &EffortContext{
		SupportedEfforts:  supportedFull(),
		Global:            DefaultGlobalEffortConfig(),
		HasThinkingIntent: true,
		ExplicitEffort:    "bogus",
		HasExplicitEffort: true,
	}
	d := ResolveEffort(ctx)
	if d.Result != DecideSuppressAll {
		t.Errorf("got %+v; want SuppressAll", d)
	}
	if !d.SuppressesLegacyXML() {
		t.Error("SuppressAll should suppress legacy XML")
	}
}

func TestResolveNoIntentUsesLegacy(t *testing.T) {
	ctx := &EffortContext{
		SupportedEfforts:  supportedFull(),
		Global:            DefaultGlobalEffortConfig(),
		HasThinkingIntent: false,
	}
	if d := ResolveEffort(ctx); d.Result != DecideLegacy {
		t.Errorf("got %+v; want Legacy", d)
	}
}

func TestResolveOffModeLegacy(t *testing.T) {
	g := DefaultGlobalEffortConfig()
	g.Mode = ModeOff
	ctx := &EffortContext{
		SupportedEfforts:  supportedFull(),
		Global:            g,
		HasThinkingIntent: true,
		ExplicitEffort:    "high",
		HasExplicitEffort: true,
	}
	if d := ResolveEffort(ctx); d.Result != DecideLegacy {
		t.Errorf("got %+v; want Legacy", d)
	}
}

func TestResolveXhighMapsToMax(t *testing.T) {
	ctx := &EffortContext{
		SupportedEfforts:  supportedFull(), // no xhigh
		Global:            DefaultGlobalEffortConfig(),
		HasThinkingIntent: true,
		ExplicitEffort:    "xhigh",
		HasExplicitEffort: true,
	}
	d := ResolveEffort(ctx)
	if d.Result != DecideNative || d.Level != EffortMax {
		t.Errorf("got %+v; want native/max", d)
	}
}

func TestResolveNoSupportLegacy(t *testing.T) {
	ctx := &EffortContext{
		SupportedEfforts:  nil,
		Global:            DefaultGlobalEffortConfig(),
		HasThinkingIntent: true,
		ExplicitEffort:    "high",
		HasExplicitEffort: true,
	}
	if d := ResolveEffort(ctx); d.Result != DecideLegacy {
		t.Errorf("unsupported model should be legacy; got %+v", d)
	}
}

func TestFallbackSupportedEfforts(t *testing.T) {
	if got := FallbackSupportedEfforts("claude-opus-4-7-thinking"); !containsEffort(got, EffortXhigh) {
		t.Error("opus 4.7 should support xhigh")
	}
	if got := FallbackSupportedEfforts("claude-opus-4-6"); containsEffort(got, EffortXhigh) {
		t.Error("opus 4.6 should not support xhigh")
	}
	if got := FallbackSupportedEfforts("claude-sonnet-4-5"); len(got) != 0 {
		t.Error("sonnet 4.5 should have no effort support")
	}
}

func TestHasThinkingIntent(t *testing.T) {
	if !HasThinkingIntent(&MessagesRequest{Model: "claude-opus-4-7-thinking"}) {
		t.Error("-thinking suffix => intent")
	}
	if !HasThinkingIntent(&MessagesRequest{Thinking: &ThinkingConfig{Type: "enabled"}}) {
		t.Error("thinking enabled => intent")
	}
	if !HasThinkingIntent(&MessagesRequest{OutputConfig: &OutputConfig{Effort: "high"}}) {
		t.Error("output_config present => intent")
	}
	if HasThinkingIntent(&MessagesRequest{Model: "claude-sonnet-4-5"}) {
		t.Error("plain request => no intent")
	}
}
