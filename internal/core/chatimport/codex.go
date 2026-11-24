package chatimport

import (
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
)

// Codex chats: ~/.codex/sessions/YYYY/MM/DD/rollout-<time>-<id>.jsonl. The
// first record, session_meta, carries the chat's id and working directory —
// the only way to know a chat's folder, since files are filed by date. The
// conversation is the response_item records, the Responses API's own items.
// loop's codex.ts.

type codexSource struct{ sessions string }

type codexFile struct{ id, path, cwd string }

// Codex reads Codex's chats under home.
func Codex(home string) Source {
	return codexSource{sessions: filepath.Join(home, ".codex", "sessions")}
}

func (codexSource) Label() string { return "Codex" }

func (c codexSource) files() []codexFile {
	var out []codexFile
	_ = filepath.WalkDir(c.sessions, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		for _, r := range readJSONLHead(path, 64*1024) {
			if str(r, "type") == "session_meta" {
				meta := obj(r, "payload")
				if id, cwd := str(meta, "id"), str(meta, "cwd"); id != "" && cwd != "" {
					out = append(out, codexFile{id, path, cwd})
				}
				break
			}
		}
		return nil
	})
	return out
}

func (c codexSource) inFolder(cwd string) []codexFile {
	var out []codexFile
	for _, f := range c.files() {
		if filepath.Clean(f.cwd) == filepath.Clean(cwd) {
			out = append(out, f)
		}
	}
	return out
}

func (c codexSource) List(cwd string) []Summary {
	var out []Summary
	for _, f := range c.inFolder(cwd) {
		if prompt := firstPrompt(readJSONLHead(f.path, 256*1024), codexPrompt); prompt != "" {
			out = append(out, Summary{ID: f.id, Title: title(prompt), UpdatedAt: modTime(f.path)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (c codexSource) Load(id, cwd string) (*Chat, error) {
	idOf := func(f codexFile) string { return f.id }
	f, ok := matchID(c.inFolder(cwd), id, idOf)
	if !ok {
		f, ok = matchID(c.files(), id, idOf)
	}
	if !ok {
		return nil, nil
	}
	records := readJSONL(f.path)
	var b Builder
	for _, r := range records {
		if str(r, "type") == "response_item" {
			addCodexItem(&b, obj(r, "payload"), r, ParseTime(r["timestamp"]))
		}
	}
	return &Chat{ID: f.id, Title: title(firstPrompt(records, codexPrompt)), Entries: b.Build()}, nil
}

func codexPrompt(r map[string]any) string {
	item := obj(r, "payload")
	if str(r, "type") != "response_item" || str(item, "type") != "message" || str(item, "role") != "user" {
		return ""
	}
	var prompts []string
	for _, p := range list(item, "content") {
		if text := UserPrompt(str(p, "text")); text != "" {
			prompts = append(prompts, text)
		}
	}
	return strings.Join(prompts, "\n\n")
}

func codexText(parts []map[string]any) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(str(p, "text"))
	}
	return b.String()
}

func addCodexItem(b *Builder, item, record map[string]any, ts int64) {
	switch str(item, "type") {
	case "message":
		switch str(item, "role") {
		case "user":
			b.User(codexPrompt(record), ts)
		case "assistant":
			b.Text(codexText(list(item, "content")), ts)
		}
	case "reasoning":
		text := codexText(list(item, "summary"))
		if text == "" {
			text = codexText(list(item, "content"))
		}
		b.Reasoning(text, ts)
	case "function_call":
		addCodexCall(b, str(item, "call_id"), str(item, "name"), ParseJSONish(item["arguments"]), ts)
	case "custom_tool_call":
		addCodexCall(b, str(item, "call_id"), str(item, "name"), map[string]any{"input": item["input"]}, ts)
	case "local_shell_call":
		addCodexCall(b, str(item, "call_id"), "shell", map[string]any{"command": obj(item, "action")["command"]}, ts)
	case "function_call_output", "custom_tool_call_output", "local_shell_call_output":
		if id := str(item, "call_id"); id != "" {
			b.ToolResult(id, codexResult(item["output"]), false, ts)
		}
	}
}

func addCodexCall(b *Builder, id, name string, input any, ts int64) {
	if id == "" || name == "" {
		return
	}
	tool, translated := TranslateCall(name, input)
	b.ToolCall(id, tool, translated, ts)
}

// codexResult: older Codex wrapped output as {"output": "...", "metadata": {...}}.
func codexResult(output any) string {
	parsed := ParseJSONish(output)
	if m, ok := parsed.(map[string]any); ok {
		if inner, has := m["output"]; has {
			return OutputText(inner)
		}
	}
	return OutputText(parsed)
}
