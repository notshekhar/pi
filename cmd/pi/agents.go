package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/tui"
)

// Session agents — loop's agents (core/agent/agents.ts) and its /agents
// manager (agent-handlers.ts): default, plan and data-analyst built in, plus
// ~/.loop/agents/<name>.md, each with a prompt, an optional tool allowlist
// and an optional subagent model.

const (
	defaultAgent = agents.DefaultAgentName
	planAgent    = agents.PlanAgentName
)

func toolsLabel(tools []string) string {
	if len(tools) == 0 {
		return "all tools"
	}
	return strings.Join(tools, ", ")
}

func modelLabel(model string) string {
	if model == "" {
		return "inherit"
	}
	return model
}

// currentAgent is the active agent's name (never "").
func (t *repl) currentAgent() string {
	if t.agent == "" {
		return defaultAgent
	}
	return t.agent
}

// applyAgent points the run at an agent: its prompt (appended to the base
// prompt; the default agent adds none) and its tool allowlist.
func (t *repl) applyAgent(name string) {
	t.agent = name
	if name == defaultAgent {
		t.agent, t.run.Persona = "", ""
	} else {
		t.run.Persona = agents.Prompt(name)
	}
	t.run.AllowedTools = agents.Tools(name)
}

// cycleAgent is shift+tab: the visible built-ins, plus the one extra agent
// the user opted into (a custom agent, or a revealed hidden built-in like
// data-analyst). Landing on plan arms plan mode; cycling away disarms it
// only when the cycle armed it.
func (t *repl) cycleAgent() {
	var cycle []string
	if t.cycleCustomAgent != "" && agents.Exists(t.cycleCustomAgent) {
		cycle = append(cycle, t.cycleCustomAgent)
	}
	for _, a := range agents.List() {
		if a.Builtin && !a.Hidden {
			cycle = append(cycle, a.Name)
		}
	}
	prev := t.currentAgent()
	at := -1
	for i, n := range cycle {
		if n == prev {
			at = i
			break
		}
	}
	next := cycle[(at+1)%len(cycle)]
	t.applyAgent(next)
	_ = config.SettingsStore().Set("agent", next)
	t.do(func() { t.app.SetAgent(next) })
	switch {
	case next == planAgent && !t.run.Planning:
		t.run.Planning, t.planViaAgent = true, true
		t.do(func() { t.app.SetPlanning(true) })
	case prev == planAgent && next != planAgent && t.planViaAgent:
		t.run.Planning, t.planViaAgent = false, false
		t.do(func() { t.app.SetPlanning(false) })
	}
}

// setAgent switches the session's agent (/agents → use, /agents <name>).
func (t *repl) setAgent(name string) {
	if name == "" {
		name = defaultAgent
	}
	if !agents.Exists(name) {
		t.fail("unknown agent: %s — /agents to create one", name)
		return
	}
	prev := t.currentAgent()
	t.applyAgent(name)
	_ = config.SettingsStore().Set("agent", name)
	t.do(func() { t.app.SetAgent(name) })
	// Custom agents — and hidden built-ins like data-analyst — join the Tab
	// cycle once explicitly selected.
	if !agents.IsBuiltin(name) || agents.IsHidden(name) {
		t.cycleCustomAgent = name
	}
	switch {
	case name == planAgent && !t.run.Planning:
		t.run.Planning, t.planViaAgent = true, true
		t.do(func() { t.app.SetPlanning(true) })
	case prev == planAgent && name != planAgent && t.planViaAgent:
		t.run.Planning, t.planViaAgent = false, false
		t.do(func() { t.app.SetPlanning(false) })
	}
	t.dim("agent → %s", name)
}

// restoreAgent is the startup half: the saved agent (never plan).
func (t *repl) restoreAgent() {
	var saved string
	config.SettingsStore().Get("agent", &saved)
	saved = agents.ResolveSaved(saved)
	if saved == "" || !agents.Exists(saved) {
		return
	}
	t.applyAgent(saved)
	if !agents.IsBuiltin(saved) || agents.IsHidden(saved) {
		t.cycleCustomAgent = saved
	}
	t.app.SetAgent(saved)
}

// agentsCmd is `/agents`: the manager, or a direct switch with a name.
func (t *repl) agentsCmd(rest string) {
	if name := strings.TrimSpace(rest); name != "" {
		t.setAgent(name)
		return
	}
	go t.manageAgents()
}

// pickAgentTools is loop's toggle over AGENT_TOOL_NAMES: nil = all tools,
// ok false = cancelled.
func (t *repl) pickAgentTools(initial []string) ([]string, bool) {
	all := append([]string{}, agents.ValidToolNames()...)
	on := map[string]bool{}
	for _, n := range all {
		on[n] = len(initial) == 0
	}
	for _, n := range initial {
		on[n] = true
	}
	picked := t.app.Toggle("Agent tools (task = can spawn subagents)", all, on)
	if picked == nil {
		return nil, false
	}
	if len(picked) == len(all) {
		return nil, true
	}
	return picked, true
}

// pickAgentModel is the subagent model: "" = inherit, ok false = cancelled.
func (t *repl) pickAgentModel(current string) (string, bool) {
	const inherit = "\x00inherit"
	desc := "(current)"
	if current != "" {
		desc = "clear the override"
	}
	items := []tui.Item{{Value: inherit, Label: "inherit (session model)", Description: desc}}
	var models []catalog.Model
	for _, m := range catalog.All() {
		if m.Available {
			models = append(models, m)
		}
	}
	sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
	for _, m := range models {
		label := m.ID
		if m.ID == current {
			label += "  (current)"
		}
		items = append(items, tui.Item{Value: m.ID, Label: label,
			Description: fmt.Sprintf("%s  ·  ctx %s  ·  $%g/$%g", m.Name, commaInt(m.Context), m.Cost.Input, m.Cost.Output)})
	}
	pick := t.app.Pick("Subagent model for this agent (type to filter)", items, 0, "")
	if pick == nil {
		return "", false
	}
	if pick.Value == inherit {
		return "", true
	}
	return pick.Value, true
}

func firstLine60(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	u := []rune(line)
	if len(u) > 60 {
		u = u[:60]
	}
	return string(u)
}

func (t *repl) manageAgents() {
	last := 0
	for {
		list := agents.List()
		items := []tui.Item{{Value: "+new", Label: "+ new agent", Description: "create an agent with its own tools and system prompt"}}
		for _, a := range list {
			label := a.Name
			if a.Name == t.currentAgent() {
				label += "  (active)"
			}
			if a.Builtin {
				label += "  [built-in]"
			}
			model := ""
			if a.Model != "" {
				model = " [" + a.Model + "]"
			}
			items = append(items, tui.Item{Value: a.Name, Label: label,
				Description: "[" + toolsLabel(a.Tools) + "]" + model + " " + firstLine60(a.Prompt)})
		}
		if last >= len(items) {
			last = len(items) - 1
		}
		pick := t.app.Select("Agents (Esc to close)", items, last, "")
		if pick == nil {
			return
		}
		last = indexOf(items, pick.Value)

		if pick.Value == "+new" {
			name := strings.TrimSpace(t.ask("agent name (e.g. reviewer)", ""))
			if name == "" {
				continue
			}
			if !agents.IsValidName(name) {
				t.fail("invalid name: %s (alphanumeric, dashes, ≤32 chars)", name)
				continue
			}
			if agents.Exists(name) || lookupCommand(name) != nil {
				t.fail(`"%s" already exists (agent or command)`, name)
				continue
			}
			tools, ok := t.pickAgentTools(nil)
			if !ok {
				continue
			}
			model, ok := t.pickAgentModel("")
			if !ok {
				continue
			}
			prompt := t.ask(fmt.Sprintf(`system prompt for "%s" [%s]`, name, toolsLabel(tools)), agents.DefaultBasePrompt)
			if strings.TrimSpace(prompt) == "" {
				continue
			}
			if err := agents.Save(name, prompt, tools, model); err != nil {
				t.fail("%s", err)
				continue
			}
			on := ""
			if model != "" {
				on = " on " + model
			}
			t.dim(`agent "%s" created [%s]%s — /%s <message> for one message, /agents → use for the session`, name, toolsLabel(tools), on, name)
			continue
		}

		name := pick.Value
		builtin := agents.IsBuiltin(name)
		tools := agents.Tools(name)
		model := agents.Model(name)
		actions := []tui.Item{
			{Value: "use", Label: "use", Description: fmt.Sprintf(`switch active agent to "%s"`, name)},
			{Value: "edit", Label: "edit prompt", Description: "edit this agent's system prompt"},
			{Value: "model", Label: "model: " + modelLabel(model), Description: "model this agent runs on as a subagent — inherit = the session model"},
		}
		if builtin {
			actions = append(actions, tui.Item{Value: "tools-view", Label: "tools (fixed)", Description: toolsLabel(tools)})
			if agents.HasBuiltinOverride(name) {
				actions = append(actions, tui.Item{Value: "delete", Label: "reset to built-in", Description: "remove the prompt override"})
			}
		} else {
			actions = append(actions,
				tui.Item{Value: "tools", Label: "edit tools", Description: "current: " + toolsLabel(tools)},
				tui.Item{Value: "delete", Label: "delete", Description: "remove agent and its /command"})
		}
		title := fmt.Sprintf("Agent: %s [%s]", name, toolsLabel(tools))
		if model != "" {
			title += " · " + model
		}
		action := t.choose(title, actions...)
		if action == nil {
			continue
		}
		prompt := agents.Prompt(name)
		if prompt == "" {
			prompt = agents.DefaultBasePrompt
		}
		switch action.Value {
		case "use":
			t.setAgent(name)
			return
		case "tools-view":
			t.dim(`agent "%s" tools (fixed): %s`, name, toolsLabel(tools))
		case "tools":
			if next, ok := t.pickAgentTools(tools); ok {
				if err := agents.Save(name, prompt, next, model); err != nil {
					t.fail("%s", err)
				} else {
					t.dim(`agent "%s" tools → %s`, name, toolsLabel(next))
					if name == t.currentAgent() {
						t.applyAgent(name)
					}
				}
			}
		case "model":
			if next, ok := t.pickAgentModel(model); ok {
				if err := agents.Save(name, prompt, tools, next); err != nil {
					t.fail("%s", err)
				} else {
					t.dim(`agent "%s" model → %s`, name, modelLabel(next))
				}
			}
		case "edit":
			if builtin {
				t.dim("tools (fixed): %s", toolsLabel(tools))
			}
			edited := t.ask(fmt.Sprintf(`system prompt for "%s" [%s]`, name, toolsLabel(tools)), prompt)
			if strings.TrimSpace(edited) == "" || strings.TrimSpace(edited) == strings.TrimSpace(prompt) {
				continue
			}
			if err := agents.Save(name, edited, tools, model); err != nil {
				t.fail("%s", err)
				continue
			}
			if name == t.currentAgent() {
				t.applyAgent(name)
			}
			t.dim(`agent "%s" prompt updated`, name)
		case "delete":
			if _, err := agents.Delete(name); err != nil {
				t.fail("%s", err)
				continue
			}
			// Resetting a hidden built-in's override leaves the agent itself
			// intact, so it stays revealed in the cycle.
			if t.cycleCustomAgent == name && !agents.IsHidden(name) {
				t.cycleCustomAgent = ""
			}
			if name == t.currentAgent() {
				if builtin {
					t.applyAgent(name)
				} else {
					t.applyAgent(defaultAgent)
					_ = config.SettingsStore().Set("agent", defaultAgent)
					t.do(func() { t.app.SetAgent(defaultAgent) })
				}
			}
			if builtin {
				t.dim(`"%s" prompt reset to built-in`, name)
			} else {
				t.dim(`agent "%s" deleted`, name)
			}
		}
	}
}
