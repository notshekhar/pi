package providerutil

import (
	"bufio"
	"bytes"
	"io"
	"strings"
)

// maxSSELineBytes caps a single event line. Model responses embed base64
// images and long tool arguments, so the default bufio limit of 64 KiB is not
// enough; 32 MiB is generous without being unbounded.
const maxSSELineBytes = 32 << 20

// SSEEvent is one server-sent event.
type SSEEvent struct {
	// Event is the "event:" field. Anthropic sets it; OpenAI does not.
	Event string
	// Data is the "data:" field, with multiple data lines joined by newlines
	// as the SSE specification requires.
	Data string
	// ID is the "id:" field, if present.
	ID string
}

// SSEDone is the sentinel data value OpenAI-style APIs send to close a stream.
const SSEDone = "[DONE]"

// ScanSSE reads an SSE body and calls fn for each event, in order, on the
// calling goroutine. It closes r before returning.
//
// Events whose data is exactly [DONE] end the scan and are not passed to fn.
// Comment lines, which begin with a colon and are used as keep-alives, are
// skipped. Returning a non-nil error from fn stops the scan and returns that
// error.
func ScanSSE(r io.ReadCloser, fn func(SSEEvent) error) error {
	defer r.Close()

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), maxSSELineBytes)

	var (
		event    string
		id       string
		dataBuf  []string
		hasEvent bool
	)

	// flush dispatches the accumulated event and resets the accumulator.
	// It reports io.EOF to mean the stream terminated normally.
	flush := func() error {
		if !hasEvent {
			return nil
		}
		ev := SSEEvent{Event: event, Data: strings.Join(dataBuf, "\n"), ID: id}
		event, id, dataBuf, hasEvent = "", "", nil, false

		if ev.Data == SSEDone {
			return io.EOF
		}
		return fn(ev)
	}

	for scanner.Scan() {
		line := scanner.Text()

		// A blank line dispatches the accumulated event.
		if line == "" {
			if err := flush(); err != nil {
				if err == io.EOF {
					return nil
				}
				return err
			}
			continue
		}

		// Comments are keep-alives.
		if strings.HasPrefix(line, ":") {
			continue
		}

		field, value, found := strings.Cut(line, ":")
		if !found {
			// A bare field name has an empty value.
			field, value = line, ""
		}
		// Exactly one leading space after the colon is part of the framing.
		value = strings.TrimPrefix(value, " ")

		switch field {
		case "event":
			event, hasEvent = value, true
		case "data":
			dataBuf, hasEvent = append(dataBuf, value), true
		case "id":
			id, hasEvent = value, true
		case "retry":
			// Reconnection timing is not used: a dropped generation cannot be
			// resumed by replaying the stream.
		}
	}

	if err := scanner.Err(); err != nil {
		return err
	}

	// A well-formed stream ends with a blank line, but tolerate one that does
	// not rather than dropping its last event.
	if err := flush(); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// SSEEventsFromString parses a complete SSE payload, for tests and replay.
func SSEEventsFromString(s string) ([]SSEEvent, error) {
	var out []SSEEvent
	err := ScanSSE(io.NopCloser(bytes.NewReader([]byte(s))), func(e SSEEvent) error {
		out = append(out, e)
		return nil
	})
	return out, err
}
