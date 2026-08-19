package tui

import (
	"testing"
	"time"
)

// scriptedStdin feeds the decoder bytes with a pause between them, the way a
// person types.
type scriptedStdin struct {
	steps chan []byte
}

func (s *scriptedStdin) read(buf []byte) (int, error) {
	chunk, ok := <-s.steps
	if !ok {
		return 0, errClosed{}
	}
	return copy(buf, chunk), nil
}

type errClosed struct{}

func (errClosed) Error() string { return "closed" }

// The bug this pins, reported from real use: press Esc to close a menu, then
// start typing, and the first character never arrives.
//
// The cause was that a lone Esc is only known to BE lone after a timeout, and
// the read racing that timeout used to be abandoned when the timer won. The
// abandoned read was still parked on stdin, so it consumed the next keystroke
// and delivered it nowhere.
func TestKeyAfterLoneEscIsNotSwallowed(t *testing.T) {
	script := &scriptedStdin{steps: make(chan []byte, 4)}
	original := readStdin
	readStdin = script.read
	defer func() { readStdin = original }()

	d := DecodeKeys()
	defer d.Close()

	// Esc alone. The decoder cannot tell yet whether it prefixes a sequence,
	// so it waits out escTimeoutMs.
	script.steps <- []byte{0x1b}
	if got := waitKey(t, d); got.Kind != KeyEsc {
		t.Fatalf("first key = %v, want Esc", got.Kind)
	}

	// Now type. This is the character that used to vanish.
	script.steps <- []byte("a")
	got := waitKey(t, d)
	if got.Kind != KeyRune || got.Rune != 'a' {
		t.Fatalf("after Esc got %v %q, want the rune 'a'", got.Kind, got.Rune)
	}

	// And the one after it, to be sure the stream did not desynchronise.
	script.steps <- []byte("b")
	got = waitKey(t, d)
	if got.Kind != KeyRune || got.Rune != 'b' {
		t.Fatalf("second key after Esc got %v %q, want 'b'", got.Kind, got.Rune)
	}
}

// Esc still combines with a sequence that arrives promptly — the timeout must
// not have turned every escape sequence into a lone Esc.
func TestEscapeSequenceStillDecodes(t *testing.T) {
	script := &scriptedStdin{steps: make(chan []byte, 4)}
	original := readStdin
	readStdin = script.read
	defer func() { readStdin = original }()

	d := DecodeKeys()
	defer d.Close()

	// Up arrow, whole.
	script.steps <- []byte("\x1b[A")
	if got := waitKey(t, d); got.Kind != KeyUp {
		t.Fatalf("got %v, want Up", got.Kind)
	}

	// And split across two reads, arriving inside the window — the case the
	// timeout exists for.
	script.steps <- []byte("\x1b")
	script.steps <- []byte("[B")
	if got := waitKey(t, d); got.Kind != KeyDown {
		t.Fatalf("split sequence gave %v, want Down", got.Kind)
	}
}

func waitKey(t *testing.T, d *KeyDecoder) Key {
	t.Helper()
	select {
	case k, ok := <-d.Ch:
		if !ok {
			t.Fatal("the decoder closed")
		}
		return k
	case <-time.After(2 * time.Second):
		t.Fatal("no key arrived")
	}
	return Key{}
}
