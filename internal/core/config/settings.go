package config

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/jsonstore"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// DefaultAutoCompactThreshold mirrors agent.DefaultAutoCompact. It is
// duplicated rather than imported because config sits below agent in the
// dependency graph.
const DefaultAutoCompactThreshold = 0.8

// Settings is ~/.loop/settings.json — loop's AppSettings (settings.ts), key
// for key. The file is loop's: every key here is spelled and typed the way
// loop reads it, and keys pi does not model (loop's own UI state, an
// extension's settings) stay in the file untouched, because writes go key by
// key rather than rewriting the file from this struct.
//
// Switches are pointers so "never set" is distinguishable from "off": an
// absent key means the default, which each accessor below states.
type Settings struct {
	DefaultModel string   `json:"defaultModel"`
	ScopedModels []string `json:"scopedModels"`
	Theme        string   `json:"theme"`
	ToolDetail   string   `json:"toolDetail"`
	PinnedInput  *bool    `json:"pinnedInput"`
	Sound        string   `json:"sound"`
	// ThinkingLevel is off|minimal|low|medium|high|xhigh.
	ThinkingLevel         string   `json:"thinkingLevel"`
	MaxSteps              int      `json:"maxSteps"`
	MaxStreamResumes      *int     `json:"maxStreamResumes"`
	SubagentMaxSteps      int      `json:"subagentMaxSteps"`
	SubagentStallSeconds  *int     `json:"subagentStallSeconds"`
	Subagents             *bool    `json:"subagents"`
	SubagentModel         string   `json:"subagentModel"`
	SubagentMaxParallel   *int     `json:"subagentMaxParallel"`
	ThreadTeams           *bool    `json:"threadTeams"`
	ThreadTeamsMaxThreads int      `json:"threadTeamsMaxThreads"`
	ThreadTeamsBudgetUsd  *float64 `json:"threadTeamsBudgetUsd"`
	Recap                 *bool    `json:"recap"`
	AskUser               *bool    `json:"askUser"`
	WebSearch             *bool    `json:"webSearch"`
	Serve                 *bool    `json:"serve"`
	ServeTerminal         *bool    `json:"serveTerminal"`
	Todos                 *bool    `json:"todos"`
	Artifacts             *bool    `json:"artifacts"`
	BackgroundShells      *bool    `json:"backgroundShells"`
	Clock                 *bool    `json:"clock"`
	Reminders             *bool    `json:"reminders"`
	AutoCompactThreshold  *float64 `json:"autoCompactThreshold"`
	IdleCompact           *bool    `json:"idleCompact"`
	IdleCompactMinPercent int      `json:"idleCompactMinPercent"`
	WorkspaceContext      *bool    `json:"workspaceContext"`
	Memory                *bool    `json:"memory"`
	Herdr                 *bool    `json:"herdr"`
	Cmux                  *bool    `json:"cmux"`
	Notch                 *bool    `json:"notch"`
	Skills                *bool    `json:"skills"`
	// Agent is the session persona last selected.
	Agent                string            `json:"agent"`
	Aliases              map[string]string `json:"aliases"`
	LastChangelogVersion string            `json:"lastChangelogVersion"`
	ImportClaudeHooks    *bool             `json:"importClaudeHooks"`
	ClaudeHooksFilter    []string          `json:"claudeHooksFilter"`
	// Hooks are Claude Code's matcher groups keyed by event; parsed by the
	// hooks package.
	Hooks         map[string]json.RawMessage  `json:"hooks"`
	MCP           *bool                       `json:"mcp"`
	MCPServers    map[string]mcp.ServerConfig `json:"mcpServers"`
	MCPToolSearch json.RawMessage             `json:"mcpToolSearch"`
	// BashDeny: nil = loop's default deny list; set (even empty) = full control.
	BashDeny    []string            `json:"bashDeny"`
	Permissions *PermissionsSetting `json:"permissions"`
	BashApprove *bool               `json:"bashApprove"`
	BashAllow   []string            `json:"bashAllow"`
	Sandbox     *SandboxSetting     `json:"sandbox"`

	// Keys loop's CLI keeps alongside (interactive UI and built-in extensions).
	LSP               *bool                     `json:"lsp"`
	UILive            *bool                     `json:"uiLive"`
	ExtensionSettings map[string]map[string]any `json:"extensionSettings"`
}

// PermissionsSetting is loop's rule lists, in Claude's rule syntax
// ("Bash(git *)", "Read(src/**)", bare "Read", "*").
type PermissionsSetting struct {
	Allow []string `json:"allow,omitempty"`
	Ask   []string `json:"ask,omitempty"`
	Deny  []string `json:"deny,omitempty"`
}

// SandboxSetting is loop's OS sandbox for bash.
type SandboxSetting struct {
	Enabled        *bool           `json:"enabled,omitempty"`
	Network        json.RawMessage `json:"network,omitempty"`
	AllowWrite     []string        `json:"allowWrite,omitempty"`
	DenyWrite      []string        `json:"denyWrite,omitempty"`
	DenyRead       []string        `json:"denyRead,omitempty"`
	AllowRead      []string        `json:"allowRead,omitempty"`
	AllowGitConfig *bool           `json:"allowGitConfig,omitempty"`
}

// On reports whether the sandbox is enabled.
func (s *SandboxSetting) On() bool { return s != nil && s.Enabled != nil && *s.Enabled }

// SettingsPath is ~/.loop/settings.json.
func SettingsPath() string { return home.Path("settings.json") }

// fieldKey is a Settings field's JSON key.
func fieldKey(f reflect.StructField) string {
	key, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	return key
}

// decodeSettings fills Settings key by key, so one value of an unexpected
// shape costs that key, not the whole file.
func decodeSettings(o *jsonstore.Object) Settings {
	var s Settings
	v := reflect.ValueOf(&s).Elem()
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		key := fieldKey(t.Field(i))
		raw := o.Raw(key)
		if raw == nil || string(raw) == "null" {
			continue
		}
		ptr := reflect.New(t.Field(i).Type)
		if json.Unmarshal(raw, ptr.Interface()) == nil {
			v.Field(i).Set(ptr.Elem())
		}
	}
	return s
}

// LoadSettings reads settings.json (cached; RefreshConfigStores re-reads).
func LoadSettings() Settings { return decodeSettings(SettingsStore().All()) }

// keepZero are keys loop writes even at their zero value.
var keepZero = map[string]bool{"maxSteps": true}

// Update applies a change and writes back only the keys it touched — the
// rest of the file (including keys pi does not know) is left exactly as it
// was. A key set to its zero value is removed, which is what loop's
// setSetting(key, undefined) does.
func Update(change func(*Settings)) error {
	return SettingsStore().Mutate(func(o *jsonstore.Object) {
		before := decodeSettings(o)
		after := before
		change(&after)
		bv, av := reflect.ValueOf(before), reflect.ValueOf(after)
		t := bv.Type()
		for i := 0; i < t.NumField(); i++ {
			if reflect.DeepEqual(bv.Field(i).Interface(), av.Field(i).Interface()) {
				continue
			}
			key := fieldKey(t.Field(i))
			f := av.Field(i)
			if f.IsZero() && !keepZero[key] {
				o.Delete(key)
				continue
			}
			_ = o.Set(key, f.Interface())
		}
	})
}

// SetSetting writes one raw key (loop's setSetting).
func SetSetting(key string, value any) error { return SettingsStore().Set(key, value) }

// SplitModel splits a full model id: "xai/grok-4" → ("xai", "grok-4");
// custom providers are "custom:<name>/<model>".
func SplitModel(full string) (prov, model string) {
	prov, model, ok := strings.Cut(full, "/")
	if !ok {
		return "", full
	}
	return prov, model
}

// ModelRef is the default model split into provider and model.
func (s Settings) ModelRef() (prov, model string) { return SplitModel(s.DefaultModel) }

// Reasoning maps thinkingLevel onto the AI layer's effort.
func (s Settings) Reasoning() provider.ReasoningEffort { return ThinkingToEffort(s.ThinkingLevel) }

// ThinkingToEffort maps loop's ThinkingLevel to an effort ("off" → none).
func ThinkingToEffort(level string) provider.ReasoningEffort {
	switch level {
	case "off":
		return provider.ReasoningNone
	case "minimal", "low", "medium", "high", "xhigh":
		return provider.ReasoningEffort(level)
	}
	return ""
}

// EffortToThinking is the inverse: the level loop stores.
func EffortToThinking(e provider.ReasoningEffort) string {
	switch e {
	case provider.ReasoningNone:
		return "off"
	case provider.ReasoningMinimal, provider.ReasoningLow, provider.ReasoningMedium,
		provider.ReasoningHigh, provider.ReasoningXHigh:
		return string(e)
	}
	return ""
}

// ApplyTo layers stored settings under a config, so an explicit flag always
// wins over a remembered preference.
func (s Settings) ApplyTo(c Config) Config {
	if c.Provider == "" && c.ModelID == "" && s.DefaultModel != "" {
		c.Provider, c.ModelID = s.ModelRef()
	}
	if c.Reasoning == "" {
		c.Reasoning = s.Reasoning()
	}
	if c.MaxSteps <= 0 {
		c.MaxSteps = s.MaxSteps
	}
	return c
}

// AutoCompact is the effective auto-compaction threshold; 0 turns it off.
func (s Settings) AutoCompact() float64 {
	if s.AutoCompactThreshold == nil {
		return DefaultAutoCompactThreshold
	}
	if *s.AutoCompactThreshold <= 0 {
		return 0
	}
	return *s.AutoCompactThreshold
}

// MCPList returns the configured servers in a stable order, each named.
func (s Settings) MCPList() []mcp.ServerConfig {
	out := make([]mcp.ServerConfig, 0, len(s.MCPServers))
	for name, cfg := range s.MCPServers {
		cfg.Name = name
		out = append(out, cfg)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// HookConfig is the validated hook table.
func (s Settings) HookConfig() (hooks.Config, error) {
	if len(s.Hooks) == 0 {
		return nil, nil
	}
	return hooks.Parse(s.Hooks)
}

func boolOr(v *bool, dflt bool) bool {
	if v == nil {
		return dflt
	}
	return *v
}

// The switches, each with loop's default.

func (s Settings) SubagentsOn() bool        { return boolOr(s.Subagents, true) }
func (s Settings) MemoryOn() bool           { return boolOr(s.Memory, true) }
func (s Settings) RecapOn() bool            { return boolOr(s.Recap, false) }
func (s Settings) AskUserOn() bool          { return boolOr(s.AskUser, false) }
func (s Settings) TodosOn() bool            { return boolOr(s.Todos, false) }
func (s Settings) ClockOn() bool            { return boolOr(s.Clock, false) }
func (s Settings) RemindersOn() bool        { return boolOr(s.Reminders, true) }
func (s Settings) MCPOn() bool              { return boolOr(s.MCP, true) }
func (s Settings) PinnedInputOn() bool      { return boolOr(s.PinnedInput, false) }
func (s Settings) UILiveOn() bool           { return boolOr(s.UILive, false) }
func (s Settings) HerdrOn() bool            { return boolOr(s.Herdr, true) }
func (s Settings) CmuxOn() bool             { return boolOr(s.Cmux, true) }
func (s Settings) NotchOn() bool            { return boolOr(s.Notch, true) }
func (s Settings) WorkspaceContextOn() bool { return boolOr(s.WorkspaceContext, true) }
func (s Settings) ServeOn() bool            { return boolOr(s.Serve, false) }
func (s Settings) ServeTerminalOn() bool    { return boolOr(s.ServeTerminal, true) }
func (s Settings) BashApproveOn() bool      { return boolOr(s.BashApprove, false) }
func (s Settings) WebSearchOn() bool        { return boolOr(s.WebSearch, false) }
func (s Settings) ArtifactsOn() bool        { return boolOr(s.Artifacts, false) }
func (s Settings) BackgroundShellsOn() bool { return boolOr(s.BackgroundShells, false) }
func (s Settings) ThreadTeamsOn() bool      { return boolOr(s.ThreadTeams, false) }
func (s Settings) IdleCompactOn() bool      { return boolOr(s.IdleCompact, true) }
func (s Settings) SkillsOn() bool           { return boolOr(s.Skills, true) }

// SubagentParallel is the concurrent-subagent cap (default 4, 0 unlimited).
func (s Settings) SubagentParallel() int {
	if s.SubagentMaxParallel == nil {
		return 4
	}
	return *s.SubagentMaxParallel
}

// ExtensionSetting reads one extension's stored value.
func (s Settings) ExtensionSetting(ext, key string) (any, bool) {
	v, ok := s.ExtensionSettings[ext][key]
	return v, ok
}

// Entries is every rule as "<mode> <rule>", deny first.
func (p *PermissionsSetting) Entries() []string {
	if p == nil {
		return nil
	}
	var out []string
	for _, r := range p.Deny {
		out = append(out, "deny "+r)
	}
	for _, r := range p.Ask {
		out = append(out, "ask "+r)
	}
	for _, r := range p.Allow {
		out = append(out, "allow "+r)
	}
	return out
}

// AddPermission appends a rule string to one of loop's three lists.
func AddPermission(mode, rule string) error {
	return Update(func(s *Settings) {
		p := PermissionsSetting{}
		if s.Permissions != nil {
			p = *s.Permissions
		}
		switch mode {
		case "deny":
			p.Deny = append(p.Deny, rule)
		case "ask":
			p.Ask = append(p.Ask, rule)
		default:
			p.Allow = append(p.Allow, rule)
		}
		s.Permissions = &p
	})
}

// RemovePermission drops an entry as Entries renders it.
func RemovePermission(entry string) error {
	mode, rule, _ := strings.Cut(entry, " ")
	drop := func(list []string) []string {
		out := list[:0:0]
		for _, r := range list {
			if r != rule {
				out = append(out, r)
			}
		}
		return out
	}
	return Update(func(s *Settings) {
		if s.Permissions == nil {
			return
		}
		p := *s.Permissions
		switch mode {
		case "deny":
			p.Deny = drop(p.Deny)
		case "ask":
			p.Ask = drop(p.Ask)
		default:
			p.Allow = drop(p.Allow)
		}
		if len(p.Deny)+len(p.Ask)+len(p.Allow) == 0 {
			s.Permissions = nil
		} else {
			s.Permissions = &p
		}
	})
}
