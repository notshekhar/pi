package extension

import (
	"context"
	_ "embed"
	"regexp"
	"strings"
)

// The two persona extensions: caveman and ponytail.
//
// Both are the same shape — a skill body injected into the system prompt,
// filtered down to one intensity level, with a command to switch level and a
// phrase that turns it off. They are ported from loop verbatim, skill text
// included, because the text IS the extension: a paraphrase would be a
// different extension wearing the same name.
//
// The bodies are embedded rather than held as Go string literals because they
// contain backticks, and a raw literal cannot.

//go:embed skills/caveman.md
var cavemanSkill string

//go:embed skills/ponytail.md
var ponytailSkill string

// persona is a mode-switching system-prompt extension.
type persona struct {
	name    string
	about   string
	skill   string
	banner  string
	modes   []string
	dflt    string
	stopFor string
	// filterExamples also drops the worked-example bullets keyed by mode.
	// caveman has them; ponytail does not, and applying the rule there would
	// silently eat any bullet whose first word happened to be a mode name.
	filterExamples bool

	store Store
}

func init() {
	Register(&persona{
		name:  "caveman",
		about: "Ultra-terse replies — fewer tokens, full substance (lite/full/ultra). /caveman",
		skill: cavemanSkill, banner: "CAVEMAN MODE ACTIVE",
		modes:   []string{"off", "lite", "full", "ultra", "wenyan-lite", "wenyan-full", "wenyan-ultra"},
		dflt:    "full",
		stopFor: "caveman", filterExamples: true,
	})
	Register(&persona{
		name:  "ponytail",
		about: "Lazy senior dev — write the minimal solution (lite/full/ultra). /ponytail",
		skill: ponytailSkill, banner: "PONYTAIL MODE ACTIVE",
		modes: []string{"off", "lite", "full", "ultra"},
		dflt:  "full", stopFor: "ponytail",
	})
}

func (p *persona) Name() string     { return p.name }
func (p *persona) About() string    { return p.about }
func (p *persona) UseStore(s Store) { p.store = s }
func (p *persona) Status() string   { return p.mode() }

// mode is the active level. Off by default in everything but name: the
// stored value wins, and an unset one means the extension has never been
// pointed anywhere, so it uses its own default.
func (p *persona) mode() string {
	if p.store == nil {
		return p.dflt
	}
	return p.normalize(p.store.Get("mode", p.dflt))
}

// normalize maps free text onto a mode, or to "off" when it is not one.
func (p *persona) normalize(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	for _, m := range p.modes {
		if m == v {
			return m
		}
	}
	return "off"
}

// SystemPrompt injects the filtered skill body.
func (p *persona) SystemPrompt(string) string {
	mode := p.mode()
	if mode == "off" {
		return ""
	}
	return p.banner + " — level: " + mode + "\n\n" + p.filter(mode)
}

// trailingPunctuation is stripped before matching a deactivation phrase, so
// "stop caveman." works as well as "stop caveman".
var trailingPunctuation = regexp.MustCompile(`[.!?\s]+$`)

// OnBeforeTurn watches for the phrase that switches the persona off.
//
// A whole-message match, not a substring: a turn that merely MENTIONS "stop
// caveman" — asking how to, for instance — must not do it.
func (p *persona) OnBeforeTurn(input string) {
	if p.store == nil {
		return
	}
	text := trailingPunctuation.ReplaceAllString(strings.ToLower(strings.TrimSpace(input)), "")
	if text == "stop "+p.stopFor || text == "normal mode" {
		_ = p.store.Set("mode", "off")
	}
}

func (p *persona) Commands() []Command {
	return []Command{{
		Name:  p.name,
		About: p.about,
		Run: func(_ context.Context, _, rest string) (string, string, error) {
			arg := strings.ToLower(strings.TrimSpace(rest))
			if arg == "" || arg == "status" {
				return p.name + " mode: " + p.mode() + " (options: " + strings.Join(p.modes, " | ") + ")", "", nil
			}
			// Checked against the list rather than run through normalize:
			// normalize answers "off" for anything unknown, which would turn
			// a typo into a silent switch-off.
			known := false
			for _, m := range p.modes {
				if m == arg {
					known = true
					break
				}
			}
			if !known {
				return "", "", &modeError{name: p.name, arg: arg, modes: p.modes}
			}
			if p.store != nil {
				if err := p.store.Set("mode", arg); err != nil {
					return "", "", err
				}
			}
			if arg == "off" {
				return p.name + " off — normal mode.", "", nil
			}
			return p.name + " " + arg, "", nil
		},
	}}
}

// modeError names what was typed and what was available.
type modeError struct {
	name, arg string
	modes     []string
}

func (e *modeError) Error() string {
	return "unknown " + e.name + " mode \"" + e.arg + "\". options: " + strings.Join(e.modes, " | ")
}

// tableRow matches an intensity-table row, whose bolded label is a mode name.
var tableRow = regexp.MustCompile(`^\|\s*\*\*(.+?)\*\*\s*\|`)

// exampleBullet matches a worked-example bullet keyed by mode.
var exampleBullet = regexp.MustCompile(`^-\s*([^:]+):\s*`)

// frontmatter is the YAML header the skill body carries, which is metadata
// for a skill loader and noise in a system prompt.
var frontmatter = regexp.MustCompile(`(?s)^---.*?---\s*`)

// filter keeps only the active mode's lines.
//
// The mode-specific lines are the intensity-table rows and (for caveman) the
// worked-example bullets, both keyed by a mode name. A line whose leading
// label is NOT a mode name is a normal rule and is kept verbatim — which is
// why this matches on the label rather than on the line's shape.
func (p *persona) filter(mode string) string {
	body := frontmatter.ReplaceAllString(p.skill, "")
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		if m := tableRow.FindStringSubmatch(line); m != nil {
			if label := p.labelMode(m[1]); label != "" && label != mode {
				continue
			}
		}
		if p.filterExamples {
			if m := exampleBullet.FindStringSubmatch(line); m != nil {
				if label := p.labelMode(m[1]); label != "" && label != mode {
					continue
				}
			}
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// labelMode is the mode a label names, or "" when it names none.
//
// Deliberately not normalize, which answers "off" for anything unknown: a
// table row labelled "**Rule**" would then be read as the off mode and
// dropped from every other level.
func (p *persona) labelMode(label string) string {
	l := strings.ToLower(strings.TrimSpace(label))
	for _, m := range p.modes {
		if m == l {
			return m
		}
	}
	return ""
}
