package kiro

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// Kiro credits are an internal cost metric for admins only. They must never
// appear in the client-visible response payload, yet must still reach the
// billing layer through the structured outcome/result fields.

func TestStreamOmitsCreditsFromClientPayload(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.75}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 1000, false, nil)
	var wire strings.Builder
	outcome, err := DriveStream(ctx, &raw, func(ev SseEvent) error {
		wire.WriteString(ev.ToSSEString())
		return nil
	})
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if outcome == nil {
		t.Fatal("DriveStream returned nil outcome")
	}

	// Billing path still receives the consumed credits.
	if outcome.Credits != 0.75 {
		t.Errorf("outcome.Credits = %v; want 0.75", outcome.Credits)
	}

	// The client-visible stream must not leak the metric.
	if strings.Contains(wire.String(), "kiro_credits") {
		t.Error("stream payload must not contain kiro_credits")
	}
}

func TestNonStreamOmitsCreditsFromClientPayload(t *testing.T) {
	raw := buildStream(t, [][2]string{
		{"assistantResponseEvent", `{"content":"Hello"}`},
		{"meteringEvent", `{"usage":0.5}`},
	})
	pr := &PreparedRequest{
		ResponseModel: "claude-sonnet-4",
		InputTokens:   1000,
	}
	res, err := BuildNonStreamResponseFor(bytes.NewReader(raw), pr)
	if err != nil {
		t.Fatalf("BuildNonStreamResponseFor: %v", err)
	}

	// Billing path still receives the consumed credits.
	if res.Credits != 0.5 {
		t.Errorf("res.Credits = %v; want 0.5", res.Credits)
	}

	usage, ok := res.Response["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage missing in %v", res.Response)
	}
	if _, present := usage["kiro_credits"]; present {
		t.Error("usage must not contain kiro_credits")
	}

	// Marshal round-trip: the key must be absent from the serialized body too.
	b, err := json.Marshal(res.Response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if bytes.Contains(b, []byte("kiro_credits")) {
		t.Errorf("marshaled body must not contain kiro_credits: %s", b)
	}
}
