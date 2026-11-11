package agent

import (
	"strings"

	"github.com/notshekhar/pi/internal/core/agents"
)

// defaultToolList is what the prompt names when no tool list is given.
const defaultToolList = "read, write, edit, bash, ls, grep, find"

// PromptParts is what loop's buildSystemPrompt assembles a prompt from.
type PromptParts struct {
	CWD string
	// Base is the agent's prompt; empty means the default agent's.
	Base string
	// Tools are the names actually offered this turn.
	Tools []string
	// Workspace is the AGENTS.md block, Memory the <memory> block.
	Workspace string
	Memory    string
}

// BuildSystemPrompt is loop's buildSystemPrompt: base, environment, then the
// workspace and memory blocks.
func BuildSystemPrompt(p PromptParts) string {
	base := strings.TrimSpace(p.Base)
	if base == "" {
		base = agents.DefaultBasePrompt
	}
	list := defaultToolList
	if len(p.Tools) > 0 {
		list = strings.Join(p.Tools, ", ")
	}
	parts := []string{base, "Working directory: " + p.CWD + "\n\nYou have these tools: " + list + "."}
	for _, block := range []string{p.Workspace, p.Memory} {
		if block != "" {
			parts = append(parts, block)
		}
	}
	return strings.Join(parts, "\n\n")
}

// SubagentNote is delegation guidance, appended when the task tool is in
// the turn's toolset.
func SubagentNote(agentNames []string) string {
	return `

Subagents (task tool): delegate work that would flood your context — broad codebase exploration, analyzing many files, research across directories, or an independent multi-file change. Each subagent runs in its own context window and returns only a final report.
Use task when: the job is self-contained, needs many file reads/searches, or you want parallel investigation of separate areas.
Do NOT use task when: the job is one or two tool calls, needs back-and-forth with the user, or depends on context only you have (unless you include it in the prompt).
Write complete prompts: the subagent knows nothing about this conversation — include paths, goals, constraints, and the exact output you expect. INDEPENDENT tasks belong in the same step: emit several task calls at once and they run in parallel (one per area to investigate) — sequential launches waste time. Only chain tasks when one needs another's report, and never mix task with other tool calls in a step. By default the subagent is a fork of you (same prompt and tools, minus task); pass agent to run a named agent instead. Available agents: ` + strings.Join(agentNames, ", ") + `.`
}

// BackgroundShellsNote is appended when the shells tool is in the toolset —
// only when the opt-in backgroundShells setting is on. The base prompt
// cannot carry it: with the setting off, bash refuses run_in_background.
const BackgroundShellsNote = `

Background shells (bash run_in_background, read with the shells tool): a command that is not meant to finish — a dev server, a watcher, a tail — goes in the background rather than holding a tool call open until it times out. Read its output with the shells tool when you need it; you are told when it exits, so never sleep-and-poll waiting for one. Kill what you started once you are done with it.`

// TodoNote is appended when the todo tool is in the toolset.
const TodoNote = `

Todos (todo tool): for any job with 3+ distinct steps, keep a visible checklist the user can watch. Create it when you start, keep exactly one item in_progress (with an activeForm label), and mark each step completed the moment it's done and verified — never batch completions at the end. Capture newly discovered follow-ups as todos; mark abandoned steps cancelled. Each call replaces the whole list, so resend every item. Skip the tool entirely for trivial or single-step requests — when in doubt, use it.`

// PlanModeNote is appended when the toolset carries exit_plan_mode: the
// session's read-only gate is on and this agent can ask to lift it. Without
// it the model learns it is planning only from a rejected edit.
const PlanModeNote = `

PLAN MODE IS ACTIVE. This is a read-only phase: edit and write are rejected and bash cannot modify anything, in every permission mode — do not try to work around it. Investigate with the read-only tools (read, ls, grep, find, read-only bash) until you understand the change well enough to describe it exactly, then call exit_plan_mode with the complete plan document. That call asks the user to approve it. If they approve, plan mode ends and you implement the plan yourself, immediately, in the same turn. If they decline, you stay in plan mode: address what they raised and call exit_plan_mode again. Never end a turn silently while plan mode is on — either deliver a plan or ask the question that is blocking it.`

// MCPServer is one configured MCP server, as the prompt names it, with its
// own usage notes once it has connected.
type MCPServer struct {
	Name         string
	Instructions string
}

// MCPToolSearchNote is loop's buildMcpToolSearchNote: a model shown one
// unfamiliar tool where it expected a server's tools usually concludes the
// capability is missing rather than searching for it.
func MCPToolSearchNote(servers []MCPServer) string {
	names := make([]string, len(servers))
	for i, s := range servers {
		names[i] = s.Name
	}
	return "\n\nMCP tools (from " + strings.Join(names, ", ") + ") are NOT listed individually in this conversation — there are too many to carry in every request. Reach them with the mcp_tools tool: search by keyword, describe the one you want, then call it. When a task needs a connected server, search before concluding you cannot do it."
}

// MCPInstructionsNote is each connected server's `instructions`, kept per
// server and attributed: two servers' notes are not interchangeable, and an
// unattributed blob reads as pi's own rules.
func MCPInstructionsNote(servers []MCPServer) string {
	var blocks []string
	for _, s := range servers {
		if text := strings.TrimSpace(s.Instructions); text != "" {
			blocks = append(blocks, `From MCP server "`+s.Name+`" (applies to its mcp__`+s.Name+`__* tools):`+"\n"+text)
		}
	}
	if len(blocks) == 0 {
		return ""
	}
	return "\n\nMCP server instructions — guidance published by the connected servers themselves, not by pi:\n\n" + strings.Join(blocks, "\n\n")
}
