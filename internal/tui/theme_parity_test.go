package tui

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// TestThemeParityWithLoop compares every slot and every escape against
// loop's own Theme (testdata/loop-themes.json, from loop's themes.ts and
// theme.ts — regenerate with the scratch script when loop's palettes move).
func TestThemeParityWithLoop(t *testing.T) {
	raw, err := os.ReadFile("testdata/loop-themes.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]struct {
		JSON ThemeJSON                    `json:"json"`
		Fg   map[string]map[string]any    `json:"fg"`
		Bg   map[string]map[string]string `json:"bg"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	build := func(key string) ThemeJSON {
		switch {
		case key == "night":
			return ThemeFromPalette(NightPalette)
		case key == "day":
			return ThemeFromPalette(DayPalette)
		}
		rest := strings.TrimPrefix(key, "system-")
		scheme, canvas, _ := strings.Cut(rest, "-")
		if canvas == "safe" {
			canvas = ""
		}
		SetSystemCanvas(canvas, scheme == "light")
		defer SetSystemCanvas("", false)
		return ThemeFromPalette(SystemPalette())
	}
	for key, want := range fixture {
		got := build(key)
		for slot, v := range want.JSON.Colors {
			if g := got.Colors[slot]; g != v {
				t.Errorf("%s.%s: pi %+v loop %+v", key, slot, g, v)
			}
		}
		for slot := range got.Colors {
			if _, ok := want.JSON.Colors[slot]; !ok && slot != "bgSunken" {
				t.Errorf("%s: pi has extra slot %s", key, slot)
			}
		}
		for mode, fgs := range want.Fg {
			th, err := NewThemeJSON(got)
			if err != nil {
				t.Fatal(err)
			}
			th.SetTrueColor(mode == "truecolor")
			for slot, esc := range fgs {
				if slot == "__heat" {
					for i, h := range esc.([]any) {
						if g := th.Heat(i+1, "x"); g != h.(string) {
							t.Errorf("%s %s heat%d: %q vs %q", key, mode, i+1, g, h)
						}
					}
					continue
				}
				if g := th.Fg(Slot(slot), "x"); g != esc.(string) {
					t.Errorf("%s %s fg %s: %q vs %q", key, mode, slot, g, esc)
				}
			}
			for slot, esc := range want.Bg[mode] {
				if g := th.Bg(Slot(slot), "x"); g != esc {
					t.Errorf("%s %s bg %s: %q vs %q", key, mode, slot, g, esc)
				}
			}
		}
	}
}
