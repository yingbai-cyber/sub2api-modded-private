package kiro

import (
	"encoding/json"
	"testing"
)

func TestIsNonWesternChar(t *testing.T) {
	western := []rune{'a', 'Z', '0', ' ', '\n', 'é', 'ñ', 'ÿ', 'ƀ'}
	for _, r := range western {
		if isNonWesternChar(r) {
			t.Errorf("rune %q (U+%04X) should be western", r, r)
		}
	}
	nonWestern := []rune{'中', '文', 'あ', '한', '😀', 'ф'}
	for _, r := range nonWestern {
		if !isNonWesternChar(r) {
			t.Errorf("rune %q (U+%04X) should be non-western", r, r)
		}
	}
}

func TestCountTokensMagnitudeTiers(t *testing.T) {
	// Empty text => 0 tokens.
	if got := countTokens(""); got != 0 {
		t.Errorf("empty => %d; want 0", got)
	}
	// Short western text: units = len, tokens = len/4, acc = tokens*1.5.
	// 40 chars => 10 tokens => *1.5 = 15.
	forty := ""
	for i := 0; i < 40; i++ {
		forty += "a"
	}
	if got := countTokens(forty); got != 15 {
		t.Errorf("40 western chars => %d; want 15", got)
	}
	// Non-western weigh 4x: 10 chars => 40 units => 10 tokens => *1.5 = 15.
	cjk := "中中中中中中中中中中"
	if got := countTokens(cjk); got != 15 {
		t.Errorf("10 cjk chars => %d; want 15", got)
	}
}

func TestCountInputTokensSumsAllParts(t *testing.T) {
	raw := `{
		"model": "claude-sonnet-4-6",
		"max_tokens": 100,
		"system": "you are helpful",
		"messages": [
			{"role": "user", "content": "hello world"},
			{"role": "assistant", "content": [{"type":"text","text":"hi there"}]}
		],
		"tools": [
			{"name": "get_weather", "description": "gets weather", "input_schema": {"type":"object"}}
		]
	}`
	var req MessagesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := CountInputTokens(&req)
	// Compute expected as the sum of the individual parts.
	want := countTokens("you are helpful") +
		countTokens("hello world") +
		countTokens("hi there") +
		countTokens("get_weather") +
		countTokens("gets weather") +
		countTokens(string(req.Tools[0].InputSchema))
	if want < 1 {
		want = 1
	}
	if got != want {
		t.Errorf("CountInputTokens => %d; want %d", got, want)
	}
}

func TestCountInputTokensMinimumOne(t *testing.T) {
	var req MessagesRequest
	req.Messages = []AnthropicMsg{{Role: "user", Content: json.RawMessage(`""`)}}
	if got := CountInputTokens(&req); got != 1 {
		t.Errorf("empty content => %d; want 1", got)
	}
}
