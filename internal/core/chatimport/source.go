package chatimport

import (
	"bufio"
	json "encoding/json/v2"
	"io"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/session"
)

// Summary is one chat in a picker.
type Summary struct {
	ID        string
	Title     string
	UpdatedAt int64 // epoch ms; pickers sort newest first
	// Origin says which variant of the agent wrote it (Cursor "CLI" / "IDE").
	Origin string
}

// Chat is a chat read into our shape, ready to preview.
type Chat struct {
	ID    string
	Title string
	// Source is the agent it came from ("Claude Code"), set by whoever loaded it.
	Source  string
	Entries []session.Entry
}

// Source is what every importer answers: which chats exist for a folder, and
// one chat as a transcript. The extension talks to this and nothing else, so
// adding an agent is one more file implementing it.
type Source interface {
	Label() string
	// List is the chats recorded in cwd, newest first.
	List(cwd string) []Summary
	// Load is one chat by id (or an unambiguous prefix), looked up in cwd
	// first and then everywhere the agent keeps chats. nil when none has it.
	Load(id, cwd string) (*Chat, error)
}

// Home is where importers look: the user's home, or LOOP_IMPORT_HOME (the
// same override loop reads), which tests point at a fake one.
func Home() string {
	if h := os.Getenv("LOOP_IMPORT_HOME"); h != "" {
		return h
	}
	h, _ := os.UserHomeDir()
	return h
}

// title is a chat's title from its first prompt, kept to one line.
func title(text string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	line = strings.TrimSpace(line)
	if line == "" {
		return "(untitled chat)"
	}
	if r := []rune(line); len(r) > 80 {
		return string(r[:79]) + "…"
	}
	return line
}

// matchID finds an id exactly, else by a prefix only one id has.
func matchID[T any](items []T, id string, idOf func(T) string) (T, bool) {
	var zero, hit T
	hits := 0
	for _, it := range items {
		switch {
		case idOf(it) == id:
			return it, true
		case strings.HasPrefix(idOf(it), id):
			hit, hits = it, hits+1
		}
	}
	if hits == 1 {
		return hit, true
	}
	return zero, false
}

// readJSONL is every parseable line of a JSONL file; a torn last line (the
// agent still writing) is skipped.
func readJSONL(path string) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseLines(f)
}

// readJSONLHead is the parseable lines in the first `bytes` of a file —
// enough for a picker's title without loading a transcript that may be
// hundreds of megabytes.
func readJSONLHead(path string, bytes int64) []map[string]any {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return parseLines(io.LimitReader(f, bytes))
}

func parseLines(r io.Reader) []map[string]any {
	var out []map[string]any
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 256*1024*1024)
	for scanner.Scan() {
		var rec map[string]any
		if json.Unmarshal(scanner.Bytes(), &rec, jsonopt.Read) == nil {
			out = append(out, rec)
		}
	}
	return out
}

// Small accessors for decoded JSON.

func str(m map[string]any, key string) string {
	s, _ := m[key].(string)
	return s
}

func obj(m map[string]any, key string) map[string]any {
	o, _ := m[key].(map[string]any)
	return o
}

func list(m map[string]any, key string) []map[string]any {
	raw, _ := m[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		if o, ok := v.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}
