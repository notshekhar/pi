package chatimport

import (
	"database/sql"
	"encoding/hex"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	_ "github.com/mattn/go-sqlite3"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
)

// Each reader against a fake home holding that agent's real on-disk format —
// never the user's own chats. loop's chat-import.test.ts, case for case.

const cwd = "/work/my.app"
const other = "/work/other"

type wirePart struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	ToolCallID string `json:"toolCallId"`
	ToolName   string `json:"toolName"`
	Input      any    `json:"input"`
	Output     struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"output"`
}

func partsOf(e session.Entry) []wirePart {
	var parts []wirePart
	_ = json.Unmarshal(e.Content, &parts, jsonopt.Read)
	return parts
}

func textOf(e session.Entry) string {
	var s string
	_ = json.Unmarshal(e.Content, &s, jsonopt.Read)
	return s
}

func roles(entries []session.Entry) []string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.Role
	}
	return out
}

// everyCallAnswered: each call has exactly one result, in the tool message
// right after its assistant message.
func everyCallAnswered(t *testing.T, entries []session.Entry) {
	t.Helper()
	for i, e := range entries {
		var calls []string
		for _, p := range partsOf(e) {
			if p.Type == "tool-call" {
				calls = append(calls, p.ToolCallID)
			}
		}
		if len(calls) == 0 {
			continue
		}
		if i+1 >= len(entries) || entries[i+1].Role != "tool" {
			t.Fatalf("calls %v have no tool message after them", calls)
		}
		var answered []string
		for _, p := range partsOf(entries[i+1]) {
			answered = append(answered, p.ToolCallID)
		}
		slices.Sort(calls)
		slices.Sort(answered)
		if !slices.Equal(calls, answered) {
			t.Fatalf("calls %v answered by %v", calls, answered)
		}
	}
}

func writeLines(t *testing.T, path string, records ...any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, r := range records {
		raw, _ := json.Marshal(r)
		b.Write(raw)
		b.WriteByte('\n')
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fingerprint is a file's bytes and mtime, to prove a reader never wrote it.
func fingerprint(t *testing.T, path string) string {
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	return fmt.Sprintf("%d:%x", info.ModTime().UnixNano(), data)
}

func TestBuilderAnswersEveryCall(t *testing.T) {
	var b Builder
	b.User("go", 0)
	b.ToolCall("a", "bash", map[string]any{"command": "ls"}, 0)
	b.ToolCall("b", "bash", map[string]any{"command": "pwd"}, 0)
	b.ToolResult("a", "files", false, 0)
	b.ToolResult("ghost", "nobody asked", false, 0)
	b.Text("done", 0)
	entries := b.Build()
	if got := roles(entries); !slices.Equal(got, []string{"user", "assistant", "tool", "assistant"}) {
		t.Fatalf("roles %v", got)
	}
	everyCallAnswered(t, entries)
	for _, p := range partsOf(entries[2]) {
		if p.ToolCallID == "b" && p.Output.Type != "error-text" {
			t.Errorf("a lost result should be a placeholder error, got %+v", p)
		}
	}
	for _, e := range entries {
		if strings.Contains(string(e.Content), "nobody asked") {
			t.Error("a result for no call was kept")
		}
	}
}

func TestUserPrompt(t *testing.T) {
	cases := map[string]string{
		"<timestamp>Mon</timestamp>\n<user_query>\nfix it\n</user_query>":      "fix it",
		"<agent_skills>…</agent_skills>\n<brand_new_tag a=1>x</brand_new_tag>": "",
		"<environment_context>\n  <cwd>/x</cwd>\n</environment_context>":       "",
		"<system-reminder>be nice</system-reminder>\nwhy is it slow?":          "why is it slow?",
		"<command-name>/clear</command-name>":                                  "",
		"# AGENTS.md instructions for /x\n\n<INSTRUCTIONS>…</INSTRUCTIONS>":    "",
		"what does <div> do here?":                                             "what does <div> do here?",
	}
	for in, want := range cases {
		if got := UserPrompt(in); got != want {
			t.Errorf("UserPrompt(%q) = %q, want %q", in, got, want)
		}
	}
}

func claudeRec(uuid, parent, typ string, content any, extra ...any) map[string]any {
	r := map[string]any{"uuid": uuid, "type": typ, "timestamp": "2026-11-01T10:00:00.000Z",
		"message": map[string]any{"role": typ, "content": content}}
	if parent != "" {
		r["parentUuid"] = parent
	}
	for i := 0; i+1 < len(extra); i += 2 {
		r[extra[i].(string)] = extra[i+1]
	}
	return r
}

func claudeChat() []any {
	return []any{
		claudeRec("u0", "", "user", "<command-name>/clear</command-name>"),
		claudeRec("u1", "u0", "user", "<system-reminder>ctx</system-reminder>\nwhat is in here?"),
		claudeRec("a1", "u1", "assistant", []any{map[string]any{"type": "thinking", "thinking": "look first"}}),
		claudeRec("a2", "a1", "assistant", []any{map[string]any{"type": "tool_use", "id": "t1", "name": "Bash", "input": map[string]any{"command": "ls"}}}),
		claudeRec("a3", "a2", "assistant", []any{map[string]any{"type": "tool_use", "id": "t2", "name": "Edit",
			"input": map[string]any{"file_path": "a.ts", "old_string": "x", "new_string": "y"}}}),
		claudeRec("r1", "a3", "user", []any{
			map[string]any{"type": "tool_result", "tool_use_id": "t1", "content": "a.ts\nb.ts"},
			map[string]any{"type": "tool_result", "tool_use_id": "t2", "content": []any{map[string]any{"type": "text", "text": "edited"}}},
		}),
		claudeRec("dead", "r1", "user", "this branch was rewound"),
		claudeRec("u2", "r1", "user", "now explain"),
		claudeRec("a4", "u2", "assistant", []any{map[string]any{"type": "text", "text": "Two files."}}),
		map[string]any{"type": "ai-title", "aiTitle": "Files in the app"},
		claudeRec("side", "a4", "assistant", []any{map[string]any{"type": "text", "text": "subagent chatter"}}, "isSidechain", true),
	}
}

func TestClaudeCode(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, ".claude", "projects", ClaudeProjectFolder(cwd), "aaaa-1111.jsonl")
	writeLines(t, path, claudeChat()...)
	writeLines(t, filepath.Join(home, ".claude", "projects", ClaudeProjectFolder(other), "bbbb-2222.jsonl"), claudeChat()...)
	before := fingerprint(t, path)
	src := ClaudeCode(home)

	list := src.List(cwd)
	if len(list) != 1 || list[0].ID != "aaaa-1111" || list[0].Title != "Files in the app" {
		t.Fatalf("list %+v", list)
	}
	chat, err := src.Load("aaaa", cwd) // a prefix is enough
	if err != nil || chat == nil {
		t.Fatalf("load %v %v", chat, err)
	}
	if got := roles(chat.Entries); !slices.Equal(got, []string{"user", "assistant", "tool", "user", "assistant"}) {
		t.Fatalf("roles %v", got)
	}
	if got := textOf(chat.Entries[0]); got != "what is in here?" {
		t.Errorf("first prompt %q", got)
	}
	parts := partsOf(chat.Entries[1])
	if parts[0].Type != "reasoning" || parts[1].ToolName != "bash" || parts[2].ToolName != "edit" {
		t.Errorf("assistant parts %+v", parts)
	}
	everyCallAnswered(t, chat.Entries)
	all := fmt.Sprint(chat.Entries)
	for _, gone := range []string{"rewound", "subagent chatter", "system-reminder"} {
		if strings.Contains(all, gone) {
			t.Errorf("%q leaked into the import", gone)
		}
	}
	if other, _ := src.Load("bbbb-2222", cwd); other == nil {
		t.Error("an id from another folder did not open")
	}
	if fingerprint(t, path) != before {
		t.Error("reading changed the transcript")
	}
}

func TestCodex(t *testing.T) {
	home := t.TempDir()
	rollout := func(dir, id string, items ...any) {
		records := []any{map[string]any{"type": "session_meta", "payload": map[string]any{"id": id, "cwd": dir}}}
		for _, it := range items {
			records = append(records, map[string]any{"type": "response_item", "timestamp": "2026-11-01T10:00:01Z", "payload": it})
		}
		writeLines(t, filepath.Join(home, ".codex", "sessions", "2026", "11", "01", "rollout-x-"+id+".jsonl"), records...)
	}
	user := func(text string) any {
		return map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": text}}}
	}
	items := []any{
		map[string]any{"type": "message", "role": "developer", "content": []any{map[string]any{"type": "input_text", "text": "you are codex"}}},
		user("<environment_context>\n  <cwd>/work/my.app</cwd>\n</environment_context>"),
		user("count the files"),
		map[string]any{"type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "use ls"}}},
		map[string]any{"type": "function_call", "name": "exec_command", "arguments": `{"cmd":"ls | wc -l"}`, "call_id": "c1"},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": "3"},
		map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": "There are 3."}}},
	}
	rollout(cwd, "019f-aaaa", items...)
	rollout(other, "019f-bbbb", items...)
	src := Codex(home)
	if list := src.List(cwd); len(list) != 1 || list[0].ID != "019f-aaaa" || list[0].Title != "count the files" {
		t.Fatalf("list %+v", list)
	}
	chat, _ := src.Load("019f-aaaa", cwd)
	if got := roles(chat.Entries); !slices.Equal(got, []string{"user", "assistant", "tool", "assistant"}) {
		t.Fatalf("roles %v", got)
	}
	call := partsOf(chat.Entries[1])[1]
	if call.ToolName != "bash" || fmt.Sprint(call.Input) != "map[command:ls | wc -l]" {
		t.Errorf("call %+v", call)
	}
	everyCallAnswered(t, chat.Entries)
	if all := fmt.Sprint(chat.Entries); strings.Contains(all, "environment_context") || strings.Contains(all, "you are codex") {
		t.Error("injected context leaked into the import")
	}
}

func rootBlob(ids ...string) []byte {
	out := []byte{0x50, 0x01} // field 10, varint — skipped
	for _, id := range ids {
		raw, _ := hex.DecodeString(id)
		out = append(append(out, 0x0a, byte(len(raw))), raw...)
	}
	return out
}

func hexID(n int) string { return fmt.Sprintf("%064x", n) }

func openWritable(t *testing.T, path string) *sql.DB {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	h, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func writeCLIChat(t *testing.T, home, dir, chatID string, messages ...any) string {
	path := filepath.Join(home, ".cursor", "chats", CursorFolderHash(dir), chatID, "store.db")
	h := openWritable(t, path)
	defer h.Close()
	h.Exec("CREATE TABLE blobs (id TEXT PRIMARY KEY, data BLOB)")
	h.Exec("CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT)")
	var ids []string
	for i, m := range messages {
		raw, _ := json.Marshal(m)
		ids = append(ids, hexID(i+1))
		h.Exec("INSERT INTO blobs VALUES (?, ?)", hexID(i+1), raw)
	}
	h.Exec("INSERT INTO blobs VALUES (?, ?)", hexID(999), rootBlob(ids...))
	meta, _ := json.Marshal(map[string]any{"name": "Cursor CLI chat", "latestRootBlobId": hexID(999)})
	h.Exec("INSERT INTO meta VALUES ('0', ?)", hex.EncodeToString(meta))
	return path
}

func writeIDEChat(t *testing.T, home, dir, chatID string) string {
	path := filepath.Join(CursorUserDir(home, runtime.GOOS), "globalStorage", "state.vscdb")
	h := openWritable(t, path)
	defer h.Close()
	h.Exec("CREATE TABLE IF NOT EXISTS cursorDiskKV (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)")
	bubbles := []map[string]any{
		{"id": "b1", "type": 1, "text": "rename the button"},
		{"id": "b2", "type": 2, "thinking": map[string]any{"text": "find it"}},
		{"id": "b3", "type": 2, "toolFormerData": map[string]any{"toolCallId": "tc1", "name": "read_file_v2",
			"params": `{"targetFile":"/work/my.app/button.tsx"}`, "result": `{"contents":"export const Button"}`, "status": "completed"}},
		{"id": "b4", "type": 2, "text": "Renamed."},
	}
	var headers []any
	for _, b := range bubbles {
		headers = append(headers, map[string]any{"bubbleId": b["id"]})
		raw, _ := json.Marshal(b)
		h.Exec("INSERT INTO cursorDiskKV VALUES (?, ?)", "bubbleId:"+chatID+":"+b["id"].(string), string(raw))
	}
	composer, _ := json.Marshal(map[string]any{"name": "Rename button", "lastUpdatedAt": 1700000000000,
		"workspaceIdentifier": map[string]any{"uri": map[string]any{"fsPath": dir}}, "fullConversationHeadersOnly": headers})
	h.Exec("INSERT INTO cursorDiskKV VALUES (?, ?)", "composerData:"+chatID, string(composer))
	return path
}

var cliMessages = []any{
	map[string]any{"role": "system", "content": "You are Cursor"},
	map[string]any{"role": "user", "content": "<user_info>\nOS: darwin\n</user_info>"},
	map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": "<timestamp>now</timestamp>\n<user_query>\nwhat is this?\n</user_query>"}}},
	map[string]any{"role": "assistant", "content": []any{
		map[string]any{"type": "reasoning", "text": "look"},
		map[string]any{"type": "tool-call", "toolCallId": "k1", "toolName": "Glob", "args": map[string]any{"glob_pattern": "*.ts", "target_directory": "/w"}},
	}},
	map[string]any{"role": "tool", "content": []any{map[string]any{"type": "tool-result", "toolCallId": "k1", "toolName": "Glob", "result": "a.ts"}}},
	map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": "A TypeScript app."}}},
}

func TestRootMessageIDs(t *testing.T) {
	if got := RootMessageIDs(rootBlob(hexID(1), hexID(2))); !slices.Equal(got, []string{hexID(1), hexID(2)}) {
		t.Errorf("ids %v", got)
	}
}

func TestCursor(t *testing.T) {
	home := t.TempDir()
	cli := writeCLIChat(t, home, cwd, "cli-chat-1", cliMessages...)
	writeCLIChat(t, home, other, "cli-chat-2", cliMessages...)
	ide := writeIDEChat(t, home, cwd, "ide-chat-1")
	writeIDEChat(t, home, other, "ide-chat-2")
	before := []string{fingerprint(t, cli), fingerprint(t, ide)}
	src := Cursor(home)

	var got []string
	for _, s := range src.List(cwd) {
		got = append(got, s.ID+"/"+s.Origin)
	}
	slices.Sort(got)
	if !slices.Equal(got, []string{"cli-chat-1/CLI", "ide-chat-1/IDE"}) {
		t.Fatalf("list %v", got)
	}

	chat, _ := src.Load("cli-chat-1", cwd)
	if chat.Title != "Cursor CLI chat" || textOf(chat.Entries[0]) != "what is this?" {
		t.Errorf("cli chat %q %q", chat.Title, textOf(chat.Entries[0]))
	}
	if call := partsOf(chat.Entries[1])[1]; call.ToolName != "find" {
		t.Errorf("call %+v", call)
	}
	everyCallAnswered(t, chat.Entries)

	ideChat, _ := src.Load("ide-chat-1", cwd)
	if got := roles(ideChat.Entries); !slices.Equal(got, []string{"user", "assistant", "tool", "assistant"}) {
		t.Fatalf("ide roles %v", got)
	}
	if out := partsOf(ideChat.Entries[2])[0].Output.Value; out != "export const Button" {
		t.Errorf("ide result %q", out)
	}
	if after := []string{fingerprint(t, cli), fingerprint(t, ide)}; !slices.Equal(after, before) {
		t.Error("reading changed Cursor's databases")
	}
}

// How Cursor leaves its stores: the newest writes still in the WAL and no
// -shm. A read-only open would fail, or create one in Cursor's folder.
func TestForeignWALReadsWithoutATrace(t *testing.T) {
	dir := t.TempDir()
	live := filepath.Join(dir, "live", "chat.db")
	w := openWritable(t, live)
	w.SetMaxOpenConns(1)
	w.Exec("PRAGMA journal_mode = WAL")
	w.Exec("PRAGMA wal_autocheckpoint = 0")
	w.Exec("CREATE TABLE t (v TEXT)")
	w.Exec("INSERT INTO t VALUES ('in the wal')")
	left := filepath.Join(dir, "left", "chat.db")
	os.MkdirAll(filepath.Dir(left), 0o755)
	for _, suffix := range []string{"", "-wal"} {
		data, err := os.ReadFile(live + suffix)
		if err != nil {
			t.Fatal(err)
		}
		os.WriteFile(left+suffix, data, 0o644)
	}
	w.Close()
	before := []string{fingerprint(t, left), fingerprint(t, left+"-wal")}

	h, release, err := db.OpenForeign(left)
	if err != nil {
		t.Fatal(err)
	}
	var v string
	err = h.QueryRow("SELECT v FROM t").Scan(&v)
	release()
	if err != nil || v != "in the wal" {
		t.Fatalf("read %q %v", v, err)
	}
	if after := []string{fingerprint(t, left), fingerprint(t, left+"-wal")}; !slices.Equal(after, before) {
		t.Error("the foreign database changed")
	}
	if _, err := os.Stat(left + "-shm"); err == nil {
		t.Error("a -shm file appeared in the other program's folder")
	}
}
