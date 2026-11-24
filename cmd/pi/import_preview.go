package main

import (
	"context"
	"fmt"

	"github.com/notshekhar/pi/internal/core/chatimport"
	"github.com/notshekhar/pi/internal/core/extension"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/tui"
)

// The preview half of importing another agent's chat — loop's
// import-preview-handlers.ts. The chat-import extension reads it; this draws
// it through the same replay a resumed session goes through (so the preview
// is exactly what saving gives), then asks: Save makes it a new session in
// this folder and switches to it; Discard (or Esc) puts the transcript back.
// Nothing touches the database before Save.

const importSave = "save"

// importChoices is the menu that takes the input box's place under the preview.
func importChoices(cwd string) []tui.Item {
	return []tui.Item{
		{Value: importSave, Label: "Save", Description: "as a new pi session in " + cwd},
		{Value: "discard", Label: "Discard", Description: "nothing is saved"},
	}
}

// runImportCommand runs an import command and previews what it read. Runs on
// the extension command's goroutine, which owns the panel.
func (t *repl) runImportCommand(ctx context.Context, cmd extension.Command, rest string) {
	chat, note, err := cmd.Import(ctx, t.cfg.CWD, rest)
	switch {
	case err != nil:
		t.fail("%s", err)
	case chat == nil && note != "":
		t.dim("%s", note)
	case chat != nil:
		t.previewImport(chat)
	}
}

func (t *repl) previewImport(chat *chatimport.Chat) {
	if t.busy() {
		return
	}
	t.showImportPreview(chat)
	choice := t.choose(fmt.Sprintf("Import “%s”?", chat.Title), importChoices(t.cfg.CWD)...)
	if choice != nil && choice.Value == importSave {
		t.saveImport(chat)
		return
	}
	t.discardImport(chat)
}

func messageCount(entries []session.Entry) int {
	n := 0
	for _, e := range entries {
		if e.Type == session.TypeMessage && e.Role != "tool" {
			n++
		}
	}
	return n
}

func (t *repl) showImportPreview(chat *chatimport.Chat) {
	preview := session.Preview(t.cfg.FullID(), t.cfg.CWD, chat.Entries)
	t.do(func() {
		th := t.app.Theme()
		t.app.Clear()
		t.app.Print(fmt.Sprintf("Preview · %s chat “%s” · %d messages %s",
			chat.Source, chat.Title, messageCount(chat.Entries), th.Fg(tui.SlotDim, "· not saved yet")))
		t.replayTranscript(preview)
	})
}

func (t *repl) saveImport(chat *chatimport.Chat) {
	saved, err := session.Create(t.cfg.FullID(), t.cfg.CWD)
	if err == nil {
		// A copy: the session stamps ids and parents onto what it appends.
		err = saved.AppendAll(append([]session.Entry(nil), chat.Entries...)...)
	}
	if err == nil {
		err = saved.SetName(chat.Title)
	}
	if err != nil {
		t.fail("could not save the %s chat: %s", chat.Source, err)
		t.discardImport(chat)
		return
	}
	t.resume(saved.ID)
	t.say(fmt.Sprintf("Saved the %s chat as session %s — carry on from here.", chat.Source, saved.ID))
}

// discardImport puts back the conversation that was on screen.
func (t *repl) discardImport(chat *chatimport.Chat) {
	current := t.run.Session
	t.do(func() {
		t.app.Clear()
		t.replayTranscript(current)
	})
	t.dim("Discarded the %s chat — nothing was saved.", chat.Source)
}
