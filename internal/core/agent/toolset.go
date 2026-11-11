package agent

import (
	"encoding/json"
	"slices"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/memory"
	"github.com/notshekhar/pi/internal/core/projects"
	"github.com/notshekhar/pi/internal/core/skills"
	"github.com/notshekhar/pi/internal/core/tools"
)

// One turn's tools and the prompt that describes them — loop's
// assembleTurnTools and the prompt assembly after it (agent/turn.ts). The
// order and every gate follow loop's: a setting that is off REMOVES its
// tool, because a tool that exists and refuses spends context on its
// description and teaches the model to keep trying it.

// assembly is a toolset being built for an agent's allowlist.
type assembly struct {
	allowed []string
	tools   []ai.Tool
}

// restricted reports an agent with its own tool list.
func (a *assembly) restricted() bool { return len(a.allowed) > 0 }

// names reports the allowlist naming a tool.
func (a *assembly) names(name string) bool { return slices.Contains(a.allowed, name) }

// offers reports whether the agent may have a tool: every tool for an
// unrestricted agent, only the named ones for a restricted one.
func (a *assembly) offers(name string) bool { return !a.restricted() || a.names(name) }

func (a *assembly) add(t ...ai.Tool) { a.tools = append(a.tools, t...) }

func (a *assembly) has(name string) bool {
	return slices.ContainsFunc(a.tools, func(t ai.Tool) bool { return t.Name() == name })
}

// isReadOnlyBashAgent is loop's: an agent allowed bash but neither write nor
// edit, whose bash must not become its way around them.
func isReadOnlyBashAgent(allowed []string) bool {
	return slices.Contains(allowed, "bash") && !slices.Contains(allowed, "write") && !slices.Contains(allowed, "edit")
}

// bindTools points the tool context at this run's session and settings.
func (r *Run) bindTools(settings config.Settings) {
	if r.Session != nil {
		r.Tools.SessionID = r.Session.ID
	}
	r.Tools.CWD = r.Config.CWD
	r.Tools.BackgroundShells = settings.BackgroundShellsOn()
	r.Tools.Sandbox = settings.Sandbox
	r.Tools.PlanMode = runPlanMode{r}
	r.Tools.ReadOnlyBash = isReadOnlyBashAgent(r.AllowedTools)
}

// Toolset is the tools this turn will carry.
func (r *Run) Toolset() []ai.Tool {
	settings := config.LoadSettings()
	r.bindTools(settings)
	a := &assembly{allowed: r.AllowedTools}
	r.addBaseTools(a, settings)
	r.addConnectedTools(a, settings)
	r.addAgentTools(a, settings)
	r.addPlanTools(a)
	if r.WrapTools != nil {
		return r.WrapTools(a.tools)
	}
	return a.tools
}

// addBaseTools is loop's createTools filtered by the agent's list, with the
// shells tool only when background shells are on, then websearch and
// artifact.
func (r *Run) addBaseTools(a *assembly, settings config.Settings) {
	for _, t := range tools.FileTools(r.Tools) {
		if t.Name() == "shells" && !settings.BackgroundShellsOn() {
			continue
		}
		if a.offers(t.Name()) {
			a.add(t)
		}
	}
	if settings.WebSearchOn() && a.offers("websearch") {
		a.add(tools.WebSearch(r.Tools))
	}
	// An artifact is written with the write tool, so an agent without write
	// never gets it, even by naming it.
	if settings.ArtifactsOn() && (!a.restricted() || (a.names("artifact") && a.names("write"))) {
		a.add(tools.Artifact(r.Tools))
	}
}

// addConnectedTools is MCP and extension tools: unrestricted agents only,
// and before task so a forked subagent inherits them. Past the tool-search
// threshold the MCP tools are not listed; mcp_tools reaches them by name,
// reading the live set so a server that connects mid-turn counts.
func (r *Run) addConnectedTools(a *assembly, settings config.Settings) {
	if a.restricted() {
		return
	}
	if mcpTools := r.connectedMCPTools(); settings.MCPOn() && len(mcpTools) > 0 {
		if settings.MCPToolSearch(len(mcpTools)) {
			a.add(tools.MCPToolSearch(r.connectedMCPTools))
		} else {
			a.add(mcpTools...)
		}
	}
	if settings.MCPOn() {
		a.add(r.mcpResourceTool()...)
	}
	a.add(r.ExtensionTools...)
}

// mcpResourceTool is the resource reader, only while a connected server
// publishes resources: a tools-only setup should not pay a tool slot to be
// told there is nothing to read.
func (r *Run) mcpResourceTool() []ai.Tool {
	src := r.MCPResources
	if src == nil || (len(src.Resources()) == 0 && len(src.ResourceTemplates()) == 0) {
		return nil
	}
	return []ai.Tool{tools.MCPResource(src)}
}

// addAgentTools is task, skill, todo, the team tools and ask, each behind
// its setting.
func (r *Run) addAgentTools(a *assembly, settings config.Settings) {
	if settings.SubagentsOn() && a.offers("task") {
		a.add(TaskTool(r, r.Progress, toolNames(a.tools)))
	}
	if len(invocable(r.turnSkills(settings))) > 0 && a.offers("skill") {
		a.add(tools.Skill(r.Tools))
	}
	if settings.TodosOn() {
		a.add(tools.Todos(r.Tools))
	}
	r.teamTools(a, settings)
	if settings.AskUserOn() && r.Tools.Ask != nil && a.offers("ask") {
		a.add(tools.Ask(r.Tools))
	}
}

// addPlanTools: a restricted planning agent delivers with `plan`; the main
// agent asks its own way in and out of plan mode, which needs someone to
// approve the switch.
func (r *Run) addPlanTools(a *assembly) {
	canSelfExit := !a.restricted() && r.Ask != nil
	planning := r.Planning()
	if (a.restricted() && a.names("plan")) || (planning && !canSelfExit) {
		a.add(tools.Plan())
	}
	if planning && canSelfExit {
		a.add(tools.ExitPlanMode(runPlanMode{r}))
	}
	if !planning && canSelfExit {
		a.add(tools.EnterPlanMode(runPlanMode{r}))
	}
}

// turnSkills is the skills this turn may see: project skills inject
// instructions into the prompt, so they wait for the folder to be trusted.
func (r *Run) turnSkills(settings config.Settings) []skills.Skill {
	if !settings.SkillsOn() || !projects.IsTrusted(r.Config.CWD) {
		return nil
	}
	return skills.Load(r.Config.CWD)
}

func invocable(all []skills.Skill) []skills.Skill {
	return slices.DeleteFunc(slices.Clone(all), func(s skills.Skill) bool { return s.DisableModelInvocation })
}

// ToolNames is the toolset's names, for the prompt's tool list.
func (r *Run) ToolNames() []string {
	return toolNames(r.Toolset())
}

func toolNames(toolset []ai.Tool) []string {
	names := make([]string, len(toolset))
	for i, t := range toolset {
		names[i] = t.Name()
	}
	return names
}

// ToolSchemaChars is how much of the window the tool definitions occupy —
// name, description, and JSON schema, which is what actually goes on the wire.
func (r *Run) ToolSchemaChars() int {
	return toolSchemaChars(r.Toolset())
}

func toolSchemaChars(toolset []ai.Tool) int {
	total := 0
	for _, t := range toolset {
		total += len(t.Name()) + len(t.Description())
		if schema, err := json.Marshal(t.InputSchema()); err == nil {
			total += len(schema)
		}
	}
	return total
}

// SystemPrompt is the turn's system prompt for an assembled toolset: loop's
// buildSystemPrompt plus the notes keyed off the tools actually offered —
// never off the settings that produced them, so an extension that drops a
// tool also silences the guidance for it.
func (r *Run) SystemPrompt(toolset []ai.Tool) string {
	settings := config.LoadSettings()
	names := toolNames(toolset)
	has := func(name string) bool { return slices.Contains(names, name) }
	system := BuildSystemPrompt(PromptParts{
		CWD:       r.Config.CWD,
		Base:      r.Persona,
		Tools:     names,
		Workspace: r.workspaceBlock(settings),
		Memory:    r.memoryBlock(settings),
	})
	if has("task") {
		system += SubagentNote(agentNames())
	}
	system += teamNote(names)
	if has("todo") {
		system += TodoNote
	}
	if has("shells") {
		system += BackgroundShellsNote
	}
	if has("exit_plan_mode") {
		system += PlanModeNote
	}
	if searchMode := has("mcp_tools"); searchMode || slices.ContainsFunc(names, isMCPTool) {
		servers := r.mcpServers()
		if searchMode {
			system += MCPToolSearchNote(servers)
		}
		system += MCPInstructionsNote(servers)
	}
	system += skills.FormatForPrompt(r.turnSkills(settings), has("skill"))
	return system + r.extensionBlock()
}

func (r *Run) mcpServers() []MCPServer {
	if r.MCPServers == nil {
		return nil
	}
	return r.MCPServers()
}

func isMCPTool(name string) bool { return len(name) > 5 && name[:5] == "mcp__" }

func agentNames() []string {
	var names []string
	for _, info := range agents.List() {
		names = append(names, info.Name)
	}
	return names
}

func (r *Run) workspaceBlock(settings config.Settings) string {
	if !settings.WorkspaceContextOn() {
		return ""
	}
	return LoadWorkspaceContext(r.Config.CWD).Text
}

// memoryBlock is the main agent's <memory> block. Subagents never get it, so
// fan-out work cannot race duplicate memory writes.
func (r *Run) memoryBlock(settings config.Settings) string {
	if !settings.MemoryOn() {
		return ""
	}
	return memory.Context(r.Config.CWD)
}

// extensionBlock is what the enabled extensions add to the system prompt —
// LAST, so an extension can qualify the agent's own instructions.
func (r *Run) extensionBlock() string {
	if r.ExtensionPrompt == "" {
		return ""
	}
	return "\n\n" + r.ExtensionPrompt
}
