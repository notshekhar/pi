package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/schedule"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// /reminder — one-time or cron-scheduled notes that fire in the transcript.
//
// Stored as a single JSON file rather than one per reminder: the whole set is
// rewritten on every change and read on every tick, and at the sizes involved
// (a handful) one file is simpler and atomic to replace.
//
// Repeats are CRON, not an interval. An interval was the easier thing to
// build and the wrong thing to have: the reminders people actually set are
// "every weekday at 9" and "the first of the month", and an interval can
// express neither. The parser lives in internal/modules/core/schedule.

// MaxReminders caps the list. Without a cap a stuck loop can fill the file.
const MaxReminders = 10

// Reminder is one scheduled note.
type Reminder struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	// At is when a one-time reminder fires. For a cron one it is the NEXT
	// firing, recomputed each time it fires.
	At time.Time `json:"at"`
	// Cron is the expression for a repeating reminder; empty means one-time.
	Cron    string `json:"cron,omitempty"`
	Enabled bool   `json:"enabled"`
}

// Kind is "once" or "cron".
func (r Reminder) Kind() string {
	if r.Cron != "" {
		return "cron"
	}
	return "once"
}

// Schedule is the human-readable form shown in the picker.
func (r Reminder) Schedule() string {
	when := tui.FormatWhen(r.At)
	if r.Cron != "" {
		when = r.Cron
	}
	label := r.Kind() + " " + when
	if !r.Enabled {
		label += "  (off)"
	}
	return label
}

func remindersPath() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "reminders.json"), nil
}

// loadReminders reads the stored set. A missing or unreadable file is an
// EMPTY list, not an error: reminders are a convenience, and refusing to
// start over a corrupt one would be worse than losing them.
func loadReminders() []Reminder {
	path, err := remindersPath()
	if err != nil {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var out []Reminder
	if err := json.Unmarshal(data, &out); err != nil {
		return nil
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

func saveReminders(list []Reminder) error {
	path, err := remindersPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// reminders is `/reminder`: the manager, always.
//
// There is no one-line form. A reminder is three answers — the text, whether
// it repeats, and when — and the one-liner could only express the simplest of
// them while looking like it covered the rest.
func (t *repl) reminders(string) {
	const addReminder = "\x00add"
	t.manage(func() (string, []tui.Item) {
		list := loadReminders()
		items := []tui.Item{{
			Value:       addReminder,
			Label:       "+ add reminder…",
			Description: "one-time or cron-scheduled",
		}}
		for _, r := range list {
			items = append(items, tui.Item{Value: r.ID, Label: r.Text, Description: r.Schedule()})
		}
		return fmt.Sprintf("Reminders · %d", len(list)), items
	}, func(choice tui.Item) {
		if choice.Value == addReminder {
			t.addReminder()
			return
		}
		t.editReminder(choice.Value)
	})
}

// addReminder asks for the text, then the schedule.
func (t *repl) addReminder() {
	if len(loadReminders()) >= MaxReminders {
		t.dim("reminder limit reached (max %d) — delete one first", MaxReminders)
		return
	}
	text := strings.TrimSpace(t.ask("reminder text", ""))
	if text == "" {
		return
	}
	at, expr, ok := t.askSchedule("")
	if !ok {
		return
	}
	list := append(loadReminders(), Reminder{
		ID:      fmt.Sprintf("%d", time.Now().UnixNano()),
		Text:    text,
		At:      at,
		Cron:    expr,
		Enabled: true,
	})
	if err := saveReminders(list); err != nil {
		t.fail("reminder: %s", err)
		return
	}
	t.dim("reminder added — %s", text)
}

// editReminder is the action menu for one reminder.
func (t *repl) editReminder(id string) {
	list := loadReminders()
	var current Reminder
	found := false
	for _, r := range list {
		if r.ID == id {
			current, found = r, true
			break
		}
	}
	if !found {
		return
	}

	toggleLabel := "disable"
	if !current.Enabled {
		toggleLabel = "enable"
	}
	action := t.choose(current.Text,
		tui.Item{Value: "toggle", Label: toggleLabel},
		tui.Item{Value: "text", Label: "edit text", Description: current.Text},
		tui.Item{Value: "schedule", Label: "edit schedule", Description: current.Schedule()},
		tui.Item{Value: "delete", Label: "delete"},
	)
	if action == nil {
		return
	}

	switch action.Value {
	case "toggle":
		current.Enabled = !current.Enabled
	case "text":
		text := strings.TrimSpace(t.ask("reminder text", current.Text))
		if text == "" {
			return
		}
		current.Text = text
	case "schedule":
		at, expr, ok := t.askSchedule(current.Cron)
		if !ok {
			return
		}
		// Re-enabled with the new schedule: editing when something fires is
		// how you bring back a reminder you had switched off.
		current.At, current.Cron, current.Enabled = at, expr, true
	case "delete":
		kept := make([]Reminder, 0, len(list))
		for _, r := range list {
			if r.ID != id {
				kept = append(kept, r)
			}
		}
		if err := saveReminders(kept); err != nil {
			t.fail("reminder: %s", err)
		}
		return
	}

	for i := range list {
		if list[i].ID == id {
			list[i] = current
		}
	}
	if err := saveReminders(list); err != nil {
		t.fail("reminder: %s", err)
	}
}

// askSchedule asks once-or-cron, then for the detail. ok is false on cancel.
func (t *repl) askSchedule(currentExpr string) (at time.Time, expr string, ok bool) {
	kind := t.choose("Reminder schedule",
		tui.Item{Value: "once", Label: "once", Description: "10m · 18:30 · 2026-06-15 09:00"},
		tui.Item{Value: "cron", Label: "cron", Description: "second-level, up to 6 fields, e.g. 0 */45 * * * *"},
	)
	if kind == nil {
		return time.Time{}, "", false
	}

	if kind.Value == "once" {
		raw := t.ask("when (10m / 18:30 / 2026-06-15 09:00)", "")
		when, valid := parseOnceWhen(raw, time.Now())
		if !valid || !when.After(time.Now()) {
			t.fail("can't parse a future time from: %s", raw)
			return time.Time{}, "", false
		}
		return when, "", true
	}

	raw := strings.TrimSpace(t.ask("cron expression", currentExpr))
	if raw == "" {
		return time.Time{}, "", false
	}
	cron, err := schedule.Parse(raw)
	if err != nil {
		t.fail("invalid cron expression: %s — %s", raw, err)
		return time.Time{}, "", false
	}
	next := cron.Next(time.Now())
	if next.IsZero() {
		t.fail("that expression never fires: %s", raw)
		return time.Time{}, "", false
	}
	return next, raw, true
}

var (
	// A duration from now: "10m", "1h30m", "2d".
	durationPattern = regexp.MustCompile(`^(\d+[smhd])+$`)
	// A clock time today, or tomorrow if it has already passed.
	clockPattern = regexp.MustCompile(`^(\d{1,2}):(\d{2})$`)
	// An absolute date and time.
	stampPattern = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})[ T](\d{1,2}):(\d{2})$`)
)

// parseOnceWhen reads the three spellings a one-time reminder accepts.
//
// Deliberately strict — a bare number is REJECTED rather than guessed at.
// "/reminder … 20" could mean twenty minutes or twenty hundred hours, and a
// reminder that fires at the wrong time is worse than one that refuses to be
// set.
func parseOnceWhen(input string, now time.Time) (time.Time, bool) {
	trimmed := strings.TrimSpace(input)
	if trimmed == "" {
		return time.Time{}, false
	}

	if durationPattern.MatchString(strings.ToLower(trimmed)) {
		if d, ok := parseDuration(strings.ToLower(trimmed)); ok {
			return now.Add(d), true
		}
		return time.Time{}, false
	}

	if m := clockPattern.FindStringSubmatch(trimmed); m != nil {
		hour, minute := atoi(m[1]), atoi(m[2])
		if hour > 23 || minute > 59 {
			return time.Time{}, false
		}
		at := time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location())
		if !at.After(now) {
			at = at.AddDate(0, 0, 1)
		}
		return at, true
	}

	if m := stampPattern.FindStringSubmatch(trimmed); m != nil {
		at := time.Date(atoi(m[1]), time.Month(atoi(m[2])), atoi(m[3]),
			atoi(m[4]), atoi(m[5]), 0, 0, now.Location())
		return at, true
	}
	return time.Time{}, false
}

// atoi is strconv.Atoi for text a regexp has already proved is numeric.
func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

// dueReminders returns the reminders that have come due, and rewrites the
// stored set: cron ones are rescheduled, one-time ones are removed.
func dueReminders(now time.Time) []Reminder {
	list := loadReminders()
	var fired []Reminder
	kept := make([]Reminder, 0, len(list))
	for _, r := range list {
		if !r.Enabled || r.At.After(now) {
			kept = append(kept, r)
			continue
		}
		fired = append(fired, r)
		if r.Cron == "" {
			continue
		}
		// Rescheduled from NOW rather than from the missed firing: a laptop
		// that slept through six firings must not then fire six times, and
		// the next one the user cares about is the next one from here.
		cron, err := schedule.Parse(r.Cron)
		if err != nil {
			continue // a corrupt expression drops the reminder rather than looping
		}
		next := cron.Next(now)
		if next.IsZero() {
			continue
		}
		r.At = next
		kept = append(kept, r)
	}
	if len(fired) > 0 {
		_ = saveReminders(kept)
	}
	return fired
}
