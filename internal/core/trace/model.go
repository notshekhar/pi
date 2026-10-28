// Package trace is loop's /trace (trace/*): the session as a timing-and-cost
// model — turns, the steps inside them, the tools inside those — rendered as
// one self-contained HTML page. The page's styles and scripts are loop's,
// synced verbatim (scripts/sync-loop), so the page is the same page.
package trace

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/notshekhar/pi/internal/core/session"
)

// Usage is loop's TraceUsage.
type Usage struct {
	Input      int64    `json:"input"`
	Output     int64    `json:"output"`
	CacheRead  int64    `json:"cacheRead"`
	CacheWrite int64    `json:"cacheWrite"`
	Reasoning  int64    `json:"reasoning"`
	USD        *float64 `json:"usd,omitempty"`
	Estimated  bool     `json:"estimated"`
	// accum marks a sum (turn/total): loop builds those from emptyUsage and
	// adds usd later, so "estimated" precedes "usd" in its JSON.
	accum bool
}

// MarshalJSON writes loop's insertion order.
func (u Usage) MarshalJSON() ([]byte, error) {
	type flat struct {
		Input      int64 `json:"input"`
		Output     int64 `json:"output"`
		CacheRead  int64 `json:"cacheRead"`
		CacheWrite int64 `json:"cacheWrite"`
		Reasoning  int64 `json:"reasoning"`
	}
	f := flat{u.Input, u.Output, u.CacheRead, u.CacheWrite, u.Reasoning}
	if u.accum {
		return marshal(struct {
			flat
			Estimated bool     `json:"estimated"`
			USD       *float64 `json:"usd,omitempty"`
		}{f, u.Estimated, u.USD})
	}
	return marshal(struct {
		flat
		USD       *float64 `json:"usd,omitempty"`
		Estimated bool     `json:"estimated"`
	}{f, u.USD, u.Estimated})
}

func (into *Usage) add(u *Usage) {
	if u == nil {
		return
	}
	into.Input += u.Input
	into.Output += u.Output
	into.CacheRead += u.CacheRead
	into.CacheWrite += u.CacheWrite
	into.Reasoning += u.Reasoning
	if u.USD != nil {
		v := *u.USD
		if into.USD != nil {
			v = *into.USD + v
		}
		into.USD = &v
	}
	if u.Estimated {
		into.Estimated = true
	}
}

// Pricer prices a usage block by catalog (loop's priceUsage); ok false for an
// unknown model. Set by the program so this package needs no agent import.
var Pricer func(model string, u session.Usage) (usd float64, ok bool)

func traceUsage(model string, block *session.Usage) *Usage {
	if block == nil {
		return nil
	}
	n := block.Normalize()
	var usd *float64
	switch {
	case n.USD != nil:
		usd = n.USD
	case n.Cost != nil:
		usd = n.Cost
	case model != "" && Pricer != nil:
		if v, ok := Pricer(model, *block); ok {
			usd = &v
		}
	}
	v := func(p *int64) int64 {
		if p == nil {
			return 0
		}
		return *p
	}
	return &Usage{Input: v(n.Input), Output: v(n.Output), CacheRead: v(n.CacheRead), CacheWrite: v(n.CacheWrite),
		Reasoning: v(n.Reasoning), USD: usd, Estimated: n.Estimated}
}

// Subagent is loop's TraceSubagent.
type Subagent struct {
	Agent      string   `json:"agent"`
	Prompt     string   `json:"prompt"`
	Result     string   `json:"result"`
	Steps      *float64 `json:"steps,omitempty"`
	DurationMs *float64 `json:"durationMs,omitempty"`
	Usage      *Usage   `json:"usage,omitempty"`
}

// ToolTiming is a recorded tool bar.
type ToolTiming struct {
	StartedAt float64  `json:"startedAt"`
	EndedAt   *float64 `json:"endedAt,omitempty"`
}

// Tool is loop's TraceTool.
type Tool struct {
	ToolCallID  string      `json:"toolCallId"`
	Name        string      `json:"name"`
	Input       string      `json:"input"`
	Output      *string     `json:"output,omitempty"`
	OutputChars *int        `json:"outputChars,omitempty"`
	Error       bool        `json:"error"`
	Timing      *ToolTiming `json:"timing,omitempty"`
	Subagent    *Subagent   `json:"subagent,omitempty"`
	// synthetic is a tool loop built from a subagent entry alone, whose
	// object literal puts output before error.
	synthetic bool
}

// MarshalJSON writes loop's insertion order: the call's own keys, then the
// timing attached at the step, then the result that arrived after it.
func (t Tool) MarshalJSON() ([]byte, error) {
	if t.synthetic {
		return marshal(struct {
			ToolCallID  string    `json:"toolCallId"`
			Name        string    `json:"name"`
			Input       string    `json:"input"`
			Output      *string   `json:"output,omitempty"`
			OutputChars *int      `json:"outputChars,omitempty"`
			Error       bool      `json:"error"`
			Subagent    *Subagent `json:"subagent,omitempty"`
		}{t.ToolCallID, t.Name, t.Input, t.Output, t.OutputChars, t.Error, t.Subagent})
	}
	return marshal(struct {
		ToolCallID  string      `json:"toolCallId"`
		Name        string      `json:"name"`
		Input       string      `json:"input"`
		Error       bool        `json:"error"`
		Timing      *ToolTiming `json:"timing,omitempty"`
		Output      *string     `json:"output,omitempty"`
		OutputChars *int        `json:"outputChars,omitempty"`
		Subagent    *Subagent   `json:"subagent,omitempty"`
	}{t.ToolCallID, t.Name, t.Input, t.Error, t.Timing, t.Output, t.OutputChars, t.Subagent})
}

// StepTiming is loop's TraceStepTiming (kind recorded | derived | none).
type StepTiming struct {
	Kind         string   `json:"kind"`
	StartedAt    *float64 `json:"startedAt,omitempty"`
	FirstTokenAt *float64 `json:"firstTokenAt,omitempty"`
	ModelEndedAt *float64 `json:"modelEndedAt,omitempty"`
	EndedAt      float64  `json:"endedAt"`
	RetryWaitMs  *float64 `json:"retryWaitMs,omitempty"`
}

// MarshalJSON keeps loop's key order per kind.
func (t StepTiming) MarshalJSON() ([]byte, error) {
	switch t.Kind {
	case "recorded":
		return marshal(struct {
			Kind         string   `json:"kind"`
			StartedAt    float64  `json:"startedAt"`
			FirstTokenAt *float64 `json:"firstTokenAt,omitempty"`
			ModelEndedAt float64  `json:"modelEndedAt"`
			EndedAt      float64  `json:"endedAt"`
			RetryWaitMs  *float64 `json:"retryWaitMs,omitempty"`
		}{t.Kind, deref(t.StartedAt), t.FirstTokenAt, deref(t.ModelEndedAt), t.EndedAt, t.RetryWaitMs})
	case "derived":
		return marshal(struct {
			Kind      string  `json:"kind"`
			StartedAt float64 `json:"startedAt"`
			EndedAt   float64 `json:"endedAt"`
		}{t.Kind, deref(t.StartedAt), t.EndedAt})
	}
	return marshal(struct {
		Kind    string  `json:"kind"`
		EndedAt float64 `json:"endedAt"`
	}{t.Kind, t.EndedAt})
}

// marshal is json.Marshal without HTML escaping (JSON.stringify's output).
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

func deref(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

// Step is loop's TraceStep.
type Step struct {
	Index       int        `json:"index"`
	EntryID     string     `json:"entryId,omitempty"`
	Model       string     `json:"model"`
	Text        string     `json:"text"`
	Reasoning   string     `json:"reasoning"`
	ReasoningMs []int64    `json:"reasoningMs,omitempty"`
	Tools       []*Tool    `json:"tools"`
	Usage       *Usage     `json:"usage,omitempty"`
	Interrupted bool       `json:"interrupted"`
	Timing      StepTiming `json:"timing"`
}

// Event is loop's TraceEvent.
type Event struct {
	TS    int64  `json:"ts"`
	Kind  string `json:"kind"`
	Label string `json:"label"`
}

// UserText is a turn's prompt.
type UserText struct {
	Text string `json:"text"`
	TS   int64  `json:"ts"`
}

// Turn is loop's TraceTurn.
type Turn struct {
	Index         int      `json:"index"`
	User          UserText `json:"user"`
	Steps         []*Step  `json:"steps"`
	Events        []Event  `json:"events"`
	StartedAt     float64  `json:"startedAt"`
	EndedAt       float64  `json:"endedAt"`
	Usage         Usage    `json:"usage"`
	FullyRecorded bool     `json:"fullyRecorded"`
}

// Totals is loop's TraceTotals.
type Totals struct {
	Turns  int     `json:"turns"`
	Steps  int     `json:"steps"`
	Tools  int     `json:"tools"`
	WallMs float64 `json:"wallMs"`
	Usage  Usage   `json:"usage"`
}

// Coverage is loop's TraceCoverage.
type Coverage struct {
	Recorded int `json:"recorded"`
	Derived  int `json:"derived"`
	None     int `json:"none"`
}

// SessionInfo is the model's session block.
type SessionInfo struct {
	ID        string `json:"id"`
	Name      string `json:"name,omitempty"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	CWD       string `json:"cwd"`
	CreatedAt int64  `json:"createdAt"`
}

// Model is loop's TraceModel.
type Model struct {
	Session     SessionInfo `json:"session"`
	GeneratedAt int64       `json:"generatedAt"`
	Turns       []*Turn     `json:"turns"`
	Totals      Totals      `json:"totals"`
	Coverage    Coverage    `json:"coverage"`
}

// FromSession is loop's sessionToTrace.
func FromSession(s *session.Session) Model {
	b := &builder{model: s.Info.Model, turns: []*Turn{}}
	for _, e := range s.CurrentBranch() {
		b.add(e)
	}
	b.closeTurn()
	totals := Totals{Turns: len(b.turns), Usage: Usage{accum: true}}
	for _, t := range b.turns {
		totals.Steps += len(t.Steps)
		for _, st := range t.Steps {
			totals.Tools += len(st.Tools)
		}
		totals.WallMs += max(0, t.EndedAt-t.StartedAt)
		u := t.Usage
		totals.Usage.add(&u)
	}
	model := s.LastModel()
	if model == "" {
		model = s.Info.Model
	}
	return Model{
		Session: SessionInfo{ID: s.ID, Name: s.Name(), Model: model, Provider: s.Info.Provider,
			CWD: s.Info.CWD, CreatedAt: s.Info.CreatedAt},
		GeneratedAt: time.Now().UnixMilli(),
		Turns:       b.turns,
		Totals:      totals,
		Coverage:    b.coverage,
	}
}

type builder struct {
	model    string
	turns    []*Turn
	coverage Coverage
	turn     *Turn
	step     *Step
	anchor   *float64
}

func (b *builder) add(e session.Entry) {
	switch e.Type {
	case session.TypeMessage:
		switch e.Role {
		case "user":
			b.openTurn(session.TextContent(e.Content), e.TS)
		case "assistant":
			b.addStep(e)
		default:
			b.addToolResults(e)
		}
	case session.TypeSubagent:
		b.addSubagent(e)
	case session.TypeModelChange:
		if e.To != "" {
			b.model = e.To
		}
		b.addEvent(e.TS, "model-change", "model → "+e.To)
	case session.TypeCompact:
		after := 0
		if e.TokensAfter != nil {
			after = *e.TokensAfter
		}
		b.addEvent(e.TS, "compact", "context compacted "+commas(int64(e.TokensBefore))+" → "+commas(int64(after))+" tokens")
	case session.TypeBranchSummary:
		b.addEvent(e.TS, "branch-summary", "branch summarized")
	}
}

func (b *builder) openTurn(text string, ts int64) *Turn {
	b.closeTurn()
	b.turn = &Turn{Index: len(b.turns) + 1, User: UserText{text, ts}, Steps: []*Step{}, Events: []Event{},
		StartedAt: float64(ts), EndedAt: float64(ts), Usage: Usage{accum: true}}
	a := float64(ts)
	b.anchor = &a
	return b.turn
}

func (b *builder) closeTurn() {
	t := b.turn
	if t == nil {
		return
	}
	t.FullyRecorded = len(t.Steps) > 0
	for _, s := range t.Steps {
		if s.Timing.Kind != "recorded" {
			t.FullyRecorded = false
		}
	}
	b.turns = append(b.turns, t)
	b.turn, b.step, b.anchor = nil, nil, nil
}

func (b *builder) addEvent(ts int64, kind, label string) {
	if b.turn != nil {
		b.turn.Events = append(b.turn.Events, Event{ts, kind, label})
	}
}

// recorded is loop's StepTiming as persisted under an entry's `timing`.
type recorded struct {
	StartedAt    float64  `json:"startedAt"`
	FirstTokenAt *float64 `json:"firstTokenAt"`
	ModelEndedAt float64  `json:"modelEndedAt"`
	EndedAt      float64  `json:"endedAt"`
	RetryWaitMs  *float64 `json:"retryWaitMs"`
	Tools        []struct {
		ToolCallID string   `json:"toolCallId"`
		ToolName   string   `json:"toolName"`
		StartedAt  float64  `json:"startedAt"`
		EndedAt    *float64 `json:"endedAt"`
		Error      bool     `json:"error"`
	} `json:"tools"`
}

func timingOf(e session.Entry) *recorded {
	raw, ok := e.Extra["timing"]
	if !ok {
		return nil
	}
	var r recorded
	if json.Unmarshal(raw, &r) != nil {
		return nil
	}
	return &r
}

func stepTiming(t *recorded, anchor *float64, ts float64) StepTiming {
	if t != nil {
		st, me := t.StartedAt, t.ModelEndedAt
		out := StepTiming{Kind: "recorded", StartedAt: &st, FirstTokenAt: t.FirstTokenAt, ModelEndedAt: &me, EndedAt: t.EndedAt}
		if t.RetryWaitMs != nil && *t.RetryWaitMs != 0 {
			out.RetryWaitMs = t.RetryWaitMs
		}
		return out
	}
	if anchor != nil && ts >= *anchor {
		a := *anchor
		return StepTiming{Kind: "derived", StartedAt: &a, EndedAt: ts}
	}
	return StepTiming{Kind: "none", EndedAt: ts}
}

func (b *builder) addStep(e session.Entry) {
	turn := b.turn
	if turn == nil {
		turn = b.openTurn("", e.TS)
		b.anchor = nil
	}
	model := e.Model
	if model == "" {
		model = b.model
	}
	rec := timingOf(e)
	timing := stepTiming(rec, b.anchor, float64(e.TS))
	switch timing.Kind {
	case "recorded":
		b.coverage.Recorded++
	case "derived":
		b.coverage.Derived++
	default:
		b.coverage.None++
	}
	text, reasoning, tools := readAssistant(e.Content)
	attachToolTimings(&tools, rec)
	usage := traceUsage(model, e.Usage)
	step := &Step{Index: len(turn.Steps) + 1, EntryID: e.ID, Model: model, Text: text, Reasoning: reasoning,
		ReasoningMs: e.ReasoningMs, Tools: tools, Usage: usage, Interrupted: e.Interrupted, Timing: timing}
	turn.Steps = append(turn.Steps, step)
	b.step = step
	turn.Usage.add(usage)
	if timing.Kind == "recorded" && *timing.StartedAt < turn.StartedAt {
		turn.StartedAt = *timing.StartedAt
	}
	b.advance(timing.EndedAt)
}

func (b *builder) addToolResults(e session.Entry) {
	step := b.step
	if step == nil {
		return
	}
	for _, p := range partsOf(e.Content) {
		if p.Type != "tool-result" || p.ToolCallID == "" {
			continue
		}
		for _, tool := range step.Tools {
			if tool.ToolCallID != p.ToolCallID {
				continue
			}
			out := toolOutputText(p.Output)
			capped := u16slice(out, OutputCap)
			n := u16len(out)
			tool.Output, tool.OutputChars = &capped, &n
			if isErrorOutput(p.Output) {
				tool.Error = true
			}
			break
		}
	}
	b.advance(float64(e.TS))
}

func extraFloat(e session.Entry, key string) *float64 {
	raw, ok := e.Extra[key]
	if !ok {
		return nil
	}
	var v float64
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func extraString(e session.Entry, key string) string {
	var v string
	if raw, ok := e.Extra[key]; ok {
		_ = json.Unmarshal(raw, &v)
	}
	return v
}

func (b *builder) addSubagent(e session.Entry) {
	step := b.step
	if step == nil {
		return
	}
	model := e.Model
	if model == "" {
		model = b.model
	}
	usage := traceUsage(model, e.Usage)
	sub := &Subagent{Agent: e.Agent, Prompt: e.Prompt, Result: e.Result, Steps: extraFloat(e, "steps"),
		DurationMs: extraFloat(e, "durationMs"), Usage: usage}
	callID := extraString(e, "toolCallId")
	var existing *Tool
	if callID != "" {
		for _, t := range step.Tools {
			if t.ToolCallID == callID {
				existing = t
				break
			}
		}
	}
	if existing != nil {
		existing.Name = "task"
		existing.Subagent = sub
		if existing.Input == "" {
			existing.Input = oneLineString(e.Prompt, 200)
		}
	} else {
		id := callID
		if id == "" {
			id = "subagent-" + jsNumber(float64(e.TS))
		}
		out := u16slice(e.Result, OutputCap)
		n := u16len(e.Result)
		step.Tools = append(step.Tools, &Tool{ToolCallID: id, Name: "task", Input: oneLineString(e.Prompt, 200),
			Output: &out, OutputChars: &n, Subagent: sub, synthetic: true})
	}
	if b.turn != nil {
		b.turn.Usage.add(usage)
	}
}

func (b *builder) advance(ts float64) {
	if b.turn != nil && ts > b.turn.EndedAt {
		b.turn.EndedAt = ts
	}
	a := ts
	if b.anchor != nil && *b.anchor > a {
		a = *b.anchor
	}
	if a < 0 {
		a = 0
	}
	b.anchor = &a
}

func readAssistant(raw json.RawMessage) (string, string, []*Tool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, "", []*Tool{}
	}
	text, reasoning := "", ""
	tools := []*Tool{}
	for _, p := range partsOf(raw) {
		switch p.Type {
		case "text":
			text += p.Text
		case "reasoning":
			reasoning += p.Text
		case "tool-call":
			if p.ToolCallID != "" {
				name := p.ToolName
				if name == "" {
					name = "tool"
				}
				tools = append(tools, &Tool{ToolCallID: p.ToolCallID, Name: name, Input: toolInputLine(p.Input, 200)})
			}
		}
	}
	return text, reasoning, tools
}

func attachToolTimings(tools *[]*Tool, t *recorded) {
	if t == nil {
		return
	}
	for _, tt := range t.Tools {
		var tool *Tool
		for _, x := range *tools {
			if x.ToolCallID == tt.ToolCallID {
				tool = x
				break
			}
		}
		if tool == nil {
			name := tt.ToolName
			if name == "" {
				name = "tool"
			}
			tool = &Tool{ToolCallID: tt.ToolCallID, Name: name}
			*tools = append(*tools, tool)
		}
		tool.Timing = &ToolTiming{StartedAt: tt.StartedAt, EndedAt: tt.EndedAt}
		if tt.Error {
			tool.Error = true
		}
	}
}
