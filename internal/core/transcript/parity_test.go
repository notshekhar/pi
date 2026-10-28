package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"

	"github.com/notshekhar/pi/internal/core/session"
)

// TestParityWithLoop compares FromEntries against loop's own fromEntries for
// every session in a loop home. Run with:
//
//	LOOP_TRANSCRIPTS=<dump.jsonl> HOME=<loop home> go test ./internal/core/transcript -run Parity
//
// where the dump comes from loop (scripts in the parity notes).
func TestParityWithLoop(t *testing.T) {
	path := os.Getenv("LOOP_TRANSCRIPTS")
	if path == "" {
		t.Skip("LOOP_TRANSCRIPTS not set")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 512<<20)
	var checked, failed int
	for sc.Scan() {
		var row struct {
			ID         string          `json:"id"`
			Transcript json.RawMessage `json:"transcript"`
			Error      string          `json:"error"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil || row.Error != "" {
			continue
		}
		s, err := session.Load(row.ID)
		if err != nil {
			t.Errorf("%s: load: %v", row.ID, err)
			failed++
			continue
		}
		branch := s.CurrentBranch()
		got := FromEntries(branch, false, LatestTodos(branch))
		gotJSON, _ := json.Marshal(got)
		var a, b any
		_ = json.Unmarshal(gotJSON, &a)
		_ = json.Unmarshal(row.Transcript, &b)
		checked++
		if !reflect.DeepEqual(a, b) {
			failed++
			if failed <= 5 {
				t.Errorf("%s differs: %s", row.ID, firstDiff(a, b, ""))
			}
		}
	}
	t.Logf("checked %d sessions, %d differ", checked, failed)
}

func firstDiff(a, b any, path string) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: type %T vs %T", path, a, b)
		}
		for k := range av {
			if _, ok := bv[k]; !ok {
				return fmt.Sprintf("%s.%s: pi has %v, loop lacks it", path, k, short(av[k]))
			}
		}
		for k := range bv {
			if _, ok := av[k]; !ok {
				return fmt.Sprintf("%s.%s: loop has %v, pi lacks it", path, k, short(bv[k]))
			}
			if !reflect.DeepEqual(av[k], bv[k]) {
				return firstDiff(av[k], bv[k], path+"."+k)
			}
		}
	case []any:
		bv, ok := b.([]any)
		if !ok {
			return fmt.Sprintf("%s: type %T vs %T", path, a, b)
		}
		if len(av) != len(bv) {
			return fmt.Sprintf("%s: len %d vs %d", path, len(av), len(bv))
		}
		for i := range av {
			if !reflect.DeepEqual(av[i], bv[i]) {
				return firstDiff(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i))
			}
		}
	}
	return fmt.Sprintf("%s: pi %v vs loop %v", path, short(a), short(b))
}

func short(v any) string {
	s := fmt.Sprint(v)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
