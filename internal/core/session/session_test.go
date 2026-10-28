package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/db"
)

// withSessionHome gives a test its own HOME and its own database. Both,
// because the handle is process-global: a handle opened by an earlier test
// would otherwise still be the one this test writes to.
func withSessionHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	db.SetPath(filepath.Join(dir, ".loop", "agent.db"))
	t.Cleanup(func() {
		db.Close()
		db.SetPath("")
	})
}

func user(text string) provider.Message {
	return provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: text}}}
}

func assistant(text string) provider.Message {
	return provider.AssistantMessage{Content: []provider.AssistantPart{provider.TextPart{Text: text}}}
}

func toolCall(id, name string, input map[string]any) provider.Message {
	return provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ToolCallPart{ToolCallID: id, ToolName: name, Input: input},
	}}
}

func toolOK(id, name, out string) provider.Message {
	return provider.ToolMessage{Content: []provider.ToolPart{
		provider.ToolResultPart{ToolCallID: id, ToolName: name, Output: provider.ToolOutputText{Value: out}},
	}}
}

// A user prompt is stored as a plain string, assistant/tool content as AI SDK
// parts with providerOptions — exactly loop's shapes.
func TestContentIsLoopsShape(t *testing.T) {
	_, c, _ := EncodeContent(user("hello"))
	if string(c) != `"hello"` {
		t.Errorf("user prompt stored as %s, want a plain string", c)
	}
	_, c, _ = EncodeContent(provider.AssistantMessage{Content: []provider.AssistantPart{
		provider.ReasoningPart{Text: "hm", ProviderOptions: provider.ProviderOptions{"xai": {"itemId": "rs_1"}}},
		provider.ToolCallPart{ToolCallID: "c1", ToolName: "read", Input: map[string]any{"path": "a"}},
	}})
	want := `[{"type":"reasoning","text":"hm","providerOptions":{"xai":{"itemId":"rs_1"}}},` +
		`{"type":"tool-call","toolCallId":"c1","toolName":"read","input":{"path":"a"}}]`
	if string(c) != want {
		t.Errorf("assistant content:\n got %s\nwant %s", c, want)
	}
	_, c, _ = EncodeContent(provider.ToolMessage{Content: []provider.ToolPart{
		provider.ToolResultPart{ToolCallID: "c1", ToolName: "read", Output: provider.ToolOutputExecutionDenied{Reason: "no"}},
	}})
	if !strings.Contains(string(c), `"output":{"type":"execution-denied","reason":"no"}`) {
		t.Errorf("denied output not in v7 shape: %s", c)
	}
}

// Every key an entry carries survives a read/write cycle, including ones pi
// does not model.
func TestEntryRoundTripKeepsUnknownKeys(t *testing.T) {
	in := `{"type":"message","ts":5,"role":"assistant","content":[{"type":"text","text":"x"}],` +
		`"reasoningMs":[2],"timing":{"startedAt":1,"tools":[{"a":1}]},"team":{"kind":"brief"},` +
		`"usage":{"inputTokens":10,"inputTokenDetails":{"cacheReadTokens":4},"raw":{"z":1},"usd":0.5},` +
		`"model":"xai/grok","id":"abcd1234","parentId":"00000000"}`
	var e Entry
	if err := json.Unmarshal([]byte(in), &e); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(e)
	var a, b any
	_ = json.Unmarshal([]byte(in), &a)
	_ = json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round trip changed the entry:\n in %s\nout %s", in, out)
	}
}

func TestCreateAppendLoad(t *testing.T) {
	withSessionHome(t)
	s := New("xai/grok-4.7", "/repo")
	if s.Saved() {
		t.Fatal("a new session must not have a row before its first append")
	}
	if err := s.Add(user("do a thing"), toolCall("c1", "read", map[string]any{"path": "a.go"}),
		toolOK("c1", "read", "contents"), assistant("done")); err != nil {
		t.Fatal(err)
	}
	got, err := Load(s.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 4 || !reflect.DeepEqual(got.Messages, s.Messages) {
		t.Fatalf("messages changed across save/load:\n%v\n%v", got.Messages, s.Messages)
	}
	if got.Entries()[0].Type != TypeSessionInfo || got.Entries()[0].ID != s.ID {
		t.Errorf("first entry should be the session-info root with the session id")
	}
	if got.Meta.Title != "do a thing" || got.Meta.CWD != "/repo" {
		t.Errorf("meta: %+v", got.Meta)
	}
}

// Branching forks the tree: the old path stays, the new one is current, and
// the model is sent only the current path.
func TestBranchAndTree(t *testing.T) {
	withSessionHome(t)
	s := New("m/x", "/repo")
	_ = s.Add(user("first"))
	firstID := s.LeafID()
	_ = s.Add(assistant("answer one"), user("second"), assistant("answer two"))
	if err := s.Branch(firstID); err != nil {
		t.Fatal(err)
	}
	_ = s.Add(assistant("another answer"))
	if n := len(s.ModelMessages()); n != 2 {
		t.Errorf("after branching the model sees %d messages, want 2", n)
	}
	roots := s.GetTree()
	if len(roots) != 1 {
		t.Fatalf("want one root, got %d", len(roots))
	}
	var first *TreeNode
	var walk func(n *TreeNode)
	walk = func(n *TreeNode) {
		if n.Entry.ID == firstID {
			first = n
		}
		for _, c := range n.Children {
			walk(c)
		}
	}
	walk(roots[0])
	if first == nil || len(first.Children) != 2 {
		t.Fatalf("the branch point should have two children")
	}
	reloaded, _ := Load(s.ID)
	if reloaded.LeafID() != s.LeafID() {
		t.Errorf("leaf after reload is the last entry written, the new branch")
	}
}

func TestNameLabelForkArchive(t *testing.T) {
	withSessionHome(t)
	s := New("m/x", "/repo")
	_ = s.Add(user("hello"), assistant("hi"))
	target := s.LeafID()
	_ = s.SetName("My task")
	_ = s.SetLabel(target, "keep")
	if s.Name() != "My task" || s.GetLabel(target) != "keep" {
		t.Fatalf("name/label not applied")
	}
	list, _ := List()
	if len(list) != 1 || list[0].Name != "My task" || list[0].Title != "hello" {
		t.Fatalf("listing: %+v", list)
	}
	f, err := Fork(s)
	if err != nil {
		t.Fatal(err)
	}
	if f.Info.ParentSession != s.Path || len(f.Messages) != 2 || f.GetLabel(target) != "keep" {
		t.Errorf("fork lost its parent, messages or labels: %+v", f.Meta)
	}
	if ok, _ := SetArchived(s.ID, true); !ok {
		t.Fatal("archive")
	}
	active, _ := ListScope("/repo", Active)
	if len(active) != 1 || active[0].ID != f.ID {
		t.Errorf("archived session still active: %+v", active)
	}
}

// Compaction appends, never rewrites: the model gets the summary block plus
// what came after the cut; the session still holds everything.
func TestCompactionContext(t *testing.T) {
	withSessionHome(t)
	s := New("m/x", "/repo")
	_ = s.Add(user("one"), assistant("1"), user("two"), assistant("2"))
	_ = s.AppendAll(Entry{Type: TypeCompact, Summary: "talked about one and two", CutAt: 4})
	_ = s.Add(user("three"))
	msgs := s.ModelMessages()
	if len(msgs) != 2 {
		t.Fatalf("model sees %d messages, want summary + three", len(msgs))
	}
	first := flattenUser(msgs[0].(provider.UserMessage))
	if !strings.HasPrefix(first, compactionPrefix) || !strings.Contains(first, "talked about") {
		t.Errorf("compaction block wrong: %q", first)
	}
	if len(s.Messages) != 5 {
		t.Errorf("the session must keep every message, has %d", len(s.Messages))
	}
}

func TestInterruptedAndNativeAgent(t *testing.T) {
	withSessionHome(t)
	s := New("m/x", "/repo")
	_ = s.Add(user("go"))
	_ = s.AppendAll(Entry{Type: TypeMessage, Role: "assistant", Content: json.RawMessage(`""`), Interrupted: true})
	_ = s.AppendAll(Entry{Type: TypeMessage, Role: "assistant", Content: json.RawMessage(
		`[{"type":"tool-call","toolCallId":"t","toolName":"bash","input":{"command":"ls"},"providerExecuted":true,"providerOptions":{"nativeAgent":{"provider":"claude-code"}}},` +
			`{"type":"tool-result","toolCallId":"t","toolName":"bash","output":{"type":"text","value":"a.go"},"providerOptions":{"nativeAgent":{"provider":"claude-code"}}}]`)})
	msgs := s.ModelMessages()
	if len(msgs) != 3 {
		t.Fatalf("got %d messages", len(msgs))
	}
	if got := flattenAssistant(msgs[1].(provider.AssistantMessage)); got != interruptNote {
		t.Errorf("empty interrupted turn should become the note, got %q", got)
	}
	got := flattenAssistant(msgs[2].(provider.AssistantMessage))
	if got != "[Claude Code ran bash: {\"command\":\"ls\"}][bash result]\na.go" {
		t.Errorf("native agent parts not flattened to text: %q", got)
	}
}

func TestLedgerSums(t *testing.T) {
	withSessionHome(t)
	s := New("xai/grok", "/repo")
	_ = s.Add(user("hi"))
	in, out := int64(100), int64(20)
	id, err := AddLedgerRow(LedgerRow{Provider: "xai", Model: "xai/grok", USD: 0.01, Source: SourceTurn,
		CWD: "/repo", SessionPub: s.ID, Usage: Usage{InputTokens: &in, OutputTokens: &out}})
	if err != nil || id == 0 {
		t.Fatalf("add: %v", err)
	}
	sums, _ := SumLedgerForSession(s.ID)
	if sums.Rows != 1 || sums.InputTokens != 100 || sums.USD != 0.01 {
		t.Errorf("sums: %+v", sums)
	}
	st, _ := Stats("/repo")
	if st.Today != 0.01 || st.Cwd != 0.01 || st.ByProvider["xai"] != 0.01 {
		t.Errorf("stats: %+v", st)
	}
}

// Against a real ~/.loop database (a COPY): every session loads, every entry
// round-trips without losing a key, and every branch yields model messages.
//
//	LOOP_COMPAT_DB=/path/to/copy/agent.db go test ./internal/core/session -run RealLoop
func TestRealLoopDatabase(t *testing.T) {
	path := os.Getenv("LOOP_COMPAT_DB")
	if path == "" {
		t.Skip("set LOOP_COMPAT_DB to a copy of ~/.loop/agent.db")
	}
	db.SetPath(path)
	t.Cleanup(func() { db.Close(); db.SetPath("") })
	recs, err := ListRecords("", AllScopes, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	h, _ := db.Open()
	entries, mismatched := 0, 0
	for _, r := range recs {
		s, err := Load(r.Info.ID)
		if err != nil {
			t.Errorf("load %s: %v", r.Info.ID, err)
			continue
		}
		_ = s.ModelMessages()
		rows, _ := h.Query("SELECT payload FROM entries WHERE session_id = ? ORDER BY id", r.RowID)
		for rows.Next() {
			var payload string
			_ = rows.Scan(&payload)
			entries++
			var e Entry
			if json.Unmarshal([]byte(payload), &e) != nil {
				continue
			}
			out, _ := json.Marshal(e)
			var a, b any
			_ = json.Unmarshal([]byte(payload), &a)
			_ = json.Unmarshal(out, &b)
			if !reflect.DeepEqual(a, b) {
				mismatched++
				if mismatched <= 3 {
					t.Errorf("entry changed on round trip:\n in %.400s\nout %.400s", payload, out)
				}
			}
		}
		rows.Close()
	}
	t.Logf("%d sessions, %d entries, %d changed by a round trip", len(recs), entries, mismatched)
	if mismatched > 0 {
		t.Fail()
	}
}
