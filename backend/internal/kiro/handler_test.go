package kiro

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// prepUnmarshalBody decodes a PreparedRequest.RequestBody into a KiroRequest.
func prepUnmarshalBody(t *testing.T, body string) KiroRequest {
	t.Helper()
	var kr KiroRequest
	if err := json.Unmarshal([]byte(body), &kr); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	return kr
}

func TestPrepareRequestBasicLegacy(t *testing.T) {
	// sonnet-4.5 has no fallback supported efforts => legacy, no native fields.
	raw := `{"model":"claude-sonnet-4-5","max_tokens":100,"stream":true,
		"messages":[{"role":"user","content":"hello"}]}`
	pr, err := PrepareRequest([]byte(raw), PrepareOptions{})
	if err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if pr.UpstreamModel != "claude-sonnet-4.5" {
		t.Errorf("UpstreamModel = %q; want claude-sonnet-4.5", pr.UpstreamModel)
	}
	if pr.ResponseModel != "claude-sonnet-4-5" {
		t.Errorf("ResponseModel = %q; want claude-sonnet-4-5", pr.ResponseModel)
	}
	if !pr.Stream {
		t.Error("Stream should be true")
	}
	if pr.InputTokens < 1 {
		t.Errorf("InputTokens = %d; want >= 1", pr.InputTokens)
	}
	if pr.EffortNative {
		t.Error("sonnet-4.5 should not use native effort")
	}
	kr := prepUnmarshalBody(t, pr.RequestBody)
	if kr.AdditionalModelRequestFields != nil {
		t.Error("legacy path should have no additionalModelRequestFields")
	}
	if kr.ConversationState.CurrentMessage.UserInputMessage.ModelID != "claude-sonnet-4.5" {
		t.Errorf("modelId = %q; want claude-sonnet-4.5",
			kr.ConversationState.CurrentMessage.UserInputMessage.ModelID)
	}
}

func TestPrepareRequestNativeEffort(t *testing.T) {
	// opus-4.6 supports efforts; explicit thinking => native effort.
	raw := `{"model":"claude-opus-4-6","max_tokens":100,
		"thinking":{"type":"enabled","budget_tokens":2000},
		"messages":[{"role":"user","content":"hi"}]}`
	pr, err := PrepareRequest([]byte(raw), PrepareOptions{})
	if err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if !pr.EffortNative {
		t.Fatal("opus-4.6 with thinking should use native effort")
	}
	if !pr.ThinkingEnabled {
		t.Error("native effort must set ThinkingEnabled")
	}
	kr := prepUnmarshalBody(t, pr.RequestBody)
	if kr.AdditionalModelRequestFields == nil {
		t.Fatal("native effort must attach additionalModelRequestFields")
	}
	if kr.AdditionalModelRequestFields.OutputConfig.Effort == "" {
		t.Error("effort tier should be set")
	}
}

func TestPrepareRequestThinkingSuffixOverride(t *testing.T) {
	// "-thinking" opus 4.6 => adaptive thinking + high effort override.
	raw := `{"model":"claude-opus-4-6-thinking","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}]}`
	pr, err := PrepareRequest([]byte(raw), PrepareOptions{})
	if err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if !pr.EffortNative || pr.EffortLevel != EffortHigh {
		t.Errorf("opus-4.6-thinking => native/high; got native=%v level=%v",
			pr.EffortNative, pr.EffortLevel)
	}
	if pr.ResponseModel != "claude-opus-4-6-thinking" {
		t.Errorf("ResponseModel = %q; want original", pr.ResponseModel)
	}
	if pr.UpstreamModel != "claude-opus-4.6" {
		t.Errorf("UpstreamModel = %q; want claude-opus-4.6", pr.UpstreamModel)
	}
}

func TestPrepareRequestUnsupportedModel(t *testing.T) {
	raw := `{"model":"gpt-4-turbo","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}]}`
	if _, err := PrepareRequest([]byte(raw), PrepareOptions{}); err == nil {
		t.Fatal("unsupported model should error")
	}
}

func TestPrepareRequestEmptyMessages(t *testing.T) {
	raw := `{"model":"claude-sonnet-4-5","max_tokens":100,"messages":[]}`
	if _, err := PrepareRequest([]byte(raw), PrepareOptions{}); err == nil {
		t.Fatal("empty messages should error")
	}
}

func TestPrepareRequestMappedModelOverride(t *testing.T) {
	// Client asked for an alias; account mapping resolved it to opus-4.6.
	raw := `{"model":"my-alias","max_tokens":100,
		"messages":[{"role":"user","content":"hi"}]}`
	pr, err := PrepareRequest([]byte(raw), PrepareOptions{
		MappedModel:   "claude-opus-4-6",
		ResponseModel: "my-alias",
	})
	if err != nil {
		t.Fatalf("PrepareRequest: %v", err)
	}
	if pr.UpstreamModel != "claude-opus-4.6" {
		t.Errorf("UpstreamModel = %q; want claude-opus-4.6", pr.UpstreamModel)
	}
	if pr.ResponseModel != "my-alias" {
		t.Errorf("ResponseModel = %q; want my-alias", pr.ResponseModel)
	}
}

func collectEmit(events *[]string) EmitFunc {
	return func(ev SseEvent) error {
		*events = append(*events, ev.Event)
		return nil
	}
}

func TestDriveStreamEventOrdering(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":" world"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.5}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 10, false, nil)
	var events []string
	outcome, err := DriveStream(ctx, &raw, collectEmit(&events))
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	// Must start with message_start and end with message_stop.
	if events[0] != "message_start" {
		t.Errorf("first event = %q; want message_start", events[0])
	}
	if events[len(events)-1] != "message_stop" {
		t.Errorf("last event = %q; want message_stop", events[len(events)-1])
	}
	// message_delta must appear exactly once, before message_stop.
	deltas := 0
	for _, e := range events {
		if e == "message_delta" {
			deltas++
		}
	}
	if deltas != 1 {
		t.Errorf("message_delta count = %d; want 1", deltas)
	}
	if outcome.Credits < 0.49 || outcome.Credits > 0.51 {
		t.Errorf("credits = %v; want ~0.5", outcome.Credits)
	}
	if outcome.ClientDisconnected {
		t.Error("should not be marked disconnected")
	}
}

func TestDriveStreamClientDisconnectDrainsCredits(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hi"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.75}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	// Fail emit immediately to simulate a client disconnect on the first event.
	emit := func(SseEvent) error { return errors.New("client gone") }
	outcome, err := DriveStream(ctx, &raw, emit)
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if !outcome.ClientDisconnected {
		t.Error("should be marked disconnected")
	}
	// Even after disconnect, upstream is drained so credits are captured.
	if outcome.Credits < 0.74 || outcome.Credits > 0.76 {
		t.Errorf("credits = %v; want ~0.75 (drained after disconnect)", outcome.Credits)
	}
}

func TestDriveStreamFatalErrorShortCircuits(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"start"}`)))
	_, _ = raw.Write(encodeFrame(t, []eventstream.Header{
		{Name: hdrMessageType, Value: eventstream.StringValue("error")},
		{Name: hdrErrorCode, Value: eventstream.StringValue("ThrottlingException")},
		{Name: hdrErrorMessage, Value: eventstream.StringValue("slow down")},
	}, nil))
	// A trailing metering frame that must NOT be reached (fatal short-circuits).
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":9.0}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	var events []string
	outcome, err := DriveStream(ctx, &raw, collectEmit(&events))
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if !outcome.HasFatal {
		t.Error("should report fatal error")
	}
	// No message_stop after a fatal error (kiro-rs short-circuits).
	for _, e := range events {
		if e == "message_stop" {
			t.Error("fatal error must not emit message_stop")
		}
	}
	// The post-fatal metering frame must not have been consumed.
	if outcome.Credits >= 9.0 {
		t.Errorf("credits = %v; post-fatal frame should not be drained", outcome.Credits)
	}
	if !strings.Contains(outcome.FatalError, "ThrottlingException") {
		t.Errorf("FatalError = %q; want it to mention ThrottlingException", outcome.FatalError)
	}
}
