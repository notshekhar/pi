package rpc

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/artifacts"
	"github.com/notshekhar/pi/internal/core/catalog"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/datasources"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// ExtensionEntry is loop's ExtensionListEntry.
type ExtensionEntry struct {
	Name        string `json:"name"`
	DisplayName string `json:"displayName"`
	Description string `json:"description,omitempty"`
	Enabled     bool   `json:"enabled"`
	Builtin     bool   `json:"builtin"`
	Version     string `json:"version,omitempty"`
	Source      string `json:"source,omitempty"`
	LinkPath    string `json:"linkPath,omitempty"`
}

// ExtensionHost lists and toggles extensions.
type ExtensionHost interface {
	List() []ExtensionEntry
	SetEnabled(name string, on bool) error
}

// MCPLive is one server's live state.
type MCPLive struct {
	Name      string
	Status    string // connecting | ready | error | disabled
	ToolCount int
	Tools     []string
	Error     string
}

// MCPHost is the running MCP manager.
type MCPHost interface {
	Live() []MCPLive
	Connected() bool
	Reconnect(ctx context.Context, cwd, name string)
	Forget(name string)
}

// ─── extensions ──────────────────────────────────────────────────────────

func (s *Server) extensionHandlers() map[string]handler {
	return map[string]handler{
		"extension.list": func(map[string]any, Transport) (any, error) {
			if s.host.Extensions == nil {
				return []ExtensionEntry{}, nil
			}
			return s.host.Extensions.List(), nil
		},
		"extension.setEnabled": func(p map[string]any, _ Transport) (any, error) {
			name := pstr(p, "name")
			value, ok := pbool(p, "value")
			if !ok {
				return nil, errors.New("value must be boolean")
			}
			if s.host.Extensions == nil {
				return nil, fmt.Errorf("unknown extension: %s", name)
			}
			found := false
			for _, e := range s.host.Extensions.List() {
				if e.Name == name {
					found = true
				}
			}
			if !found {
				return nil, fmt.Errorf("unknown extension: %s", name)
			}
			if err := s.host.Extensions.SetEnabled(name, value); err != nil {
				return nil, err
			}
			catalog.Bust()
			return map[string]any{"name": name, "value": value}, nil
		},
	}
}

// ─── datasources ─────────────────────────────────────────────────────────

func (s *Server) datasourceHandlers() map[string]handler {
	return map[string]handler{
		"datasource.list": func(map[string]any, Transport) (any, error) {
			out := []map[string]any{}
			for _, e := range datasources.List() {
				c := e.Config
				pw := c.Password
				c.Password = nil
				row := toMap(c)
				isRef := pw != nil && strings.HasPrefix(*pw, "${env:")
				if isRef {
					row["password"] = *pw
				}
				out = append(out, map[string]any{"id": e.ID, "config": row,
					"hasPassword": pw != nil && *pw != "", "passwordIsEnvRef": isRef})
			}
			return out, nil
		},
		"datasource.save": func(p map[string]any, _ Transport) (any, error) {
			id := strings.TrimSpace(pstr(p, "id"))
			if !datasources.IsValidID(id) {
				return nil, fmt.Errorf("invalid connection id: %s", id)
			}
			draft, err := datasources.Parse(p["config"])
			if err != nil {
				return nil, err
			}
			if existing, ok := datasources.Get(id); ok && draft.Password == nil && existing.Password != nil {
				draft.Password = existing.Password
			}
			if err := datasources.Save(id, draft); err != nil {
				return nil, err
			}
			return map[string]any{"id": id}, nil
		},
		"datasource.remove": func(p map[string]any, _ Transport) (any, error) {
			ok, err := datasources.Delete(strings.TrimSpace(pstr(p, "id")))
			if err != nil {
				return nil, err
			}
			return map[string]any{"ok": ok}, nil
		},
		"datasource.test": func(p map[string]any, _ Transport) (any, error) {
			id := strings.TrimSpace(pstr(p, "id"))
			if p["config"] == nil {
				saved, ok := datasources.Get(id)
				if id == "" || !ok {
					return nil, fmt.Errorf("unknown datasource: %s", orStr(id, "(none)"))
				}
				return datasources.Test(saved), nil
			}
			draft, err := datasources.Parse(p["config"])
			if err != nil {
				return nil, err
			}
			if existing, ok := datasources.Get(id); id != "" && ok && draft.Password == nil && existing.Password != nil {
				draft.Password = existing.Password
			}
			return datasources.Test(draft), nil
		},
	}
}

// ─── mcp ─────────────────────────────────────────────────────────────────

func transportOf(c mcp.ServerConfig) string {
	if c.IsHTTP() {
		if c.Type == "sse" {
			return "sse"
		}
		return "http"
	}
	return "stdio"
}

var serverPrefixClean = strings.NewReplacer("-", "_", ".", "_", " ", "_")

// serverPrefix is how a server's tools are namespaced.
func serverPrefix(name string) string { return "mcp__" + serverPrefixClean.Replace(name) + "__" }

func (s *Server) mcpList(cwd string) map[string]any {
	live := map[string]MCPLive{}
	connected := false
	if s.host.MCP != nil {
		for _, l := range s.host.MCP.Live() {
			live[l.Name] = l
		}
		connected = s.host.MCP.Connected()
	}
	globals := config.GlobalMCPServers()
	projectServers := config.ProjectMCPServers(cwd)
	configured := config.LoadMCPServers(cwd)
	names := make([]string, 0, len(configured))
	for n := range configured {
		names = append(names, n)
	}
	sortStrings(names)
	servers := []map[string]any{}
	for _, name := range names {
		c := configured[name]
		l, seen := live[name]
		enabled := !c.Disabled()
		status := "connecting"
		if !enabled {
			status = "disabled"
		}
		if seen && l.Status != "" {
			status = l.Status
		}
		scope := "project"
		if _, inProject := projectServers[name]; !inProject {
			if _, inGlobal := globals[name]; inGlobal {
				scope = "global"
			}
		}
		oauth := c.Auth == "oauth"
		row := map[string]any{"name": name, "scope": scope, "transport": transportOf(c), "status": status,
			"enabled": enabled, "toolCount": l.ToolCount, "oauth": oauth}
		if l.Error != "" {
			row["error"] = l.Error
		}
		if !c.IsHTTP() {
			row["command"] = strings.TrimSpace(strings.Join(append([]string{c.Command}, c.Args...), " "))
		} else {
			row["url"] = c.URL
		}
		if oauth {
			row["authorized"] = false
		}
		if len(l.Tools) > 0 {
			row["tools"] = l.Tools
		}
		if c.AllowedTools != nil || len(c.DeniedTools) > 0 {
			row["toolPolicy"] = true
		}
		servers = append(servers, row)
	}
	return map[string]any{"enabled": config.LoadSettings().MCPOn(), "servers": servers,
		"projectConfigPath": config.ProjectMCPPath(cwd), "connected": connected}
}

// parseServerConfig is mcp-flows' wire validation for a server entry.
func parseServerConfig(v any) (mcp.ServerConfig, error) {
	m, ok := v.(map[string]any)
	if !ok {
		return mcp.ServerConfig{}, errors.New("server config required")
	}
	var c mcp.ServerConfig
	c.Type = pstr(m, "type")
	switch c.Type {
	case "http", "sse":
		c.URL = strings.TrimSpace(pstr(m, "url"))
		if u, err := url.Parse(c.URL); err != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return c, errors.New("an http/sse server needs an http(s) url")
		}
		if h, ok := m["headers"].(map[string]any); ok {
			c.Headers = map[string]string{}
			for k, v := range h {
				c.Headers[k] = fmt.Sprint(v)
			}
		}
		if pstr(m, "auth") == "oauth" {
			c.Auth = "oauth"
		}
		c.ClientID, c.ClientSecret = pstr(m, "clientId"), pstr(m, "clientSecret")
	case "", "stdio":
		c.Type = ""
		c.Command = strings.TrimSpace(pstr(m, "command"))
		if c.Command == "" {
			return c, errors.New("a stdio server needs a command")
		}
		if args, ok := m["args"].([]any); ok {
			for _, a := range args {
				c.Args = append(c.Args, fmt.Sprint(a))
			}
		}
		if env, ok := m["env"].(map[string]any); ok {
			c.Env = map[string]string{}
			for k, v := range env {
				c.Env[k] = fmt.Sprint(v)
			}
		}
	default:
		return c, fmt.Errorf("unknown server type: %s", c.Type)
	}
	if e, ok := m["enabled"].(bool); ok && !e {
		c.Enabled = &e
	}
	return c, nil
}

func (s *Server) mcpHandlers() map[string]handler {
	cwdOf := func(p map[string]any) string { return orStr(pstr(p, "cwd"), s.host.CWD) }
	reconnect := func(cwd, name string) {
		if s.host.MCP != nil {
			s.host.MCP.Reconnect(context.Background(), cwd, name)
		}
	}
	return map[string]handler{
		"mcp.list": func(p map[string]any, _ Transport) (any, error) { return s.mcpList(cwdOf(p)), nil },
		"mcp.add": func(p map[string]any, _ Transport) (any, error) {
			name := strings.TrimSpace(pstr(p, "name"))
			if name == "" {
				return nil, errors.New("server name required")
			}
			c, err := parseServerConfig(p["config"])
			if err != nil {
				return nil, err
			}
			cwd := cwdOf(p)
			if p["scope"] == "project" {
				err = config.AddProjectMCPServer(cwd, name, c)
			} else {
				err = config.AddMCPServer(name, c)
			}
			if err != nil {
				return nil, err
			}
			reconnect(cwd, name)
			var server any
			for _, row := range s.mcpList(cwd)["servers"].([]map[string]any) {
				if row["name"] == name {
					server = row
				}
			}
			return map[string]any{"name": name, "server": server}, nil
		},
		"mcp.remove": func(p map[string]any, _ Transport) (any, error) {
			name, cwd := strings.TrimSpace(pstr(p, "name")), cwdOf(p)
			var ok bool
			var err error
			if p["scope"] == "project" {
				ok, err = config.RemoveProjectMCPServer(cwd, name)
			} else {
				ok, err = config.RemoveMCPServer(name)
			}
			if ok && s.host.MCP != nil {
				s.host.MCP.Forget(name)
			}
			return map[string]any{"ok": ok}, err
		},
		"mcp.setEnabled": func(p map[string]any, _ Transport) (any, error) {
			name, cwd := strings.TrimSpace(pstr(p, "name")), cwdOf(p)
			value, isBool := pbool(p, "value")
			if !isBool {
				return nil, errors.New("value must be boolean")
			}
			var ok bool
			var err error
			if p["scope"] == "project" {
				ok, err = config.SetProjectMCPServerEnabled(cwd, name, value)
			} else {
				ok, err = config.SetMCPServerEnabled(name, value)
			}
			if ok {
				if value {
					reconnect(cwd, name)
				} else if s.host.MCP != nil {
					s.host.MCP.Forget(name)
				}
			}
			return map[string]any{"ok": ok}, err
		},
		"mcp.reconnect": func(p map[string]any, _ Transport) (any, error) {
			cwd := cwdOf(p)
			reconnect(cwd, strings.TrimSpace(pstr(p, "name")))
			return s.mcpList(cwd), nil
		},
		"mcp.login.start": func(p map[string]any, _ Transport) (any, error) {
			return nil, errors.New("MCP OAuth sign-in is not available in pi yet")
		},
		"mcp.login.poll": func(p map[string]any, _ Transport) (any, error) {
			return nil, fmt.Errorf("unknown login flow: %s", pstr(p, "flowId"))
		},
		"mcp.login.cancel": func(map[string]any, Transport) (any, error) {
			return map[string]any{"ok": true}, nil
		},
	}
}

// ─── artifacts ───────────────────────────────────────────────────────────

func artifactRow(m artifacts.Meta) map[string]any {
	row := toMap(m)
	path := artifacts.FilePath(m.Record)
	row["path"] = path
	row["url"] = (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
	return row
}

func (s *Server) artifactHandlers() map[string]handler {
	return map[string]handler{
		"artifact.list": func(map[string]any, Transport) (any, error) {
			out := []map[string]any{}
			for _, m := range artifacts.List() {
				out = append(out, artifactRow(m))
			}
			return out, nil
		},
		"artifact.get": func(p map[string]any, _ Transport) (any, error) {
			m, ok := artifacts.Get(pstr(p, "id"))
			if !ok {
				return nil, fmt.Errorf("no such artifact: %s", pstr(p, "id"))
			}
			return artifactRow(m), nil
		},
		"artifact.read": func(p map[string]any, _ Transport) (any, error) {
			m, ok := artifacts.Get(pstr(p, "id"))
			if !ok {
				return nil, fmt.Errorf("no such artifact: %s", pstr(p, "id"))
			}
			if !m.Written {
				return nil, fmt.Errorf("artifact %s has no content yet", m.ID)
			}
			content, err := artifacts.Read(m.ID)
			if err != nil {
				return nil, err
			}
			row := artifactRow(m)
			row["content"] = content
			return row, nil
		},
		"artifact.export": func(p map[string]any, _ Transport) (any, error) {
			path, err := artifacts.Export(pstr(p, "id"), pstr(p, "dest"))
			if err != nil {
				return nil, err
			}
			return map[string]any{"path": path}, nil
		},
		"artifact.delete": func(p map[string]any, _ Transport) (any, error) {
			return map[string]any{"deleted": artifacts.Delete(pstr(p, "id"))}, nil
		},
	}
}

func sortStrings(s []string) { sort.Strings(s) }
