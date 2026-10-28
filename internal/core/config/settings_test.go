package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// withHome points HOME at a temp dir so settings read and write in isolation.
func withHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // windows
	return dir
}

func settingsFile(t *testing.T, home string) string {
	return filepath.Join(home, ".loop", "settings.json")
}

func writeSettings(t *testing.T, home, body string) {
	t.Helper()
	path := settingsFile(t, home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A missing file yields loop's defaults, and creates the file the way
// configstore does.
func TestLoadSettingsMissingFileGivesLoopDefaults(t *testing.T) {
	home := withHome(t)
	s := LoadSettings()
	if s.Theme != "dark" || s.AutoCompact() != 0.8 || !s.WorkspaceContextOn() {
		t.Errorf("defaults = %+v", s)
	}
	data, err := os.ReadFile(settingsFile(t, home))
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n\t\"defaultModel\": null,\n\t\"theme\": \"dark\",\n\t\"maxSteps\": 0,\n\t\"autoCompactThreshold\": 0.8,\n\t\"workspaceContext\": true\n}"
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
}

// A corrupt file must cost you your preferences, not your session.
func TestLoadSettingsCorruptFileIsNotAnError(t *testing.T) {
	home := withHome(t)
	writeSettings(t, home, "{not json")
	if got := LoadSettings(); got.Theme != "dark" {
		t.Errorf("corrupt file yielded %+v", got)
	}
}

// loop's real keys read into the right fields, and a value of an unexpected
// shape costs only that key.
func TestLoadSettingsReadsLoopKeys(t *testing.T) {
	home := withHome(t)
	writeSettings(t, home, `{"defaultModel":"xai/grok-4.7","thinkingLevel":"off","toolDetail":"compact",
		"extensionSettings":{"rtk":{"enabled":true},"statusline-themes":{"theme":"default"}},
		"bashAllow":["cd","git log"],"maxSteps":"oops","permissions":{"deny":["Bash(rm *)"]},
		"sandbox":{"enabled":true},"uiThemes":{"grok":"night"}}`)
	s := LoadSettings()
	if prov, model := s.ModelRef(); prov != "xai" || model != "grok-4.7" {
		t.Errorf("model = %s/%s", prov, model)
	}
	if s.Reasoning() != provider.ReasoningNone {
		t.Errorf("thinking off should be none, got %q", s.Reasoning())
	}
	if v, _ := s.ExtensionSetting("rtk", "enabled"); v != true {
		t.Errorf("extension setting = %v", v)
	}
	if !reflect.DeepEqual(s.BashAllow, []string{"cd", "git log"}) || s.ToolDetail != "compact" {
		t.Errorf("lists = %+v", s)
	}
	if s.MaxSteps != 0 {
		t.Errorf("a bad maxSteps should be skipped, got %d", s.MaxSteps)
	}
	if s.Permissions == nil || s.Permissions.Deny[0] != "Bash(rm *)" || !s.Sandbox.On() {
		t.Errorf("permissions/sandbox = %+v %+v", s.Permissions, s.Sandbox)
	}
}

// Update writes only the keys it changed: key order, unknown keys and
// untouched values (including a "maxSteps": 0) stay exactly as loop wrote
// them.
func TestUpdateTouchesOnlyChangedKeys(t *testing.T) {
	home := withHome(t)
	body := "{\n\t\"defaultModel\": \"xai/grok-4\",\n\t\"theme\": \"system\",\n\t\"maxSteps\": 0,\n\t\"autoCompactThreshold\": 0.8,\n\t\"workspaceContext\": true,\n\t\"uiMode\": \"loop\",\n\t\"uiThemes\": {\n\t\t\"grok\": \"night\"\n\t},\n\t\"pinnedInput\": false\n}"
	writeSettings(t, home, body)
	if err := Update(func(s *Settings) { s.Theme = "day" }); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(settingsFile(t, home))
	want := strings.Replace(body, `"theme": "system"`, `"theme": "day"`, 1)
	if string(data) != want {
		t.Errorf("file =\n%s\nwant\n%s", data, want)
	}
	// A new key goes last; clearing a key removes it.
	if err := Update(func(s *Settings) { s.SubagentModel = "xai/grok-mini"; s.DefaultModel = "" }); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(settingsFile(t, home))
	if strings.Contains(string(data), "defaultModel") || !strings.HasSuffix(string(data), "\t\"subagentModel\": \"xai/grok-mini\"\n}") {
		t.Errorf("file =\n%s", data)
	}
}

// An explicit flag must always beat a remembered preference.
func TestApplyToLayersUnderExplicitConfig(t *testing.T) {
	stored := Settings{DefaultModel: "kimi/k3", ThinkingLevel: "high", MaxSteps: 40}
	got := stored.ApplyTo(Config{})
	if got.Provider != "kimi" || got.ModelID != "k3" || got.Reasoning != provider.ReasoningHigh || got.MaxSteps != 40 {
		t.Errorf("blank config did not take stored values: %+v", got)
	}
	got = stored.ApplyTo(Config{Provider: "xai", ModelID: "grok", MaxSteps: 5})
	if got.Provider != "xai" || got.ModelID != "grok" || got.MaxSteps != 5 {
		t.Errorf("explicit config overridden: %+v", got)
	}
}

func TestThinkingMapping(t *testing.T) {
	for _, level := range []string{"off", "minimal", "low", "medium", "high", "xhigh"} {
		if EffortToThinking(ThinkingToEffort(level)) != level {
			t.Errorf("%s does not round-trip", level)
		}
	}
	if ThinkingToEffort("junk") != "" {
		t.Error("junk should map to unset")
	}
}

func TestAutoCompactThreshold(t *testing.T) {
	zero, half := 0.0, 0.5
	for _, c := range []struct {
		in   *float64
		want float64
	}{{nil, DefaultAutoCompactThreshold}, {&zero, 0}, {&half, 0.5}} {
		if got := (Settings{AutoCompactThreshold: c.in}).AutoCompact(); got != c.want {
			t.Errorf("AutoCompact(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
