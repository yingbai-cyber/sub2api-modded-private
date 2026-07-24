package kiro

import (
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
)

// AWS event-stream well-known header keys.
const (
	hdrMessageType  = ":message-type"
	hdrEventType    = ":event-type"
	hdrErrorCode    = ":error-code"
	hdrErrorMessage = ":error-message"
	hdrExceptionTyp = ":exception-type"
)

// EventDecoder decodes an AWS event-stream binary response body into a sequence
// of high-level Kiro Events. It wraps the AWS SDK eventstream.Decoder, which
// handles prelude/message CRC validation and header parsing.
//
// It is a thin, stateful reader-adapter: framing state lives entirely in the
// SDK decoder + the underlying io.Reader, so this type has minimal surface.
type EventDecoder struct {
	dec     *eventstream.Decoder
	r       io.Reader
	payload []byte
}

// NewEventDecoder builds a decoder over the given response body reader.
func NewEventDecoder(r io.Reader) *EventDecoder {
	return &EventDecoder{
		dec:     eventstream.NewDecoder(),
		r:       r,
		payload: make([]byte, 0, 8*1024),
	}
}

// Next decodes and returns the next event. It returns io.EOF when the stream
// is exhausted. Unknown event types are returned as Event{Kind: EventUnknown}
// so callers can choose to skip them.
func (d *EventDecoder) Next() (Event, error) {
	msg, err := d.dec.Decode(d.r, d.payload[:0])
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Event{}, io.EOF
		}
		return Event{}, err
	}

	messageType := msg.Headers.Get(hdrMessageType)
	mt := ""
	if messageType != nil {
		mt = messageType.String()
	}

	switch mt {
	case "", "event":
		return d.decodeEvent(msg)
	case "error":
		return decodeErrorFrame(msg), nil
	case "exception":
		return decodeExceptionFrame(msg), nil
	default:
		// Unknown message type: surface as unknown rather than failing the
		// whole stream (mirrors kiro-rs resilience).
		return Event{Kind: EventUnknown}, nil
	}
}

// decodeEvent handles :message-type=event frames by dispatching on :event-type.
func (d *EventDecoder) decodeEvent(msg eventstream.Message) (Event, error) {
	etHdr := msg.Headers.Get(hdrEventType)
	et := ""
	if etHdr != nil {
		et = etHdr.String()
	}
	kind := eventTypeFromString(et)
	if kind == EventUnknown {
		return Event{Kind: EventUnknown}, nil
	}
	return decodeEventPayload(kind, msg.Payload)
}

// decodeErrorFrame builds an Event from an :message-type=error frame.
func decodeErrorFrame(msg eventstream.Message) Event {
	code := "UnknownError"
	if h := msg.Headers.Get(hdrErrorCode); h != nil {
		code = h.String()
	}
	message := string(msg.Payload)
	if h := msg.Headers.Get(hdrErrorMessage); h != nil && message == "" {
		message = h.String()
	}
	return Event{Kind: EventError, ErrorCode: code, ErrorMessage: message}
}

// decodeExceptionFrame builds an Event from an :message-type=exception frame.
func decodeExceptionFrame(msg eventstream.Message) Event {
	typ := "UnknownException"
	if h := msg.Headers.Get(hdrExceptionTyp); h != nil {
		typ = h.String()
	}
	return Event{Kind: EventException, ExceptionType: typ, ErrorMessage: string(msg.Payload)}
}
