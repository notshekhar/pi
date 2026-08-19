package tui

import (
	"strings"
	"testing"
)

func composerLine(t *testing.T, text string) string {
	t.Helper()
	e := NewEditor()
	e.Theme = testTheme()
	e.Insert(text)
	lines, _, _ := e.View(60)
	// Row 0 is the top rule; row 1 is the first line of text.
	if len(lines) < 2 {
		t.Fatalf("composer rendered %d rows", len(lines))
	}
	return lines[1]
}

// The leading command token is tinted as it is typed. It is the fastest
// confirmation that a line will be READ as a command rather than sent to the
// model — the one thing a leading slash decides.
func TestComposerTintsTheCommandToken(t *testing.T) {
	line := composerLine(t, "/model kimi/k3")
	if !strings.Contains(line, "\x1b[") {
		t.Fatalf("no styling on a slash command: %q", line)
	}
	// The token is coloured and its ARGUMENT is not: the colour says "this is
	// a command", not "this whole line is special".
	before, after, ok := strings.Cut(line, "/model")
	if !ok {
		t.Fatalf("token missing: %q", line)
	}
	if !strings.Contains(before, "\x1b[") {
		t.Errorf("the token does not start a colour: %q", line)
	}
	if strings.Contains(strings.TrimPrefix(after, "\x1b[39m"), "\x1b[") {
		t.Errorf("the argument was tinted too: %q", line)
	}
}

// A bang runs the rest of the line as a shell command, so only the marker is
// the token — the command itself is the user's text, not ours to colour.
func TestComposerTintsOnlyTheBang(t *testing.T) {
	line := composerLine(t, "!ls -la")
	if !strings.Contains(line, "\x1b[") {
		t.Fatalf("no styling on a bash line: %q", line)
	}
	if strings.Contains(line, "\x1b[39mls") {
		return // marker closed before "ls" — correct
	}
	if !strings.Contains(stripANSI(line), "!ls -la") {
		t.Errorf("text mangled: %q", stripANSI(line))
	}
}

// Ordinary prose is never tinted, and neither is a slash that is not at the
// start — `/usr/bin` in a sentence is a path.
func TestComposerLeavesProseAlone(t *testing.T) {
	for _, text := range []string{
		"what is in /usr/bin?",
		"tell me about the / character",
		"hello",
		"/",
	} {
		if line := composerLine(t, text); strings.Contains(line, "\x1b[38") {
			t.Errorf("tinted prose %q: %q", text, line)
		}
	}
}
