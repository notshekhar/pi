package tui

// The markdown token model, shaped after marked's — the lexer loop's
// renderer was written against.
//
// The invariant the whole streaming path rests on: concatenating every
// top-level token's Raw reproduces the source byte for byte. That is what
// lets a settled prefix of a streaming message be frozen and never re-lexed
// (see freezePoint), and it is checked at runtime before any freeze is
// trusted — a lexer bug costs the optimisation, never a wrong frame.

// TokenType names a block or inline construct.
type TokenType string

const (
	// Block
	TokSpace      TokenType = "space"
	TokParagraph  TokenType = "paragraph"
	TokHeading    TokenType = "heading"
	TokCode       TokenType = "code"
	TokList       TokenType = "list"
	TokListItem   TokenType = "list_item"
	TokBlockquote TokenType = "blockquote"
	TokHr         TokenType = "hr"
	TokTable      TokenType = "table"
	TokHTML       TokenType = "html"
	TokDef        TokenType = "def"

	// Inline
	TokText     TokenType = "text"
	TokEscape   TokenType = "escape"
	TokStrong   TokenType = "strong"
	TokEm       TokenType = "em"
	TokDel      TokenType = "del"
	TokCodespan TokenType = "codespan"
	TokLink     TokenType = "link"
	TokImage    TokenType = "image"
	TokBr       TokenType = "br"
)

// Align is a table column's alignment.
type Align string

const (
	AlignNone   Align = ""
	AlignLeft   Align = "left"
	AlignCenter Align = "center"
	AlignRight  Align = "right"
)

// Cell is one table cell, kept unrendered until the renderer knows its width.
type Cell struct {
	Text   string
	Tokens []Token
}

// Token is one node. Fields outside a type's own set are zero.
type Token struct {
	Type TokenType
	// Raw is the exact source this token consumed. See the file comment.
	Raw string
	// Text is the token's literal content: a code block's body, a codespan's
	// contents, the source a paragraph's inline tokens were lexed from.
	Text string
	// Tokens are the children — inline for a paragraph or heading, block for
	// a blockquote or list item.
	Tokens []Token

	Depth int    // heading level
	Lang  string // fenced code info string

	Ordered bool    // list
	Start   int     // ordered list's first number
	Loose   bool    // list items are wrapped in paragraphs
	Items   []Token // list items

	Task    bool // list item is a checkbox
	Checked bool

	Href  string // link / image
	Title string

	Header []Cell   // table
	Align  []Align  //
	Rows   [][]Cell //
}

// isBlank reports whether a line has nothing but spaces and tabs.
func isBlank(line string) bool {
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' && line[i] != '\t' && line[i] != '\r' {
			return false
		}
	}
	return true
}

// indentOf counts a line's leading indentation in columns, with tabs
// expanding to the next multiple of four.
func indentOf(line string) int {
	n := 0
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case ' ':
			n++
		case '\t':
			n += 4 - n%4
		default:
			return n
		}
	}
	return n
}

// stripIndent removes up to n columns of leading whitespace.
func stripIndent(line string, n int) string {
	i, col := 0, 0
	for i < len(line) && col < n {
		switch line[i] {
		case ' ':
			col++
		case '\t':
			col += 4 - col%4
		default:
			return line[i:]
		}
		i++
	}
	return line[i:]
}
