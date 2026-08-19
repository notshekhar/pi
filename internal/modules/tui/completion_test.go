package tui

import (
	"os"
	"strings"
	"testing"
)

// newComposer wires an editor to a fixed command list.
func newComposer(commands []Item, files func(string) []Item) *Editor {
	a := &App{editor: NewEditor(), commands: commands, files: files, theme: testTheme()}
	a.editor.Theme = a.theme
	a.editor.SetCompleter(a.complete)
	return a.editor
}

func testCommands() []Item {
	return []Item{
		{Value: "/help", Label: "help", Description: "Show available commands"},
		{Value: "/compact", Label: "compact", Description: "Replace the history with a summary"},
		{Value: "/context", Label: "context", Description: "How much of the window is used"},
		{Value: "/cost", Label: "cost", Description: "What this session has spent"},
		{Value: "/new", Label: "new", Description: "Start a fresh session"},
	}
}

func menuLabels(e *Editor) []string {
	if e.menu == nil {
		return nil
	}
	out := make([]string, 0, len(e.menu.Items))
	for _, it := range e.menu.Items {
		out = append(out, it.Label)
	}
	return out
}

// A bare `/` must open the list. The trigger is the FIRST character of the
// word, so looking behind the word meant this never fired at all.
func TestSlashOpensTheCommandList(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/")
	if e.menu == nil {
		t.Fatal("typing / did not open the command list")
	}
	if len(e.menu.Items) != len(testCommands()) {
		t.Errorf("got %d commands, want all of them", len(e.menu.Items))
	}
}

// The menu must not swallow typing — the list narrows as the word grows.
func TestTypingFiltersTheOpenList(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/co")
	if e.Value() != "/co" {
		t.Fatalf("the menu swallowed the keystrokes: draft = %q", e.Value())
	}
	got := menuLabels(e)
	for _, want := range []string{"compact", "context", "cost"} {
		if !contains(got, want) {
			t.Errorf("%q missing from %v", want, got)
		}
	}
	// Matched on the name, not the description — `help` describes itself as
	// "Show available commands", which contains "co".
	if contains(got, "help") {
		t.Errorf("help matched on its description: %v", got)
	}
}

func TestPrefixMatchesRankFirst(t *testing.T) {
	e := newComposer([]Item{
		{Value: "/recap", Label: "recap"},
		{Value: "/cost", Label: "cost"},
	}, nil)
	typeText(e, "/ca")
	// "recap" contains "ca"; nothing starts with it.
	if got := menuLabels(e); len(got) != 1 || got[0] != "recap" {
		t.Errorf("got %v", got)
	}
}

// Enter on a slash command ACCEPTS AND RUNS it, as loop does. Accepting
// alone left the completed command sitting in the composer waiting for a
// second Enter, which is not what the keystroke reads as.
func TestEnterRunsTheSelectedCommand(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/comp")
	submit, _, _ := e.Handle(Key{Kind: KeyEnter}, "")
	if submit != "/compact" {
		t.Errorf("submitted %q, want /compact", submit)
	}
	if e.Value() != "" {
		t.Errorf("draft = %q, want it cleared on submit", e.Value())
	}
	if e.menu != nil {
		t.Error("the menu stayed open after accepting")
	}
}

// Tab accepts WITHOUT running, so a command can be completed and then given
// arguments.
func TestTabAcceptsWithoutRunning(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/comp")
	submit, _, _ := e.Handle(Key{Kind: KeyTab}, "")
	if submit != "" {
		t.Errorf("tab submitted %q; it must only complete", submit)
	}
	if e.Value() != "/compact" {
		t.Errorf("draft = %q, want /compact", e.Value())
	}
}

func TestArrowsMoveTheSelectionWithoutTyping(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/co")
	press(e, KeyDown)
	submit, _, _ := e.Handle(Key{Kind: KeyEnter}, "")
	if submit != "/context" {
		t.Errorf("submitted %q, want the second match", submit)
	}
}

// A file completion is part of a sentence still being written, so Enter
// accepts it and stays put rather than sending the half-written message.
func TestEnterOnAFileCompletionDoesNotSubmit(t *testing.T) {
	files := func(string) []Item { return []Item{{Value: "@main.go", Label: "main.go"}} }
	e := newComposer(testCommands(), files)
	typeText(e, "look at @mai")
	submit, _, _ := e.Handle(Key{Kind: KeyEnter}, "")
	if submit != "" {
		t.Errorf("a file completion submitted %q", submit)
	}
	if !strings.Contains(e.Value(), "@main.go") {
		t.Errorf("draft = %q, want the completed path", e.Value())
	}
}

func TestEscClosesTheListAndKeepsTheDraft(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/co")
	press(e, KeyEsc)
	if e.menu != nil {
		t.Error("esc did not close the list")
	}
	if e.Value() != "/co" {
		t.Errorf("esc discarded the draft: %q", e.Value())
	}
}

// A slash is only a command at the very start of the draft; anywhere else it
// is a path separator.
func TestSlashMidLineIsNotACommand(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "see src/co")
	if e.menu != nil {
		t.Errorf("a mid-line slash opened the command list: %v", menuLabels(e))
	}
}

func TestAtCompletesFiles(t *testing.T) {
	files := func(word string) []Item {
		if strings.HasPrefix("internal", word) {
			return []Item{{Value: "@internal", Label: "internal/"}}
		}
		return nil
	}
	e := newComposer(testCommands(), files)
	typeText(e, "look at @int")
	if e.menu == nil {
		t.Fatal("@ did not open file completion")
	}
	press(e, KeyEnter)
	// The value carries the `@`, because the replaced word includes it.
	if e.Value() != "look at @internal" {
		t.Errorf("draft = %q", e.Value())
	}
}

func TestBackspacingBelowTheTriggerClosesTheList(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "/co")
	press(e, KeyBackspace)
	press(e, KeyBackspace)
	press(e, KeyBackspace)
	if e.menu != nil {
		t.Error("the list survived deleting the trigger")
	}
}

func contains(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// The cursor must land immediately after the text. View reports a column that
// already includes the composer's padding, so the only adjustment the caller
// may make is 0→1 indexing.
func TestComposerCursorSitsAfterTheText(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "hello")

	lines, curRow, curCol := e.View(60)
	// Row 0 is the top rule; the draft is row 1.
	if curRow != 1 {
		t.Errorf("curRow = %d, want 1 (under the top rule)", curRow)
	}
	drafted := stripANSI(lines[curRow])
	// One column of padding, then the text, then the cursor.
	if drafted != " hello" {
		t.Errorf("draft line = %q, want %q", drafted, " hello")
	}
	if curCol != len(" hello") {
		t.Errorf("curCol = %d, want %d (just past the text)", curCol, len(" hello"))
	}
}

func TestComposerIsFramedByRules(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, "hi")
	lines, _, _ := e.View(40)
	if len(lines) < 3 {
		t.Fatalf("got %d lines", len(lines))
	}
	first, last := stripANSI(lines[0]), stripANSI(lines[len(lines)-1])
	if first != strings.Repeat("─", 40) {
		t.Errorf("top rule = %q", first)
	}
	if last != strings.Repeat("─", 40) {
		t.Errorf("bottom rule = %q", last)
	}
}

// A wrapped draft keeps the cursor on the right visual row.
func TestComposerCursorFollowsAWrap(t *testing.T) {
	e := newComposer(testCommands(), nil)
	typeText(e, strings.Repeat("x", 30))
	lines, curRow, curCol := e.View(20)
	if curRow < 1 || curRow >= len(lines)-1 {
		t.Fatalf("curRow = %d, outside the draft rows (0..%d)", curRow, len(lines)-1)
	}
	if curCol < 1 || curCol > 20 {
		t.Errorf("curCol = %d, off the composer", curCol)
	}
}

// The width and background probes read from stdin. If either leaves a reader
// parked there after giving up, it eats the user's first keystrokes — the
// prompt swallowed `he` from `hello` before this was fixed. There is nothing
// to assert about a goroutine that should not exist, so this pins the
// contract that makes it impossible: the probe path must be deadline-based,
// never a goroutine we abandon.
func TestProbeUsesADeadlineNotAnOrphanReader(t *testing.T) {
	src, err := os.ReadFile("probe.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	if !strings.Contains(body, "SetReadDeadline") {
		t.Error("askTerminal must bound its read with a deadline")
	}
	if strings.Contains(body, "go func()") {
		t.Error("askTerminal must not read on a goroutine it cannot stop")
	}
}

func TestParseCPR(t *testing.T) {
	cases := []struct {
		in  string
		col int
		ok  bool
	}{
		{"\x1b[12;34R", 34, true},
		{"\x1b[1;1R", 1, true},
		{"garbage", 0, false},
		{"", 0, false},
		{"\x1b[12R", 0, false},
	}
	for _, c := range cases {
		col, ok := parseCPR(c.in)
		if ok != c.ok || (ok && col != c.col) {
			t.Errorf("parseCPR(%q) = %d,%v want %d,%v", c.in, col, ok, c.col, c.ok)
		}
	}
}

func TestParseOSCBackground(t *testing.T) {
	cases := []struct {
		in   string
		want Background
	}{
		{"\x1b]11;rgb:ffff/ffff/ffff\x07", BackgroundLight},
		{"\x1b]11;rgb:0000/0000/0000\x07", BackgroundDark},
		{"\x1b]11;rgb:1414/1414/1414\x07", BackgroundDark},
		// Replies vary in bits per channel; a short one must not read as dark
		// just because the digits are fewer.
		{"\x1b]11;rgb:ff/ff/ff\x07", BackgroundLight},
		{"nonsense", BackgroundUnknown},
	}
	for _, c := range cases {
		if got := parseOSCBackground(c.in); got != c.want {
			t.Errorf("parseOSCBackground(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}
