package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/mcp"
	"github.com/notshekhar/pi/internal/modules/tui"
)

// MCP servers: connecting at startup, and `/mcp` to see what happened.

// startMCP brings up the configured servers and hands their tools to the run.
//
// Runs in the background so a slow server does not delay the first prompt.
// Until it finishes the agent simply has fewer tools, which is a far better
// failure than a session that will not start.
func (t *repl) startMCP(ctx context.Context) {
	stored := config.LoadSettings()
	// The switch is checked BEFORE connecting, not after: connecting spawns
	// the servers' processes, and an off switch that still spawns them has
	// not turned anything off.
	if !stored.MCPOn() {
		return
	}
	servers := stored.MCPList()
	if len(servers) == 0 {
		return
	}
	go func() {
		t.mcp.Connect(ctx, servers, t.cfg.CWD)
		tools := t.mcp.Tools()

		t.app.Do(func() {
			th := t.app.Theme()
			var failed []mcp.Status
			for _, s := range t.mcp.Status() {
				if s.Err != nil {
					failed = append(failed, s)
				}
			}
			if len(tools) > 0 {
				t.app.Print(th.Fg(tui.SlotDim, fmt.Sprintf(
					"mcp: %d tool%s from %d server%s",
					len(tools), plural(len(tools)),
					connected(t.mcp.Status()), plural(connected(t.mcp.Status())))))
			}
			// Failures are reported once, here, rather than only inside
			// /mcp — a server that silently did not start is a tool the user
			// thinks they have.
			for _, s := range failed {
				t.app.Print(th.Fg(tui.SlotWarning, "mcp "+s.Name+": "+s.Err.Error()))
			}
		})

		// The tool set is read when a turn starts, so assigning between turns
		// is enough; a turn already running keeps the set it began with.
		t.mu.Lock()
		t.run.Extra = tools
		t.mu.Unlock()
	}()
}

func connected(status []mcp.Status) int {
	n := 0
	for _, s := range status {
		if s.Err == nil && !s.Skipped {
			n++
		}
	}
	return n
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// mcpCmd reports server status, or reconnects.
func (t *repl) mcpCmd(rest string) {
	switch strings.TrimSpace(rest) {
	case "":
		t.mcpPanel()
	case "list":
		t.showMCP()
	case "reconnect":
		if t.busy() {
			return
		}
		t.mcp.Close()
		t.mu.Lock()
		t.run.Extra = nil
		t.mu.Unlock()
		t.dim("reconnecting…")
		t.startMCP(context.Background())
	default:
		t.dim("usage: /mcp · /mcp list · /mcp reconnect")
	}
}

// mcpPanel is `/mcp` with no argument: the server manager.
//
// A panel rather than a listing, and it always offers "+ add server" — an
// empty configuration was otherwise a dead end that told you which file to go
// and edit by hand.
func (t *repl) mcpPanel() {
	const addServer = "\x00add"
	t.manage(func() (string, []tui.Item) {
		configured := config.LoadSettings().MCPList()
		status := map[string]mcp.Status{}
		for _, s := range t.mcp.Status() {
			status[s.Name] = s
		}
		items := []tui.Item{{
			Value:       addServer,
			Label:       "+ add server",
			Description: "configure and connect a new MCP server",
		}}
		for _, cfg := range configured {
			items = append(items, tui.Item{
				Value:       cfg.Name,
				Label:       cfg.Name + " — " + mcpState(cfg, status[cfg.Name]),
				Description: mcpDetail(cfg),
			})
		}
		return "MCP servers (type to filter, Esc to close)", items
	}, func(choice tui.Item) bool {
		if choice.Value == addServer {
			t.addMCPServer()
			return keepPanel
		}
		t.mcpActions(choice.Value)
		return keepPanel
	})
}

// mcpState is a server's live state, which is not the same as its config: a
// server can be configured, enabled, and failing.
func mcpState(cfg mcp.ServerConfig, s mcp.Status) string {
	switch {
	case cfg.Disabled:
		return "disabled"
	case s.Err != nil:
		return "error"
	case s.Name == "":
		return "not connected"
	case s.Tools > 0:
		return fmt.Sprintf("%d tools", s.Tools)
	}
	return "ready"
}

// mcpDetail is how the server is reached, or why it is not.
func mcpDetail(cfg mcp.ServerConfig) string {
	if cfg.URL != "" {
		return cfg.URL
	}
	return strings.TrimSpace(cfg.Command + " " + strings.Join(cfg.Args, " "))
}

// mcpActions is the per-server menu.
func (t *repl) mcpActions(name string) {
	stored := config.LoadSettings().MCPServers[name]
	toggle, about := "disable", "keep it configured but unused"
	if stored.Disabled {
		toggle, about = "enable", "connect it again"
	}
	action := t.choose(name,
		tui.Item{Value: "reconnect", Label: "reconnect", Description: "retry the connection"},
		tui.Item{Value: toggle, Label: toggle, Description: about},
		tui.Item{Value: "delete", Label: "delete", Description: "remove it from settings"},
	)
	if action == nil {
		return
	}
	switch action.Value {
	case "reconnect":
		t.mcpCmd("reconnect")
	case "enable", "disable":
		t.setMCPEnabled(name, action.Value == "enable")
	case "delete":
		if t.confirmRemove(name, "remove this server from settings") {
			t.removeMCPServer(name)
		}
	}
}

func (t *repl) setMCPEnabled(name string, on bool) {
	if err := config.Update(func(s *config.Settings) {
		cfg := s.MCPServers[name]
		cfg.Disabled = !on
		s.MCPServers[name] = cfg
	}); err != nil {
		t.fail("mcp: %s", err)
		return
	}
	state := "disabled"
	if on {
		state = "enabled"
	}
	t.dim("%s %s — /mcp reconnect to apply", name, state)
}

func (t *repl) removeMCPServer(name string) {
	if err := config.Update(func(s *config.Settings) { delete(s.MCPServers, name) }); err != nil {
		t.fail("mcp: %s", err)
		return
	}
	t.dim("removed %s", name)
}

// addMCPServer asks for the three things a server needs: a name, a transport,
// and how to reach it.
func (t *repl) addMCPServer() {
	name := strings.TrimSpace(t.ask("MCP server name (e.g. filesystem)", ""))
	if name == "" {
		return
	}
	if _, taken := config.LoadSettings().MCPServers[name]; taken {
		t.fail("there is already a server named %q", name)
		return
	}

	transport := t.choose("Transport",
		tui.Item{Value: "stdio", Label: "stdio", Description: "run a local command and speak over its stdio"},
		tui.Item{Value: "http", Label: "http", Description: "connect to a URL"},
	)
	if transport == nil {
		return
	}

	cfg := mcp.ServerConfig{Name: name}
	if transport.Value == "stdio" {
		command := strings.TrimSpace(t.ask("command (e.g. npx)", ""))
		if command == "" {
			return
		}
		cfg.Command = command
		if args := strings.TrimSpace(t.ask("args (space-separated, optional)", "")); args != "" {
			cfg.Args = strings.Fields(args)
		}
	} else {
		url := strings.TrimSpace(t.ask("url (https://…)", ""))
		if url == "" {
			return
		}
		cfg.URL = url
	}

	if err := config.Update(func(s *config.Settings) {
		if s.MCPServers == nil {
			s.MCPServers = map[string]mcp.ServerConfig{}
		}
		s.MCPServers[name] = cfg
	}); err != nil {
		t.fail("mcp: %s", err)
		return
	}
	t.dim("added %s — connecting…", name)
	t.mcpCmd("reconnect")
}

func (t *repl) showMCP() {
	configured := config.LoadSettings().MCPList()
	status := t.mcp.Status()
	tools := t.mcp.Tools()

	t.app.Do(func() {
		th := t.app.Theme()
		if len(configured) == 0 {
			path, _ := config.SettingsPath()
			t.app.Print(
				th.Fg(tui.SlotDim, "no mcp servers configured"),
				th.Fg(tui.SlotDim, "add them under \"mcpServers\" in "+path),
				"",
				th.Fg(tui.SlotDim, `  "mcpServers": {`),
				th.Fg(tui.SlotDim, `    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] },`),
				th.Fg(tui.SlotDim, `    "docs":   { "url": "https://example.com/mcp" }`),
				th.Fg(tui.SlotDim, `  }`))
			return
		}

		byName := map[string]mcp.Status{}
		for _, s := range status {
			byName[s.Name] = s
		}

		var lines []string
		for _, cfg := range configured {
			s, seen := byName[cfg.Name]
			state, slot := "connecting…", tui.SlotDim
			switch {
			case cfg.Disabled:
				state, slot = "disabled", tui.SlotDim
			case seen && s.Err != nil:
				state, slot = s.Err.Error(), tui.SlotError
			case seen:
				state, slot = fmt.Sprintf("%d tools · %s", s.Tools, s.Info.Name), tui.SlotSuccess
			}
			lines = append(lines,
				th.Fg(tui.SlotAccent, padRight(cfg.Name, 16))+th.Fg(slot, state))
		}
		if len(tools) > 0 {
			lines = append(lines, "", th.Fg(tui.SlotDim, "tools"))
			for _, tool := range tools {
				lines = append(lines, "  "+th.Fg(tui.SlotMuted, tool.Name()))
			}
		}
		lines = append(lines, "", th.Fg(tui.SlotDim, "/mcp reconnect to restart them"))
		t.app.Print(lines...)
	})
}
