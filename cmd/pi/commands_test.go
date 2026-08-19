package main

import (
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/core/config"
)

func TestCommas(t *testing.T) {
	cases := map[int]string{
		0: "0", 42: "42", 999: "999", 1000: "1,000",
		18420: "18,420", 1000000: "1,000,000", 200000: "200,000",
	}
	for in, want := range cases {
		if got := commas(in); got != want {
			t.Errorf("commas(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestMeter(t *testing.T) {
	cases := []struct {
		pct, width, filled int
	}{
		{0, 10, 0},
		{50, 10, 5},
		{100, 10, 10},
		// A percentage past the end must not run off the bar, and a negative
		// one must not produce a negative repeat count (which panics).
		{150, 10, 10},
		{-5, 10, 0},
	}
	for _, c := range cases {
		got := meter(c.pct, c.width)
		if n := len([]rune(got)); n != c.width {
			t.Errorf("meter(%d,%d) is %d cells, want %d", c.pct, c.width, n, c.width)
		}
		if n := strings.Count(got, "█"); n != c.filled {
			t.Errorf("meter(%d,%d) filled %d, want %d", c.pct, c.width, n, c.filled)
		}
	}
}

// findKey is the lookup /settings does.
func findKey(name string) *settingKey {
	for i := range settingKeys {
		if settingKeys[i].name == name {
			return &settingKeys[i]
		}
	}
	return nil
}

func TestSettingKeysValidate(t *testing.T) {
	cases := []struct {
		key, value string
		ok         bool
	}{
		{"theme", "night", true},
		{"theme", "day", true},
		{"theme", "purple", false},
		{"thinking", "high", true},
		{"thinking", "wildly-invalid", false},
		{"maxSteps", "40", true},
		{"maxSteps", "0", false},
		{"maxSteps", "-5", false},
		{"maxSteps", "lots", false},
		{"autoCompact", "0.8", true},
		{"autoCompact", "80%", true},
		{"autoCompact", "80", true},
		{"autoCompact", "off", true},
		{"autoCompact", "150", false},
		{"autoCompact", "nonsense", false},
	}
	for _, c := range cases {
		key := findKey(c.key)
		if key == nil {
			t.Fatalf("no such setting %q", c.key)
		}
		var s config.Settings
		err := key.set(&s, c.value)
		if (err == nil) != c.ok {
			t.Errorf("%s=%q: err=%v, want ok=%v", c.key, c.value, err, c.ok)
		}
	}
}

// Percent and fraction forms must land on the same stored value.
func TestAutoCompactAcceptsBothForms(t *testing.T) {
	key := findKey("autoCompact")
	var a, b config.Settings
	if err := key.set(&a, "0.8"); err != nil {
		t.Fatal(err)
	}
	if err := key.set(&b, "80%"); err != nil {
		t.Fatal(err)
	}
	if a.AutoCompactThreshold != b.AutoCompactThreshold {
		t.Errorf("0.8 → %v but 80%% → %v", a.AutoCompactThreshold, b.AutoCompactThreshold)
	}
}

// "off" has to be distinguishable from "unset", or disabling it silently
// restores the default on the next read.
func TestAutoCompactOffSurvivesAReload(t *testing.T) {
	key := findKey("autoCompact")
	var s config.Settings
	if err := key.set(&s, "off"); err != nil {
		t.Fatal(err)
	}
	if s.AutoCompact() != 0 {
		t.Errorf("after off, AutoCompact() = %v, want 0", s.AutoCompact())
	}
	if key.get(s) != "off" {
		t.Errorf("get = %q, want off", key.get(s))
	}
}

func TestSettingKeysAllRenderADefault(t *testing.T) {
	var zero config.Settings
	for _, key := range settingKeys {
		// value(), not get(): a manager row has no stored setting of its own
		// and renders through status instead.
		if got := key.value(zero); got == "" {
			t.Errorf("%s renders empty for unset settings", key.name)
		}
		if key.help == "" {
			t.Errorf("%s has no help text", key.name)
		}
		if key.manager == nil && key.set == nil {
			t.Errorf("%s is neither settable nor a panel", key.name)
		}
	}
}
