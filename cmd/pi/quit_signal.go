package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/notshekhar/pi/internal/tui"
)

// quitOnSignal stops the TUI when the process is told to go: SIGTERM and
// SIGINT arrive as parent's cancellation, SIGHUP when the terminal tab is
// closed. Stopping the app — rather than dying where the signal lands — is
// what runs the exit path: the terminal is restored, the database is closed
// cleanly, and background shells, which sit in their own process groups and
// would otherwise run on under pid 1, are killed. The returned func stops
// watching.
func quitOnSignal(parent context.Context, app *tui.App) func() {
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	done := make(chan struct{})
	go func() {
		select {
		case <-hup:
		case <-parent.Done():
		case <-done:
			return
		}
		app.Do(app.Stop)
	}()
	return func() {
		signal.Stop(hup)
		close(done)
	}
}
