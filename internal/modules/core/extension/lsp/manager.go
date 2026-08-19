package lsp

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// The client pool.
//
// Keyed by (server, root) rather than by server alone: a monorepo with three
// tsconfigs gets three TypeScript servers, each rooted at its own package,
// which is what makes their diagnostics right. Sharing one server across all
// three would have it resolving imports against the wrong config.

// DiagnosticTimeout bounds how long a write waits for a server's verdict.
//
// Generous, because a cold server has to index the project before it can say
// anything — and stingy enough that a hung one does not hold up the turn.
const DiagnosticTimeout = 10 * time.Second

// RequestTimeout bounds a navigation request, which needs no indexing pass
// once the server is warm.
const RequestTimeout = 15 * time.Second

// Manager owns the running servers for one workspace.
type Manager struct {
	workspace string

	mu      sync.Mutex
	clients map[string]*Client
	// unavailable remembers servers that could not be launched, so a file
	// type with no server installed does not pay a discovery probe on every
	// single edit.
	unavailable map[string]bool
}

// NewManager builds a pool for a workspace.
func NewManager(workspace string) *Manager {
	return &Manager{
		workspace:   workspace,
		clients:     map[string]*Client{},
		unavailable: map[string]bool{},
	}
}

// clientFor returns a started client for a server and file, or nil.
func (m *Manager) clientFor(ctx context.Context, d ServerDef, absPath string) *Client {
	root := d.Root(absPath, m.workspace)
	key := d.Key + "\x00" + root

	m.mu.Lock()
	if m.unavailable[key] {
		m.mu.Unlock()
		return nil
	}
	if c := m.clients[key]; c != nil {
		m.mu.Unlock()
		if c.Alive() {
			return c
		}
		// It died. Forget it and let the next call start a fresh one — a
		// crashed server should cost one failed request, not the rest of the
		// session.
		m.mu.Lock()
		delete(m.clients, key)
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()

	spec, ok := SpecFor(d, root)
	if !ok {
		m.mu.Lock()
		m.unavailable[key] = true
		m.mu.Unlock()
		return nil
	}

	client := NewClient(spec, root)
	if err := client.Start(ctx); err != nil {
		m.mu.Lock()
		m.unavailable[key] = true
		m.mu.Unlock()
		client.Shutdown()
		return nil
	}

	m.mu.Lock()
	// Another goroutine may have won the race; keep whichever landed first so
	// two servers are never running for the same root.
	if existing := m.clients[key]; existing != nil {
		m.mu.Unlock()
		client.Shutdown()
		return existing
	}
	m.clients[key] = client
	m.mu.Unlock()
	return client
}

// ClientsFor is every started server that handles a file.
func (m *Manager) ClientsFor(ctx context.Context, absPath string) []*Client {
	var out []*Client
	for _, d := range ServersFor(absPath, m.workspace) {
		if c := m.clientFor(ctx, d, absPath); c != nil {
			out = append(out, c)
		}
	}
	return out
}

// Diagnose runs a file past every server that handles it.
//
// In parallel: two servers analysing the same file have nothing to do with
// each other, and a slow one should not decide how long the fast one takes.
func (m *Manager) Diagnose(ctx context.Context, absPath string) []Diagnostic {
	clients := m.ClientsFor(ctx, absPath)
	if len(clients) == 0 {
		return nil
	}
	body, err := os.ReadFile(absPath)
	if err != nil {
		return nil
	}
	content := string(body)

	results := make([][]Diagnostic, len(clients))
	var wg sync.WaitGroup
	for i, c := range clients {
		wg.Add(1)
		go func(i int, c *Client) {
			defer wg.Done()
			results[i] = c.Diagnose(ctx, absPath, content, DiagnosticTimeout)
		}(i, c)
	}
	wg.Wait()

	var all []Diagnostic
	for _, r := range results {
		all = append(all, r...)
	}
	return dedupe(all)
}

// Available lists the servers that handle a file and can actually run, and
// those that handle it but are not installed.
//
// Both halves, because "no server for .rs" and "rust-analyzer is not
// installed" are different problems with different fixes, and a tool that
// reported them the same way would send the user looking in the wrong place.
func (m *Manager) Available(absPath string) (ready, missing []string) {
	for _, d := range ServersFor(absPath, m.workspace) {
		if _, ok := SpecFor(d, d.Root(absPath, m.workspace)); ok {
			ready = append(ready, d.Key)
			continue
		}
		missing = append(missing, d.Key)
	}
	return ready, missing
}

// Shutdown stops every running server.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	clients := make([]*Client, 0, len(m.clients))
	for _, c := range m.clients {
		clients = append(clients, c)
	}
	m.clients = map[string]*Client{}
	m.mu.Unlock()

	// In parallel with a bounded wait: shutting down eight servers one at a
	// time, each with its own timeout, is how quitting comes to take a minute.
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func(c *Client) {
			defer wg.Done()
			c.Shutdown()
		}(c)
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
}

// Workspace is the root this manager serves.
func (m *Manager) Workspace() string { return m.workspace }

// Abs resolves a possibly-relative path against the workspace.
func (m *Manager) Abs(path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(m.workspace, path)
}
