package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// Session agents: the persona this session speaks as.
//
// Distinct from the task tool's subagents, which is the distinction pi-agent
// was missing. `explore` and `review` are things you DELEGATE to — they run in
// their own context and report back — and offering them as session personas
// conflated "who am I talking to" with "who do I send away to look". loop
// keeps the two lists apart, and the session list is short on purpose:
// `default`, `plan`, and whatever the user has written.
//
// The list is what shift+tab cycles, which is the reason `plan` is in it at
// all: plan mode is a thing you flip in and out of dozens of times an hour,
// and a keystroke is the right cost for that.

const (
	defaultAgent = "default"
	planAgent    = "plan"
)

// agentsDirName holds user-written agents under the config directory.
const agentsDirName = "agents"

// sessionAgent is one persona this session can speak as.
type sessionAgent struct {
	Name string
	// About is the one-line description shown in the picker.
	About string
	// Prompt is the system prompt. Empty means the built-in default.
	Prompt string
	// Plan marks the agent that arms plan mode when selected.
	Plan bool
}

// planAgentPrompt is what `plan` speaks as. Plan mode enforces the read-only
// half; this is what makes the agent actually behave like a planner rather
// than a coder who keeps being refused.
const planAgentPrompt = `You are a planning assistant for coding tasks. You investigate, you never modify.

Method:
1. Map the territory first — ls/find for structure, grep for the patterns and call sites involved, read the files that matter. Never plan against imagined code.
2. Use bash for read-only investigation only: inspect state and gather facts (git log/status/diff, ls, cat, build/test output, dependency versions). Never use it to change anything.
3. For broad exploration, delegate to subagents with the task tool — they run read-only and return focused reports, keeping your context lean. Give each ONE narrow target and the context you already have; they start blank and see none of your work.
4. When the plan is final, deliver it with the plan tool. The tool's argument is the ENTIRE plan document as markdown — every section and step. Never write the plan as chat text.

The plan must contain:
- Ordered steps: which file changes, what goes where, and why that order.
- Exact anchors: function names, line references, existing patterns to mirror.
- Risks, unknowns, and any decision the user still has to make — flagged, not silently assumed.

A plan is done when another agent could execute it without re-discovering anything.`

// sessionAgents is the list, built-ins first then the user's own.
func sessionAgents() []sessionAgent {
	agents := []sessionAgent{
		{Name: defaultAgent, About: "the main agent — full tool access"},
		{Name: planAgent, About: "investigate and plan — read-only until the plan is approved",
			Prompt: planAgentPrompt, Plan: true},
	}
	for _, a := range userAgents() {
		agents = append(agents, a)
	}
	return agents
}

// userAgents reads `<config>/agents/*.md`. The file's body is the prompt; a
// leading `# Title` line, if present, is the description.
//
// A file named after a built-in is skipped rather than merged: silently
// replacing `plan` with a half-written prompt is the kind of surprise that
// only shows up as the agent quietly doing the wrong thing.
func userAgents() []sessionAgent {
	dir, err := config.Dir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(filepath.Join(dir, agentsDirName))
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	out := make([]sessionAgent, 0, len(names))
	for _, file := range names {
		name := strings.TrimSuffix(file, ".md")
		if name == defaultAgent || name == planAgent || !validAgentName(name) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, agentsDirName, file))
		if err != nil {
			continue
		}
		prompt := strings.TrimSpace(string(body))
		if prompt == "" {
			continue
		}
		about := "custom agent from " + file
		if title, rest, ok := strings.Cut(prompt, "\n"); ok && strings.HasPrefix(title, "# ") {
			about = strings.TrimSpace(strings.TrimPrefix(title, "# "))
			prompt = strings.TrimSpace(rest)
		}
		out = append(out, sessionAgent{Name: name, About: about, Prompt: prompt})
	}
	return out
}

// validAgentName is loop's rule: starts alphanumeric, then alnum/dash/
// underscore, at most 32 characters — the constraint is that it has to be
// spellable as a slash command.
func validAgentName(name string) bool {
	if name == "" || len(name) > 32 {
		return false
	}
	for i, r := range name {
		alnum := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'
		if i == 0 {
			if !alnum {
				return false
			}
			continue
		}
		if !alnum && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

// findSessionAgent looks one up by name.
func findSessionAgent(name string) (sessionAgent, bool) {
	for _, a := range sessionAgents() {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return sessionAgent{}, false
}

// cycleAgent is shift+tab: step to the next agent in the list.
//
// Landing on `plan` arms plan mode, and cycling away disarms it — but only
// when the cycle is what armed it. A gate set deliberately with `/plan` must
// survive someone browsing the agent list, or the two ways of entering plan
// mode fight each other.
func (t *repl) cycleAgent() {
	agents := sessionAgents()
	current := t.agent
	if current == "" {
		current = defaultAgent
	}
	at := 0
	for i, a := range agents {
		if a.Name == current {
			at = i
			break
		}
	}
	next := agents[(at+1)%len(agents)]
	t.setAgent(next.Name)
}

// setAgent switches the persona this session speaks as.
func (t *repl) setAgent(name string) {
	if name == "" {
		name = defaultAgent
	}
	agent, ok := findSessionAgent(name)
	if !ok {
		t.fail("unknown agent %q — /agents to choose", name)
		return
	}

	was := t.agent
	t.agent, t.run.Persona = agent.Name, agent.Prompt
	if agent.Name == defaultAgent {
		t.agent = ""
	}
	if err := config.Update(func(s *config.Settings) { s.Agent = agent.Name }); err != nil {
		t.fail("agent: %s", err)
	}
	t.app.Do(func() { t.app.SetAgent(agent.Name) })

	switch {
	case agent.Plan && !t.run.Planning:
		t.run.Planning = true
		t.planViaAgent = true
		t.app.Do(func() { t.app.SetPlanning(true) })
	case !agent.Plan && was == planAgent && t.planViaAgent:
		t.run.Planning = false
		t.planViaAgent = false
		t.app.Do(func() { t.app.SetPlanning(false) })
	}

	if agent.Name == defaultAgent {
		t.dim("agent default")
		return
	}
	t.dim("agent %s — %s", agent.Name, agent.About)
}

// pickAgent is `/agents` with no argument: choose the agent this session
// speaks as.
func (t *repl) pickAgent() {
	agents := sessionAgents()
	items := make([]tui.Item, 0, len(agents))
	for _, a := range agents {
		items = append(items, tui.Item{Value: a.Name, Label: a.Name, Description: a.About})
	}
	current := t.agent
	if current == "" {
		current = defaultAgent
	}
	t.pickPlain("Agents (Esc to close)", items, indexOf(items, current), current, func(choice tui.Item) {
		t.setAgent(choice.Value)
	})
}
