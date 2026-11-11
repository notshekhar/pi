package main

import (
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/notshekhar/pi/internal/core/artifacts"
	"github.com/notshekhar/pi/internal/core/config"
)

// artifactsCommand is loop's cmdArtifacts: the pages the agent wrote, one
// per line, or `artifacts export <id> [dir]` to save one out.
//
// It prints the file:// URL rather than opening anything — this is the
// scriptable surface; opening lives in the TUI's /artifacts. One reserved
// but never written is listed and marked, so it does not look lost.
func artifactsCommand(c *cliCall) int {
	if p := c.args.positional; len(p) > 0 && p[0] == "export" {
		return exportArtifact(p[1:])
	}
	if !config.LoadSettings().ArtifactsOn() {
		fmt.Fprintf(os.Stderr, "Artifacts are off. Enable them in %s → /settings → artifacts.\n", productName)
		return 1
	}
	list := artifacts.List()
	if len(list) == 0 {
		fmt.Println("No artifacts yet.")
		return 0
	}
	for _, a := range list {
		where := "(not written yet)"
		if a.Written {
			where = (&url.URL{Scheme: "file", Path: artifacts.FilePath(a.Record)}).String()
		}
		updated := time.UnixMilli(int64(a.UpdatedAt)).UTC().Format(isoMillis)
		fmt.Printf("%s  %s  %s\t%s\n", a.ID, updated, a.Title, where)
	}
	return 0
}

// isoMillis is JavaScript's Date.toISOString shape, which loop prints.
const isoMillis = "2006-01-02T15:04:05.000Z"

// exportArtifact shares the picker's Download implementation, so both name
// the saved file alike.
func exportArtifact(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "Usage: %s artifacts export <id> [dir]\n", productName)
		return 1
	}
	dest := ""
	if len(args) > 1 {
		dest = args[1]
	}
	saved, err := artifacts.Export(args[0], dest)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println(saved)
	return 0
}
