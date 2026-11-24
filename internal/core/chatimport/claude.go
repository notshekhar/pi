package chatimport

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Claude Code chats: ~/.claude/projects/<folder>/<session id>.jsonl, where
// <folder> is the working directory with every non-alphanumeric character
// turned into "-". The conversation is the `user` / `assistant` records (one
// assistant message split across several, one per content block); records
// form a tree through parentUuid, so the chat is the path from the newest
// record back to the root — across a compaction the link is
// logicalParentUuid. loop's claude-code.ts.

var notAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

// ClaudeProjectFolder is Claude Code's folder name for a working directory.
func ClaudeProjectFolder(cwd string) string { return notAlnum.ReplaceAllString(cwd, "-") }

type claudeSource struct{ projects string }

// ClaudeCode reads Claude Code's chats under home.
func ClaudeCode(home string) Source {
	return claudeSource{projects: filepath.Join(home, ".claude", "projects")}
}

func (claudeSource) Label() string { return "Claude Code" }

func (c claudeSource) dirOf(cwd string) string {
	return filepath.Join(c.projects, ClaudeProjectFolder(cwd))
}

func (c claudeSource) List(cwd string) []Summary {
	var out []Summary
	for _, path := range jsonlFiles(c.dirOf(cwd)) {
		if s, ok := claudeSummary(path); ok {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (c claudeSource) Load(id, cwd string) (*Chat, error) {
	path, ok := matchID(jsonlFiles(c.dirOf(cwd)), id, chatIDOf)
	if !ok {
		path, ok = c.findAnywhere(id)
	}
	if !ok {
		return nil, nil
	}
	return readClaudeChat(path), nil
}

func (c claudeSource) findAnywhere(id string) (string, bool) {
	folders, _ := os.ReadDir(c.projects)
	for _, f := range folders {
		path := filepath.Join(c.projects, f.Name(), id+".jsonl")
		if _, err := os.Stat(path); err == nil {
			return path, true
		}
	}
	return "", false
}

func jsonlFiles(dir string) []string {
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	return out
}

func chatIDOf(path string) string { return strings.TrimSuffix(filepath.Base(path), ".jsonl") }

func modTime(path string) int64 {
	info, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return info.ModTime().UnixMilli()
}

// claudeSummary is a picker row from the head of the file; none for a file
// with no prompt in it (a bare /clear).
func claudeSummary(path string) (Summary, bool) {
	records := readJSONLHead(path, 256*1024)
	prompt := firstPrompt(records, claudePrompt)
	if prompt == "" {
		return Summary{}, false
	}
	return Summary{ID: chatIDOf(path), Title: title(claudeTitle(records, prompt)), UpdatedAt: modTime(path)}, true
}

func claudeTitle(records []map[string]any, fallback string) string {
	for i := len(records) - 1; i >= 0; i-- {
		if str(records[i], "type") == "ai-title" && str(records[i], "aiTitle") != "" {
			return str(records[i], "aiTitle")
		}
	}
	return fallback
}

func firstPrompt(records []map[string]any, promptOf func(map[string]any) string) string {
	for _, r := range records {
		if p := promptOf(r); p != "" {
			return p
		}
	}
	return ""
}

func isClaudeConversation(r map[string]any) bool {
	t := str(r, "type")
	return (t == "user" || t == "assistant") && r["isSidechain"] != true && r["isMeta"] != true && r["isCompactSummary"] != true
}

// claudeBranch is the path from the newest conversation record back to the
// root, oldest first.
func claudeBranch(records []map[string]any) []map[string]any {
	byID := map[string]map[string]any{}
	var leaf map[string]any
	for _, r := range records {
		if id := str(r, "uuid"); id != "" {
			byID[id] = r
			if isClaudeConversation(r) {
				leaf = r
			}
		}
	}
	var path []map[string]any
	seen := map[string]bool{}
	for at := leaf; at != nil && !seen[str(at, "uuid")]; {
		seen[str(at, "uuid")] = true
		if isClaudeConversation(at) {
			path = append(path, at)
		}
		parent := str(at, "parentUuid")
		if parent == "" {
			parent = str(at, "logicalParentUuid")
		}
		at = byID[parent]
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

func claudePrompt(r map[string]any) string {
	if str(r, "type") != "user" || !isClaudeConversation(r) {
		return ""
	}
	msg := obj(r, "message")
	if s, ok := msg["content"].(string); ok {
		return UserPrompt(s)
	}
	var texts []string
	for _, b := range list(msg, "content") {
		if str(b, "type") == "text" {
			texts = append(texts, str(b, "text"))
		}
	}
	return UserPrompt(strings.Join(texts, "\n"))
}

func readClaudeChat(path string) *Chat {
	records := readJSONL(path)
	var b Builder
	for _, r := range claudeBranch(records) {
		addClaudeRecord(&b, r)
	}
	return &Chat{ID: chatIDOf(path), Title: title(claudeTitle(records, firstPrompt(records, claudePrompt))), Entries: b.Build()}
}

func addClaudeRecord(b *Builder, r map[string]any) {
	ts := ParseTime(r["timestamp"])
	msg := obj(r, "message")
	if s, ok := msg["content"].(string); ok {
		if str(r, "type") == "user" {
			b.User(UserPrompt(s), ts)
		} else {
			b.Text(s, ts)
		}
		return
	}
	blocks := list(msg, "content")
	if str(r, "type") == "user" {
		addClaudeUserBlocks(b, blocks, ts)
		return
	}
	for _, block := range blocks {
		switch str(block, "type") {
		case "text":
			b.Text(str(block, "text"), ts)
		case "thinking":
			b.Reasoning(str(block, "thinking"), ts)
		case "tool_use":
			if id, name := str(block, "id"), str(block, "name"); id != "" && name != "" {
				tool, input := TranslateCall(name, block["input"])
				b.ToolCall(id, tool, input, ts)
			}
		}
	}
}

// addClaudeUserBlocks: the results a user record carries back, then whatever
// the person wrote as one prompt.
func addClaudeUserBlocks(b *Builder, blocks []map[string]any, ts int64) {
	var written []string
	for _, block := range blocks {
		switch str(block, "type") {
		case "tool_result":
			if id := str(block, "tool_use_id"); id != "" {
				b.ToolResult(id, OutputText(block["content"]), block["is_error"] == true, ts)
			}
		case "text":
			if p := UserPrompt(str(block, "text")); p != "" {
				written = append(written, p)
			}
		case "image":
			written = append(written, "[image]")
		}
	}
	b.User(strings.Join(written, "\n\n"), ts)
}
