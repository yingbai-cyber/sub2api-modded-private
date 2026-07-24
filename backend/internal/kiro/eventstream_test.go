package kiro

import (
	"bytes"
	"io"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// encodeFrame builds a single event-stream frame with the given headers/payload
// using the AWS SDK encoder, so the decoder path is exercised end-to-end.
func encodeFrame(t *testing.T, headers []eventstream.Header, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := eventstream.NewEncoder()
	msg := eventstream.Message{Headers: headers, Payload: payload}
	if err := enc.Encode(&buf, msg); err != nil {
		t.Fatalf("encode frame: %v", err)
	}
	return buf.Bytes()
}

func eventHeaders(eventType string) []eventstream.Header {
	return []eventstream.Header{
		{Name: hdrMessageType, Value: eventstream.StringValue("event")},
		{Name: hdrEventType, Value: eventstream.StringValue(eventType)},
	}
}

func TestDecodeAssistantAndMetering(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":"Hello"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("assistantResponseEvent"), []byte(`{"content":" world"}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("meteringEvent"), []byte(`{"unit":"credit","usage":0.247}`)))

	dec := NewEventDecoder(&raw)
	var text string
	var credits float64
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch ev.Kind {
		case EventAssistantResponse:
			text += ev.Assistant.Content
		case EventMetering:
			credits = ev.Metering.Usage
		}
	}
	if text != "Hello world" {
		t.Errorf("assembled text = %q; want %q", text, "Hello world")
	}
	if credits < 0.24 || credits > 0.25 {
		t.Errorf("credits = %v; want ~0.247", credits)
	}
}

func TestDecodeToolUseAndReasoning(t *testing.T) {
	var raw bytes.Buffer
	_, _ = raw.Write(encodeFrame(t, eventHeaders("reasoningContentEvent"), []byte(`{"text":"thinking..."}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("toolUseEvent"),
		[]byte(`{"name":"read_file","toolUseId":"t1","input":"{\"path\":","stop":false}`)))
	_, _ = raw.Write(encodeFrame(t, eventHeaders("toolUseEvent"),
		[]byte(`{"name":"read_file","toolUseId":"t1","input":"\"/x\"}","stop":true}`)))

	dec := NewEventDecoder(&raw)
	var reasoning, toolInput string
	var sawStop bool
	for {
		ev, err := dec.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		switch ev.Kind {
		case EventReasoningContent:
			reasoning += ev.Reasoning.Text
		case EventToolUse:
			toolInput += ev.ToolUse.Input
			if ev.ToolUse.Stop {
				sawStop = true
			}
		}
	}
	if reasoning != "thinking..." {
		t.Errorf("reasoning = %q", reasoning)
	}
	if toolInput != `{"path":"/x"}` {
		t.Errorf("assembled tool input = %q; want %q", toolInput, `{"path":"/x"}`)
	}
	if !sawStop {
		t.Error("expected final tool-use chunk with stop=true")
	}
}

func TestDecodeErrorFrame(t *testing.T) {
	headers := []eventstream.Header{
		{Name: hdrMessageType, Value: eventstream.StringValue("error")},
		{Name: hdrErrorCode, Value: eventstream.StringValue("ThrottlingException")},
	}
	raw := encodeFrame(t, headers, []byte("rate exceeded"))
	dec := NewEventDecoder(bytes.NewReader(raw))
	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Kind != EventError {
		t.Fatalf("kind = %v; want EventError", ev.Kind)
	}
	if ev.ErrorCode != "ThrottlingException" {
		t.Errorf("error code = %q", ev.ErrorCode)
	}
	if ev.ErrorMessage != "rate exceeded" {
		t.Errorf("error message = %q", ev.ErrorMessage)
	}
}

func TestDecodeUnknownEventSkipped(t *testing.T) {
	raw := encodeFrame(t, eventHeaders("someFutureEvent"), []byte(`{"x":1}`))
	dec := NewEventDecoder(bytes.NewReader(raw))
	ev, err := dec.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if ev.Kind != EventUnknown {
		t.Errorf("kind = %v; want EventUnknown", ev.Kind)
	}
}
