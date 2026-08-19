// Command mddemo renders the TUI's own output to stdout, so the markdown
// renderer and the noir row grammar can be checked without a live session.
package main

import (
	"fmt"
	"os"

	"github.com/notshekhar/pi/internal/modules/tui"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "transcript" {
		palette := tui.NightPalette
		if len(os.Args) > 2 && os.Args[2] == "day" {
			palette = tui.DayPalette
		}
		fmt.Print(tui.DemoTranscript(palette, 76, 0))
		return
	}
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: mddemo <file.md> | mddemo transcript [day]")
		os.Exit(2)
	}
	src, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(tui.DemoRender(string(src), 76))
}
