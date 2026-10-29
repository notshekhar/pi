package mcp

import (
	"bufio"
	"io"
	"strings"
)

// sseEvent is one Server-Sent Event.
type sseEvent struct {
	ID    string
	Event string
	Data  string
}

// readSSE parses an event stream, calling emit per event until the stream
// ends or emit returns false. Comments and retry fields are ignored.
func readSSE(r io.Reader, emit func(sseEvent) bool) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	var ev sseEvent
	var data []string
	flush := func() bool {
		if len(data) == 0 && ev.Event == "" {
			ev = sseEvent{ID: ev.ID}
			return true
		}
		ev.Data = strings.Join(data, "\n")
		ok := emit(ev)
		ev, data = sseEvent{ID: ev.ID}, nil
		return ok
	}
	for sc.Scan() {
		line := sc.Text()
		if line == "" {
			if !flush() {
				return nil
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "data":
			data = append(data, value)
		case "event":
			ev.Event = value
		case "id":
			ev.ID = value
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	flush()
	return nil
}

// isMessageEvent: an event with no name, or named "message", carries a
// JSON-RPC message.
func isMessageEvent(name string) bool { return name == "" || name == "message" }
