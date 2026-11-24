package extension

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/core/chatimport"
)

// chatImport brings a conversation from Claude Code, Codex or Cursor into
// this folder — loop's chat-import extension. `/claude-import`,
// `/codex-import`, `/cursor-import` list that agent's chats from the folder;
// with a session id they open that chat from anywhere. The host previews it
// and asks Save or Discard; nothing is written before Save.
type chatImport struct{}

func init() { Register(chatImport{}) }

func (chatImport) Name() string { return "chat-import" }
func (chatImport) About() string {
	return "Import a chat from Claude Code, Codex or Cursor into this folder — preview it, then save or discard. /claude-import · /codex-import · /cursor-import [session id]"
}

// importSources pairs each command with the source it reads.
var importSources = []struct {
	command string
	source  func(home string) chatimport.Source
}{
	{"claude-import", chatimport.ClaudeCode},
	{"codex-import", chatimport.Codex},
	{"cursor-import", chatimport.Cursor},
}

func (chatImport) Commands() []Command {
	out := make([]Command, 0, len(importSources))
	for _, s := range importSources {
		source := s.source
		command := s.command
		label := source(chatimport.Home()).Label()
		out = append(out, Command{
			Name:  command,
			About: fmt.Sprintf("Import a %s chat from this folder (preview, then save or discard): /%s [session id]", label, command),
			Import: func(_ context.Context, cwd, rest string) (*chatimport.Chat, string, error) {
				return importChat(source(chatimport.Home()), command, cwd, strings.TrimSpace(rest))
			},
		})
	}
	return out
}

func importChat(src chatimport.Source, command, cwd, id string) (*chatimport.Chat, string, error) {
	if id == "" {
		picked, note := pickChat(src, command, cwd)
		if picked == "" {
			return nil, note, nil
		}
		id = picked
	}
	chat, err := src.Load(id, cwd)
	if err != nil {
		return nil, "", fmt.Errorf("could not read the %s chat: %w", src.Label(), err)
	}
	if chat == nil {
		return nil, "", fmt.Errorf("no %s chat with id %s", src.Label(), id)
	}
	if len(chat.Entries) == 0 {
		return nil, "", fmt.Errorf("that %s chat has no messages pi can import", src.Label())
	}
	chat.Source = src.Label()
	return chat, "", nil
}

// pickChat opens the picker of this folder's chats; "" when there are none
// (with a note saying how to import from elsewhere) or the user backed out.
func pickChat(src chatimport.Source, command, cwd string) (string, string) {
	chats := src.List(cwd)
	if len(chats) == 0 {
		return "", fmt.Sprintf("no %s chats in %s — /%s <session id> imports one from anywhere", src.Label(), cwd, command)
	}
	host, ok := Ask()
	if !ok {
		return "", ""
	}
	return host.Select(src.Label()+" chats in this folder", ChatPickerItems(chats, time.Now()), ""), ""
}

// ChatPickerItems is one row per chat: its title, then when, which variant,
// and enough of the id to tell two apart.
func ChatPickerItems(chats []chatimport.Summary, now time.Time) []Item {
	items := make([]Item, len(chats))
	for i, c := range chats {
		parts := []string{ago(time.UnixMilli(c.UpdatedAt), now)}
		if c.Origin != "" {
			parts = append(parts, c.Origin)
		}
		parts = append(parts, c.ID[:min(8, len(c.ID))])
		items[i] = Item{Value: c.ID, Label: c.Title, Description: strings.Join(parts, " · ")}
	}
	return items
}

// ago is "just now", "5m ago", "3h ago", "2d ago", then the date.
func ago(at, now time.Time) string {
	elapsed := now.Sub(at)
	switch {
	case elapsed < time.Minute:
		return "just now"
	case elapsed < time.Hour:
		return fmt.Sprintf("%dm ago", int(elapsed.Minutes()))
	case elapsed < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(elapsed.Hours()))
	case elapsed < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(elapsed.Hours()/24))
	}
	return at.UTC().Format("2006-01-02")
}
