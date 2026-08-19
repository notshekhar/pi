package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/hooks"
	"github.com/notshekhar/pi/internal/modules/core/mcp"
)

// DefaultAutoCompactThreshold mirrors agent.DefaultAutoCompact. It is
// duplicated rather than imported because config sits below agent in the
// dependency graph, and inverting that to share one constant is not worth it.
const DefaultAutoCompactThreshold = 0.8

// Persisted preferences: the choices a session makes that should outlive the
// process. Kept deliberately small — this is preferences, not state.
//
// Everything here is optional and every field has a working zero value, so a
// missing, empty, or corrupt file is never fatal: a bad settings file costs
// you your theme, it does not cost you the session.

// Settings is what survives a restart.
type Settings struct {
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Theme     string `json:"theme,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	// Agent is the session persona last selected. "plan" is deliberately NOT
	// restored on a new session — plan mode is a mode you are in, not a
	// preference you hold, and a fresh session starting read-only because the
	// last one ended mid-plan is a surprise nobody can explain.
	Agent    string `json:"agent,omitempty"`
	MaxSteps int    `json:"maxSteps,omitempty"`
	// Permissions are tool-call rules in `<mode> <tool>[(<glob>)]` form.
	// They ADD to the shipped defaults; they never replace them.
	Permissions []string `json:"permissions,omitempty"`
	// WebSearchEnabled turns on the network-facing search tool.
	WebSearchEnabled bool `json:"webSearch,omitempty"`
	// MCPServers are external tool servers, keyed by the name their tools are
	// namespaced under.
	MCPServers map[string]mcp.ServerConfig `json:"mcpServers,omitempty"`
	// Aliases map a short command name to the line it expands to.
	Aliases map[string]string `json:"aliases,omitempty"`
	// Providers are user-defined OpenAI-compatible endpoints, keyed by the
	// name they are selected under.
	Providers map[string]CustomProvider `json:"providers,omitempty"`
	// CustomModels are model ids registered by hand under a provider, keyed
	// by provider. The catalog cannot know every model a gateway serves, and
	// a model you cannot name is a model you cannot use.
	CustomModels map[string][]string `json:"customModels,omitempty"`
	// ScopedModels is the ctrl+p cycle list: the handful of models worth
	// switching between mid-session, reachable with one chord.
	ScopedModels []string `json:"scopedModels,omitempty"`
	// SubagentModel and CompactModel override the model used for delegated
	// work and for summarisation. Separate keys rather than one map, because
	// they are separate questions and a map invites a third scope nobody
	// implements.
	SubagentModel string `json:"subagentModel,omitempty"`
	CompactModel  string `json:"compactModel,omitempty"`
	// Sandbox bounds shell writes: "off" or "workspace".
	Sandbox string `json:"sandbox,omitempty"`
	// Telegram credentials for the chat gateway.
	TelegramToken  string `json:"telegramToken,omitempty"`
	TelegramChatID int64  `json:"telegramChatId,omitempty"`
	// Hooks are shell commands keyed by lifecycle event. Held as raw JSON
	// because the value has two accepted shapes — a bare list of commands, or
	// Claude Code's matcher groups — and deciding between them belongs to the
	// hooks package, not to this struct.
	Hooks map[string]json.RawMessage `json:"hooks,omitempty"`
	// AutoCompactThreshold is the fraction of the context window at which a
	// session compacts itself. Unset means the default; negative turns it off.
	AutoCompactThreshold float64 `json:"autoCompactThreshold,omitempty"`

	// Feature switches. These are POINTERS, not bools, because the zero
	// value has to mean "not set" rather than "off": a plain bool would turn
	// every one of these off for everyone who already has a settings file,
	// since an absent key unmarshals to false. Nil means "use the default",
	// which each accessor below states explicitly.
	Subagents        *bool `json:"subagents,omitempty"`
	MemoryEnabled    *bool `json:"memory,omitempty"`
	RecapEnabled     *bool `json:"recap,omitempty"`
	AskUser          *bool `json:"askUser,omitempty"`
	TodosEnabled     *bool `json:"todos,omitempty"`
	ClockEnabled     *bool `json:"clock,omitempty"`
	PinnedInput      *bool `json:"pinnedInput,omitempty"`
	RemindersEnabled *bool `json:"reminders,omitempty"`
	MCPEnabled       *bool `json:"mcp,omitempty"`
	WorkspaceContext *bool `json:"workspaceContext,omitempty"`
	ServeEnabled     *bool `json:"serve,omitempty"`
	BashApprove      *bool `json:"bashApprove,omitempty"`
	// UILive holds noir's live look on outside navigation — runs of finished
	// tool calls stay folded into one row while you type.
	UILive *bool `json:"uiLive,omitempty"`
	// HerdrEnabled reports pane state to a herdr multiplexer. Only ever does
	// anything inside a herdr pane, which is why it defaults on.
	HerdrEnabled *bool `json:"herdr,omitempty"`

	// SubagentMaxParallel bounds concurrent subagent streams; 0 means the
	// built-in default.
	SubagentMaxParallel int `json:"subagentMaxParallel,omitempty"`
	// BashAllow are commands the approval prompt skips.
	BashAllow []string `json:"bashAllow,omitempty"`
	// ExtensionSettings are per-extension key/value pairs, namespaced by
	// extension name so two extensions cannot collide on a key like "mode".
	ExtensionSettings map[string]map[string]string `json:"extensionSettings,omitempty"`
	// ExtensionsOff names the extensions the user has disabled. Stored as
	// the DENY list, not the allow list, so a newly shipped extension is on
	// by default — one that ships disabled is one nobody discovers.
	ExtensionsOff []string `json:"extensionsOff,omitempty"`
	// ExtensionsOn are the ones switched on. Extensions are opt-in, so this
	// is the list that matters; ExtensionsOff exists for an extension that
	// ships enabled, and for configs written before the default flipped.
	ExtensionsOn []string `json:"extensionsOn,omitempty"`
}

// DirName is the per-project config directory's name, and the last segment of
// the user-level one. Named once so a rename is one edit rather than a grep.
const DirName = ".pi-agent"

// Dir is ~/.pi-agent, created if it is missing. Everything this agent stores
// outside a session lives here.
func Dir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, DirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	return dir, nil
}

// SettingsPath is ~/.pi-agent/settings.json.
func SettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-agent", "settings.json"), nil
}

// LoadSettings reads the settings file. A missing or unreadable file yields
// zero settings and no error — the caller's defaults then apply unchanged.
func LoadSettings() Settings {
	path, err := SettingsPath()
	if err != nil {
		return Settings{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return Settings{}
	}
	return s
}

// SaveSettings writes the settings file, creating its directory.
//
// The write is atomic: a crash midway through leaves the previous file
// intact rather than a truncated one that parses as empty and silently drops
// every preference.
func SaveSettings(s Settings) error {
	path, err := SettingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Update applies a change to the stored settings and writes them back.
func Update(change func(*Settings)) error {
	s := LoadSettings()
	change(&s)
	return SaveSettings(s)
}

// ApplyTo layers stored settings under a config, so an explicit flag always
// wins over a remembered preference.
func (s Settings) ApplyTo(c Config) Config {
	if c.Provider == "" {
		c.Provider = s.Provider
	}
	if c.ModelID == "" {
		c.ModelID = s.Model
	}
	if c.Reasoning == "" && s.Reasoning != "" {
		c.Reasoning = parseReasoning(s.Reasoning)
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = s.MaxSteps
	}
	return c
}

// parseReasoning maps a stored string onto an effort level, ignoring anything
// unrecognised rather than passing junk to a provider.
func parseReasoning(s string) provider.ReasoningEffort {
	switch provider.ReasoningEffort(s) {
	case provider.ReasoningNone, provider.ReasoningMinimal, provider.ReasoningLow,
		provider.ReasoningMedium, provider.ReasoningHigh, provider.ReasoningXHigh,
		provider.ReasoningDefault:
		return provider.ReasoningEffort(s)
	}
	return ""
}

// AutoCompact is the effective auto-compaction threshold: the stored value,
// or the default when unset. A negative value turns it off.
func (s Settings) AutoCompact() float64 {
	if s.AutoCompactThreshold == 0 {
		return DefaultAutoCompactThreshold
	}
	if s.AutoCompactThreshold < 0 {
		return 0
	}
	return s.AutoCompactThreshold
}

// MCPList returns the configured servers in a stable order, with each name
// filled in from its key.
func (s Settings) MCPList() []mcp.ServerConfig {
	out := make([]mcp.ServerConfig, 0, len(s.MCPServers))
	for name, cfg := range s.MCPServers {
		cfg.Name = name
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HookConfig is the validated hook table. An invalid entry yields the error
// and an empty table, so a typo disables hooks rather than crashing a turn.
func (s Settings) HookConfig() (hooks.Config, error) {
	if len(s.Hooks) == 0 {
		return nil, nil
	}
	return hooks.Parse(s.Hooks)
}

// The feature switches, each stating its own default.
//
// Read through these accessors rather than the fields: a nil pointer means
// "never set", and every caller has to resolve that to the same answer.

// boolOr resolves a tri-state switch.
func boolOr(v *bool, dflt bool) bool {
	if v == nil {
		return dflt
	}
	return *v
}

// SubagentsOn reports whether the task tool is offered. On by default.
func (s Settings) SubagentsOn() bool { return boolOr(s.Subagents, true) }

// MemoryOn reports whether the memory tool is offered. On by default.
func (s Settings) MemoryOn() bool { return boolOr(s.MemoryEnabled, true) }

// RecapOn reports whether a recap is generated after file-changing turns.
// OFF by default: it costs an extra model call per turn.
func (s Settings) RecapOn() bool { return boolOr(s.RecapEnabled, false) }

// AskUserOn reports whether the agent may stop and ask. On by default.
func (s Settings) AskUserOn() bool { return boolOr(s.AskUser, true) }

// TodosOn reports whether the todo tool and its panel are offered.
func (s Settings) TodosOn() bool { return boolOr(s.TodosEnabled, true) }

// ClockOn reports whether the status line carries a clock. Off by default —
// a ticking clock forces a repaint every second forever, which is exactly
// what the render loop is built to avoid.
func (s Settings) ClockOn() bool { return boolOr(s.ClockEnabled, false) }

// RemindersOn reports whether due reminders fire. On by default; turning it
// off mutes them without deleting any.
func (s Settings) RemindersOn() bool { return boolOr(s.RemindersEnabled, true) }

// MCPOn reports whether configured MCP servers are connected. On by default,
// but with no servers configured it does nothing.
func (s Settings) MCPOn() bool { return boolOr(s.MCPEnabled, true) }

// PinnedInputOn reports whether the composer is held on the last rows.
//
// Off by default: a short conversation reads better growing downward from
// where it started than pushed to the bottom of the screen behind a wall of
// blank lines.
func (s Settings) PinnedInputOn() bool { return boolOr(s.PinnedInput, false) }

// UILiveOn reports whether the live look is held outside navigation.
//
// Off by default: folding hides which calls a turn made, and the arrows that
// open a fold back up only exist in navigation — so the default view is the
// one you can read without entering a mode.
func (s Settings) UILiveOn() bool { return boolOr(s.UILive, false) }

// HerdrOn reports whether pane state is mirrored to herdr. On by default:
// outside a herdr pane the reporter never subscribes and never opens a
// socket, so the cost of leaving it on is zero, and a multiplexer that
// silently shows nothing is the harder failure to explain.
func (s Settings) HerdrOn() bool { return boolOr(s.HerdrEnabled, true) }

// WorkspaceContextOn reports whether AGENTS.md/CLAUDE.md are injected.
func (s Settings) WorkspaceContextOn() bool { return boolOr(s.WorkspaceContext, true) }

// ServeOn reports whether `serve` may expose this machine. OFF by default:
// anyone with the URL and token drives the agent.
func (s Settings) ServeOn() bool { return boolOr(s.ServeEnabled, false) }

// BashApproveOn reports whether every bash command is confirmed first. Off
// by default; the permission rules are the finer-grained control.
func (s Settings) BashApproveOn() bool { return boolOr(s.BashApprove, false) }

// SubagentParallel is the concurrent-subagent cap.
func (s Settings) SubagentParallel() int {
	if s.SubagentMaxParallel <= 0 {
		return 4
	}
	return s.SubagentMaxParallel
}

// ExtensionState is each extension the user has an opinion about, and which
// way. Absent means "no opinion" — the extension's own default decides.
//
// Both directions are recorded because either can differ from the default,
// and an off-list alone cannot say "on" for something that ships off.
func (s Settings) ExtensionState() map[string]bool {
	state := make(map[string]bool, len(s.ExtensionsOn)+len(s.ExtensionsOff))
	for _, name := range s.ExtensionsOn {
		state[name] = true
	}
	// Off wins a contradiction: the stricter reading of a file that somehow
	// names the same extension twice.
	for _, name := range s.ExtensionsOff {
		state[name] = false
	}
	return state
}
