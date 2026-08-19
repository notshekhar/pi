package tui

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Replaying a RECORDED session through the screen model.
//
// The synthetic frames above are shapes someone thought of. This is the shape
// the app actually produced — captured from a real turn in a real pty with
// PI_FRAME_LOG — and it is here because the gap that motivated all of this
// did not reproduce from any shape anyone thought of.
func TestReplayRecordedSession(t *testing.T) {
	f, err := os.Open("testdata/frames.txt")
	if err != nil {
		t.Skip("no recording")
	}
	defer f.Close()

	type rec struct{ commit, view []string }
	var frames []rec
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for scan.Scan() {
		line := scan.Text()
		switch {
		case line == "FRAME":
			frames = append(frames, rec{})
		case strings.HasPrefix(line, "C "), strings.HasPrefix(line, "V "):
			s, err := strconv.Unquote(line[2:])
			if err != nil {
				t.Fatalf("bad recording line: %q", line)
			}
			cur := &frames[len(frames)-1]
			if line[0] == 'C' {
				cur.commit = append(cur.commit, s)
			} else {
				cur.view = append(cur.view, s)
			}
		}
	}
	cols, rows := 100, 30
	sc := newScreen(cols, rows)
	var b strings.Builder
	w := &inlineWriter{out: &b}
	top := 0
	for n, fr := range frames {
		if len(fr.view) == 0 {
			continue
		}
		b.Reset()
		w.Paint(fr.commit, fr.view, cols, len(fr.view)-1, 0, false)
		sc.feed(b.String())
		top -= len(fr.commit)
		if over := top + len(fr.view) - 1 - (rows - 1); over > 0 {
			top -= over
		}
		if top < 0 {
			top = 0
		}
		got := sc.lines()
		for i, want := range fr.view {
			want = strings.TrimRight(want, " ")
			if top+i >= rows {
				break
			}
			if got[top+i] != want {
				t.Fatalf("frame %d (commit=%d view=%d), screen row %d:\n got %q\nwant %q\n--- screen ---\n%s",
					n, len(fr.commit), len(fr.view), top+i, got[top+i], want, strings.Join(got, "\n"))
			}
		}
	}
}
