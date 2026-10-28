package tui

import (
	"strings"
	"unicode"
)

// Syntax highlighting for fenced code blocks.
//
// One scanner, per-language keyword sets. A code block in a chat transcript
// is read, not edited: the job is to separate strings from comments from
// identifiers at a glance, which a lexer this size does. Anything it cannot
// classify falls through as plain text rather than being guessed at.

// keywordsByLang maps a fence info string to its reserved words.
var keywordsByLang = map[string][]string{
	"go": {
		"break", "case", "chan", "const", "continue", "default", "defer", "else",
		"fallthrough", "for", "func", "go", "goto", "if", "import", "interface",
		"map", "package", "range", "return", "select", "struct", "switch", "type", "var",
	},
	"js": {
		"async", "await", "break", "case", "catch", "class", "const", "continue",
		"default", "delete", "do", "else", "export", "extends", "finally", "for",
		"function", "if", "import", "in", "instanceof", "let", "new", "of", "return",
		"static", "super", "switch", "this", "throw", "try", "typeof", "var", "void",
		"while", "yield",
	},
	"ts": {
		"abstract", "any", "as", "async", "await", "boolean", "break", "case", "catch",
		"class", "const", "continue", "declare", "default", "delete", "do", "else",
		"enum", "export", "extends", "finally", "for", "function", "if", "implements",
		"import", "in", "instanceof", "interface", "keyof", "let", "namespace", "never",
		"new", "number", "of", "private", "protected", "public", "readonly", "return",
		"static", "string", "super", "switch", "this", "throw", "try", "type", "typeof",
		"unknown", "var", "void", "while", "yield",
	},
	"python": {
		"and", "as", "assert", "async", "await", "break", "class", "continue", "def",
		"del", "elif", "else", "except", "finally", "for", "from", "global", "if",
		"import", "in", "is", "lambda", "nonlocal", "not", "or", "pass", "raise",
		"return", "try", "while", "with", "yield",
	},
	"rust": {
		"as", "async", "await", "break", "const", "continue", "crate", "dyn", "else",
		"enum", "extern", "fn", "for", "if", "impl", "in", "let", "loop", "match",
		"mod", "move", "mut", "pub", "ref", "return", "self", "static", "struct",
		"super", "trait", "type", "unsafe", "use", "where", "while",
	},
	"sh": {
		"case", "do", "done", "elif", "else", "esac", "fi", "for", "function", "if",
		"in", "local", "return", "then", "until", "while", "export", "source",
	},
}

// langAliases folds the many names a fence can carry onto one keyword set.
var langAliases = map[string]string{
	"golang":     "go",
	"javascript": "js", "jsx": "js", "mjs": "js", "cjs": "js", "node": "js",
	"typescript": "ts", "tsx": "ts",
	"py": "python", "python3": "python",
	"rs":   "rust",
	"bash": "sh", "zsh": "sh", "shell": "sh", "console": "sh",
	"json":  "json",
	"jsonc": "json",
	"c":     "ts", "cpp": "ts", "c++": "ts", "java": "ts", "kotlin": "ts", "swift": "ts",
}

// literals read as constants in every language this highlights.
var literals = map[string]bool{
	"true": true, "false": true, "null": true, "nil": true, "None": true,
	"True": true, "False": true, "undefined": true, "NaN": true, "iota": true,
}

// Highlight colours one code block, returning its lines.
func Highlight(t *Theme, code, lang string) []string {
	lines := strings.Split(code, "\n")
	key := strings.ToLower(strings.TrimSpace(lang))
	if alias, ok := langAliases[key]; ok {
		key = alias
	}
	keywords, known := keywordsByLang[key]
	if !known {
		// An unknown or absent language still gets strings, numbers and
		// comments — the marks that matter most for skimming.
		keywords = nil
	}
	kw := make(map[string]bool, len(keywords))
	for _, k := range keywords {
		kw[k] = true
	}

	lineComment := "//"
	if key == "python" || key == "sh" {
		lineComment = "#"
	}

	out := make([]string, len(lines))
	inBlockComment := false
	for i, line := range lines {
		out[i], inBlockComment = highlightLine(t, line, kw, lineComment, inBlockComment)
	}
	return out
}

// highlightLine scans one line, returning it styled and whether a block
// comment is still open at the end of it.
func highlightLine(t *Theme, line string, kw map[string]bool, lineComment string, inBlock bool) (string, bool) {
	var b strings.Builder
	i := 0

	if inBlock {
		if end := strings.Index(line, "*/"); end >= 0 {
			b.WriteString(t.Fg(SlotSyntaxComment, line[:end+2]))
			i = end + 2
			inBlock = false
		} else {
			return t.Fg(SlotSyntaxComment, line), true
		}
	}

	for i < len(line) {
		c := line[i]
		switch {
		case strings.HasPrefix(line[i:], lineComment):
			b.WriteString(t.Fg(SlotSyntaxComment, line[i:]))
			return b.String(), false

		case strings.HasPrefix(line[i:], "/*"):
			if end := strings.Index(line[i+2:], "*/"); end >= 0 {
				b.WriteString(t.Fg(SlotSyntaxComment, line[i:i+2+end+2]))
				i += 2 + end + 2
				continue
			}
			b.WriteString(t.Fg(SlotSyntaxComment, line[i:]))
			return b.String(), true

		case c == '"' || c == '\'' || c == '`':
			str, n := scanString(line, i)
			b.WriteString(t.Fg(SlotSyntaxString, str))
			i += n

		case unicode.IsDigit(rune(c)) && (i == 0 || !isIdentByte(line[i-1])):
			start := i
			for i < len(line) && (isIdentByte(line[i]) || line[i] == '.') {
				i++
			}
			b.WriteString(t.Fg(SlotSyntaxNumber, line[start:i]))

		case isIdentStart(c):
			start := i
			for i < len(line) && isIdentByte(line[i]) {
				i++
			}
			word := line[start:i]
			switch {
			case kw[word]:
				b.WriteString(t.Fg(SlotSyntaxKeyword, word))
			case literals[word]:
				b.WriteString(t.Fg(SlotSyntaxNumber, word))
			case i < len(line) && line[i] == '(':
				b.WriteString(t.Fg(SlotSyntaxFunction, word))
			case unicode.IsUpper(rune(word[0])):
				// A leading capital is a type in most of these languages,
				// and a harmless recolour of a constant in the rest.
				b.WriteString(t.Fg(SlotSyntaxType, word))
			default:
				b.WriteString(t.Fg(SlotSyntaxVariable, word))
			}

		case isOperatorByte(c):
			start := i
			for i < len(line) && isOperatorByte(line[i]) {
				i++
			}
			b.WriteString(t.Fg(SlotSyntaxOperator, line[start:i]))

		case isPunctByte(c):
			b.WriteString(t.Fg(SlotSyntaxPunctuation, string(c)))
			i++

		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String(), false
}

// scanString consumes a quoted string, honouring backslash escapes. An
// unterminated quote runs to end of line rather than swallowing the file.
func scanString(line string, i int) (string, int) {
	quote := line[i]
	j := i + 1
	for j < len(line) {
		if line[j] == '\\' {
			j += 2
			continue
		}
		if line[j] == quote {
			j++
			break
		}
		j++
	}
	if j > len(line) {
		j = len(line)
	}
	return line[i:j], j - i
}

func isIdentStart(c byte) bool {
	return c == '_' || c == '$' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isIdentByte(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
}

func isOperatorByte(c byte) bool {
	return strings.IndexByte("+-*/%=<>!&|^~?:", c) >= 0
}

func isPunctByte(c byte) bool {
	return strings.IndexByte("()[]{},;.", c) >= 0
}
