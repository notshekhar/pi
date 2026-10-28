// Package docs is loop's internal docs (docs/index.ts), served by the read
// tool under loop://docs. The bodies are loop's own, embedded by
// scripts/sync-loop/export.ts.
package docs

import (
	_ "embed"
	"encoding/json"
	"strings"
)

//go:embed data/docs.json
var docsJSON []byte

// Entry is one doc.
type Entry struct {
	Name    string `json:"name"`
	Summary string `json:"summary"`
	Content string `json:"content"`
}

var all = func() []Entry {
	var out []Entry
	_ = json.Unmarshal(docsJSON, &out)
	return out
}()

// List is every doc.
func List() []Entry { return append([]Entry(nil), all...) }

// Get looks a doc up; the .md suffix is optional.
func Get(name string) (Entry, bool) {
	if !strings.HasSuffix(name, ".md") {
		name += ".md"
	}
	for _, d := range all {
		if d.Name == name {
			return d, true
		}
	}
	return Entry{}, false
}

// RenderIndex is the loop://docs listing.
func RenderIndex() string {
	lines := []string{"Internal loop docs. Read one with the read tool, e.g. read loop://docs/config.md", ""}
	for _, d := range all {
		lines = append(lines, "- loop://docs/"+d.Name+" — "+d.Summary)
	}
	return strings.Join(lines, "\n")
}
