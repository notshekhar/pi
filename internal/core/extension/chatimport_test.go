package extension

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/core/chatimport"
)

type scriptedUI struct {
	pickers []string
	answer  func(items []Item) string
}

func (u *scriptedUI) Select(title string, items []Item, _ string) string {
	u.pickers = append(u.pickers, title)
	return u.answer(items)
}
func (u *scriptedUI) Prompt(string, string) string { return "" }

// importCommand finds one of chat-import's commands, against a fake home.
func importCommand(t *testing.T, name string) Command {
	t.Helper()
	for _, c := range (chatImport{}).Commands() {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no /%s", name)
	return Command{}
}

func withClaudeChat(t *testing.T) string {
	home := t.TempDir()
	t.Setenv("LOOP_IMPORT_HOME", home)
	cwd := "/work/my.app"
	dir := filepath.Join(home, ".claude", "projects", chatimport.ClaudeProjectFolder(cwd))
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "dddd-4444.jsonl"), []byte(
		`{"uuid":"u1","type":"user","message":{"role":"user","content":"hello there"}}`+"\n"+
			`{"uuid":"a1","parentUuid":"u1","type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}`+"\n"), 0o644)
	return cwd
}

func TestChatImportCommands(t *testing.T) {
	var names []string
	for _, c := range (chatImport{}).Commands() {
		names = append(names, c.Name)
		if c.Import == nil || c.Run != nil {
			t.Errorf("/%s must be an import command", c.Name)
		}
	}
	if strings.Join(names, " ") != "claude-import codex-import cursor-import" {
		t.Errorf("commands %v", names)
	}
	if _, defaultOn := any(chatImport{}).(DefaultEnabled); defaultOn {
		t.Error("chat-import must ship off")
	}
}

func TestChatImportPickThenPreview(t *testing.T) {
	cwd := withClaudeChat(t)
	ui := &scriptedUI{answer: func(items []Item) string { return items[0].Value }}
	SetUI(ui)
	defer SetUI(nil)
	chat, note, err := importCommand(t, "claude-import").Import(context.Background(), cwd, "")
	if err != nil || note != "" || chat == nil {
		t.Fatalf("chat %v note %q err %v", chat, note, err)
	}
	if strings.Join(ui.pickers, "|") != "Claude Code chats in this folder" {
		t.Errorf("pickers %v", ui.pickers)
	}
	if chat.Source != "Claude Code" || chat.Title != "hello there" || len(chat.Entries) != 2 {
		t.Errorf("chat %+v", chat)
	}
}

func TestChatImportByIDAndMisses(t *testing.T) {
	cwd := withClaudeChat(t)
	ui := &scriptedUI{answer: func([]Item) string { return "" }}
	SetUI(ui)
	defer SetUI(nil)
	if chat, _, _ := importCommand(t, "claude-import").Import(context.Background(), cwd, "dddd-4444"); chat == nil || len(ui.pickers) != 0 {
		t.Errorf("an id should open straight away (chat %v, pickers %v)", chat, ui.pickers)
	}
	if _, _, err := importCommand(t, "claude-import").Import(context.Background(), cwd, "nope"); err == nil || err.Error() != "no Claude Code chat with id nope" {
		t.Errorf("unknown id: %v", err)
	}
	_, note, _ := importCommand(t, "codex-import").Import(context.Background(), cwd, "")
	if !strings.Contains(note, "no Codex chats in /work/my.app") || !strings.Contains(note, "/codex-import <session id>") || len(ui.pickers) != 0 {
		t.Errorf("empty folder: note %q, pickers %v", note, ui.pickers)
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	for at, want := range map[time.Time]string{
		now.Add(-10 * time.Second):    "just now",
		now.Add(-5 * time.Minute):     "5m ago",
		now.Add(-3 * time.Hour):       "3h ago",
		now.Add(-48 * time.Hour):      "2d ago",
		now.Add(-60 * 24 * time.Hour): "2026-09-25",
	} {
		if got := ago(at, now); got != want {
			t.Errorf("ago(%v) = %q, want %q", at, got, want)
		}
	}
}
