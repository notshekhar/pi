package agent

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/skills"
	"github.com/notshekhar/pi/internal/core/tools"
)

// Subagents — loop's task tool (agent/subagent.ts). A subagent is by default
// a FORK of the turn's agent: same prompt, same tools minus task, a fresh
// context window. Only its final report reaches the parent model; the whole
// run is saved to the session, so a later task call can continue it with
// follow_up.

// subagentSuffix is loop's SUBAGENT_SYSTEM_SUFFIX: the rules of a run.
const subagentSuffix = `

You are a subagent launched by a main agent. Rules of the run:
- Work autonomously — there is no user to ask; resolve ambiguity with the most reasonable reading of the prompt and state which reading you chose.
- If the prompt contains NO actionable task — only a path, a name, or a fragment with no instruction — do NOT invent one. Stop immediately and reply: "The task prompt contains no instruction — re-issue the task stating exactly what to find or do." Guessing at an unstated task wastes the entire run.
- Stay on the given task. Do not expand scope or touch anything the prompt didn't cover.
- Be economical while investigating: take the shortest path to the answer. Don't repeat searches, or exhaustively sweep directories when targeted reads suffice. Stop the moment you can answer — every extra step costs real money and delays the main agent.
- Only your final message returns to the main agent — it is their only window into your work. Write like the main agent would: lead with the outcome, then the insight needed to understand and act (exact paths, names, key findings, conclusions, evidence, surprises). Be precise and concise for what was asked — complete enough that the main agent need not redo your investigation, but no padding and no play-by-play of what you tried.
- If you cannot finish, report exactly how far you got and what blocked you; a precise partial report beats a vague complete-sounding one.`

// Defaults for the subagent settings, as loop's.
const (
	defaultSubagentMaxSteps = 50
	defaultStallSeconds     = 180
)

// TaskProgress reports a subagent's activity so the parent can show a live
// status on its row. Nil disables reporting.
type TaskProgress func(id, status string)

type taskArgs struct {
	Agent    string `json:"agent,omitempty"`
	Prompt   string `json:"prompt"`
	FollowUp string `json:"follow_up,omitempty"`
}

// SubagentStats is a run's summary, for display only.
type SubagentStats struct {
	Steps      int      `json:"steps"`
	DurationMs int64    `json:"durationMs"`
	USD        *float64 `json:"usd,omitzero"`
	Model      string   `json:"model"`
}

// SubagentOutput is the task tool's UI value (loop's SubagentOutput); the
// model gets only the bounded report.
type SubagentOutput struct {
	History Activity       `json:"history"`
	Report  string         `json:"report"`
	Stats   *SubagentStats `json:"stats,omitzero"`
}

// TaskTool returns the task tool for a parent run. parentTools caps what a
// subagent may use: delegation never widens access.
func TaskTool(parent *Run, progress TaskProgress, parentTools []string) ai.Tool {
	def := taskDef()
	gate := newSubagentGate(config.LoadSettings().SubagentParallel())
	return ai.NewToolWithSchema("task", def.Description, def.Schema, func(ctx context.Context, a taskArgs) (ai.ToolResult, error) {
		s := &subagentRun{parent: parent, progress: progress, parentTools: parentTools, gate: gate, callID: ai.ToolCallID(ctx)}
		out := s.run(ctx, a)
		return ai.ToolText(boundReport(out.Report)).WithDisplay(out), nil
	})
}

// taskDef is loop's task definition with the agent list as it is now — the
// exported copy names only the agents that existed when it was generated.
func taskDef() tools.Def {
	def := tools.LoopDef("task")
	var schema map[string]any
	raw, _ := json.Marshal(def.Schema, jsonopt.Write)
	if json.Unmarshal(raw, &schema, jsonopt.Read) != nil {
		return def
	}
	props, _ := schema["properties"].(map[string]any)
	if agent, ok := props["agent"].(map[string]any); ok {
		agent["description"] = "Optional named agent to run instead of a fork of yourself (its prompt applies; its tools are capped to yours). One of: " + strings.Join(agentNames(), ", ")
	}
	patched, _ := json.Marshal(schema, jsonopt.Write)
	_ = json.Unmarshal(patched, &def.Schema, jsonopt.Read)
	return def
}

// subagentGate caps how many subagents of one turn run at once; the rest
// wait their turn. A nil gate is unlimited.
type subagentGate chan struct{}

func newSubagentGate(limit int) subagentGate {
	if limit <= 0 {
		return nil
	}
	return make(subagentGate, limit)
}

// acquire waits for a slot; false when ctx ended first.
func (g subagentGate) acquire(ctx context.Context) bool {
	if g == nil {
		return true
	}
	select {
	case g <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (g subagentGate) tryAcquire() bool {
	if g == nil {
		return true
	}
	select {
	case g <- struct{}{}:
		return true
	default:
		return false
	}
}

func (g subagentGate) release() {
	if g != nil {
		<-g
	}
}

// subagentRun is one task call.
type subagentRun struct {
	parent      *Run
	progress    TaskProgress
	parentTools []string
	gate        subagentGate
	callID      string

	name string
	// followUpOf is the run this one continues, "" for a fresh run.
	followUpOf string
	cfg        config.Config
	activity   Activity
}

func (s *subagentRun) status(text string) {
	if s.progress != nil && s.callID != "" {
		s.progress(s.callID, text)
	}
}

// note puts a visible line at the head of the run's box.
func (s *subagentRun) note(text string) {
	s.activity.delta("text", "["+text+"]\n")
}

func (s *subagentRun) run(ctx context.Context, a taskArgs) SubagentOutput {
	defer s.status("")
	thread := s.resolveAgent(a)
	if !s.gate.tryAcquire() {
		s.status("queued — waiting for a free subagent slot")
		if !s.gate.acquire(ctx) {
			return SubagentOutput{Report: "Subagent aborted before it started (turn interrupted)."}
		}
	}
	defer s.gate.release()
	if ctx.Err() != nil {
		return SubagentOutput{Report: "Subagent aborted before it started (turn interrupted)."}
	}
	out, err := s.drive(ctx, a.Prompt, thread)
	if err != nil {
		msg := "Subagent failed: " + err.Error()
		return SubagentOutput{History: Activity{{Type: "text", Text: msg}}, Report: msg}
	}
	return out
}

// resolveAgent picks the agent: a fork of the turn's agent, a named one, or
// — for a follow_up — the agent of the run being continued. It returns that
// run's thread, nil when starting fresh.
func (s *subagentRun) resolveAgent(a taskArgs) []provider.Message {
	s.name = s.parent.Agent
	if s.name == "" || !agents.Exists(s.name) {
		s.name = agents.DefaultAgentName
	}
	if a.Agent != "" && agents.Exists(a.Agent) {
		s.name = a.Agent
	}
	if a.FollowUp == "" {
		return nil
	}
	s.parent.Session.RLock()
	branch := s.parent.Session.CurrentBranch()
	s.parent.Session.RUnlock()
	agentName, thread, ok := followUpThread(branch, a.FollowUp)
	if !ok {
		s.note(fmt.Sprintf("follow_up %q matches no earlier task in this session — starting fresh", a.FollowUp))
		return nil
	}
	if agents.Exists(agentName) {
		s.name = agentName
	}
	s.followUpOf = a.FollowUp
	return thread
}

// drive runs the subagent to its end and saves the run.
func (s *subagentRun) drive(ctx context.Context, prompt string, thread []provider.Message) (SubagentOutput, error) {
	settings := config.LoadSettings()
	cfg, warning := subagentModel(s.parent.Config, agents.Model(s.name), settings.SubagentModel)
	if warning != "" {
		s.note(warning)
	}
	s.cfg = cfg
	model, err := config.LanguageModel(cfg)
	if err != nil {
		return SubagentOutput{}, err
	}
	toolset := s.toolset()
	sub := &Run{Config: cfg, Permissions: s.parent.Permissions, Ask: s.parent.Ask, PreTool: s.parent.PreTool, RewriteCall: s.parent.RewriteCall}
	needsApproval, approveTool := sub.approvalHooks()
	maxSteps := settings.SubagentMaxSteps
	if maxSteps <= 0 {
		maxSteps = defaultSubagentMaxSteps
	}
	stallSeconds := defaultStallSeconds
	if settings.SubagentStallSeconds != nil {
		stallSeconds = *settings.SubagentStallSeconds
	}
	watch, runCtx := newStallWatch(ctx, time.Duration(stallSeconds)*time.Second)
	defer watch.stop()

	started := time.Now()
	res, err := ai.StreamText(runCtx, cachedOptions(ai.Options{
		Model:         model,
		System:        s.systemPrompt(settings, toolset),
		Messages:      append(slices.Clone(thread), ai.UserText(prompt)),
		Tools:         toolset,
		MaxSteps:      maxSteps,
		Reasoning:     cfg.Reasoning,
		NeedsApproval: needsApproval,
		ApproveTool:   approveTool,
	}, cfg, s.parent.Session.ID+"/"+s.callID))
	if err != nil {
		return SubagentOutput{}, err
	}
	billing := s.consume(res.Stream, watch)
	report := ""
	if final, _ := res.Final(); final != nil {
		report = strings.TrimSpace(final.Text)
	}
	if watch.Stalled() {
		report = s.stallReport(report, billing.steps, stallSeconds)
	}
	stats := &SubagentStats{Steps: billing.steps, DurationMs: time.Since(started).Milliseconds(), USD: billing.usd, Model: cfg.FullID()}
	s.save(prompt, report, billing, stats)
	return SubagentOutput{History: s.activity, Report: report, Stats: stats}, nil
}

// toolset is the subagent's tools: the candidates (file tools plus what the
// parent's MCP servers and extensions offer) narrowed to the agent's list
// and capped to the parent's. It shares the parent's session-scoped state —
// read-before-edit records, the plan-mode gate, background shells — so a
// subagent's reads unlock the parent's edits and plan mode binds it too.
func (s *subagentRun) toolset() []ai.Tool {
	parentCtx := s.parent.Tools
	tc := &tools.Context{
		CWD: parentCtx.CWD, SessionID: parentCtx.SessionID, Registry: parentCtx.Registry, Todos: &tools.TodoList{},
		PlanMode: parentCtx.PlanMode, Sandbox: parentCtx.Sandbox, BackgroundShells: parentCtx.BackgroundShells,
	}
	candidates := tools.FileTools(tc)
	if !tc.BackgroundShells {
		candidates = slices.DeleteFunc(candidates, func(t ai.Tool) bool { return t.Name() == "shells" })
	}
	candidates = append(candidates, s.parent.connectedMCPTools()...)
	candidates = append(candidates, s.parent.mcpResourceTool()...)
	candidates = append(candidates, s.parent.ExtensionTools...)
	effective := resolveSubagentTools(toolNames(candidates), agents.Tools(s.name), s.parentTools)
	tc.ReadOnlyBash = isReadOnlyBashAgent(effective)
	return slices.DeleteFunc(candidates, func(t ai.Tool) bool { return !slices.Contains(effective, t.Name()) })
}

// systemPrompt is the parent's composition — so a fork behaves like the
// main agent — with the run rules appended. Skills use the read-tool
// wording: the skill tool is the parent's alone.
func (s *subagentRun) systemPrompt(settings config.Settings, toolset []ai.Tool) string {
	return BuildSystemPrompt(PromptParts{
		CWD:       s.parent.Config.CWD,
		Base:      agents.Prompt(s.name),
		Tools:     toolNames(toolset),
		Workspace: s.parent.workspaceBlock(settings),
	}) + skills.FormatForPrompt(s.parent.turnSkills(settings), false) + subagentSuffix
}

// subagentBilling is what a run spent, step by step.
type subagentBilling struct {
	steps  int
	usd    *float64
	usage  provider.Usage
	rowIDs []int64
}

// consume drains the stream into the activity log, bills each step into the
// parent's tracker and keeps the watchdog fed.
func (s *subagentRun) consume(stream <-chan ai.StreamPart, watch *stallWatch) subagentBilling {
	var b subagentBilling
	tracker := s.parent.tracker()
	bill := BillContext{Source: session.SourceSubagent, CWD: s.parent.Config.CWD, SessionPub: s.parent.Session.ID}
	for part := range stream {
		watch.part()
		switch p := part.(type) {
		case provider.TextDelta:
			s.activity.delta("text", p.Delta)
		case provider.ReasoningDelta:
			s.activity.delta("reasoning", p.Delta)
		case provider.ToolCall:
			watch.toolStarted()
			s.activity.tool(p.ToolName, p.Input)
			s.status(s.name + " · " + p.ToolName)
		case ai.ToolExecuted:
			watch.toolDone()
		case ai.StepFinish:
			b.steps++
			stamped := StampUsage(s.cfg.FullID(), ToUsage(p.Usage))
			_, rowID := tracker.AddRow(s.cfg.FullID(), stamped, bill)
			b.rowIDs = append(b.rowIDs, rowID)
			if stamped.USD != nil {
				b.usd = addUSD(b.usd, *stamped.USD)
			}
		case ai.RunFinish:
			b.usage = p.Usage
		}
	}
	return b
}

func addUSD(total *float64, usd float64) *float64 {
	sum := usd
	if total != nil {
		sum += *total
	}
	return &sum
}

// stallReport makes a watchdog abort visible in the box and gives the
// parent a report it can act on: the run is saved, so follow_up resumes it.
func (s *subagentRun) stallReport(report string, steps, stallSeconds int) string {
	note := fmt.Sprintf("[aborted: provider streamed nothing for %ds — stalled connection]", stallSeconds)
	s.activity.delta("text", "\n"+note+"\n")
	if report != "" {
		return report
	}
	tail := s.activity.Text()
	if len(tail) > 3000 {
		tail = strings.ToValidUTF8(tail[len(tail)-3000:], "")
	}
	plural := "s"
	if steps == 1 {
		plural = ""
	}
	return fmt.Sprintf("Subagent aborted after %d completed step%s: the provider stream stalled (no output for %ds). Partial activity:\n%s\n\nRetry the task — pass follow_up with this call's id to continue from the partial work.",
		steps, plural, stallSeconds, tail)
}

// save records the run in the session — loop's subagent entry — and files
// every step's ledger row under it.
func (s *subagentRun) save(prompt, report string, b subagentBilling, stats *SubagentStats) {
	result := report
	if result == "" {
		result = s.activity.Text()
	}
	if result == "" {
		result = "(subagent produced no output)"
	}
	extra := map[string]any{"toolCallId": s.callID, "steps": stats.Steps, "durationMs": stats.DurationMs}
	if len(s.activity) > 0 {
		extra["activity"] = s.activity
	}
	if s.followUpOf != "" {
		extra["followUpOf"] = s.followUpOf
	}
	entry := session.Entry{Type: session.TypeSubagent, Agent: s.name, Prompt: prompt, Result: result, Model: stats.Model, Extra: map[string]jsontext.Value{}}
	for k, v := range extra {
		entry.Extra[k], _ = json.Marshal(v, jsonopt.Write)
	}
	usage := StampUsage(stats.Model, ToUsage(b.usage))
	entry.Usage = &usage
	batch := []session.Entry{entry}
	s.parent.Session.Lock()
	err := s.parent.Session.AppendAll(batch...)
	s.parent.Session.Unlock()
	if err != nil {
		return
	}
	for _, rowID := range b.rowIDs {
		if rowID != 0 {
			_ = session.AttachLedgerEntry(rowID, batch[0].ID)
		}
	}
}
