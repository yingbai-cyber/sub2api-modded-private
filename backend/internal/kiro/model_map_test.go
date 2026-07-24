package kiro

import "testing"

func TestMapModel(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"claude-sonnet-4-20250514", "claude-sonnet-4.5", true},
		{"claude-sonnet-4-5-20250929-thinking", "claude-sonnet-4.5", true},
		{"claude-sonnet-4-6", "claude-sonnet-4.6", true},
		{"claude-sonnet-4.6-thinking", "claude-sonnet-4.6", true},
		{"claude-opus-4-20250514", "claude-opus-4.6", true},
		{"claude-opus-4-5-20251101-thinking", "claude-opus-4.5", true},
		{"claude-opus-4-6-thinking", "claude-opus-4.6", true},
		{"claude-opus-4-7", "claude-opus-4.7", true},
		{"claude-opus-4.8", "claude-opus-4.8", true},
		{"claude-haiku-4-5-20251001-thinking", "claude-haiku-4.5", true},
		{"claude-haiku-4-20250514", "claude-haiku-4.5", true},
		{"gpt-5.2", "gpt-5.2", true},
		{"GPT-5-Codex", "gpt-5-codex", true},
		{"unknown-model", "", false},
	}
	for _, c := range cases {
		got, ok := MapModel(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("MapModel(%q) = (%q,%v); want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestContextWindowSize(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"claude-sonnet-4-6", 1_000_000},
		{"claude-opus-4-6", 1_000_000},
		{"claude-opus-4-7", 1_000_000},
		{"claude-opus-4.8", 1_000_000},
		{"claude-sonnet-4-5", 200_000},
		{"claude-opus-4-5", 200_000},
		{"claude-haiku-4-5", 200_000},
		{"unknown-model", 200_000},
	}
	for _, c := range cases {
		if got := ContextWindowSize(c.in); got != c.want {
			t.Errorf("ContextWindowSize(%q) = %d; want %d", c.in, got, c.want)
		}
	}
}
