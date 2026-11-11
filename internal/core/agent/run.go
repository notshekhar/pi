package agent

import (
	"context"
	"sync/atomic"

	"github.com/notshekhar/pi/internal/ai"

	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/permissions"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

// Run is one user turn driven to completion.
//
// Rendering is injected, never imported: core has no idea a terminal exists,
// which is what keeps the module boundary honest (and is why the one-shot and
// interactive paths can render the same turn completely differently).
type Run struct {
	Config  config.Config
	Session *session.Session
	Tools   *tools.Context

	// Permissions gates tool calls. The zero value means the shipped default.
	Permissions permissions.Policy
	// Ask puts an `ask` call to a person. Nil means nothing can be approved,
	// which makes `ask` behave as `deny` — the safe reading for a
	// non-interactive run.
	Ask Ask
	// planning is plan mode; read and switched with Planning/SetPlanning,
	// from the UI and from the plan tools mid-turn.
	planning atomic.Bool
	// OnPlanMode is told when plan mode changes, so the UI follows a switch
	// the agent made.
	OnPlanMode func(active bool)
	// Progress reports subagent activity so the UI can show a live status.
	Progress TaskProgress
	// ExtensionTools and ExtensionPrompt are what the enabled extensions
	// contribute to a turn. Separate from Extra, which the MCP manager owns
	// and rewrites wholesale whenever a server connects or drops — merging
	// them into one slice meant a reconnect silently discarded every
	// extension tool.
	ExtensionTools  []ai.Tool
	ExtensionPrompt string
	// WrapTools lets extensions DECORATE the assembled tool set — appending
	// to a result, not replacing a tool. Applied last, so it sees the
	// extensions' own tools too.
	WrapTools func([]ai.Tool) []ai.Tool
	// PreTool is consulted before every tool call. A non-empty `deny` REFUSES
	// the call, with the string as the reason shown to the model and the
	// user; a non-nil `args` REPLACES what the call runs with. This is how a
	// PreToolUse hook enforces something the permission rules cannot express,
	// and how a wrapper rewrites a command before it executes.
	PreTool func(ctx context.Context, tool string, args map[string]any) (updated map[string]any, deny string)
	// RewriteCall is the extensions' turn at the same seam, after PreTool.
	// Hooks come first because a hook is the user's own rule and an extension
	// is a convenience — a rewrite must never be able to slip past a refusal.
	RewriteCall func(tool string, args map[string]any) map[string]any
	// OnPermissionRequest is told when the policy escalates a call to the
	// user, just before the prompt goes up. Observation only — the answer
	// still comes from Ask.
	OnPermissionRequest func(ctx context.Context, tool string, args map[string]any, reason string)
	// Agent is the active agent's name ("" = default), and Persona its
	// prompt, which REPLACES the base prompt (loop's agent semantics); the
	// working directory, tool list, workspace and memory blocks are appended
	// either way. Set both, with AllowedTools, through UseAgent.
	Agent   string
	Persona string
	// AllowedTools is the agent's tool allowlist (loop's getAgentTools);
	// empty means every tool. A restricted agent keeps exactly its list: no
	// MCP or extension tools, and `task` only when named.
	AllowedTools []string
	// MCPTools, when set, is read at the start of every turn: the MCP
	// manager's live tool set, which changes as servers connect, drop and
	// re-list without anyone reassigning a slice.
	MCPTools func() []ai.Tool
	// MCPResources is where mcp_resource reads from; nil means no MCP.
	MCPResources tools.ResourceSource
	// turnTracker is the running turn's cost tracker, which subagents bill
	// into; set by RunTurn.
	turnTracker *CostTracker
	// MCPServers, when set, is the configured MCP servers (not disabled
	// ones) and the instructions of those connected, for the prompt notes
	// while their tools are in the turn.
	MCPServers func() []MCPServer
}

// Turn streams one user prompt through the model and tools, handing the
// stream to consume.
func (r *Run) Turn(ctx context.Context, prompt string, consume Consume) error {
	_, err := r.TurnStream(ctx, prompt, consume)
	return err
}

// connectedMCPTools is the MCP servers' tools right now.
func (r *Run) connectedMCPTools() []ai.Tool {
	if r.MCPTools == nil {
		return nil
	}
	return r.MCPTools()
}

// MCPToolCount is how many MCP tools are connected.
func (r *Run) MCPToolCount() int { return len(r.connectedMCPTools()) }

// tracker is where this turn's spend goes; a throwaway outside RunTurn,
// whose ledger rows still record the spend.
func (r *Run) tracker() *CostTracker {
	if r.turnTracker != nil {
		return r.turnTracker
	}
	return NewCostTracker()
}

// UseAgent points the run at a named agent: its prompt, its tool list, and
// the name a forked subagent inherits. "" is the default agent.
func (r *Run) UseAgent(name string) {
	if name == agents.DefaultAgentName {
		name = ""
	}
	r.Agent = name
	r.Persona = ""
	if name != "" {
		r.Persona = agents.Prompt(name)
	}
	r.AllowedTools = agents.Tools(name)
}

// Fork is a run for a new conversation with this one's setup: same model,
// agent, policy and hooks, but a fresh tool context (read-before-edit and
// the todo list belong to a conversation) and plan mode off — plan mode is a
// stance on a conversation, and the fork is a different one. The caller
// gives it a session.
func (r *Run) Fork() *Run {
	tc := *r.Tools
	tc.Registry, tc.Todos = tools.NewRegistry(), &tools.TodoList{}
	return &Run{
		Config:              r.Config,
		Tools:               &tc,
		Permissions:         r.Permissions,
		Ask:                 r.Ask,
		OnPlanMode:          r.OnPlanMode,
		Progress:            r.Progress,
		ExtensionTools:      r.ExtensionTools,
		ExtensionPrompt:     r.ExtensionPrompt,
		WrapTools:           r.WrapTools,
		PreTool:             r.PreTool,
		RewriteCall:         r.RewriteCall,
		OnPermissionRequest: r.OnPermissionRequest,
		Agent:               r.Agent,
		Persona:             r.Persona,
		AllowedTools:        r.AllowedTools,
		MCPTools:            r.MCPTools,
		MCPResources:        r.MCPResources,
		MCPServers:          r.MCPServers,
	}
}
