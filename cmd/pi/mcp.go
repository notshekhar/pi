package main

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/mcp"
	"github.com/notshekhar/pi/internal/core/projects"
	"github.com/notshekhar/pi/internal/tui"
)

// MCP servers: connecting at startup, `/mcp` to manage them.
//
// The manager owns the connections and heals them on its own (probes,
// reconnect backoff, list_changed). Every run reads its tools from the
// manager when a turn starts, so a server that connects, drops or changes
// its tools is reflected on the next turn without anyone reassigning
// anything.

// mcpSource is the servers that may connect here: off entirely when MCP is
// switched off, and project servers only in a trusted folder.
func mcpSource(cwd string) func() []mcp.ServerConfig {
	return func() []mcp.ServerConfig {
		if !config.LoadSettings().MCPOn() {
			return nil
		}
		return config.ConnectableMCPServers(cwd, projects.IsTrusted(cwd))
	}
}

// startMCP brings up the configured servers in the background, so a slow
// server does not delay the first prompt.
func (t *repl) startMCP(ctx context.Context) {
	source := mcpSource(t.cfg.CWD)
	t.watchMCP()
	go func() {
		t.mcp.Init(ctx, t.cfg.CWD, source)
		status := t.mcp.Status()
		t.do(func() {
			th := t.app.Theme()
			if n, servers := len(t.mcp.Tools()), countState(status, mcp.StateReady); n > 0 {
				t.app.Print(th.Fg(tui.SlotDim, fmt.Sprintf("mcp: %d tool%s from %d server%s", n, plural(n), servers, plural(servers))))
			}
			// Reported once, here, rather than only inside /mcp: a server
			// that silently did not start is a tool the user thinks they have.
			for _, s := range status {
				switch s.State {
				case mcp.StateError:
					t.app.Print(th.Fg(tui.SlotWarning, "mcp "+s.Name+": "+s.Error))
				case mcp.StateNeedsAuth:
					t.app.Print(th.Fg(tui.SlotWarning, "mcp "+s.Name+": needs sign-in — /mcp login "+s.Name))
				}
			}
		})
	}()
}

// watchMCP reports a server that drops mid-session and one that comes
// back, once each — the manager heals quietly, and the user should still
// know their tools went away.
func (t *repl) watchMCP() {
	var mu sync.Mutex
	// The last settled state per server; "connecting" is a transition, not
	// news.
	stable := map[string]mcp.State{}
	t.mcp.OnChange(func() {
		mu.Lock()
		defer mu.Unlock()
		for _, s := range t.mcp.Status() {
			if s.State == mcp.StateConnecting {
				continue
			}
			prev, seen := stable[s.Name]
			stable[s.Name] = s.State
			switch {
			case !seen:
			case prev == mcp.StateReady && s.State == mcp.StateError:
				t.warnf("mcp %s: %s — reconnecting", s.Name, s.Error)
			case prev == mcp.StateError && s.State == mcp.StateReady:
				t.dim("mcp %s: reconnected · %d tool%s", s.Name, s.Tools, plural(s.Tools))
			}
		}
	})
}

func (t *repl) warnf(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	t.do(func() { t.app.Print(t.app.Theme().Fg(tui.SlotWarning, text)) })
}

func countState(status []mcp.Status, state mcp.State) int {
	n := 0
	for _, s := range status {
		if s.State == state {
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

// mcpCmd is /mcp and its subcommands.
func (t *repl) mcpCmd(rest string) {
	verb, arg, _ := strings.Cut(strings.TrimSpace(rest), " ")
	arg = strings.TrimSpace(arg)
	switch verb {
	case "":
		t.mcpPanel()
	case "list":
		t.showMCP()
	case "reconnect":
		t.reconnectMCP(arg)
	case "login", "auth", "authorize":
		if arg == "" {
			t.dim("usage: /mcp login <server>")
			return
		}
		t.loginMCP(arg)
	case "logout":
		if arg == "" {
			t.dim("usage: /mcp logout <server>")
			return
		}
		t.logoutMCP(arg)
	case "enable", "disable":
		if arg == "" {
			t.dim("usage: /mcp %s <server>", verb)
			return
		}
		t.setMCPEnabled(arg, verb == "enable")
	case "remove", "delete":
		if arg == "" {
			t.dim("usage: /mcp remove <server>")
			return
		}
		t.removeMCPServer(arg)
	case "add":
		t.addMCPServer()
	default:
		t.dim("usage: /mcp · /mcp list · /mcp add · /mcp reconnect [server] · /mcp login|logout|enable|disable|remove <server>")
	}
}

// configuredMCP is every server configured for this folder, project entries
// winning on a name, whether or not this folder is trusted to run them.
func (t *repl) configuredMCP() []mcp.ServerConfig {
	all := config.LoadMCPServers(t.cfg.CWD)
	out := make([]mcp.ServerConfig, 0, len(all))
	for name, c := range all {
		c.Name = name
		out = append(out, c)
	}
	sortServers(out)
	return out
}

func sortServers(s []mcp.ServerConfig) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].Name < s[j-1].Name; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

func (t *repl) mcpScope(name string) string { return config.MCPScope(t.cfg.CWD, name) }

// withheld reports a project server this folder is not trusted to run.
func (t *repl) withheld(name string) bool {
	return t.mcpScope(name) == "project" && !projects.IsTrusted(t.cfg.CWD)
}

// mcpPanel is `/mcp`: the server manager. It always offers "+ add server",
// so an empty configuration is not a dead end.
func (t *repl) mcpPanel() {
	const addServer = "\x00add"
	t.manage(func() (string, []tui.Item) {
		items := []tui.Item{{Value: addServer, Label: "+ add server", Description: "configure and connect a new MCP server"}}
		for _, cfg := range t.configuredMCP() {
			s, _ := t.mcp.Server(cfg.Name)
			items = append(items, tui.Item{
				Value:       cfg.Name,
				Label:       cfg.Name + " — " + t.mcpState(cfg, s),
				Description: mcpDetail(cfg, s),
			})
		}
		title := "MCP servers (type to filter, Esc to close)"
		if !config.LoadSettings().MCPOn() {
			title = "MCP servers — MCP is switched off in /settings"
		}
		return title, items
	}, func(choice tui.Item) bool {
		if choice.Value == addServer {
			t.addMCPServer()
			return keepPanel
		}
		t.mcpActions(choice.Value)
		return keepPanel
	})
}

// mcpState is a server's live state, which is not its config: a server can
// be configured, enabled, and failing.
func (t *repl) mcpState(cfg mcp.ServerConfig, s mcp.Status) string {
	switch {
	case cfg.Disabled():
		return "disabled"
	case t.withheld(cfg.Name):
		return "not trusted here"
	case s.Name == "":
		return "not connected"
	case s.State == mcp.StateReady:
		return fmt.Sprintf("%d tool%s", s.Tools, plural(s.Tools))
	case s.State == mcp.StateNeedsAuth:
		return "needs sign-in"
	}
	return string(s.State)
}

// mcpDetail is how the server is reached, or why it is not.
func mcpDetail(cfg mcp.ServerConfig, s mcp.Status) string {
	if s.Error != "" && s.State != mcp.StateReady {
		return s.Error
	}
	if cfg.URL != "" {
		return cfg.URL
	}
	return strings.TrimSpace(cfg.Command + " " + strings.Join(cfg.Args, " "))
}

// mcpActions is the per-server menu.
func (t *repl) mcpActions(name string) {
	var cfg mcp.ServerConfig
	found := false
	for _, c := range t.configuredMCP() {
		if c.Name == name {
			cfg, found = c, true
		}
	}
	if !found {
		return
	}
	s, _ := t.mcp.Server(name)
	var items []tui.Item
	if !cfg.Disabled() {
		items = append(items, tui.Item{Value: "reconnect", Label: "reconnect", Description: "restart this server's connection"})
	}
	if mcp.IsOAuthServer(name, cfg, s.State == mcp.StateNeedsAuth) || cfg.IsHTTP() {
		label, about := "sign in", "open the browser to authorize"
		if mcp.HasStoredTokens(name) {
			label, about = "sign in again", "replace the stored session"
		}
		items = append(items, tui.Item{Value: "login", Label: label, Description: about})
	}
	if mcp.HasStoredTokens(name) {
		items = append(items, tui.Item{Value: "logout", Label: "sign out", Description: "forget the stored OAuth session"})
	}
	if cfg.Disabled() {
		items = append(items, tui.Item{Value: "enable", Label: "enable", Description: "connect it again"})
	} else {
		items = append(items, tui.Item{Value: "disable", Label: "disable", Description: "keep it configured but unused"})
	}
	if s.Tools > 0 {
		items = append(items, tui.Item{Value: "tools", Label: "tools", Description: fmt.Sprintf("list its %d tools", s.Tools)})
	}
	items = append(items, tui.Item{Value: "delete", Label: "delete", Description: "remove it from " + t.mcpScope(name) + " settings"})
	action := t.choose(name, items...)
	if action == nil {
		return
	}
	switch action.Value {
	case "reconnect":
		t.reconnectMCP(name)
	case "login":
		t.loginMCP(name)
	case "logout":
		t.logoutMCP(name)
	case "enable", "disable":
		t.setMCPEnabled(name, action.Value == "enable")
	case "tools":
		t.showMCPTools(name)
	case "delete":
		if t.confirmRemove(name, "remove this server from "+t.mcpScope(name)+" settings") {
			t.removeMCPServer(name)
		}
	}
}

func (t *repl) reconnectMCP(name string) {
	if name != "" && t.withheld(name) {
		t.fail("%s is a project server and this folder is not trusted — /trust first", name)
		return
	}
	if name == "" {
		t.dim("reconnecting every server…")
	} else {
		t.dim("reconnecting %s…", name)
	}
	go func() {
		t.mcp.Reconnect(context.Background(), name)
		if name == "" {
			t.dim("mcp: %d tool%s from %d server%s", len(t.mcp.Tools()), plural(len(t.mcp.Tools())),
				countState(t.mcp.Status(), mcp.StateReady), plural(countState(t.mcp.Status(), mcp.StateReady)))
			return
		}
		t.reportMCP(name)
	}()
}

// reportMCP says how one server ended up after an action.
func (t *repl) reportMCP(name string) {
	s, ok := t.mcp.Server(name)
	switch {
	case !ok:
		t.dim("%s is not connected", name)
	case s.State == mcp.StateReady:
		t.dim("%s connected · %d tool%s", name, s.Tools, plural(s.Tools))
	case s.State == mcp.StateDisabled:
		t.dim("%s disabled", name)
	case s.State == mcp.StateNeedsAuth:
		t.fail("%s needs sign-in — /mcp login %s", name, name)
	default:
		t.fail("%s: %s", name, s.Error)
	}
}

func (t *repl) loginMCP(name string) {
	cfg, ok := t.mcpConfig(name)
	if !ok {
		t.fail("unknown MCP server: %s", name)
		return
	}
	if !cfg.IsHTTP() {
		t.fail("%s is a local (stdio) server — sign-in only applies to http/sse servers", name)
		return
	}
	t.dim("authorizing %s — finish in the browser…", name)
	go func() {
		err := t.mcp.Authorize(context.Background(), name, func(url string) {
			if !openBrowser(url) {
				t.dim("open this URL to sign in:")
			}
			t.dim("%s", url)
		})
		if err != nil {
			t.fail("mcp %s: %s", name, err)
			return
		}
		t.reportMCP(name)
	}()
}

func (t *repl) logoutMCP(name string) {
	go func() {
		t.mcp.Logout(context.Background(), name)
		t.dim("signed out of %s", name)
		t.reportMCP(name)
	}()
}

func (t *repl) mcpConfig(name string) (mcp.ServerConfig, bool) {
	for _, c := range t.configuredMCP() {
		if c.Name == name {
			return c, true
		}
	}
	return mcp.ServerConfig{}, false
}

// setMCPEnabled flips a server where it is defined and applies it at once:
// enabling connects it, disabling disconnects it and withdraws its tools.
func (t *repl) setMCPEnabled(name string, on bool) {
	if ok, err := config.SetMCPEnabledHere(t.cfg.CWD, name, on); err != nil || !ok {
		t.failMissing(name, err)
		return
	}
	if on {
		t.reconnectMCP(name)
		return
	}
	t.mcp.Reconnect(context.Background(), name) // re-syncs: closes it
	t.dim("%s disabled", name)
}

// removeMCPServer deletes the entry where it is defined, disconnects the
// server and forgets its OAuth session. A server also defined in the other
// scope is reconnected as that one instead.
func (t *repl) removeMCPServer(name string) {
	scope, ok, err := config.RemoveMCPHere(t.cfg.CWD, name)
	if err != nil || !ok {
		t.failMissing(name, err)
		return
	}
	if _, still := t.mcpConfig(name); still {
		t.dim("removed the %s entry for %s — the other one remains", scope, name)
		t.reconnectMCP(name)
		return
	}
	t.mcp.Remove(name)
	t.dim("removed %s", name)
}

func (t *repl) failMissing(name string, err error) {
	if err != nil {
		t.fail("mcp: %s", err)
		return
	}
	t.fail("unknown MCP server: %s", name)
}

// addMCPServer asks for what a server needs: a name, a transport, how to
// reach it, and where to keep it.
func (t *repl) addMCPServer() {
	name := strings.TrimSpace(t.ask("MCP server name (e.g. filesystem)", ""))
	if name == "" {
		return
	}
	if _, taken := t.mcpConfig(name); taken {
		t.fail("there is already a server named %q", name)
		return
	}
	transport := t.choose("Transport",
		tui.Item{Value: "stdio", Label: "stdio", Description: "run a local command and speak over its stdio"},
		tui.Item{Value: "http", Label: "http", Description: "streamable HTTP at a URL (falls back to SSE on its own)"},
		tui.Item{Value: "sse", Label: "sse", Description: "the older HTTP+SSE transport"},
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
		if reason := mcp.UnsupportedRemoteServer(url); reason != "" {
			t.fail("%s", reason)
			return
		}
		cfg.Type, cfg.URL = transport.Value, url
	}
	scope := t.choose("Save it for",
		tui.Item{Value: "global", Label: "every folder", Description: "~/.loop/settings.json"},
		tui.Item{Value: "project", Label: "this project", Description: ".loop/mcp.json here"},
	)
	if scope == nil {
		return
	}
	var err error
	if scope.Value == "project" {
		err = config.AddProjectMCPServer(t.cfg.CWD, name, cfg)
	} else {
		err = config.AddMCPServer(name, cfg)
	}
	if err != nil {
		t.fail("mcp: %s", err)
		return
	}
	t.dim("added %s", name)
	t.reconnectMCP(name)
}

func (t *repl) showMCPTools(name string) {
	s, ok := t.mcp.Server(name)
	if !ok || len(s.ToolNames) == 0 {
		t.dim("%s has no tools connected", name)
		return
	}
	t.do(func() {
		th := t.app.Theme()
		lines := []string{th.Fg(tui.SlotDim, name+" tools")}
		for _, n := range s.ToolNames {
			lines = append(lines, "  "+th.Fg(tui.SlotMuted, n))
		}
		t.app.Print(lines...)
	})
}

func (t *repl) showMCP() {
	configured := t.configuredMCP()
	t.do(func() {
		th := t.app.Theme()
		if len(configured) == 0 {
			t.app.Print(
				th.Fg(tui.SlotDim, "no mcp servers configured — /mcp add, or under \"mcpServers\" in "+config.SettingsPath()),
				"",
				th.Fg(tui.SlotDim, `  "mcpServers": {`),
				th.Fg(tui.SlotDim, `    "github": { "command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"] },`),
				th.Fg(tui.SlotDim, `    "docs":   { "type": "http", "url": "https://example.com/mcp" }`),
				th.Fg(tui.SlotDim, `  }`))
			return
		}
		var lines []string
		for _, cfg := range configured {
			s, _ := t.mcp.Server(cfg.Name)
			state, slot := t.mcpState(cfg, s), tui.SlotDim
			switch s.State {
			case mcp.StateReady:
				slot = tui.SlotSuccess
				if s.Info.Name != "" {
					state += " · " + s.Info.Name
				}
				if s.ProtocolVersion != "" {
					state += " · " + s.ProtocolVersion
				}
			case mcp.StateError:
				state, slot = s.Error, tui.SlotError
			case mcp.StateNeedsAuth:
				slot = tui.SlotWarning
			}
			lines = append(lines, th.Fg(tui.SlotAccent, padRight(cfg.Name, 16))+th.Fg(slot, state))
		}
		lines = append(lines, "", th.Fg(tui.SlotDim, "/mcp to manage · /mcp reconnect [server]"))
		t.app.Print(lines...)
	})
}
