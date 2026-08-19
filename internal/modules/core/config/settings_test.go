package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// withHome points HOME at a temp dir so settings read and write in isolation.
func withHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir) // windows
	return dir
}

func TestLoadSettingsMissingFileIsNotAnError(t *testing.T) {
	withHome(t)
	if got := LoadSettings(); !reflect.DeepEqual(got, Settings{}) {
		t.Errorf("missing file yielded %+v, want zero", got)
	}
}

// A corrupt file must cost you your preferences, not your session.
func TestLoadSettingsCorruptFileIsNotAnError(t *testing.T) {
	home := withHome(t)
	path := filepath.Join(home, ".pi-agent", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := LoadSettings(); !reflect.DeepEqual(got, Settings{}) {
		t.Errorf("corrupt file yielded %+v, want zero", got)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	withHome(t)
	want := Settings{Provider: "kimi", Model: "k3", Theme: "day", Reasoning: "high", MaxSteps: 40}
	if err := SaveSettings(want); err != nil {
		t.Fatal(err)
	}
	if got := LoadSettings(); !reflect.DeepEqual(got, want) {
		t.Errorf("round trip = %+v, want %+v", got, want)
	}
}

func TestUpdateMergesRatherThanReplaces(t *testing.T) {
	withHome(t)
	if err := SaveSettings(Settings{Provider: "kimi", Model: "k3"}); err != nil {
		t.Fatal(err)
	}
	if err := Update(func(s *Settings) { s.Theme = "day" }); err != nil {
		t.Fatal(err)
	}
	got := LoadSettings()
	if got.Theme != "day" {
		t.Errorf("theme not written: %+v", got)
	}
	if got.Provider != "kimi" || got.Model != "k3" {
		t.Errorf("update clobbered other fields: %+v", got)
	}
}

// An explicit flag must always beat a remembered preference.
func TestApplyToLayersUnderExplicitConfig(t *testing.T) {
	stored := Settings{Provider: "kimi", Model: "k3", Reasoning: "high", MaxSteps: 40}

	got := stored.ApplyTo(Config{})
	if got.Provider != "kimi" || got.ModelID != "k3" {
		t.Errorf("blank config did not take stored values: %+v", got)
	}
	if got.Reasoning != provider.ReasoningHigh || got.MaxSteps != 40 {
		t.Errorf("reasoning/steps not applied: %+v", got)
	}

	explicit := Config{Provider: "anthropic", ModelID: "opus", MaxSteps: 5}
	got = stored.ApplyTo(explicit)
	if got.Provider != "anthropic" || got.ModelID != "opus" || got.MaxSteps != 5 {
		t.Errorf("stored settings overrode explicit config: %+v", got)
	}
}

func TestApplyToIgnoresJunkReasoning(t *testing.T) {
	got := Settings{Reasoning: "wildly-invalid"}.ApplyTo(Config{})
	if got.Reasoning != "" {
		t.Errorf("junk reasoning passed through as %q", got.Reasoning)
	}
}

// The write is atomic, so a reader never sees a truncated file.
func TestSaveLeavesNoTempFile(t *testing.T) {
	home := withHome(t)
	if err := SaveSettings(Settings{Theme: "night"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(home, ".pi-agent"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestAutoCompactThreshold(t *testing.T) {
	// Unset means the default; negative is an explicit "off".
	if got := (Settings{}).AutoCompact(); got != DefaultAutoCompactThreshold {
		t.Errorf("unset = %v, want the default %v", got, DefaultAutoCompactThreshold)
	}
	if got := (Settings{AutoCompactThreshold: -1}).AutoCompact(); got != 0 {
		t.Errorf("negative = %v, want 0 (disabled)", got)
	}
	if got := (Settings{AutoCompactThreshold: 0.5}).AutoCompact(); got != 0.5 {
		t.Errorf("explicit = %v, want 0.5", got)
	}
}
