package main

import (
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/tui"
)

// /timer — a countdown in the status line.
//
// Small, but it is the reason the frame clock can be asked to keep running
// with nothing else animating: see App.live.

// timer sets, reports, or cancels the countdown.
func (t *repl) timer(rest string) {
	input := strings.ToLower(strings.TrimSpace(rest))

	switch input {
	case "off", "cancel":
		t.app.Do(func() { t.app.SetTimer(time.Time{}) })
		t.timerLabel = ""
		t.dim("timer cancelled")
		return
	case "":
		ends := t.app.TimerEndsAt()
		if ends.IsZero() {
			t.dim("no timer running — usage: /timer 1h30m · /timer off")
			return
		}
		t.dim("timer: %s left (of %s)", tui.FormatCountdown(time.Until(ends)), t.timerLabel)
		return
	}

	d, ok := parseDuration(input)
	if !ok {
		t.fail("can't parse duration: %s — try 30s, 5m, 1h30m, 1d", input)
		return
	}
	ends := time.Now().Add(d)
	t.timerLabel = input
	t.app.Do(func() { t.app.SetTimer(ends) })
	t.dim("timer set — %s", input)

	// One shot, and it announces itself even if the deadline passes while a
	// turn is running: the status line alone is easy to miss.
	go func() {
		time.Sleep(d)
		if !t.app.TimerEndsAt().Equal(ends) {
			return // cancelled or replaced
		}
		t.app.Do(func() {
			t.app.SetTimer(time.Time{})
			th := t.app.Theme()
			t.app.Print(th.Fg(tui.SlotWarning, th.Bold("timer up")) + th.Fg(tui.SlotMuted, " — "+input))
		})
	}()
}

// parseDuration reads "1h30m", "45s", "2d" and the like.
//
// Deliberately not time.ParseDuration: that rejects "1d", which is the unit
// a person reaches for first when setting a reminder, and accepts "1.5h"
// spellings nobody types at a prompt.
func parseDuration(s string) (time.Duration, bool) {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return 0, false
	}
	units := map[byte]time.Duration{
		's': time.Second,
		'm': time.Minute,
		'h': time.Hour,
		'd': 24 * time.Hour,
	}

	var total time.Duration
	var digits strings.Builder
	seen := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= '0' && c <= '9' {
			digits.WriteByte(c)
			continue
		}
		unit, ok := units[c]
		if !ok || digits.Len() == 0 {
			return 0, false
		}
		n, err := strconv.Atoi(digits.String())
		if err != nil {
			return 0, false
		}
		total += time.Duration(n) * unit
		digits.Reset()
		seen = true
	}
	// A bare number is minutes — "/timer 20" means twenty minutes, not twenty
	// nanoseconds.
	if digits.Len() > 0 {
		n, err := strconv.Atoi(digits.String())
		if err != nil {
			return 0, false
		}
		total += time.Duration(n) * time.Minute
		seen = true
	}
	if !seen || total <= 0 {
		return 0, false
	}
	return total, true
}
