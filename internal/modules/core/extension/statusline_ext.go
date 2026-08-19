package extension

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/modules/core/extension/statusline"
)

// statusline-themes — customise the status line along two independent axes:
//
//	layout (/statusline)   the STRUCTURE: which information appears and how it
//	                       is arranged (native, compact, vitals, powerline, …)
//	colour (/statuscolor)  a theme that recolours whatever the layout drew
//
// They compose because the layout transform runs first and the colour
// transform repaints its output — neither has to know about the other, which
// is why there are two commands rather than one list of twelve-times-eight
// combinations.

// StatusTransform rewrites rendered status rows. Declared here rather than
// imported from the TUI: this module knows nothing about the terminal, and
// the app adapts between the two shapes at the one place they meet.
type StatusTransform func(lines []string, s statusline.Snapshot) []string

// StatusLineProvider contributes transforms to the status line.
type StatusLineProvider interface {
	StatusTransforms() []StatusTransform
}

// StatusTransformsFrom collects every enabled extension's transforms.
func StatusTransformsFrom(list []Extension) []StatusTransform {
	var out []StatusTransform
	for _, e := range list {
		if p, ok := e.(StatusLineProvider); ok {
			out = append(out, p.StatusTransforms()...)
		}
	}
	return out
}

// statusThemes is the extension.
type statusThemes struct {
	store Store
	// sampler runs only while a layout that shows vitals is active, so the
	// OS is never probed for numbers nobody is looking at.
	mu      sync.Mutex
	sampler *statusline.Sampler
	// repaint is how a sampler tick reaches the screen. The clock and the CPU
	// figure change with no user action, which would otherwise never trigger
	// a render.
	repaint func()
}

func init() { Register(&statusThemes{}) }

func (*statusThemes) Name() string { return "statusline-themes" }
func (*statusThemes) About() string {
	return "Status line layouts and colour themes. /statusline · /statuscolor"
}

func (x *statusThemes) UseStore(s Store) { x.store = s }

// SetRepaint installs the callback a sampler tick uses to refresh the screen.
func (x *statusThemes) SetRepaint(f func()) {
	x.mu.Lock()
	x.repaint = f
	x.mu.Unlock()
	x.syncSampler()
}

func (x *statusThemes) layoutID() string {
	if x.store == nil {
		return statusline.DefaultLayout
	}
	return statusline.GetLayout(x.store.Get("layout", statusline.DefaultLayout)).ID
}

func (x *statusThemes) themeID() string {
	if x.store == nil {
		return statusline.DefaultTheme
	}
	return statusline.GetTheme(x.store.Get("theme", statusline.DefaultTheme)).ID
}

// Status is the active pair, which is what a user forgets — the layout alone
// does not say why the status line is suddenly green.
func (x *statusThemes) Status() string {
	layout, theme := x.layoutID(), x.themeID()
	if theme == statusline.DefaultTheme {
		return layout
	}
	return layout + " · " + theme
}

// syncSampler starts or stops vitals sampling to match the active layout.
func (x *statusThemes) syncSampler() {
	needs := statusline.GetLayout(x.layoutID()).NeedsVitals

	x.mu.Lock()
	defer x.mu.Unlock()
	if !needs {
		if x.sampler != nil {
			x.sampler.Stop()
			x.sampler = nil
		}
		return
	}
	if x.sampler != nil {
		return
	}
	x.sampler = &statusline.Sampler{}
	x.sampler.Start(x.repaint, time.Second)
}

func (x *statusThemes) vitals() statusline.Vitals {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.sampler == nil {
		return statusline.Vitals{}
	}
	return x.sampler.Get()
}

// StatusTransforms is the two-stage chain: structure, then colour.
func (x *statusThemes) StatusTransforms() []StatusTransform {
	return []StatusTransform{
		// 1) Layout — replace the rows with the active preset's output.
		func(lines []string, s statusline.Snapshot) []string {
			layout := statusline.GetLayout(x.layoutID())
			if layout.Render == nil {
				return nil // native: leave the built-in rows alone
			}
			rendered := layout.Render(s, x.vitals())
			if len(rendered) == 0 {
				// A layout that produced nothing must not blank the status
				// line — better the native rows than an empty strip.
				return nil
			}
			return rendered
		},
		// 2) Colour — repaint whatever stage one produced.
		func(lines []string, _ statusline.Snapshot) []string {
			theme := statusline.GetTheme(x.themeID())
			out := make([]string, len(lines))
			for i, l := range lines {
				out[i] = theme.Apply(l)
			}
			return out
		},
	}
}

func (x *statusThemes) Commands() []Command {
	return []Command{
		{
			Name:  "statusline",
			About: "Pick a status line layout (opens a menu, or /statusline <name>)",
			Run: func(_ context.Context, _, rest string) (string, string, error) {
				arg := strings.ToLower(strings.TrimSpace(rest))
				if arg == "" {
					// No argument opens the picker, as every other no-argument
					// command in the app does. Printing the list instead made
					// the user read eight rows and then retype one of them.
					picked, ok := x.pick("Status line layout (type to filter, Esc to close)",
						x.layoutID(), layoutChoices())
					if !ok {
						return x.list("Status line layout", x.layoutID(), layoutChoices()), "", nil
					}
					if picked == "" {
						return "", "", nil // cancelled
					}
					arg = picked
				}
				if statusline.GetLayout(arg).ID != arg {
					return "", "", &choiceError{what: "layout", got: arg, options: layoutNames()}
				}
				if x.store != nil {
					if err := x.store.Set("layout", arg); err != nil {
						return "", "", err
					}
				}
				x.syncSampler()
				return "status line layout: " + arg, "", nil
			},
		},
		{
			Name:  "statuscolor",
			About: "Pick a status line color theme (opens a menu, or /statuscolor <name>)",
			Run: func(_ context.Context, _, rest string) (string, string, error) {
				arg := strings.ToLower(strings.TrimSpace(rest))
				if arg == "" {
					picked, ok := x.pick("Status line color (type to filter, Esc to close)",
						x.themeID(), themeChoices())
					if !ok {
						return x.list("Status line color", x.themeID(), themeChoices()), "", nil
					}
					if picked == "" {
						return "", "", nil // cancelled
					}
					arg = picked
				}
				if statusline.GetTheme(arg).ID != arg {
					return "", "", &choiceError{what: "theme", got: arg, options: themeNames()}
				}
				if x.store != nil {
					if err := x.store.Set("theme", arg); err != nil {
						return "", "", err
					}
				}
				return "status line color: " + arg, "", nil
			},
		},
	}
}

// choice is one option in a listing.
type choice struct{ id, about string }

func layoutChoices() []choice {
	out := make([]choice, 0, len(statusline.Layouts))
	for _, l := range statusline.Layouts {
		out = append(out, choice{l.ID, l.Description + "  →  " + l.Sample})
	}
	return out
}

func themeChoices() []choice {
	out := make([]choice, 0, len(statusline.Themes))
	for _, t := range statusline.Themes {
		out = append(out, choice{t.ID, t.Description})
	}
	return out
}

func layoutNames() []string {
	out := make([]string, 0, len(statusline.Layouts))
	for _, l := range statusline.Layouts {
		out = append(out, l.ID)
	}
	return out
}

func themeNames() []string {
	out := make([]string, 0, len(statusline.Themes))
	for _, t := range statusline.Themes {
		out = append(out, t.ID)
	}
	return out
}

// pick opens the host's picker. ok is false when there is no host — a
// headless run, or a gateway — and the caller falls back to printing.
func (x *statusThemes) pick(title, current string, options []choice) (string, bool) {
	host, ok := Ask()
	if !ok {
		return "", false
	}
	items := make([]Item, 0, len(options))
	for _, o := range options {
		items = append(items, Item{Value: o.id, Label: o.id, Description: o.about})
	}
	return host.Select(title, items, current), true
}

// list renders the options as text — the fallback when there is no picker to
// open, and what `run` mode and the gateways see.
func (x *statusThemes) list(title, active string, options []choice) string {
	width := 0
	for _, o := range options {
		width = max(width, len(o.id))
	}
	lines := []string{title + " — " + active + " is active"}
	for _, o := range options {
		marker := "  "
		if o.id == active {
			marker = "→ "
		}
		lines = append(lines, marker+pad(o.id, width)+"  "+o.about)
	}
	return strings.Join(lines, "\n")
}

func pad(s string, width int) string {
	if n := width - len([]rune(s)); n > 0 {
		return s + strings.Repeat(" ", n)
	}
	return s
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// choiceError names what was typed and what was available.
type choiceError struct {
	what, got string
	options   []string
}

func (e *choiceError) Error() string {
	return "unknown " + e.what + " \"" + e.got + "\". options: " + strings.Join(e.options, " | ")
}
