package tui

import (
	"strings"
	"testing"
)

// An aside is loop's /btw answer card: its heading, a rule, the answer as
// markdown, a rule, its footer — each behind one column of padding.
func TestAsideIsRuledBetweenItsHeadingAndFooter(t *testing.T) {
	theme := NewTheme(NightPalette)
	e := &entry{kind: entDraft, aside: &asideText{head: "↳ btw · why?", foot: "not part of this conversation"},
		text: "**because**", md: NewMarkdown(MarkdownThemeFor(theme), nil)}
	lines := e.render(theme, 40, 0, false, false)
	for i := range lines {
		lines[i] = strings.TrimRight(stripANSI(lines[i]), " ")
	}
	rule := strings.Repeat("─", 40)
	want := []string{" ↳ btw · why?", rule, " because", rule, " not part of this conversation"}
	if strings.Join(lines, "\n") != strings.Join(want, "\n") {
		t.Fatalf("aside:\n%s\nwant:\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// A tagged prompt is gap, tag, panel — the panel's own gap is the tag's.
func TestTaggedUserEchoPutsTheTagBetweenTheGapAndThePanel(t *testing.T) {
	theme := NewTheme(NightPalette)
	plain := (&entry{kind: entUser, text: "why?"}).render(theme, 40, 0, false, false)
	tagged := (&entry{kind: entUser, text: "why?", tag: "↳ Side question"}).render(theme, 40, 0, false, false)
	if len(tagged) != len(plain)+1 || stripANSI(tagged[0]) != "" || strings.TrimSpace(stripANSI(tagged[1])) != "↳ Side question" {
		t.Fatalf("tagged prompt:\n%s", stripANSI(strings.Join(tagged, "\n")))
	}
	if strings.Join(tagged[2:], "\n") != strings.Join(plain[1:], "\n") {
		t.Error("the panel under the tag is not the plain prompt's panel")
	}
}
