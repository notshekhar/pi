package tools

import (
	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/config"
)

// Context is the shared state every tool sees.
type Context struct {
	CWD string
	// SessionID scopes background shells to the session that started them.
	SessionID string
	Registry  *Registry
	// Todos is the agent's visible plan. Shared with the renderer, so it
	// guards itself.
	Todos *TodoList
	// Ask puts questions to the user. Nil means there is nobody to ask.
	Ask Asker
	// PlanMode is the session's read-only planning gate. Nil means the
	// session has none, which reads as "off".
	PlanMode PlanMode
	// Sandbox is the user's `sandbox` setting for bash. Nil means off.
	Sandbox *config.SandboxSetting
	// BackgroundShells lets bash start commands that outlive their call.
	BackgroundShells bool
	// ReadOnlyBash runs every bash call in the fail-closed read-only sandbox:
	// set for an agent allowed bash but neither write nor edit (loop's
	// isReadOnlyBashAgent), so bash cannot become its way around them.
	ReadOnlyBash bool
}

func (t *Context) planMode() bool {
	return t.PlanMode != nil && t.PlanMode.Active()
}

// FileTools is loop's base tool set (tools/index.ts createTools), in loop's
// order: the tools every turn starts from before settings add to them.
func FileTools(t *Context) []ai.Tool {
	if t.Registry == nil {
		t.Registry = NewRegistry()
	}
	if t.Todos == nil {
		t.Todos = &TodoList{}
	}
	return []ai.Tool{Read(t), Write(t), Edit(t), Bash(t), Ls(t), Grep(t), Find(t), SQL(), Shells(t)}
}
