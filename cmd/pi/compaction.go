package main

import (
	"fmt"

	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/tui"
)

// Compaction as the transcript shows it (loop's turn-emitter compact
// handlers): the working line says "Compacting" while it runs, and a
// finished compaction lands in the conversation as a [compaction] box, with
// the context meter reset to what the compacted window holds.

// compaction is one finished compaction, whatever started it.
type compaction struct {
	summary, handoff, truncated, focus string
	tokensBefore, tokensAfter          int
}

// compactStarted puts compaction on the working line. A compaction inside a
// turn re-labels the turn's line; an idle one shows its own, unless a turn
// the user started already owns it.
func (t *repl) compactStarted(idle bool) {
	t.mu.Lock()
	t.idleCompacting = idle
	t.idleShown = idle && !t.turning
	shown := t.idleShown
	t.mu.Unlock()
	switch {
	case shown:
		t.do(func() { t.app.LoaderStart("Compacting while idle") })
	case !idle:
		t.do(func() { t.app.LoaderMessage("Compacting") })
	}
}

func (t *repl) compactEnded(end agent.CompactEndEvent) {
	t.mu.Lock()
	idle, shown := t.idleCompacting, t.idleShown
	t.idleCompacting, t.idleShown = false, false
	turning := t.turning
	t.mu.Unlock()
	done := compaction{summary: end.Summary, handoff: end.Handoff, truncated: end.Truncated,
		tokensBefore: end.TokensBefore, tokensAfter: end.TokensAfter}
	if idle {
		if shown && !turning {
			t.do(t.app.LoaderStop)
		}
		t.idleCompactionEnded(end, done)
		return
	}
	t.do(func() { t.app.LoaderMessage(tui.DefaultLoaderMessage) })
	switch {
	case end.Aborted:
		t.dim("compact aborted")
	case end.Error != "":
		// Not fatal: the session is untouched and this turn may still fit.
		t.fail("compact failed: %s — continuing uncompacted", end.Error)
	case end.Summary != "" || end.Handoff != "":
		t.showCompaction(done)
	}
}

// idleCompactionEnded reports an idle compaction — one cancelled because the
// user came back is not news.
func (t *repl) idleCompactionEnded(end agent.CompactEndEvent, done compaction) {
	switch {
	case end.Error != "":
		t.fail("idle compaction failed: %s", end.Error)
	case !end.Aborted && end.Summary != "":
		t.dim("%s", fmt.Sprintf("⟲ Compacted while idle, before the prompt cache expired · %s → %s tokens",
			formatTokens(end.TokensBefore), formatTokens(end.TokensAfter)))
		t.showCompaction(done)
	}
}

// showCompaction adds the [compaction] box and resets the context meter:
// the last measured size was the context this just replaced, and until the
// next request measures it, the compacted window's estimate is the honest
// number.
func (t *repl) showCompaction(c compaction) {
	if c.truncated != "" {
		t.fail("summary failed: %s — truncated the older conversation instead", c.truncated)
	}
	t.do(func() {
		t.app.AddCompaction(tui.Compaction{Summary: c.summary, Handoff: c.handoff, Focus: c.focus, TokensBefore: c.tokensBefore})
		if c.tokensAfter > 0 {
			t.app.SetContextTokens(c.tokensAfter)
		}
	})
}
