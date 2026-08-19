package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// /background and /daemon — work that runs while the prompt stays yours.
//
// A background task is a full agent turn against its own session, so it can
// read and edit like any other turn. It reports into the transcript when it
// finishes rather than streaming, because two things writing the transcript
// at once is unreadable.
//
// The daemon is the ticker that fires reminders and any task given a delay.
// It is OFF by default: a scheduler that starts itself is a surprise, and
// this one can start agent turns that cost money.

// bgTask is one background job.
type bgTask struct {
	ID      string
	Prompt  string
	Started time.Time
	Done    time.Time
	Err     error
	Result  string
	cancel  context.CancelFunc
}

// Status is the one-line state for the manager.
func (b *bgTask) Status() string {
	switch {
	case b.Err != nil:
		return "failed after " + tui.FormatCountdown(b.Done.Sub(b.Started)) + " — " + firstLine(b.Err.Error())
	case !b.Done.IsZero():
		return "done in " + tui.FormatCountdown(b.Done.Sub(b.Started))
	default:
		return "running for " + tui.FormatCountdown(time.Since(b.Started))
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// background runs a task, or opens the manager when given nothing.
func (t *repl) background(parent context.Context, rest string) {
	rest = strings.TrimSpace(rest)
	if rest == "" {
		t.backgroundManager(parent)
		return
	}
	if t.bg.count() >= MaxBackground {
		t.dim("background limit reached (max %d) — let one finish first", MaxBackground)
		return
	}
	task := t.bg.start(parent, rest, t)
	t.dim("background started — %s", task.ID)
}

// MaxBackground caps concurrent tasks. Each is a real agent turn, so this is
// a spend limit as much as a resource one.
const MaxBackground = 4

// bgSet is the live task list.
type bgSet struct {
	mu    sync.Mutex
	tasks []*bgTask
}

func (s *bgSet) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, t := range s.tasks {
		if t.Done.IsZero() {
			n++
		}
	}
	return n
}

func (s *bgSet) list() []*bgTask {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*bgTask{}, s.tasks...)
}

// start launches a task against its own session and context.
func (s *bgSet) start(parent context.Context, prompt string, t *repl) *bgTask {
	ctx, cancel := context.WithCancel(parent)
	task := &bgTask{
		ID:      fmt.Sprintf("bg-%d", time.Now().Unix()%100000),
		Prompt:  prompt,
		Started: time.Now(),
		cancel:  cancel,
	}
	s.mu.Lock()
	s.tasks = append(s.tasks, task)
	s.mu.Unlock()

	go func() {
		defer cancel()
		result, err := t.runDetached(ctx, prompt)
		s.mu.Lock()
		task.Done, task.Err, task.Result = time.Now(), err, result
		s.mu.Unlock()

		t.app.Do(func() {
			th := t.app.Theme()
			switch {
			case err != nil:
				t.app.Print(th.Fg(tui.SlotError, task.ID+" failed") + th.Fg(tui.SlotMuted, " — "+firstLine(err.Error())))
			default:
				t.app.Print(th.Fg(tui.SlotSuccess, task.ID+" done") + th.Fg(tui.SlotMuted, " — "+firstLine(result)))
			}
		})
	}()
	return task
}

// backgroundManager lists tasks and offers to cancel a running one.
const addRow = "\x00add"

func (t *repl) backgroundManager(parent context.Context) {
	// Rebuilt each pass, so a task that finished while the panel was open
	// shows its result rather than still saying "running".
	t.manage(func() (string, []tui.Item) {
		tasks := t.bg.list()
		// The add row is ALWAYS offered, so an empty panel is not a dead end.
		// Without it `/background` on a fresh session opened nothing at all
		// and said nothing — which is indistinguishable from a broken command.
		items := []tui.Item{{
			Value:       addRow,
			Label:       "+ add task",
			Description: "run a prompt in its own session, reporting when it finishes",
		}}
		for _, task := range tasks {
			items = append(items, tui.Item{Value: task.ID, Label: firstLine(task.Prompt), Description: task.Status()})
		}
		return fmt.Sprintf("Background · %d", len(tasks)), items
	}, func(choice tui.Item) {
		if choice.Value == addRow {
			if text := strings.TrimSpace(t.ask("background task", "")); text != "" {
				t.background(parent, text)
			}
			return
		}
		for _, task := range t.bg.list() {
			if task.ID != choice.Value {
				continue
			}
			if !task.Done.IsZero() {
				result := task.Result
				t.app.Do(func() { t.app.Print(result) })
				return
			}
			if t.confirmRemove(firstLine(task.Prompt), "cancel this running task") {
				task.cancel()
				t.dim("%s cancelled", task.ID)
			}
		}
	})
}

// daemon toggles the scheduler that fires reminders.
func (t *repl) daemon(rest string) {
	switch strings.ToLower(strings.TrimSpace(rest)) {
	case "on":
		t.startDaemon()
	case "off":
		t.stopDaemon()
	case "status":
		if t.daemonOn() {
			t.dim("daemon running — %d reminders scheduled", len(loadReminders()))
			return
		}
		t.dim("daemon stopped — /daemon on")
	case "":
		// Bare /daemon toggles, which is what loop does.
		if t.daemonOn() {
			t.stopDaemon()
			return
		}
		t.startDaemon()
	default:
		t.fail("usage: /daemon on|off|status")
	}
}

func (t *repl) daemonOn() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.daemonStop != nil
}

// startDaemon begins the one-second tick that fires due reminders.
func (t *repl) startDaemon() {
	t.mu.Lock()
	if t.daemonStop != nil {
		t.mu.Unlock()
		t.dim("daemon already running")
		return
	}
	stop := make(chan struct{})
	t.daemonStop = stop
	t.mu.Unlock()

	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-tick.C:
				// Muted rather than stopped: the daemon keeps running so
				// turning reminders back on does not require restarting it,
				// and nothing is deleted.
				if !config.LoadSettings().RemindersOn() {
					continue
				}
				for _, r := range dueReminders(now) {
					reminder := r
					t.app.Do(func() {
						th := t.app.Theme()
						t.app.Print(th.Fg(tui.SlotWarning, th.Bold("reminder")) + th.Fg(tui.SlotText, " "+reminder.Text))
					})
				}
			}
		}
	}()
	t.dim("daemon running — reminders will fire here")
}

func (t *repl) stopDaemon() {
	t.mu.Lock()
	stop := t.daemonStop
	t.daemonStop = nil
	t.mu.Unlock()
	if stop == nil {
		t.dim("daemon not running")
		return
	}
	close(stop)
	t.dim("daemon stopped")
}

// runDetached runs a background prompt as a full turn in its own session.
//
// Its own session, not this one: two turns appending to one conversation
// interleave into a history neither of them wrote, and the model would then
// read the other task's work as its own context.
func (t *repl) runDetached(ctx context.Context, prompt string) (string, error) {
	run, err := newRun(t.cfg)
	if err != nil {
		return "", err
	}
	run.Permissions = t.run.Permissions
	if err := run.Session.SetTitle("background: " + firstLine(prompt)); err != nil {
		return "", err
	}
	// The stream is drained and discarded: a background turn reports its
	// result when it finishes, because two writers on one transcript is
	// unreadable. Draining is not optional — abandoning the channel blocks
	// the producer and leaks the provider connection.
	discard := func(stream <-chan ai.StreamPart) error {
		for range stream {
		}
		return nil
	}
	if err := run.Turn(ctx, prompt, discard); err != nil {
		return "", err
	}
	return run.Session.LastAssistant(), nil
}
