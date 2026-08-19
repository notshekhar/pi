package tui

import "testing"

// The bug this pins, and it is a real one people hit: macOS terminals bind
// cmd+→ to the literal byte `\x05`, which IS legacy ctrl+e. Pressing cmd+→ to
// jump to the end of the line threw people into nav mode.
func TestCmdRightDoesNotToggleNav(t *testing.T) {
	SetKittyForTest(true)
	defer SetKittyForTest(false)

	// What a terminal macro sends: the bare byte, decoded the legacy way.
	macro, n := decodeKeyFull([]byte{0x05})
	if n != 1 || macro.Kind != KeyCtrlE {
		t.Fatalf("\\x05 decoded as %v (%d bytes)", macro.Kind, n)
	}
	if navToggle(macro) {
		t.Error("a terminal macro's \\x05 toggled nav mode")
	}

	// What the keyboard sends once the protocol is negotiated.
	real, n := decodeKeyFull([]byte("\x1b[101;5u"))
	if n != 8 || real.Kind != KeyCtrlE || !real.Unambiguous {
		t.Fatalf("ctrl+e CSI-u decoded as %+v (%d bytes)", real, n)
	}
	if !navToggle(real) {
		t.Error("a real ctrl+e did not toggle nav mode")
	}
}

// On a terminal that cannot negotiate, the bare byte is the only ctrl+e there
// is — demanding the unambiguous form would break the chord entirely.
func TestBareByteStillWorksWithoutTheProtocol(t *testing.T) {
	SetKittyForTest(false)
	k, _ := decodeKeyFull([]byte{0x05})
	if !navToggle(k) {
		t.Error("ctrl+e stopped working on a terminal without the kitty protocol")
	}
}

// Turning the protocol on must not quietly retire the other chords: they all
// arrive as CSI-u too.
func TestOtherChordsSurviveTheProtocol(t *testing.T) {
	cases := map[string]KeyKind{
		"\x1b[99;5u":  KeyCtrlC,
		"\x1b[112;5u": KeyCtrlP,
		"\x1b[108;5u": KeyCtrlL,
		"\x1b[13;1u":  KeyEnter,
		"\x1b[27;1u":  KeyEsc,
		"\x1b[9;1u":   KeyTab,
		"\x1b[127;1u": KeyBackspace,
	}
	for seq, want := range cases {
		k, n := decodeKeyFull([]byte(seq))
		if n != len(seq) || k.Kind != want {
			t.Errorf("%q decoded as %v (%d bytes), want %v", seq, k.Kind, n, want)
		}
	}
	// And a plain letter still arrives as a rune.
	if k, _ := decodeKeyFull([]byte("\x1b[101;1u")); k.Kind != KeyRune || k.Rune != 'e' {
		t.Errorf("plain e decoded as %+v", k)
	}
}

// The reply parser has to tell "protocol in force" from a terminal that only
// answered the device-attributes terminator.
func TestKittyReplyDetection(t *testing.T) {
	if !kittyReplied("\x1b[?1u\x1b[?62;c") {
		t.Error("a flags report was not recognised")
	}
	if kittyReplied("\x1b[?62;1;6c") {
		t.Error("a bare device-attributes reply was read as protocol support")
	}
	if kittyReplied("\x1b[?0u") {
		t.Error("flags of 0 means the protocol is not in force")
	}
}
