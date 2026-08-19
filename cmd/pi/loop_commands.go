package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// sessionInfo prints what this session is and what it has cost.
//
// The fields are the ones you need when something has gone sideways and you
// are about to ask someone else about it: which session, which model, which
// directory, and what it has cost so far.
func (t *repl) sessionInfo() {
	s := t.run.Session
	m := s.Meta
	in, out, _, cost := t.app.Stats()
	t.app.Do(func() {
		th := t.app.Theme()
		row := func(label, value string) string {
			return th.Fg(tui.SlotDim, padRight(label, 13)) + th.Fg(tui.SlotText, value)
		}
		id := m.ID
		if id == "" {
			id = "unsaved"
		}
		lines := []string{row("session id", id)}
		if m.Name != "" {
			lines = append(lines, row("name", m.Name))
		}
		lines = append(lines,
			row("model", t.cfg.FullID()),
			row("provider", t.cfg.Provider),
			row("thinking", thinkingName(t.cfg.Reasoning)),
			row("cwd", t.cfg.CWD),
			row("messages", strconv.Itoa(len(s.Messages))),
			row("tokens", fmt.Sprintf("in:%d out:%d", in, out)),
			row("cost (sess)", fmt.Sprintf("$%.4f", cost)))
		t.app.Print(lines...)
	})
}

// steak draws a token-usage heatmap, GitHub-contributions style.
//
// Built from the session HEADERS, which carry per-session totals and an
// updated timestamp — so a year renders without opening a single
// conversation. The cost is that usage lands on the day a session was last
// touched rather than being spread across the days it ran, which for a
// coding session is the same day nearly always.
func (t *repl) steak(rest string) {
	t.app.Do(func() { t.app.Print(t.steakLines(rest)...) })
}

// steakLines is the heatmap and its stats, so /cost can lead with it.
func (t *repl) steakLines(rest string) []string {
	th := t.app.Theme()
	rest = strings.TrimSpace(rest)

	// The default window is the TRAILING year, not the calendar one. On the
	// third of January a calendar year is three columns of squares, which
	// says nothing; the trailing year always shows the same amount of
	// history no matter when it is asked.
	var from, to time.Time
	period := "the last year"
	now := time.Now()
	if rest != "" {
		n, err := strconv.Atoi(rest)
		if err != nil || n < 2000 || n > 2100 {
			return []string{th.Fg(tui.SlotError, "steak: "+rest+" is not a year")}
		}
		from = time.Date(n, 1, 1, 0, 0, 0, 0, time.Local)
		to = time.Date(n, 12, 31, 0, 0, 0, 0, time.Local)
		period = strconv.Itoa(n)
	} else {
		to = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
		from = to.AddDate(-1, 0, 1)
	}

	metas, err := session.List()
	if err != nil {
		return []string{th.Fg(tui.SlotError, "steak: "+err.Error())}
	}
	perDay := map[string]int64{}
	var total int64
	for _, m := range metas {
		day := time.Date(m.Updated.Year(), m.Updated.Month(), m.Updated.Day(), 0, 0, 0, 0, time.Local)
		if day.Before(from) || day.After(to) {
			continue
		}
		n := m.InputTokens + m.OutputTokens
		perDay[day.Format("2006-01-02")] += n
		total += n
	}
	if total == 0 {
		return []string{th.Fg(tui.SlotDim, "no recorded usage in "+period)}
	}

	out := []string{th.Fg(tui.SlotAccent, th.Bold(
		fmt.Sprintf("%s tokens in %s", formatTokens(int(total)), period))), ""}
	out = append(out, steakGrid(th, perDay, from, to)...)
	return append(out, steakStats(th, perDay)...)
}

// steakGrid lays the range out as seven rows of weeks, Sunday at the top.
//
// One cell per day with NO separating space: 53 weeks at two cells each is
// 106 columns, which wrapped on a 100-column terminal and turned the grid
// into unreadable ribbon. At one cell it is 53 wide and fits anywhere.
func steakGrid(th *tui.Theme, perDay map[string]int64, from, to time.Time) []string {
	// Back up to the Sunday on or before the start so every column is a full
	// week and the rows line up with the weekday labels.
	start := from.AddDate(0, 0, -int(from.Weekday()))

	var peak int64
	for _, n := range perDay {
		peak = max(peak, n)
	}

	const labelWidth = 4
	rows := make([]strings.Builder, 7)

	// The month row is painted into a fixed-width canvas, with each label
	// DROPPED AT its own column. Appending them to a builder instead packs
	// them against each other ("AugSepOct Nov"), because a month is three
	// cells wide and a week is one — the gaps have to come from the columns,
	// not from the labels.
	weeks := 0
	for day := start; !day.After(to); day = day.AddDate(0, 0, 7) {
		weeks++
	}
	monthRow := make([]rune, weeks)
	for i := range monthRow {
		monthRow[i] = ' '
	}

	column := 0
	lastMonth := -1
	for day := start; !day.After(to); day = day.AddDate(0, 0, 7) {
		if m := int(day.Month()); !day.Before(from) && m != lastMonth {
			for i, r := range day.Format("Jan") {
				if column+i < len(monthRow) {
					monthRow[column+i] = r
				}
			}
			lastMonth = m
		}
		for w := 0; w < 7; w++ {
			d := day.AddDate(0, 0, w)
			if d.Before(from) || d.After(to) {
				rows[w].WriteString(" ")
				continue
			}
			rows[w].WriteString(th.Heat(steakLevel(perDay[d.Format("2006-01-02")], peak), "■"))
		}
		column++
	}

	pad := strings.Repeat(" ", labelWidth)
	out := []string{th.Fg(tui.SlotDim, pad+strings.TrimRight(string(monthRow), " "))}
	// Every other weekday labelled: seven labels in a column four cells wide
	// is a wall of text beside a chart nobody reads row-wise.
	labels := []string{"", "Mon", "", "Wed", "", "Fri", ""}
	for i := range rows {
		out = append(out, th.Fg(tui.SlotDim, padRight(labels[i], labelWidth))+rows[i].String())
	}
	legend := ""
	for level := 0; level <= 4; level++ {
		legend += th.Heat(level, "■")
	}
	return append(out, "", pad+th.Fg(tui.SlotDim, "Less ")+legend+th.Fg(tui.SlotDim, " More"))
}

// steakStats is the block under the wall: the numbers a streak chart is
// actually read for.
func steakStats(th *tui.Theme, perDay map[string]int64) []string {
	days := make([]string, 0, len(perDay))
	for day, n := range perDay {
		if n > 0 {
			days = append(days, day)
		}
	}
	if len(days) == 0 {
		return nil
	}
	sort.Strings(days)

	var busiest string
	var busiestTokens int64
	for day, n := range perDay {
		if n > busiestTokens {
			busiest, busiestTokens = day, n
		}
	}

	// Streaks are runs of CONSECUTIVE calendar days, so they have to be
	// walked by date rather than by index — two entries next to each other in
	// a sorted list can be a month apart.
	longest, run := 0, 0
	var previous time.Time
	for _, day := range days {
		at, err := time.ParseInLocation("2006-01-02", day, time.Local)
		if err != nil {
			continue
		}
		if !previous.IsZero() && at.Sub(previous) == 24*time.Hour {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
		previous = at
	}
	// The current streak only counts if it reaches today or yesterday —
	// a run that ended last month is history, not a streak.
	current := 0
	today := time.Now().Truncate(24 * time.Hour)
	if !previous.IsZero() && today.Sub(previous.Truncate(24*time.Hour)) <= 24*time.Hour {
		current = run
	}

	stat := func(label, value, note string) string {
		row := "  " + th.Fg(tui.SlotDim, padRight(label, 16)) + th.Fg(tui.SlotAccent, value)
		if note != "" {
			row += "   " + th.Fg(tui.SlotDim, note)
		}
		return row
	}
	plural := func(n int) string {
		if n == 1 {
			return "1 day"
		}
		return fmt.Sprintf("%d days", n)
	}
	out := []string{
		"",
		stat("current streak", plural(current), ""),
		stat("longest streak", plural(longest), ""),
		stat("active days", strconv.Itoa(len(days)), ""),
	}
	if busiest != "" {
		when := busiest
		if at, err := time.ParseInLocation("2006-01-02", busiest, time.Local); err == nil {
			when = at.Format("Jan 2, 2006")
		}
		out = append(out, stat("busiest day", formatTokens(int(busiestTokens))+" tokens", when))
	}
	return out
}

// steakLevel buckets a day's usage into one of the four heat steps.
//
// Buckets are fractions of the PEAK day rather than fixed token counts: a
// week of light use should still show contrast, and a fixed scale would
// render it as one flat colour.
func steakLevel(n, peak int64) int {
	if n == 0 || peak == 0 {
		return 0
	}
	switch frac := float64(n) / float64(peak); {
	case frac > 0.66:
		return 4
	case frac > 0.33:
		return 3
	case frac > 0.1:
		return 2
	}
	return 1
}

// attach adds an image to the next message: a path if given, else whatever
// the clipboard holds.
func (t *repl) attach(rest string) {
	path := strings.TrimSpace(rest)
	if path == "" {
		if img, err := clipboardImage(); err == nil && img != "" {
			path = img
		} else {
			t.dim("no image on the clipboard — /attach <path>")
			return
		}
	}
	path = tui.UnquotePath(path)
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.fail("attach: %s", err)
		return
	}
	// DetectAttachment is the same path a drag-drop takes, so a typed path
	// and a dropped file end up as the same attachment.
	att, ok := tui.DetectAttachment(path)
	if !ok {
		t.fail("attach: %s is not an image this can send", filepath.Base(path))
		return
	}
	t.app.Do(func() {
		t.app.AddAttachment(att)
		t.dim("attached %s", att.Label())
	})
}

// paste puts the clipboard into the message: an image is attached, text goes
// into the draft.
//
// loop splits these across /attach and /paste; here one command covers both,
// because a user reaching for /paste has a clipboard, not a type in mind.
func (t *repl) paste(rest string) {
	if strings.TrimSpace(rest) != "" {
		t.attach(rest)
		return
	}
	if img, err := clipboardImage(); err == nil && img != "" {
		t.attach(img)
		return
	}
	text, err := clipboardRead()
	if err != nil {
		t.fail("paste: %s", err)
		return
	}
	if strings.TrimSpace(text) == "" {
		t.dim("clipboard is empty")
		return
	}
	t.app.Do(func() { t.app.InsertDraft(text) })
}

// importSession reads a JSONL conversation into a new session.
func (t *repl) importSession(rest string) {
	path := tui.UnquotePath(strings.TrimSpace(rest))
	if path == "" {
		t.dim("usage: /import <path.jsonl>")
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.fail("import: %s", err)
		return
	}
	// Validate BEFORE creating anything: a session created and then found to
	// be unreadable leaves an empty stub in the picker forever.
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !json.Valid([]byte(line)) {
			t.fail("import: %s line %d is not JSON", filepath.Base(path), i+1)
			return
		}
	}

	created, err := session.Create(t.cfg.FullID(), t.cfg.CWD)
	if err != nil {
		t.fail("import: %s", err)
		return
	}
	if err := os.WriteFile(created.Path, data, 0o600); err != nil {
		t.fail("import: %s", err)
		return
	}
	loaded, err := session.Load(created.ID)
	if err != nil {
		t.fail("import: %s", err)
		return
	}
	if err := loaded.SetTitle("imported " + filepath.Base(path)); err != nil {
		t.fail("import: %s", err)
		return
	}
	t.dim("imported %d messages — /resume to open it", len(loaded.Messages))
}

// sortedMetas is session.List with the newest first, for the commands that
// summarise across sessions.
func sortedMetas() []session.Meta {
	metas, err := session.List()
	if err != nil {
		return nil
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Updated.After(metas[j].Updated) })
	return metas
}
