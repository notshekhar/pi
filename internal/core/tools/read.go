package tools

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/docs"
)

// loopScheme is the URI scheme of the read tool's internal sources. It stays
// "loop" in pi: the docs it serves are loop's own and describe ~/.loop, the
// home both agents share.
const loopScheme = "loop://"

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset,omitzero"`
	Limit  int    `json:"limit,omitzero"`
}

// Read returns the read tool: a local file, a URL, or loop's internal docs.
func Read(t *Context) ai.Tool {
	return LoopTool("read", func(ctx context.Context, a readArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		trimmed := strings.TrimSpace(a.Path)
		if strings.HasPrefix(strings.ToLower(trimmed), loopScheme) {
			return ai.ToolText(readLoopURI(ctx, trimmed)), nil
		}
		if IsHTTPURL(trimmed) {
			return ai.ToolText(FetchURLAsText(ctx, trimmed)), nil
		}
		text, err := readFile(ctx, t, a)
		if err != nil {
			return fail(err)
		}
		return ai.ToolText(text), nil
	})
}

// readLoopURI resolves loop://docs, loop://docs/<name>.md and loop://fetch/<url>.
func readLoopURI(ctx context.Context, uri string) string {
	rest := uri[len(loopScheme):]
	switch {
	case rest == "docs" || rest == "docs/":
		return docs.RenderIndex()
	case strings.HasPrefix(rest, "docs/"):
		name := strings.TrimPrefix(rest, "docs/")
		doc, ok := docs.Get(name)
		if !ok {
			return fmt.Sprintf("[no such doc: %s]\n\n%s", name, docs.RenderIndex())
		}
		return doc.Content
	case strings.HasPrefix(rest, "fetch/"):
		url := strings.TrimSpace(strings.TrimPrefix(rest, "fetch/"))
		if !IsHTTPURL(url) {
			return fmt.Sprintf("[loop://fetch/ expects an http(s):// URL, got: %s]", url)
		}
		return FetchURLAsText(ctx, url)
	}
	return fmt.Sprintf("[unknown loop:// path: %s. Use loop://docs, loop://docs/<name>.md, or loop://fetch/<url>]", uri)
}

var imageExt = regexp.MustCompile(`(?i)\.(jpe?g|png|gif|webp)$`)

func imageMimeType(path string) string {
	m := imageExt.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	switch ext := strings.ToLower(m[1]); ext {
	case "jpg", "jpeg":
		return "image/jpeg"
	default:
		return "image/" + ext
	}
}

func readFile(ctx context.Context, t *Context, a readArgs) (string, error) {
	path := ResolveRead(a.Path, t.CWD)
	// Stat before reading: the mtime recorded for read-before-modify has to
	// predate the content, and a directory needs its own message.
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s is a directory, not a file. Use ls to list its contents, or find to match files inside it.", a.Path)
	}
	if mime := imageMimeType(path); mime != "" {
		return fmt.Sprintf("[image file: %s at %s. Image rendering not supported in this tool result; use bash 'file' or vision-capable read elsewhere.]", mime, path), nil
	}
	binary, err := looksBinary(path)
	if err != nil {
		return "", err
	}
	if binary {
		return fmt.Sprintf("[binary file: %s at %s. Not printed as text; inspect it with bash (file, xxd, strings).]", FormatSize(info.Size()), path), nil
	}
	sel, err := selectLines(ctx, path, selectOptions{offset: a.Offset, limit: a.Limit, size: info.Size()})
	if err != nil {
		return "", err
	}
	// Unlocks edit for this file. write needs the whole file covered, which
	// successive offset= reads add up to.
	end := sel.startLine + len(sel.lines) - 1
	t.Registry.RecordRead(path, info.ModTime().UnixNano(), sel.startLine, end, !sel.hasMore)
	return renderSelection(sel, a.Path), nil
}

func renderSelection(sel lineSelection, displayPath string) string {
	if sel.firstLineBytes > 0 {
		measured := FormatSize(int64(sel.firstLineBytes))
		if sel.firstLineClipped {
			measured = "over " + measured
		}
		return fmt.Sprintf("[Line %d is %s, exceeds %s limit. Use bash: sed -n '%dp' %s | head -c %d]",
			sel.startLine, measured, FormatSize(DefaultMaxBytes), sel.startLine, shellQuote(displayPath), DefaultMaxBytes)
	}
	text := strings.Join(sel.lines, "\n")
	if sel.trailingNewline {
		text += "\n"
	}
	if !sel.hasMore {
		return text
	}
	return text + "\n\n" + continuationNotice(sel)
}

// continuationNotice tells the model where to carry on. "of N" appears only
// when the file was scanned end to end — a streamed read of a huge file stops
// early and never learns the total.
func continuationNotice(sel lineSelection) string {
	end := sel.startLine + len(sel.lines) - 1
	next := end + 1
	ofTotal := ""
	if sel.totalLines >= 0 {
		ofTotal = fmt.Sprintf(" of %d", sel.totalLines)
	}
	switch {
	case sel.truncatedBy == "lines":
		return fmt.Sprintf("[Showing lines %d-%d%s. Use offset=%d to continue.]", sel.startLine, end, ofTotal, next)
	case sel.truncatedBy == "bytes":
		return fmt.Sprintf("[Showing lines %d-%d%s (%s limit). Use offset=%d to continue.]", sel.startLine, end, ofTotal, FormatSize(DefaultMaxBytes), next)
	case sel.totalLines >= 0:
		remaining := sel.totalLines - end
		plural := "s"
		if remaining == 1 {
			plural = ""
		}
		return fmt.Sprintf("[%d more line%s in file. Use offset=%d to continue.]", remaining, plural, next)
	}
	return fmt.Sprintf("[More lines in file. Use offset=%d to continue.]", next)
}

// shellQuote single-quotes a path for the bash hint in a truncation notice.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}
