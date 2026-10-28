package tui

import (
	"strings"
)

// The markdown renderer, ported from loop's packages/tui/src/components/markdown.ts.

// MarkdownTheme is one style function per markdown element. The renderer
// never names a colour itself, so a palette swap moves the whole document.
type MarkdownTheme struct {
	Heading         func(string) string
	Link            func(string) string
	LinkURL         func(string) string
	Code            func(string) string
	CodeBlock       func(string) string
	CodeBlockBorder func(string) string
	Quote           func(string) string
	QuoteBorder     func(string) string
	Hr              func(string) string
	ListBullet      func(string) string
	Bold            func(string) string
	Italic          func(string) string
	Strikethrough   func(string) string
	Underline       func(string) string
	// HighlightCode colours a code block's body. Nil renders it flat.
	HighlightCode func(code, lang string) []string
	// CodeBlockIndent prefixes every rendered code line (default "  ").
	CodeBlockIndent string
}

// MarkdownThemeFor derives the element styles from a theme's slots.
func MarkdownThemeFor(t *Theme) MarkdownTheme {
	slot := func(s Slot) func(string) string {
		return func(text string) string { return t.Fg(s, text) }
	}
	return MarkdownTheme{
		Heading:         slot(SlotMdHeading),
		Link:            slot(SlotMdLink),
		LinkURL:         slot(SlotMdLinkURL),
		Code:            slot(SlotMdCode),
		CodeBlock:       slot(SlotMdCodeBlock),
		CodeBlockBorder: slot(SlotMdCodeBlockBorder),
		Quote:           slot(SlotMdQuote),
		QuoteBorder:     slot(SlotMdQuoteBorder),
		Hr:              slot(SlotMdHr),
		ListBullet:      slot(SlotMdListBullet),
		Bold:            t.Bold,
		Italic:          t.Italic,
		Strikethrough:   t.Strike,
		Underline:       t.Underline,
		HighlightCode:   func(code, lang string) []string { return Highlight(t, code, lang) },
	}
}

// DefaultTextStyle is the base styling under everything markdown does not
// style itself.
type DefaultTextStyle struct {
	Color         func(string) string
	BgColor       func(string) string
	Bold          bool
	Italic        bool
	Strikethrough bool
	Underline     bool
}

// inlineStyle is the styling an inline token must restore after its own
// reset. A codespan closes with its own escape; without re-opening the
// surrounding style, the rest of a heading would fall back to body text
// halfway through the line.
type inlineStyle struct {
	apply  func(string) string
	prefix string
}

// stable is the settled head of a streaming document: src renders to exactly
// lines, and nothing arriving later can change that.
type stable struct {
	width int
	src   string
	lines []string
}

// Markdown renders a markdown document to styled terminal lines.
type Markdown struct {
	text     string
	PaddingX int
	PaddingY int
	Theme    MarkdownTheme
	Default  *DefaultTextStyle
	// Hyperlinks renders links as clickable OSC 8 instead of printing the
	// URL inline. Set from the terminal probe, injectable for tests.
	Hyperlinks bool

	cachedText  string
	cachedWidth int
	cachedLines []string
	hasCache    bool

	// streaming means the text is still being appended to. The incremental
	// head is only built on the way in; on the way out it is thrown away and
	// the finished text is rendered once, whole — that final pass is what
	// resolves a link definition arriving after the link that uses it.
	streaming bool
	head      *stable

	defaultPrefix string
	hasPrefix     bool
}

// NewMarkdown builds a renderer for a theme.
func NewMarkdown(theme MarkdownTheme, def *DefaultTextStyle) *Markdown {
	return &Markdown{Theme: theme, Default: def, Hyperlinks: TerminalCaps().Hyperlinks}
}

// SetText replaces the document.
func (m *Markdown) SetText(text string) {
	m.text = text
	m.hasCache = false
}

// Text is the current source.
func (m *Markdown) Text() string { return m.text }

// SetStreaming toggles incremental rendering.
func (m *Markdown) SetStreaming(streaming bool) {
	if m.streaming == streaming {
		return
	}
	m.streaming = streaming
	m.head = nil
	if !streaming {
		m.Invalidate()
	}
}

// Invalidate drops every cache, forcing a full re-render.
func (m *Markdown) Invalidate() {
	m.hasCache = false
	m.head = nil
}

// Render lays the document out to width cells.
func (m *Markdown) Render(width int) []string {
	if m.hasCache && m.cachedText == m.text && m.cachedWidth == width {
		return m.cachedLines
	}
	contentWidth := max(1, width-m.PaddingX*2)

	if strings.TrimSpace(m.text) == "" {
		m.cachedText, m.cachedWidth, m.cachedLines, m.hasCache = m.text, width, nil, true
		return nil
	}

	normalized := strings.ReplaceAll(m.text, "\t", "   ")

	// Streaming: everything up to the settled head was rendered on an earlier
	// delta and cannot have changed, so only the tail is lexed. Without this
	// every delta re-lexes the whole message, and past a few tens of
	// kilobytes a single frame no longer fits in the render budget.
	var head *stable
	if m.head != nil && m.head.width == width && strings.HasPrefix(normalized, m.head.src) {
		head = m.head
	}
	tailText := normalized
	if head != nil {
		tailText = normalized[len(head.src):]
	}

	tokens := Lex(tailText)
	trimPartialClosingFences(tokens)

	leftMargin := strings.Repeat(" ", m.PaddingX)
	rightMargin := leftMargin
	bg := (*DefaultTextStyle)(nil)
	if m.Default != nil && m.Default.BgColor != nil {
		bg = m.Default
	}

	// Wrapping and margins happen as we go so each token's span of finished
	// lines is known — that span is what the streaming head is cut from.
	var tailLines []string
	tokenEnds := make([]int, 0, len(tokens))
	for i := range tokens {
		var nextType TokenType
		if i+1 < len(tokens) {
			nextType = tokens[i+1].Type
		}
		for _, tokenLine := range m.renderToken(&tokens[i], contentWidth, nextType, nil) {
			for _, wrapped := range wrapTextWithAnsi(tokenLine, contentWidth) {
				line := leftMargin + wrapped + rightMargin
				if bg != nil {
					tailLines = append(tailLines, applyBackgroundToLine(line, width, bg.BgColor))
				} else {
					tailLines = append(tailLines, padToWidth(line, width))
				}
			}
		}
		tokenEnds = append(tokenEnds, len(tailLines))
	}

	if m.streaming {
		m.advanceStable(head, tokens, tokenEnds, tailLines, tailText, width)
	}

	content := tailLines
	if head != nil {
		content = append(append([]string{}, head.lines...), tailLines...)
	}

	var out []string
	empty := strings.Repeat(" ", width)
	if bg != nil {
		empty = applyBackgroundToLine(empty, width, bg.BgColor)
	}
	for i := 0; i < m.PaddingY; i++ {
		out = append(out, empty)
	}
	out = append(out, content...)
	for i := 0; i < m.PaddingY; i++ {
		out = append(out, empty)
	}

	m.cachedText, m.cachedWidth, m.cachedLines, m.hasCache = m.text, width, out, true
	return out
}

// advanceStable pushes the settled head forward over whatever this render
// sealed.
//
// The frozen source is rebuilt from the tokens' own Raw and checked back
// against the text before it is trusted: if the lexer ever stops
// reconstructing the source exactly, the head is dropped and the next render
// lexes the whole document — the old cost, never a wrong frame.
func (m *Markdown) advanceStable(head *stable, tokens []Token, tokenEnds []int, tailLines []string, tailText string, width int) {
	last := freezePoint(tokens)
	if last < 0 {
		return
	}
	var frozen strings.Builder
	for i := 0; i <= last; i++ {
		frozen.WriteString(tokens[i].Raw)
	}
	if !strings.HasPrefix(tailText, frozen.String()) {
		m.head = nil
		return
	}
	next := &stable{width: width}
	if head != nil {
		next.src = head.src
		next.lines = append(next.lines, head.lines...)
	}
	next.src += frozen.String()
	next.lines = append(next.lines, tailLines[:tokenEnds[last]]...)
	m.head = next
}

// sealedBlockTypes are the blocks a following blank line SEALS: once one is
// closed by a blank line, no amount of text arriving after it can change how
// it lexed.
//
// `list` is missing on purpose. A blank line does not end a list — "- a\n\n-
// b" is ONE loose list, not two — so freezing across a trailing list would
// re-lex "- b" as a fresh list and renumber an ordered one from 1. Indented
// code, html and defs are left out for the same class of reason.
var sealedBlockTypes = map[TokenType]bool{
	TokParagraph:  true,
	TokHeading:    true,
	TokHr:         true,
	TokBlockquote: true,
	TokTable:      true,
}

// isClosedFencedCode reports a fenced block whose closing fence has already
// arrived — sealed, unlike an indented code block, which a later indented
// line extends.
func isClosedFencedCode(t *Token) bool {
	if t.Type != TokCode {
		return false
	}
	raw := strings.TrimRight(t.Raw, " \t\r\n")
	marker := fenceMarker(raw)
	if marker == "" {
		return false // indented code block
	}
	lines := strings.Split(raw, "\n")
	last := lines[len(lines)-1]
	return len(raw) > len(marker) && strings.HasPrefix(strings.TrimLeft(last, " "), marker)
}

// fenceMarker is the run of backticks or tildes a fenced block opens with.
func fenceMarker(raw string) string {
	trimmed := strings.TrimLeft(raw, " ")
	if !strings.HasPrefix(trimmed, "```") && !strings.HasPrefix(trimmed, "~~~") {
		return ""
	}
	c := trimmed[0]
	n := 0
	for n < len(trimmed) && trimmed[n] == c {
		n++
	}
	return trimmed[:n]
}

// freezePoint is the index of the last token that may be frozen, or -1.
//
// Only blank-line boundaries qualify, and only when the block in front of the
// blank line is sealed. The final two tokens are never frozen: the last is
// still growing, and a token's rendering is handed the NEXT token's type, so
// the second-to-last one's output is not settled either.
func freezePoint(tokens []Token) int {
	for i := len(tokens) - 3; i >= 1; i-- {
		if tokens[i].Type != TokSpace {
			continue
		}
		prev := &tokens[i-1]
		if sealedBlockTypes[prev.Type] || isClosedFencedCode(prev) {
			return i
		}
	}
	return -1
}

// trimPartialClosingFences drops a half-arrived closing fence so a streaming
// code block does not shrink and flicker when its last backtick lands.
func trimPartialClosingFences(tokens []Token) {
	if len(tokens) == 0 {
		return
	}
	t := &tokens[len(tokens)-1]
	switch t.Type {
	case TokList:
		if len(t.Items) > 0 {
			trimPartialClosingFences(t.Items[len(t.Items)-1].Tokens)
		}
		return
	case TokBlockquote:
		trimPartialClosingFences(t.Tokens)
		return
	case TokCode:
	default:
		return
	}

	marker := fenceMarker(t.Raw)
	if marker == "" {
		return
	}
	lines := strings.Split(t.Raw, "\n")
	last := lines[len(lines)-1]
	if last == "" || len(last) >= len(marker) || strings.Trim(last, string(marker[0])) != "" {
		return
	}
	t.Text = strings.TrimSuffix(strings.TrimSuffix(t.Text, last), "\n")
}

// applyDefaultStyle is the base styling applied to all text content. The
// background is deliberately NOT applied here — it goes on at the padding
// stage so it reaches the full line width.
func (m *Markdown) applyDefaultStyle(text string) string {
	if m.Default == nil {
		return text
	}
	styled := text
	if m.Default.Color != nil {
		styled = m.Default.Color(styled)
	}
	if m.Default.Bold {
		styled = m.Theme.Bold(styled)
	}
	if m.Default.Italic {
		styled = m.Theme.Italic(styled)
	}
	if m.Default.Strikethrough {
		styled = m.Theme.Strikethrough(styled)
	}
	if m.Default.Underline {
		styled = m.Theme.Underline(styled)
	}
	return styled
}

func (m *Markdown) defaultStylePrefix() string {
	if m.Default == nil {
		return ""
	}
	if !m.hasPrefix {
		m.defaultPrefix = StylePrefix(m.applyDefaultStyle)
		m.hasPrefix = true
	}
	return m.defaultPrefix
}

func (m *Markdown) defaultInlineStyle() inlineStyle {
	return inlineStyle{apply: m.applyDefaultStyle, prefix: m.defaultStylePrefix()}
}

func (m *Markdown) renderToken(token *Token, width int, nextType TokenType, sc *inlineStyle) []string {
	var lines []string
	// A blank line after a block, unless a space token is already coming —
	// otherwise the gap doubles.
	spaceAfter := func() {
		if nextType != "" && nextType != TokSpace {
			lines = append(lines, "")
		}
	}

	switch token.Type {
	case TokHeading:
		var style func(string) string
		if token.Depth == 1 {
			style = func(t string) string { return m.Theme.Heading(m.Theme.Bold(m.Theme.Underline(t))) }
		} else {
			style = func(t string) string { return m.Theme.Heading(m.Theme.Bold(t)) }
		}
		hs := inlineStyle{apply: style, prefix: StylePrefix(style)}
		text := m.renderInlineTokens(token.Tokens, &hs)
		if token.Depth >= 3 {
			text = style(strings.Repeat("#", token.Depth)+" ") + text
		}
		lines = append(lines, text)
		spaceAfter()

	case TokParagraph:
		lines = append(lines, m.renderInlineTokens(token.Tokens, sc))
		if nextType != "" && nextType != TokList && nextType != TokSpace {
			lines = append(lines, "")
		}

	case TokText:
		lines = append(lines, m.renderInlineTokens([]Token{*token}, sc))

	case TokCode:
		indent := m.Theme.CodeBlockIndent
		if indent == "" {
			indent = "  "
		}
		lines = append(lines, m.Theme.CodeBlockBorder("```"+token.Lang))
		if m.Theme.HighlightCode != nil {
			for _, l := range m.Theme.HighlightCode(token.Text, token.Lang) {
				lines = append(lines, indent+l)
			}
		} else {
			for _, l := range strings.Split(token.Text, "\n") {
				lines = append(lines, indent+m.Theme.CodeBlock(l))
			}
		}
		lines = append(lines, m.Theme.CodeBlockBorder("```"))
		spaceAfter()

	case TokList:
		lines = append(lines, m.renderList(token, 0, width, sc)...)

	case TokTable:
		lines = append(lines, m.renderTable(token, width, nextType, sc)...)

	case TokBlockquote:
		lines = append(lines, m.renderBlockquote(token, width, nextType)...)

	case TokHr:
		lines = append(lines, m.Theme.Hr(strings.Repeat("─", min(width, 80))))
		spaceAfter()

	case TokHTML:
		lines = append(lines, m.applyDefaultStyle(strings.TrimSpace(token.Raw)))

	case TokSpace:
		lines = append(lines, "")

	case TokDef:
		// Link definitions are metadata, not content: they resolve the links
		// that reference them and render as nothing.

	default:
		if token.Text != "" {
			lines = append(lines, token.Text)
		}
	}
	return lines
}

func (m *Markdown) renderBlockquote(token *Token, width int, nextType TokenType) []string {
	style := func(t string) string { return m.Theme.Quote(m.Theme.Italic(t)) }
	prefix := StylePrefix(style)
	apply := func(line string) string {
		if prefix == "" {
			return style(line)
		}
		// Re-open the quote style after every nested reset, or an inline
		// codespan would leave the rest of the quote unstyled.
		return style(strings.ReplaceAll(line, "\x1b[0m", "\x1b[0m"+prefix))
	}

	contentWidth := max(1, width-2)
	inner := inlineStyle{apply: func(t string) string { return t }, prefix: prefix}

	var rendered []string
	for i := range token.Tokens {
		var next TokenType
		if i+1 < len(token.Tokens) {
			next = token.Tokens[i+1].Type
		}
		rendered = append(rendered, m.renderToken(&token.Tokens[i], contentWidth, next, &inner)...)
	}
	for len(rendered) > 0 && rendered[len(rendered)-1] == "" {
		rendered = rendered[:len(rendered)-1]
	}

	var lines []string
	for _, l := range rendered {
		for _, wrapped := range wrapTextWithAnsi(apply(l), contentWidth) {
			lines = append(lines, m.Theme.QuoteBorder("│ ")+wrapped)
		}
	}
	if nextType != "" && nextType != TokSpace {
		lines = append(lines, "")
	}
	return lines
}

func (m *Markdown) renderInlineTokens(tokens []Token, sc *inlineStyle) string {
	style := m.defaultInlineStyle()
	if sc != nil {
		style = *sc
	}
	applyLines := func(text string) string {
		parts := strings.Split(text, "\n")
		for i := range parts {
			parts[i] = style.apply(parts[i])
		}
		return strings.Join(parts, "\n")
	}

	var b strings.Builder
	for i := range tokens {
		token := &tokens[i]
		switch token.Type {
		case TokEscape:
			b.WriteString(applyLines(token.Text))

		case TokText:
			if len(token.Tokens) > 0 {
				b.WriteString(m.renderInlineTokens(token.Tokens, &style))
			} else {
				b.WriteString(applyLines(token.Text))
			}

		case TokParagraph:
			b.WriteString(m.renderInlineTokens(token.Tokens, &style))

		case TokStrong:
			b.WriteString(m.Theme.Bold(m.renderInlineTokens(token.Tokens, &style)) + style.prefix)

		case TokEm:
			b.WriteString(m.Theme.Italic(m.renderInlineTokens(token.Tokens, &style)) + style.prefix)

		case TokDel:
			b.WriteString(m.Theme.Strikethrough(m.renderInlineTokens(token.Tokens, &style)) + style.prefix)

		case TokCodespan:
			b.WriteString(m.Theme.Code(token.Text) + style.prefix)

		case TokLink:
			b.WriteString(m.renderLink(token, &style) + style.prefix)

		case TokImage:
			// No inline images in a text terminal: the alt text is the
			// content, and the URL rides along the way a link's does.
			b.WriteString(m.Theme.Link(token.Text) + m.Theme.LinkURL(" ("+token.Href+")") + style.prefix)

		case TokBr:
			b.WriteString("\n")

		case TokHTML:
			b.WriteString(applyLines(token.Raw))

		default:
			if token.Text != "" {
				b.WriteString(applyLines(token.Text))
			}
		}
	}

	out := b.String()
	for style.prefix != "" && strings.HasSuffix(out, style.prefix) {
		out = out[:len(out)-len(style.prefix)]
	}
	return out
}

func (m *Markdown) renderLink(token *Token, style *inlineStyle) string {
	text := m.renderInlineTokens(token.Tokens, style)
	styled := m.Theme.Link(m.Theme.Underline(text))

	// An internal anchor has no clickable target in a terminal — the emulator
	// would try to open the fragment as a URL, and there is no app-owned
	// viewport to scroll. Style only.
	if strings.HasPrefix(token.Href, "#") {
		return styled
	}
	if m.Hyperlinks {
		return Hyperlink(styled, token.Href)
	}
	href := strings.TrimPrefix(token.Href, "mailto:")
	if token.Text == token.Href || token.Text == href {
		return styled
	}
	return styled + m.Theme.LinkURL(" ("+token.Href+")")
}

func (m *Markdown) renderList(token *Token, depth, width int, sc *inlineStyle) []string {
	var lines []string
	indent := strings.Repeat("    ", depth)
	start := token.Start
	if start == 0 {
		start = 1
	}

	for i := range token.Items {
		item := &token.Items[i]
		bullet := "- "
		if token.Ordered {
			bullet = itoa(start+i) + ". "
		}
		if item.Task {
			if item.Checked {
				bullet += "[x] "
			} else {
				bullet += "[ ] "
			}
		}
		firstPrefix := indent + m.Theme.ListBullet(bullet)
		continuation := indent + strings.Repeat(" ", visibleWidth(bullet))
		itemWidth := max(1, width-visibleWidth(firstPrefix))
		rendered := false

		for t := range item.Tokens {
			child := &item.Tokens[t]
			if child.Type == TokList {
				lines = append(lines, m.renderList(child, depth+1, width, sc)...)
				rendered = true
				continue
			}
			for _, line := range m.renderToken(child, itemWidth, "", sc) {
				for _, wrapped := range wrapTextWithAnsi(line, itemWidth) {
					prefix := firstPrefix
					if rendered {
						prefix = continuation
					}
					lines = append(lines, prefix+wrapped)
					rendered = true
				}
			}
		}
		if !rendered {
			lines = append(lines, firstPrefix)
		}
		if token.Loose && i < len(token.Items)-1 {
			lines = append(lines, "")
		}
	}
	return lines
}

// itoa is strconv.Itoa without the import churn in this file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
