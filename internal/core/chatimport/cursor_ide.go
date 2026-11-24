package chatimport

import (
	"database/sql"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// Cursor IDE (Composer / Agent) chats, all in one database:
// <Cursor user dir>/globalStorage/state.vscdb, table cursorDiskKV.
// `composerData:<id>` is a chat — its name, the folder it was opened in
// (workspaceIdentifier.uri.fsPath) and fullConversationHeadersOnly, its
// bubbles in order. `bubbleId:<chat id>:<bubble id>` is one bubble: type 1 the
// user's, type 2 the agent's (text, thinking, or one tool call in
// toolFormerData). loop's cursor-ide.ts.

const userBubble = 1

// CursorUserDir is where Cursor keeps its user data on each platform.
func CursorUserDir(home, goos string) string {
	switch goos {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Cursor", "User")
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User")
	}
	return filepath.Join(home, ".config", "Cursor", "User")
}

type cursorIDESource struct{ database string }

type composerRow struct {
	id, name  string
	updatedAt int64
}

// CursorIDE reads the Cursor editor's chats under home.
func CursorIDE(home string) Source {
	return cursorIDESource{database: filepath.Join(CursorUserDir(home, runtime.GOOS), "globalStorage", "state.vscdb")}
}

func (cursorIDESource) Label() string { return "Cursor" }

func (c cursorIDESource) query(use func(*sql.DB) error) error {
	if _, err := os.Stat(c.database); err != nil {
		return nil
	}
	return withForeign(c.database, use)
}

const composerColumns = `substr(key, 14), coalesce(json_extract(value, '$.name'), ''),
	coalesce(json_extract(value, '$.lastUpdatedAt'), json_extract(value, '$.createdAt'), 0)`

func composers(h *sql.DB, where string, arg any) []composerRow {
	rows, err := h.Query(`SELECT `+composerColumns+` FROM cursorDiskKV WHERE json_valid(value) AND `+where, arg)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []composerRow
	for rows.Next() {
		var r composerRow
		var updated float64
		if rows.Scan(&r.id, &r.name, &updated) == nil {
			r.updatedAt = int64(updated)
			out = append(out, r)
		}
	}
	return out
}

func composersIn(h *sql.DB, cwd string) []composerRow {
	return composers(h, `key LIKE 'composerData:%' AND json_extract(value, '$.workspaceIdentifier.uri.fsPath') = ?`, cwd)
}

func (c cursorIDESource) List(cwd string) []Summary {
	var out []Summary
	_ = c.query(func(h *sql.DB) error {
		for _, r := range composersIn(h, cwd) {
			if r.name != "" { // an empty composer Cursor opened and never used
				out = append(out, Summary{ID: r.id, Title: title(r.name), UpdatedAt: r.updatedAt, Origin: "IDE"})
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (c cursorIDESource) Load(id, cwd string) (*Chat, error) {
	var chat *Chat
	err := c.query(func(h *sql.DB) error {
		row, ok := matchID(composersIn(h, cwd), id, func(r composerRow) string { return r.id })
		if !ok {
			byID := composers(h, `key = ?`, "composerData:"+id)
			if len(byID) == 0 {
				return nil
			}
			row = byID[0]
		}
		chat = readIDEChat(h, row)
		return nil
	})
	return chat, err
}

func bubbles(h *sql.DB, chatID string) []map[string]any {
	var headersRaw string
	_ = h.QueryRow(`SELECT coalesce(json_extract(value, '$.fullConversationHeadersOnly'), '[]') FROM cursorDiskKV WHERE key = ?`,
		"composerData:"+chatID).Scan(&headersRaw)
	var headers []struct {
		BubbleID string `json:"bubbleId"`
	}
	_ = json.Unmarshal([]byte(headersRaw), &headers, jsonopt.Read)
	var out []map[string]any
	for _, hd := range headers {
		var value string
		if hd.BubbleID == "" || h.QueryRow(`SELECT value FROM cursorDiskKV WHERE key = ?`, "bubbleId:"+chatID+":"+hd.BubbleID).Scan(&value) != nil {
			continue
		}
		var bubble map[string]any
		if json.Unmarshal([]byte(value), &bubble, jsonopt.Read) == nil {
			out = append(out, bubble)
		}
	}
	return out
}

func readIDEChat(h *sql.DB, row composerRow) *Chat {
	all := bubbles(h, row.id)
	var b Builder
	firstPrompt := ""
	for _, bubble := range all {
		if bubbleType(bubble) == userBubble && firstPrompt == "" {
			firstPrompt = str(bubble, "text")
		}
		addBubble(&b, bubble)
	}
	name := row.name
	if name == "" {
		name = firstPrompt
	}
	return &Chat{ID: row.id, Title: title(name), Entries: b.Build()}
}

func bubbleType(bubble map[string]any) int {
	t, _ := bubble["type"].(float64)
	return int(t)
}

func addBubble(b *Builder, bubble map[string]any) {
	ts := ParseTime(bubble["createdAt"])
	if bubbleType(bubble) == userBubble {
		b.User(UserPrompt(str(bubble, "text")), ts)
		return
	}
	b.Reasoning(str(obj(bubble, "thinking"), "text"), ts)
	b.Text(str(bubble, "text"), ts)
	tool := obj(bubble, "toolFormerData")
	id, name := str(tool, "toolCallId"), str(tool, "name")
	if id == "" || name == "" {
		return
	}
	params := str(tool, "params")
	if params == "" {
		params = str(tool, "rawArgs")
	}
	if params == "" {
		params = "{}"
	}
	callName, input := TranslateCall(name, ParseJSONish(params))
	b.ToolCall(id, callName, input, ts)
	status := str(tool, "status")
	b.ToolResult(id, ideResult(str(tool, "result")), status == "error" || status == "cancelled", ts)
}

// ideResult: a result is a JSON string; most carry their readable part under one key.
func ideResult(result string) string {
	parsed := ParseJSONish(result)
	if m, ok := parsed.(map[string]any); ok {
		for _, key := range []string{"contents", "content", "output", "text"} {
			if s, ok := m[key].(string); ok {
				return s
			}
		}
	}
	return OutputText(parsed)
}
