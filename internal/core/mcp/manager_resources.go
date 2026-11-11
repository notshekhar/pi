package mcp

import (
	"context"
	"fmt"
	"sort"
)

// loadResources fetches a ready server's resources and templates. Run after
// the server is marked ready, never before: they are extras, and a slow
// catalog must not stand between the user and their tools.
func (m *Manager) loadResources(name string, gen int) {
	client, liveGen := m.liveClient(name)
	if client == nil || liveGen != gen || client.Capabilities().Resources == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout())
	defer cancel()
	resources, err := client.ListResources(ctx, name)
	if err != nil {
		// A failed listing keeps what was there; resources are optional.
		return
	}
	templates, _ := client.ListResourceTemplates(ctx, name)
	m.mu.Lock()
	s := m.servers[name]
	if m.gens[name] != gen || s == nil || s.State != StateReady {
		m.mu.Unlock()
		return
	}
	next := *s
	next.resources, next.templates = resources, templates
	m.servers[name] = &next
	m.mu.Unlock()
	m.changed()
}

// Resources is every resource on every ready server, server by server.
func (m *Manager) Resources() []Resource {
	return collect(m, func(s *serverState) []Resource { return s.resources })
}

// ResourceTemplates is every ready server's uri templates.
func (m *Manager) ResourceTemplates() []ResourceTemplate {
	return collect(m, func(s *serverState) []ResourceTemplate { return s.templates })
}

// HasResources is whether any ready server publishes resources or templates
// — the condition for offering the mcp_resource tool at all.
func (m *Manager) HasResources() bool {
	return len(m.Resources()) > 0 || len(m.ResourceTemplates()) > 0
}

// ReadResource reads one resource from a ready server.
func (m *Manager) ReadResource(ctx context.Context, server, uri string) ([]ResourcePart, error) {
	client, _ := m.liveClient(server)
	if client == nil {
		return nil, m.notReady(server)
	}
	return client.ReadResource(ctx, uri)
}

// notReady names why a server can't be used right now.
func (m *Manager) notReady(name string) error {
	s, ok := m.Server(name)
	if !ok {
		return fmt.Errorf("unknown MCP server: %s", name)
	}
	if s.Error != "" {
		return fmt.Errorf("MCP server %q is %s: %s", name, s.State, s.Error)
	}
	return fmt.Errorf("MCP server %q is %s", name, s.State)
}

// collect gathers one per-server list across ready servers in name order,
// so listings don't shuffle between calls.
func collect[T any](m *Manager, pick func(*serverState) []T) []T {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.servers))
	for name, s := range m.servers {
		if s.State == StateReady {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	var out []T
	for _, name := range names {
		out = append(out, pick(m.servers[name])...)
	}
	return out
}
