package tui

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The inline lexer.
//
// Emphasis is the part regex-based renderers get wrong. `**a * b**` is one
// bold span, `a*b*c` is emphasis but `a_b_c` is not, and `***x***` is bold
// inside italic — none of which a substitution pass can decide, because
// whether a run of `*` opens or closes depends on the characters on BOTH
// sides of it. So the scanner emits delimiter runs as a flat list and a
// second pass matches them with CommonMark's delimiter-stack algorithm.

var (
	reAutolink    = regexp.MustCompile(`^<([A-Za-z][A-Za-z0-9+.-]{1,31}:[^<>\x00-\x20]*)>`)
	reEmailLink   = regexp.MustCompile(`^<([A-Za-z0-9.!#$%&'*+/=?^_` + "`" + `{|}~-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)*)>`)
	reInlineHTML  = regexp.MustCompile(`^</?[A-Za-z][A-Za-z0-9-]*(?:\s+[^<>]*?)?/?>`)
	reBareURL     = regexp.MustCompile(`^(https?://|www\.)[^\s<]+[^\s<.,:;"')\]!?]`)
	rePunctuation = regexp.MustCompile(`^[!-/:-@\[-` + "`" + `{-~]$`)
)

// inode is one node of the inline stream: either a finished token, or a run
// of emphasis delimiters still waiting to be matched.
type inode struct {
	tok     Token
	delim   byte // 0 when this is a real token
	count   int
	canOpen bool
	canShut bool
	dead    bool
}

// lexInline turns one block's text into inline tokens.
func lexInline(src string, defs linkDefs) []Token {
	if src == "" {
		return nil
	}
	nodes := scanInline(src, defs)
	nodes = resolveEmphasis(nodes, 0)
	return collect(nodes)
}

// scanInline produces the flat node stream: literal tokens plus unmatched
// delimiter runs.
func scanInline(src string, defs linkDefs) []inode {
	var nodes []inode
	var text strings.Builder
	textRaw := strings.Builder{}

	flush := func() {
		if text.Len() > 0 {
			nodes = append(nodes, inode{tok: Token{Type: TokText, Raw: textRaw.String(), Text: text.String()}})
			text.Reset()
			textRaw.Reset()
		}
	}
	push := func(t Token) {
		flush()
		nodes = append(nodes, inode{tok: t})
	}

	i := 0
	for i < len(src) {
		c := src[i]
		switch {
		case c == '\\' && i+1 < len(src):
			if src[i+1] == '\n' {
				push(Token{Type: TokBr, Raw: src[i : i+2]})
				i += 2
				continue
			}
			if rePunctuation.MatchString(string(src[i+1])) {
				push(Token{Type: TokEscape, Raw: src[i : i+2], Text: string(src[i+1])})
				i += 2
				continue
			}
			text.WriteByte(c)
			textRaw.WriteByte(c)
			i++

		case c == '`':
			if tok, n := scanCodespan(src, i); n > 0 {
				push(tok)
				i += n
				continue
			}
			text.WriteByte(c)
			textRaw.WriteByte(c)
			i++

		case c == '\n':
			// Two trailing spaces before a newline is a hard break; anything
			// else is a soft one, which the renderer folds into wrapping.
			if strings.HasSuffix(text.String(), "  ") {
				s := strings.TrimRight(text.String(), " ")
				r := textRaw.String()
				text.Reset()
				textRaw.Reset()
				text.WriteString(s)
				textRaw.WriteString(r)
				push(Token{Type: TokBr, Raw: "\n"})
				i++
				continue
			}
			text.WriteByte(c)
			textRaw.WriteByte(c)
			i++

		case c == '<':
			if m := reAutolink.FindStringSubmatch(src[i:]); m != nil {
				push(Token{Type: TokLink, Raw: m[0], Text: m[1], Href: m[1],
					Tokens: []Token{{Type: TokText, Raw: m[1], Text: m[1]}}})
				i += len(m[0])
				continue
			}
			if m := reEmailLink.FindStringSubmatch(src[i:]); m != nil {
				push(Token{Type: TokLink, Raw: m[0], Text: m[1], Href: "mailto:" + m[1],
					Tokens: []Token{{Type: TokText, Raw: m[1], Text: m[1]}}})
				i += len(m[0])
				continue
			}
			if m := reInlineHTML.FindString(src[i:]); m != "" {
				push(Token{Type: TokHTML, Raw: m, Text: m})
				i += len(m)
				continue
			}
			text.WriteByte(c)
			textRaw.WriteByte(c)
			i++

		case c == '!' && i+1 < len(src) && src[i+1] == '[':
			if tok, n := scanLink(src, i+1, defs, true); n > 0 {
				tok.Raw = "!" + tok.Raw
				push(tok)
				i += n + 1
				continue
			}
			text.WriteByte(c)
			textRaw.WriteByte(c)
			i++

		case c == '[':
			if tok, n := scanLink(src, i, defs, false); n > 0 {
				push(tok)
				i += n
				continue
			}
			text.WriteByte(c)
			textRaw.WriteByte(c)
			i++

		case c == '*' || c == '_' || c == '~':
			run := 1
			for i+run < len(src) && src[i+run] == c {
				run++
			}
			if c == '~' && run != 2 {
				text.WriteString(src[i : i+run])
				textRaw.WriteString(src[i : i+run])
				i += run
				continue
			}
			flush()
			open, shut := flanking(src, i, i+run, c)
			nodes = append(nodes, inode{
				tok:     Token{Type: TokText, Raw: src[i : i+run], Text: src[i : i+run]},
				delim:   c,
				count:   run,
				canOpen: open,
				canShut: shut,
			})
			i += run

		default:
			if m := reBareURL.FindString(src[i:]); m != "" && (i == 0 || isBoundary(src[i-1])) {
				href := m
				if strings.HasPrefix(m, "www.") {
					href = "http://" + m
				}
				push(Token{Type: TokLink, Raw: m, Text: m, Href: href,
					Tokens: []Token{{Type: TokText, Raw: m, Text: m}}})
				i += len(m)
				continue
			}
			_, sz := utf8.DecodeRuneInString(src[i:])
			text.WriteString(src[i : i+sz])
			textRaw.WriteString(src[i : i+sz])
			i += sz
		}
	}
	flush()
	return nodes
}

func isBoundary(b byte) bool {
	return b == ' ' || b == '\t' || b == '\n' || b == '(' || b == '['
}

// scanCodespan matches a backtick-delimited span. The opening and closing
// runs must be the same length, so “ `a` “ survives a backtick inside it.
func scanCodespan(src string, i int) (Token, int) {
	open := 0
	for i+open < len(src) && src[i+open] == '`' {
		open++
	}
	j := i + open
	for j < len(src) {
		if src[j] != '`' {
			j++
			continue
		}
		run := 0
		for j+run < len(src) && src[j+run] == '`' {
			run++
		}
		if run == open {
			content := src[i+open : j]
			// One leading and trailing space is stripped, so `` ` `` can
			// hold a literal backtick without the padding showing.
			if len(content) > 1 && strings.HasPrefix(content, " ") && strings.HasSuffix(content, " ") &&
				strings.TrimSpace(content) != "" {
				content = content[1 : len(content)-1]
			}
			raw := src[i : j+run]
			return Token{Type: TokCodespan, Raw: raw, Text: strings.ReplaceAll(content, "\n", " ")}, len(raw)
		}
		j += run
	}
	return Token{}, 0
}

// scanLink matches `[text](href "title")` and the reference forms, returning
// the token and how many bytes it consumed.
func scanLink(src string, i int, defs linkDefs, image bool) (Token, int) {
	close := matchBracket(src, i)
	if close < 0 {
		return Token{}, 0
	}
	label := src[i+1 : close]
	kind := TokLink
	if image {
		kind = TokImage
	}

	// Inline form: [text](href "title")
	if close+1 < len(src) && src[close+1] == '(' {
		if end := matchParen(src, close+1); end >= 0 {
			href, title := splitDestination(src[close+2 : end])
			tok := Token{Type: kind, Raw: src[i : end+1], Text: label, Href: href, Title: title}
			if !image {
				tok.Tokens = lexInline(label, defs)
			}
			return tok, end + 1 - i
		}
	}

	// Reference forms: [text][ref] and the collapsed/shortcut [ref].
	ref := strings.ToLower(strings.TrimSpace(label))
	end := close
	if close+1 < len(src) && src[close+1] == '[' {
		if refClose := matchBracket(src, close+1); refClose >= 0 {
			if inner := strings.TrimSpace(src[close+2 : refClose]); inner != "" {
				ref = strings.ToLower(inner)
			}
			end = refClose
		}
	}
	def, ok := defs[ref]
	if !ok {
		return Token{}, 0
	}
	tok := Token{Type: kind, Raw: src[i : end+1], Text: label, Href: def.Href, Title: def.Title}
	if !image {
		tok.Tokens = lexInline(label, defs)
	}
	return tok, end + 1 - i
}

// matchBracket finds the `]` closing the `[` at i, honouring nesting and
// escapes. Returns -1 when it never closes.
func matchBracket(src string, i int) int {
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '[':
			depth++
		case ']':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

func matchParen(src string, i int) int {
	depth := 0
	for j := i; j < len(src); j++ {
		switch src[j] {
		case '\\':
			j++
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return j
			}
		}
	}
	return -1
}

// splitDestination separates a link destination from its optional title.
func splitDestination(s string) (href, title string) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<") {
		if end := strings.Index(s, ">"); end >= 0 {
			href = s[1:end]
			title = strings.TrimSpace(s[end+1:])
			return href, strings.Trim(title, `"'()`)
		}
	}
	for _, q := range []string{` "`, ` '`, ` (`} {
		if idx := strings.Index(s, q); idx >= 0 {
			return s[:idx], strings.Trim(strings.TrimSpace(s[idx+1:]), `"'()`)
		}
	}
	return s, ""
}

// flanking decides whether a delimiter run can open or close emphasis.
//
// The rule is about what surrounds the run: left-flanking means it is not
// followed by whitespace and either not followed by punctuation or preceded
// by whitespace/punctuation, and right-flanking is the mirror. `_` is
// stricter than `*` — that is why snake_case_words keep their underscores
// while a*b*c still italicises.
func flanking(src string, start, end int, c byte) (canOpen, canClose bool) {
	before := ' '
	if start > 0 {
		before, _ = utf8.DecodeLastRuneInString(src[:start])
	}
	after := ' '
	if end < len(src) {
		after, _ = utf8.DecodeRuneInString(src[end:])
	}
	beforeSpace, beforePunct := isSpaceRune(before), isPunctRune(before)
	afterSpace, afterPunct := isSpaceRune(after), isPunctRune(after)

	left := !afterSpace && (!afterPunct || beforeSpace || beforePunct)
	right := !beforeSpace && (!beforePunct || afterSpace || afterPunct)

	if c == '_' {
		return left && (!right || beforePunct), right && (!left || afterPunct)
	}
	return left, right
}

func isSpaceRune(r rune) bool { return unicode.IsSpace(r) }
func isPunctRune(r rune) bool {
	return unicode.IsPunct(r) || unicode.IsSymbol(r)
}

// resolveEmphasis matches delimiter runs into strong/em/del spans.
//
// CommonMark's algorithm: walk forward to each potential closer, walk back to
// the nearest compatible opener, wrap everything between them, and repeat.
// `depth` guards the recursion into the wrapped span.
func resolveEmphasis(nodes []inode, depth int) []inode {
	if depth > 8 {
		return nodes
	}
	for closer := 0; closer < len(nodes); closer++ {
		if nodes[closer].delim == 0 || nodes[closer].dead || !nodes[closer].canShut {
			continue
		}
		c := nodes[closer].delim

		opener := -1
		for k := closer - 1; k >= 0; k-- {
			if nodes[k].delim != c || nodes[k].dead || !nodes[k].canOpen {
				continue
			}
			// "Rule of three": when one run can both open and close, the sum
			// of the two lengths cannot be a multiple of three unless both
			// are. Without it ***a*** nests the wrong way round.
			if (nodes[k].canShut || nodes[closer].canOpen) &&
				(nodes[k].count+nodes[closer].count)%3 == 0 &&
				(nodes[k].count%3 != 0 || nodes[closer].count%3 != 0) {
				continue
			}
			opener = k
			break
		}
		if opener < 0 {
			// Never an opener: it can still close a later run, so only its
			// opening role is retired.
			nodes[closer].canShut = false
			continue
		}

		use := 1
		kind := TokEm
		if c == '~' {
			use, kind = 2, TokDel
		} else if nodes[opener].count >= 2 && nodes[closer].count >= 2 {
			use, kind = 2, TokStrong
		}

		inner := resolveEmphasis(cloneNodes(nodes[opener+1:closer]), depth+1)
		span := Token{
			Type:   kind,
			Raw:    strings.Repeat(string(c), use) + rawOf(nodes[opener+1:closer]) + strings.Repeat(string(c), use),
			Tokens: collect(inner),
		}
		span.Text = textOf(span.Tokens)

		nodes[opener].count -= use
		nodes[closer].count -= use
		nodes[opener].tok.Text = strings.Repeat(string(c), nodes[opener].count)
		nodes[opener].tok.Raw = nodes[opener].tok.Text
		nodes[closer].tok.Text = strings.Repeat(string(c), nodes[closer].count)
		nodes[closer].tok.Raw = nodes[closer].tok.Text

		// Everything between the two runs is now inside the span.
		rebuilt := append([]inode{}, nodes[:opener]...)
		if nodes[opener].count > 0 {
			rebuilt = append(rebuilt, nodes[opener])
		}
		rebuilt = append(rebuilt, inode{tok: span})
		closerIdx := len(rebuilt)
		if nodes[closer].count > 0 {
			rebuilt = append(rebuilt, nodes[closer])
		}
		rebuilt = append(rebuilt, nodes[closer+1:]...)
		nodes = rebuilt
		closer = closerIdx - 1
	}
	return nodes
}

func cloneNodes(in []inode) []inode {
	out := make([]inode, len(in))
	copy(out, in)
	return out
}

func rawOf(nodes []inode) string {
	var b strings.Builder
	for _, n := range nodes {
		b.WriteString(n.tok.Raw)
	}
	return b.String()
}

func textOf(tokens []Token) string {
	var b strings.Builder
	for _, t := range tokens {
		if len(t.Tokens) > 0 {
			b.WriteString(textOf(t.Tokens))
			continue
		}
		b.WriteString(t.Text)
	}
	return b.String()
}

// collect drops the node wrapper, merging adjacent literal text so leftover
// delimiter runs read as the characters they are.
func collect(nodes []inode) []Token {
	var out []Token
	for _, n := range nodes {
		if n.dead || (n.delim != 0 && n.count == 0) {
			continue
		}
		t := n.tok
		if len(out) > 0 && t.Type == TokText && out[len(out)-1].Type == TokText && len(out[len(out)-1].Tokens) == 0 {
			out[len(out)-1].Text += t.Text
			out[len(out)-1].Raw += t.Raw
			continue
		}
		out = append(out, t)
	}
	return out
}
