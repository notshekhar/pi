package tui

import "strings"

// Table layout, ported from loop's renderTable.
//
// Columns are sized from what the content actually needs, then shrunk toward
// a floor set by each column's longest unbreakable word, so narrowing the
// terminal degrades a table gracefully instead of shredding it.

// maxUnbrokenWordWidth caps how much a single long word can hold a column
// open — past this, the word wraps rather than starving its neighbours.
const maxUnbrokenWordWidth = 30

func (m *Markdown) renderTable(token *Token, availableWidth int, nextType TokenType, sc *inlineStyle) []string {
	cols := len(token.Header)
	if cols == 0 {
		return nil
	}

	// Border overhead: "│ " + (n-1)×" │ " + " │" = 3n + 1.
	overhead := 3*cols + 1
	forCells := availableWidth - overhead
	if forCells < cols {
		// Too narrow for a stable table — fall back to the raw source.
		lines := wrapTextWithAnsi(token.Raw, availableWidth)
		if nextType != "" && nextType != TokSpace {
			lines = append(lines, "")
		}
		return lines
	}

	natural := make([]int, cols)
	minWord := make([]int, cols)
	headerText := make([]string, cols)
	for i := range token.Header {
		headerText[i] = m.renderInlineTokens(token.Header[i].Tokens, sc)
		natural[i] = visibleWidth(headerText[i])
		minWord[i] = max(1, longestWordWidth(headerText[i], maxUnbrokenWordWidth))
	}
	rowText := make([][]string, len(token.Rows))
	for r, row := range token.Rows {
		rowText[r] = make([]string, cols)
		for i := 0; i < cols && i < len(row); i++ {
			rowText[r][i] = m.renderInlineTokens(row[i].Tokens, sc)
			natural[i] = max(natural[i], visibleWidth(rowText[r][i]))
			minWord[i] = max(minWord[i], longestWordWidth(rowText[r][i], maxUnbrokenWordWidth))
		}
	}

	minWidths := minWord
	minTotal := sum(minWidths)
	if minTotal > forCells {
		// Even the floors do not fit: give every column one cell and share
		// what is left in proportion to what each wanted.
		minWidths = make([]int, cols)
		for i := range minWidths {
			minWidths[i] = 1
		}
		if remaining := forCells - cols; remaining > 0 {
			totalWeight := 0
			for _, w := range minWord {
				totalWeight += max(0, w-1)
			}
			allocated := 0
			for i := range minWidths {
				if totalWeight > 0 {
					grow := max(0, minWord[i]-1) * remaining / totalWeight
					minWidths[i] += grow
					allocated += grow
				}
			}
			for i := 0; allocated < remaining && i < cols; i++ {
				minWidths[i]++
				allocated++
			}
		}
		minTotal = sum(minWidths)
	}

	widths := make([]int, cols)
	if sum(natural)+overhead <= availableWidth {
		for i := range widths {
			widths[i] = max(natural[i], minWidths[i])
		}
	} else {
		growPotential := 0
		for i := range natural {
			growPotential += max(0, natural[i]-minWidths[i])
		}
		extra := max(0, forCells-minTotal)
		for i := range widths {
			grow := 0
			if growPotential > 0 {
				grow = max(0, natural[i]-minWidths[i]) * extra / growPotential
			}
			widths[i] = minWidths[i] + grow
		}
		// Hand out the cells lost to integer division.
		for remaining := forCells - sum(widths); remaining > 0; {
			grew := false
			for i := 0; i < cols && remaining > 0; i++ {
				if widths[i] < natural[i] {
					widths[i]++
					remaining--
					grew = true
				}
			}
			if !grew {
				break
			}
		}
	}

	var lines []string
	rule := func(left, mid, right string) string {
		parts := make([]string, cols)
		for i, w := range widths {
			parts[i] = strings.Repeat("─", w)
		}
		return left + strings.Join(parts, mid) + right
	}
	lines = append(lines, rule("┌─", "─┬─", "─┐"))
	lines = append(lines, m.tableRow(headerText, widths, token.Align, true)...)
	separator := rule("├─", "─┼─", "─┤")
	lines = append(lines, separator)
	for r := range rowText {
		lines = append(lines, m.tableRow(rowText[r], widths, token.Align, false)...)
		if r < len(rowText)-1 {
			lines = append(lines, separator)
		}
	}
	lines = append(lines, rule("└─", "─┴─", "─┘"))

	if nextType != "" && nextType != TokSpace {
		lines = append(lines, "")
	}
	return lines
}

// tableRow wraps every cell to its column and emits as many physical lines as
// the tallest cell needs.
func (m *Markdown) tableRow(cells []string, widths []int, align []Align, header bool) []string {
	wrapped := make([][]string, len(widths))
	height := 1
	for i := range widths {
		text := ""
		if i < len(cells) {
			text = cells[i]
		}
		wrapped[i] = wrapTextWithAnsi(text, max(1, widths[i]))
		height = max(height, len(wrapped[i]))
	}

	lines := make([]string, 0, height)
	for row := 0; row < height; row++ {
		parts := make([]string, len(widths))
		for i := range widths {
			text := ""
			if row < len(wrapped[i]) {
				text = wrapped[i][row]
			}
			a := AlignNone
			if i < len(align) {
				a = align[i]
			}
			parts[i] = padCell(text, widths[i], a)
			if header {
				parts[i] = m.Theme.Bold(parts[i])
			}
		}
		lines = append(lines, "│ "+strings.Join(parts, " │ ")+" │")
	}
	return lines
}

// padCell pads a cell to its column width, honouring the column's alignment.
func padCell(text string, width int, align Align) string {
	gap := max(0, width-visibleWidth(text))
	switch align {
	case AlignRight:
		return strings.Repeat(" ", gap) + text
	case AlignCenter:
		left := gap / 2
		return strings.Repeat(" ", left) + text + strings.Repeat(" ", gap-left)
	default:
		return text + strings.Repeat(" ", gap)
	}
}

// longestWordWidth is the width of the widest whitespace-delimited word,
// capped at maxWidth.
func longestWordWidth(text string, maxWidth int) int {
	longest := 0
	for _, word := range strings.Fields(text) {
		longest = max(longest, visibleWidth(word))
	}
	return min(longest, maxWidth)
}

func sum(xs []int) int {
	total := 0
	for _, x := range xs {
		total += x
	}
	return total
}
