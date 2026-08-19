package providerutil

import (
	"strings"
	"testing"
)

func TestScanSSEBasicEvents(t *testing.T) {
	events, err := SSEEventsFromString(
		"event: message_start\ndata: {\"a\":1}\n\n" +
			"event: message_stop\ndata: {\"b\":2}\n\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Event != "message_start" || events[0].Data != `{"a":1}` {
		t.Errorf("event 0 = %+v", events[0])
	}
}

func TestScanSSEStopsAtDone(t *testing.T) {
	events, err := SSEEventsFromString(
		"data: {\"a\":1}\n\ndata: [DONE]\n\ndata: {\"never\":true}\n\n",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1: [DONE] terminates the stream", len(events))
	}
}

func TestScanSSESkipsCommentsAndJoinsDataLines(t *testing.T) {
	// A comment line is a keep-alive; multiple data lines join with newlines
	// as the SSE specification requires.
	events, err := SSEEventsFromString(": keep-alive\n\ndata: line one\ndata: line two\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].Data != "line one\nline two" {
		t.Errorf("data = %q", events[0].Data)
	}
}

func TestScanSSEHandlesMissingTrailingBlankLine(t *testing.T) {
	// A stream that is cut off mid-frame should still yield what it sent
	// rather than silently dropping the last event.
	events, err := SSEEventsFromString("data: {\"a\":1}")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Data != `{"a":1}` {
		t.Errorf("events = %+v", events)
	}
}

func TestScanSSEHandlesLargeLines(t *testing.T) {
	// A base64 image or a long tool argument blows past bufio's 64 KiB
	// default, which would otherwise fail the whole stream.
	big := strings.Repeat("x", 1<<20)
	events, err := SSEEventsFromString("data: " + big + "\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(events[0].Data) != len(big) {
		t.Errorf("large line was not read intact")
	}
}

func TestScanSSEPreservesLeadingSpaceRule(t *testing.T) {
	// Exactly one space after the colon is framing; further spaces are data.
	events, err := SSEEventsFromString("data:  two spaces\n\n")
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Data != " two spaces" {
		t.Errorf("data = %q, want one leading space preserved", events[0].Data)
	}
}

func TestExtractErrorMessageShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{"nested", `{"error":{"message":"boom"}}`, "boom"},
		{"string error", `{"error":"boom"}`, "boom"},
		{"top-level message", `{"message":"boom"}`, "boom"},
		{"detail", `{"detail":"boom"}`, "boom"},
		{"unparseable short body", `not json`, "not json"},
		{"empty body falls back to status", ``, "429 Too Many Requests"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractErrorMessage(tc.body, "429 Too Many Requests"); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
