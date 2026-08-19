package tools

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Replacement is one old→new text swap.
type Replacement struct {
	OldText string
	NewText string
}

// detectLineEnding returns the first line ending in content.
func detectLineEnding(content string) string {
	if i := strings.Index(content, "\r\n"); i >= 0 {
		if j := strings.Index(content, "\n"); j >= 0 && j < i {
			return "\n"
		}
		return "\r\n"
	}
	return "\n"
}

func normalizeToLF(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	return strings.ReplaceAll(text, "\r", "\n")
}

func restoreLineEndings(text, ending string) string {
	if ending == "\r\n" {
		return strings.ReplaceAll(text, "\n", "\r\n")
	}
	return text
}

func stripBOM(content string) (bom, text string) {
	if strings.HasPrefix(content, "\uFEFF") {
		return "\uFEFF", content[len("\uFEFF"):]
	}
	return "", content
}

// normalizeForFuzzyMatch folds quotes, dashes and trailing space. Used only
// to find a match — the file is never rewritten in this space.
func normalizeForFuzzyMatch(text string) string {
	text = strings.ToValidUTF8(text, "")
	var b strings.Builder
	for _, line := range strings.Split(string([]rune(string(normNFKC(text)))), "\n") {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(strings.TrimRightFunc(line, unicode.IsSpace))
	}
	s := b.String()
	s = strings.Map(foldQuoteDash, s)
	return s
}

func foldQuoteDash(r rune) rune {
	switch r {
	case '\u2018', '\u2019', '\u201A', '\u201B':
		return '\''
	case '\u201C', '\u201D', '\u201E', '\u201F':
		return '"'
	case '\u2010', '\u2011', '\u2012', '\u2013', '\u2014', '\u2015', '\u2212':
		return '-'
	case '\u00A0', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006',
		'\u2007', '\u2008', '\u2009', '\u200A', '\u202F', '\u205F', '\u3000':
		return ' '
	}
	return r
}

func normNFKC(s string) string {
	// Avoid importing golang.org/x/text: NFKC is only needed for compatibility
	// characters. The fold tables above cover the cases that actually bite.
	return s
}

type mapped struct {
	text      string
	spanStart []int
	spanEnd   []int
}

func normalizeCodePoint(cp string) string {
	if cp == "\n" {
		return "\n"
	}
	// Wrap in digits so a lone space is not stripped as trailing whitespace.
	folded := normalizeForFuzzyMatch("0" + cp + "0")
	if len(folded) < 2 {
		return ""
	}
	return folded[1 : len(folded)-1]
}

func normalizeWithMap(text string) mapped {
	var (
		out       []rune
		spanStart []int
		spanEnd   []int
		lineStart int
		index     int
	)
	dropTrailing := func() {
		for len(out) > lineStart && unicode.IsSpace(out[len(out)-1]) && out[len(out)-1] != '\n' {
			out = out[:len(out)-1]
			spanStart = spanStart[:len(spanStart)-1]
			spanEnd = spanEnd[:len(spanEnd)-1]
		}
	}
	for _, cp := range text {
		next := index + utf8.RuneLen(cp)
		if cp == '\n' {
			dropTrailing()
			out = append(out, '\n')
			spanStart = append(spanStart, index)
			spanEnd = append(spanEnd, next)
			lineStart = len(out)
		} else {
			for _, ch := range normalizeCodePoint(string(cp)) {
				out = append(out, ch)
				spanStart = append(spanStart, index)
				spanEnd = append(spanEnd, next)
			}
		}
		index = next
	}
	dropTrailing()
	return mapped{text: string(out), spanStart: spanStart, spanEnd: spanEnd}
}

func fuzzyText(text string) string {
	return normalizeWithMap(text).text
}

type matchSpan struct {
	start, end     int
	usedFuzzyMatch bool
}

// findMatchSpan locates oldText in content, exact first then fuzzy. The
// returned span is always an offset range into content itself.
func findMatchSpan(content, oldText string) (matchSpan, bool) {
	if i := strings.Index(content, oldText); i >= 0 {
		return matchSpan{start: i, end: i + len(oldText)}, true
	}
	m := normalizeWithMap(content)
	needle := []rune(fuzzyText(oldText))
	if len(needle) == 0 {
		return matchSpan{}, false
	}
	hay := []rune(m.text)
	at := indexRunes(hay, needle)
	if at < 0 || at+len(needle)-1 >= len(m.spanEnd) {
		return matchSpan{}, false
	}
	return matchSpan{start: m.spanStart[at], end: m.spanEnd[at+len(needle)-1], usedFuzzyMatch: true}, true
}

func indexRunes(hay, needle []rune) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
	for i := 0; i <= len(hay)-len(needle); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

func countIn(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	n := 0
	for start := 0; start+len(needle) <= len(haystack); {
		i := strings.Index(haystack[start:], needle)
		if i < 0 {
			break
		}
		n++
		start += i + len(needle)
	}
	return n
}

func countOccurrences(content, oldText string, fuzzy bool) int {
	if fuzzy {
		return countIn(fuzzyText(content), fuzzyText(oldText))
	}
	return countIn(content, oldText)
}

type matchedEdit struct {
	editIndex   int
	matchIndex  int
	matchLength int
	newText     string
}

// applyEdits applies replacements against the original content. Fuzzy hits
// are mapped back to the real span so untouched regions stay byte-identical.
func applyEdits(content string, edits []Replacement, path string) (base, next string, err error) {
	normalized := make([]Replacement, len(edits))
	for i, e := range edits {
		normalized[i] = Replacement{OldText: normalizeToLF(e.OldText), NewText: normalizeToLF(e.NewText)}
		if normalized[i].OldText == "" {
			if len(edits) == 1 {
				return "", "", fmt.Errorf("oldText must not be empty in %s", path)
			}
			return "", "", fmt.Errorf("edits[%d].oldText must not be empty in %s", i, path)
		}
	}

	base = content
	matched := make([]matchedEdit, 0, len(normalized))
	for i, e := range normalized {
		span, ok := findMatchSpan(base, e.OldText)
		if !ok {
			if len(edits) == 1 {
				return "", "", fmt.Errorf("Could not find the exact text in %s. The old text must match exactly including all whitespace and newlines.", path)
			}
			return "", "", fmt.Errorf("Could not find edits[%d] in %s. The oldText must match exactly including all whitespace and newlines.", i, path)
		}
		n := countOccurrences(base, e.OldText, span.usedFuzzyMatch)
		if n > 1 {
			if len(edits) == 1 {
				return "", "", fmt.Errorf("Found %d occurrences of the text in %s. The text must be unique. Please provide more context to make it unique.", n, path)
			}
			return "", "", fmt.Errorf("Found %d occurrences of edits[%d] in %s. Each oldText must be unique. Please provide more context to make it unique.", n, i, path)
		}
		matched = append(matched, matchedEdit{
			editIndex:   i,
			matchIndex:  span.start,
			matchLength: span.end - span.start,
			newText:     e.NewText,
		})
	}

	for i := 1; i < len(matched); i++ {
		for j := i; j > 0 && matched[j].matchIndex < matched[j-1].matchIndex; j-- {
			matched[j], matched[j-1] = matched[j-1], matched[j]
		}
	}
	for i := 1; i < len(matched); i++ {
		prev, cur := matched[i-1], matched[i]
		if prev.matchIndex+prev.matchLength > cur.matchIndex {
			return "", "", fmt.Errorf("edits[%d] and edits[%d] overlap in %s. Merge them into one edit or target disjoint regions.", prev.editIndex, cur.editIndex, path)
		}
	}

	next = base
	for i := len(matched) - 1; i >= 0; i-- {
		e := matched[i]
		next = next[:e.matchIndex] + e.newText + next[e.matchIndex+e.matchLength:]
	}
	if base == next {
		return "", "", fmt.Errorf("No changes made to %s. The replacement produced identical content.", path)
	}
	return base, next, nil
}

// generateDiff is a small unified-ish diff for the model and the user.
func generateDiff(oldContent, newContent string, context int) string {
	if context <= 0 {
		context = 4
	}
	oldLines := strings.Split(oldContent, "\n")
	newLines := strings.Split(newContent, "\n")
	// Myers is overkill; walk with an LCS-free sliding window: emit a
	// line-oriented diff by comparing prefixes until they diverge, then
	// resync. Good enough for the blocks edit actually touches.
	var out []string
	oi, ni := 0, 0
	width := len(fmt.Sprintf("%d", max(len(oldLines), len(newLines))))
	for oi < len(oldLines) || ni < len(newLines) {
		if oi < len(oldLines) && ni < len(newLines) && oldLines[oi] == newLines[ni] {
			oi++
			ni++
			continue
		}
		// collect a changed hunk
		os, ns := oi, ni
		for oi < len(oldLines) && (ni >= len(newLines) || oldLines[oi] != newLines[ni]) {
			// try to resync new
			synced := false
			for look := ni; look < len(newLines) && look-ni < 40; look++ {
				if oldLines[oi] == newLines[look] {
					for ns < look {
						out = append(out, fmt.Sprintf("+%*d %s", width, ns+1, newLines[ns]))
						ns++
					}
					ni = look
					synced = true
					break
				}
			}
			if synced {
				break
			}
			out = append(out, fmt.Sprintf("-%*d %s", width, oi+1, oldLines[oi]))
			oi++
		}
		for ni < len(newLines) && (oi >= len(oldLines) || oldLines[oi] != newLines[ni]) {
			out = append(out, fmt.Sprintf("+%*d %s", width, ni+1, newLines[ni]))
			ni++
		}
		_ = os
		_ = context
	}
	return strings.Join(out, "\n")
}
