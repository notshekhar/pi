package tui

import (
	"strings"
	"testing"
)

// The masthead's model row follows a model switched after it is on screen —
// /model, /provider and ctrl+p all change it once the banner is up.
func TestWelcomeFollowsModel(t *testing.T) {
	a := &App{Conversation: NewConversation(""), theme: testTheme(), size: Size{Rows: 30, Cols: 80}}
	a.ShowWelcome(WelcomeInfo{Name: "x", Model: "anthropic/old-model", CWD: "~/repo", Session: "unsaved"})

	a.SetModel("openai/new-model")

	banner := strings.Join(a.welcome.lines, "\n")
	if !strings.Contains(banner, "openai/new-model") || strings.Contains(banner, "anthropic/old-model") {
		t.Fatalf("masthead kept the old model:\n%s", banner)
	}
	if !a.welcome.dirty {
		t.Fatal("masthead was not marked for repaint")
	}
}
