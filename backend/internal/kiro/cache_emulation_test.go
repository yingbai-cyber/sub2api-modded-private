package kiro

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestSplitCacheTokens(t *testing.T) {
	cases := []struct {
		name      string
		input     int
		ratio     float64
		wantReal  int
		wantCache int
	}{
		{"disabled zero ratio", 1000, 0, 1000, 0},
		{"negative ratio", 1000, -0.5, 1000, 0},
		{"zero input", 0, 0.8, 0, 0},
		{"negative input", -5, 0.8, -5, 0},
		{"half", 1000, 0.5, 500, 500},
		{"eighty percent", 10000, 0.8, 2000, 8000},
		{"ratio one keeps min input 1", 1000, 1.0, 1, 999},
		{"ratio above one clamps", 1000, 5.0, 1, 999},
		{"tiny input keeps min 1", 1, 0.9, 1, 0},
		{"two tokens high ratio", 2, 0.9, 1, 1},
		{"rounding truncates", 7, 0.5, 4, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			real, cache := splitCacheTokens(c.input, c.ratio)
			if real != c.wantReal || cache != c.wantCache {
				t.Errorf("splitCacheTokens(%d, %v) = (%d, %d); want (%d, %d)",
					c.input, c.ratio, real, cache, c.wantReal, c.wantCache)
			}
			if c.input > 0 && real+cache != c.input {
				t.Errorf("split must conserve tokens: %d + %d != %d", real, cache, c.input)
			}
		})
	}
}

// driveCollectData runs DriveStream collecting full event payloads.
func driveCollectData(t *testing.T, ctx *StreamContext, raw *bytes.Buffer) []SseEvent {
	t.Helper()
	var events []SseEvent
	_, err := DriveStream(ctx, raw, func(ev SseEvent) error {
		events = append(events, ev)
		return nil
	})
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	return events
}

func usageFrom(t *testing.T, ev SseEvent, path ...string) map[string]any {
	t.Helper()
	cur := ev.Data
	for _, p := range path {
		next, ok := cur[p].(map[string]any)
		if !ok {
			t.Fatalf("missing %q in %v", p, cur)
		}
		cur = next
	}
	return cur
}

func TestStreamCacheEmulationSplitsUsage(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.5}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 1000, false, nil)
	ctx.CacheEmulationRatio = 0.8
	events := driveCollectData(t, ctx, &raw)

	// message_start usage must carry the split.
	first := events[0]
	if first.Event != "message_start" {
		t.Fatalf("first event = %q", first.Event)
	}
	su := usageFrom(t, first, "message", "usage")
	if su["input_tokens"] != 200 || su["cache_read_input_tokens"] != 800 {
		t.Errorf("message_start usage = %v; want input 200 / cache_read 800", su)
	}

	// message_delta usage must carry the split too.
	var delta *SseEvent
	for i := range events {
		if events[i].Event == "message_delta" {
			delta = &events[i]
		}
	}
	if delta == nil {
		t.Fatal("no message_delta")
	}
	du := usageFrom(t, *delta, "usage")
	if du["input_tokens"] != 200 || du["cache_read_input_tokens"] != 800 {
		t.Errorf("message_delta usage = %v; want input 200 / cache_read 800", du)
	}
}

func TestStreamCacheEmulationOutcome(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hi"}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 500, false, nil)
	ctx.CacheEmulationRatio = 0.5
	outcome, err := DriveStream(ctx, &raw, func(SseEvent) error { return nil })
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if outcome.InputTokens != 250 || outcome.CacheReadTokens != 250 {
		t.Errorf("outcome = input %d / cache %d; want 250 / 250",
			outcome.InputTokens, outcome.CacheReadTokens)
	}
}

func TestStreamCacheEmulationDisabledUnchanged(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.25}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 1000, false, nil)
	events := driveCollectData(t, ctx, &raw)

	su := usageFrom(t, events[0], "message", "usage")
	if su["input_tokens"] != 1000 {
		t.Errorf("message_start input_tokens = %v; want 1000", su["input_tokens"])
	}
	if _, present := su["cache_read_input_tokens"]; present {
		t.Error("cache_read_input_tokens must be absent when emulation is off")
	}

	var delta *SseEvent
	for i := range events {
		if events[i].Event == "message_delta" {
			delta = &events[i]
		}
	}
	if delta == nil {
		t.Fatal("no message_delta")
	}
	du := usageFrom(t, *delta, "usage")
	if du["input_tokens"] != 1000 {
		t.Errorf("message_delta input_tokens = %v; want 1000", du["input_tokens"])
	}
	if _, present := du["cache_read_input_tokens"]; present {
		t.Error("cache_read_input_tokens must be absent when emulation is off")
	}
}

func TestStreamCacheEmulationWithContextUsageEvent(t *testing.T) {
	// contextUsageEvent overrides the estimate; the split must apply to the
	// upstream-reported final input, not the initial estimate.
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hi"}`)))
	// 1% of the 200k window = 2000 tokens.
	_, _ = raw.Write(encodeFrame(t, eventHeaders("contextUsageEvent"), []byte(`{"contextUsagePercentage":1.0}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 10, false, nil)
	ctx.CacheEmulationRatio = 0.5
	window := ContextWindowSize("claude-sonnet-4.5")
	wantTotal := int(1.0 * float64(window) / 100.0)

	outcome, err := DriveStream(ctx, &raw, func(SseEvent) error { return nil })
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if outcome.InputTokens+outcome.CacheReadTokens != wantTotal {
		t.Errorf("input %d + cache %d != context total %d",
			outcome.InputTokens, outcome.CacheReadTokens, wantTotal)
	}
	if outcome.CacheReadTokens != wantTotal/2 {
		t.Errorf("cache = %d; want %d", outcome.CacheReadTokens, wantTotal/2)
	}
}

func TestNonStreamCacheEmulation(t *testing.T) {
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"Hello"}`},
		{"meteringEvent", `{"usage":0.5}`},
	})
	pr := &PreparedRequest{
		ResponseModel:       "claude-sonnet-4",
		InputTokens:         1000,
		CacheEmulationRatio: 0.8,
	}
	res, err := BuildNonStreamResponseFor(bytes.NewReader(raw), pr)
	if err != nil {
		t.Fatalf("BuildNonStreamResponseFor: %v", err)
	}
	if res.InputTokens != 200 || res.CacheReadTokens != 800 {
		t.Errorf("result = input %d / cache %d; want 200 / 800", res.InputTokens, res.CacheReadTokens)
	}
	u, _ := res.Response["usage"].(map[string]any)
	if u["input_tokens"] != 200 || u["cache_read_input_tokens"] != 800 {
		t.Errorf("usage = %v; want input 200 / cache_read 800", u)
	}
	// Marshal round-trip: cache_read_input_tokens must appear in the JSON body.
	b, _ := json.Marshal(res.Response)
	var decoded map[string]any
	_ = json.Unmarshal(b, &decoded)
	du, _ := decoded["usage"].(map[string]any)
	if du["cache_read_input_tokens"].(float64) != 800 {
		t.Errorf("marshaled usage = %v", du)
	}
}

func TestNonStreamCacheEmulationDisabledUnchanged(t *testing.T) {
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"Hello"}`},
	})
	res, err := BuildNonStreamResponse(bytes.NewReader(raw), "claude-sonnet-4", false, 1000, nil)
	if err != nil {
		t.Fatalf("BuildNonStreamResponse: %v", err)
	}
	if res.InputTokens != 1000 || res.CacheReadTokens != 0 {
		t.Errorf("result = input %d / cache %d; want 1000 / 0", res.InputTokens, res.CacheReadTokens)
	}
	u, _ := res.Response["usage"].(map[string]any)
	if _, present := u["cache_read_input_tokens"]; present {
		t.Error("cache_read_input_tokens must be absent when emulation is off")
	}
}

func TestPrepareRequestCarriesCacheEmulationRatio(t *testing.T) {
	raw := `{"model":"claude-sonnet-4-5","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}]}`
	pr, err := PrepareRequest([]byte(raw), PrepareOptions{CacheEmulationRatio: 0.7})
	if err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if pr.CacheEmulationRatio != 0.7 {
		t.Errorf("CacheEmulationRatio = %v; want 0.7", pr.CacheEmulationRatio)
	}
	sc := pr.NewStreamContext()
	if sc.CacheEmulationRatio != 0.7 {
		t.Errorf("StreamContext ratio = %v; want 0.7", sc.CacheEmulationRatio)
	}
}
