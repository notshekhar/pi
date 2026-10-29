package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/session"
)

// When every summary attempt fails, a compaction truncates instead (loop's
// compact-truncate.test.ts): the span a summary would have replaced is
// dropped and a mechanical record of it takes its place.

// failingCompaction fills a session on the fake gateway, then compacts it
// with every summary request answered by refusal.
func failingCompaction(t *testing.T, ctx context.Context, refusal string) (*Run, CompactResult, error) {
	t.Helper()
	r, g, _ := newTurn(t)
	for i := 0; i < 3; i++ {
		input := "request " + string(rune('A'+i)) + " " + strings.Repeat("history ", 400)
		if _, err := r.RunTurn(context.Background(), TurnOptions{Input: input}); err != nil {
			t.Fatal(err)
		}
	}
	g.mu.Lock()
	for len(g.script) < len(g.bodies) {
		g.script = append(g.script, textStep)
	}
	for i := 0; i < 8; i++ {
		g.script = append(g.script, refusal)
	}
	g.mu.Unlock()
	res, err := Compact(ctx, r, CompactOptions{Manual: true})
	return r, res, err
}

const badKey = `HTTP 401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`

func TestFailedSummaryTruncatesInstead(t *testing.T) {
	r, res, err := failingCompaction(t, context.Background(), badKey)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Truncated, "invalid x-api-key") {
		t.Errorf("Truncated = %q", res.Truncated)
	}
	if res.CutAt == 0 || res.TokensAfter >= res.TokensBefore {
		t.Errorf("nothing was cut: %+v", res)
	}
	if !strings.HasPrefix(res.Summary, TruncationPreamble) {
		t.Errorf("summary is not a truncation record:\n%s", res.Summary)
	}
	// The user's own words survive the cut.
	if !strings.Contains(res.Summary, "request A") {
		t.Errorf("record lost the user's first request:\n%s", res.Summary)
	}
	c, ok := r.Session.LatestCompact()
	if !ok {
		t.Fatal("no compaction saved")
	}
	var why string
	_ = json.Unmarshal(c.Extra["truncated"], &why)
	if !strings.Contains(why, "invalid x-api-key") {
		t.Errorf("entry truncated = %q", why)
	}
}

func TestCancelledCompactionIsNotTruncated(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r, _, err := failingCompaction(t, ctx, badKey)
	if err == nil {
		t.Fatal("a cancelled compaction succeeded")
	}
	if _, ok := r.Session.LatestCompact(); ok {
		t.Error("a cancelled compaction saved an entry")
	}
}

func TestIdleSummaryFailureIsReportedNotTruncated(t *testing.T) {
	r, g, _ := newTurn(t)
	for i := 0; i < 3; i++ {
		if _, err := r.RunTurn(context.Background(), TurnOptions{Input: strings.Repeat("history ", 400)}); err != nil {
			t.Fatal(err)
		}
	}
	_ = g
	_, err := Compact(context.Background(), r, CompactOptions{Trigger: "idle",
		Summarize: func(context.Context) (string, *session.Usage, provider.FinishReasonKind, error) {
			return "", nil, "", errString("idle summary failed")
		}})
	if err == nil || !strings.Contains(err.Error(), "idle summary failed") {
		t.Fatalf("err = %v", err)
	}
	if _, ok := r.Session.LatestCompact(); ok {
		t.Error("a failed idle compaction saved an entry")
	}
}

func TestRolloverHandoffKeepsInputsNotProse(t *testing.T) {
	msg := func(role, content string) session.Entry {
		return session.Entry{Type: session.TypeMessage, Role: role, Content: json.RawMessage(content)}
	}
	branch := []session.Entry{
		msg("user", `"fix the parser"`),
		msg("assistant", `[{"type":"text","text":"PROSE"},{"type":"tool-call","toolCallId":"1","toolName":"edit","input":{"path":"src/p.ts"}},{"type":"tool-call","toolCallId":"2","toolName":"bash","input":{"command":"go test ./...\nmore"}}]`),
		msg("tool", `[{"type":"tool-result","toolCallId":"1","toolName":"edit","output":{"type":"text","value":"ok"}}]`),
		msg("assistant", `"done"`),
	}
	out := BuildRolloverHandoff(branch, HandoffOptions{Limit: MaxHandoffChars})
	for _, want := range []string{"1. fix the parser", "- src/p.ts", "- go test ./..."} {
		if !strings.Contains(out, want) {
			t.Errorf("record missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "PROSE") || strings.Contains(out, "more") {
		t.Errorf("record kept assistant prose or a command's later lines:\n%s", out)
	}
}
