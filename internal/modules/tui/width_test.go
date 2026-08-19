package tui

import (
	"strings"
	"testing"
)

func TestVisibleWidth(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"ascii", "hello", 5},
		{"empty", "", 0},
		{"ansi is free", "\x1b[1mhello\x1b[0m", 5},
		{"truecolor is free", "\x1b[38;2;119;160;220mhi\x1b[0m", 2},
		{"osc hyperlink is free", "\x1b]8;;https://x.dev\x07link\x1b]8;;\x07", 4},
		{"tab is three", "a\tb", 5},
		{"cjk is double", "日本語", 6},
		{"fullwidth punctuation", "！？", 4},
		{"combining acute is free", "é", 1},
		// The bug loop shipped a fix for: a Devanagari cluster is several
		// runes but its matras are what take cells, not the rune count.
		{"devanagari cluster", "प्रे", 2},
		{"devanagari word", "नमस्ते", 4},
		{"emoji is double", "🎉", 2},
		{"emoji with selector", "❤️", 2},
		{"zwj family is one emoji", "👨‍👩‍👧", 2},
		{"flag is one emoji", "🇮🇳", 2},
		{"box drawing is single", "┃│─", 3},
		{"diamond is single", "◆", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := visibleWidth(c.in); got != c.want {
				t.Errorf("visibleWidth(%q) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

func TestWrapTextWithAnsiKeepsWidth(t *testing.T) {
	text := "the quick brown fox jumps over the lazy dog and keeps running"
	for _, width := range []int{10, 20, 37} {
		for _, line := range wrapTextWithAnsi(text, width) {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: line %q is %d cells", width, line, w)
			}
		}
	}
}

func TestWrapTextWithAnsiCarriesStyleAcrossWrap(t *testing.T) {
	// A style opened before the break must be re-opened on the next line,
	// or the second half of a coloured paragraph renders unstyled.
	lines := wrapTextWithAnsi("\x1b[1mbold words that will not fit on one line\x1b[0m", 12)
	if len(lines) < 2 {
		t.Fatalf("expected a wrap, got %d line(s)", len(lines))
	}
	if !strings.Contains(lines[1], "\x1b[1m") {
		t.Errorf("continuation line lost its bold: %q", lines[1])
	}
}

func TestWrapTextWithAnsiBreaksOverlongToken(t *testing.T) {
	long := strings.Repeat("x", 25)
	lines := wrapTextWithAnsi(long, 10)
	if len(lines) != 3 {
		t.Fatalf("expected 3 pieces, got %d: %q", len(lines), lines)
	}
	if strings.Join(lines, "") != long {
		t.Errorf("broken token did not reassemble: %q", lines)
	}
}

func TestWrapTextWithAnsiKeepsBlankLines(t *testing.T) {
	lines := wrapTextWithAnsi("a\n\nb", 20)
	want := []string{"a", "", "b"}
	if len(lines) != len(want) {
		t.Fatalf("got %q, want %q", lines, want)
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

func TestTruncateToWidth(t *testing.T) {
	if got := truncateToWidth("hello world", 20, "…"); got != "hello world" {
		t.Errorf("short text was altered: %q", got)
	}
	got := truncateToWidth("hello world", 8, "…")
	if w := visibleWidth(got); w != 8 {
		t.Errorf("truncateToWidth = %q (%d cells), want 8", got, w)
	}
	// A double-width cell must not be split into half a cell.
	got = truncateToWidth("日本語です", 5, "")
	if w := visibleWidth(got); w > 5 {
		t.Errorf("truncated CJK is %d cells: %q", w, got)
	}
}

func TestFitRowNeverOverflows(t *testing.T) {
	// An overflowing row wraps and pushes the whole grid down a line, which
	// desynchronises every absolute cursor move after it.
	row := "\x1b[1m◆ bash\x1b[0m " + strings.Repeat("git log --oneline ", 10)
	fitted := fitRow(row, 40)
	if w := visibleWidth(fitted); w > 40 {
		t.Errorf("fitRow left %d cells: %q", w, fitted)
	}
	if !strings.HasSuffix(fitted, "…") {
		t.Errorf("truncated row lost its ellipsis: %q", fitted)
	}
}

func TestPadToWidth(t *testing.T) {
	if got := padToWidth("\x1b[1mab\x1b[0m", 6); visibleWidth(got) != 6 {
		t.Errorf("padToWidth = %q (%d cells), want 6", got, visibleWidth(got))
	}
}
