package bedrock

import (
	"bytes"
	"io"
	"testing"
)

func TestEventStreamRoundTrip(t *testing.T) {
	payload := []byte(`{"delta":{"text":"hi"}}`)
	frame := encodeEventMessage("contentBlockDelta", payload)

	r := newEventStreamReader(bytes.NewReader(frame))
	msg, err := r.next()
	if err != nil {
		t.Fatal(err)
	}
	if msg.eventType() != "contentBlockDelta" {
		t.Errorf("event = %q", msg.eventType())
	}
	if msg.messageType() != "event" {
		t.Errorf("message = %q", msg.messageType())
	}
	if string(msg.Payload) != string(payload) {
		t.Errorf("payload = %s", msg.Payload)
	}

	if _, err := r.next(); err != io.EOF {
		t.Errorf("second read = %v, want EOF", err)
	}
}

func TestEventStreamChecksumMismatch(t *testing.T) {
	frame := encodeEventMessage("contentBlockDelta", []byte(`{}`))
	frame[len(frame)-1] ^= 0xff

	_, err := newEventStreamReader(bytes.NewReader(frame)).next()
	if err == nil || !bytes.Contains([]byte(err.Error()), []byte("checksum")) {
		t.Fatalf("err = %v, want checksum mismatch", err)
	}
}

func TestEventStreamExceptionHeaders(t *testing.T) {
	frame := encodeExceptionMessage("validationException", "bad request")
	msg, err := newEventStreamReader(bytes.NewReader(frame)).next()
	if err != nil {
		t.Fatal(err)
	}
	if msg.messageType() != "exception" {
		t.Errorf("message = %q", msg.messageType())
	}
	if msg.exceptionType() != "validationException" {
		t.Errorf("exception = %q", msg.exceptionType())
	}
}

func TestEventStreamTruncatedPrelude(t *testing.T) {
	_, err := newEventStreamReader(bytes.NewReader([]byte{0, 0, 0})).next()
	if err == nil {
		t.Fatal("expected an error for a truncated frame")
	}
}
