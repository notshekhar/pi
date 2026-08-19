package tui

import (
	"strings"
	"testing"
)

func paint(w *inlineWriter, b *strings.Builder, lines []string, curRow int) string {
	b.Reset()
	w.out = b
	w.Paint(nil, lines, 40, curRow, 0, true)
	return b.String()
}

// The first paint has no region to return to, so it must not try to move up.
// Moving up on the first frame walks into rows the shell owns and overwrites
// the user's prompt.
func TestFirstPaintDoesNotMoveUp(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	out := paint(w, &b, []string{"a", "b", "c"}, 2)
	if strings.Contains(out, "A") && strings.Contains(out, "\x1b[3A") {
		t.Fatalf("first paint moved up: %q", out)
	}
	if w.painted != 3 || w.row != 2 {
		t.Fatalf("region = %d rows, cursor row %d; want 3, 2", w.painted, w.row)
	}
}

// The second paint must return to the top of the region — cursor row rows up
// — before it touches anything.
func TestRepaintReturnsToRegionTop(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)
	out := paint(w, &b, []string{"x", "b", "c"}, 2)
	if !strings.Contains(out, "\x1b[2A\r") {
		t.Fatalf("repaint did not return to the region top: %q", out)
	}
}

// A frame identical to the one on screen writes no content at all. This is
// the diff's whole purpose: the app repaints on every keystroke and every
// streaming mutation, and most of those frames differ from the last by one
// row or by nothing.
func TestUnchangedFrameWritesNothing(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)
	out := paint(w, &b, []string{"a", "b", "c"}, 2)
	for _, text := range []string{"a", "b", "c"} {
		if strings.Contains(out, text) {
			t.Fatalf("an unchanged frame rewrote %q: %q", text, out)
		}
	}
}

// Only the span that actually changed is rewritten — the rows above and
// below it are left as the terminal already has them.
func TestOnlyTheChangedSpanIsRewritten(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"head", "old", "tail"}, 2)
	out := paint(w, &b, []string{"head", "new", "tail"}, 2)
	if !strings.Contains(out, "new") {
		t.Fatalf("the changed row was not painted: %q", out)
	}
	if strings.Contains(out, "head") || strings.Contains(out, "tail") {
		t.Fatalf("unchanged rows were repainted: %q", out)
	}
	// Down one from the top of the region, and back up one to the cursor row.
	if !strings.Contains(out, "\x1b[1B") {
		t.Fatalf("did not move to the changed row: %q", out)
	}
}

// Every frame is wrapped in synchronized output, so the terminal presents it
// whole instead of showing a repaint in progress.
func TestFrameIsSynchronized(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	out := paint(w, &b, []string{"a", "b"}, 1)
	if !strings.HasPrefix(out, syncBegin) {
		t.Fatalf("frame did not begin synchronized output: %q", out)
	}
	if !strings.HasSuffix(out, syncEnd) {
		t.Fatalf("frame did not end synchronized output: %q", out)
	}
}

// A row appended past the end of the previous frame has never been printed,
// so it is not "unchanged" just because it is blank — the region has to
// physically grow to reach it. Getting this wrong grows the row count
// without growing the region, and every later cursor move is off by that
// many rows.
func TestAppendedBlankRowsStillGrowTheRegion(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b"}, 1)
	out := paint(w, &b, []string{"a", "b", "", ""}, 3)
	if strings.Count(out, "\r\n") < 2 {
		t.Fatalf("the region did not grow by two rows: %q", out)
	}
	if w.painted != 4 || w.row != 3 {
		t.Fatalf("region = %d rows, cursor row %d; want 4, 3", w.painted, w.row)
	}
}

// A width change reflows every row, so nothing on screen can be trusted and
// the whole region is rewritten.
func TestWidthChangeRepaintsEveryRow(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)
	b.Reset()
	w.out = &b
	w.Paint(nil, []string{"a", "b", "c"}, 60, 2, 0, true)
	out := b.String()
	for _, text := range []string{"a", "b", "c"} {
		if !strings.Contains(out, text) {
			t.Fatalf("a width change did not rewrite %q: %q", text, out)
		}
	}
}

// Invalidate distrusts what is on screen while KEEPING the region's position:
// the next frame rewrites every row where the region already is. Reset there
// would write the recovery frame BELOW the damaged one.
func TestInvalidateRewritesInPlace(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)
	w.Invalidate()
	if w.painted != 3 {
		t.Fatalf("Invalidate forgot the region: painted=%d, want 3", w.painted)
	}
	out := paint(w, &b, []string{"a", "b", "c"}, 2)
	if !strings.Contains(out, "\x1b[2A") {
		t.Fatalf("the frame after Invalidate did not return to the region top: %q", out)
	}
	for _, text := range []string{"a", "b", "c"} {
		if !strings.Contains(out, text) {
			t.Fatalf("the frame after Invalidate did not rewrite %q: %q", text, out)
		}
	}
}

// A shrinking region must blank the rows it no longer occupies, or the tail
// of the previous frame stays printed under the new one — a closing menu
// leaving its own rows behind.
func TestShrinkingRegionErasesTheRowsItLeaves(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c", "d", "e"}, 4)
	out := paint(w, &b, []string{"a", "b"}, 1)
	// One erase per abandoned row, and no more: rows "a" and "b" did not
	// change, so the diff leaves them alone.
	if got := strings.Count(out, "\x1b[K"); got != 3 {
		t.Fatalf("erased %d rows, want the 3 abandoned ones: %q", got, out)
	}
	if strings.Contains(out, "a") && strings.Contains(out, "b") {
		t.Fatalf("shrinking rewrote rows that did not change: %q", out)
	}
	// And it must come back up out of them.
	if !strings.Contains(out, "\x1b[3A") {
		t.Fatalf("shrink did not return from the erased rows: %q", out)
	}
	if w.painted != 2 {
		t.Fatalf("region = %d rows; want 2", w.painted)
	}
}

// Committed lines are printed above the region and the region shrinks by
// what they covered, so the screen scrolls by exactly the amount committed.
func TestCommitPrintsAboveTheRegion(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)
	b.Reset()
	w.Paint([]string{"gone1", "gone2"}, []string{"c", "d", "e"}, 40, 2, 0, true)
	out := b.String()
	if !strings.Contains(out, "gone1") || !strings.Contains(out, "gone2") {
		t.Fatalf("committed lines were not printed: %q", out)
	}
	if strings.Index(out, "gone1") > strings.Index(out, "\rc") && strings.Contains(out, "\rc") {
		t.Fatal("committed lines were printed below the region")
	}
	if w.painted != 3 {
		t.Fatalf("region = %d rows; want 3", w.painted)
	}
	// The committed rows were written over the TOP of the old region, so what
	// survives of it is still a usable diff baseline: row "c" was already on
	// screen and must not be rewritten under it.
	if strings.Contains(strings.SplitN(out, "gone2", 2)[1], "\rc\x1b[K") {
		t.Fatalf("a commit threw away the baseline and rewrote an unchanged row: %q", out)
	}
}

// Exit erases the chrome and leaves the conversation behind.
func TestFinishErasesOnlyTheChrome(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"talk", "talk", "composer", "status"}, 3)
	b.Reset()
	w.out = &b
	w.Finish(2)
	out := b.String()
	// Cursor sits on the last row (3) and the chrome starts at row 2.
	if !strings.Contains(out, "\x1b[1A") {
		t.Fatalf("did not move up to the first chrome row: %q", out)
	}
	if !strings.Contains(out, "\x1b[J") {
		t.Fatalf("did not clear to the end of the screen: %q", out)
	}
	if w.painted != 0 {
		t.Fatal("region was not forgotten")
	}
}

// Resize makes the writer's bookkeeping untrue, and acting on it is what
// walks the cursor into rows it does not own.
func TestResetForgetsTheRegion(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)
	w.Reset()
	out := paint(w, &b, []string{"a", "b", "c"}, 2)
	if strings.Contains(out, "\x1b[2A\r") {
		t.Fatalf("paint after reset moved up: %q", out)
	}
}

// A region that GROWS is repainted in place, not appended below the old one.
//
// The regression this pins: a caller reset the writer when the region's
// height was about to change, on the theory that the old row count was now
// wrong. It is not — Paint walks up and rewrites — and forgetting the
// position instead wrote the new frame BELOW the old, leaving a full copy of
// the masthead on screen.
func TestGrowingRegionRepaintsInPlace(t *testing.T) {
	var b strings.Builder
	w := &inlineWriter{}
	paint(w, &b, []string{"a", "b", "c"}, 2)

	out := paint(w, &b, []string{"", "", "a", "b", "c"}, 4)
	// Back to the top of the OLD region first — two rows up from row 2.
	if !strings.Contains(out, "\x1b[2A") {
		t.Errorf("a growing region did not return to its top: %q", out)
	}
	if w.painted != 5 {
		t.Errorf("region = %d rows, want 5", w.painted)
	}
}

// And the caller-facing behaviour: toggling pinned input must not make the
// next frame land somewhere new.
func TestSetPinnedInputKeepsTheRegionPosition(t *testing.T) {
	a := &App{}
	a.out.painted, a.out.row = 12, 11

	a.SetPinnedInput(true)
	if a.out.painted != 12 || a.out.row != 11 {
		t.Errorf("pinning forgot the region: painted=%d row=%d, want 12/11", a.out.painted, a.out.row)
	}
	if !a.pinnedInput {
		t.Error("pinning did not take effect")
	}
}
