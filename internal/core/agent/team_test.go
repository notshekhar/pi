package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/teams"
	"github.com/notshekhar/pi/internal/core/tools"
)

// teamGateway sends a member's requests (its system prompt carries the
// member rules) to one script and the lead's to another: the two run at the
// same time, so one ordered script could not say whose reply is whose.
type teamGateway struct{ lead, member *gateway }

func (g teamGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	r.Body = io.NopCloser(bytes.NewReader(raw))
	if strings.Contains(string(raw), "You are in a thread team") {
		g.member.ServeHTTP(w, r)
		return
	}
	g.lead.ServeHTTP(w, r)
}

// memberRuntime runs a member's turn the way a host does: load its
// session, run the turn with the team's meta, and record how it went.
type memberRuntime struct {
	cfg     config.Config
	mu      sync.Mutex
	running map[string]bool
	done    sync.WaitGroup
	errs    []error
}

func (m *memberRuntime) Deliver(sessionID, input string, meta teams.TurnMeta) error {
	sess, err := session.Load(sessionID)
	if err != nil {
		return err
	}
	m.mu.Lock()
	m.running[sessionID] = true
	m.mu.Unlock()
	m.done.Add(1)
	go func() {
		defer m.done.Done()
		r := &Run{Config: m.cfg, Session: sess, Tools: &tools.Context{CWD: m.cfg.CWD, Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}}
		_, err := r.RunTurn(context.Background(), TurnOptions{Input: input, Team: &meta})
		m.mu.Lock()
		delete(m.running, sessionID)
		if err != nil {
			m.errs = append(m.errs, err)
		}
		m.mu.Unlock()
	}()
	return nil
}

func (m *memberRuntime) IsRunning(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.running[id]
}

func (m *memberRuntime) Cancel(string) {}

// A lead spawns a thread, waits, and gets its report; the thread runs as
// its own session, saved with loop's team key and billed as "team".
func TestLeadSpawnsWaitsAndGetsTheReport(t *testing.T) {
	lead := &gateway{script: []string{
		toolCallStep("spawn_threads", "toolu_spawn", map[string]any{"threads": []map[string]string{{"title": "CSV export", "brief": "add the endpoint"}}}),
		toolCallStep("wait_for_team", "toolu_wait", map[string]any{}),
		textStep,
	}}
	member := &gateway{script: []string{
		toolCallStep("report", "toolu_report", map[string]any{"summary": "endpoint added", "files": []string{"api.go"}}),
	}}
	cfg := withGateway(t, "anthropic", teamGateway{lead: lead, member: member})
	cfg.CWD = t.TempDir()
	for k, v := range map[string]any{"autoCompactThreshold": 0, "idleCompact": false, "threadTeams": true} {
		if err := config.SetSetting(k, v); err != nil {
			t.Fatal(err)
		}
	}
	rt := &memberRuntime{cfg: cfg, running: map[string]bool{}}
	if !teams.ClaimRuntime(rt, rt) {
		t.Fatal("runtime already claimed")
	}
	t.Cleanup(func() { teams.ReleaseRuntime(rt) })

	r := &Run{Config: cfg, Session: session.New(cfg.FullID(), cfg.CWD),
		Tools: &tools.Context{CWD: cfg.CWD, Registry: tools.NewRegistry(), Todos: &tools.TodoList{}}}
	if _, err := r.RunTurn(context.Background(), TurnOptions{Input: "split the work"}); err != nil {
		t.Fatal(err)
	}
	rt.done.Wait()
	if len(rt.errs) > 0 {
		t.Fatalf("member turn failed: %v", rt.errs)
	}

	leadReqs := lead.requests()
	if len(leadReqs) != 3 {
		t.Fatalf("lead requests = %d, want 3", len(leadReqs))
	}
	if !strings.Contains(systemText(leadReqs[0]), "# Thread teams") {
		t.Error("the lead was not told about thread teams")
	}
	afterWait := fmt.Sprint(leadReqs[2]["messages"])
	for _, want := range []string{"Every thread has finished.", "team-report", "endpoint added", "Files: api.go"} {
		if !strings.Contains(afterWait, want) {
			t.Errorf("the lead's wait result lacks %q:\n%s", want, afterWait)
		}
	}

	snap, ok := teams.SnapshotForSession(r.Session.ID)
	if !ok || len(snap.Members) != 1 {
		t.Fatalf("snapshot = %+v", snap)
	}
	m := snap.Members[0]
	if m.Title != "CSV export" || m.State != teams.StateDone {
		t.Errorf("member = %+v, want CSV export done", m)
	}
	h, err := db.Open()
	if err != nil {
		t.Fatal(err)
	}
	var source string
	if err := h.QueryRow("SELECT source FROM cost_ledger WHERE session_pub = ?", m.ID).Scan(&source); err != nil || source != "team" {
		t.Errorf("member billed as %q (%v), want team", source, err)
	}
	memberSess, err := session.Load(m.ID)
	if err != nil {
		t.Fatal(err)
	}
	opening := memberSess.CurrentBranch()
	var sawTeamKey bool
	for _, e := range opening {
		if e.Type == session.TypeMessage && e.Role == "user" && strings.Contains(string(e.Extra["team"]), `"kind":"spawn"`) {
			sawTeamKey = true
		}
	}
	if !sawTeamKey {
		t.Error("the member's opening entry lacks loop's team key")
	}
	if member.requests()[0]["tools"] == nil || strings.Contains(fmt.Sprint(requestTools(member.requests()[0])), "spawn_threads") {
		t.Errorf("member tools = %v; a member must not spawn", requestTools(member.requests()[0]))
	}
}

// Mail taken between steps goes in once, at the point it arrived, and stays
// there on every later step — never re-sent, never moved to the end.
func TestTeamMailStaysWhereItWentIn(t *testing.T) {
	r := testRun(t, map[string]any{"threadTeams": true})
	lead := r.Session.ID
	if err := r.Session.SetName("lead"); err != nil {
		t.Fatal(err)
	}
	teamID, err := teams.CreateTeam(lead)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := teams.PostMessages(teamID, lead, []string{lead}, teams.KindUpdate, "heads up"); err != nil {
		t.Fatal(err)
	}
	var events []string
	step := r.teamMail([]string{teams.SendMessageTool}, func(e string, _ any) { events = append(events, e) })
	if step == nil {
		t.Fatal("a lead in a team gets no mail step")
	}
	prompt, answer, result := ai.UserText("go"), ai.UserText("a1"), ai.UserText("t1")
	first, _ := step(context.Background(), ai.StepContext{Messages: []provider.Message{prompt}})
	if len(first.Messages) != 2 || !strings.Contains(messageText(first.Messages[1]), "heads up") {
		t.Fatalf("first step = %v", first.Messages)
	}
	later, _ := step(context.Background(), ai.StepContext{Messages: []provider.Message{prompt, answer, result}})
	if len(later.Messages) != 4 || !strings.Contains(messageText(later.Messages[1]), "heads up") || !strings.Contains(messageText(later.Messages[3]), "t1") {
		t.Fatalf("later step = %v, want the mail kept at index 1", later.Messages)
	}
	if teams.InboxCount(lead) != 0 || len(events) != 1 || events[0] != "team-message" {
		t.Errorf("inbox %d, events %v", teams.InboxCount(lead), events)
	}
}
