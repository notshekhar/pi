package session

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/db"
)

func toolCall(id, name string, input map[string]any) provider.Message {
	return provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ToolCallPart{ToolCallID: id, ToolName: name, Input: input},
	}}
}

func toolOK(id, name, out string) provider.Message {
	return provider.ToolMessage{Content: []provider.ToolPart{
		provider.ToolResultPart{ToolCallID: id, ToolName: name,
			Output: provider.ToolOutputText{Value: out}},
	}}
}

// --- codec -----------------------------------------------------------------

func TestCodecRoundTripsEveryPartKind(t *testing.T) {
	messages := []provider.Message{
		provider.SystemMessage{Content: "be terse"},
		user("read the file"),
		provider.AssistantMessage{Content: []provider.AssistantPart{
			provider.ReasoningPart{Text: "I should read it"},
			provider.TextPart{Text: "Reading now."},
			provider.ToolCallPart{
				ToolCallID: "call_1", ToolName: "read",
				Input: map[string]any{"path": "a.go", "offset": float64(10)},
			},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_1", ToolName: "read",
				Output: provider.ToolOutputText{Value: "package main"}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_2", ToolName: "bash",
				Output: provider.ToolOutputErrorText{Value: "exit 1"}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_3", ToolName: "x",
				Output: provider.ToolOutputJSON{Value: map[string]any{"ok": true}}},
		}},
		provider.ToolMessage{Content: []provider.ToolPart{
			provider.ToolResultPart{ToolCallID: "call_4", ToolName: "x",
				Output: provider.ToolOutputExecutionDenied{Reason: "user said no"}},
		}},
	}

	for _, want := range messages {
		wire, err := encodeMessage(want)
		if err != nil {
			t.Fatalf("encode %T: %v", want, err)
		}
		got, err := decodeMessage(wire)
		if err != nil {
			t.Fatalf("decode %T: %v", want, err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("round trip changed %T:\n got %#v\nwant %#v", want, got, want)
		}
	}
}

// Anthropic and OpenAI round-trip SIGNED reasoning through ProviderOptions.
// A resumed session that dropped the signature is rejected by the API, so
// this is the single most important thing the codec preserves.
func TestCodecPreservesProviderOptions(t *testing.T) {
	opts := provider.ProviderOptions{
		"anthropic": provider.JSONObject{"signature": "sig-abc-123"},
	}
	msg := provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ReasoningPart{Text: "thinking", ProviderOptions: opts},
	}}
	wire, err := encodeMessage(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeMessage(wire)
	if err != nil {
		t.Fatal(err)
	}
	part := got.(provider.AssistantMessage).Content[0].(provider.ReasoningPart)
	if part.ProviderOptions["anthropic"]["signature"] != "sig-abc-123" {
		t.Errorf("reasoning signature lost: %#v", part.ProviderOptions)
	}
}

// An unrepresentable part must fail loudly. Silently dropping one half of a
// tool-call/result pair writes a session the provider will reject.
func TestCodecRefusesUnknownPart(t *testing.T) {
	msg := provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.CustomPart{Kind: "vendor.thing"},
	}}
	if _, err := encodeMessage(msg); err == nil {
		t.Error("expected an error encoding an unsupported part")
	}
}

func TestCodecRefusesUnknownPartType(t *testing.T) {
	if _, err := decodePart(wirePart{Type: "from-the-future"}); err == nil {
		t.Error("expected an error decoding an unknown part type")
	}
}

// --- store -----------------------------------------------------------------

// withSessionHome gives a test its own HOME and its own database.
//
// Both, because the database handle is process-global: pointing HOME at a
// temp directory is no longer enough on its own, since a handle opened by an
// earlier test would still be the one this test writes to — which is how a
// test run ends up reading the developer's real session history.
func withSessionHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	db.SetPath(filepath.Join(dir, "agent.db"))
	t.Cleanup(func() {
		db.Close()
		db.SetPath("")
	})
}

func TestCreateLoadRoundTrip(t *testing.T) {
	withSessionHome(t)
	s, err := Create("kimi/k3", "/repo")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Add(user("do a thing"), toolCall("c1", "read", map[string]any{"path": "a.go"}),
		toolOK("c1", "read", "contents"), assistant("done")); err != nil {
		t.Fatal(err)
	}

	got, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != len(s.Messages) {
		t.Fatalf("loaded %d messages, wrote %d", len(got.Messages), len(s.Messages))
	}
	if !reflect.DeepEqual(got.Messages, s.Messages) {
		t.Errorf("history changed across a save/load cycle")
	}
	if got.Meta.Model != "kimi/k3" || got.Meta.CWD != "/repo" {
		t.Errorf("metadata lost: %+v", got.Meta)
	}
}

func TestTitleComesFromFirstPrompt(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("add a rail to the transcript\nand more detail here")); err != nil {
		t.Fatal(err)
	}
	if s.Meta.Title != "add a rail to the transcript" {
		t.Errorf("title = %q", s.Meta.Title)
	}
	// A later prompt must not rename the session.
	if err := s.Add(user("something else entirely")); err != nil {
		t.Fatal(err)
	}
	if s.Meta.Title != "add a rail to the transcript" {
		t.Errorf("title was overwritten: %q", s.Meta.Title)
	}
}

func TestTitleIsTruncated(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user(strings.Repeat("x", 200))); err != nil {
		t.Fatal(err)
	}
	if r := []rune(s.Meta.Title); len(r) > 61 {
		t.Errorf("title is %d runes: %q", len(r), s.Meta.Title)
	}
}

func TestListIsNewestFirst(t *testing.T) {
	withSessionHome(t)
	a, _ := Create("m", "/repo")
	if err := a.Add(user("first session")); err != nil {
		t.Fatal(err)
	}
	b, _ := Create("m", "/repo")
	if err := b.Add(user("second session")); err != nil {
		t.Fatal(err)
	}

	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2", len(list))
	}
	if list[0].ID != b.ID {
		t.Errorf("newest session is not first: %v", list[0].ID)
	}
	if list[0].Title != "second session" || list[0].Messages != 1 {
		t.Errorf("listing metadata wrong: %+v", list[0])
	}
}

// A payload that cannot be decoded is real corruption and must be reported:
// the alternative is resuming a conversation with a hole in it, which the
// provider rejects with an error about a tool call that has no result.
func TestLoadReportsAnUndecodablePayload(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("one"), assistant("two")); err != nil {
		t.Fatal(err)
	}
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Exec(
		`UPDATE entries SET payload = '{not json' WHERE seq = 0`); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(s.ID); err == nil {
		t.Error("expected an error for an undecodable message")
	}
}

func TestForkCopiesWithoutTouchingTheOriginal(t *testing.T) {
	withSessionHome(t)
	orig, _ := Create("m", "/repo")
	if err := orig.Add(user("shared history")); err != nil {
		t.Fatal(err)
	}

	forked, err := Fork(orig)
	if err != nil {
		t.Fatal(err)
	}
	if forked.ID == orig.ID {
		t.Fatal("fork reused the original's id")
	}
	if err := forked.Add(user("only in the fork")); err != nil {
		t.Fatal(err)
	}

	reloaded, err := Load(orig.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Messages) != 1 {
		t.Errorf("the fork wrote into the original: %d messages", len(reloaded.Messages))
	}
	if !strings.Contains(forked.Meta.Title, "fork") {
		t.Errorf("fork title = %q", forked.Meta.Title)
	}
}

func TestReplaceRewritesBody(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("one"), assistant("two"), user("three")); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(user("summary")); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 1 {
		t.Fatalf("expected 1 message after replace, got %d", len(got.Messages))
	}
	if text := got.Text(); strings.Contains(text, "three") {
		t.Errorf("replaced content survived on disk:\n%s", text)
	}
}

func TestResetClearsBodyAndTitle(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("gone")); err != nil {
		t.Fatal(err)
	}
	if err := s.Reset(); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 0 {
		t.Errorf("messages survived reset: %d", len(got.Messages))
	}
	if got.Meta.Title != "" {
		t.Errorf("title survived reset: %q", got.Meta.Title)
	}
}

func TestDeleteRemovesTheConversation(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("x")); err != nil {
		t.Fatal(err)
	}
	if err := Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	list, _ := List()
	if len(list) != 0 {
		t.Errorf("session still listed after delete: %+v", list)
	}
	// The messages must go with it. They only do because foreign keys are ON
	// — SQLite ignores ON DELETE CASCADE otherwise, and the conversation
	// would sit there forever attached to a session that no longer exists.
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	var entries int
	if err := h.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 0 {
		t.Errorf("%d messages orphaned by the delete", entries)
	}
}

// Storage is append-only: a turn must not rewrite what came before it. The
// cost of a turn has to be the size of the turn, not the size of the
// conversation, or a long session gets slower with every exchange.
func TestAddAppendsRatherThanRewrites(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("one")); err != nil {
		t.Fatal(err)
	}
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	var firstRow int64
	if err := h.QueryRow(`SELECT id FROM entries ORDER BY seq LIMIT 1`).Scan(&firstRow); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(user("two")); err != nil {
		t.Fatal(err)
	}
	var afterRow int64
	if err := h.QueryRow(`SELECT id FROM entries ORDER BY seq LIMIT 1`).Scan(&afterRow); err != nil {
		t.Fatal(err)
	}
	if afterRow != firstRow {
		t.Errorf("the first message was rewritten (row %d became %d)", firstRow, afterRow)
	}
	var count int
	if err := h.QueryRow(`SELECT COUNT(*) FROM entries`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("stored %d messages, want 2", count)
	}
}

// Replace REWRITES rather than appends — the compaction path. A compacted
// session whose storage still held the old messages would grow forever and
// contradict what the model is being sent.
func TestReplaceRewritesStoredMessages(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("m", "/repo")
	if err := s.Add(user("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Replace(user("y")); err != nil {
		t.Fatal(err)
	}
	stored, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stored.Messages) != 1 {
		t.Fatalf("stored %d messages after replace, want 1", len(stored.Messages))
	}
	if !strings.Contains(stored.Text(), "y") || strings.Contains(stored.Text(), "x") {
		t.Errorf("replace did not rewrite the conversation:\n%s", stored.Text())
	}
}

// Ids are claimed exclusively, so sessions created back-to-back — which is
// exactly what a fork does — never share a file.
func TestCreateNeverCollides(t *testing.T) {
	withSessionHome(t)
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		s, err := Create("m", "/repo")
		if err != nil {
			t.Fatal(err)
		}
		if seen[s.ID] {
			t.Fatalf("duplicate session id %q at iteration %d", s.ID, i)
		}
		seen[s.ID] = true
	}
	list, _ := List()
	if len(list) != 50 {
		t.Errorf("listed %d sessions, want 50", len(list))
	}
}

// The ledger is per TURN, which is the thing session totals could not be.
// Spend used to be attributed to the day a session was last touched, so a
// conversation resumed a week later moved its whole history into "today".
func TestUsageLandsInTheLedgerPerTurn(t *testing.T) {
	withSessionHome(t)
	s, _ := Create("kimi/k3", "/repo")
	if err := s.Add(user("x")); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(100, 20, 0.5); err != nil {
		t.Fatal(err)
	}
	if err := s.AddUsage(50, 10, 0.25); err != nil {
		t.Fatal(err)
	}

	spend, err := Spend(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(spend) != 1 {
		t.Fatalf("ledger has %d days, want 1", len(spend))
	}
	if spend[0].CostUSD != 0.75 || spend[0].InputTokens != 150 || spend[0].OutputTokens != 30 {
		t.Errorf("day totals wrong: %+v", spend[0])
	}

	// And the session row carries the same total, so a listing never has to
	// touch the ledger.
	list, err := List()
	if err != nil {
		t.Fatal(err)
	}
	if list[0].CostUSD != 0.75 || list[0].InputTokens != 150 {
		t.Errorf("session totals wrong: %+v", list[0])
	}

	b, err := SpendBuckets("/repo")
	if err != nil {
		t.Fatal(err)
	}
	if b.Today != 0.75 || b.Directory != 0.75 || b.ByProvider["kimi"] != 0.75 {
		t.Errorf("buckets wrong: %+v", b)
	}
	if b.Directory == 0 {
		t.Error("the directory bucket did not match the session's cwd")
	}
}
