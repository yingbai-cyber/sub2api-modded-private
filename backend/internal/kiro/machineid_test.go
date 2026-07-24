package kiro

import (
	"strings"
	"testing"
)

func TestNormalizeMachineID(t *testing.T) {
	hex64 := strings.Repeat("a", 64)
	if got, ok := normalizeMachineID(hex64); !ok || got != hex64 {
		t.Errorf("64-hex should pass through: got %q ok=%v", got, ok)
	}
	// UUID (36 chars w/ dashes) -> 32 hex duplicated to 64.
	uuid := "12345678-1234-1234-1234-123456789abc"
	got, ok := normalizeMachineID(uuid)
	if !ok || len(got) != 64 {
		t.Errorf("uuid should normalize to 64 hex: got len=%d ok=%v", len(got), ok)
	}
	// Uppercase hex must be accepted AND preserve its case (matches kiro-rs,
	// which validates case-insensitively but never lowercases the output).
	upper := strings.Repeat("A", 64)
	if got, ok := normalizeMachineID(upper); !ok || got != upper {
		t.Errorf("uppercase 64-hex should pass through unchanged: got %q ok=%v", got, ok)
	}
	uuidUpper := "ABCDEF12-1234-1234-1234-123456789ABC"
	if got, ok := normalizeMachineID(uuidUpper); !ok || got != "ABCDEF12123412341234123456789ABCABCDEF12123412341234123456789ABC" {
		t.Errorf("uppercase uuid should normalize preserving case: got %q ok=%v", got, ok)
	}
	if _, ok := normalizeMachineID("not-hex-!!!"); ok {
		t.Error("invalid string should not normalize")
	}
	if _, ok := normalizeMachineID(""); ok {
		t.Error("empty should not normalize")
	}
}

func TestGenerateMachineID_APIKey(t *testing.T) {
	c := &Credentials{ID: 1, KiroAPIKey: "ksk_test", AuthMethod: AuthAPIKey}
	got := GenerateMachineID(c, "")
	want := sha256Hex("KiroAPIKey/ksk_test")
	if got != want {
		t.Errorf("api key machine id = %q; want %q", got, want)
	}
}

func TestGenerateMachineID_OAuth(t *testing.T) {
	c := &Credentials{ID: 1, RefreshToken: "rt_test"}
	got := GenerateMachineID(c, "")
	want := sha256Hex("KotlinNativeAPI/rt_test")
	if got != want {
		t.Errorf("oauth machine id = %q; want %q", got, want)
	}
}

func TestGenerateMachineID_ExplicitOverride(t *testing.T) {
	hex64 := strings.Repeat("b", 64)
	c := &Credentials{ID: 1, RefreshToken: "rt", MachineID: hex64}
	if got := GenerateMachineID(c, ""); got != hex64 {
		t.Errorf("explicit machine id should win: got %q", got)
	}
}

func TestGenerateMachineID_DefaultOverride(t *testing.T) {
	hex64 := strings.Repeat("c", 64)
	c := &Credentials{ID: 1, RefreshToken: "rt"}
	if got := GenerateMachineID(c, hex64); got != hex64 {
		t.Errorf("default machine id should be used: got %q", got)
	}
}

func TestGenerateMachineID_FallbackStable(t *testing.T) {
	c := &Credentials{ID: 999}
	first := GenerateMachineID(c, "")
	second := GenerateMachineID(c, "")
	if first != second {
		t.Errorf("fallback machine id should be stable per account: %q != %q", first, second)
	}
	if len(first) != 64 {
		t.Errorf("fallback should be 64 hex, got len=%d", len(first))
	}
}
