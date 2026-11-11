package tools

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Output limits, loop's tools/utils/truncate.ts. Two independent caps;
// whichever is hit first wins.
const (
	DefaultMaxLines = 2000
	DefaultMaxBytes = 50 * 1024
	// GrepMaxLineLength caps one grep match line, in characters.
	GrepMaxLineLength = 500
)

// allLines disables the line cap for output that is only bounded by bytes.
const allLines = int(^uint(0) >> 1)

// Truncation is what a truncate call kept and why it stopped.
type Truncation struct {
	Content     string
	Truncated   bool
	TruncatedBy string // "lines", "bytes", or "" when not truncated
	TotalLines  int
	TotalBytes  int
	OutputLines int
	OutputBytes int
	// LastLinePartial is set when tail truncation had to cut inside a line.
	LastLinePartial bool
}

// FormatSize renders a byte count as loop does: 12B, 1.5KB, 2.0MB.
func FormatSize(bytes int64) string {
	switch {
	case bytes < 1024:
		return fmt.Sprintf("%dB", bytes)
	case bytes < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(bytes)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(bytes)/(1024*1024))
	}
}

func untruncated(content string, lines int) Truncation {
	return Truncation{Content: content, TotalLines: lines, TotalBytes: len(content), OutputLines: lines, OutputBytes: len(content)}
}

// TruncateHead keeps the first lines that fit. It never returns a partial
// line: when the first line alone is over the byte cap, nothing is kept.
func TruncateHead(content string, maxLines, maxBytes int) Truncation {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines && len(content) <= maxBytes {
		return untruncated(content, len(lines))
	}
	result := Truncation{Truncated: true, TruncatedBy: "lines", TotalLines: len(lines), TotalBytes: len(content)}
	var kept []string
	bytes := 0
	for i := 0; i < len(lines) && i < maxLines; i++ {
		cost := len(lines[i])
		if i > 0 {
			cost++ // the newline that joins it to the previous line
		}
		if bytes+cost > maxBytes {
			result.TruncatedBy = "bytes"
			break
		}
		kept = append(kept, lines[i])
		bytes += cost
	}
	result.Content = strings.Join(kept, "\n")
	result.OutputLines, result.OutputBytes = len(kept), len(result.Content)
	return result
}

// TruncateTail keeps the last lines that fit — for command output, where the
// end (errors, the final result) is the part worth reading. When even the
// last line is over the byte cap, its end is kept and LastLinePartial is set.
func TruncateTail(content string, maxLines, maxBytes int) Truncation {
	lines := strings.Split(content, "\n")
	if len(lines) <= maxLines && len(content) <= maxBytes {
		return untruncated(content, len(lines))
	}
	result := Truncation{Truncated: true, TruncatedBy: "lines", TotalLines: len(lines), TotalBytes: len(content)}
	var kept []string
	bytes := 0
	for i := len(lines) - 1; i >= 0 && len(kept) < maxLines; i-- {
		cost := len(lines[i])
		if len(kept) > 0 {
			cost++
		}
		if bytes+cost > maxBytes {
			result.TruncatedBy = "bytes"
			if len(kept) == 0 {
				kept = append(kept, lastBytes(lines[i], maxBytes))
				result.LastLinePartial = true
			}
			break
		}
		kept = append(kept, lines[i])
		bytes += cost
	}
	reverse(kept)
	result.Content = strings.Join(kept, "\n")
	result.OutputLines, result.OutputBytes = len(kept), len(result.Content)
	return result
}

// lastBytes is the end of s that fits in maxBytes, cut on a rune boundary.
func lastBytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	start := len(s) - maxBytes
	for start < len(s) && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func reverse(items []string) {
	for i, j := 0, len(items)-1; i < j; i, j = i+1, j-1 {
		items[i], items[j] = items[j], items[i]
	}
}

// TruncateLine caps one line at maxChars characters, marking the cut.
func TruncateLine(line string, maxChars int) (text string, wasTruncated bool) {
	if utf8.RuneCountInString(line) <= maxChars {
		return line, false
	}
	runes := []rune(line)
	return string(runes[:maxChars]) + "... [truncated]", true
}

// withNotices appends loop's bracketed footer, e.g. "[500 entries limit reached]".
func withNotices(output string, notices []string) string {
	if len(notices) == 0 {
		return output
	}
	return output + "\n\n[" + strings.Join(notices, ". ") + "]"
}
