package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/core/session"
)

// The session subcommands and the resume flag, ported from loop's
// commands.ts (cmdSessions / cmdArchive) and interactive/resume-hint.ts.

// productName is what the resume hint tells you to type.
const productName = "pi"

// sessionsCommand is `pi sessions [--all|--archived]`: the sessions that ran
// in this directory, newest first.
func sessionsCommand(cwd string, args []string) {
	fs := flag.NewFlagSet("sessions", flag.ExitOnError)
	all := fs.Bool("all", false, "include archived sessions")
	archived := fs.Bool("archived", false, "only archived sessions")
	_ = fs.Parse(args)

	scope := session.Active
	switch {
	case *all:
		scope = session.AllScopes
	case *archived:
		scope = session.Archived
	}
	list, err := session.ListScope(cwd, scope)
	if err != nil {
		fatal(err)
	}
	if len(list) == 0 {
		if scope == session.Archived {
			fmt.Println("No archived sessions in this cwd.")
		} else {
			fmt.Println("No sessions in this cwd.")
		}
		return
	}
	for _, m := range list {
		// A name identifies a session far better than its first prompt.
		preview := m.Name
		if preview == "" {
			preview, _, _ = strings.Cut(m.Title, "\n")
		}
		mark := ""
		if scope == session.AllScopes && !m.ArchivedAt.IsZero() {
			mark = "  [archived]"
		}
		fmt.Printf("%s  %s  %s  %s%s\n", m.ID, m.Model, m.Updated.UTC().Format("2006-01-02T15:04:05.000Z"), preview, mark)
	}
}

// archiveCommand is `pi archive <id>` / `pi unarchive <id>`: put a session
// away (it leaves the lists but stays resumable), or bring it back.
func archiveCommand(args []string, archived bool) {
	verb := "archive"
	if !archived {
		verb = "unarchive"
	}
	if len(args) == 0 || args[0] == "" {
		fmt.Fprintf(os.Stderr, "Usage: %s %s <session-id>\n", productName, verb)
		os.Exit(1)
	}
	id := args[0]
	ok, err := session.SetArchived(id, archived)
	if err != nil {
		fatal(err)
	}
	if !ok {
		fmt.Fprintf(os.Stderr, "No such session: %s\n", id)
		os.Exit(1)
	}
	if archived {
		fmt.Printf("Archived %s.\n", id)
	} else {
		fmt.Printf("Restored %s.\n", id)
	}
}

// openSession loads the session named by --session, exiting on a miss.
func openSession(id string) *session.Session {
	s, err := session.Load(id)
	if err != nil {
		fatal(fmt.Errorf("Session not found: %s", id))
	}
	return s
}

// printResumeHint writes the parting line: how to get this conversation
// back. Called after the TUI has stopped, so it lands in the terminal's own
// scrollback. Silent with no session (nothing to resume by) and when stdout is
// not a terminal (so it never lands in a pipe).
func printResumeHint(id string) {
	if id == "" || !isTerminal(os.Stdout) {
		return
	}
	fmt.Printf("\nResume this session with:\n  %s --session %s\n", productName, id)
}

// isTerminal reports whether f is a character device.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
