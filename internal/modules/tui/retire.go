package tui

// Retirement: handing finished blocks to the terminal so they become real
// scrollback.
//
// Without this the inline renderer only LOOKS right. The live region is
// capped at the terminal height and repainted in place, so once a
// conversation outgrows the screen the head is overwritten row by row and is
// gone — the same loss the alternate screen caused, just arrived at
// differently. Scrolling the terminal back showed nothing.
//
// A block is retired by PRINTING its lines above the region and then
// DELETING it from a.entries. Deleting is what makes this safe: a retired
// block cannot render again, cannot be selected, cannot be folded or
// expanded, so there is no way for it to appear twice. The terminal owns
// those rows now, which is exactly the arrangement loop has.
//
// Retirement is therefore irreversible, and only ever applied to blocks that
// have nothing left to say.

// retire commits whole entries that have scrolled above the viewport,
// returning the lines to print above the live region.
//
// avail is the number of transcript rows the viewport can show.
func (a *App) retire(avail int) []string {
	// Nav is reading the transcript and scroll is looking back through it;
	// retiring under either would delete what the user is looking at.
	// expandAll can change every block's height, so heights measured now
	// would not be the heights drawn.
	if a.nav || a.scroll > 0 || a.expandAll {
		return nil
	}

	var commit []string
	for {
		over := a.transcriptLen() - avail
		if over <= 0 || len(a.entries) <= 1 {
			break
		}
		e := a.entries[0]
		if !a.retirable(e) {
			break
		}
		// Rendered as though nav were OFF: a retired block is handed to the
		// terminal for good, and a hint that said "→ to expand" would still
		// be sitting in the scrollback long after nav was left — pointing at
		// a key that no longer does anything to a block that can no longer
		// be selected.
		lines := e.render(a.theme, a.size.Cols, AnimTick(), false, false)
		// Only if the block clears the top of the viewport ENTIRELY. A block
		// straddling the edge must stay: half of it is still on screen, and
		// retiring it would print the whole thing above a region that then
		// redraws none of it, leaving the visible half duplicated.
		if len(lines) > over {
			break
		}
		commit = append(commit, lines...)
		a.dropFirstEntry()
	}
	return commit
}

// retirable reports whether the front block is finished with.
func (a *App) retirable(e *entry) bool {
	// Still moving, so its final appearance is not known yet.
	if e.animated() || e.streaming {
		return false
	}
	// The live stream and thinking blocks are written to by name; retiring
	// one would leave the writer mutating a detached block.
	if e == a.stream || e == a.thinking {
		return false
	}
	// A folded run renders as ONE header row standing in for several
	// entries. Retiring its first member alone would print a member that is
	// not drawn on its own and leave the rest to re-fold into a run with the
	// wrong count.
	if _, folded := a.groups()[0]; folded {
		return false
	}
	return true
}

// dropFirstEntry removes the front block and shifts the index-keyed state
// that points at the ones behind it.
func (a *App) dropFirstEntry() {
	dropped := a.entries[0]
	a.entries = a.entries[1:]

	// groupsOpen is keyed by POSITION, so every key has to slide down one or
	// the wrong runs come back open.
	if len(a.groupsOpen) > 0 {
		shifted := make(map[int]bool, len(a.groupsOpen))
		for i, open := range a.groupsOpen {
			if i > 0 {
				shifted[i-1] = open
			}
		}
		a.groupsOpen = shifted
	}

	// A retired tool must leave the id map, or a late result would be
	// written into a block nothing renders.
	for id, e := range a.tools {
		if e == dropped {
			delete(a.tools, id)
		}
	}
}
