package agent

import (
	"context"

	"github.com/notshekhar/pi/internal/modules/ai"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/permissions"
	"github.com/notshekhar/pi/internal/modules/core/session"
	"github.com/notshekhar/pi/internal/modules/core/tools"
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
	// Planning puts the run in plan mode: read-only, and told to produce a
	// proposal rather than carry one out.
	Planning bool
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
	// Subagents offers the task tool. Set from settings.
	Subagents bool
	// Persona is an extra instruction block appended to the system prompt,
	// set by /agents. Empty means the default agent.
	//
	// Appended rather than replacing the base prompt: an agent is a
	// specialisation ("review, do not fix"), and a persona that replaced the
	// prompt would also drop the tool list and the working directory with it.
	Persona string
	// Extra are tools contributed from outside the built-in set — MCP
	// servers today. They are ordinary tools: same dispatch, same rows, and
	// the same permissions policy.
	Extra []ai.Tool
}

// Turn streams one user prompt through the model and tools, handing the
// stream to consume.
func (r *Run) Turn(ctx context.Context, prompt string, consume Consume) error {
	_, err := r.TurnStream(ctx, prompt, consume)
	return err
}
