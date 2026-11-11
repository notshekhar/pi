package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/notshekhar/pi/internal/core/mcp"
)

// Argument → config parsing for `pi mcp add`, loop's mcp-add-parse.ts, in
// the Claude Code / Codex CLI shape:
//
//	pi mcp add --transport http <name> <url>
//	pi mcp add --transport sse  <name> <url> --header "Authorization: Bearer x"
//	pi mcp add                  <name> -- <command> [args...]
//	pi mcp add                  <name> <command> [args...]

// mcpScope is where a server entry lives: settings.json ("user") or
// <cwd>/.loop/mcp.json ("project").
type mcpScope string

const (
	scopeUser    mcpScope = "user"
	scopeProject mcpScope = "project"
)

// mcpUsageError is a bad invocation; the handler prints it without a stack.
type mcpUsageError struct{ msg string }

func (e mcpUsageError) Error() string { return e.msg }

func usageErrorf(format string, a ...any) error { return mcpUsageError{fmt.Sprintf(format, a...)} }

var serverNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// mcpAddSpec is a parsed `mcp add`.
type mcpAddSpec struct {
	name  string
	cfg   mcp.ServerConfig
	scope mcpScope
}

// parseScope accepts loop's vocabulary, with the other tools' words for the
// same two places.
func parseScope(raw string) (mcpScope, error) {
	switch raw {
	case "user", "global":
		return scopeUser, nil
	case "project", "local":
		return scopeProject, nil
	}
	return "", usageErrorf(`invalid --scope "%s" (use "user" or "project")`, raw)
}

// mcpValueFlags swallow the next word in the simple subcommands.
var mcpValueFlags = map[string]bool{"--scope": true, "-s": true}

// mcpPositionals is every non-flag word, with flag VALUES skipped: without
// that, `mcp remove --scope project api` read "project" as the name.
func mcpPositionals(args []string) []string {
	var out []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--":
			return append(out, args[i+1:]...)
		case mcpValueFlags[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			out = append(out, a)
		}
	}
	return out
}

// firstMCPPositional is the server name of a simple subcommand.
func firstMCPPositional(args []string) string {
	if p := mcpPositionals(args); len(p) > 0 {
		return p[0]
	}
	return ""
}

// scopeFlag is an optional --scope; "" when absent.
func scopeFlag(args []string) (mcpScope, error) {
	for i, a := range args {
		if mcpValueFlags[a] && i+1 < len(args) {
			return parseScope(args[i+1])
		}
		if v, ok := strings.CutPrefix(a, "--scope="); ok {
			return parseScope(v)
		}
	}
	return "", nil
}

// addFlags is what `mcp add` collected from its flags.
type addFlags struct {
	transport, clientID, clientSecret string
	scope                             mcpScope
	oauth                             bool
	scopes                            []string
	headers, env                      map[string]string
	positional                        []string
}

// normalizeEquals expands --key=value into two words so the walker handles
// one form.
func normalizeEquals(head []string) []string {
	var out []string
	for _, a := range head {
		if k, v, ok := strings.Cut(a, "="); ok && strings.HasPrefix(a, "--") {
			out = append(out, k, v)
			continue
		}
		out = append(out, a)
	}
	return out
}

func splitPair(raw, sep, flag, shape, emptyWhat string) (string, string, error) {
	k, v, ok := strings.Cut(raw, sep)
	if !ok {
		return "", "", usageErrorf(`invalid %s "%s" (expected "%s")`, flag, raw, shape)
	}
	if k = strings.TrimSpace(k); k == "" {
		return "", "", usageErrorf(`invalid %s "%s" (empty %s)`, flag, raw, emptyWhat)
	}
	return k, v, nil
}

func parseAddFlags(head []string) (addFlags, error) {
	f := addFlags{scope: scopeUser, headers: map[string]string{}, env: map[string]string{}}
	for i := 0; i < len(head); i++ {
		a := head[i]
		take := func() (string, error) {
			if i+1 >= len(head) {
				return "", usageErrorf("missing value for %s", a)
			}
			i++
			return head[i], nil
		}
		var v string
		var err error
		switch a {
		case "--header", "-H", "--env", "-e", "--transport", "-t", "--scope", "-s",
			"--client-id", "--client-secret", "--oauth-scopes":
			if v, err = take(); err != nil {
				return f, err
			}
		}
		switch a {
		case "--header", "-H":
			k, val, err := splitPair(v, ":", "--header", "Name: Value", "header name")
			if err != nil {
				return f, err
			}
			f.headers[k] = strings.TrimSpace(val)
		case "--env", "-e":
			k, val, err := splitPair(v, "=", "--env", "KEY=VALUE", "key")
			if err != nil {
				return f, err
			}
			f.env[k] = val
		case "--transport", "-t":
			f.transport = strings.ToLower(v)
		case "--scope", "-s":
			if f.scope, err = parseScope(v); err != nil {
				return f, err
			}
		case "--oauth":
			f.oauth = true
		case "--client-id":
			f.clientID = v
		case "--client-secret":
			f.clientSecret = v
		case "--oauth-scopes":
			for _, s := range strings.Split(v, ",") {
				if s = strings.TrimSpace(s); s != "" {
					f.scopes = append(f.scopes, s)
				}
			}
		default:
			if strings.HasPrefix(a, "-") {
				return f, usageErrorf("unknown flag: %s", a)
			}
			f.positional = append(f.positional, a)
		}
	}
	return f, nil
}

// buildAddConfig turns the words after `mcp add` into a server and scope.
func buildAddConfig(args []string) (mcpAddSpec, error) {
	head, tail := args, []string(nil)
	for i, a := range args {
		if a == "--" {
			head, tail = args[:i], args[i+1:]
			break
		}
	}
	f, err := parseAddFlags(normalizeEquals(head))
	if err != nil {
		return mcpAddSpec{}, err
	}
	if len(f.positional) == 0 {
		return mcpAddSpec{}, usageErrorf("a server name is required")
	}
	name := f.positional[0]
	if !serverNameRE.MatchString(name) {
		return mcpAddSpec{}, usageErrorf(`invalid server name "%s" (use letters, digits, "-" or "_")`, name)
	}
	transport := f.transport
	if transport == "" {
		transport = "stdio"
	}
	var cfg mcp.ServerConfig
	switch transport {
	case "http", "sse":
		cfg, err = remoteServer(transport, f, tail)
	case "stdio":
		cfg, err = stdioServer(f, tail)
	default:
		err = usageErrorf(`unknown transport "%s" (use "stdio", "http" or "sse")`, f.transport)
	}
	return mcpAddSpec{name: name, cfg: cfg, scope: f.scope}, err
}

func remoteServer(transport string, f addFlags, tail []string) (mcp.ServerConfig, error) {
	if len(f.positional) < 2 {
		return mcp.ServerConfig{}, usageErrorf("a URL is required for %s transport", transport)
	}
	url := f.positional[1]
	lower := strings.ToLower(url)
	switch {
	case !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://"):
		return mcp.ServerConfig{}, usageErrorf(`invalid URL "%s" (must start with http:// or https://)`, url)
	case len(tail) > 0:
		return mcp.ServerConfig{}, usageErrorf("`--` command arguments only apply to stdio transport")
	case len(f.env) > 0:
		return mcp.ServerConfig{}, usageErrorf("--env only applies to stdio transport")
	}
	cfg := mcp.ServerConfig{Type: transport, URL: url, ClientID: f.clientID, ClientSecret: f.clientSecret, Scopes: f.scopes}
	if len(f.headers) > 0 {
		cfg.Headers = f.headers
	}
	if f.oauth {
		cfg.Auth = "oauth"
	}
	return cfg, nil
}

func stdioServer(f addFlags, tail []string) (mcp.ServerConfig, error) {
	var command string
	var args []string
	switch {
	case len(tail) > 0:
		command, args = tail[0], tail[1:]
	case len(f.positional) > 1:
		command, args = f.positional[1], f.positional[2:]
	default:
		return mcp.ServerConfig{}, usageErrorf("a command is required for stdio transport (e.g. `%s mcp add my-server -- npx -y @scope/pkg`)", productName)
	}
	switch {
	case len(f.headers) > 0:
		return mcp.ServerConfig{}, usageErrorf("--header only applies to http/sse transport")
	case f.oauth:
		return mcp.ServerConfig{}, usageErrorf("--oauth only applies to http/sse transport")
	}
	cfg := mcp.ServerConfig{Command: command}
	if len(args) > 0 {
		cfg.Args = args
	}
	if len(f.env) > 0 {
		cfg.Env = f.env
	}
	return cfg, nil
}
