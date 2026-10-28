package trace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/core/session"
)

// TestHTMLParity: LOOP_TRACE_HTML=<dir of <id>.loop.html> HOME=<loop home>.
func TestHTMLParity(t *testing.T) {
	dir := os.Getenv("LOOP_TRACE_HTML")
	if dir == "" {
		t.Skip("LOOP_TRACE_HTML not set")
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.loop.html"))
	for _, f := range files {
		id := strings.TrimSuffix(filepath.Base(f), ".loop.html")
		s, err := session.Load(id)
		if err != nil {
			t.Fatal(err)
		}
		m := FromSession(s)
		m.GeneratedAt = 0
		got := HTML(m)
		want, _ := os.ReadFile(f)
		if got != string(want) {
			i := 0
			for i < len(got) && i < len(want) && got[i] == want[i] {
				i++
			}
			lo := max(0, i-80)
			t.Errorf("%s differs at %d:\npi:   %q\nloop: %q", id, i, got[lo:min(len(got), i+80)], string(want[lo:min(len(want), i+80)]))
		}
	}
	t.Logf("compared %d pages", len(files))
}
