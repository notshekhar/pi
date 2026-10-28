package tui

import (
	"strings"
	"testing"
)

// plainTheme renders structure without colour, so a test asserts on layout
// rather than on escape sequences.
func plainTheme() MarkdownTheme {
	id := func(s string) string { return s }
	return MarkdownTheme{
		Heading: id, Link: id, LinkURL: id, Code: id, CodeBlock: id,
		CodeBlockBorder: id, Quote: id, QuoteBorder: id, Hr: id, ListBullet: id,
		Bold: id, Italic: id, Strikethrough: id, Underline: id,
	}
}

// render lays out src and strips styling, for structural assertions.
func render(t *testing.T, src string, width int) []string {
	t.Helper()
	m := NewMarkdown(plainTheme(), nil)
	m.SetText(src)
	out := m.Render(width)
	lines := make([]string, len(out))
	for i, l := range out {
		lines[i] = strings.TrimRight(stripANSI(l), " ")
	}
	return lines
}

func joinLines(lines []string) string { return strings.Join(lines, "\n") }

// --- lexer -----------------------------------------------------------------

// The invariant the entire streaming path rests on.
func TestLexRawReconstructsSource(t *testing.T) {
	sources := []string{
		"# Heading\n\nA paragraph with **bold** text.\n\n- one\n- two\n\n```go\nfunc main() {}\n```\n",
		"> quoted\n> lines\n\n| a | b |\n| - | - |\n| 1 | 2 |\n",
		"1. first\n2. second\n\n   continued\n\n---\n\ntrailing",
		"no trailing newline",
		"\n\n\nleading blanks\n",
	}
	for _, src := range sources {
		var b strings.Builder
		for _, tok := range Lex(src) {
			b.WriteString(tok.Raw)
		}
		if b.String() != src {
			t.Errorf("raw concatenation != source\n got: %q\nwant: %q", b.String(), src)
		}
	}
}

func TestLexBlockTypes(t *testing.T) {
	tokens := Lex("# H\n\npara\n\n- a\n\n> q\n\n```\ncode\n```\n\n---\n")
	var got []TokenType
	for _, tok := range tokens {
		if tok.Type != TokSpace {
			got = append(got, tok.Type)
		}
	}
	want := []TokenType{TokHeading, TokParagraph, TokList, TokBlockquote, TokCode, TokHr}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token %d = %s, want %s", i, got[i], want[i])
		}
	}
}

func TestLexNestedAndOrderedLists(t *testing.T) {
	tokens := Lex("1. first\n2. second\n    - nested\n    - also\n3. third\n")
	if len(tokens) == 0 || tokens[0].Type != TokList {
		t.Fatalf("expected a list, got %v", tokens)
	}
	list := tokens[0]
	if !list.Ordered {
		t.Error("list should be ordered")
	}
	if len(list.Items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(list.Items))
	}
	// The nested list must hang off item 2, not become a sibling.
	found := false
	for _, tok := range list.Items[1].Tokens {
		if tok.Type == TokList && len(tok.Items) == 2 {
			found = true
		}
	}
	if !found {
		t.Errorf("nested list not attached to item 2: %+v", list.Items[1].Tokens)
	}
}

func TestLexOrderedListRespectsStart(t *testing.T) {
	list := Lex("3. three\n4. four\n")[0]
	if list.Start != 3 {
		t.Errorf("Start = %d, want 3", list.Start)
	}
}

func TestLexTaskList(t *testing.T) {
	list := Lex("- [x] done\n- [ ] todo\n")[0]
	if len(list.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(list.Items))
	}
	if !list.Items[0].Task || !list.Items[0].Checked {
		t.Error("item 1 should be a checked task")
	}
	if !list.Items[1].Task || list.Items[1].Checked {
		t.Error("item 2 should be an unchecked task")
	}
}

// A blank line does not end a list — it makes it loose. Getting this wrong
// renumbers an ordered list from 1 partway down.
func TestLexBlankLineMakesListLooseNotTwoLists(t *testing.T) {
	tokens := Lex("- a\n\n- b\n")
	lists := 0
	for _, tok := range tokens {
		if tok.Type == TokList {
			lists++
			if !tok.Loose {
				t.Error("list should be loose")
			}
			if len(tok.Items) != 2 {
				t.Errorf("expected 2 items in one list, got %d", len(tok.Items))
			}
		}
	}
	if lists != 1 {
		t.Errorf("expected 1 list, got %d", lists)
	}
}

func TestLexTable(t *testing.T) {
	tok := Lex("| Name | Qty |\n| :--- | ---: |\n| pear | 3 |\n")[0]
	if tok.Type != TokTable {
		t.Fatalf("expected a table, got %s", tok.Type)
	}
	if len(tok.Header) != 2 || len(tok.Rows) != 1 {
		t.Fatalf("header %d cols, %d rows", len(tok.Header), len(tok.Rows))
	}
	if tok.Align[0] != AlignLeft || tok.Align[1] != AlignRight {
		t.Errorf("alignments = %v", tok.Align)
	}
}

func TestLexSetextHeading(t *testing.T) {
	tokens := Lex("Title\n=====\n\nbody\n")
	if tokens[0].Type != TokHeading || tokens[0].Depth != 1 || tokens[0].Text != "Title" {
		t.Errorf("setext h1 not recognised: %+v", tokens[0])
	}
}

func TestLexFencedCodeKeepsBlankLinesAndLang(t *testing.T) {
	tok := Lex("```go\na\n\nb\n```\n")[0]
	if tok.Type != TokCode || tok.Lang != "go" {
		t.Fatalf("got type %s lang %q", tok.Type, tok.Lang)
	}
	if tok.Text != "a\n\nb" {
		t.Errorf("code body = %q, want %q", tok.Text, "a\n\nb")
	}
}

// The streaming case: a fence that has not closed yet still lexes as code, so
// the block renders as it arrives instead of appearing all at once.
func TestLexUnclosedFenceIsStillCode(t *testing.T) {
	tok := Lex("```go\nfunc main() {\n")[0]
	if tok.Type != TokCode {
		t.Fatalf("unclosed fence lexed as %s", tok.Type)
	}
}

// --- inline ----------------------------------------------------------------

func inlineTypes(src string) []TokenType {
	var out []TokenType
	for _, tok := range lexInline(src, linkDefs{}) {
		out = append(out, tok.Type)
	}
	return out
}

func TestInlineEmphasis(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		types []TokenType
	}{
		{"bold", "**bold**", []TokenType{TokStrong}},
		{"italic star", "*it*", []TokenType{TokEm}},
		{"italic underscore", "_it_", []TokenType{TokEm}},
		{"code", "`x`", []TokenType{TokCodespan}},
		{"strike", "~~gone~~", []TokenType{TokDel}},
		{"plain", "just text", []TokenType{TokText}},
		// Intraword: * emphasises, _ does not — which is what keeps
		// snake_case_names intact in prose.
		{"intraword star", "a*b*c", []TokenType{TokText, TokEm, TokText}},
		{"intraword underscore", "snake_case_name", []TokenType{TokText}},
		// A lone delimiter is just a character.
		{"unmatched", "2 * 3 * 4", []TokenType{TokText}},
		{"escaped", `\*not emphasis\*`, []TokenType{TokEscape, TokText, TokEscape}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := inlineTypes(c.in)
			if len(got) != len(c.types) {
				t.Fatalf("%q -> %v, want %v", c.in, got, c.types)
			}
			for i := range got {
				if got[i] != c.types[i] {
					t.Errorf("%q token %d = %s, want %s", c.in, i, got[i], c.types[i])
				}
			}
		})
	}
}

func TestInlineNestedEmphasis(t *testing.T) {
	tokens := lexInline("***both***", linkDefs{})
	if len(tokens) != 1 {
		t.Fatalf("expected one span, got %v", inlineTypes("***both***"))
	}
	// Bold wrapping italic, or italic wrapping bold — either nesting is
	// correct, a flat pair of delimiters is not.
	outer := tokens[0]
	if outer.Type != TokStrong && outer.Type != TokEm {
		t.Fatalf("outer = %s", outer.Type)
	}
	if len(outer.Tokens) != 1 || (outer.Tokens[0].Type != TokEm && outer.Tokens[0].Type != TokStrong) {
		t.Errorf("expected nesting, got %+v", outer.Tokens)
	}
}

func TestInlineBoldContainingLoneStar(t *testing.T) {
	tokens := lexInline("**a * b**", linkDefs{})
	if len(tokens) != 1 || tokens[0].Type != TokStrong {
		t.Fatalf("expected one bold span, got %v", inlineTypes("**a * b**"))
	}
}

func TestInlineCodespanHoldsMarkup(t *testing.T) {
	// Markup inside a codespan is literal — a substitution pass gets this
	// wrong and italicises the middle of the code.
	tokens := lexInline("`a*b*c`", linkDefs{})
	if len(tokens) != 1 || tokens[0].Type != TokCodespan || tokens[0].Text != "a*b*c" {
		t.Errorf("codespan = %+v", tokens)
	}
}

func TestInlineLinks(t *testing.T) {
	tokens := lexInline("[text](https://x.dev)", linkDefs{})
	if len(tokens) != 1 || tokens[0].Type != TokLink {
		t.Fatalf("got %v", inlineTypes("[text](https://x.dev)"))
	}
	if tokens[0].Href != "https://x.dev" || tokens[0].Text != "text" {
		t.Errorf("link = %+v", tokens[0])
	}
}

func TestInlineLinkWithTitle(t *testing.T) {
	tok := lexInline(`[t](https://x.dev "Title")`, linkDefs{})[0]
	if tok.Href != "https://x.dev" || tok.Title != "Title" {
		t.Errorf("href %q title %q", tok.Href, tok.Title)
	}
}

func TestInlineReferenceLink(t *testing.T) {
	defs := newLinkDefs("[ref]: https://x.dev\n")
	tok := lexInline("see [text][ref] here", defs)
	found := false
	for _, tk := range tok {
		if tk.Type == TokLink && tk.Href == "https://x.dev" {
			found = true
		}
	}
	if !found {
		t.Errorf("reference link unresolved: %+v", tok)
	}
}

func TestInlineAutolinkAndBareURL(t *testing.T) {
	if types := inlineTypes("<https://x.dev>"); len(types) != 1 || types[0] != TokLink {
		t.Errorf("autolink -> %v", types)
	}
	tokens := lexInline("go to https://x.dev now", linkDefs{})
	found := false
	for _, tk := range tokens {
		if tk.Type == TokLink && tk.Href == "https://x.dev" {
			found = true
		}
	}
	if !found {
		t.Errorf("bare URL not linked: %+v", tokens)
	}
}

// --- rendering -------------------------------------------------------------

func TestRenderNeverExceedsWidth(t *testing.T) {
	src := "# A heading that is quite long indeed\n\n" +
		"A paragraph with **bold** and `code` and a [link](https://example.com/very/long/path) in it.\n\n" +
		"- a list item long enough to need wrapping at narrow widths\n" +
		"  - and a nested one that is also fairly long\n\n" +
		"> a blockquote that also runs past the edge of a narrow terminal\n\n" +
		"| column one | column two |\n| --- | --- |\n| a value | another value |\n"
	for _, width := range []int{20, 40, 80} {
		for _, line := range render(t, src, width) {
			if w := visibleWidth(line); w > width {
				t.Errorf("width %d: %d-cell line %q", width, w, line)
			}
		}
	}
}

func TestRenderHeadingsAndParagraphs(t *testing.T) {
	got := joinLines(render(t, "# Title\n\nBody text.\n", 40))
	if !strings.Contains(got, "Title") || !strings.Contains(got, "Body text.") {
		t.Errorf("render = %q", got)
	}
	// A level-3 heading keeps its hashes; a level-1 does not.
	deep := joinLines(render(t, "### Deep\n", 40))
	if !strings.Contains(deep, "### Deep") {
		t.Errorf("h3 lost its marker: %q", deep)
	}
}

func TestRenderList(t *testing.T) {
	got := render(t, "- one\n- two\n", 40)
	if !strings.Contains(got[0], "- one") || !strings.Contains(got[1], "- two") {
		t.Errorf("list = %q", got)
	}
}

func TestRenderOrderedListNumbers(t *testing.T) {
	got := joinLines(render(t, "1. one\n2. two\n3. three\n", 40))
	for _, want := range []string{"1. one", "2. two", "3. three"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in %q", want, got)
		}
	}
}

func TestRenderNestedListIndents(t *testing.T) {
	got := render(t, "- outer\n    - inner\n", 40)
	if len(got) < 2 {
		t.Fatalf("got %q", got)
	}
	outer := len(got[0]) - len(strings.TrimLeft(got[0], " "))
	inner := len(got[1]) - len(strings.TrimLeft(got[1], " "))
	if inner <= outer {
		t.Errorf("nested item not indented: %q", got)
	}
}

func TestRenderCodeBlockFencesAndBody(t *testing.T) {
	got := render(t, "```go\nfunc main() {}\n```\n", 40)
	if !strings.HasPrefix(got[0], "```go") {
		t.Errorf("missing opening fence: %q", got)
	}
	body := joinLines(got)
	if !strings.Contains(body, "func main() {}") {
		t.Errorf("missing code body: %q", body)
	}
}

func TestRenderTableHasBorders(t *testing.T) {
	got := joinLines(render(t, "| a | b |\n| --- | --- |\n| 1 | 2 |\n", 40))
	for _, want := range []string{"┌", "├", "└", "│"} {
		if !strings.Contains(got, want) {
			t.Errorf("table missing %q:\n%s", want, got)
		}
	}
}

// A table too narrow to draw falls back to its source rather than shredding.
func TestRenderTableFallsBackWhenTooNarrow(t *testing.T) {
	for _, line := range render(t, "| a | b | c | d |\n| - | - | - | - |\n| 1 | 2 | 3 | 4 |\n", 10) {
		if w := visibleWidth(line); w > 10 {
			t.Errorf("narrow table line is %d cells: %q", w, line)
		}
	}
}

func TestRenderBlockquoteHasBorder(t *testing.T) {
	got := render(t, "> quoted\n", 40)
	if !strings.HasPrefix(got[0], "│ ") {
		t.Errorf("blockquote border missing: %q", got)
	}
}

func TestRenderHr(t *testing.T) {
	if got := render(t, "---\n", 40); !strings.Contains(got[0], "───") {
		t.Errorf("hr = %q", got)
	}
}

func TestRenderLinkShowsURLWithoutHyperlinks(t *testing.T) {
	// With OSC 8 unavailable the URL has to ride along visibly, or the
	// address is simply lost.
	m := NewMarkdown(plainTheme(), nil)
	m.Hyperlinks = false
	m.SetText("[text](https://x.dev)\n")
	got := stripANSI(joinLines(m.Render(60)))
	if !strings.Contains(got, "https://x.dev") {
		t.Errorf("URL dropped: %q", got)
	}
}

func TestRenderLinkUsesOSC8WhenAvailable(t *testing.T) {
	// With OSC 8 the URL is the link's target, not visible text — printing
	// it inline as well would duplicate it on every link.
	m := NewMarkdown(plainTheme(), nil)
	m.Hyperlinks = true
	m.SetText("[text](https://x.dev)\n")
	out := joinLines(m.Render(60))
	if !strings.Contains(out, "\x1b]8;;https://x.dev") {
		t.Errorf("no OSC 8 sequence: %q", out)
	}
	if strings.Contains(stripANSI(out), "(https://x.dev)") {
		t.Errorf("URL duplicated as visible text: %q", stripANSI(out))
	}
}

// An anchor has no clickable target in a terminal and no viewport to scroll,
// so it must render as styled text rather than a dead link.
func TestRenderAnchorLinkIsNotClickable(t *testing.T) {
	m := NewMarkdown(plainTheme(), nil)
	m.Hyperlinks = true
	m.SetText("[jump](#section)\n")
	out := joinLines(m.Render(60))
	if strings.Contains(out, "\x1b]8;;") {
		t.Errorf("anchor became a hyperlink: %q", out)
	}
	if !strings.Contains(stripANSI(out), "jump") {
		t.Errorf("anchor text lost: %q", out)
	}
}

func TestRenderEmptyDocument(t *testing.T) {
	if got := render(t, "   \n\n", 40); len(got) != 0 {
		t.Errorf("expected no lines, got %q", got)
	}
}

// --- streaming -------------------------------------------------------------

// Rendering a document one delta at a time must land on exactly what
// rendering it whole produces.
func TestStreamingMatchesWholeRender(t *testing.T) {
	src := "# Title\n\nFirst paragraph with **bold**.\n\n" +
		"```go\nfunc main() {\n\tprintln(\"hi\")\n}\n```\n\n" +
		"- item one\n- item two\n\nLast paragraph.\n"

	whole := NewMarkdown(plainTheme(), nil)
	whole.SetText(src)
	want := whole.Render(60)

	streamed := NewMarkdown(plainTheme(), nil)
	streamed.SetStreaming(true)
	for i := 1; i <= len(src); i += 7 {
		streamed.SetText(src[:min(i, len(src))])
		streamed.Render(60)
	}
	streamed.SetText(src)
	streamed.SetStreaming(false)
	got := streamed.Render(60)

	if len(got) != len(want) {
		t.Fatalf("streamed %d lines, whole %d\n--- streamed ---\n%s\n--- whole ---\n%s",
			len(got), len(want), joinLines(got), joinLines(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// The freeze must never cut across a list: "- a\n\n- b" is one loose list,
// and re-lexing its tail alone would renumber an ordered one from 1.
func TestStreamingDoesNotFreezeAcrossList(t *testing.T) {
	src := "intro\n\n1. one\n\n2. two\n\n3. three\n"
	m := NewMarkdown(plainTheme(), nil)
	m.SetStreaming(true)
	for i := 1; i <= len(src); i++ {
		m.SetText(src[:i])
		m.Render(60)
	}
	got := joinLines(m.Render(60))
	for _, want := range []string{"1. one", "2. two", "3. three"} {
		if !strings.Contains(got, want) {
			t.Errorf("streamed list lost %q:\n%s", want, got)
		}
	}
}

// A code block must not shrink and flicker as its closing fence arrives one
// backtick at a time.
func TestStreamingPartialClosingFenceDoesNotShrinkBlock(t *testing.T) {
	m := NewMarkdown(plainTheme(), nil)
	m.SetStreaming(true)
	m.SetText("```go\nline one\nline two\n")
	full := len(m.Render(60))
	for _, partial := range []string{"`", "``"} {
		m.SetText("```go\nline one\nline two\n" + partial)
		if got := len(m.Render(60)); got < full {
			t.Errorf("block shrank to %d lines (from %d) at partial fence %q", got, full, partial)
		}
	}
}

func TestFreezePointOnlySealsSettledBlocks(t *testing.T) {
	// A paragraph closed by a blank line is sealed; a trailing list is not.
	if got := freezePoint(Lex("para\n\nmore\n\nstill\n")); got < 0 {
		t.Error("a closed paragraph should be freezable")
	}
	if got := freezePoint(Lex("- a\n\n- b\n")); got >= 0 {
		t.Errorf("a list must never be frozen across, got index %d", got)
	}
}

func TestStreamingHeadIsDroppedOnRewrite(t *testing.T) {
	// If the text stops extending what was frozen, the head must be thrown
	// away rather than prefixed onto unrelated content.
	m := NewMarkdown(plainTheme(), nil)
	m.SetStreaming(true)
	m.SetText("alpha\n\nbeta\n\ngamma\n")
	m.Render(60)
	m.SetText("totally different\n\ncontent here\n\nagain\n")
	got := joinLines(m.Render(60))
	if strings.Contains(got, "alpha") {
		t.Errorf("stale frozen head survived a rewrite:\n%s", got)
	}
}

// A tight list followed by a blank line and a list of the OTHER family must
// stay tight — the trailing bullet ends it rather than extending it.
func TestLexTightListFollowedByOtherListStaysTight(t *testing.T) {
	tokens := Lex("- a\n- b\n\n1. one\n2. two\n")
	var lists []Token
	for _, tok := range tokens {
		if tok.Type == TokList {
			lists = append(lists, tok)
		}
	}
	if len(lists) != 2 {
		t.Fatalf("expected 2 lists, got %d", len(lists))
	}
	for i, l := range lists {
		if l.Loose {
			t.Errorf("list %d should be tight", i)
		}
	}
}

func TestRenderTightListHasNoBlankLines(t *testing.T) {
	got := render(t, "- a\n- b\n- c\n", 40)
	for _, line := range got {
		if strings.TrimSpace(line) == "" {
			t.Errorf("tight list rendered with a blank line:\n%q", got)
		}
	}
}

func TestRenderLooseListHasBlankLines(t *testing.T) {
	got := render(t, "- a\n\n- b\n", 40)
	blanks := 0
	for _, line := range got {
		if strings.TrimSpace(line) == "" {
			blanks++
		}
	}
	if blanks == 0 {
		t.Errorf("loose list rendered tight:\n%q", got)
	}
}

// The blank line separating two lists belongs to the document, not to the
// first list — swallowing it drops the gap the author wrote.
func TestLexBlankBetweenListsBecomesSpace(t *testing.T) {
	tokens := Lex("- a\n- b\n\n1. one\n")
	var types []TokenType
	for _, tok := range tokens {
		types = append(types, tok.Type)
	}
	want := []TokenType{TokList, TokSpace, TokList}
	if len(types) != len(want) {
		t.Fatalf("got %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("token %d = %s, want %s", i, types[i], want[i])
		}
	}
}

func TestRenderGapBetweenListsSurvives(t *testing.T) {
	got := render(t, "- a\n- b\n\n1. one\n2. two\n", 40)
	blanks := 0
	for _, line := range got {
		if strings.TrimSpace(line) == "" {
			blanks++
		}
	}
	if blanks != 1 {
		t.Errorf("expected exactly one gap between the lists, got %d:\n%q", blanks, got)
	}
}

// Two blank lines end a list; both belong to the document afterwards.
func TestLexTwoBlanksEndList(t *testing.T) {
	tokens := Lex("- a\n\n\nparagraph\n")
	if tokens[0].Type != TokList || len(tokens[0].Items) != 1 {
		t.Fatalf("expected a one-item list, got %+v", tokens[0])
	}
	if tokens[len(tokens)-1].Type != TokParagraph {
		t.Errorf("expected a trailing paragraph, got %s", tokens[len(tokens)-1].Type)
	}
}
