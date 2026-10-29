package tui

import (
	"strings"
	"testing"
)

// Behaviour pinned against loop's editor.ts.

func key(kind KeyKind, mods ...string) Key {
	k := Key{Kind: kind}
	for _, m := range mods {
		switch m {
		case "alt":
			k.Alt = true
		case "ctrl":
			k.Ctrl = true
		case "shift":
			k.Shift = true
		case "super":
			k.Super = true
		}
	}
	return k
}

func altRune(r rune) Key { return Key{Kind: KeyRune, Rune: r, Alt: true} }

func at(e *Editor) (int, int) { return e.row, e.col }

func TestUpOnTheFirstLineRecallsHistoryWithTheCursorAtTheStart(t *testing.T) {
	e := NewEditor()
	e.PushHistory("older")
	e.PushHistory("newer")
	typeText(e, "draft")
	press(e, KeyUp)
	if e.Value() != "newer" {
		t.Fatalf("up = %q, want the most recent entry", e.Value())
	}
	if r, c := at(e); r != 0 || c != 0 {
		t.Errorf("cursor at %d,%d, want the start (loop places it there going up)", r, c)
	}
	press(e, KeyUp)
	if e.Value() != "older" {
		t.Fatalf("second up = %q", e.Value())
	}
	press(e, KeyDown)
	if e.Value() != "newer" {
		t.Fatalf("down = %q", e.Value())
	}
	if _, c := at(e); c != len("newer") {
		t.Errorf("going down puts the cursor at the end, got col %d", c)
	}
	press(e, KeyDown)
	if e.Value() != "draft" {
		t.Errorf("down past the newest = %q, want the draft back", e.Value())
	}
}

func TestUpInsideAMultilineDraftMovesTheCursor(t *testing.T) {
	e := NewEditor()
	e.PushHistory("old")
	e.Insert("one\ntwo")
	press(e, KeyUp)
	if e.Value() != "one\ntwo" {
		t.Fatalf("up on the second line replaced the draft with %q", e.Value())
	}
	if r, c := at(e); r != 0 || c != 3 {
		t.Errorf("cursor at %d,%d, want 0,3", r, c)
	}
}

func TestDownOnTheLastLineGoesToItsEnd(t *testing.T) {
	e := NewEditor()
	typeText(e, "hello")
	press(e, KeyHome)
	press(e, KeyDown)
	if _, c := at(e); c != 5 {
		t.Errorf("col = %d, want the end of the line", c)
	}
}

func TestHistorySkipsConsecutiveDuplicates(t *testing.T) {
	e := NewEditor()
	e.PushHistory("same")
	e.PushHistory(" same ")
	if len(e.history) != 1 {
		t.Errorf("history = %q", e.history)
	}
}

func TestWordMotionStopsAtPunctuation(t *testing.T) {
	e := NewEditor()
	typeText(e, "foo.bar baz")
	e.Handle(key(KeyLeft, "alt"), "")
	if _, c := at(e); c != 8 {
		t.Fatalf("alt+left from the end -> %d, want 8", c)
	}
	e.Handle(key(KeyLeft, "ctrl"), "")
	if _, c := at(e); c != 4 {
		t.Fatalf("ctrl+left -> %d, want 4 (after the dot)", c)
	}
	e.Handle(altRune('b'), "")
	if _, c := at(e); c != 3 {
		t.Fatalf("alt+b -> %d, want 3 (before the dot)", c)
	}
	e.Handle(altRune('f'), "")
	if _, c := at(e); c != 4 {
		t.Errorf("alt+f -> %d, want 4", c)
	}
}

func TestKillRingAccumulatesAndYanks(t *testing.T) {
	e := NewEditor()
	typeText(e, "one two three")
	press(e, KeyCtrlW)
	press(e, KeyCtrlW)
	if e.Value() != "one " {
		t.Fatalf("after two ctrl+w = %q", e.Value())
	}
	press(e, KeyCtrlY)
	if e.Value() != "one two three" {
		t.Errorf("yank = %q, want both kills as one entry", e.Value())
	}
}

func TestYankPopCyclesTheRing(t *testing.T) {
	e := NewEditor()
	typeText(e, "alpha")
	press(e, KeyCtrlU)
	typeText(e, "beta")
	press(e, KeyCtrlU)
	press(e, KeyCtrlY)
	if e.Value() != "beta" {
		t.Fatalf("yank = %q", e.Value())
	}
	e.Handle(altRune('y'), "")
	if e.Value() != "alpha" {
		t.Errorf("yank-pop = %q, want the older kill", e.Value())
	}
}

func TestCtrlKAtTheEndJoinsTheNextLine(t *testing.T) {
	e := NewEditor()
	e.Insert("ab\ncd")
	e.Handle(key(KeyUp, "super"), "")
	press(e, KeyEnd)
	press(e, KeyCtrlK)
	if e.Value() != "abcd" {
		t.Errorf("ctrl+k at end = %q", e.Value())
	}
}

func TestAltDeleteDeletesTheNextWord(t *testing.T) {
	e := NewEditor()
	typeText(e, "keep drop")
	press(e, KeyHome)
	e.Handle(altRune('f'), "")
	e.Handle(altRune('d'), "")
	if e.Value() != "keep" {
		t.Errorf("alt+d = %q", e.Value())
	}
}

func TestNewlineKeys(t *testing.T) {
	for name, k := range map[string]Key{
		"shift+enter": key(KeyEnter, "shift"),
		"alt+enter":   key(KeyEnter, "alt"),
		"ctrl+j":      key(KeyEnter, "ctrl"),
	} {
		e := NewEditor()
		typeText(e, "a")
		if _, submitted := e.Handle(k, ""); submitted {
			t.Errorf("%s submitted", name)
		}
		if e.Value() != "a\n" {
			t.Errorf("%s -> %q", name, e.Value())
		}
	}
}

func TestBackslashEnterIsANewline(t *testing.T) {
	e := NewEditor()
	typeText(e, `line\`)
	if _, submitted := e.Handle(Key{Kind: KeyEnter}, ""); submitted {
		t.Fatal("backslash+enter submitted")
	}
	if e.Value() != "line\n" {
		t.Errorf("value = %q", e.Value())
	}
}

func TestEscWithNoListLeavesTheDraft(t *testing.T) {
	e := NewEditor()
	typeText(e, "keep me")
	press(e, KeyEsc)
	if e.Value() != "keep me" {
		t.Errorf("esc changed the draft to %q", e.Value())
	}
}

func TestLargePasteBecomesAMarkerAndExpandsOnSubmit(t *testing.T) {
	e := NewEditor()
	body := strings.Repeat("line\n", 11) + "end"
	e.Paste(body)
	if e.Value() != "[paste #1 +12 lines]" {
		t.Fatalf("draft = %q", e.Value())
	}
	submit, _ := e.Handle(Key{Kind: KeyEnter}, "")
	if submit != body {
		t.Errorf("submitted %q, want the paste expanded", submit)
	}
}

func TestBackspaceRemovesAMarkerWholeAndRenumbers(t *testing.T) {
	e := NewEditor()
	e.Paste(strings.Repeat("a", 1001))
	typeText(e, " ")
	e.Paste(strings.Repeat("b", 1002))
	if e.Value() != "[paste #1 1001 chars] [paste #2 1002 chars]" {
		t.Fatalf("draft = %q", e.Value())
	}
	press(e, KeyHome)
	e.Handle(altRune('f'), "")
	press(e, KeyBackspace)
	if e.Value() != " [paste #1 1002 chars]" {
		t.Fatalf("after deleting the first marker = %q", e.Value())
	}
	press(e, KeyEnd)
	submit, _ := e.Handle(Key{Kind: KeyEnter}, "")
	if submit != strings.Repeat("b", 1002) {
		t.Errorf("the renumbered marker expanded to %d bytes", len(submit))
	}
}

func TestPasteNormalisesTabsAndControlBytes(t *testing.T) {
	e := NewEditor()
	e.Paste("a\tb\x07c\r\nd")
	if e.Value() != "a    bc\nd" {
		t.Errorf("draft = %q", e.Value())
	}
}

func TestPastedPathAfterAWordGetsASpace(t *testing.T) {
	e := NewEditor()
	typeText(e, "see")
	e.Paste("/tmp/x")
	if e.Value() != "see /tmp/x" {
		t.Errorf("draft = %q", e.Value())
	}
}

func TestJumpToChar(t *testing.T) {
	e := NewEditor()
	typeText(e, "abcabc")
	press(e, KeyHome)
	press(e, KeyCtrlBracket)
	typeText(e, "c")
	if _, c := at(e); c != 2 {
		t.Fatalf("jump forward to c -> %d", c)
	}
	e.Handle(Key{Kind: KeyCtrlBracket, Alt: true}, "")
	typeText(e, "a")
	if _, c := at(e); c != 0 {
		t.Errorf("jump backward to a -> %d", c)
	}
	if e.Value() != "abcabc" {
		t.Errorf("jump typed into the draft: %q", e.Value())
	}
}

func TestTallDraftScrollsWithIndicators(t *testing.T) {
	e := NewEditor()
	e.TermRows = 10 // five visible lines
	e.Insert(strings.Repeat("x\n", 9) + "x")
	lines, _, _ := e.View(40)
	if !strings.Contains(stripANSI(lines[0]), "↑ 5 more") {
		t.Errorf("top border = %q", stripANSI(lines[0]))
	}
	if got := len(lines); got != 7 {
		t.Errorf("rendered %d rows, want 5 lines between two borders", got)
	}
}

func TestEmojiIsOneCursorStep(t *testing.T) {
	e := NewEditor()
	typeText(e, "a👨‍👩‍👧b")
	press(e, KeyLeft)
	press(e, KeyLeft)
	if _, c := at(e); c != 1 {
		t.Errorf("col = %d, want 1 (the whole emoji skipped)", c)
	}
	press(e, KeyDelete)
	if e.Value() != "ab" {
		t.Errorf("delete = %q", e.Value())
	}
}

func TestTabWithNoListCompletesASingleFile(t *testing.T) {
	files := func(word string) []Item { return []Item{{Value: "@README.md", Label: "README.md"}} }
	e := newComposer(testCommands(), files)
	typeText(e, "open REA")
	press(e, KeyTab)
	if e.Value() != "open README.md" {
		t.Errorf("forced tab = %q", e.Value())
	}
}

func TestAtCompletionAddsASpaceAfterAFile(t *testing.T) {
	files := func(string) []Item { return []Item{{Value: "@main.go", Label: "main.go"}} }
	e := newComposer(testCommands(), files)
	typeText(e, "@ma")
	press(e, KeyTab)
	if e.Value() != "@main.go " {
		t.Errorf("draft = %q", e.Value())
	}
}

func TestSlashListOnlyOnTheFirstLine(t *testing.T) {
	e := newComposer(testCommands(), nil)
	e.Insert("hi\n")
	typeText(e, "/co")
	if e.menu != nil {
		t.Error("a slash on the second line opened the command list")
	}
}

func TestLegacyChordsDecode(t *testing.T) {
	cases := map[string]Key{
		"\x02":       {Kind: KeyCtrlB},
		"\x06":       {Kind: KeyCtrlF},
		"\x16":       {Kind: KeyCtrlV},
		"\x1d":       {Kind: KeyCtrlBracket},
		"\x1f":       {Kind: KeyCtrlMinus},
		"\n":         {Kind: KeyEnter, Ctrl: true},
		"\x1b\x7f":   {Kind: KeyBackspace, Alt: true},
		"\x1b\x1d":   {Kind: KeyCtrlBracket, Alt: true},
		"\x1b[1;9D":  {Kind: KeyLeft, Super: true},
		"\x1b[3;3~":  {Kind: KeyDelete, Alt: true},
		"\x1b[13;2~": {Kind: KeyEnter, Shift: true},
	}
	for in, want := range cases {
		got, n := decodeKeyFull([]byte(in))
		if n != len(in) || got != want {
			t.Errorf("%q -> %+v (%d bytes), want %+v", in, got, n, want)
		}
	}
}

func TestKittyChordsDecode(t *testing.T) {
	cases := map[string]Key{
		"122;9": {Kind: KeyRune, Rune: 'z', Super: true},
		"127;9": {Kind: KeyBackspace, Super: true},
		"127;3": {Kind: KeyBackspace, Alt: true},
		"13;2":  {Kind: KeyEnter, Shift: true},
		"106;5": {Kind: KeyEnter, Ctrl: true},
		"45;5":  {Kind: KeyCtrlMinus},
		"105;5": {Kind: KeyCtrlI},
	}
	for params, want := range cases {
		if got, ok := decodeKittyKey(params); !ok || got != want {
			t.Errorf("CSI %su -> %+v, want %+v", params, got, want)
		}
	}
}
