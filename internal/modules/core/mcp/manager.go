package mcp

import (
	"context"
	"sort"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai"
)

// Manager owns the connections to every configured server.
//
// Connecting is best-effort by design: one broken server must not stop a
// session. A server that fails to start is recorded with its error and
// reported by `/mcp`, so the failure is visible without being fatal — the
// alternative is an agent that refuses to run because of a tool it was never
// going to use.

// Status is what happened to one configured server.
type Status struct {
	Name    string
	Info    ServerInfo
	Tools   int
	Err     error
	Skipped bool
}

// Manager holds live clients.
type Manager struct {
	mu      sync.Mutex
	clients map[string]*Client
	status  []Status
	tools   []ai.Tool
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{clients: map[string]*Client{}}
}

// Connect brings up every configured server, in parallel.
//
// Parallel because a handshake is mostly waiting, and starting six servers
// one after another is six startup delays a user sits through.
func (m *Manager) Connect(ctx context.Context, configs []ServerConfig, cwd string) {
	type outcome struct {
		status Status
		client *Client
		tools  []ai.Tool
	}
	results := make([]outcome, len(configs))

	var wg sync.WaitGroup
	for i, cfg := range configs {
		if cfg.Disabled {
			results[i] = outcome{status: Status{Name: cfg.Name, Skipped: true}}
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			client, err := Connect(ctx, cfg, cwd)
			if err != nil {
				results[i] = outcome{status: Status{Name: cfg.Name, Err: err}}
				return
			}
			tools, err := client.Tools(ctx)
			if err != nil {
				client.Close()
				results[i] = outcome{status: Status{Name: cfg.Name, Err: err}}
				return
			}
			results[i] = outcome{
				status: Status{Name: cfg.Name, Info: client.Info(), Tools: len(tools)},
				client: client,
				tools:  tools,
			}
		}()
	}
	wg.Wait()

	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range results {
		m.status = append(m.status, r.status)
		if r.client != nil {
			m.clients[r.status.Name] = r.client
			m.tools = append(m.tools, r.tools...)
		}
	}
	// Stable order, so the tool list handed to the model does not shuffle
	// between turns because six goroutines finished in a different order.
	sort.Slice(m.tools, func(i, j int) bool { return m.tools[i].Name() < m.tools[j].Name() })
	sort.Slice(m.status, func(i, j int) bool { return m.status[i].Name < m.status[j].Name })
}

// Tools is every bridged tool.
func (m *Manager) Tools() []ai.Tool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]ai.Tool{}, m.tools...)
}

// Status reports what happened to each configured server.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Status{}, m.status...)
}

// Close shuts every connection down.
func (m *Manager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.clients {
		c.Close()
	}
	m.clients = map[string]*Client{}
	m.tools = nil
	m.status = nil
}
