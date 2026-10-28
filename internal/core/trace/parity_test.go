package trace

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"testing"

	"github.com/notshekhar/pi/internal/core/session"
)

// TestParityWithLoop compares FromSession and Headline against loop's
// sessionToTrace for every session in a dump. Run with:
//
//	LOOP_TRACES=<dump.jsonl> HOME=<loop home> go test ./internal/core/trace -run Parity
//
// The dump is loop's sessionToTrace per session, generatedAt zeroed.
func TestParityWithLoop(t *testing.T) {
	path := os.Getenv("LOOP_TRACES")
	if path == "" {
		t.Skip("LOOP_TRACES not set")
	}
	if Pricer == nil {
		t.Skip("needs the catalog pricer (set by the agent package test helper)")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<20), 512<<20)
	checked, failed := 0, 0
	for sc.Scan() {
		var row struct {
			ID       string          `json:"id"`
			Trace    json.RawMessage `json:"trace"`
			Headline string          `json:"headline"`
		}
		if json.Unmarshal(sc.Bytes(), &row) != nil || row.Trace == nil {
			continue
		}
		s, err := session.Load(row.ID)
		if err != nil {
			t.Errorf("%s: load: %v", row.ID, err)
			continue
		}
		m := FromSession(s)
		m.GeneratedAt = 0
		raw, _ := json.Marshal(m)
		var a, b any
		_ = json.Unmarshal(raw, &a)
		_ = json.Unmarshal(row.Trace, &b)
		checked++
		if !reflect.DeepEqual(a, b) || Headline(m) != row.Headline {
			failed++
			if failed <= 6 {
				d := diff(a, b, "")
				if d == "" {
					d = fmt.Sprintf("headline %q vs %q", Headline(m), row.Headline)
				}
				t.Errorf("%s: %s", row.ID, d)
			}
		}
	}
	t.Logf("checked %d sessions, %d differ", checked, failed)
}

func diff(a, b any, path string) string {
	switch av := a.(type) {
	case map[string]any:
		bv, ok := b.(map[string]any)
		if !ok {
			return fmt.Sprintf("%s: %T vs %T", path, a, b)
		}
		keys := map[string]bool{}
		for k := range av {
			keys[k] = true
		}
		for k := range bv {
			keys[k] = true
		}
		var ks []string
		for k := range keys {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		for _, k := range ks {
			x, okx := av[k]
			y, oky := bv[k]
			if !okx || !oky {
				return fmt.Sprintf("%s.%s: pi %v / loop %v", path, k, clip(x), clip(y))
			}
			if d := diff(x, y, path+"."+k); d != "" {
				return d
			}
		}
		return ""
	case []any:
		bv, ok := b.([]any)
		if !ok || len(av) != len(bv) {
			return fmt.Sprintf("%s: len %d vs %v", path, len(av), clip(b))
		}
		for i := range av {
			if d := diff(av[i], bv[i], fmt.Sprintf("%s[%d]", path, i)); d != "" {
				return d
			}
		}
		return ""
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Sprintf("%s: pi %v / loop %v", path, clip(a), clip(b))
	}
	return ""
}

func clip(v any) string {
	s := fmt.Sprintf("%#v", v)
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}
