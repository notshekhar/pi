package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/ai"
)

// Manager owns the connections to every configured server (loop's
// McpManager). Connections are long-lived: servers hold subprocesses and
// sockets, so they are made once and kept across turns.
//
// Best-effort by design — one broken server never stops a session — and
// self-healing: a server that dies is withdrawn at once (its tools leave the
// next turn's tool set) and brought back with backoff; a remote one that
// goes silently dead is caught by a periodic probe; one that changes its
// tools is re-listed on list_changed.

// State is where one server stands.
type State string

const (
	StateDisabled   State = "disabled"
	StateConnecting State = "connecting"
	StateReady      State = "ready"
	StateError      State = "error"
	StateNeedsAuth  State = "needs-auth"
)

// Status is a snapshot of one server.
type Status struct {
	Name            string
	State           State
	Tools           int
	ToolNames       []string
	Error           string
	Info            ServerInfo
	ProtocolVersion string
	Instructions    string
	Config          ServerConfig
}

type serverState struct {
	Status
	client *Client
	tools  []ai.Tool
}

// Reconnect backoff after a server drops: doubling from 1s, capped, and
// given up on after a handful of tries — a server gone for good must not be
// respawned every second for the rest of the session.
var reconnectBase = time.Second // a var so tests need not wait seconds

const (
	reconnectMax         = 30 * time.Second
	maxReconnectAttempts = 5
)

func connectTimeout() time.Duration {
	return positiveEnvDuration("MCP_CONNECT_TIMEOUT_MS", 30*time.Second)
}

// healthInterval is how often ready servers are probed; 0 turns it off. A
// dead stdio child announces itself, but an HTTP server can be silently gone
// for an hour while its tools stay advertised and every call fails.
func healthInterval() time.Duration { return envDuration("MCP_HEALTH_INTERVAL_MS", 60*time.Second) }

// probeTimeout bounds one probe: without it the probe inherits the very
// hang it exists to detect.
func probeTimeout() time.Duration { return envDuration("MCP_PROBE_TIMEOUT_MS", 10*time.Second) }

// Manager holds live clients.
type Manager struct {
	mu      sync.Mutex
	servers map[string]*serverState
	// gens is bumped for a server every time something new is asked of it.
	// A connect carries the generation it started under and refuses to
	// write its result if that has moved — otherwise a connect still in
	// flight when the user hits delete lands afterwards and puts the server
	// back, with a live client nobody will ever close.
	gens       map[string]int
	refreshing map[string]bool
	again      map[string]bool
	timers     map[string]*time.Timer
	attempts   map[string]int
	cwd        string
	source     func() []ServerConfig
	listeners  []func()
	stopHealth chan struct{}
	closed     bool
}

// NewManager returns an empty manager.
func NewManager() *Manager {
	return &Manager{
		servers: map[string]*serverState{}, gens: map[string]int{},
		refreshing: map[string]bool{}, again: map[string]bool{},
		timers: map[string]*time.Timer{}, attempts: map[string]int{},
	}
}

// OnChange registers f to run after any server's state or tools change.
// It runs on whatever goroutine made the change, outside the manager's lock.
func (m *Manager) OnChange(f func()) {
	m.mu.Lock()
	m.listeners = append(m.listeners, f)
	m.mu.Unlock()
}

func (m *Manager) changed() {
	m.mu.Lock()
	fs := append([]func(){}, m.listeners...)
	m.mu.Unlock()
	for _, f := range fs {
		f()
	}
}

// Init connects every server source yields, in parallel, and waits for
// them (each bounded by the connect timeout). source is read again on a
// full Reconnect, so added and removed entries are picked up.
func (m *Manager) Init(ctx context.Context, cwd string, source func() []ServerConfig) {
	m.mu.Lock()
	m.cwd, m.source = cwd, source
	m.mu.Unlock()
	m.connectAll(ctx, source())
	m.startHealthChecks()
}

// Connect is Init over a fixed list.
func (m *Manager) Connect(ctx context.Context, configs []ServerConfig, cwd string) {
	m.Init(ctx, cwd, func() []ServerConfig { return configs })
}

func (m *Manager) connectAll(ctx context.Context, configs []ServerConfig) {
	var wg sync.WaitGroup
	for _, cfg := range configs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			m.connectOne(ctx, cfg)
		}()
	}
	wg.Wait()
}

func (m *Manager) nextGenLocked(name string) int {
	m.gens[name]++
	return m.gens[name]
}

// supersedeLocked invalidates whatever is in flight for a server and stops
// any reconnect we scheduled: what the user just did outranks it.
func (m *Manager) supersedeLocked(name string) {
	m.nextGenLocked(name)
	if t := m.timers[name]; t != nil {
		t.Stop()
	}
	delete(m.timers, name)
	delete(m.attempts, name)
}

// connectOne brings one server to the state its config asks for.
func (m *Manager) connectOne(ctx context.Context, cfg ServerConfig) {
	gen, instructions, ok := m.begin(cfg)
	if !ok {
		return
	}
	client, defs, err := m.dial(ctx, cfg, gen)
	m.settle(cfg, gen, instructions, client, defs, err)
}

// begin claims a generation and records where the server stands before any
// I/O: disabled, refused for a prefix clash, or connecting. It reports
// whether a connection should be made.
func (m *Manager) begin(cfg ServerConfig) (gen int, instructions string, connect bool) {
	name := cfg.Name
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return 0, "", false
	}
	gen = m.nextGenLocked(name)
	var old *Client
	if prev := m.servers[name]; prev != nil {
		// The last instructions stay through a reconnect: dropping them for
		// the seconds a server spends connecting would churn the system
		// prompt.
		old, instructions = prev.client, prev.Instructions
	}
	next := Status{Name: name, Config: cfg, Instructions: instructions}
	switch clash := m.prefixClashLocked(name); {
	case cfg.Disabled():
		next.State = StateDisabled
	case clash != "":
		// Two names that differ only in characters the tool-name charset
		// can't carry share a prefix: the second's tools would overwrite the
		// first's. Say so instead.
		next.State = StateError
		next.Error = fmt.Sprintf("tool prefix %s collides with server %q — rename one of them", ServerPrefix(name), clash)
	default:
		next.State, connect = StateConnecting, true
	}
	m.servers[name] = &serverState{Status: next}
	m.mu.Unlock()
	closeAsync(old)
	m.changed()
	return gen, instructions, connect
}

// dial connects and lists tools, bounded by the connect timeout.
func (m *Manager) dial(ctx context.Context, cfg ServerConfig, gen int) (*Client, []ToolDef, error) {
	m.mu.Lock()
	cwd := m.cwd
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, connectTimeout())
	defer cancel()
	client, err := Connect(ctx, cfg, cwd, ConnectOptions{
		OnNotification: func(method string, _ json.RawMessage) { m.handleNotification(cfg.Name, gen, method) },
		OnClose:        func(err error) { m.markDisconnected(cfg.Name, gen, err) },
	})
	var defs []ToolDef
	if err == nil {
		if defs, err = client.ListTools(ctx); err != nil {
			// The handshake succeeded, so a process or session is live behind
			// this client and nobody else has a handle on it.
			client.Close()
			client = nil
		}
	}
	if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
		err = fmt.Errorf("connection to %q timed out after %s", cfg.Name, connectTimeout())
	}
	return client, defs, err
}

// settle records a connect's outcome — unless the server was removed,
// disabled or reconnected meanwhile, in which case the result belongs to
// nobody and is closed rather than leaked.
func (m *Manager) settle(cfg ServerConfig, gen int, instructions string, client *Client, defs []ToolDef, err error) {
	name := cfg.Name
	m.mu.Lock()
	if m.gens[name] != gen || m.closed {
		m.mu.Unlock()
		closeAsync(client)
		return
	}
	if err != nil {
		state := StateError
		if IsUnauthorized(err) {
			state = StateNeedsAuth
		}
		m.servers[name] = &serverState{Status: Status{Name: name, State: state, Config: cfg, Error: describe(err), Instructions: instructions}}
	} else {
		tools := m.bridge(name, cfg, defs)
		m.servers[name] = &serverState{
			Status: Status{Name: name, State: StateReady, Config: cfg, Tools: len(tools), ToolNames: toolNames(tools),
				Info: client.Info(), ProtocolVersion: client.ProtocolVersion(), Instructions: client.Instructions()},
			client: client, tools: tools,
		}
		delete(m.attempts, name)
	}
	m.mu.Unlock()
	m.changed()
}

func (m *Manager) prefixClashLocked(name string) string {
	prefix := ServerPrefix(name)
	for other, s := range m.servers {
		if other == name || (s.State != StateReady && s.State != StateConnecting) {
			continue
		}
		if ServerPrefix(other) == prefix {
			return other
		}
	}
	return ""
}

// benignProtocolError is a message the protocol layer declined, not a dead
// connection — it must never cost a working server its tools.
var benignProtocolError = regexp.MustCompile(`(?i)unsupported message type|unsupported request method|no elicitation handler`)

// markDisconnected withdraws a server whose connection died on its own and
// schedules it back.
func (m *Manager) markDisconnected(name string, gen int, err error) {
	if err != nil && benignProtocolError.MatchString(err.Error()) {
		return
	}
	m.mu.Lock()
	s := m.servers[name]
	if m.gens[name] != gen || s == nil || s.State != StateReady || m.closed {
		m.mu.Unlock()
		return
	}
	client := s.client
	m.servers[name] = &serverState{Status: Status{Name: name, State: StateError, Config: s.Config, Info: s.Info,
		Instructions: s.Instructions, Error: "disconnected: " + describe(err)}}
	m.scheduleReconnectLocked(name)
	m.mu.Unlock()
	closeAsync(client)
	m.changed()
}

// scheduleReconnectLocked brings a dropped server back on its own. Only
// servers that were WORKING get here: a config error or a missing login
// fails identically forever, and retrying those is noise with a
// subprocess attached.
func (m *Manager) scheduleReconnectLocked(name string) {
	if m.timers[name] != nil || m.closed {
		return
	}
	attempt := m.attempts[name]
	if attempt >= maxReconnectAttempts {
		return
	}
	m.attempts[name] = attempt + 1
	delay := time.Duration(math.Min(float64(reconnectBase)*math.Pow(2, float64(attempt)), float64(reconnectMax)))
	var t *time.Timer
	t = time.AfterFunc(delay, func() {
		m.mu.Lock()
		if m.timers[name] != t {
			m.mu.Unlock()
			return
		}
		delete(m.timers, name)
		s := m.servers[name]
		// Anything that changed the server meanwhile — a manual reconnect,
		// a disable, a delete — wins over this.
		if s == nil || s.State != StateError || m.closed {
			m.mu.Unlock()
			return
		}
		cfg := s.Config
		attempts := m.attempts[name]
		m.mu.Unlock()
		m.connectOne(context.Background(), cfg)
		m.mu.Lock()
		if s := m.servers[name]; s != nil && s.State == StateError {
			m.attempts[name] = attempts
			m.scheduleReconnectLocked(name)
		}
		m.mu.Unlock()
	})
	m.timers[name] = t
}

// handleNotification: tools/list_changed is the one with work behind it —
// a server that advertises it expects the client to re-list, and one that
// doesn't goes on calling tools that no longer exist.
func (m *Manager) handleNotification(name string, gen int, method string) {
	if method == "notifications/tools/list_changed" {
		go m.refreshTools(name, gen)
	}
}

// refreshTools re-lists one server's tools. Coalesced: a server rebuilding
// its list emits a burst of list_changed, and while one refresh runs later
// ones set a flag that runs exactly one more.
func (m *Manager) refreshTools(name string, gen int) {
	m.mu.Lock()
	if m.refreshing[name] {
		m.again[name] = true
		m.mu.Unlock()
		return
	}
	m.refreshing[name] = true
	s := m.servers[name]
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		delete(m.refreshing, name)
		rerun := m.again[name]
		delete(m.again, name)
		m.mu.Unlock()
		if rerun {
			m.refreshTools(name, gen)
		}
	}()
	if s == nil || s.client == nil || s.State != StateReady {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout())
	defer cancel()
	defs, err := s.client.ListTools(ctx)
	if err != nil {
		// A failed re-list leaves the previous tools: they worked a moment
		// ago, and one bad round trip is not a reason to withdraw them.
		return
	}
	m.applyTools(name, gen, defs)
}

func (m *Manager) applyTools(name string, gen int, defs []ToolDef) {
	m.mu.Lock()
	s := m.servers[name]
	if m.gens[name] != gen || s == nil || s.State != StateReady {
		m.mu.Unlock()
		return
	}
	tools := m.bridge(name, s.Config, defs)
	names := toolNames(tools)
	if slices.Equal(names, s.ToolNames) {
		m.mu.Unlock()
		return
	}
	next := *s
	next.tools, next.Tools, next.ToolNames = tools, len(tools), names
	m.servers[name] = &next
	m.mu.Unlock()
	m.changed()
}

func (m *Manager) startHealthChecks() {
	interval := healthInterval()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.stopHealth != nil || interval <= 0 || m.closed {
		return
	}
	stop := make(chan struct{})
	m.stopHealth = stop
	go func() {
		tick := time.NewTicker(interval)
		defer tick.Stop()
		for {
			select {
			case <-stop:
				return
			case <-tick.C:
				m.CheckHealth()
			}
		}
	}()
}

// CheckHealth probes every ready server once, in parallel, each bounded. A
// probe is a tools/list — the one request every server implements — and
// doubles as a refresh for servers that change tools without saying so.
func (m *Manager) CheckHealth() {
	type probe struct {
		name   string
		gen    int
		client *Client
	}
	m.mu.Lock()
	var probes []probe
	for name, s := range m.servers {
		if s.State == StateReady && s.client != nil {
			probes = append(probes, probe{name, m.gens[name], s.client})
		}
	}
	m.mu.Unlock()
	var wg sync.WaitGroup
	for _, p := range probes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), probeTimeout())
			defer cancel()
			defs, err := p.client.ListTools(ctx)
			if err != nil {
				if errors.Is(ctx.Err(), context.DeadlineExceeded) {
					err = fmt.Errorf("health probe timed out after %s", probeTimeout())
				}
				m.markDisconnected(p.name, p.gen, err)
				return
			}
			m.applyTools(p.name, p.gen, defs)
		}()
	}
	wg.Wait()
}

// Reconnect re-reads the configuration and reconnects one server, or with
// name "" every server — picking up entries that were added, dropping ones
// that were removed, and applying enable/disable.
func (m *Manager) Reconnect(ctx context.Context, name string) {
	m.mu.Lock()
	source := m.source
	m.mu.Unlock()
	var configs []ServerConfig
	if source != nil {
		configs = source()
	}
	if name != "" {
		for _, cfg := range configs {
			if cfg.Name == name {
				m.mu.Lock()
				m.supersedeLocked(name)
				m.mu.Unlock()
				m.connectOne(ctx, cfg)
				return
			}
		}
		// No longer configured: reconnecting it means letting it go.
		m.Forget(name)
		return
	}
	want := map[string]bool{}
	for _, cfg := range configs {
		want[cfg.Name] = true
	}
	m.mu.Lock()
	var gone []string
	for n := range m.servers {
		if !want[n] {
			gone = append(gone, n)
		}
	}
	for _, cfg := range configs {
		m.supersedeLocked(cfg.Name)
	}
	m.mu.Unlock()
	for _, n := range gone {
		m.Forget(n)
	}
	m.connectAll(ctx, configs)
	m.startHealthChecks()
}

// SetCWD changes the folder whose project servers apply.
func (m *Manager) SetCWD(cwd string, source func() []ServerConfig) {
	m.mu.Lock()
	m.cwd = cwd
	if source != nil {
		m.source = source
	}
	m.mu.Unlock()
}

// Forget disconnects a server and drops it from the manager.
func (m *Manager) Forget(name string) {
	m.mu.Lock()
	m.supersedeLocked(name)
	s := m.servers[name]
	delete(m.servers, name)
	m.mu.Unlock()
	if s != nil {
		closeAsync(s.client)
		m.changed()
	}
}

// Remove is Forget plus the server's stored OAuth session.
func (m *Manager) Remove(name string) {
	m.Forget(name)
	_ = ClearAuth(name)
}

// Authorize runs the browser login for a server, then reconnects it.
func (m *Manager) Authorize(ctx context.Context, name string, open func(string)) error {
	cfg, ok := m.config(name)
	if !ok {
		return fmt.Errorf("unknown MCP server: %s", name)
	}
	if err := Authorize(ctx, name, cfg, open); err != nil {
		return err
	}
	m.Reconnect(ctx, name)
	if s, ok := m.Server(name); ok && s.State != StateReady {
		return fmt.Errorf("signed in, but %s did not connect: %s", name, s.Error)
	}
	return nil
}

// Logout forgets a server's stored session and reconnects it anonymously.
func (m *Manager) Logout(ctx context.Context, name string) {
	_ = ClearAuth(name)
	m.Reconnect(ctx, name)
}

func (m *Manager) config(name string) (ServerConfig, bool) {
	m.mu.Lock()
	source := m.source
	s := m.servers[name]
	m.mu.Unlock()
	if source != nil {
		for _, cfg := range source() {
			if cfg.Name == name {
				return cfg, true
			}
		}
	}
	if s != nil {
		return s.Config, true
	}
	return ServerConfig{}, false
}

// liveClient is a ready server's client and the generation it belongs to.
func (m *Manager) liveClient(name string) (*Client, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := m.servers[name]
	if s == nil || s.State != StateReady {
		return nil, 0
	}
	return s.client, m.gens[name]
}

// Tools is every ready server's tools, in a stable order so the tool list
// handed to the model does not shuffle between turns.
func (m *Manager) Tools() []ai.Tool {
	m.mu.Lock()
	var out []ai.Tool
	for _, s := range m.servers {
		if s.State == StateReady {
			out = append(out, s.tools...)
		}
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// Status reports every server, by name.
func (m *Manager) Status() []Status {
	m.mu.Lock()
	out := make([]Status, 0, len(m.servers))
	for _, s := range m.servers {
		out = append(out, s.Status)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Server is one server's snapshot.
func (m *Manager) Server(name string) (Status, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.servers[name]; s != nil {
		return s.Status, true
	}
	return Status{}, false
}

// Instructions are the ready servers' own usage notes, by server.
func (m *Manager) Instructions() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]string{}
	for name, s := range m.servers {
		if s.State == StateReady && strings.TrimSpace(s.Instructions) != "" {
			out[name] = s.Instructions
		}
	}
	return out
}

// Close shuts every connection down and stops probes and retries.
func (m *Manager) Close() {
	m.mu.Lock()
	m.closed = true
	if m.stopHealth != nil {
		close(m.stopHealth)
		m.stopHealth = nil
	}
	for _, t := range m.timers {
		t.Stop()
	}
	m.timers = map[string]*time.Timer{}
	var clients []*Client
	for name, s := range m.servers {
		m.nextGenLocked(name)
		if s.client != nil {
			clients = append(clients, s.client)
		}
	}
	m.servers = map[string]*serverState{}
	m.mu.Unlock()
	// In parallel: a stdio server's graceful shutdown can take seconds, and
	// six of them in a row is an exit the user sits through.
	var wg sync.WaitGroup
	for _, c := range clients {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Close()
		}()
	}
	wg.Wait()
}

func closeAsync(c *Client) {
	if c != nil {
		go c.Close()
	}
}

func toolNames(tools []ai.Tool) []string {
	names := make([]string, len(tools))
	for i, t := range tools {
		names[i] = t.Name()
	}
	sort.Strings(names)
	return names
}

func describe(err error) string {
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}
