package tools

import (
	"bufio"
	"context"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/notshekhar/pi/internal/modules/ai"
)

const (
	defaultMaxLines = 2000
	defaultMaxBytes = 64 << 10
)

type readArgs struct {
	Path   string `json:"path" jsonschema:"description=Local file path, relative or absolute"`
	Offset *int   `json:"offset,omitempty" jsonschema:"description=1-indexed line to start at,minimum=1"`
	Limit  *int   `json:"limit,omitempty" jsonschema:"description=Maximum number of lines to read,minimum=1"`
}

// Read returns the read tool.
func Read(t *Context) ai.Tool {
	return ai.NewTool("read",
		"Read a file. Large files are truncated; continue with offset= until complete. Directories need ls or glob. Read a file before editing it.",
		func(ctx context.Context, a readArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			path := Resolve(a.Path, t.CWD)
			info, err := os.Stat(path)
			if err != nil {
				return ai.ToolErrorf("could not read %s: %v", a.Path, err), nil
			}
			if info.IsDir() {
				return ai.ToolErrorf("%s is a directory. Use ls to list it, or glob to match files inside it.", a.Path), nil
			}
			if !utf8SafeFile(path) {
				return ai.ToolErrorf("binary file (%s) at %s. Inspect it with bash (file, xxd, strings).", formatSize(info.Size()), path), nil
			}

			mtime := info.ModTime().UnixNano()
			offset := 1
			if a.Offset != nil && *a.Offset > 0 {
				offset = *a.Offset
			}
			limit := defaultMaxLines
			if a.Limit != nil && *a.Limit > 0 {
				limit = *a.Limit
			}

			f, err := os.Open(path)
			if err != nil {
				return ai.ToolErrorf("could not read %s: %v", a.Path, err), nil
			}
			defer f.Close()

			scan := bufio.NewScanner(f)
			scan.Buffer(nil, 1<<20)
			var (
				lines   []string
				lineNo  int
				bytes   int
				hasMore bool
				sawEOF  = true
				start   int
			)
			for scan.Scan() {
				lineNo++
				if lineNo < offset {
					continue
				}
				if start == 0 {
					start = lineNo
				}
				if len(lines) >= limit || bytes+len(scan.Bytes()) > defaultMaxBytes {
					hasMore = true
					sawEOF = false
					break
				}
				lines = append(lines, scan.Text())
				bytes += len(scan.Bytes()) + 1
			}
			if err := scan.Err(); err != nil {
				return ai.ToolErrorf("reading %s: %v", a.Path, err), nil
			}
			if start == 0 {
				start = offset
			}
			end := start
			if len(lines) > 0 {
				end = start + len(lines) - 1
			}
			t.Registry.RecordRead(path, mtime, start, end, sawEOF && !hasMore)

			text := strings.Join(lines, "\n")
			if hasMore {
				next := end + 1
				text += "\n\n[Showing lines " + itoa(start) + "-" + itoa(end) + ". Use offset=" + itoa(next) + " to continue.]"
			}
			return ai.ToolText(text), nil
		})
}

func utf8SafeFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	buf := make([]byte, 8000)
	n, _ := f.Read(buf)
	if n == 0 {
		return true
	}
	if strings.IndexByte(string(buf[:n]), 0) >= 0 {
		return false
	}
	return utf8.Valid(buf[:n]) || utf8.RuneCount(buf[:n]) > 0
}

func formatSize(n int64) string {
	switch {
	case n < 1024:
		return itoa(int(n)) + "B"
	case n < 1024*1024:
		return itoa(int(n/1024)) + "KB"
	default:
		return itoa(int(n/(1024*1024))) + "MB"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
