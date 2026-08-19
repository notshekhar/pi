package tools

import (
	"context"

	"github.com/notshekhar/pi/internal/modules/ai"
)

// Context is the shared state every tool sees.
type Context struct {
	CWD      string
	Registry *Registry
	// Todos is the agent's visible plan. Shared with the renderer, so it
	// guards itself.
	Todos *TodoList
	// WebSearch enables the network-facing search tool. Off by default: the
	// agent should not reach the internet because someone forgot a setting.
	WebSearch bool
	// Ask puts a question to the user. Nil means the `ask` and `plan` tools
	// report that there is nobody to ask, rather than inventing an answer.
	Ask Asker
	// Sandbox bounds what shell commands may write. Not a security boundary
	// — see sandbox.go.
	Sandbox SandboxMode
	// Feature switches from settings. Each REMOVES its tool rather than
	// leaving one that refuses every call: a tool that exists but always
	// fails still spends context on its description and teaches the model to
	// keep trying it.
	NoAsk    bool // askUser off — the agent cannot stop to ask
	NoMemory bool // memory off
	NoTodos  bool // todos off
}

// All returns the built-in tool set.
func All(ctx *Context) []ai.Tool {
	if ctx.Registry == nil {
		ctx.Registry = NewRegistry()
	}
	if ctx.Todos == nil {
		ctx.Todos = &TodoList{}
	}
	set := []ai.Tool{
		Read(ctx),
		Write(ctx),
		Edit(ctx),
		Bash(ctx),
		Ls(ctx),
		Grep(ctx),
		Glob(ctx),
		Find(ctx),
		Skill(ctx),
	}
	if !ctx.NoAsk {
		set = append(set, Ask(ctx), Plan(ctx))
	}
	if !ctx.NoTodos {
		set = append(set, Todos(ctx))
	}
	if !ctx.NoMemory {
		set = append(set, Memory(ctx))
	}
	// Registered only when enabled, rather than registered and always
	// failing: a tool that exists but refuses every call spends context on
	// its description and teaches the model to keep trying it.
	if ctx.WebSearch {
		set = append(set, WebSearch(ctx))
	}
	return set
}

func aborted(ctx context.Context) bool {
	return ctx.Err() != nil
}
