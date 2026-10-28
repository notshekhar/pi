package tui

import "strings"

// The component model, ported from loop's packages/tui (pi-mono's split TUI).
//
// A component renders itself to rows for a width. Rendering is PURE: the
// renderer measures by rendering (the transcript's reserve, an overlay's
// anchor, a click mapped to a row), so a render that changed state would
// change what it measured. Animation advances on the clock, never on render.

// Component is anything that can draw itself into rows of a given width.
type Component interface {
	Render(width int) []string
}

// Invalidator is a component holding render caches that a theme change or a
// resize must drop.
type Invalidator interface {
	Invalidate()
}

// CursorMarker is emitted by a focused component at the cursor position. The
// renderer finds it, strips it, and parks the hardware cursor there — an APC
// sequence, which terminals ignore, so a stray one costs nothing.
const CursorMarker = "\x1b_pi:c\x07"

// Func adapts a render function into a Component. It is a pointer so it is
// comparable — the layout caches renders per component within a frame.
type Func struct {
	Fn func(width int) []string
}

// NewFunc wraps fn as a Component.
func NewFunc(fn func(width int) []string) *Func { return &Func{Fn: fn} }

func (f *Func) Render(width int) []string {
	if f.Fn == nil {
		return nil
	}
	return f.Fn(width)
}

// Container renders its children one after another.
type Container struct {
	Children []Component
}

func (c *Container) Add(children ...Component) { c.Children = append(c.Children, children...) }

func (c *Container) Remove(child Component) {
	for i, ch := range c.Children {
		if ch == child {
			c.Children = append(c.Children[:i], c.Children[i+1:]...)
			return
		}
	}
}

func (c *Container) Clear() { c.Children = nil }

func (c *Container) Render(width int) []string {
	var out []string
	for _, ch := range c.Children {
		out = append(out, ch.Render(width)...)
	}
	return out
}

func (c *Container) Invalidate() {
	for _, ch := range c.Children {
		if inv, ok := ch.(Invalidator); ok {
			inv.Invalidate()
		}
	}
}

// Spacer is n blank rows.
type Spacer struct{ N int }

func (s *Spacer) Render(int) []string { return make([]string, s.N) }

// extractCursor finds the cursor marker in the bottom `height` rows, strips
// it, and reports where it was.
func extractCursor(lines []string, height int) (row, col int, ok bool) {
	top := max(0, len(lines)-height)
	for r := len(lines) - 1; r >= top; r-- {
		i := strings.Index(lines[r], CursorMarker)
		if i < 0 {
			continue
		}
		col = visibleWidth(lines[r][:i])
		lines[r] = lines[r][:i] + lines[r][i+len(CursorMarker):]
		return r, col, true
	}
	return 0, 0, false
}
