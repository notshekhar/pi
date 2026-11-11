package tools

import (
	"fmt"
	"strings"
)

// The diff edit and write show the user, loop's generateDiffString: changed
// lines with their line numbers, a few lines of context either side, and
// "..." where unchanged lines are skipped.

// diffContextLines is how many unchanged lines are shown beside a change.
const diffContextLines = 4

// maxDiffDistance bounds the search for a minimal diff. The search keeps one
// row per edit, so its memory grows with the square of the number of changed
// lines; past this many, the changed region is shown as a plain replacement.
const maxDiffDistance = 2000

type diffKind int8

const (
	diffEqual diffKind = iota
	diffRemoved
	diffAdded
)

// diffPart is a run of consecutive lines that share one kind.
type diffPart struct {
	kind  diffKind
	lines []string
}

// splitKeepingNewlines cuts text into lines that keep their "\n", so a last
// line with no newline differs from the same line with one.
func splitKeepingNewlines(text string) []string {
	if text == "" {
		return nil
	}
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// diffLines compares two texts line by line.
func diffLines(oldText, newText string) []diffPart {
	a, b := splitKeepingNewlines(oldText), splitKeepingNewlines(newText)
	prefix := 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-1-suffix] == b[len(b)-1-suffix] {
		suffix++
	}
	var parts []diffPart
	parts = appendPart(parts, diffEqual, a[:prefix])
	parts = append(parts, middleParts(a[prefix:len(a)-suffix], b[prefix:len(b)-suffix])...)
	return appendPart(parts, diffEqual, a[len(a)-suffix:])
}

func appendPart(parts []diffPart, kind diffKind, lines []string) []diffPart {
	if len(lines) == 0 {
		return parts
	}
	if n := len(parts); n > 0 && parts[n-1].kind == kind {
		parts[n-1].lines = append(parts[n-1].lines, lines...)
		return parts
	}
	return append(parts, diffPart{kind: kind, lines: append([]string(nil), lines...)})
}

// middleParts diffs the region between the common prefix and suffix. Within
// one changed hunk, removed lines come before added ones.
func middleParts(a, b []string) []diffPart {
	ops, ok := shortestEdit(a, b)
	if !ok {
		return appendPart(appendPart(nil, diffRemoved, a), diffAdded, b)
	}
	var parts []diffPart
	var removed, added []string
	flush := func() {
		parts = appendPart(appendPart(parts, diffRemoved, removed), diffAdded, added)
		removed, added = nil, nil
	}
	x, y := 0, 0
	for _, op := range ops {
		switch op {
		case diffEqual:
			flush()
			parts = appendPart(parts, diffEqual, a[x:x+1])
			x, y = x+1, y+1
		case diffRemoved:
			removed = append(removed, a[x])
			x++
		case diffAdded:
			added = append(added, b[y])
			y++
		}
	}
	flush()
	return parts
}

// shortestEdit is Myers' algorithm: the fewest line removals and additions
// that turn a into b. ok is false when that takes more than maxDiffDistance.
func shortestEdit(a, b []string) (ops []diffKind, ok bool) {
	n, m := len(a), len(b)
	limit := min(n+m, maxDiffDistance)
	// furthest[k+limit] is the furthest x reached on diagonal k = x - y.
	furthest := make([]int32, 2*limit+2)
	var history [][]int32
	for d := 0; d <= limit; d++ {
		history = append(history, append([]int32(nil), furthest...))
		for k := -d; k <= d; k += 2 {
			x := nextX(furthest, k, d, limit)
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x, y = x+1, y+1
			}
			furthest[k+limit] = int32(x)
			if x >= n && y >= m {
				return backtrack(history, n, m, limit), true
			}
		}
	}
	return nil, false
}

// nextX picks the better of stepping down (an addition) or right (a removal)
// onto diagonal k.
func nextX(furthest []int32, k, d, limit int) int {
	if k == -d || (k != d && furthest[k-1+limit] < furthest[k+1+limit]) {
		return int(furthest[k+1+limit])
	}
	return int(furthest[k-1+limit]) + 1
}

func backtrack(history [][]int32, n, m, limit int) []diffKind {
	var reversed []diffKind
	x, y := n, m
	for d := len(history) - 1; d > 0; d-- {
		before := history[d]
		k := x - y
		prevK := k - 1
		if k == -d || (k != d && before[k-1+limit] < before[k+1+limit]) {
			prevK = k + 1
		}
		prevX := int(before[prevK+limit])
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			reversed = append(reversed, diffEqual)
			x, y = x-1, y-1
		}
		if x == prevX {
			reversed = append(reversed, diffAdded)
			y--
		} else {
			reversed = append(reversed, diffRemoved)
			x--
		}
	}
	for ; x > 0 && y > 0; x, y = x-1, y-1 {
		reversed = append(reversed, diffEqual)
	}
	ops := make([]diffKind, len(reversed))
	for i, op := range reversed {
		ops[len(reversed)-1-i] = op
	}
	return ops
}

// diffWriter renders parts as numbered lines.
type diffWriter struct {
	out           []string
	width         int
	oldLine       int
	newLine       int
	lastWasChange bool
}

func (w *diffWriter) changed(part diffPart) {
	for _, line := range part.lines {
		line = strings.TrimSuffix(line, "\n")
		if part.kind == diffAdded {
			w.out = append(w.out, fmt.Sprintf("+%*d %s", w.width, w.newLine, line))
			w.newLine++
		} else {
			w.out = append(w.out, fmt.Sprintf("-%*d %s", w.width, w.oldLine, line))
			w.oldLine++
		}
	}
	w.lastWasChange = true
}

func (w *diffWriter) show(lines []string) {
	for _, line := range lines {
		w.out = append(w.out, fmt.Sprintf(" %*d %s", w.width, w.oldLine, strings.TrimSuffix(line, "\n")))
		w.oldLine++
		w.newLine++
	}
}

func (w *diffWriter) skip(count int) {
	if count <= 0 {
		return
	}
	w.out = append(w.out, fmt.Sprintf(" %*s ...", w.width, ""))
	w.oldLine += count
	w.newLine += count
}

// context shows the unchanged lines next to a change and skips the rest.
func (w *diffWriter) context(lines []string, changeFollows bool) {
	changeBefore := w.lastWasChange
	w.lastWasChange = false
	switch {
	case changeBefore && changeFollows:
		if len(lines) <= diffContextLines*2 {
			w.show(lines)
			return
		}
		w.show(lines[:diffContextLines])
		w.skip(len(lines) - diffContextLines*2)
		w.show(lines[len(lines)-diffContextLines:])
	case changeBefore:
		shown := min(len(lines), diffContextLines)
		w.show(lines[:shown])
		w.skip(len(lines) - shown)
	case changeFollows:
		skipped := max(0, len(lines)-diffContextLines)
		w.skip(skipped)
		w.show(lines[skipped:])
	default:
		w.oldLine += len(lines)
		w.newLine += len(lines)
	}
}

// generateDiff renders what changed between two versions of a file.
func generateDiff(oldContent, newContent string) string {
	longest := max(strings.Count(oldContent, "\n"), strings.Count(newContent, "\n")) + 1
	w := &diffWriter{width: len(fmt.Sprint(longest)), oldLine: 1, newLine: 1}
	parts := diffLines(oldContent, newContent)
	for i, part := range parts {
		if part.kind != diffEqual {
			w.changed(part)
			continue
		}
		w.context(part.lines, i < len(parts)-1)
	}
	return strings.Join(w.out, "\n")
}
