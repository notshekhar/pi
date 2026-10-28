package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/agent"
	"github.com/notshekhar/pi/internal/core/agents"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/db"
	"github.com/notshekhar/pi/internal/core/extension"
	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/mcp"
	"github.com/notshekhar/pi/internal/core/projects"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/shells"
	"github.com/notshekhar/pi/internal/core/tools"
	"github.com/notshekhar/pi/internal/rpc"
)

// `pi rpc` — loop's `loop rpc`: the JSON-RPC server on stdio that loop's
// desktop app spawns (LOOP_SPAWN_BINARY=1 LOOP_BINARY=<path to pi>). One
// process serves every project; sessions carry their own cwd.

// rpcHost is what the server needs from the program: runs built the way the
// TUI builds them, the MCP servers, and the extensions.
type rpcHost struct {
	mu  sync.Mutex
	mgr *mcp.Manager
	cwd string // what the manager last connected for
}

// newHeadlessRun is the TUI's run setup without a terminal.
func (h *rpcHost) newRun(sess *session.Session, modelID string, ask tools.Asker) (*agent.Run, error) {
	prov, model := config.SplitModel(modelID)
	cfg, err := config.Resolve(config.Config{Provider: prov, ModelID: model, CWD: sess.Info.CWD})
	if err != nil {
		return nil, err
	}
	stored := config.LoadSettings()
	if cfg.Reasoning == "" {
		cfg.Reasoning = stored.Reasoning()
	}
	if cfg.MaxSteps <= 0 || stored.MaxSteps > 0 {
		if stored.MaxSteps > 0 {
			cfg.MaxSteps = stored.MaxSteps
		}
	}
	tc := toolContext(cfg)
	tc.Ask = ask
	if ask == nil {
		tc.NoAsk = true
	}
	run := &agent.Run{Config: cfg, Session: sess, Tools: tc, Permissions: policyFor(cfg.CWD), Subagents: stored.SubagentsOn()}
	active := activeExtensions()
	run.ExtensionTools = extension.ToolsFrom(active, run.Tools)
	run.WrapTools = func(set []ai.Tool) []ai.Tool { return extension.WrapTools(activeExtensions(), set) }
	run.ExtensionPrompt = strings.TrimSpace(extension.PromptFrom(active, cfg.CWD))
	run.RewriteCall = func(tool string, args map[string]any) map[string]any {
		return extension.RewriteFrom(activeExtensions(), tool, args)
	}
	if hc, err := stored.HookConfig(); err == nil && len(hc[hooks.PreToolUse]) > 0 {
		run.PreTool = func(ctx context.Context, tool string, args map[string]any) (map[string]any, string) {
			out := hooks.Run(ctx, hc, hooks.Context{Event: hooks.PreToolUse, ToolName: tool, ToolInput: args,
				CWD: cfg.CWD, SessionID: sess.ID})
			if out.Block {
				return nil, out.Reason
			}
			if updated, ok := out.UpdatedInput.(map[string]any); ok && updated != nil {
				return updated, ""
			}
			return nil, ""
		}
	}
	// No approval prompt over RPC: an `ask` rule fails closed, as loop's does
	// headless. Agents and plan mode come from session.send's `agent`.
	if saved := agents.ResolveSaved(stored.Agent); saved != "" && saved != agents.DefaultAgentName && agents.Exists(saved) {
		run.Persona = agents.Prompt(saved)
	}
	h.ensureMCP(cfg.CWD)
	run.Extra = h.mgr.Tools()
	return run, nil
}

// ensureMCP connects the servers for cwd once; a client asks mcp.reconnect
// for anything fresher.
func (h *rpcHost) ensureMCP(cwd string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mgr != nil {
		return
	}
	h.mgr = mcp.NewManager()
	if !config.LoadSettings().MCPOn() {
		return
	}
	h.cwd = cwd
	h.mgr.Connect(context.Background(), config.ConnectableMCPServers(cwd, projects.IsTrusted(cwd)), cwd)
}

func (h *rpcHost) Live() []rpc.MCPLive {
	h.mu.Lock()
	mgr := h.mgr
	h.mu.Unlock()
	if mgr == nil {
		return nil
	}
	tools := mgr.Tools()
	var out []rpc.MCPLive
	for _, s := range mgr.Status() {
		l := rpc.MCPLive{Name: s.Name, ToolCount: s.Tools, Status: "ready"}
		switch {
		case s.Skipped:
			l.Status = "disabled"
		case s.Err != nil:
			l.Status, l.Error = "error", s.Err.Error()
		}
		prefix := s.Name + mcp.NameSeparator
		for _, t := range tools {
			if strings.HasPrefix(t.Name(), prefix) {
				l.Tools = append(l.Tools, t.Name())
			}
		}
		out = append(out, l)
	}
	return out
}

func (h *rpcHost) Connected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mgr != nil
}

func (h *rpcHost) Reconnect(ctx context.Context, cwd, _ string) {
	h.mu.Lock()
	if h.mgr != nil {
		h.mgr.Close()
	}
	h.mgr = mcp.NewManager()
	mgr := h.mgr
	h.cwd = cwd
	h.mu.Unlock()
	if config.LoadSettings().MCPOn() {
		mgr.Connect(ctx, config.ConnectableMCPServers(cwd, projects.IsTrusted(cwd)), cwd)
	}
}

func (h *rpcHost) Forget(string) {
	h.mu.Lock()
	cwd := h.cwd
	h.mu.Unlock()
	h.Reconnect(context.Background(), cwd, "")
}

func (h *rpcHost) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.mgr != nil {
		h.mgr.Close()
	}
}

// rpcExtensions is the extension list in loop's shape.
type rpcExtensions struct{}

func (rpcExtensions) List() []rpc.ExtensionEntry {
	state := config.ExtensionState()
	var out []rpc.ExtensionEntry
	for _, e := range extension.All() {
		out = append(out, rpc.ExtensionEntry{Name: e.Name(), DisplayName: e.Name(), Description: e.About(),
			Enabled: extension.IsEnabled(e, state), Builtin: true})
	}
	return out
}

func (rpcExtensions) SetEnabled(name string, on bool) error {
	return config.SetBuiltinEnabled(name, on)
}

// newRPCHost is the program's half of an RPC server (stdio or serve).
func newRPCHost(cwd string) (rpc.Host, *rpcHost) {
	host := &rpcHost{}
	return rpc.Host{
		NewRun: host.newRun,
		HasCommand: func(name string) bool {
			return lookupCommand(name) != nil
		},
		Reload: func(string) int {
			return len(commands())
		},
		Extensions: rpcExtensions{},
		MCP:        host,
		CWD:        cwd,
		Version:    version,
	}, host
}

// rpcCommand runs `pi rpc`.
func rpcCommand(cwd string) {
	h, host := newRPCHost(cwd)
	server := rpc.NewServer(h)
	config.ConfigureCatalog()
	go func() {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
		<-ch
		server.Dispose()
		host.close()
		shells.KillAll()
		db.Close()
		os.Exit(0)
	}()
	server.Serve(os.Stdin, os.Stdout)
	server.Dispose()
	host.close()
	shells.KillAll()
}

func init() {
	// Tools the enabled extensions add are valid in an agent's allowlist,
	// as loop's agentToolNames includes them.
	agents.ExtraToolNames = func() []string {
		var names []string
		for _, t := range extension.ToolsFrom(activeExtensions(), &tools.Context{Registry: tools.NewRegistry()}) {
			names = append(names, t.Name())
		}
		return names
	}
}
