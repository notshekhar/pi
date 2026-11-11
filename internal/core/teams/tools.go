package teams

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

// The thread-team tools (loop's teams/tools.ts): spawn_threads (the lead's),
// send_message, team_board, wait_for_team, and report (a member's). A member
// gets every tool but spawn_threads, so a team is one level deep.

// ToolContext is the turn the tools run in.
type ToolContext struct {
	SessionID string
	ModelID   string
	CWD       string
	// Thinking is the lead's reasoning level — its threads run at the same.
	Thinking string
	// Planning reports plan mode on the calling session.
	Planning func() bool
}

func (c ToolContext) planning() bool { return c.Planning != nil && c.Planning() }

// planModeRefusal: plan mode gates the session it is on, and a thread is
// another session that would write freely. Refused when called, like edit
// and write: plan mode can flip mid-turn.
const planModeRefusal = "REJECTED: plan mode is active, and threads are not bound by it. Keep investigating yourself; start or message threads once the plan is approved."

const noTeamYet = "REJECTED: there is no team yet — start one with " + SpawnThreadsTool + "."

const (
	maxThreadTitleRunes = 48
	defaultWaitSec      = 600
	minWaitSec          = 5
	maxWaitSec          = 3600
	// waitTick is a slow safety net for a change nobody announced.
	waitTick = 5 * time.Second
	// spawnGap keeps ids made in a row apart: ids from the same millisecond
	// differ only in their random tail, and a model told to address threads
	// by id confuses ids that look alike.
	spawnGap = 2 * time.Millisecond
)

// Tools is what a session gets this turn: everything for a lead, all but
// spawn_threads for a member. With no runtime nothing can run a thread, so a
// session in no team gets none, and one already in a team keeps what still
// works — its mail and its board. A lead gets the whole set from the start:
// the turn that spawns is the one most likely to post the contract and wait.
func Tools(c ToolContext) []ai.Tool {
	team, inTeam := TeamOf(c.SessionID)
	canRun := HasRuntime()
	if !inTeam && !canRun {
		return nil
	}
	member := inTeam && team.Role == RoleMember
	var out []ai.Tool
	if !member && canRun {
		out = append(out, spawnThreadsTool(c))
	}
	out = append(out, sendMessageTool(c), boardTool(c), waitTool(c))
	if member {
		out = append(out, reportTool(c))
	}
	return out
}

func rejected(text string) (ai.ToolResult, error) { return ai.ToolText(text), nil }

func rosterText(roster []Member, selfID string) string {
	lines := make([]string, len(roster))
	now := time.Now()
	for i, m := range roster {
		who := string(m.Effective(now))
		if m.Role == RoleLead {
			who = "lead"
		}
		you := ""
		if m.SessionID == selfID {
			you = " (you)"
		}
		lines[i] = fmt.Sprintf("- \"%s\" — %s — id %s%s", m.Title, who, m.SessionID, you)
	}
	return strings.Join(lines, "\n")
}

// ─── spawn_threads ──────────────────────────────────────────────────────

type threadRequest struct {
	Title string `json:"title"`
	Brief string `json:"brief"`
	Model string `json:"model,omitempty"`
	Agent string `json:"agent,omitempty"`
}

type spawnArgs struct {
	Threads []threadRequest `json:"threads"`
}

func spawnThreadsTool(c ToolContext) ai.Tool {
	max := MaxThreads()
	def := tools.LoopDef(SpawnThreadsTool)
	description := strings.Replace(def.Description, fmt.Sprintf("Up to %d threads", DefaultMaxThreads), fmt.Sprintf("Up to %d threads", max), 1)
	return ai.NewToolWithSchema(SpawnThreadsTool, description, def.Schema, func(_ context.Context, a spawnArgs) (ai.ToolResult, error) {
		unlock := lockLead(c.SessionID)
		defer unlock()
		return ai.ToolText(spawnThreads(a.Threads, c, max)), nil
	})
}

// leadLocks: one spawn at a time per lead. Two spawn_threads calls in one
// step run side by side, and both would count the same working threads
// against the cap.
var leadLocks sync.Map

func lockLead(leadID string) func() {
	mu, _ := leadLocks.LoadOrStore(leadID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

func spawnThreads(threads []threadRequest, c ToolContext, max int) string {
	self := c.SessionID
	if c.planning() {
		return planModeRefusal
	}
	if !HasRuntime() {
		return "REJECTED: thread teams are not available in this process."
	}
	existing, inTeam := TeamOf(self)
	if inTeam && existing.Role == RoleMember {
		return "REJECTED: you are a thread in a team; only the lead starts threads. Ask the lead with send_message if the work needs another thread."
	}
	working := 0
	if inTeam {
		working = workingThreads(existing.TeamID)
	}
	if working+len(threads) > max {
		return fmt.Sprintf("REJECTED: at most %d threads can work at once; %d are still working and you asked for %d more. Start fewer, wait for some to finish, or give the work to a finished thread with %s.",
			max, working, len(threads), SendMessageTool)
	}
	plans, refusal := planThreads(threads, c.ModelID)
	if refusal != "" {
		return refusal
	}
	if inTeam && IsStopped(existing.TeamID) && IsOverBudget(existing.TeamID) {
		return "REJECTED: the team was stopped because it reached its budget (threadTeamsBudgetUsd). Finish the job yourself, or ask the user to raise the budget."
	}
	teamID, err := CreateTeam(self)
	if err != nil {
		return "REJECTED: " + err.Error()
	}
	created, err := createThreads(teamID, !inTeam, plans, c)
	if err != nil {
		return "REJECTED: " + err.Error()
	}
	if inTeam && IsStopped(teamID) {
		_ = MarkStopped(teamID, false)
	}
	Emit(Change{TeamID: teamID, Kind: ChangeMembers})
	earlier := announceNewcomers(teamID, self, created)
	failed := startThreads(teamID, self, created)
	for _, m := range earlier {
		if !m.Effective(time.Now()).Finished() {
			WakeInbox(m.SessionID)
		}
	}
	return spawnResult(teamID, created, earlier, failed)
}

// workingThreads is threads working right now. Finished ones stay in the
// team for good and never count against the cap.
func workingThreads(teamID string) int {
	roster, _ := ListTeam(teamID)
	n := 0
	for _, m := range roster {
		if m.Role == RoleMember && !m.Effective(time.Now()).Finished() {
			n++
		}
	}
	return n
}

type threadPlan struct {
	title, brief, model, agent string
}

// planThreads checks every thread before any is created — a bad model or
// agent on the third must not leave the first two running.
func planThreads(threads []threadRequest, defaultModel string) ([]threadPlan, string) {
	var plans []threadPlan
	for _, t := range threads {
		if strings.TrimSpace(t.Title) == "" {
			return nil, "REJECTED: every thread needs a title."
		}
		if strings.TrimSpace(t.Brief) == "" {
			return nil, fmt.Sprintf("REJECTED: \"%s\" has no brief. Resend it with what the thread should do.", t.Title)
		}
		model := strings.TrimSpace(t.Model)
		if model == "" {
			model = defaultModel
		}
		if prov, name, ok := strings.Cut(model, "/"); !ok || prov == "" || name == "" {
			return nil, fmt.Sprintf("REJECTED: \"%s\" is not a provider/model id.", model)
		}
		agent := strings.TrimSpace(t.Agent)
		if agent != "" && !agents.Exists(agent) {
			return nil, fmt.Sprintf("REJECTED: there is no agent named \"%s\".", agent)
		}
		// A restricted agent gets no team tools, so it could never report
		// back and its lead would wait on it for good.
		if agent != "" && len(agents.Tools(agent)) > 0 {
			return nil, fmt.Sprintf("REJECTED: the \"%s\" agent has a restricted tool set, so it cannot %s to you. Run that thread as an unrestricted agent.", agent, ReportTool)
		}
		plans = append(plans, threadPlan{title: clipTitle(t.Title), brief: t.Brief, model: model, agent: agent})
	}
	return plans, ""
}

func clipTitle(title string) string {
	one := strings.Join(strings.Fields(title), " ")
	if r := []rune(one); len(r) > maxThreadTitleRunes {
		return string(r[:maxThreadTitleRunes-1]) + "…"
	}
	return one
}

type createdThread struct {
	id, title, brief, agent string
}

// createThreads makes every planned thread in the team, or none: a failure
// partway deletes the ones already made (and the team, if this call made
// it).
func createThreads(teamID string, teamIsNew bool, plans []threadPlan, c ToolContext) ([]createdThread, error) {
	var created []createdThread
	undo := func() {
		for _, t := range created {
			_, _ = session.Delete(t.id)
		}
		if teamIsNew {
			_ = RemoveTeam(teamID)
		}
	}
	for i, p := range plans {
		if i > 0 {
			time.Sleep(spawnGap)
		}
		s := session.New(p.model, c.CWD)
		// Named by the lead, so the member never spends a model call titling
		// itself.
		if err := s.SetName(p.title); err != nil {
			undo()
			return nil, err
		}
		created = append(created, createdThread{id: s.ID, title: p.title, brief: p.brief, agent: p.agent})
		if err := AddMember(teamID, s.ID, StateStarting); err != nil {
			undo()
			return nil, err
		}
		// Every later turn runs as this one does, whichever host picks it up.
		if err := SetRunConfig(s.ID, RunConfig{Agent: p.agent, Thinking: c.Thinking}); err != nil {
			undo()
			return nil, err
		}
	}
	return created, nil
}

// announceNewcomers tells every earlier member who joined — the roster is
// not in the system prompt, so this mail is how they learn. Returns them.
func announceNewcomers(teamID, leadID string, created []createdThread) []Member {
	roster, _ := ListTeam(teamID)
	isNew := map[string]bool{}
	names := make([]string, len(created))
	for i, t := range created {
		isNew[t.id] = true
		names[i] = fmt.Sprintf("\"%s\" (id %s)", t.title, t.id)
	}
	var earlier []Member
	var ids []string
	for _, m := range roster {
		if m.Role == RoleMember && !isNew[m.SessionID] {
			earlier = append(earlier, m)
			ids = append(ids, m.SessionID)
		}
	}
	if len(earlier) > 0 {
		_, _ = PostMessages(teamID, leadID, ids, KindUpdate, "New in the team: "+strings.Join(names, ", ")+".")
	}
	return earlier
}

// startThreads hands each new thread its brief. One that will not start is
// marked failed, so nobody waits on it.
func startThreads(teamID, leadID string, created []createdThread) []string {
	leadTitle := SessionTitle(leadID)
	roster, _ := ListTeam(teamID)
	var failed []string
	for _, t := range created {
		opening := OpeningMessage(leadTitle, t.title, t.brief, roster, t.id)
		meta := TurnMeta{Kind: "spawn", TeamID: teamID, From: &Peer{ID: leadID, Title: leadTitle}, Title: t.title, Agent: t.agent}
		if DeliverTurn(t.id, opening, meta) != nil {
			Move(t.id, StateFailed, "")
			failed = append(failed, t.title)
		}
	}
	return failed
}

func spawnResult(teamID string, created []createdThread, earlier []Member, failed []string) string {
	var b strings.Builder
	plural := "s"
	if len(created) == 1 {
		plural = ""
	}
	fmt.Fprintf(&b, "Started %d thread%s (team %s):", len(created), plural, teamID)
	for _, t := range created {
		fmt.Fprintf(&b, "\n- \"%s\" — id %s", t.title, t.id)
	}
	if len(earlier) > 0 {
		fmt.Fprintf(&b, "\n\nAlso in your team (finished threads keep their context; message one with %s to give it more work):", SendMessageTool)
		for _, m := range earlier {
			fmt.Fprintf(&b, "\n- \"%s\" — %s — id %s", m.Title, m.Effective(time.Now()), m.SessionID)
		}
	}
	if len(failed) > 0 {
		quoted := make([]string, len(failed))
		for i, f := range failed {
			quoted[i] = "\"" + f + "\""
		}
		fmt.Fprintf(&b, "\n\nThese could not be started and are marked failed: %s.", strings.Join(quoted, ", "))
	}
	fmt.Fprintf(&b, "\n\nThey are working in the background. Their messages and reports reach you on their own. Call %s only when your next step needs their results; otherwise keep working or end your turn.", WaitForTeamTool)
	return b.String()
}

// ─── send_message ───────────────────────────────────────────────────────

type sendArgs struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

func sendMessageTool(c ToolContext) ai.Tool {
	return tools.LoopTool(SendMessageTool, func(_ context.Context, a sendArgs) (ai.ToolResult, error) {
		return ai.ToolText(sendMessage(c, a)), nil
	})
}

func sendMessage(c ToolContext, a sendArgs) string {
	self := c.SessionID
	if c.planning() {
		return planModeRefusal
	}
	team, ok := TeamOf(self)
	if !ok {
		return noTeamYet
	}
	if strings.TrimSpace(a.Message) == "" {
		return "REJECTED: the message is empty."
	}
	roster, _ := ListTeam(team.TeamID)
	target := strings.TrimSpace(a.To)
	var recipients []Member
	for _, m := range roster {
		if m.SessionID != self && (target == "all" || m.SessionID == target) {
			recipients = append(recipients, m)
		}
	}
	if len(recipients) == 0 {
		return fmt.Sprintf("REJECTED: no thread in this team has id \"%s\". The team:\n%s", target, rosterText(roster, self))
	}
	ids := make([]string, len(recipients))
	for i, r := range recipients {
		ids[i] = r.SessionID
	}
	_, _ = PostMessages(team.TeamID, self, ids, KindMessage, a.Message)
	for _, id := range ids {
		WakeInbox(id)
	}
	sent := fmt.Sprintf("Sent to \"%s\".", recipients[0].Title)
	if target == "all" {
		sent = fmt.Sprintf("Sent to all %d threads.", len(recipients))
	}
	// Mail to a stopped team waits; without saying so the sender would wait
	// on an answer that is not coming.
	if IsStopped(team.TeamID) {
		return sent + " The team is stopped, so nobody will read it until the lead starts threads again."
	}
	return sent
}

// ─── team_board ─────────────────────────────────────────────────────────

type boardArgs struct {
	Action  string  `json:"action"`
	Key     string  `json:"key,omitempty"`
	Content *string `json:"content,omitempty"`
}

func boardTool(c ToolContext) ai.Tool {
	return tools.LoopTool(TeamBoardTool, func(_ context.Context, a boardArgs) (ai.ToolResult, error) {
		return ai.ToolText(board(c, a)), nil
	})
}

func board(c ToolContext, a boardArgs) string {
	self := c.SessionID
	team, ok := TeamOf(self)
	// A lead's board note before its first spawn — often in the same step
	// as the spawn — starts the team rather than bouncing.
	if !ok && a.Action == "post" {
		if _, err := CreateTeam(self); err == nil {
			team, ok = TeamOf(self)
		}
	}
	if !ok {
		return noTeamYet
	}
	key := strings.TrimSpace(a.Key)
	if a.Action == "post" {
		return postNote(team.TeamID, self, key, a.Content)
	}
	notes, _ := ReadBoard(team.TeamID)
	var blocks []string
	for _, n := range notes {
		if key == "" || n.Key == key {
			blocks = append(blocks, fmt.Sprintf("## %s\n(by \"%s\")\n%s", n.Key, SessionTitle(n.From), n.Body))
		}
	}
	if len(blocks) == 0 {
		if key != "" {
			return fmt.Sprintf("The board has no note \"%s\".", a.Key)
		}
		return "The board is empty."
	}
	return strings.Join(blocks, "\n\n")
}

func postNote(teamID, self, key string, content *string) string {
	if key == "" {
		return "REJECTED: posting needs a key."
	}
	if content == nil {
		return "REJECTED: posting needs content (an empty string removes the note)."
	}
	if _, err := PostBoardNote(teamID, self, key, *content); err != nil {
		return "REJECTED: " + err.Error()
	}
	Emit(Change{TeamID: teamID, Kind: ChangeBoard, SessionID: self})
	if strings.TrimSpace(*content) == "" {
		return fmt.Sprintf("Removed \"%s\" from the board.", key)
	}
	return fmt.Sprintf("Posted \"%s\" to the board.", key)
}

// ─── report ─────────────────────────────────────────────────────────────

type reportArgs struct {
	Summary string   `json:"summary"`
	Files   []string `json:"files,omitempty"`
	Open    string   `json:"open,omitempty"`
}

func reportTool(c ToolContext) ai.Tool {
	return tools.LoopTool(ReportTool, func(_ context.Context, a reportArgs) (ai.ToolResult, error) {
		return ai.ToolText(report(c, a)), nil
	})
}

func report(c ToolContext, a reportArgs) string {
	self := c.SessionID
	team, ok := TeamOf(self)
	if !ok {
		return noTeamYet
	}
	if team.Role == RoleLead {
		return "REJECTED: report is how a thread answers its lead; you are the lead. Answer the user directly."
	}
	parts := []string{strings.TrimSpace(a.Summary)}
	if len(a.Files) > 0 {
		parts = append(parts, "Files: "+strings.Join(a.Files, ", "))
	}
	if open := strings.TrimSpace(a.Open); open != "" {
		parts = append(parts, "Open: "+open)
	}
	var body []string
	for _, p := range parts {
		if p != "" {
			body = append(body, p)
		}
	}
	_, _ = PostMessages(team.TeamID, self, []string{team.LeadID}, KindReport, strings.Join(body, "\n\n"))
	Move(self, StateDone, "")
	WakeInbox(team.LeadID)
	return "Reported to the lead. Your job is done — end your turn now."
}

// Reported is whether a step called report: a member's turn ends there.
func Reported(calls []string) bool {
	for _, c := range calls {
		if c == ReportTool {
			return true
		}
	}
	return false
}

// decodeUntil reads wait_for_team's until: "all", "any", or a list of ids.
func decodeUntil(raw json.RawMessage) (mode string, ids []string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "all", nil
	}
	if json.Unmarshal(raw, &mode) == nil {
		return mode, nil
	}
	_ = json.Unmarshal(raw, &ids)
	var kept []string
	for _, id := range ids {
		if id = strings.TrimSpace(id); id != "" {
			kept = append(kept, id)
		}
	}
	return "list", kept
}
