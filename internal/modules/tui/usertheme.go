package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// User themes: a palette from a JSON file, so a look can be changed without
// rebuilding.
//
// The file supplies PRIMITIVES, not slots — the same handful the built-in
// palettes fill in. Exposing the forty derived slots would let a theme be
// internally inconsistent (a heading colour that clashes with the surface it
// derives from), and would freeze the slot table as public API. Primitives in,
// derivation shared.
//
// Every field is optional and falls back to the night palette, so a two-line
// file that only changes the accent is a valid theme.

// ThemeFile is the on-disk shape.
type ThemeFile struct {
	Name  string `json:"name"`
	Light bool   `json:"light,omitempty"`
	Wash  *bool  `json:"wash,omitempty"`

	Bg       string `json:"bg,omitempty"`
	BgRaised string `json:"bgRaised,omitempty"`
	BgSunken string `json:"bgSunken,omitempty"`
	Line     string `json:"line,omitempty"`

	Text  string `json:"text,omitempty"`
	Muted string `json:"muted,omitempty"`
	Dim   string `json:"dim,omitempty"`

	Accent     string `json:"accent,omitempty"`
	AccentLift string `json:"accentLift,omitempty"`
	Success    string `json:"success,omitempty"`
	Error      string `json:"error,omitempty"`
	Warning    string `json:"warning,omitempty"`

	Heading    string `json:"heading,omitempty"`
	InlineCode string `json:"inlineCode,omitempty"`
	CodeBlock  string `json:"codeBlock,omitempty"`

	ThinkingPeak string            `json:"thinkingPeak,omitempty"`
	Syntax       map[string]string `json:"syntax,omitempty"`
}

// ThemesDir is ~/.pi-agent/themes.
func ThemesDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-agent", "themes"), nil
}

// LoadUserThemes reads every *.json theme, sorted by name.
//
// A malformed file is skipped rather than fatal: one bad theme must not cost
// you the others, and you will notice a missing theme immediately.
func LoadUserThemes() []Palette {
	dir, err := ThemesDir()
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []Palette
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var f ThemeFile
		if err := json.Unmarshal(data, &f); err != nil {
			continue
		}
		if f.Name == "" {
			f.Name = strings.TrimSuffix(e.Name(), ".json")
		}
		out = append(out, f.Palette())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Palette turns a theme file into a palette, filling gaps from the built-in
// base for its light/dark side.
func (f ThemeFile) Palette() Palette {
	p := NightPalette
	if f.Light {
		p = DayPalette
	}
	p.Name = f.Name
	p.Light = f.Light
	if f.Wash != nil {
		p.Wash = *f.Wash
	}

	set := func(dst *string, v string) {
		if isHex(v) {
			*dst = v
		}
	}
	set(&p.Bg, f.Bg)
	set(&p.BgRaised, f.BgRaised)
	set(&p.BgSunken, f.BgSunken)
	set(&p.Line, f.Line)
	set(&p.Text, f.Text)
	set(&p.Muted, f.Muted)
	set(&p.Dim, f.Dim)
	set(&p.Accent, f.Accent)
	set(&p.AccentLift, f.AccentLift)
	set(&p.Success, f.Success)
	set(&p.Error, f.Error)
	set(&p.Warning, f.Warning)
	set(&p.Heading, f.Heading)
	set(&p.InlineCode, f.InlineCode)
	set(&p.CodeBlock, f.CodeBlock)
	set(&p.ThinkingPeak, f.ThinkingPeak)

	syntax := map[string]*string{
		"comment": &p.Syntax.Comment, "keyword": &p.Syntax.Keyword,
		"function": &p.Syntax.Function, "variable": &p.Syntax.Variable,
		"string": &p.Syntax.String, "number": &p.Syntax.Number,
		"type": &p.Syntax.Type, "operator": &p.Syntax.Operator,
		"punctuation": &p.Syntax.Punctuation,
	}
	for key, dst := range syntax {
		set(dst, f.Syntax[key])
	}
	return p
}

// isHex accepts #rgb and #rrggbb. A malformed colour is IGNORED rather than
// parsed to black: a typo should leave the built-in value showing, not paint a
// hole in the theme.
func isHex(s string) bool {
	if !strings.HasPrefix(s, "#") {
		return false
	}
	switch len(s) {
	case 4, 7:
	default:
		return false
	}
	for _, r := range s[1:] {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// AllPalettes is the built-ins plus every user theme.
func AllPalettes() []Palette {
	return append([]Palette{NightPalette, DayPalette}, LoadUserThemes()...)
}

// FindPalette looks up a palette by name.
func FindPalette(name string) (Palette, bool) {
	for _, p := range AllPalettes() {
		if strings.EqualFold(p.Name, name) {
			return p, true
		}
	}
	return Palette{}, false
}
