package statusline

import (
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Layout presets — the STRUCTURE of the status line, as against themes, which
// only recolour. A layout is handed a snapshot plus the cached vitals and
// returns fully rendered rows.
//
// `native` is the special one: it renders nothing and leaves the built-in
// two-row status line untouched, which is why it is the default. Everything
// else replaces those rows wholesale.

// Snapshot is what a layout draws from. Mirrors tui.StatusContext, and is
// declared here rather than imported because this module must not depend on
// the terminal — the layouts are pure text.
type Snapshot struct {
	Agent    string
	ModelID  string
	Model    string
	Session  string
	Thinking string
	// Reasoning is whether the model reasons at all. A layout must never show
	// a stale level for a model that has none.
	Reasoning bool
	Cost      float64
	InputTokens,
	OutputTokens,
	CachedTokens int64
	ContextUsed, ContextMax int
	Width                   int
}

// Layout is one structural preset.
type Layout struct {
	ID          string
	Description string
	// Sample is a stripped one-liner for the picker.
	Sample string
	// NeedsVitals gates the background sampler — the OS is never probed for
	// numbers no layout is showing.
	NeedsVitals bool
	// Render returns the rows, or nil to keep the built-in render.
	Render func(s Snapshot, v Vitals) []string
}

// DefaultLayout is the built-in two-row status line.
const DefaultLayout = "native"

// GetLayout finds a layout by id, falling back to native.
func GetLayout(id string) Layout {
	for _, l := range Layouts {
		if l.ID == id {
			return l
		}
	}
	return Layouts[0]
}

// separator between segments.
var separator = Dim(" │ ")

// ── formatting ──────────────────────────────────────────────────────────────

// formatTokens abbreviates a count.
func formatTokens(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	}
	return fmt.Sprint(n)
}

func formatBytes(n uint64) string {
	if g := float64(n) / (1 << 30); g >= 1 {
		return fmt.Sprintf("%.1fG", g)
	}
	return fmt.Sprintf("%dM", n>>20)
}

func formatClock() string { return time.Now().Format("15:04:05") }

var (
	claudeName = regexp.MustCompile(`(?i)claude-(opus|sonnet|haiku)-(\d+)[-.](\d+)`)
	fableName  = regexp.MustCompile(`(?i)fable-(\d+)`)
	vendorHead = regexp.MustCompile(`(?i)^(claude|anthropic)-`)
	alphaWord  = regexp.MustCompile(`^[a-z]+$`)
)

// PrettyModel turns a model id into a label a person reads:
// "anthropic/claude-opus-4-5" → "Opus 4.5".
//
// The dashboard layouts are dense, and a full provider-qualified id is the
// single longest thing that could go in one — long enough to push everything
// else off a narrow terminal.
func PrettyModel(s Snapshot) string {
	raw := s.Model
	if raw == "" {
		raw = s.ModelID
	}
	if raw == "" {
		raw = "no-model"
	}
	if i := strings.LastIndex(raw, "/"); i >= 0 {
		raw = raw[i+1:]
	}
	if m := claudeName.FindStringSubmatch(raw); m != nil {
		family := strings.ToUpper(m[1][:1]) + strings.ToLower(m[1][1:])
		return fmt.Sprintf("%s %s.%s", family, m[2], m[3])
	}
	if m := fableName.FindStringSubmatch(raw); m != nil {
		return "Fable " + m[1]
	}
	words := strings.FieldsFunc(vendorHead.ReplaceAllString(raw, ""), func(r rune) bool {
		return r == '-' || r == '_'
	})
	for i, w := range words {
		if alphaWord.MatchString(w) {
			words[i] = strings.ToUpper(w[:1]) + w[1:]
		}
	}
	return strings.Join(words, " ")
}

func contextRatio(s Snapshot) float64 {
	if s.ContextMax <= 0 {
		return 0
	}
	return float64(s.ContextUsed) / float64(s.ContextMax)
}

// percent renders one decimal, always.
//
// Fixed precision on purpose: a value parked on an integer boundary reads
// steadily instead of flickering between its rounded neighbours as the token
// count jitters by a few.
func percent(r float64) string { return fmt.Sprintf("%.1f%%", r*100) }

// tokens is the per-kind breakdown, plus the share of input served from cache.
type tokenCounts struct {
	input, output, cached, total int64
	hit                          float64
}

func breakdown(s Snapshot) tokenCounts {
	t := tokenCounts{input: s.InputTokens, output: s.OutputTokens, cached: s.CachedTokens}
	t.total = t.input + t.output + t.cached
	if t.input+t.cached > 0 {
		t.hit = float64(t.cached) / float64(t.input+t.cached)
	}
	return t
}

// ── shared pieces ───────────────────────────────────────────────────────────

func modelChip(s Snapshot) string { return Bold(FG(Colours.Cyan, PrettyModel(s))) }

// agentChip is the selected agent — dim for the default, highlighted when a
// custom one is active so it stands out.
func agentChip(s Snapshot) string {
	name := s.Agent
	if name == "" {
		name = "default"
	}
	if name == "default" {
		return Dim("@" + name)
	}
	return FG(Colours.Orange, "@"+name)
}

// thinkValue is the level as a bare word, or "" when the model does not
// reason or thinking is off — so a non-reasoning model never shows a stale one.
func thinkValue(s Snapshot) string {
	if s.Reasoning && s.Thinking != "" && s.Thinking != "off" {
		return s.Thinking
	}
	return ""
}

func thinkChip(s Snapshot) string {
	if v := thinkValue(s); v != "" {
		return FG(Colours.Magenta, v)
	}
	return ""
}

// join drops empty parts and joins the rest.
func join(parts []string, sep string) string {
	kept := parts[:0:0]
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, sep)
}

// fit keeps the longest leading run that fits, dropping lower-priority
// trailing segments.
//
// Parts arrive in priority order, so a narrow terminal sheds the least
// important bits rather than hard-clipping the right edge mid-segment. The
// first part is always kept — the renderer clips it if even that overflows.
func fit(parts []string, width int, sep string) string {
	sepLen := Len(sep)
	out, used := "", 0
	for _, p := range parts {
		if p == "" {
			continue
		}
		add := Len(p)
		if out != "" {
			add += sepLen
		}
		if out != "" && used+add > width {
			break
		}
		if out == "" {
			out = p
		} else {
			out += sep + p
		}
		used += add
	}
	return out
}

// wrap flows parts across as many rows as needed, so nothing is dropped.
//
// The dashboards use this rather than fit: on a narrow terminal a dense
// layout should spill onto another line, not quietly stop showing the cost.
func wrap(parts []string, width int, sep string) []string {
	sepLen := Len(sep)
	var rows []string
	current, used := "", 0
	for _, p := range parts {
		if p == "" {
			continue
		}
		n := Len(p)
		switch {
		case current == "":
			current, used = p, n
		case used+sepLen+n <= width:
			current += sep + p
			used += sepLen + n
		default:
			rows = append(rows, current)
			current, used = p, n
		}
	}
	if current != "" {
		rows = append(rows, current)
	}
	return rows
}

// ink is the dark text painted on a powerline block.
var ink = RGB{20, 20, 20}

// block is one powerline segment.
type block struct {
	text   string
	colour RGB
}

// powerline renders coloured blocks joined by arrow separators (a Nerd Font
// glyph). Trailing blocks that would not fit are dropped, at least one kept,
// so the strip never runs off a narrow terminal.
func powerline(blocks []block, width int) string {
	var kept []block
	used := 0
	for _, b := range blocks {
		cols := len([]rune(b.text)) + 2 + 1 // padding either side, plus the arrow
		if len(kept) > 0 && used+cols > width {
			break
		}
		kept = append(kept, b)
		used += cols
	}
	var out string
	for i, b := range kept {
		out += BG(b.colour, FG(ink, " "+b.text+" "))
		if i+1 < len(kept) {
			// The arrow is this block's colour painted over the next one's.
			out += BG(kept[i+1].colour, FG(b.colour, ""))
			continue
		}
		out += FG(b.colour, "")
	}
	return out
}

// ── the layouts ─────────────────────────────────────────────────────────────

// Layouts is every structural preset, in menu order.
var Layouts = []Layout{
	{
		ID:          "native",
		Description: "the built-in two-row status line (agent/model · session/cost/ctx)",
		Sample:      "agent default · deepseek/v4  /  session a1b2 · $0.00 · ctx 12k/200k",
		Render:      nil,
	},
	{
		ID:          "compact",
		Description: "agent · model · thinking · context bar · percent · tokens, on one row",
		Sample:      "@plan │ Opus 4.8 │ high │ [██████░░░░] 30.5% │ 61k/200k tokens",
		Render: func(s Snapshot, _ Vitals) []string {
			r := contextRatio(s)
			filled, empty := BarCells(r, 16)
			bar := Dim("[") + FG(Heat(r), filled) + Dim(empty) + Dim("]")
			toks := Dim(formatTokens(int64(s.ContextUsed)) + " tokens")
			if s.ContextMax > 0 {
				toks = Dim(formatTokens(int64(s.ContextUsed)) + "/" + formatTokens(int64(s.ContextMax)) + " tokens")
			}
			return []string{fit([]string{
				agentChip(s), modelChip(s), thinkChip(s), FG(Heat(r), percent(r)), bar, toks,
			}, s.Width, separator)}
		},
	},
	{
		ID:          "vitals",
		Description: "agent · model · thinking · ctx% · tokens · cached · hit% · cost · clock · cpu · mem",
		Sample:      "@plan │ Opus 4.8 │ high │ 19.0% ctx │ 11.6k tok │ cached 21.6k │ hit 87% │ $0.0042 │ 16:17:06 │ cpu:100% mem:29.3G",
		NeedsVitals: true,
		Render: func(s Snapshot, v Vitals) []string {
			r := contextRatio(s)
			t := breakdown(s)
			cpu := ""
			if v.CPUValid {
				cpu = FG(Heat(v.CPU), fmt.Sprintf("cpu:%d%%", int(math.Round(v.CPU*100))))
			}
			mem := ""
			if v.MemUsed > 0 {
				mem = FG(Colours.Muted, "mem:"+formatBytes(v.MemUsed))
			}
			// Wrapped rather than fitted: on a narrow terminal the whole
			// dashboard stays visible on more lines, which is the point of
			// choosing a dashboard.
			return wrap([]string{
				agentChip(s),
				modelChip(s),
				thinkChip(s),
				FG(Heat(r), percent(r)+" ctx"),
				FG(Colours.Green, formatTokens(int64(s.ContextUsed))+" tok"),
				// Shown even at zero on a fresh session, so the dashboard is complete.
				FG(Colours.Blue, "cached "+formatTokens(t.cached)),
				FG(Colours.Orange, fmt.Sprintf("hit %d%%", int(t.hit*100))),
				FG(Colours.Yellow, fmt.Sprintf("$%.4f", s.Cost)),
				FG(Colours.Magenta, formatClock()),
				join([]string{cpu, mem}, " "),
			}, s.Width, separator)
		},
	},
	{
		ID:          "tokens",
		Description: "agent · model · token economics — in · out · cached · total · cache-hit% · cost",
		Sample:      "@plan │ Opus 4.8 │ high │ in 3.2k · out 98 · cached 21.6k │ total 24.9k │ hit 87% │ $0.0042",
		Render: func(s Snapshot, _ Vitals) []string {
			t := breakdown(s)
			parts := FG(Colours.Blue, "in "+formatTokens(t.input)) + Dim(" · ") +
				FG(Colours.Magenta, "out "+formatTokens(t.output)) + Dim(" · ") +
				FG(Colours.Green, "cached "+formatTokens(t.cached))
			return wrap([]string{
				agentChip(s), modelChip(s), thinkChip(s), parts,
				Dim("total " + formatTokens(t.total)),
				FG(Colours.Orange, fmt.Sprintf("hit %d%%", int(t.hit*100))),
				FG(Colours.Yellow, fmt.Sprintf("$%.4f", s.Cost)),
			}, s.Width, separator)
		},
	},
	{
		ID:          "flex",
		Description: "three-row powerline dashboard: agent/model/ctx/thinking, tokens, cache/cost (needs a Nerd Font)",
		Sample:      "Agent: plan / Model: Opus 4.8 / Thinking: high / Ctx: 30.5%  //  In / Out / Cached / Total  //  Cache / Cost",
		Render: func(s Snapshot, _ Vitals) []string {
			r := contextRatio(s)
			t := breakdown(s)
			agent := s.Agent
			if agent == "" {
				agent = "default"
			}
			first := []block{
				{"Agent: " + agent, Colours.Faint},
				{"Model: " + PrettyModel(s), Colours.Red},
			}
			if think := thinkValue(s); think != "" {
				first = append(first, block{"Thinking: " + think, Colours.Green})
			}
			first = append(first, block{"Ctx: " + percent(r), Heat(r)})
			return []string{
				powerline(first, s.Width),
				powerline([]block{
					{"In: " + formatTokens(t.input), Colours.Red},
					{"Out: " + formatTokens(t.output), Colours.Blue},
					{"Cached: " + formatTokens(t.cached), Colours.Green},
					{"Total: " + formatTokens(t.total), Colours.Faint},
				}, s.Width),
				powerline([]block{
					{fmt.Sprintf("Cache: %.1f%%", t.hit*100), Colours.Orange},
					{fmt.Sprintf("Cost: $%.4f", s.Cost), Colours.Green},
				}, s.Width),
			}
		},
	},
	{
		ID:          "powerline",
		Description: "one row of coloured blocks with arrow separators (needs a Nerd Font)",
		Sample:      "@plan / Opus 4.8 / high / 30.5% ctx / 61k tok / $0.0042",
		Render: func(s Snapshot, _ Vitals) []string {
			r := contextRatio(s)
			agent := s.Agent
			if agent == "" {
				agent = "default"
			}
			chip := Colours.Orange
			if agent == "default" {
				chip = Colours.Faint
			}
			blocks := []block{{"@" + agent, chip}, {PrettyModel(s), Colours.Cyan}}
			if think := thinkValue(s); think != "" {
				blocks = append(blocks, block{think, Colours.Magenta})
			}
			blocks = append(blocks,
				block{percent(r) + " ctx", Heat(r)},
				block{formatTokens(int64(s.ContextUsed)) + " tok", Colours.Faint},
				block{fmt.Sprintf("$%.4f", s.Cost), Colours.Green})
			return []string{powerline(blocks, s.Width)}
		},
	},
	{
		ID:          "minimal",
		Description: "just the agent, model, context percent, and thinking — stay out of the way",
		Sample:      "@plan · Opus 4.8 · high · 30.5%",
		Render: func(s Snapshot, _ Vitals) []string {
			r := contextRatio(s)
			return []string{fit([]string{
				agentChip(s), modelChip(s), thinkChip(s), FG(Heat(r), percent(r)),
			}, s.Width, Dim(" · "))}
		},
	},
	{
		ID:          "bar",
		Description: "a wide context bar with agent, thinking, tokens and cost alongside the model",
		Sample:      "@plan Opus 4.8 high  [████████████░░░░░░░░░░] 30.5%  61k/200k · $0.0042",
		Render: func(s Snapshot, _ Vitals) []string {
			r := contextRatio(s)
			width := min(40, max(10, s.Width-34))
			filled, empty := BarCells(r, width)
			bar := Dim("[") + FG(Heat(r), filled) + Dim(empty) + Dim("]")
			toks := formatTokens(int64(s.ContextUsed))
			if s.ContextMax > 0 {
				toks += "/" + formatTokens(int64(s.ContextMax))
			}
			tail := join([]string{Dim(toks), FG(Colours.Green, fmt.Sprintf("$%.4f", s.Cost))}, Dim(" · "))
			head := join([]string{agentChip(s), modelChip(s), thinkChip(s)}, " ")
			return []string{head + "  " + bar + " " + FG(Heat(r), percent(r)) + "  " + tail}
		},
	},
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
