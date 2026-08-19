package tui

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
)

// The kitty keyboard protocol, negotiated for exactly one reason: to tell a
// real ctrl+e apart from a terminal macro that sends the same byte.
//
// macOS terminals ship the readline line-navigation bindings by default.
// Ghostty binds cmd+← / cmd+→ to `text:\x01` / `text:\x05` — the literal
// control BYTES — and legacy ctrl+e IS `\x05`. Under a byte comparison the two
// are the same key, which is how cmd+→ ended up flinging people into nav mode
// while they were only trying to get to the end of the line.
//
// They separate once this protocol is on: a real ctrl+e then arrives as a
// CSI-u sequence, `\x1b[101;5u`, and only a terminal macro still sends the
// bare byte. So the nav toggle requires the unambiguous form when the protocol
// is active, and falls back to the byte when it is not — because on a terminal
// that cannot negotiate, the byte is all there is and demanding CSI-u would
// break ctrl+e entirely.
//
// Only flag 1 (disambiguate escape codes) is requested. loop asks for 7,
// which adds key-event types and alternate keys; those change how EVERY key
// arrives — including release events that a decoder has to filter — and none
// of it is needed for the one ambiguity being resolved here.

const kittyFlags = 1

// kittyOn is the negotiated state, read by the decoder on every keystroke.
var kittyOn atomic.Bool

// KittyActive reports whether the kitty keyboard protocol was negotiated.
func KittyActive() bool { return kittyOn.Load() }

// SetKittyForTest pins the state so the ambiguity can be tested both ways.
func SetKittyForTest(on bool) { kittyOn.Store(on) }

// EnableKittyKeyboard negotiates the protocol and returns the function that
// pops it back off.
//
// MUST run with the terminal already in raw mode and BEFORE the key decoder
// starts: it reads the reply from stdin, and a decoder running alongside would
// race for those bytes — swallowing the answer, or delivering the escape
// sequence to the composer as keystrokes.
func EnableKittyKeyboard() func() {
	if !isTerminal(os.Stdin) || !isTerminal(os.Stdout) {
		return func() {}
	}
	// Push the flags, ask what is now in force, and request device attributes
	// as a TERMINATOR. The last part is what makes this safe on a terminal
	// that has never heard of the protocol: it ignores the first two
	// sequences and answers the third, so the read completes with an answer
	// that is recognisably not a kitty reply instead of waiting out the
	// timeout on every launch.
	reply, ok := askTerminal(
		fmt.Sprintf("\x1b[>%du\x1b[?u\x1b[c", kittyFlags), probeTimeout)
	if !ok || !kittyReplied(reply) {
		// Nothing was pushed if the terminal did not understand it, but pop
		// anyway: a terminal that understood the push and then failed to
		// answer would otherwise be left with our flags in force after exit.
		os.Stdout.WriteString("\x1b[<u")
		return func() {}
	}
	kittyOn.Store(true)
	return func() {
		kittyOn.Store(false)
		os.Stdout.WriteString("\x1b[<u")
	}
}

// kittyReplied reports whether the answer contains a kitty flags report,
// `ESC [ ? <flags> u`, with the protocol actually in force.
func kittyReplied(reply string) bool {
	for i := 0; ; {
		j := strings.Index(reply[i:], "\x1b[?")
		if j < 0 {
			return false
		}
		i += j + 3
		end := i
		for end < len(reply) && reply[end] >= '0' && reply[end] <= '9' {
			end++
		}
		if end < len(reply) && reply[end] == 'u' && end > i {
			flags, err := strconv.Atoi(reply[i:end])
			return err == nil && flags != 0
		}
	}
}

// decodeKittyKey reads `ESC [ <codepoint> ; <mods> u`, the CSI-u form.
//
// Only the keys whose legacy encoding is ambiguous need handling here; the
// rest still arrive exactly as they did, because flag 1 changes nothing else.
// An unrecognised CSI-u sequence is consumed rather than passed on, since
// emitting a bare Esc for it would be worse than dropping it.
func decodeKittyKey(params string) (Key, bool) {
	code, mods, _ := strings.Cut(params, ";")
	cp, err := strconv.Atoi(code)
	if err != nil {
		return Key{}, false
	}
	// The modifier field is a bitfield PLUS ONE, so 5 is ctrl (4) + 1.
	mod := 1
	if mods != "" {
		// It can carry an event type after a colon (`5:3` = ctrl, release).
		modNum, _, _ := strings.Cut(mods, ":")
		if n, err := strconv.Atoi(modNum); err == nil {
			mod = n
		}
	}
	bits := mod - 1
	ctrl, shift, alt := bits&4 != 0, bits&1 != 0, bits&2 != 0

	if ctrl {
		// The one that matters, and the reason this file exists.
		if cp == 'e' {
			return Key{Kind: KeyCtrlE, Unambiguous: true}, true
		}
		if k, ok := ctrlRuneKey(rune(cp)); ok {
			return k, true
		}
		return Key{}, false
	}
	switch cp {
	case 13:
		return Key{Kind: KeyEnter, Shift: shift, Alt: alt}, true
	case 27:
		return Key{Kind: KeyEsc}, true
	case 9:
		if shift {
			return Key{Kind: KeyBacktab}, true
		}
		return Key{Kind: KeyTab}, true
	case 127:
		return Key{Kind: KeyBackspace}, true
	}
	if cp >= 32 && cp != 127 {
		return Key{Kind: KeyRune, Rune: rune(cp), Alt: alt, Shift: shift}, true
	}
	return Key{}, false
}

// ctrlRuneKey maps ctrl+<letter> to the kind its legacy byte would produce,
// so turning the protocol on does not quietly retire every other chord.
func ctrlRuneKey(r rune) (Key, bool) {
	switch r {
	case 'a':
		return Key{Kind: KeyCtrlA}, true
	case 'c':
		return Key{Kind: KeyCtrlC}, true
	case 'd':
		return Key{Kind: KeyCtrlD}, true
	case 'g':
		return Key{Kind: KeyCtrlG}, true
	case 'k':
		return Key{Kind: KeyCtrlK}, true
	case 'l':
		return Key{Kind: KeyCtrlL}, true
	case 'p':
		return Key{Kind: KeyCtrlP}, true
	case 'u':
		return Key{Kind: KeyCtrlU}, true
	case 'w':
		return Key{Kind: KeyCtrlW}, true
	case 'y':
		return Key{Kind: KeyCtrlY}, true
	case 'z':
		return Key{Kind: KeyCtrlZ}, true
	}
	return Key{}, false
}

// navToggle reports whether a key should turn nav mode on or off.
//
// This is the whole point of the negotiation. With the protocol active only
// the CSI-u form counts, so Ghostty's cmd+→ — which sends the bare `\x05` —
// moves to the end of the line and nothing else. Without it, the bare byte is
// the only ctrl+e a terminal can send and has to be honoured.
func navToggle(k Key) bool {
	return k.Kind == KeyCtrlE && (k.Unambiguous || !KittyActive())
}
