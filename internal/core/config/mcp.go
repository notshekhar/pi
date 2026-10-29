package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/jsonstore"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// MCP server sources (mcp/config.ts): global servers in settings.json, and
// project servers in <cwd>/.loop/mcp.json, the latter winning on a name and
// connecting only in a trusted folder.

// GlobalMCPServers is settings.json's mcpServers.
func GlobalMCPServers() map[string]mcp.ServerConfig {
	out := map[string]mcp.ServerConfig{}
	SettingsStore().Get("mcpServers", &out)
	return out
}

// ProjectMCPPath is <cwd>/.loop/mcp.json.
func ProjectMCPPath(cwd string) string { return filepath.Join(cwd, home.DirName, "mcp.json") }

// ProjectMCPServers reads the project file ({mcpServers:{…}} or a bare map).
func ProjectMCPServers(cwd string) map[string]mcp.ServerConfig {
	out := map[string]mcp.ServerConfig{}
	data, err := os.ReadFile(ProjectMCPPath(cwd))
	if err != nil {
		return out
	}
	var wrapped struct {
		MCPServers map[string]mcp.ServerConfig `json:"mcpServers"`
	}
	if json.Unmarshal(data, &wrapped) == nil && wrapped.MCPServers != nil {
		return wrapped.MCPServers
	}
	_ = json.Unmarshal(data, &out)
	return out
}

// LoadMCPServers merges global and project (project wins).
func LoadMCPServers(cwd string) map[string]mcp.ServerConfig {
	out := GlobalMCPServers()
	for k, v := range ProjectMCPServers(cwd) {
		out[k] = v
	}
	return out
}

// ConnectableMCPServers are the servers that may connect in cwd: project
// servers only in a trusted folder.
func ConnectableMCPServers(cwd string, trusted bool) []mcp.ServerConfig {
	src := GlobalMCPServers()
	if trusted {
		src = LoadMCPServers(cwd)
	}
	out := make([]mcp.ServerConfig, 0, len(src))
	for name, c := range src {
		c.Name = name
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func writeGlobalServers(servers map[string]mcp.ServerConfig) error {
	return SettingsStore().Mutate(func(o *jsonstore.Object) { _ = o.Set("mcpServers", servers) })
}

// AddMCPServer adds or replaces a global server.
func AddMCPServer(name string, c mcp.ServerConfig) error {
	s := GlobalMCPServers()
	s[name] = c
	return writeGlobalServers(s)
}

// SetMCPServerEnabled flips a global server; false when it is not one.
func SetMCPServerEnabled(name string, on bool) (bool, error) {
	s := GlobalMCPServers()
	c, ok := s[name]
	if !ok {
		return false, nil
	}
	c.Enabled = &on
	s[name] = c
	return true, writeGlobalServers(s)
}

// RemoveMCPServer deletes a global server; false when it is not one.
func RemoveMCPServer(name string) (bool, error) {
	s := GlobalMCPServers()
	if _, ok := s[name]; !ok {
		return false, nil
	}
	delete(s, name)
	return true, writeGlobalServers(s)
}

func writeProjectServers(cwd string, servers map[string]mcp.ServerConfig) error {
	path := ProjectMCPPath(cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(map[string]any{"mcpServers": servers}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}

// AddProjectMCPServer adds a project server.
func AddProjectMCPServer(cwd, name string, c mcp.ServerConfig) error {
	s := ProjectMCPServers(cwd)
	s[name] = c
	return writeProjectServers(cwd, s)
}

// SetProjectMCPServerEnabled flips a project server.
func SetProjectMCPServerEnabled(cwd, name string, on bool) (bool, error) {
	s := ProjectMCPServers(cwd)
	c, ok := s[name]
	if !ok {
		return false, nil
	}
	c.Enabled = &on
	s[name] = c
	return true, writeProjectServers(cwd, s)
}

// RemoveProjectMCPServer deletes a project server.
func RemoveProjectMCPServer(cwd, name string) (bool, error) {
	s := ProjectMCPServers(cwd)
	if _, ok := s[name]; !ok {
		return false, nil
	}
	delete(s, name)
	return true, writeProjectServers(cwd, s)
}

// MCPScope is the file that defines a server here: "project" when
// <cwd>/.loop/mcp.json has it (it wins on a name), else "global".
func MCPScope(cwd, name string) string {
	if _, ok := ProjectMCPServers(cwd)[name]; ok {
		return "project"
	}
	return "global"
}

// SetMCPEnabledHere flips a server in the file that defines it.
func SetMCPEnabledHere(cwd, name string, on bool) (bool, error) {
	if MCPScope(cwd, name) == "project" {
		return SetProjectMCPServerEnabled(cwd, name, on)
	}
	return SetMCPServerEnabled(name, on)
}

// RemoveMCPHere deletes a server from the file that defines it, reporting
// which one that was.
func RemoveMCPHere(cwd, name string) (scope string, ok bool, err error) {
	scope = MCPScope(cwd, name)
	if scope == "project" {
		ok, err = RemoveProjectMCPServer(cwd, name)
	} else {
		ok, err = RemoveMCPServer(name)
	}
	return scope, ok, err
}
