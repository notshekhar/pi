package tui

import (
	"strings"
	"testing"
)

// A terminal, enough of one to check the renderer against.
//
// The diff cannot be verified by reading the escape sequences it emits — that
// is how the region-walks-up-the-page class of bug survives review, because
// a frame that lands in the wrong place still LOOKS like a frame. What can be
// checked is the screen: feed the bytes to a model of a terminal and compare
// the result against the frame that was supposed to be drawn.
//
// It implements only what inlineWriter emits — relative cursor moves, erase
// to end of line, erase to end of screen, absolute column, and the scroll a
// newline on the last row causes — and it PANICS on anything else, so a new
// sequence cannot quietly go unmodelled.
type screen struct {
	cols, rows int
	grid       [][]rune
	row, col   int
	// history is what scrolled off the top: the terminal's scrollback, which
	// is where retired blocks are supposed to end up.
	history []string
}

func newScreen(cols, rows int) *screen {
	s := &screen{cols: cols, rows: rows, grid: make([][]rune, rows)}
	for i := range s.grid {
		s.grid[i] = blankRow(cols)
	}
	return s
}

func blankRow(cols int) []rune {
	row := make([]rune, cols)
	for i := range row {
		row[i] = ' '
	}
	return row
}

func (s *screen) feed(data string) {
	for i := 0; i < len(data); {
		c := data[i]
		switch {
		case c == '\r':
			s.col = 0
			i++
		case c == '\n':
			s.newline()
			i++
		case c == 0x1b:
			i += s.escape(data[i:])
		default:
			j := i
			for j < len(data) && data[j] != 0x1b && data[j] != '\r' && data[j] != '\n' {
				j++
			}
			s.put(data[i:j])
			i = j
		}
	}
}

// escape consumes one sequence and returns its length.
func (s *screen) escape(data string) int {
	if len(data) < 2 {
		panic("truncated escape")
	}
	if data[1] != '[' {
		panic("unmodelled escape: " + strings.ReplaceAll(data[:min(len(data), 8)], "\x1b", "ESC"))
	}
	end := 2
	for end < len(data) && !(data[end] >= 0x40 && data[end] <= 0x7e) {
		end++
	}
	if end >= len(data) {
		panic("truncated CSI")
	}
	params, final := data[2:end], data[end]
	n := end + 1
	// The private modes the renderer sets: cursor visibility and synchronized
	// output. Both are invisible to a screen's contents by construction.
	if strings.HasPrefix(params, "?") {
		switch final {
		case 'h', 'l':
			return n
		}
		panic("unmodelled private mode: " + params + string(final))
	}
	count := 1
	if params != "" {
		count = 0
		for _, r := range params {
			if r < '0' || r > '9' {
				panic("unmodelled CSI params: " + params)
			}
			count = count*10 + int(r-'0')
		}
	}
	switch final {
	case 'A':
		s.row = max(s.row-count, 0)
	case 'B':
		// Cursor-down STOPS at the last row. That is the whole reason a
		// growing region has to use newlines instead: CUD past the bottom
		// silently lands short and every later move is off by that much.
		s.row = min(s.row+count, s.rows-1)
	case 'G':
		s.col = min(max(count-1, 0), s.cols-1)
	case 'K':
		s.erase(s.row, s.col)
	case 'J':
		s.erase(s.row, s.col)
		for r := s.row + 1; r < s.rows; r++ {
			s.erase(r, 0)
		}
	default:
		panic("unmodelled CSI: " + params + string(final))
	}
	return n
}

func (s *screen) erase(row, from int) {
	for i := from; i < s.cols; i++ {
		s.grid[row][i] = ' '
	}
}

// put writes cells, not bytes. A terminal counts columns, and the difference
// is the whole reason this repo has its own width model — a row of box-drawing
// and a `❯` is three times as many bytes as cells.
func (s *screen) put(text string) {
	for _, r := range text {
		if s.col >= s.cols {
			panic("wrote past the right edge")
		}
		s.grid[s.row][s.col] = r
		s.col++
	}
}

// newline moves down, scrolling the screen when it is already at the bottom —
// which is what pushes the region's top into the terminal's scrollback.
func (s *screen) newline() {
	if s.row < s.rows-1 {
		s.row++
		return
	}
	s.history = append(s.history, strings.TrimRight(string(s.grid[0]), " "))
	copy(s.grid, s.grid[1:])
	s.grid[s.rows-1] = blankRow(s.cols)
}

func (s *screen) lines() []string {
	out := make([]string, s.rows)
	for i, l := range s.grid {
		out[i] = strings.TrimRight(string(l), " ")
	}
	return out
}

// paintFrames drives the writer through a sequence of frames and returns the
// screen. Every frame is checked against the screen as it lands, so the
// failure names the frame that broke rather than the last one.
func paintFrames(t *testing.T, cols, rows int, frames [][]string) *screen {
	t.Helper()
	sc := newScreen(cols, rows)
	var b strings.Builder
	w := &inlineWriter{out: &b}
	// Where the region sits. It starts where the shell left the cursor — the
	// top — and grows DOWNWARD; only once its last row reaches the bottom of
	// the screen does it start sliding up, one row per row of growth, with
	// what falls off going into scrollback.
	top := 0
	for n, frame := range frames {
		b.Reset()
		w.Paint(nil, frame, cols, len(frame)-1, 0, false)
		sc.feed(b.String())
		if over := top + len(frame) - 1 - (rows - 1); over > 0 {
			top -= over
		}
		got := sc.lines()
		for i, want := range frame {
			want = strings.TrimRight(want, " ")
			if got[top+i] != want {
				t.Fatalf("frame %d, screen row %d:\n got %q\nwant %q\n--- screen ---\n%s",
					n, top+i, got[top+i], want, strings.Join(got, "\n"))
			}
		}
	}
	return sc
}

// A frame that fills the whole screen and then SLIDES — every row taking the
// content of the row below it — is the shape a transcript makes once it
// outgrows the terminal, and the shape the renderer got wrong.
func TestFullScreenFrameThatSlides(t *testing.T) {
	const cols, rows = 40, 10
	body := func(offset int) []string {
		out := make([]string, rows)
		for i := range out {
			out[i] = "row " + itoa(offset+i)
		}
		return out
	}
	paintFrames(t, cols, rows, [][]string{body(0), body(1), body(2), body(3)})
}

// The same slide, arrived at by GROWING into it: a short frame, then one that
// fills the screen, then one that has to slide because it cannot grow further.
func TestFrameGrowsToFillTheScreenThenSlides(t *testing.T) {
	const cols, rows = 40, 10
	frame := func(n, offset int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "row " + itoa(offset+i)
		}
		return out
	}
	paintFrames(t, cols, rows, [][]string{
		frame(4, 0), frame(7, 0), frame(10, 0), frame(10, 1), frame(10, 2),
	})
}

// A line exactly as wide as the terminal leaves the cursor in the pending-wrap
// state on a real terminal. The renderer must not be relying on that column
// surviving.
func TestFullWidthLinesDoNotDesync(t *testing.T) {
	const cols, rows = 20, 6
	full := strings.Repeat("x", cols)
	paintFrames(t, cols, rows, [][]string{
		{full, "short", full, "a", "b", "c"},
		{full, "shorter", full, "a", "b", "d"},
	})
}

// A frame that shrinks must blank what it gave up, and one that grows again
// must fill it back in.
func TestFrameShrinksThenGrows(t *testing.T) {
	const cols, rows = 30, 12
	frame := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = "line " + itoa(i)
		}
		return out
	}
	sc := paintFrames(t, cols, rows, [][]string{frame(9), frame(4), frame(9)})
	for i := 9; i < rows; i++ {
		if sc.lines()[i] != "" {
			t.Errorf("row %d was left dirty: %q", i, sc.lines()[i])
		}
	}
}

// A full-screen frame that slides must SCROLL the terminal, not rewrite every
// row. Two things depend on it: the rows that leave the top have to end up in
// the terminal's scrollback where they can still be read, and a repaint of the
// whole screen per streamed token is what tears on a terminal that ignores
// synchronized output.
func TestSlidingFrameScrollsInsteadOfRepainting(t *testing.T) {
	const cols, rows = 40, 10
	body := func(offset int) []string {
		out := make([]string, rows)
		for i := range out {
			out[i] = "row " + itoa(offset+i)
		}
		return out
	}
	sc := newScreen(cols, rows)
	var b strings.Builder
	w := &inlineWriter{out: &b, full: true}

	w.Paint(nil, body(0), cols, rows-1, 0, false)
	sc.feed(b.String())

	b.Reset()
	w.Paint(nil, body(1), cols, rows-1, 0, false)
	frame := b.String()
	sc.feed(frame)

	// Only the newly exposed row is written. One row of content in the whole
	// frame, and it is the new one.
	if n := strings.Count(frame, "row "); n != 1 {
		t.Errorf("a slide wrote %d rows instead of scrolling and writing 1: %q", n, frame)
	}
	if !strings.Contains(frame, "row "+itoa(rows)+"\x1b[K") {
		t.Errorf("the newly exposed row was not written: %q", frame)
	}
	got := sc.lines()
	for i, want := range body(1) {
		if got[i] != want {
			t.Fatalf("row %d = %q, want %q\n--- screen ---\n%s", i, got[i], want, strings.Join(got, "\n"))
		}
	}
	// And the row that left the top is in scrollback, not gone.
	if len(sc.history) != 1 || sc.history[0] != "row 0" {
		t.Errorf("the scrolled-off row did not reach scrollback: %v", sc.history)
	}
}

// A region that does NOT fill the screen must never take the scroll path: a
// newline at its last row just moves down, so "scrolling" would write the new
// rows below the region and leave the old ones on screen.
func TestPartialRegionNeverScrolls(t *testing.T) {
	const cols, rows = 40, 12
	body := func(offset int) []string {
		out := make([]string, 6)
		for i := range out {
			out[i] = "row " + itoa(offset+i)
		}
		return out
	}
	paintFrames(t, cols, rows, [][]string{body(0), body(1), body(2)})
}
