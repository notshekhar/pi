package tui

import (
	"regexp"
	"strconv"
	"strings"
)

// The block lexer. Line-oriented: every rule consumes a run of whole lines,
// and a token's Raw is exactly the lines it consumed, so the concatenation
// invariant in markdown_token.go holds by construction.

var (
	reATXHeading  = regexp.MustCompile(`^ {0,3}(#{1,6})(?:[ \t]+(.*?))?(?:[ \t]+#+)?[ \t]*$`)
	reFence       = regexp.MustCompile("^ {0,3}(`{3,}|~{3,})[ \t]*(.*?)[ \t]*$")
	reHr          = regexp.MustCompile(`^ {0,3}(?:(?:\*[ \t]*){3,}|(?:-[ \t]*){3,}|(?:_[ \t]*){3,})$`)
	reBullet      = regexp.MustCompile(`^( {0,3})([-+*]|\d{1,9}[.)])([ \t]+|$)`)
	reSetext      = regexp.MustCompile(`^ {0,3}(=+|-+)[ \t]*$`)
	reTableDelim  = regexp.MustCompile(`^ {0,3}\|?[ \t]*:?-+:?[ \t]*(\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`)
	reTaskMarker  = regexp.MustCompile(`^\[([ xX])\][ \t]+`)
	reHTMLBlock   = regexp.MustCompile(`^ {0,3}<(?:[A-Za-z][A-Za-z0-9-]*|/[A-Za-z][A-Za-z0-9-]*|!--)`)
	reLinkDefLine = regexp.MustCompile(`^ {0,3}\[([^\]]+)\]:[ \t]*(\S+)(?:[ \t]+["'(](.*)["')])?[ \t]*$`)
)

// Lex turns markdown source into top-level block tokens.
func Lex(src string) []Token {
	return lexBlocks(splitLinesKeepEnds(src), newLinkDefs(src))
}

// splitLinesKeepEnds splits on newlines, keeping each "\n" attached to the
// line it terminates. A trailing partial line (the streaming case) is its own
// element, so nothing is lost or invented.
func splitLinesKeepEnds(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// trimEnd strips a line's trailing newline for content matching.
func trimEnd(line string) string { return strings.TrimRight(line, "\r\n") }

// linkDefs holds the document's `[label]: url` definitions. Reference links
// resolve against it, which is the one thing a purely incremental lexer
// cannot see — hence the whole-document pass when a stream finishes.
type linkDefs map[string]Token

func newLinkDefs(src string) linkDefs {
	defs := linkDefs{}
	for _, line := range strings.Split(src, "\n") {
		if m := reLinkDefLine.FindStringSubmatch(line); m != nil {
			defs[strings.ToLower(strings.TrimSpace(m[1]))] = Token{Href: m[2], Title: m[3]}
		}
	}
	return defs
}

func lexBlocks(lines []string, defs linkDefs) []Token {
	var out []Token
	i := 0
	for i < len(lines) {
		tok, next := lexBlock(lines, i, defs)
		if next == i {
			// A rule that consumes nothing would spin forever; treat the
			// line as text and move on.
			next = i + 1
			tok = Token{Type: TokParagraph, Raw: lines[i], Text: trimEnd(lines[i])}
			tok.Tokens = lexInline(tok.Text, defs)
		}
		out = append(out, tok)
		i = next
	}
	return out
}

// lexBlock matches one block starting at lines[i], returning it and the index
// after it.
func lexBlock(lines []string, i int, defs linkDefs) (Token, int) {
	line := trimEnd(lines[i])

	if isBlank(line) {
		j := i
		for j < len(lines) && isBlank(trimEnd(lines[j])) {
			j++
		}
		return Token{Type: TokSpace, Raw: join(lines, i, j)}, j
	}
	if m := reFence.FindStringSubmatch(line); m != nil {
		return lexFence(lines, i, m)
	}
	if m := reATXHeading.FindStringSubmatch(line); m != nil {
		tok := Token{Type: TokHeading, Raw: lines[i], Depth: len(m[1]), Text: m[2]}
		tok.Tokens = lexInline(tok.Text, defs)
		return tok, i + 1
	}
	if reHr.MatchString(line) {
		return Token{Type: TokHr, Raw: lines[i]}, i + 1
	}
	if strings.HasPrefix(strings.TrimLeft(line, " "), ">") && indentOf(line) < 4 {
		return lexBlockquote(lines, i, defs)
	}
	if reBullet.MatchString(line) && !reHr.MatchString(line) {
		return lexList(lines, i, defs)
	}
	if tok, next, ok := lexTable(lines, i, defs); ok {
		return tok, next
	}
	if m := reLinkDefLine.FindStringSubmatch(line); m != nil {
		return Token{Type: TokDef, Raw: lines[i], Text: m[1], Href: m[2], Title: m[3]}, i + 1
	}
	if reHTMLBlock.MatchString(line) {
		j := i
		for j < len(lines) && !isBlank(trimEnd(lines[j])) {
			j++
		}
		return Token{Type: TokHTML, Raw: join(lines, i, j), Text: strings.TrimRight(join(lines, i, j), "\n")}, j
	}
	if indentOf(line) >= 4 {
		return lexIndentedCode(lines, i)
	}
	return lexParagraph(lines, i, defs)
}

func join(lines []string, i, j int) string { return strings.Join(lines[i:j], "") }

// lexFence consumes a fenced code block. An unclosed fence — the streaming
// case — runs to the end of the input rather than failing to lex, so code
// renders as it arrives instead of appearing all at once at the close.
func lexFence(lines []string, i int, m []string) (Token, int) {
	marker := m[1]
	fenceChar := marker[0]
	j := i + 1
	var body []string
	for j < len(lines) {
		l := trimEnd(lines[j])
		trimmed := strings.TrimLeft(l, " ")
		if len(trimmed) >= len(marker) && strings.Trim(trimmed, string(fenceChar)) == "" {
			j++
			break
		}
		body = append(body, l)
		j++
	}
	// The info string's first word is the language; the rest is metadata a
	// terminal has no use for. A bare fence has neither.
	lang := ""
	if fields := strings.Fields(m[2]); len(fields) > 0 {
		lang = fields[0]
	}
	return Token{
		Type: TokCode,
		Raw:  join(lines, i, j),
		Lang: lang,
		Text: strings.Join(body, "\n"),
	}, j
}

// lexIndentedCode consumes a run of 4-space-indented lines, plus the blank
// lines inside it (but not the ones trailing it).
func lexIndentedCode(lines []string, i int) (Token, int) {
	j := i
	last := i
	var body []string
	for j < len(lines) {
		l := trimEnd(lines[j])
		if isBlank(l) {
			body = append(body, "")
			j++
			continue
		}
		if indentOf(l) < 4 {
			break
		}
		body = append(body, stripIndent(l, 4))
		j++
		last = j
	}
	for len(body) > 0 && body[len(body)-1] == "" {
		body = body[:len(body)-1]
	}
	return Token{Type: TokCode, Raw: join(lines, i, last), Text: strings.Join(body, "\n")}, last
}

// lexBlockquote consumes a `>`-prefixed run, including lazy continuation
// lines, then lexes the stripped content as blocks.
func lexBlockquote(lines []string, i int, defs linkDefs) (Token, int) {
	j := i
	var inner []string
	for j < len(lines) {
		l := trimEnd(lines[j])
		trimmed := strings.TrimLeft(l, " ")
		switch {
		case strings.HasPrefix(trimmed, ">"):
			content := strings.TrimPrefix(trimmed, ">")
			inner = append(inner, strings.TrimPrefix(content, " ")+"\n")
		case isBlank(l):
			j++
			goto done
		case startsBlock(l):
			goto done
		default:
			// Lazy continuation: a bare line under a quoted paragraph.
			inner = append(inner, l+"\n")
		}
		j++
	}
done:
	tok := Token{Type: TokBlockquote, Raw: join(lines, i, j)}
	tok.Tokens = lexBlocks(splitLinesKeepEnds(strings.Join(inner, "")), defs)
	return tok, j
}

// startsBlock reports whether a line would begin a new block, which is what
// stops a paragraph or a lazy continuation.
func startsBlock(line string) bool {
	if indentOf(line) >= 4 {
		return false // an indented line continues a paragraph, not a code block
	}
	return reFence.MatchString(line) ||
		reATXHeading.MatchString(line) ||
		reHr.MatchString(line) ||
		reBullet.MatchString(line) ||
		strings.HasPrefix(strings.TrimLeft(line, " "), ">") ||
		reHTMLBlock.MatchString(line)
}

// lexParagraph consumes lines until a blank line or a line that starts a new
// block, promoting to a setext heading when an `===`/`---` underline follows.
func lexParagraph(lines []string, i int, defs linkDefs) (Token, int) {
	j := i
	var text []string
	for j < len(lines) {
		l := trimEnd(lines[j])
		if isBlank(l) {
			break
		}
		if j > i {
			if m := reSetext.FindStringSubmatch(l); m != nil {
				depth := 2
				if m[1][0] == '=' {
					depth = 1
				}
				tok := Token{
					Type: TokHeading, Raw: join(lines, i, j+1),
					Depth: depth, Text: strings.TrimSpace(strings.Join(text, "\n")),
				}
				tok.Tokens = lexInline(tok.Text, defs)
				return tok, j + 1
			}
			if startsBlock(l) {
				break
			}
		}
		text = append(text, strings.TrimSpace(l))
		j++
	}
	tok := Token{Type: TokParagraph, Raw: join(lines, i, j), Text: strings.Join(text, "\n")}
	tok.Tokens = lexInline(tok.Text, defs)
	return tok, j
}

// lexTable consumes a GFM table: a header row, an alignment row, then body
// rows until a blank line or a line that is not part of the table.
func lexTable(lines []string, i int, defs linkDefs) (Token, int, bool) {
	if i+1 >= len(lines) {
		return Token{}, i, false
	}
	header := trimEnd(lines[i])
	delim := trimEnd(lines[i+1])
	if !strings.Contains(header, "|") || !reTableDelim.MatchString(delim) {
		return Token{}, i, false
	}
	headerCells := splitTableRow(header)
	alignCells := splitTableRow(delim)
	if len(headerCells) != len(alignCells) {
		return Token{}, i, false
	}

	tok := Token{Type: TokTable}
	for _, c := range headerCells {
		tok.Header = append(tok.Header, Cell{Text: c, Tokens: lexInline(c, defs)})
	}
	for _, a := range alignCells {
		a = strings.TrimSpace(a)
		switch {
		case strings.HasPrefix(a, ":") && strings.HasSuffix(a, ":"):
			tok.Align = append(tok.Align, AlignCenter)
		case strings.HasSuffix(a, ":"):
			tok.Align = append(tok.Align, AlignRight)
		case strings.HasPrefix(a, ":"):
			tok.Align = append(tok.Align, AlignLeft)
		default:
			tok.Align = append(tok.Align, AlignNone)
		}
	}

	j := i + 2
	for j < len(lines) {
		l := trimEnd(lines[j])
		if isBlank(l) || !strings.Contains(l, "|") {
			break
		}
		cells := splitTableRow(l)
		row := make([]Cell, len(tok.Header))
		for k := range row {
			text := ""
			if k < len(cells) {
				text = cells[k]
			}
			row[k] = Cell{Text: text, Tokens: lexInline(text, defs)}
		}
		tok.Rows = append(tok.Rows, row)
		j++
	}
	tok.Raw = join(lines, i, j)
	return tok, j, true
}

// splitTableRow splits on unescaped pipes, dropping the optional leading and
// trailing ones.
func splitTableRow(row string) []string {
	row = strings.TrimSpace(row)
	row = strings.TrimPrefix(row, "|")
	row = strings.TrimSuffix(row, "|")
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(row); i++ {
		if row[i] == '\\' && i+1 < len(row) && row[i+1] == '|' {
			cur.WriteByte('|')
			i++
			continue
		}
		if row[i] == '|' {
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			continue
		}
		cur.WriteByte(row[i])
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// lexList consumes a whole list: every item at this level, its nested
// content, and the blank lines between items.
func lexList(lines []string, i int, defs linkDefs) (Token, int) {
	first := reBullet.FindStringSubmatch(trimEnd(lines[i]))
	ordered := len(first[2]) > 1 || (first[2][0] >= '0' && first[2][0] <= '9')
	start := 1
	if ordered {
		start, _ = strconv.Atoi(strings.TrimRight(first[2], ".)"))
	}
	list := Token{Type: TokList, Ordered: ordered, Start: start}

	j := i
	sawBlank := false
	for j < len(lines) {
		// Blank lines between items belong to the list only when another item
		// of the SAME family follows — then they make it loose. Anything else
		// ends the list and hands the blanks back, so they lex as a `space`
		// token and render as the gap the author wrote.
		next := j
		blanks := 0
		for next < len(lines) && isBlank(trimEnd(lines[next])) {
			next++
			blanks++
		}
		if blanks > 1 || next >= len(lines) {
			break // two blank lines end a list outright
		}
		line := trimEnd(lines[next])
		m := reBullet.FindStringSubmatch(line)
		if m == nil || reHr.MatchString(line) {
			break
		}
		// A different marker family starts a NEW list rather than extending
		// this one — "- a" then "1. b" are two lists, not one.
		if isOrderedMarker(m[2]) != ordered {
			break
		}
		if blanks > 0 {
			sawBlank = true
		}
		j = next

		itemStart := j
		contentIndent := len(m[1]) + len(m[2]) + len(m[3])
		var content []string
		content = append(content, line[min(contentIndent, len(line)):]+"\n")
		j++

		blanksPending := 0
		for j < len(lines) {
			l := trimEnd(lines[j])
			if isBlank(l) {
				blanksPending++
				j++
				if blanksPending > 1 {
					break
				}
				continue
			}
			if indentOf(l) >= contentIndent {
				// A blank line inside an item makes the whole list loose.
				for b := 0; b < blanksPending; b++ {
					content = append(content, "\n")
					sawBlank = true
				}
				blanksPending = 0
				content = append(content, stripIndent(l, contentIndent)+"\n")
				j++
				continue
			}
			// A bare line still continues the item's paragraph (lazy
			// continuation) — but only while no blank has intervened.
			if blanksPending == 0 && !startsBlock(l) {
				content = append(content, strings.TrimSpace(l)+"\n")
				j++
				continue
			}
			break
		}
		// Trailing blanks are the list's business, not this item's.
		j -= blanksPending

		item := Token{Type: TokListItem, Raw: join(lines, itemStart, j)}
		body := strings.Join(content, "")
		if tm := reTaskMarker.FindStringSubmatch(body); tm != nil {
			item.Task = true
			item.Checked = tm[1] != " "
			body = body[len(tm[0]):]
		}
		item.Tokens = lexBlocks(splitLinesKeepEnds(body), defs)
		list.Items = append(list.Items, item)
	}

	list.Loose = sawBlank
	if !list.Loose {
		// A tight item's paragraphs are not paragraphs: they render inline,
		// with no blank line under them.
		for k := range list.Items {
			for t := range list.Items[k].Tokens {
				if list.Items[k].Tokens[t].Type == TokParagraph {
					list.Items[k].Tokens[t].Type = TokText
				}
			}
		}
	}
	list.Raw = join(lines, i, j)
	return list, j
}

func isOrderedMarker(marker string) bool {
	return marker[0] >= '0' && marker[0] <= '9'
}
