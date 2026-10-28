package main

import (
	"runtime"
	"strings"

	"github.com/notshekhar/pi/internal/core/sound"
)

// /sound — loop's: bare toggles on/off; `max` is only ever asked for
// explicitly, so a toggle can never surprise anyone with the chatty tier.
func (t *repl) soundCmd(args string) {
	arg := strings.ToLower(strings.TrimSpace(args))
	next := ""
	switch arg {
	case "":
		if sound.Level() == "off" {
			next = "on"
		} else {
			next = "off"
		}
	case "on", "off", "max":
		next = arg
	default:
		t.fail("usage: /sound [on|off|max]")
		return
	}
	if err := sound.SetLevel(next); err != nil {
		t.fail("%s", err)
		return
	}
	// Confirmed by ear as well; at off, the silence is the confirmation.
	if next != "off" {
		sound.Cue(sound.Click)
	}
	where := ""
	if runtime.GOOS != "darwin" {
		where = " (terminal bell — chimes need macOS)"
	}
	t.dim("sound %s%s", next, where)
}
