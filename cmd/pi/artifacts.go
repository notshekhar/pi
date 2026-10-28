package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	"github.com/notshekhar/pi/internal/core/artifacts"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/tui"
)

// /artifacts — loop's artifact-handlers.ts: the pages the agent wrote, opened
// from disk by handing the OS browser a file:// URL. The URL is printed either
// way: over SSH nothing opens.

func formatBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d B", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1f KB", float64(n)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(n)/1024/1024)
}

func artifactLabel(a artifacts.Meta) string {
	icon := ""
	if a.Favicon != "" {
		icon = a.Favicon + " "
	}
	if !a.Written {
		return icon + a.Title + " — not written yet"
	}
	return icon + a.Title + " — " + formatBytes(a.Size)
}

func defaultExportDir() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Downloads")
}

func (t *repl) manageArtifacts() {
	if !config.LoadSettings().ArtifactsOn() {
		t.dim("artifacts are off — turn them on in /settings, then ask the agent for a report or write-up.")
		return
	}
	if len(artifacts.List()) == 0 {
		t.dim("no artifacts yet — ask for a document, report or write-up and one will appear here.")
		return
	}
	t.manage(func() (string, []tui.Item) {
		var items []tui.Item
		for _, a := range artifacts.List() {
			desc := a.Description
			if desc == "" {
				desc = artifacts.FilePath(a.Record)
			}
			items = append(items, tui.Item{Value: a.ID, Label: artifactLabel(a), Description: desc})
		}
		return "Artifacts (type to filter, Esc to close)", items
	}, func(choice tui.Item) bool {
		a, ok := artifacts.Get(choice.Value)
		if !ok {
			return keepPanel
		}
		path := artifacts.FilePath(a.Record)
		if !a.Written {
			t.dim(`"%s" has no content yet — the agent reserved it but never wrote it.`, a.Title)
			return keepPanel
		}
		action := t.choose(a.Title,
			tui.Item{Value: "open", Label: "Open in browser", Description: path},
			tui.Item{Value: "export", Label: "Download a copy", Description: "to " + defaultExportDir()},
			tui.Item{Value: "delete", Label: "Delete", Description: "remove this artifact from disk"})
		if action == nil {
			return keepPanel
		}
		switch action.Value {
		case "export":
			if saved, err := artifacts.Export(a.ID, ""); err != nil {
				t.dim("could not export: %s", err)
			} else {
				t.dim("saved %s", saved)
			}
			return keepPanel
		case "delete":
			if artifacts.Delete(a.ID) {
				t.dim(`deleted "%s"`, a.Title)
			} else {
				t.dim("could not delete %s", a.ID)
			}
			return keepPanel
		}
		u := (&url.URL{Scheme: "file", Path: path}).String()
		if openBrowser(u) {
			t.dim(`opening "%s" — %s`, a.Title, u)
		} else {
			t.dim("open it yourself: %s", u)
		}
		return closePanel
	})
}
