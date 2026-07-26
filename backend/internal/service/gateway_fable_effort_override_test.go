package service

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestForceFableOutputEffort(t *testing.T) {
	base := []byte(`{"model":"claude-fable-5","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)

	t.Run("empty effort disables feature", func(t *testing.T) {
		out, changed := ForceFableOutputEffort(base, "claude-fable-5", "")
		if changed {
			t.Fatal("must not change when effort is empty")
		}
		if string(out) != string(base) {
			t.Fatal("body must be byte-identical when disabled")
		}
	})

	t.Run("non-fable model untouched", func(t *testing.T) {
		out, changed := ForceFableOutputEffort(base, "claude-opus-4-6", "max")
		if changed {
			t.Fatal("must not change non-fable models")
		}
		if string(out) != string(base) {
			t.Fatal("body must be unchanged")
		}
	})

	t.Run("injects effort and adaptive thinking", func(t *testing.T) {
		out, changed := ForceFableOutputEffort(base, "claude-fable-5", "max")
		if !changed {
			t.Fatal("expected rewrite")
		}
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "max" {
			t.Errorf("effort = %q; want max", got)
		}
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
			t.Errorf("thinking.type = %q; want adaptive", got)
		}
	})

	t.Run("overwrites existing effort", func(t *testing.T) {
		body := []byte(`{"model":"claude-fable-5","output_config":{"effort":"low"},"thinking":{"type":"enabled","budget_tokens":1024}}`)
		out, changed := ForceFableOutputEffort(body, "claude-fable-5", "max")
		if !changed {
			t.Fatal("expected rewrite")
		}
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "max" {
			t.Errorf("effort = %q; want max", got)
		}
		// enabled thinking is kept as-is.
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "enabled" {
			t.Errorf("thinking.type = %q; want enabled (preserved)", got)
		}
	})

	t.Run("already at target effort with adaptive thinking is a no-op", func(t *testing.T) {
		body := []byte(`{"model":"claude-fable-5","output_config":{"effort":"max"},"thinking":{"type":"adaptive"}}`)
		_, changed := ForceFableOutputEffort(body, "claude-fable-5", "max")
		if changed {
			t.Fatal("expected no-op when already at target state")
		}
	})

	t.Run("disabled thinking gets upgraded to adaptive", func(t *testing.T) {
		body := []byte(`{"model":"claude-fable-5","thinking":{"type":"disabled"}}`)
		out, changed := ForceFableOutputEffort(body, "claude-fable-5", "high")
		if !changed {
			t.Fatal("expected rewrite")
		}
		if got := gjson.GetBytes(out, "thinking.type").String(); got != "adaptive" {
			t.Errorf("thinking.type = %q; want adaptive", got)
		}
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "high" {
			t.Errorf("effort = %q; want high", got)
		}
	})

	t.Run("fable substring matching covers mapped aliases", func(t *testing.T) {
		out, changed := ForceFableOutputEffort(base, "Claude-Fable-5-Thinking", "max")
		if !changed {
			t.Fatal("expected rewrite for case-insensitive fable alias")
		}
		if got := gjson.GetBytes(out, "output_config.effort").String(); got != "max" {
			t.Errorf("effort = %q; want max", got)
		}
	})
}

func TestReadFableForceEffortFromEnv(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", ""},
		{"  ", ""},
		{"banana", ""},
		{"max", "max"},
		{"MAX", "max"},
		{" xhigh ", "xhigh"},
		{"low", "low"},
	}
	for _, c := range cases {
		t.Setenv(fableForceEffortEnv, c.raw)
		if got := ReadFableForceEffortFromEnv(); got != c.want {
			t.Errorf("env %q => %q; want %q", c.raw, got, c.want)
		}
	}
}
