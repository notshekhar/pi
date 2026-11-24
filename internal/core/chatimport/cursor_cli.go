package chatimport

import (
	"crypto/md5"
	"database/sql"
	"encoding/hex"
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/db"
)

// Cursor CLI (cursor-agent) chats: ~/.cursor/chats/<md5 of folder>/<chat
// id>/store.db. Each store is content-addressed: a `blobs` table of id →
// bytes, and a `meta` row (hex-encoded JSON) naming the chat and its latest
// ROOT blob. The root is a protobuf whose field 1 lists the message blobs in
// order; each message blob is AI-SDK-shaped JSON. loop's cursor-cli.ts.

// CursorFolderHash is how Cursor files a folder's chats.
func CursorFolderHash(cwd string) string {
	sum := md5.Sum([]byte(cwd))
	return hex.EncodeToString(sum[:])
}

type cursorCLISource struct{ chats string }

type chatDir struct{ id, store string }

// CursorCLI reads cursor-agent's chats under home.
func CursorCLI(home string) Source {
	return cursorCLISource{chats: filepath.Join(home, ".cursor", "chats")}
}

func (cursorCLISource) Label() string { return "Cursor CLI" }

func chatDirs(folder string) []chatDir {
	entries, _ := os.ReadDir(folder)
	var out []chatDir
	for _, e := range entries {
		store := filepath.Join(folder, e.Name(), "store.db")
		if _, err := os.Stat(store); e.IsDir() && err == nil {
			out = append(out, chatDir{e.Name(), store})
		}
	}
	return out
}

func (c cursorCLISource) inFolder(cwd string) []chatDir {
	return chatDirs(filepath.Join(c.chats, CursorFolderHash(cwd)))
}

func (c cursorCLISource) everywhere() []chatDir {
	folders, _ := os.ReadDir(c.chats)
	var out []chatDir
	for _, f := range folders {
		out = append(out, chatDirs(filepath.Join(c.chats, f.Name()))...)
	}
	return out
}

func (c cursorCLISource) List(cwd string) []Summary {
	var out []Summary
	for _, dir := range c.inFolder(cwd) {
		_ = withForeign(dir.store, func(h *sql.DB) error {
			messages := cursorConversation(h)
			prompt := firstMessagePrompt(messages)
			if prompt == "" {
				return nil
			}
			name := readStoreMeta(h).Name
			if name == "" {
				name = prompt
			}
			out = append(out, Summary{ID: dir.id, Title: title(name), UpdatedAt: lastWrite(dir.store), Origin: "CLI"})
			return nil
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt > out[j].UpdatedAt })
	return out
}

func (c cursorCLISource) Load(id, cwd string) (*Chat, error) {
	idOf := func(d chatDir) string { return d.id }
	dir, ok := matchID(c.inFolder(cwd), id, idOf)
	if !ok {
		dir, ok = matchID(c.everywhere(), id, idOf)
	}
	if !ok {
		return nil, nil
	}
	var chat *Chat
	err := withForeign(dir.store, func(h *sql.DB) error {
		messages := cursorConversation(h)
		var b Builder
		for _, m := range messages {
			addCursorMessage(&b, m)
		}
		name := readStoreMeta(h).Name
		if name == "" {
			name = firstMessagePrompt(messages)
		}
		chat = &Chat{ID: dir.id, Title: title(name), Entries: b.Build()}
		return nil
	})
	return chat, err
}

// withForeign runs use against another program's database, read-only.
func withForeign(path string, use func(*sql.DB) error) error {
	h, release, err := db.OpenForeign(path)
	if err != nil {
		return err
	}
	defer release()
	return use(h)
}

// lastWrite is when the store last changed: a write lands in its WAL first.
func lastWrite(store string) int64 { return max(modTime(store), modTime(store+"-wal")) }

type storeMeta struct {
	Name             string `json:"name"`
	LatestRootBlobID string `json:"latestRootBlobId"`
}

func readStoreMeta(h *sql.DB) storeMeta {
	var value string
	var meta storeMeta
	if h.QueryRow("SELECT value FROM meta WHERE key = '0'").Scan(&value) != nil {
		return meta
	}
	raw, err := hex.DecodeString(value)
	if err == nil {
		_ = json.Unmarshal(raw, &meta, jsonopt.Read)
	}
	return meta
}

// cursorConversation is the chat's messages in order, read through its root.
func cursorConversation(h *sql.DB) []map[string]any {
	rootID := readStoreMeta(h).LatestRootBlobID
	if rootID == "" {
		return nil
	}
	blob := func(id string) []byte {
		var data []byte
		_ = h.QueryRow("SELECT data FROM blobs WHERE id = ?", id).Scan(&data)
		return data
	}
	var out []map[string]any
	for _, id := range RootMessageIDs(blob(rootID)) {
		var m map[string]any
		if json.Unmarshal(blob(id), &m, jsonopt.Read) == nil {
			out = append(out, m)
		}
	}
	return out
}

// RootMessageIDs reads field 1 of the root protobuf: the message blob ids, as hex.
func RootMessageIDs(root []byte) []string {
	var ids []string
	for at := 0; at < len(root); {
		key, next := readVarint(root, at)
		field, wire := key>>3, key&7
		at = next
		switch wire {
		case 0:
			_, at = readVarint(root, at)
		case 1:
			at += 8
		case 5:
			at += 4
		case 2:
			length, start := readVarint(root, at)
			end := min(start+int(length), len(root))
			if field == 1 {
				ids = append(ids, hex.EncodeToString(root[start:end]))
			}
			at = end
		default:
			return ids // groups are not used by Cursor; stop rather than misread
		}
	}
	return ids
}

func readVarint(b []byte, at int) (uint64, int) {
	var value uint64
	for shift := uint(0); at < len(b); shift += 7 {
		c := b[at]
		at++
		value |= uint64(c&0x7f) << shift
		if c < 0x80 {
			break
		}
	}
	return value, at
}

// cursorParts is a message's content as parts, a plain string included.
func cursorParts(m map[string]any) []map[string]any {
	if s, ok := m["content"].(string); ok {
		return []map[string]any{{"type": "text", "text": s}}
	}
	return list(m, "content")
}

func cursorPrompt(m map[string]any) string {
	if str(m, "role") != "user" {
		return ""
	}
	var prompts []string
	for _, p := range cursorParts(m) {
		if str(p, "type") == "text" {
			if text := UserPrompt(str(p, "text")); text != "" {
				prompts = append(prompts, text)
			}
		}
	}
	return strings.Join(prompts, "\n\n")
}

func firstMessagePrompt(messages []map[string]any) string {
	return firstPrompt(messages, cursorPrompt)
}

func addCursorMessage(b *Builder, m map[string]any) {
	switch str(m, "role") {
	case "user":
		b.User(cursorPrompt(m), 0)
	case "assistant":
		for _, p := range cursorParts(m) {
			switch str(p, "type") {
			case "text":
				b.Text(str(p, "text"), 0)
			case "reasoning":
				b.Reasoning(str(p, "text"), 0)
			case "tool-call":
				if id, name := str(p, "toolCallId"), str(p, "toolName"); id != "" && name != "" {
					input := p["args"]
					if input == nil {
						input = p["input"]
					}
					tool, translated := TranslateCall(name, input)
					b.ToolCall(id, tool, translated, 0)
				}
			}
		}
	case "tool":
		for _, p := range cursorParts(m) {
			if id := str(p, "toolCallId"); str(p, "type") == "tool-result" && id != "" {
				result := p["result"]
				if result == nil {
					result = p["output"]
				}
				b.ToolResult(id, OutputText(result), p["isError"] == true, 0)
			}
		}
	}
}
