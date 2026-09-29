package kiro

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

func TestPrepareRequestOpus55NativeMaxEffort(t *testing.T) {
	// Production case: opus-5.5 + effort=max must reach the upstream as a native
	// output_config.effort field, not be dropped or turned into legacy XML.
	for _, shape := range []string{
		`"output_config":{"effort":"max"}`,
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"max"}`,
		`"thinking":{"type":"enabled","budget_tokens":16000},"output_config":{"effort":"max"}`,
	} {
		raw := `{"model":"claude-opus-5.5","max_tokens":100,` + shape +
			`,"messages":[{"role":"user","content":"hi"}]}`
		pr, err := PrepareRequest([]byte(raw), PrepareOptions{})
		if err != nil {
			t.Fatalf("PrepareRequest(%s): %v", shape, err)
		}
		if pr.UpstreamModel != "claude-opus-5.5" {
			t.Errorf("%s: UpstreamModel = %q; want claude-opus-5.5", shape, pr.UpstreamModel)
		}
		if !pr.EffortNative || pr.EffortLevel != EffortMax {
			t.Errorf("%s: want native/max; got native=%v level=%v", shape, pr.EffortNative, pr.EffortLevel)
		}
		kr := prepUnmarshalBody(t, pr.RequestBody)
		if kr.AdditionalModelRequestFields == nil || kr.AdditionalModelRequestFields.OutputConfig.Effort != "max" {
			t.Errorf("%s: upstream body must carry output_config.effort=max", shape)
		}
		if strings.Contains(pr.RequestBody, "thinking_mode") {
			t.Errorf("%s: native effort must suppress legacy XML", shape)
		}
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

func TestDriveStreamKeepalivePingsWhileUpstreamSilent(t *testing.T) {
	// The upstream sends one frame, stays silent, then finishes. Pings must go
	// out during the silence, and the stream must still end normally.
	first := encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hi"}`))
	last := encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.25}`))
	upR, upW := io.Pipe()
	defer func() { _ = upR.Close() }()
	go func() {
		_, _ = upW.Write(first)
		time.Sleep(300 * time.Millisecond)
		_, _ = upW.Write(last)
		_ = upW.Close()
	}()

	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	var events []string
	outcome, err := DriveStreamWithOptions(ctx, upR, collectEmit(&events), DriveOptions{KeepaliveInterval: 40 * time.Millisecond})
	if err != nil {
		t.Fatalf("DriveStreamWithOptions: %v", err)
	}
	if outcome.Pings < 1 || countName(events, "ping") != outcome.Pings {
		t.Errorf("pings = %d (emitted %d); want >= 1 and equal", outcome.Pings, countName(events, "ping"))
	}
	if outcome.Interrupted || events[len(events)-1] != "message_stop" {
		t.Errorf("want a normal end; interrupted=%v last=%q", outcome.Interrupted, events[len(events)-1])
	}
	if outcome.Credits < 0.24 || outcome.Credits > 0.26 {
		t.Errorf("credits = %v; want ~0.25", outcome.Credits)
	}
}

func TestDriveStreamIdleTimeoutInterrupts(t *testing.T) {
	first := encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hi"}`))
	upR, upW := io.Pipe()
	defer func() { _ = upR.Close() }()
	defer func() { _ = upW.Close() }()
	go func() { _, _ = upW.Write(first) }() // then the upstream stalls

	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	var events []string
	outcome, err := DriveStreamWithOptions(ctx, upR, collectEmit(&events), DriveOptions{IdleTimeout: 80 * time.Millisecond})
	if err != nil {
		t.Fatalf("DriveStreamWithOptions: %v", err)
	}
	if !outcome.Interrupted || outcome.InterruptReason != InterruptIdleTimeout {
		t.Fatalf("want idle_timeout interrupt; got interrupted=%v reason=%q", outcome.Interrupted, outcome.InterruptReason)
	}
	if countName(events, "error") != 1 || countName(events, "message_stop") != 0 {
		t.Errorf("want one error event and no message_stop; got %v", events)
	}
}

func TestDriveStreamTruncatedFrameIsReadErrorNotNormalEnd(t *testing.T) {
	// A frame cut off mid-payload used to decode as a clean io.EOF, so the
	// reply ended with end_turn + message_stop and looked complete.
	full := encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`))
	cut := encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":" world and more"}`))
	var raw bytes.Buffer
	_, _ = raw.Write(full)
	_, _ = raw.Write(cut[:len(cut)-8])

	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	var events []string
	outcome, err := DriveStream(ctx, &raw, collectEmit(&events))
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if !outcome.Interrupted || outcome.InterruptReason != InterruptReadError {
		t.Fatalf("want read_error interrupt; got interrupted=%v reason=%q", outcome.Interrupted, outcome.InterruptReason)
	}
	if countName(events, "message_stop") != 0 || countName(events, "error") != 1 {
		t.Errorf("want an error event and no message_stop; got %v", events)
	}
}

func TestDriveStreamSkipsMalformedPayloadFrame(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{not json`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":" world"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.5}`)))

	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	var events []string
	outcome, err := DriveStream(ctx, &raw, collectEmit(&events))
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if outcome.Interrupted || outcome.SkippedFrames != 1 {
		t.Errorf("want 1 skipped frame and no interrupt; got skipped=%d interrupted=%v", outcome.SkippedFrames, outcome.Interrupted)
	}
	if events[len(events)-1] != "message_stop" {
		t.Errorf("last event = %q; want message_stop", events[len(events)-1])
	}
	if outcome.Credits < 0.49 || outcome.Credits > 0.51 {
		t.Errorf("credits = %v; want ~0.5 (frames after the bad one still read)", outcome.Credits)
	}
}

func TestDriveStreamTooManyMalformedFramesInterrupts(t *testing.T) {
	var raw bytes.Buffer
	for i := 0; i < maxConsecutiveDecodeErrors; i++ {
		_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{not json`)))
	}
	ctx := NewStreamContext("claude-sonnet-4.5", 5, false, nil)
	var events []string
	outcome, err := DriveStream(ctx, &raw, collectEmit(&events))
	if err != nil {
		t.Fatalf("DriveStream: %v", err)
	}
	if !outcome.Interrupted || outcome.InterruptReason != InterruptDecodeErrors {
		t.Fatalf("want decode_errors interrupt; got interrupted=%v reason=%q", outcome.Interrupted, outcome.InterruptReason)
	}
}
