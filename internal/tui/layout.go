package tui

import (
	"math"
	"sort"
	"strings"
	"time"
)

// Layout, ported from loop's layout.ts / stack.ts / scroll-view.ts.
//
// Most components are plain: they render rows and the layout stacks them.
// Two are layout NODES — a Stack, which shares out a fixed height between
// its children by grow/shrink, and a ScrollView, which shows a window onto a
// child taller than the room it has. The renderer lays out a tree of these
// every frame, paints it into exactly one screen of rows, and keeps the boxes
// so a wheel event, a drag or a click can be mapped back to what is under it.

// Rect is a cell rectangle in screen coordinates.
type Rect struct{ X, Y, W, H int }

func intersect(a, b Rect) Rect {
	x, y := max(a.X, b.X), max(a.Y, b.Y)
	r, btm := min(a.X+a.W, b.X+b.W), min(a.Y+a.H, b.Y+b.H)
	return Rect{x, y, max(0, r-x), max(0, btm-y)}
}

func (r Rect) contains(x, y int) bool {
	return x >= r.X && x < r.X+r.W && y >= r.Y && y < r.Y+r.H
}

// StackEntry is one child of a Stack and how it takes part in sharing the
// stack's room. Basis < 0 means "its own height".
type StackEntry struct {
	Component Component
	Basis     int
	Grow      int
	// Shrink is the weight this entry gives up room with; a nil Shrink is 1,
	// and 0 means it never shrinks.
	Shrink  *int
	MinSize int
	MaxSize int // 0 = unbounded
	Visible func(w, h int) bool
}

// NoShrink is a Shrink of zero, for entries that must keep their height.
var NoShrink = func() *int { z := 0; return &z }()

func (e *StackEntry) shrink() int {
	if e.Shrink == nil {
		return 1
	}
	return *e.Shrink
}

func (e *StackEntry) maxSize() int {
	if e.MaxSize <= 0 {
		return math.MaxInt32
	}
	return e.MaxSize
}

// Stack lays its children out vertically (VStack) or horizontally.
type Stack struct {
	Entries    []*StackEntry
	Gap        int
	Horizontal bool
	// Align, for a horizontal stack: "stretch" (default), "start", "center", "end".
	Align string
}

// VStack builds a vertical stack.
func VStack(entries ...*StackEntry) *Stack { return &Stack{Entries: entries} }

// Entry is a StackEntry with natural height.
func Entry(c Component) *StackEntry { return &StackEntry{Component: c, Basis: -1} }

// Render, outside a layout, is the children one after another — which is
// what the exit print of a pinned frame needs: the whole document, unclipped.
func (s *Stack) Render(width int) []string {
	var out []string
	for _, e := range s.Entries {
		out = append(out, e.Component.Render(width)...)
	}
	return out
}

func (s *Stack) Invalidate() {
	for _, e := range s.Entries {
		if inv, ok := e.Component.(Invalidator); ok {
			inv.Invalidate()
		}
	}
}

func (s *Stack) visible(w, h int) []*StackEntry {
	var out []*StackEntry
	for _, e := range s.Entries {
		if e.Visible == nil || e.Visible(w, h) {
			out = append(out, e)
		}
	}
	return out
}

func clampSize(size int, e *StackEntry) int {
	lo := max(0, e.MinSize)
	hi := max(lo, e.maxSize())
	return max(lo, min(hi, max(0, size)))
}

func distribute(sizes []int, entries []*StackEntry, amount int, grow bool) {
	remaining := amount
	for remaining > 0 {
		type cand struct{ i, w int }
		var cands []cand
		total := 0
		for i, e := range entries {
			var ok bool
			var w int
			if grow {
				ok = e.Grow > 0 && sizes[i] < e.maxSize()
				w = e.Grow
			} else {
				ok = e.shrink() > 0 && sizes[i] > e.MinSize
				w = e.shrink() * max(1, sizes[i])
			}
			if ok {
				cands = append(cands, cand{i, w})
				total += w
			}
		}
		if len(cands) == 0 || total == 0 {
			return
		}
		moved := 0
		for _, c := range cands {
			if remaining <= 0 {
				break
			}
			e := entries[c.i]
			proposed := max(1, remaining*c.w/total)
			capacity := sizes[c.i] - e.MinSize
			if grow {
				capacity = e.maxSize() - sizes[c.i]
			}
			d := min(remaining, proposed, capacity)
			if d <= 0 {
				continue
			}
			if grow {
				sizes[c.i] += d
			} else {
				sizes[c.i] -= d
			}
			remaining -= d
			moved += d
		}
		if moved == 0 {
			return
		}
	}
}

// allocateStackSizes shares `avail` (or -1 for "natural") between entries.
func allocateStackSizes(entries []*StackEntry, intrinsic []int, avail, gap int) []int {
	sizes := make([]int, len(entries))
	for i, e := range entries {
		b := intrinsic[i]
		if e.Basis >= 0 {
			b = e.Basis
		}
		sizes[i] = clampSize(b, e)
	}
	if avail < 0 {
		return sizes
	}
	content := max(0, avail-max(0, len(entries)-1)*gap)
	total := 0
	for _, s := range sizes {
		total += s
	}
	if total < content {
		distribute(sizes, entries, content-total, true)
	} else if total > content {
		distribute(sizes, entries, total-content, false)
	}
	return sizes
}

// ScrollView shows a window onto one child. With FollowEnd it sticks to the
// end as the child grows, until the user scrolls away from it.
type ScrollView struct {
	child     Component
	FollowEnd bool
	Primary   bool
	// Contain stops a wheel event that hits this view's edge from scrolling
	// the view around it.
	Contain bool
	// Scrollbar: "hidden" (default), "auto" (shown while scrolling), "always".
	Scrollbar      string
	ScrollbarStyle func(string) string

	scrollTop        int
	contentHeight    int
	viewportHeight   int
	following        bool
	followSuppressed bool
	transientBar     bool
	barActive        bool
	barHide          *time.Timer
	requestRender    func()
}

// NewScrollView wraps child.
func NewScrollView(child Component, followEnd, primary bool) *ScrollView {
	return &ScrollView{
		child: child, FollowEnd: followEnd, following: followEnd, Primary: primary,
		Scrollbar:      "hidden",
		ScrollbarStyle: func(s string) string { return "\x1b[100m" + s + "\x1b[49m" },
	}
}

func (v *ScrollView) Child() Component     { return v.child }
func (v *ScrollView) ScrollTop() int       { return v.scrollTop }
func (v *ScrollView) IsFollowingEnd() bool { return v.following }
func (v *ScrollView) ViewportHeight() int  { return v.viewportHeight }
func (v *ScrollView) ContentHeight() int   { return v.contentHeight }
func (v *ScrollView) maxScrollTop() int    { return max(0, v.contentHeight-v.viewportHeight) }
func (v *ScrollView) contentWidth(w int) int {
	if v.Scrollbar == "always" && w > 1 {
		return w - 1
	}
	return w
}

func (v *ScrollView) Render(width int) []string {
	cw := v.contentWidth(width)
	lines := v.child.Render(cw)
	if cw == width {
		return lines
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = l + " "
	}
	return out
}

func (v *ScrollView) Invalidate() {
	if inv, ok := v.child.(Invalidator); ok {
		inv.Invalidate()
	}
}

func (v *ScrollView) scrollbarVisible() bool {
	if v.Scrollbar == "always" {
		return v.viewportHeight > 0
	}
	return v.Scrollbar == "auto" && v.contentHeight > v.viewportHeight && v.transientBar
}

func (v *ScrollView) markActivity() {
	if v.Scrollbar != "auto" || v.contentHeight <= v.viewportHeight {
		return
	}
	v.transientBar = true
	if v.barHide != nil {
		v.barHide.Stop()
		v.barHide = nil
	}
	if v.barActive {
		return
	}
	rr := v.requestRender
	v.barHide = time.AfterFunc(time.Second, func() {
		if rr != nil {
			rr()
		}
	})
}

// expireScrollbar is run on the render goroutine when the hide timer fires.
func (v *ScrollView) expireScrollbar() {
	if v.barHide != nil || v.barActive {
		return
	}
	v.transientBar = false
}

func (v *ScrollView) setScrollbarActive(on bool) {
	if on == v.barActive {
		return
	}
	v.barActive = on
	v.markActivity()
}

func (v *ScrollView) notify() {
	if v.requestRender != nil {
		v.requestRender()
	}
}

// ScrollTo moves the window. disableFollow keeps follow-end off even when
// the target is the end (a search hit on the last row should not re-arm it).
func (v *ScrollView) ScrollTo(top int, disableFollow bool) {
	mx := v.maxScrollTop()
	next := max(0, min(mx, top))
	suppressed := disableFollow && next == mx
	following := !suppressed && v.FollowEnd && next == mx
	if next == v.scrollTop && following == v.following && suppressed == v.followSuppressed {
		return
	}
	moved := next != v.scrollTop
	v.scrollTop, v.following, v.followSuppressed = next, following, suppressed
	if moved {
		v.markActivity()
	}
	v.notify()
}

// ScrollBy moves by n rows and returns how much of n it could NOT use, so a
// wheel event at the edge of a nested view can carry on to the one outside.
func (v *ScrollView) ScrollBy(n int) int {
	if n == 0 {
		return 0
	}
	mx := v.maxScrollTop()
	start := v.scrollTop
	if v.following {
		start = mx
	}
	next := max(0, min(mx, start+n))
	moved := next - start
	was := v.following
	v.scrollTop = next
	v.following = v.FollowEnd && next == mx
	v.followSuppressed = false
	if moved != 0 {
		v.markActivity()
	}
	if moved != 0 || v.following != was {
		v.notify()
	}
	return n - moved
}

func (v *ScrollView) ScrollToStart() {
	following := v.FollowEnd && v.contentHeight <= v.viewportHeight
	changed := v.scrollTop != 0 || v.following != following
	v.scrollTop, v.following, v.followSuppressed = 0, following, false
	if changed {
		v.markActivity()
		v.notify()
	}
}

func (v *ScrollView) ScrollToEnd() {
	next := v.maxScrollTop()
	changed := v.scrollTop != next || v.following != v.FollowEnd
	v.scrollTop, v.following, v.followSuppressed = next, v.FollowEnd, false
	if changed {
		v.markActivity()
		v.notify()
	}
}

func (v *ScrollView) updateLayout(content, viewport int, rr func()) {
	v.contentHeight = max(0, content)
	v.viewportHeight = max(0, viewport)
	v.requestRender = rr
	mx := v.maxScrollTop()
	if v.following {
		v.scrollTop = mx
	} else {
		v.scrollTop = max(0, min(v.scrollTop, mx))
	}
	if v.scrollTop < mx {
		v.followSuppressed = false
	}
	if v.FollowEnd && v.scrollTop == mx && !v.followSuppressed {
		v.following = true
	}
	if v.contentHeight <= v.viewportHeight {
		v.transientBar = false
		if v.barHide != nil {
			v.barHide.Stop()
			v.barHide = nil
		}
	}
}

// layoutBox is one laid-out component.
type layoutBox struct {
	component   Component
	rect, clip  Rect
	children    []*layoutBox
	lines       []string
	lineOffset  int
	scrollView  *ScrollView
	scrollLines []string
}

// layoutFrame is one laid-out, painted screen.
type layoutFrame struct {
	root          *layoutBox
	width, height int
	lines         []string
	primary       *ScrollView
}

type layoutCtx struct {
	w, h          int
	cache         map[Component]map[int][]string
	requestRender func()
	primary       *ScrollView
}

func (c *layoutCtx) render(comp Component, width int) []string {
	width = max(1, width)
	byW := c.cache[comp]
	if byW == nil {
		byW = map[int][]string{}
		c.cache[comp] = byW
	}
	if l, ok := byW[width]; ok {
		return l
	}
	l := comp.Render(width)
	byW[width] = l
	return l
}

func translateBox(b *layoutBox, dy int) {
	b.rect.Y += dy
	for _, ch := range b.children {
		translateBox(ch, dy)
	}
}

func updateClips(b *layoutBox, parent Rect) {
	b.clip = intersect(parent, b.rect)
	for _, ch := range b.children {
		updateClips(ch, b.clip)
	}
}

// layoutComponent lays comp out at (x, y). height < 0 means natural height.
func (c *layoutCtx) layout(comp Component, x, y, width, height int, clip Rect) *layoutBox {
	width = max(1, width)
	switch n := comp.(type) {
	case *ScrollView:
		prev := n.scrollTop
		cw := n.contentWidth(width)
		child := c.layout(n.child, x, y-prev, cw, -1, clip)
		content := child.rect.H
		viewport := content
		if height >= 0 {
			viewport = height
		}
		n.updateLayout(content, viewport, c.requestRender)
		translateBox(child, prev-n.scrollTop)
		if n.Primary || c.primary == nil {
			c.primary = n
		}
		rect := Rect{x, y, width, viewport}
		box := &layoutBox{
			component: comp, rect: rect, clip: intersect(clip, rect),
			children: []*layoutBox{child}, scrollView: n,
			scrollLines: c.render(n.child, cw),
		}
		updateClips(child, box.clip)
		return box

	case *Stack:
		entries := n.visible(c.w, c.h)
		gapTotal := max(0, len(entries)-1) * n.Gap
		if !n.Horizontal {
			intrinsic := make([]int, len(entries))
			for i, e := range entries {
				if e.Basis >= 0 {
					intrinsic[i] = e.Basis
				} else {
					intrinsic[i] = len(c.render(e.Component, width))
				}
			}
			sizes := allocateStackSizes(entries, intrinsic, height, n.Gap)
			natural := gapTotal
			for _, s := range sizes {
				natural += s
			}
			h := natural
			if height >= 0 {
				h = height
			}
			rect := Rect{x, y, width, h}
			box := &layoutBox{component: comp, rect: rect, clip: intersect(clip, rect)}
			cy := y
			for i, e := range entries {
				box.children = append(box.children, c.layout(e.Component, x, cy, width, sizes[i], box.clip))
				cy += sizes[i] + n.Gap
			}
			return box
		}
		intrinsicW := make([]int, len(entries))
		for i, e := range entries {
			if e.Basis >= 0 {
				intrinsicW[i] = e.Basis
				continue
			}
			for _, l := range c.render(e.Component, width) {
				intrinsicW[i] = max(intrinsicW[i], visibleWidth(l))
			}
		}
		widths := allocateStackSizes(entries, intrinsicW, width, n.Gap)
		heights := make([]int, len(entries))
		h := 0
		for i, e := range entries {
			heights[i] = len(c.render(e.Component, max(1, widths[i])))
			h = max(h, heights[i])
		}
		if height >= 0 {
			h = height
		}
		rect := Rect{x, y, width, h}
		box := &layoutBox{component: comp, rect: rect, clip: intersect(clip, rect)}
		cx := x
		for i, e := range entries {
			ch := min(h, heights[i])
			if n.Align == "" || n.Align == "stretch" {
				ch = h
			}
			cy := y
			switch n.Align {
			case "center":
				cy += (h - ch) / 2
			case "end":
				cy += h - ch
			}
			if widths[i] == 0 {
				box.children = append(box.children, &layoutBox{component: e.Component, rect: Rect{cx, cy, 0, ch}, clip: Rect{cx, cy, 0, 0}})
			} else {
				box.children = append(box.children, c.layout(e.Component, cx, cy, widths[i], ch, box.clip))
			}
			cx += widths[i] + n.Gap
		}
		return box
	}

	lines := c.render(comp, width)
	h := len(lines)
	if height >= 0 {
		h = height
	}
	offset := 0
	// A component taller than its box shows its top — unless the cursor is
	// below the fold, in which case it scrolls just far enough to show it.
	if len(lines) > h && h > 0 {
		for i, l := range lines {
			if strings.Contains(l, CursorMarker) {
				if i >= h {
					offset = i - h + 1
				}
				break
			}
		}
	}
	rect := Rect{x, y, width, h}
	return &layoutBox{component: comp, rect: rect, clip: intersect(clip, rect), lines: lines, lineOffset: offset}
}

// scrollbarGeometry is where a scroll view's thumb sits.
type scrollbarGeometry struct {
	column, trackTop, trackHeight, thumbTop, thumbHeight, maxScrollTop int
}

func getScrollbarGeometry(b *layoutBox) (scrollbarGeometry, bool) {
	v := b.scrollView
	if v == nil || !v.scrollbarVisible() || b.rect.W <= 0 || b.rect.H <= 0 {
		return scrollbarGeometry{}, false
	}
	content := len(b.scrollLines)
	if len(b.children) > 0 {
		content = b.children[0].rect.H
	}
	track := b.rect.H
	minThumb := min(2, track)
	thumb := max(minThumb, min(track, int(math.Round(float64(track*track)/float64(max(1, content))))))
	mx := max(0, content-track)
	maxThumbTop := track - thumb
	off := 0
	if mx > 0 {
		off = int(math.Round(float64(v.scrollTop) / float64(mx) * float64(maxThumbTop)))
	}
	col := b.rect.X + b.rect.W - 1
	if col < b.clip.X || col >= b.clip.X+b.clip.W {
		return scrollbarGeometry{}, false
	}
	return scrollbarGeometry{col, b.rect.Y, track, b.rect.Y + off, thumb, mx}, true
}

func styleCell(line string, column, total int, style func(string) string) string {
	start, end, ok := graphemeCellRange(line, column)
	if !ok {
		start, end = column, column+1
	}
	before := sliceByColumn(line, 0, start, true)
	target := sliceByColumn(line, start, end-start, true)
	after := sliceByColumn(line, end, max(0, total-end), true)
	prefix := ""
	i := 0
	for i < len(target) {
		code, n := extractAnsiCode(target, i)
		if n == 0 {
			break
		}
		prefix += code
		i += n
	}
	text := target[i:]
	if text == "" {
		text = strings.Repeat(" ", end-start)
	}
	return before + strings.Repeat(" ", max(0, start-visibleWidth(before))) + prefix + style(text) + after
}

func paintBox(b *layoutBox, screen []string, total int) {
	if b.lines != nil {
		first := max(b.rect.Y, b.clip.Y, 0)
		last := min(b.rect.Y+b.rect.H, b.clip.Y+b.clip.H, len(screen))
		for row := first; row < last; row++ {
			i := b.lineOffset + row - b.rect.Y
			if i < 0 || i >= len(b.lines) {
				continue
			}
			line := b.lines[i]
			if b.rect.X == 0 && b.rect.W >= total && screen[row] == "" {
				screen[row] = line
			} else {
				screen[row] = compositeLine(screen[row], line, b.rect.X, b.rect.W, total)
			}
		}
	}
	for _, ch := range b.children {
		paintBox(ch, screen, total)
	}
	if g, ok := getScrollbarGeometry(b); ok {
		for off := 0; off < g.thumbHeight; off++ {
			row := g.thumbTop + off
			if row < b.clip.Y || row >= b.clip.Y+b.clip.H || row < 0 || row >= len(screen) {
				continue
			}
			screen[row] = styleCell(screen[row], g.column, total, b.scrollView.ScrollbarStyle)
		}
	}
}

func renderLayoutFrame(root Component, width, height int, rr func()) *layoutFrame {
	width, height = max(1, width), max(1, height)
	c := &layoutCtx{w: width, h: height, cache: map[Component]map[int][]string{}, requestRender: rr}
	box := c.layout(root, 0, 0, width, height, Rect{0, 0, width, height})
	lines := make([]string, height)
	paintBox(box, lines, width)
	return &layoutFrame{root: box, width: width, height: height, lines: lines, primary: c.primary}
}

func (f *layoutFrame) boxOf(v *ScrollView) *layoutBox {
	var visit func(b *layoutBox) *layoutBox
	visit = func(b *layoutBox) *layoutBox {
		if b.scrollView == v {
			return b
		}
		for _, ch := range b.children {
			if m := visit(ch); m != nil {
				return m
			}
		}
		return nil
	}
	return visit(f.root)
}

// scrollViewsAt lists the scroll views under a cell, innermost first.
func (f *layoutFrame) scrollViewsAt(x, y int) []*ScrollView {
	type hit struct {
		v     *ScrollView
		depth int
	}
	var hits []hit
	var visit func(b *layoutBox, d int)
	visit = func(b *layoutBox, d int) {
		if !b.clip.contains(x, y) {
			return
		}
		if b.scrollView != nil && b.rect.contains(x, y) {
			hits = append(hits, hit{b.scrollView, d})
		}
		for _, ch := range b.children {
			visit(ch, d+1)
		}
	}
	visit(f.root, 0)
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].depth > hits[j].depth })
	out := make([]*ScrollView, len(hits))
	for i, h := range hits {
		out[i] = h.v
	}
	return out
}
