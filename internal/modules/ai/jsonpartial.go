package ai

import (
	"encoding/json"
	"strings"
)

// completeJSON turns the prefix of a JSON document into a parseable document,
// so that an object can be shown while it is still being written.
//
// It finds the furthest point that can legally be closed, then appends the
// closing brackets for whatever is still open. A value the model is halfway
// through is dropped, except for a string value, which is closed where it
// stands so that long strings fill in as they stream.
//
// It reports false when nothing parseable has arrived yet.
func completeJSON(partial string) (string, bool) {
	s := &jsonScanner{input: partial}
	s.scan()

	if !s.hasSafe {
		return "", false
	}
	return s.input[:s.safeEnd] + s.safeClosers, true
}

// jsonScanner walks a partial document tracking the last position at which it
// could be closed, along with the brackets that closing would need.
type jsonScanner struct {
	input string

	// stack holds the closing bracket for each open container.
	stack []byte

	hasSafe     bool
	safeEnd     int
	safeClosers string
}

// mark records that the document could be closed just after end.
func (s *jsonScanner) mark(end int) {
	s.hasSafe = true
	s.safeEnd = end
	s.safeClosers = closers(s.stack)
}

// markString records a safe point in the middle of a string value, closing the
// string itself as well as the enclosing containers.
func (s *jsonScanner) markString(end int) {
	s.hasSafe = true
	s.safeEnd = trimIncompleteEscape(s.input[:end])
	s.safeClosers = `"` + closers(s.stack)
}

// trimIncompleteEscape returns the length of s without a trailing escape
// sequence that has not finished arriving. Closing the string on a lone
// backslash, or halfway through é, would produce invalid JSON.
func trimIncompleteEscape(s string) int {
	backslashes := 0
	for i := len(s) - 1; i >= 0 && s[i] == '\\'; i-- {
		backslashes++
	}
	if backslashes%2 == 1 {
		return len(s) - 1
	}

	// A \u escape needs four hex digits; anything shorter is still in flight.
	for i := len(s) - 1; i >= 0 && i > len(s)-6; i-- {
		if s[i] != 'u' || i == 0 || s[i-1] != '\\' {
			continue
		}
		if isEscaped(s, i-1) && len(s)-(i+1) < 4 {
			return i - 1
		}
	}
	return len(s)
}

// isEscaped reports whether the backslash at i is itself an escape introducer
// rather than an escaped backslash.
func isEscaped(s string, i int) bool {
	backslashes := 0
	for j := i; j >= 0 && s[j] == '\\'; j-- {
		backslashes++
	}
	return backslashes%2 == 1
}

// closers renders the stack as the text that would close it.
func closers(stack []byte) string {
	if len(stack) == 0 {
		return ""
	}
	var b strings.Builder
	for i := len(stack) - 1; i >= 0; i-- {
		b.WriteByte(stack[i])
	}
	return b.String()
}

// scan walks the input once.
func (s *jsonScanner) scan() {
	// expectingKey distinguishes an object's key from its value, which is what
	// decides whether a half-written string can be kept.
	expectingKey := false

	for i := 0; i < len(s.input); {
		c := s.input[i]

		switch {
		case c == ' ' || c == '\t' || c == '\n' || c == '\r':
			i++

		case c == '{':
			s.stack = append(s.stack, '}')
			expectingKey = true
			// An object with nothing in it yet still closes cleanly.
			s.mark(i + 1)
			i++

		case c == '[':
			s.stack = append(s.stack, ']')
			expectingKey = false
			s.mark(i + 1)
			i++

		case c == '}' || c == ']':
			if len(s.stack) > 0 {
				s.stack = s.stack[:len(s.stack)-1]
			}
			expectingKey = s.inObject()
			s.mark(i + 1)
			i++

		case c == ',':
			// A comma promises another element, so the document cannot be
			// closed here; the previous safe point stands.
			expectingKey = s.inObject()
			i++

		case c == ':':
			expectingKey = false
			i++

		case c == '"':
			end, closed := scanString(s.input, i)
			if !closed {
				// The string runs to the end of the input. A value can be
				// shown as far as it got; a key cannot, since it has no value.
				if !expectingKey {
					s.markString(len(s.input))
				}
				return
			}
			if !expectingKey {
				s.mark(end)
			}
			i = end

		default:
			end := scanLiteral(s.input, i)
			// A literal that runs to the end of the input may still be growing
			// ("1" becoming "12"), so it only counts once it is well-formed and
			// something follows it.
			if end < len(s.input) && json.Valid([]byte(s.input[i:end])) {
				s.mark(end)
			}
			if end == i {
				// Not valid JSON at all; stop rather than spin.
				return
			}
			i = end
		}
	}
}

// inObject reports whether the innermost open container is an object.
func (s *jsonScanner) inObject() bool {
	return len(s.stack) > 0 && s.stack[len(s.stack)-1] == '}'
}

// scanString returns the index just past a string starting at i, and whether
// its closing quote was present.
func scanString(input string, i int) (int, bool) {
	for j := i + 1; j < len(input); j++ {
		switch input[j] {
		case '\\':
			j++
		case '"':
			return j + 1, true
		}
	}
	return len(input), false
}

// scanLiteral returns the index just past a number, true, false or null.
func scanLiteral(input string, i int) int {
	for j := i; j < len(input); j++ {
		switch input[j] {
		case ',', '}', ']', ' ', '\t', '\n', '\r', ':':
			return j
		}
	}
	return len(input)
}
