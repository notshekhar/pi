package main

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/jsonstore"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// `pi mcp …` — loop's mcp-commands.ts: manage MCP servers from the shell,
// over the same settings.json and .loop/mcp.json the TUI's /mcp edits.

// mcpCommand dispatches the words after `mcp`.
func mcpCommand(cwd string, argv []string) int {
	sub, rest := "", []string(nil)
	if len(argv) > 0 {
		sub, rest = argv[0], argv[1:]
	}
	var err error
	switch sub {
	case "add":
		err = mcpAdd(cwd, rest)
	case "add-json":
		err = mcpAddJSON(cwd, rest)
	case "list", "ls":
		mcpList(cwd)
	case "get":
		err = mcpGet(cwd, rest)
	case "remove", "rm", "delete":
		err = mcpRemove(cwd, rest)
	case "enable", "disable":
		err = mcpSetEnabled(cwd, rest, sub == "enable")
	case "login", "authorize":
		err = mcpLogin(cwd, rest)
	case "", "help", "--help", "-h":
		printMCPHelp()
	default:
		fmt.Fprintf(os.Stderr, "unknown mcp subcommand: %q\n\n", sub)
		printMCPHelp()
		return 1
	}
	if err != nil {
		// A usage error or a refused server is an answer, not a crash.
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}

// scopedServer is one configured server and the file it lives in.
type scopedServer struct {
	name  string
	cfg   mcp.ServerConfig
	scope mcpScope
}

// allMCPServers is every configured server across both scopes, user first,
// each in the order its file lists them — loop prints file order.
func allMCPServers(cwd string) []scopedServer {
	var out []scopedServer
	for _, src := range []struct {
		scope   mcpScope
		servers map[string]mcp.ServerConfig
		order   []string
	}{
		{scopeUser, config.GlobalMCPServers(), keyOrder(config.SettingsStore().Raw("mcpServers"))},
		{scopeProject, config.ProjectMCPServers(cwd), projectServerOrder(cwd)},
	} {
		for _, name := range orderedNames(src.servers, src.order) {
			out = append(out, scopedServer{name, src.servers[name], src.scope})
		}
	}
	return out
}

// keyOrder is the keys of a JSON object as written; nil for anything else.
func keyOrder(raw jsontext.Value) []string {
	if raw == nil {
		return nil
	}
	o, err := jsonstore.ParseObject(raw)
	if err != nil {
		return nil
	}
	return o.Keys()
}

// projectServerOrder is the server order in <cwd>/.loop/mcp.json, which is
// either {"mcpServers": {…}} or the bare map.
func projectServerOrder(cwd string) []string {
	data, err := os.ReadFile(config.ProjectMCPPath(cwd))
	if err != nil {
		return nil
	}
	o, err := jsonstore.ParseObject(data)
	if err != nil {
		return nil
	}
	if wrapped := o.Raw("mcpServers"); wrapped != nil {
		return keyOrder(wrapped)
	}
	return o.Keys()
}

// orderedNames is the map's names in the given order, then any the order
// missed sorted, so nothing configured is ever left out of a listing.
func orderedNames(servers map[string]mcp.ServerConfig, order []string) []string {
	var names, rest []string
	listed := map[string]bool{}
	for _, name := range order {
		if _, ok := servers[name]; ok && !listed[name] {
			names, listed[name] = append(names, name), true
		}
	}
	for name := range servers {
		if !listed[name] {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	return append(names, rest...)
}

// describeTarget is the one-line summary of how a server is reached.
func describeTarget(cfg mcp.ServerConfig) string {
	if cfg.IsHTTP() {
		auth := ""
		switch {
		case cfg.Auth == "oauth":
			auth = " (oauth)"
		case cfg.Headers != nil:
			auth = " (header auth)"
		}
		kind := cfg.Type
		if kind == "" {
			kind = "http"
		}
		return kind + " " + cfg.URL + auth
	}
	return strings.TrimSpace("stdio " + cfg.Command + " " + strings.Join(cfg.Args, " "))
}

// refuseUnsupported stops before writing an entry that can never sign in: an
// "Added" followed by a refused sign-in every time leaves a row that looks
// like configuration and behaves like a fault.
func refuseUnsupported(cfg mcp.ServerConfig) error {
	if !cfg.IsHTTP() {
		return nil
	}
	if reason := mcp.UnsupportedRemoteServer(cfg.URL); reason != "" {
		return errors.New(reason)
	}
	return nil
}

func saveMCPServer(cwd, name string, cfg mcp.ServerConfig, scope mcpScope) error {
	if scope == scopeProject {
		return config.AddProjectMCPServer(cwd, name, cfg)
	}
	return config.AddMCPServer(name, cfg)
}

func mcpAdd(cwd string, args []string) error {
	spec, err := buildAddConfig(args)
	if err != nil {
		return err
	}
	if err := refuseUnsupported(spec.cfg); err != nil {
		return err
	}
	if _, exists := config.LoadMCPServers(cwd)[spec.name]; exists {
		fmt.Printf("Note: replacing existing MCP server %q.\n", spec.name)
	}
	if err := saveMCPServer(cwd, spec.name, spec.cfg, spec.scope); err != nil {
		return err
	}
	where := "~/" + home.DirName + "/settings.json"
	if spec.scope == scopeProject {
		where = config.ProjectMCPPath(cwd)
	}
	fmt.Printf("Added MCP server %q — %s  [scope: %s]\n", spec.name, describeTarget(spec.cfg), spec.scope)
	fmt.Printf("  written to %s\n", where)
	if spec.cfg.IsHTTP() && spec.cfg.Auth == "oauth" {
		fmt.Printf("\n  This server uses OAuth. Sign in with:\n    %s mcp login %s\n", productName, spec.name)
	}
	return nil
}

func mcpAddJSON(cwd string, args []string) error {
	pos := mcpPositionals(args)
	if len(pos) < 2 {
		return usageErrorf("usage: %s mcp add-json <name> '<json>' [--scope user|project]", productName)
	}
	name, raw := pos[0], pos[1]
	var probe map[string]jsontext.Value
	if err := json.Unmarshal([]byte(raw), &probe, jsonopt.Read); err != nil {
		if _, isSyntax := errors.AsType[*jsontext.SyntacticError](err); isSyntax {
			return usageErrorf("invalid JSON: %s", err)
		}
		return usageErrorf("JSON must be a server config object")
	}
	_, hasURL := probe["url"]
	_, hasCommand := probe["command"]
	var cfg mcp.ServerConfig
	if err := json.Unmarshal([]byte(raw), &cfg, jsonopt.Read); err != nil {
		return usageErrorf("invalid JSON: %s", err)
	}
	if err := refuseUnsupported(cfg); err != nil {
		return err
	}
	if !hasURL && !hasCommand {
		return usageErrorf(`config needs either "url" (http/sse) or "command" (stdio)`)
	}
	scope, err := scopeFlag(args)
	if err != nil {
		return err
	}
	if scope == "" {
		scope = scopeUser
	}
	if err := saveMCPServer(cwd, name, cfg, scope); err != nil {
		return err
	}
	fmt.Printf("Added MCP server %q — %s  [scope: %s]\n", name, describeTarget(cfg), scope)
	return nil
}

func mcpList(cwd string) {
	servers := allMCPServers(cwd)
	if len(servers) == 0 {
		fmt.Printf("No MCP servers configured.\nAdd one with:  %s mcp add --transport http <name> <url>\n", productName)
		return
	}
	for _, s := range servers {
		disabled := ""
		if s.cfg.Disabled() {
			disabled = "  (disabled)"
		}
		fmt.Printf("%s  —  %s  [%s]%s\n", s.name, describeTarget(s.cfg), s.scope, disabled)
	}
}

func mcpGet(cwd string, args []string) error {
	name := firstMCPPositional(args)
	if name == "" {
		return usageErrorf("usage: %s mcp get <name>", productName)
	}
	for _, s := range allMCPServers(cwd) {
		if s.name != name {
			continue
		}
		// Redacted: this lands in scrollback, screen shares and bug reports.
		fmt.Printf("%s  [scope: %s]\n%s\n", name, s.scope, describeTarget(s.cfg))
		// v2 never HTML-escapes, so "<redacted>" prints as written.
		enc := jsontext.NewEncoder(os.Stdout, jsontext.WithIndent("  "))
		return json.MarshalEncode(enc, redactServerConfig(s.cfg), jsonopt.Write)
	}
	return usageErrorf("no MCP server named %q", name)
}

func mcpRemove(cwd string, args []string) error {
	name := firstMCPPositional(args)
	if name == "" {
		return usageErrorf("usage: %s mcp remove <name> [--scope user|project]", productName)
	}
	wanted, err := scopeFlag(args)
	if err != nil {
		return err
	}
	removed, err := removeFromScope(cwd, name, wanted)
	if err != nil {
		return err
	}
	if removed == "" {
		where := ""
		if wanted != "" {
			where = " in scope " + string(wanted)
		}
		return usageErrorf("no MCP server named %q%s", name, where)
	}
	// Forget the OAuth session only once nothing of that name is configured
	// anywhere: removing the project copy must not sign the user copy out.
	if _, still := config.LoadMCPServers(cwd)[name]; !still {
		_ = mcp.ClearAuth(name)
	}
	fmt.Printf("Removed MCP server %q [scope: %s]\n", name, removed)
	return nil
}

// removeFromScope deletes the entry from the wanted scope, or the first one
// that has it (user, then project); "" when none did.
func removeFromScope(cwd, name string, wanted mcpScope) (mcpScope, error) {
	if wanted != scopeProject {
		if ok, err := config.RemoveMCPServer(name); err != nil || ok {
			return scopeUser, err
		}
	}
	if wanted != scopeUser {
		if ok, err := config.RemoveProjectMCPServer(cwd, name); err != nil || ok {
			return scopeProject, err
		}
	}
	return "", nil
}

func mcpSetEnabled(cwd string, args []string, on bool) error {
	verb, done := "disable", "Disabled"
	if on {
		verb, done = "enable", "Enabled"
	}
	name := firstMCPPositional(args)
	if name == "" {
		return usageErrorf("usage: %s mcp %s <name>", productName, verb)
	}
	wanted, err := scopeFlag(args)
	if err != nil {
		return err
	}
	ok := false
	if wanted != scopeProject {
		if ok, err = config.SetMCPServerEnabled(name, on); err != nil {
			return err
		}
	}
	if !ok && wanted != scopeUser {
		if ok, err = config.SetProjectMCPServerEnabled(cwd, name, on); err != nil {
			return err
		}
	}
	if !ok {
		return usageErrorf("no MCP server named %q", name)
	}
	fmt.Printf("%s MCP server %q\n", done, name)
	return nil
}

func mcpLogin(cwd string, args []string) error {
	name := firstMCPPositional(args)
	if name == "" {
		return usageErrorf("usage: %s mcp login <name>", productName)
	}
	cfg, ok := config.LoadMCPServers(cwd)[name]
	if !ok {
		return usageErrorf("no MCP server named %q", name)
	}
	if !cfg.IsHTTP() {
		return usageErrorf("%q is a stdio server — OAuth only applies to http/sse", name)
	}
	// An entry added by hand or an older build still gets the answer before
	// a browser opens on a doomed consent screen.
	if err := refuseUnsupported(cfg); err != nil {
		return err
	}
	cfg.Name = name
	fmt.Printf("Authorizing %q…\n", name)
	err := mcp.Authorize(context.Background(), name, cfg, func(url string) {
		fmt.Printf("\nOpening your browser to:\n  %s\n\n", url)
		openBrowser(url)
	})
	if err != nil {
		return err
	}
	fmt.Printf("Authorized %q. It will connect on next launch.\n", name)
	return nil
}

// secretKey matches header and env names whose value is a credential.
var secretKey = regexp.MustCompile(`(?i)authorization|auth[-_]?token|api[-_]?key|access[-_]?key|secret|password|cookie|token`)

// envReference is a ${env:VAR} placeholder: it names a secret rather than
// revealing one, and hiding it would obscure that the secret is kept out of
// the file properly.
var envReference = regexp.MustCompile(`^\s*\$\{env:[A-Za-z_][A-Za-z0-9_]*\}\s*$`)

const redacted = "<redacted>"

// redactServerConfig masks a server's credentials for anything that prints it.
func redactServerConfig(cfg mcp.ServerConfig) mcp.ServerConfig {
	if cfg.IsHTTP() {
		cfg.Headers = redactMap(cfg.Headers)
		if cfg.ClientSecret != "" && !envReference.MatchString(cfg.ClientSecret) {
			cfg.ClientSecret = redacted
		}
		return cfg
	}
	cfg.Env = redactMap(cfg.Env)
	return cfg
}

func redactMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if secretKey.MatchString(k) && !envReference.MatchString(v) {
			v = redacted
		}
		out[k] = v
	}
	return out
}

func printMCPHelp() {
	fmt.Printf(`%[1]s mcp — manage MCP (Model Context Protocol) servers

Usage:
  %[1]s mcp add [options] <name> <url>            add an http/sse server
  %[1]s mcp add [options] <name> -- <cmd> [args]  add a stdio (local) server
  %[1]s mcp add-json <name> '<json>'              add from a raw JSON config
  %[1]s mcp list                                  list configured servers
  %[1]s mcp get <name>                            show one server's config
  %[1]s mcp remove <name>                         remove a server
  %[1]s mcp enable|disable <name>                 toggle a server
  %[1]s mcp login <name>                          run the OAuth browser sign-in

Options for add:
  --transport, -t <stdio|http|sse>   transport (default: stdio)
  --scope, -s <user|project>         where to store it (default: user)
  --header, -H "Name: Value"         static auth header (repeatable, http/sse)
  --env, -e KEY=VALUE                environment variable (repeatable, stdio)
  --oauth                            use OAuth sign-in (http/sse)
  --client-id <id>                   pre-registered OAuth client id
  --client-secret <secret>           OAuth client secret (supports ${env:VAR})
  --oauth-scopes a,b,c               OAuth scopes to request

Examples:
  %[1]s mcp add --transport http docs https://code.claude.com/docs/mcp
  %[1]s mcp add --transport http figma https://mcp.figma.com/mcp --oauth
  %[1]s mcp add --transport sse linear https://mcp.linear.app/sse \
    --header "Authorization: Bearer ${env:LINEAR_TOKEN}"
  %[1]s mcp add fs -- npx -y @modelcontextprotocol/server-filesystem ~/code

Tokens: headers and ${env:VAR} placeholders resolve from your environment at
connect time, so secrets stay out of the config file.
`, productName)
}
