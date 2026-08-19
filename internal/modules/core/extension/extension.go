// Package extension is pi-agent's plugin layer.
//
// loop's extensions are JavaScript, loaded by a runtime that is already in
// the process. Go has no equivalent: `plugin.Open` exists but demands the
// exact same compiler version, the same versions of every shared dependency,
// and the same build flags as the host binary, and it does not work at all
// on Windows or with a statically linked binary — which is what pi-agent
// ships as. An extension system built on it would break on the first `go`
// upgrade.
//
// So extensions here are Go, in the two forms Go actually supports:
//
//   - BUILT IN — a Go value compiled into the binary and registered in the
//     table below. This is what loop's own extensions are too: lsp,
//     wayfinder and the rest ship inside loop rather than being installed.
//     They can contribute tools, commands, and system-prompt fragments.
//
//   - EXTERNAL — a separate Go PROGRAM that speaks MCP over stdio. `/install`
//     compiles it and registers it as a server. This reuses the MCP client
//     that already works rather than inventing a second protocol, and it
//     gives real isolation: an extension that panics takes down its own
//     process and nothing else.
//
// Capabilities are optional interfaces rather than one fat one, so an
// extension that only adds a command does not have to stub out four methods
// it does not use.
package extension

import (
	"context"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/tools"
)

// Extension is the minimum: something with a name that can say what it is.
type Extension interface {
	Name() string
	About() string
}

// ToolProvider contributes tools to the agent.
type ToolProvider interface {
	Tools(ctx *tools.Context) []ai.Tool
}

// CommandProvider contributes slash commands.
type CommandProvider interface {
	Commands() []Command
}

// PromptProvider contributes a system-prompt fragment, appended when the
// extension is enabled.
type PromptProvider interface {
	SystemPrompt(cwd string) string
}

// StatusProvider reports the extension's current state in one word, for the
// startup line — "ponytail (full)" rather than a bare "ponytail".
//
// An extension with modes is otherwise invisible: you can see that it is on
// and not which way it is pointed, which is the thing you actually forget.
type StatusProvider interface {
	Status() string
}

// TurnObserver is told about a prompt before it is sent.
//
// Read-only by construction — it returns nothing. It exists for extensions
// that watch for a phrase ("stop caveman"), not for ones that want to rewrite
// the turn; rewriting belongs to hooks, which are auditable.
type TurnObserver interface {
	OnBeforeTurn(input string)
}

// Store is the per-extension settings an extension reads and writes.
//
// Namespaced by extension name, so two extensions cannot collide on "mode",
// and passed IN rather than reached for: an extension that could open the
// settings file could also read another's keys, and the boundary is worth
// more than the convenience.
type Store interface {
	Get(key, fallback string) string
	Set(key, value string) error
}

// Configurable receives its settings store once, at registration.
type Configurable interface {
	UseStore(s Store)
}

// UI is the little the extensions may ask of the terminal.
//
// An interface declared HERE and implemented by the app, for the same reason
// Store is: an extension that could reach the renderer directly would invert
// the module graph, and this package must not import the terminal at all.
// What it exposes is deliberately small — a picker and a text prompt — because
// those are the two questions a command asks, and anything more would be an
// extension drawing its own chrome.
type UI interface {
	// Select opens a picker and blocks until the user chooses. It returns ""
	// when they cancel. `current` is marked in the list.
	Select(title string, items []Item, current string) string
	// Prompt asks for a line of text. "" is both a blank answer and a cancel.
	Prompt(label, initial string) string
}

// Item is one row of a picker.
type Item struct {
	Value       string
	Label       string
	Description string
}

// ui is the installed host. Nil until the app sets one — a headless run has
// no picker to open, and a command that needs one has to cope.
var ui UI

// SetUI installs the host's UI.
func SetUI(host UI) { ui = host }

// Ask returns the installed UI, and whether there is one.
//
// Callers must handle the absent case rather than assume: `run` mode and the
// gateways drive the same commands with nobody to answer a picker.
func Ask() (UI, bool) { return ui, ui != nil }

// Repainter is an extension that needs to trigger a redraw — one whose output
// changes on a timer rather than in response to anything the user did.
type Repainter interface {
	SetRepaint(f func())
}

// SetRepaint gives every registered extension a way to ask for a repaint.
func SetRepaint(f func()) {
	for _, e := range builtins {
		if r, ok := e.(Repainter); ok {
			r.SetRepaint(f)
		}
	}
}

// Closer is an extension that owns something needing an orderly shutdown — a
// child process, a connection. A leaked language server holds a whole
// toolchain's memory, so this is not optional for anything that spawns.
type Closer interface {
	Shutdown()
}

// Shutdown closes every registered extension. Called once as the session ends,
// on EVERY extension rather than only the enabled ones: one switched off
// mid-session may still have processes running from before.
func Shutdown() {
	for _, e := range builtins {
		if c, ok := e.(Closer); ok {
			c.Shutdown()
		}
	}
}

// Bind hands every registered extension its own settings store.
func Bind(open func(name string) Store) {
	for _, e := range builtins {
		if c, ok := e.(Configurable); ok {
			c.UseStore(open(e.Name()))
		}
	}
}

// StatusOf is an extension's status word, empty when it has none.
func StatusOf(e Extension) string {
	if s, ok := e.(StatusProvider); ok {
		return s.Status()
	}
	return ""
}

// NotifyTurn tells every enabled observer about a prompt.
func NotifyTurn(enabled []Extension, input string) {
	for _, e := range enabled {
		if o, ok := e.(TurnObserver); ok {
			o.OnBeforeTurn(input)
		}
	}
}

// Command is one extension-provided slash command.
//
// Run returns text to print rather than writing to the UI: an extension must
// not need a reference to the renderer, both because that would invert the
// module graph and because it is what makes a command testable.
type Command struct {
	Name  string
	About string
	// Run does the work. `rest` is the argument text. A returned prompt is
	// sent to the model as though the user had typed it, which is how a
	// command like /wayfinder works.
	Run func(ctx context.Context, cwd, rest string) (output string, prompt string, err error)
}

// builtins is every extension compiled into the binary, in listing order.
var builtins []Extension

// Register adds a builtin. Called from package init functions, so adding an
// extension is adding a file rather than editing a list.
func Register(e Extension) { builtins = append(builtins, e) }

// All returns every builtin extension.
func All() []Extension { return append([]Extension{}, builtins...) }

// Find returns a builtin by name.
func Find(name string) (Extension, bool) {
	for _, e := range builtins {
		if e.Name() == name {
			return e, true
		}
	}
	return nil, false
}

// DefaultEnabled marks an extension that is on unless switched off.
//
// Nothing implements it today, which is the point: every builtin is OPT-IN,
// so a fresh install behaves exactly as it would with no extensions at all.
// The rule used to be the opposite — on unless disabled, on the reasoning
// that a builtin shipping disabled is one nobody discovers — and that is
// wrong for anything with a persona: `caveman` on by default would silently
// rewrite every reply in a voice nobody asked for. Discovery is `/extensions`
// listing them, not switching them on.
type DefaultEnabled interface {
	DefaultEnabled() bool
}

// IsEnabled reports whether one extension is on, given the user's choices.
//
// `state` records BOTH directions, because either can differ from the
// default: an off-list alone cannot express "on" for something that ships off.
func IsEnabled(e Extension, state map[string]bool) bool {
	if on, chosen := state[e.Name()]; chosen {
		return on
	}
	if d, ok := e.(DefaultEnabled); ok {
		return d.DefaultEnabled()
	}
	return false
}

// Enabled filters to the extensions currently switched on.
func Enabled(state map[string]bool) []Extension {
	var out []Extension
	for _, e := range builtins {
		if IsEnabled(e, state) {
			out = append(out, e)
		}
	}
	return out
}

// ToolsFrom collects the tools every enabled extension contributes.
func ToolsFrom(list []Extension, ctx *tools.Context) []ai.Tool {
	var out []ai.Tool
	for _, e := range list {
		if p, ok := e.(ToolProvider); ok {
			out = append(out, p.Tools(ctx)...)
		}
	}
	return out
}

// CommandsFrom collects the commands every enabled extension contributes.
// CallRewriter rewrites a tool call's arguments before it runs.
//
// Returning nil leaves the call alone, which is the answer for every call an
// extension does not care about — and the reason this is a separate
// capability rather than a field on every extension.
type CallRewriter interface {
	RewriteCall(tool string, args map[string]any) map[string]any
}

// RewriteFrom runs each enabled rewriter in turn, threading the result.
//
// In order, so two rewriters compose rather than race. Returns nil when
// nothing changed, so the caller can tell "rewritten to the same thing" from
// "not rewritten" — which matters, because a rewrite makes the policy re-judge
// the call.
func RewriteFrom(list []Extension, tool string, args map[string]any) map[string]any {
	var out map[string]any
	current := args
	for _, e := range list {
		r, ok := e.(CallRewriter)
		if !ok {
			continue
		}
		if updated := r.RewriteCall(tool, current); updated != nil {
			out, current = updated, updated
		}
	}
	return out
}

func CommandsFrom(list []Extension) []Command {
	var out []Command
	for _, e := range list {
		if p, ok := e.(CommandProvider); ok {
			out = append(out, p.Commands()...)
		}
	}
	return out
}

// PromptFrom joins the system-prompt fragments of every enabled extension.
func PromptFrom(list []Extension, cwd string) string {
	out := ""
	for _, e := range list {
		if p, ok := e.(PromptProvider); ok {
			if frag := p.SystemPrompt(cwd); frag != "" {
				out += "\n\n" + frag
			}
		}
	}
	return out
}
