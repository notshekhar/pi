package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// How a tool call is summarised on one line — the single source of truth,
// ported from loop's tool-summary.ts.
//
// The summary is what a row shows instead of its arguments: `read src/app.ts`
// rather than a JSON blob. It has to fit on one line and stay readable when
// the argument is a 400-character bash pipeline.

// rel shortens an absolute path under cwd to a repo-relative one.
func rel(p any, cwd string) string {
	s, ok := p.(string)
	if !ok {
		return ""
	}
	if cwd != "" && strings.HasPrefix(s, cwd) {
		trimmed := strings.TrimPrefix(strings.TrimPrefix(s, cwd), "/")
		if trimmed == "" {
			return "."
		}
		return trimmed
	}
	return s
}

// str pulls a string argument, falling back through alternative names.
func str(args map[string]any, names ...string) any {
	for _, n := range names {
		if v, ok := args[n]; ok {
			return v
		}
	}
	return nil
}

// clip shortens s to max cells with an ellipsis.
func clip(s string, max int) string {
	if visibleWidth(s) <= max {
		return s
	}
	return truncateToWidth(s, max-1, "") + "…"
}

// summaryMax is loop's SUMMARY_MAX: the row is cut to the terminal's width
// when drawn; this only bounds a pathological argument.
const summaryMax = 1000

// BackgroundSuffix marks a bash call that started a background shell.
const BackgroundSuffix = " (background)"

func clamp(text string) string {
	r := []rune(text)
	if len(r) > summaryMax {
		return string(r[:summaryMax-1]) + "…"
	}
	return text
}

func argStr(args map[string]any, name string) string {
	s, _ := args[name].(string)
	return s
}

func joinNonEmpty(sep string, parts ...string) string {
	var kept []string
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

var spaceRun = regexp.MustCompile(`\s+`)

// FormatToolArgs is loop's formatToolArgs: the one-line argument summary for
// a tool call ("" when there is nothing useful to show).
func FormatToolArgs(toolName string, args map[string]any, cwd string) string {
	if len(args) == 0 {
		return ""
	}
	if ToolSummaryHook != nil {
		if custom, ok := ToolSummaryHook(toolName, args, cwd); ok {
			return custom
		}
	}
	first := func(s string) string { return strings.SplitN(s, "\n", 2)[0] }
	switch toolName {
	case "read", "write", "edit", "ls":
		return rel(str(args, "path", "file_path", "filePath"), cwd)
	case "bash":
		bg := ""
		if args["run_in_background"] == true {
			bg = BackgroundSuffix
		}
		return clamp(first(argStr(args, "command"))) + bg
	case "shells":
		action := argStr(args, "action")
		if action == "list" {
			return "list"
		}
		return joinNonEmpty(" ", action, argStr(args, "id"))
	case "grep":
		pattern, _ := args["pattern"].(string)
		return joinNonEmpty(" in ", pattern, rel(args["path"], cwd))
	case "find", "glob":
		return argStr(args, "pattern")
	case "websearch":
		return argStr(args, "query")
	case "skill":
		return argStr(args, "name")
	case "sql":
		q := strings.TrimSpace(spaceRun.ReplaceAllString(argStr(args, "query"), " "))
		return joinNonEmpty(" · ", argStr(args, "connectionId"), clamp(q))
	case "task":
		agent := argStr(args, "agent")
		if agent == "" {
			agent = "default"
		}
		if prompt := clamp(first(argStr(args, "prompt"))); prompt != "" {
			return agent + " · " + prompt
		}
		return agent
	case "spawn_threads":
		threads, _ := args["threads"].([]any)
		var titles []string
		for _, th := range threads {
			if m, ok := th.(map[string]any); ok {
				if t, ok := m["title"].(string); ok && t != "" {
					titles = append(titles, t)
				}
			}
		}
		if len(titles) == 0 {
			return ""
		}
		noun := "threads"
		if len(titles) == 1 {
			noun = "thread"
		}
		return fmt.Sprintf("%d %s · %s", len(titles), noun, clamp(strings.Join(titles, ", ")))
	case "send_message":
		to := argStr(args, "to")
		who := to
		if to == "all" {
			who = "everyone"
		} else if to != "" {
			who = `"` + to + `"`
		}
		arrow := ""
		if who != "" {
			arrow = "→ " + who
		}
		msg := strings.TrimSpace(spaceRun.ReplaceAllString(argStr(args, "message"), " "))
		return joinNonEmpty(" · ", arrow, clamp(msg))
	case "team_board":
		return joinNonEmpty(" ", argStr(args, "action"), argStr(args, "key"))
	case "wait_for_team":
		switch until := args["until"].(type) {
		case []any:
			var ids []string
			for _, id := range until {
				ids = append(ids, `"`+fmt.Sprint(id)+`"`)
			}
			return "for " + strings.Join(ids, ", ")
		case string:
			if until == "any" {
				return "for the next message"
			}
		}
		return "for every thread"
	case "report":
		return clamp(first(argStr(args, "summary")))
	case "plan", "exit_plan_mode":
		return clamp(strings.TrimSpace(headingPrefix.ReplaceAllString(first(argStr(args, "plan")), "")))
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return ""
	}
	return clamp(string(encoded))
}

var headingPrefix = regexp.MustCompile(`^#+\s*`)

// ToolSummaryHook lets extensions render their own tools' summaries
// (loop's renderToolSummary); ok false falls through to the built-ins.
var ToolSummaryHook func(toolName string, args map[string]any, cwd string) (string, bool)

// ReadLineRangeText is the `:120-180` suffix a ranged read shows. Modes only
// get the path, so without this an offset read is indistinguishable from a
// whole-file one.
func ReadLineRangeText(args map[string]any) string {
	offset, hasOffset := numArg(args, "offset")
	limit, hasLimit := numArg(args, "limit")
	if !hasOffset && !hasLimit {
		return ""
	}
	start := 1
	if hasOffset {
		start = offset
	}
	if !hasLimit {
		return fmt.Sprintf(":%d", start)
	}
	return fmt.Sprintf(":%d-%d", start, start+limit-1)
}

// numArg reads a numeric argument. JSON decoding gives float64, but a
// hand-built map may hold an int.
func numArg(args map[string]any, name string) (int, bool) {
	switch v := args[name].(type) {
	case float64:
		return int(v), true
	case int:
		return v, true
	case int64:
		return int(v), true
	}
	return 0, false
}

// readNotice matches a `[…]`-only result line — the read tool's own notices,
// never file content.
var readNotice = regexp.MustCompile(`^\[.*\]$`)

// ReadGutterPrefixes are right-aligned absolute line numbers for a read
// result, one prefix per output line ("" = do not number this line).
//
// Numbering starts at the call's offset, not at 1: an offset read shows the
// file's real line numbers, which is the whole point — a preview that
// restarts at 1 cannot be matched back to the file it came from.
func ReadGutterPrefixes(lines []string, args map[string]any) []string {
	if len(lines) == 0 {
		return nil
	}
	if len(lines) == 1 && readNotice.MatchString(strings.TrimSpace(lines[0])) {
		return []string{""}
	}
	base := 1
	if n, ok := numArg(args, "offset"); ok {
		base = n
	}
	// The tool appends its own "[N more lines…]" notice after a blank line;
	// neither the notice nor its separator is file content.
	bodyEnd := len(lines)
	if bodyEnd >= 2 && readNotice.MatchString(strings.TrimSpace(lines[bodyEnd-1])) &&
		strings.TrimSpace(lines[bodyEnd-2]) == "" {
		bodyEnd -= 2
	}
	width := len(fmt.Sprint(base + max(0, bodyEnd-1)))
	out := make([]string, len(lines))
	for i := range lines {
		if i < bodyEnd {
			out[i] = fmt.Sprintf("%*d  ", width, base+i)
		}
	}
	return out
}

// streamingKeys are the argument a tool's live input tail should show, per
// tool. A `write` streams its file body; a `bash` streams its command.
var streamingKeys = map[string][]string{
	"write": {"content"},
	"edit":  {"new_string", "newString"},
	"bash":  {"command"},
}

// StreamingPreview pulls the interesting argument out of a tool call's
// half-arrived JSON, so a long write shows its body landing rather than a
// frozen row.
//
// The input is invalid JSON by definition — it is still being written — so
// this scans for the key and decodes forward, stopping wherever the text
// currently ends.
func StreamingPreview(toolName, raw string) string {
	keys, ok := streamingKeys[toolName]
	if !ok {
		return ""
	}
	for _, key := range keys {
		marker := `"` + key + `":`
		i := strings.Index(raw, marker)
		if i < 0 {
			continue
		}
		rest := strings.TrimLeft(raw[i+len(marker):], " \t\n")
		if !strings.HasPrefix(rest, `"`) {
			continue
		}
		return decodePartialJSONString(rest[1:])
	}
	return ""
}

// decodePartialJSONString decodes a JSON string body up to its closing quote
// or the end of what has arrived, whichever comes first.
func decodePartialJSONString(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '"' {
			break
		}
		if c != '\\' {
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(s) {
			break // a half-arrived escape: drop it rather than print a stray \
		}
		i++
		switch s[i] {
		case 'n':
			b.WriteByte('\n')
		case 't':
			b.WriteByte('\t')
		case 'r':
		case 'u':
			// \uXXXX — skip if the whole escape has not landed yet.
			if i+4 >= len(s) {
				return b.String()
			}
			var r rune
			if _, err := fmt.Sscanf(s[i+1:i+5], "%04x", &r); err == nil {
				b.WriteRune(r)
			}
			i += 4
		default:
			b.WriteByte(s[i])
		}
	}
	return b.String()
}
