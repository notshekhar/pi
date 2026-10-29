package agent

import (
	"strings"
	"testing"
)

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		// The decoration models add unasked.
		`**"Fix the flaky pty test."**`: "Fix the flaky pty test",
		"`Add cmux status reporting`":   "Add cmux status reporting",
		"# Refactor the parser":         "Refactor the parser",
		// One line: a newline in an OSC sequence ends it.
		"Fix the pty test\n\nThis title covers the work described above.": "Fix the pty test",
		"\n\n  Add session titles  \n":                                    "Add session titles",
		"Fix cmux loop status reporter":                                   "Fix cmux loop status reporter",
		"":                                                                "",
		"\n \n":                                                           "",
	}
	for in, want := range cases {
		if got := CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// Elided at a word boundary, so what survives still reads as a name.
func TestCleanTitleElidesAtAWord(t *testing.T) {
	long := "Add loop awareness to the cmux terminal integration everywhere"
	out := CleanTitle(long)
	if out != "Add loop awareness to the cmux terminal…" {
		t.Fatalf("got %q", out)
	}
	if !strings.HasPrefix(long, strings.TrimSuffix(out, "…")) {
		t.Errorf("%q is not a prefix of the input", out)
	}
}
