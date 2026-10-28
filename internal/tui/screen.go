package tui

import (
	"encoding/base64"
	"math"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Screen is loop's alternate-screen renderer (TuiAltScreen), ported.
//
// The chat lives on the ALTERNATE screen, in a viewport the app owns. The
// document is always the same two blocks — transcript, then chrome — and the
// screen shows it one of two ways:
//
//	flowing  an implicit follow-end scroll view over the whole document. A
//	         short session reads top-down with the prompt right under the last
//	         message; a long one keeps the prompt on the last rows; scrolling
//	         back moves the page, prompt included.
//	pinned   a layout root: the transcript in its own scroll view taking
//	         every row the chrome does not, the chrome held on the last rows.
//
// Menus and pickers are OVERLAYS painted over the frame. Appended to it they
// made it taller for as long as they were open, which on a full screen
// scrolled rows away that never came back when they closed. Painted over it,
// they cost the frame no height at all.
//
// Every frame is a full screen of rows diffed against the last one by row,
// so a frame rewrites only what changed. On exit the document is printed
// into the normal screen, so the conversation survives in scrollback.
type Screen struct {
	write func(string)
	size  func() (int, int)

	// Children is the implicit document, in flowing mode.
	Children Container

	layoutRoot Component
	implicit   *ScrollView
	current    *layoutFrame

	overlays   []*Overlay
	orderCount int

	prev        []string
	prevW       int
	prevH       int
	active      bool
	contentRows int
	fullRedraws int

	// ShowCursor shows the hardware cursor where the focused component put
	// the marker. Off, the cursor is parked but hidden (the editor draws its
	// own block cursor).
	ShowCursor bool

	// RequestRender asks the owner for a frame; it is how timers (a flash
	// expiring, the scrollbar fading) reach the render loop. Must not block.
	RequestRender func()
	// OnClick is told about a primary-button click: pressed and released on
	// the same cell, no drag, not on a link. 0-based cells.
	OnClick func(x, y int)
	// OpenURL activates an OSC 8 link that was clicked.
	OpenURL func(url string)
	// CopySelection copies selected text to the clipboard; nil falls back to
	// an OSC 52 write.
	CopySelection func(text string) bool
	// ScrollToEndIndicator renders the clickable "jump to newest" label shown
	// on the last row of the primary view while it is scrolled back.
	ScrollToEndIndicator func() string
	// MouseInterceptor gets first refusal on a mouse event (1-based coords).
	MouseInterceptor func(ev MouseEvent) bool

	CopyOnSelect bool
	// WheelLines is the rows one wheel event scrolls; 0 accelerates by
	// event velocity instead (pi-mono's "auto").
	WheelLines int
	mouse      bool
	mouseSeq   string

	wheel          wheelAccel
	wheelHoldUntil time.Time

	// selection
	selAnchor, selFocus *selPoint
	selGranularity      int // 0 char, 1 word, 2 line
	selInitial          *selRange
	lastClick           *clickTarget
	selPressActive      bool
	selDragged          bool
	pressedURL          string
	autoScrollDir       int
	autoScrollPointer   [2]int
	autoScrollTicker    *time.Ticker
	autoScrollStop      chan struct{}

	scrollbarDrag  *scrollbarDrag
	scrollbarHover *ScrollView
	endIndicator   *Rect

	flashes []flashEntry
	flashID int
}

const (
	syncBegin          = "\x1b[?2026h"
	syncEnd            = "\x1b[?2026l"
	enterAltScreen     = "\x1b[?1049h"
	exitAltScreen      = "\x1b[?1049l"
	disableAutowrap    = "\x1b[?7l"
	enableAutowrap     = "\x1b[?7h"
	mouseButtonMove    = "\x1b[?1000h\x1b[?1002h\x1b[?1004h\x1b[?1006h"
	mouseAllMove       = "\x1b[?1000h\x1b[?1002h\x1b[?1003h\x1b[?1004h\x1b[?1006h"
	mouseOff           = "\x1b[?1006l\x1b[?1004l\x1b[?1003l\x1b[?1002l\x1b[?1000l"
	pageScrollOverlap  = 4
	altWheelMultiplier = 5
	// wheelYieldsToKeyboard: a trackpad keeps sending wheel-up for a few
	// hundred ms after the fingers lift. A person who scrolls back and starts
	// typing gets the jump to the end on the first key, then the tail drags
	// the view up again. Terminals send no scroll phase, so time is the only
	// way to tell the tail from a hand on the wheel.
	wheelYieldsToKeyboard = 400 * time.Millisecond
	doubleClickInterval   = 500 * time.Millisecond
)

// NewScreen builds a renderer writing to write and measuring with size.
func NewScreen(write func(string), size func() (int, int)) *Screen {
	s := &Screen{write: write, size: size, mouse: true, CopyOnSelect: true}
	s.implicit = NewScrollView(&s.Children, true, true)
	s.wheel = newWheelAccel()
	return s
}

func (s *Screen) requestRender() {
	if s.RequestRender != nil {
		s.RequestRender()
	}
}

// SetLayoutRoot switches between flowing (nil) and an author-owned layout.
func (s *Screen) SetLayoutRoot(c Component) {
	if s.layoutRoot == c {
		return
	}
	s.layoutRoot = c
	s.current = nil
	s.requestRender()
}

func (s *Screen) primary() *ScrollView {
	if s.current != nil && s.current.primary != nil {
		return s.current.primary
	}
	return s.implicit
}

// ViewportTop is the primary view's scroll position.
func (s *Screen) ViewportTop() int { return s.primary().scrollTop }

// IsFollowingOutput reports whether the primary view is stuck to the end.
func (s *Screen) IsFollowingOutput() bool { return s.primary().following }

// RowsBelowContent is the rows between the end of the document and the bottom
// of the screen, as drawn last frame: positive when the document is shorter
// than the screen, NEGATIVE when it is scrolled back and its end is below the
// screen. Overlays anchored to the prompt anchor to this.
func (s *Screen) RowsBelowContent() int {
	if s.layoutRoot != nil {
		return 0
	}
	_, h := s.size()
	return h - (s.contentRows - s.implicit.scrollTop)
}

// ContentHeight is where the frame's content ends.
func (s *Screen) ContentHeight() int { return s.contentRows }

// FullRedraws counts frames that repainted every row.
func (s *Screen) FullRedraws() int { return s.fullRedraws }

// Start enters the alternate screen.
func (s *Screen) Start(mouse bool) {
	s.mouse = mouse
	s.active = true
	s.prev = nil
	s.selAnchor, s.selFocus, s.selInitial, s.lastClick = nil, nil, nil, nil
	term := strings.ToLower(os.Getenv("TERM"))
	if os.Getenv("TMUX") != "" || os.Getenv("ZELLIJ") != "" || os.Getenv("STY") != "" ||
		strings.HasPrefix(term, "tmux") || strings.HasPrefix(term, "screen") {
		// Multiplexers lag when every pointer movement is forwarded.
		s.mouseSeq = mouseButtonMove
	} else {
		s.mouseSeq = mouseAllMove
	}
	seq := enterAltScreen + disableAutowrap
	if s.mouse {
		seq += s.mouseSeq
	}
	s.write(seq + "\x1b[2J\x1b[H\x1b[?25l")
}

// SetMouse turns mouse capture on or off while running. Off hands the wheel
// and drag-selection back to the terminal.
func (s *Screen) SetMouse(on bool) {
	if on == s.mouse || !s.active {
		s.mouse = on
		return
	}
	s.mouse = on
	if on {
		s.write(s.mouseSeq)
	} else {
		s.write(mouseOff)
	}
}

// Stop leaves the alternate screen. With printDocument the whole document is
// printed into the normal screen on the way out, so the conversation stays in
// the terminal's scrollback.
func (s *Screen) Stop(printDocument bool) {
	s.stopAutoScroll()
	if !s.active {
		return
	}
	s.active = false
	pre := syncBegin
	if s.mouse {
		pre += mouseOff
	}
	pre += enableAutowrap + syncEnd
	s.write(pre)
	if !printDocument {
		s.write(syncBegin + exitAltScreen + "\x1b[?25h" + syncEnd)
		return
	}
	w, _ := s.size()
	doc := s.exitDocument(max(1, w))
	var b strings.Builder
	b.WriteString(syncBegin + exitAltScreen + disableAutowrap)
	for i, l := range doc {
		if i > 0 {
			b.WriteString("\r\n")
		}
		b.WriteString("\r\x1b[2K")
		b.WriteString(l)
	}
	b.WriteString("\x1b[0m" + enableAutowrap + "\r\n\x1b[?25h" + syncEnd)
	s.write(b.String())
}

func (s *Screen) exitDocument(width int) (out []string) {
	defer func() {
		if r := recover(); r != nil {
			logRenderPanic(r)
			out = nil
		}
	}()
	var lines []string
	if s.layoutRoot != nil {
		lines = s.layoutRoot.Render(width)
	} else {
		lines = s.Children.Render(width)
	}
	out = make([]string, len(lines))
	for i, l := range lines {
		l = strings.ReplaceAll(l, CursorMarker, "")
		l = normalizeTerminalOutput(l) + segmentReset
		if visibleWidth(l) > width {
			l = sliceByColumn(l, 0, width, true)
		}
		out[i] = l
	}
	return out
}

// Invalidate forgets what is on screen, so the next frame repaints every row.
func (s *Screen) Invalidate() {
	s.prev = nil
	s.prevW, s.prevH = 0, 0
}

// Resized is called on SIGWINCH: the cached screen is no longer a safe diff
// baseline, but scroll state is kept so input before the repaint still
// targets the current view.
func (s *Screen) Resized() { s.prev = nil }

// ScrollBy scrolls the primary view.
func (s *Screen) ScrollBy(n int) {
	s.primary().ScrollBy(n)
	s.requestRender()
}

func (s *Screen) ScrollToTop() {
	s.primary().ScrollToStart()
	s.requestRender()
}

// ScrollToBottom is a keyboard-driven jump to the end. The wheel yields to it
// for a moment so a trackpad's momentum tail cannot drag the view back up.
func (s *Screen) ScrollToBottom() {
	s.primary().ScrollToEnd()
	s.wheelHoldUntil = time.Now().Add(wheelYieldsToKeyboard)
	s.requestRender()
}

// PageUp / PageDown move a screen less a few rows of overlap.
func (s *Screen) PageUp() { s.ScrollBy(-max(1, s.primary().viewportHeight-pageScrollOverlap)) }
func (s *Screen) PageDown() {
	s.ScrollBy(max(1, s.primary().viewportHeight-pageScrollOverlap))
}

// NoteKey extends the wheel hold while the user is typing inside it.
func (s *Screen) NoteKey() {
	if now := time.Now(); now.Before(s.wheelHoldUntil) {
		s.wheelHoldUntil = now.Add(wheelYieldsToKeyboard)
	}
}

// ---- overlays ----

// Size is an absolute cell count, or a percentage when Pct is set.
type SizeValue struct {
	N   int
	Pct float64
	Set bool
}

func Cells(n int) SizeValue       { return SizeValue{N: n, Set: true} }
func Percent(p float64) SizeValue { return SizeValue{Pct: p, Set: true} }

func (v SizeValue) resolve(ref int) (int, bool) {
	if !v.Set {
		return 0, false
	}
	if v.Pct > 0 {
		return int(math.Floor(float64(ref) * v.Pct / 100)), true
	}
	return v.N, true
}

// Margin is distance kept from the screen's edges.
type Margin struct{ Top, Right, Bottom, Left int }

// OverlayOptions positions an overlay. The zero value is a centered box.
type OverlayOptions struct {
	Width     SizeValue
	MinWidth  int
	MaxHeight SizeValue
	// Anchor: center (default), top-left, top-right, bottom-left,
	// bottom-right, top-center, bottom-center, left-center, right-center.
	Anchor           string
	OffsetX, OffsetY int
	Row, Col         SizeValue
	// Margin is read once per composite, so an overlay can stay anchored to
	// something whose place in the frame moves — a menu held above a prompt
	// that grows with the draft.
	Margin  func() Margin
	Visible func(w, h int) bool
}

// Overlay is a component painted over the frame.
type Overlay struct {
	s         *Screen
	Component Component
	Options   OverlayOptions
	hidden    bool
	order     int
	bounds    *Rect
}

// ShowOverlay paints c over the frame until the handle is hidden.
func (s *Screen) ShowOverlay(c Component, opts OverlayOptions) *Overlay {
	s.orderCount++
	o := &Overlay{s: s, Component: c, Options: opts, order: s.orderCount}
	s.overlays = append(s.overlays, o)
	s.requestRender()
	return o
}

// Hide removes the overlay for good.
func (o *Overlay) Hide() {
	for i, x := range o.s.overlays {
		if x == o {
			o.s.overlays = append(o.s.overlays[:i], o.s.overlays[i+1:]...)
			o.s.requestRender()
			return
		}
	}
}

// SetHidden hides or shows the overlay without removing it.
func (o *Overlay) SetHidden(h bool) {
	if o.hidden == h {
		return
	}
	o.hidden = h
	if !h {
		o.s.orderCount++
		o.order = o.s.orderCount
	}
	o.s.requestRender()
}

// Bounds is where the overlay was last painted.
func (o *Overlay) Bounds() (Rect, bool) {
	if o.bounds == nil || !o.visible() {
		return Rect{}, false
	}
	return *o.bounds, true
}

func (o *Overlay) visible() bool {
	if o.hidden {
		return false
	}
	if o.Options.Visible != nil {
		w, h := o.s.size()
		return o.Options.Visible(w, h)
	}
	return true
}

// HasOverlay reports whether any overlay is showing.
func (s *Screen) HasOverlay() bool {
	for _, o := range s.overlays {
		if o.visible() {
			return true
		}
	}
	return false
}

func (s *Screen) resolveOverlay(opt OverlayOptions, oh, tw, th int) (width, row, col, maxH int, hasMax bool) {
	var m Margin
	if opt.Margin != nil {
		m = opt.Margin()
	}
	mt, mr, mb, ml := max(0, m.Top), max(0, m.Right), max(0, m.Bottom), max(0, m.Left)
	aw := max(1, tw-ml-mr)
	ah := max(1, th-mt-mb)
	width = min(80, aw)
	if w, ok := opt.Width.resolve(tw); ok {
		width = w
	}
	if opt.MinWidth > 0 {
		width = max(width, opt.MinWidth)
	}
	width = max(1, min(width, aw))
	if mh, ok := opt.MaxHeight.resolve(th); ok {
		maxH, hasMax = max(1, min(mh, ah)), true
	}
	eff := oh
	if hasMax {
		eff = min(oh, maxH)
	}
	anchor := opt.Anchor
	if anchor == "" {
		anchor = "center"
	}
	if opt.Row.Set {
		if opt.Row.Pct > 0 {
			row = mt + int(math.Floor(float64(max(0, ah-eff))*opt.Row.Pct/100))
		} else {
			row = opt.Row.N
		}
	} else {
		switch anchor {
		case "top-left", "top-center", "top-right":
			row = mt
		case "bottom-left", "bottom-center", "bottom-right":
			row = mt + ah - eff
		default:
			row = mt + (ah-eff)/2
		}
	}
	if opt.Col.Set {
		if opt.Col.Pct > 0 {
			col = ml + int(math.Floor(float64(max(0, aw-width))*opt.Col.Pct/100))
		} else {
			col = opt.Col.N
		}
	} else {
		switch anchor {
		case "top-left", "left-center", "bottom-left":
			col = ml
		case "top-right", "right-center", "bottom-right":
			col = ml + aw - width
		default:
			col = ml + (aw-width)/2
		}
	}
	row += opt.OffsetY
	col += opt.OffsetX
	row = max(mt, min(row, th-mb-eff))
	col = max(ml, min(col, tw-mr-width))
	return
}

func (s *Screen) compositeOverlays(lines []string, tw, th int) []string {
	if len(s.overlays) == 0 {
		return lines
	}
	result := append([]string{}, lines...)
	var vis []*Overlay
	for _, o := range s.overlays {
		o.bounds = nil
		if o.visible() {
			vis = append(vis, o)
		}
	}
	// Later focus order paints on top.
	for i := 1; i < len(vis); i++ {
		for j := i; j > 0 && vis[j].order < vis[j-1].order; j-- {
			vis[j], vis[j-1] = vis[j-1], vis[j]
		}
	}
	type painted struct {
		lines       []string
		row, col, w int
	}
	var out []painted
	need := len(result)
	for _, o := range vis {
		w, _, _, mh, hasMax := s.resolveOverlay(o.Options, 0, tw, th)
		ol := o.Component.Render(w)
		if hasMax && len(ol) > mh {
			ol = ol[:mh]
		}
		_, row, col, _, _ := s.resolveOverlay(o.Options, len(ol), tw, th)
		o.bounds = &Rect{col, row, w, len(ol)}
		out = append(out, painted{ol, row, col, w})
		need = max(need, row+len(ol))
	}
	working := max(len(result), th, need)
	for len(result) < working {
		result = append(result, "")
	}
	start := max(0, working-th)
	for _, p := range out {
		for i, l := range p.lines {
			idx := start + p.row + i
			if idx < 0 || idx >= len(result) {
				continue
			}
			if visibleWidth(l) > p.w {
				l = sliceByColumn(l, 0, p.w, true)
			}
			result[idx] = compositeLine(result[idx], l, p.col, p.w, tw)
		}
	}
	return result
}

// HitTestOverlay finds the topmost overlay under a 0-based cell.
func (s *Screen) HitTestOverlay(x, y int) (*Overlay, int, int, bool) {
	for i := len(s.overlays) - 1; i >= 0; i-- {
		o := s.overlays[i]
		if r, ok := o.Bounds(); ok && r.contains(x, y) {
			return o, x - r.X, y - r.Y, true
		}
	}
	return nil, 0, 0, false
}

// ---- flashes ----

type flashEntry struct {
	id      int
	message string
	until   time.Time
}

// Flash shows a transient message in the top-right corner.
func (s *Screen) Flash(message string, d time.Duration) {
	if d <= 0 {
		d = time.Second
	}
	s.flashID++
	s.flashes = append(s.flashes, flashEntry{s.flashID, message, time.Now().Add(d)})
	time.AfterFunc(d+10*time.Millisecond, s.requestRender)
	s.requestRender()
}

func (s *Screen) compositeFlashes(screen []string, width, height int) []string {
	now := time.Now()
	live := s.flashes[:0]
	for _, f := range s.flashes {
		if now.Before(f.until) {
			live = append(live, f)
		}
	}
	s.flashes = live
	if len(live) == 0 {
		return screen
	}
	result := append([]string{}, screen...)
	for len(result) < height {
		result = append(result, "")
	}
	for row, f := range live {
		if row >= height {
			break
		}
		line := "\x1b[7m" + truncateToWidth(" "+f.message+" ", width, "") + "\x1b[27m"
		fw := visibleWidth(line)
		if fw == 0 {
			continue
		}
		result[row] = compositeLine(result[row], line, width-fw, fw, width)
	}
	return result
}

// ---- render ----

// Render composes and writes one frame.
func (s *Screen) Render() {
	if !s.active {
		return
	}
	width, height := s.size()
	width, height = max(1, width), max(1, height)
	var root Component = s.implicit
	if s.layoutRoot != nil {
		root = s.layoutRoot
	}
	for _, v := range []*ScrollView{s.implicit, s.primary()} {
		v.expireScrollbar()
	}
	frame := renderLayoutFrame(root, width, height, s.requestRender)
	if s.layoutRoot != nil {
		s.contentRows = len(frame.lines)
	} else {
		s.contentRows = s.implicit.contentHeight
	}
	screen := frame.lines
	screen = s.compositeEndIndicator(screen, frame, width)
	screen = s.compositeOverlays(screen, width, height)
	if len(screen) > height {
		screen = screen[len(screen)-height:]
	}
	s.current = frame
	screen = s.applySelection(screen)
	screen = s.compositeFlashes(screen, width, height)

	curRow, curCol, hasCursor := extractCursor(screen, height)
	for i, l := range screen {
		l = normalizeTerminalOutput(l) + segmentReset
		if visibleWidth(l) > width {
			l = sliceByColumn(l, 0, width, true)
		}
		screen[i] = l
	}

	full := len(s.prev) == 0 || s.prevW != width || s.prevH != height
	var b strings.Builder
	b.WriteString(syncBegin)
	if full {
		s.fullRedraws++
		b.WriteString("\x1b[2J")
	}
	for row := 0; row < height; row++ {
		line := ""
		if row < len(screen) {
			line = screen[row]
		}
		if !full && row < len(s.prev) && line == s.prev[row] {
			continue
		}
		b.WriteString("\x1b[")
		b.WriteString(strconv.Itoa(row + 1))
		b.WriteString(";1H\x1b[2K")
		b.WriteString(line)
	}
	if hasCursor {
		b.WriteString("\x1b[" + strconv.Itoa(curRow+1) + ";" + strconv.Itoa(min(width, curCol)+1) + "H")
		if s.ShowCursor {
			b.WriteString("\x1b[?25h")
		} else {
			b.WriteString("\x1b[?25l")
		}
	} else {
		b.WriteString("\x1b[?25l")
	}
	b.WriteString(syncEnd)
	s.write(b.String())
	s.prev = screen
	s.prevW, s.prevH = width, height
}

func (s *Screen) compositeEndIndicator(screen []string, f *layoutFrame, width int) []string {
	s.endIndicator = nil
	v := s.implicit
	if f.primary != nil {
		v = f.primary
	}
	if s.ScrollToEndIndicator == nil || !v.FollowEnd || v.following {
		return screen
	}
	box := f.boxOf(v)
	if box == nil || box.clip.W <= 0 || box.clip.H <= 0 {
		return screen
	}
	row := box.clip.Y + box.clip.H - 1
	if row >= len(screen) {
		return screen
	}
	right := box.clip.X + box.clip.W
	if g, ok := getScrollbarGeometry(box); ok {
		right = g.column
	}
	label := truncateToWidth(s.ScrollToEndIndicator(), box.clip.W, "")
	col := box.clip.X + (box.clip.W-visibleWidth(label))/2
	text := truncateToWidth(label, max(0, right-col), "")
	tw := visibleWidth(text)
	if tw == 0 {
		return screen
	}
	out := append([]string{}, screen...)
	out[row] = compositeLine(out[row], text, col, tw, width)
	s.endIndicator = &Rect{col, row, tw, 1}
	return out
}

// ---- mouse ----

// MouseEvent is one SGR mouse report. X and Y are 0-based.
type MouseEvent struct {
	Button  int
	X, Y    int
	Release bool
}

func (e MouseEvent) wheel() (dir int, ok bool) {
	if e.Button&64 == 0 {
		return 0, false
	}
	switch e.Button & 3 {
	case 0:
		return -1, true
	case 1:
		return 1, true
	}
	return 0, false
}

// HandleMouse routes a mouse event: wheel, scroll-to-end label, scrollbar,
// selection. It always consumes the event.
func (s *Screen) HandleMouse(ev MouseEvent) {
	if dir, ok := ev.wheel(); ok {
		if dir < 0 && time.Now().Before(s.wheelHoldUntil) {
			return
		}
		lines := s.WheelLines
		if lines <= 0 {
			lines = s.wheel.next(dir, time.Now())
		}
		if ev.Button&8 != 0 { // alt
			lines *= altWheelMultiplier
		}
		s.routeWheel(ev, dir*lines)
		return
	}
	if s.MouseInterceptor != nil && s.MouseInterceptor(MouseEvent{ev.Button, ev.X + 1, ev.Y + 1, ev.Release}) {
		return
	}
	if s.handleEndIndicator(ev) {
		return
	}
	handled := s.handleScrollbar(ev)
	if s.scrollbarDrag == nil {
		s.updateScrollbarHover(ev.X, ev.Y)
	}
	if !handled {
		s.handleSelection(ev)
	}
}

func (s *Screen) routeWheel(ev MouseEvent, delta int) {
	remaining := delta
	seen := map[*ScrollView]bool{}
	if s.current != nil {
		for _, v := range s.current.scrollViewsAt(ev.X, ev.Y) {
			seen[v] = true
			remaining = v.ScrollBy(remaining)
			if remaining == 0 || v.Contain {
				break
			}
		}
	}
	if p := s.primary(); remaining != 0 && !seen[p] {
		p.ScrollBy(remaining)
	}
	s.updateScrollbarHover(ev.X, ev.Y)
	s.requestRender()
}

func (s *Screen) handleEndIndicator(ev MouseEvent) bool {
	r := s.endIndicator
	if r == nil || ev.Release || ev.Button&32 != 0 || ev.Button&3 != 0 {
		return false
	}
	if ev.Y != r.Y || ev.X < r.X || ev.X >= r.X+r.W {
		return false
	}
	s.ScrollToBottom()
	return true
}

type scrollbarDrag struct {
	view *ScrollView
	grab int
}

func (s *Screen) scrollbarAt(x, y int) (*ScrollView, scrollbarGeometry, bool) {
	if s.HasOverlay() || s.current == nil {
		return nil, scrollbarGeometry{}, false
	}
	for _, v := range s.current.scrollViewsAt(x, y) {
		box := s.current.boxOf(v)
		if box == nil {
			continue
		}
		if g, ok := getScrollbarGeometry(box); ok && x == g.column && y >= g.thumbTop && y < g.thumbTop+g.thumbHeight {
			return v, g, true
		}
	}
	return nil, scrollbarGeometry{}, false
}

func (s *Screen) updateScrollbarHover(x, y int) {
	v, _, _ := s.scrollbarAt(x, y)
	if v == s.scrollbarHover {
		return
	}
	if s.scrollbarHover != nil {
		s.scrollbarHover.setScrollbarActive(false)
	}
	s.scrollbarHover = v
	if v != nil {
		v.setScrollbarActive(true)
	}
}

func (s *Screen) handleScrollbar(ev MouseEvent) bool {
	if d := s.scrollbarDrag; d != nil {
		if ev.Release {
			s.scrollbarDrag = nil
			return true
		}
		if s.current != nil {
			if box := s.current.boxOf(d.view); box != nil {
				if g, ok := getScrollbarGeometry(box); ok {
					maxOff := g.trackHeight - g.thumbHeight
					off := max(0, min(maxOff, ev.Y-g.trackTop-d.grab))
					top := 0
					if maxOff > 0 {
						top = int(math.Round(float64(off) / float64(maxOff) * float64(g.maxScrollTop)))
					}
					d.view.ScrollTo(top, false)
				}
			}
		}
		return true
	}
	if ev.Release || ev.Button&32 != 0 || ev.Button&3 != 0 {
		return false
	}
	v, g, ok := s.scrollbarAt(ev.X, ev.Y)
	if !ok {
		return false
	}
	s.stopAutoScroll()
	s.selPressActive = false
	s.selAnchor, s.selFocus, s.selInitial, s.lastClick = nil, nil, nil, nil
	s.selGranularity = 0
	s.pressedURL = ""
	s.selDragged = false
	s.scrollbarDrag = &scrollbarDrag{v, ev.Y - g.thumbTop}
	return true
}

// ---- selection ----

type selPoint struct {
	row, col int
	view     *ScrollView
	boundary bool
}

type selRange struct{ start, end selPoint }

type clickTarget struct {
	at                 time.Time
	count, row         int
	view               *ScrollView
	wordStart, wordEnd int
}

func (s *Screen) scrollPoint(v *ScrollView, x, y int) (selPoint, bool) {
	if s.current == nil {
		return selPoint{}, false
	}
	box := s.current.boxOf(v)
	if box == nil || box.rect.H <= 0 || box.clip.H <= 0 {
		return selPoint{}, false
	}
	_, th := s.size()
	top := max(0, box.rect.Y, box.clip.Y)
	bottom := min(th-1, box.rect.Y+box.rect.H-1, box.clip.Y+box.clip.H-1)
	if bottom < top {
		return selPoint{}, false
	}
	pr := max(top, min(bottom, y))
	maxRow := max(0, len(box.scrollLines)-1)
	return selPoint{
		row:  max(0, min(maxRow, v.scrollTop+pr-box.rect.Y)),
		col:  max(0, min(box.rect.W-1, x-box.rect.X)),
		view: v,
	}, true
}

func (s *Screen) pointFor(ev MouseEvent, v *ScrollView) selPoint {
	if v != nil {
		if p, ok := s.scrollPoint(v, ev.X, ev.Y); ok {
			return p
		}
	}
	tw, th := s.size()
	return selPoint{row: max(0, min(th-1, ev.Y)), col: max(0, min(tw-1, ev.X))}
}

func (s *Screen) sourceLine(p selPoint) string {
	if p.view != nil && s.current != nil {
		if box := s.current.boxOf(p.view); box != nil {
			if p.row < len(box.scrollLines) {
				return box.scrollLines[p.row]
			}
			return ""
		}
	}
	if p.row < len(s.prev) {
		return s.prev[p.row]
	}
	return ""
}

// wordSelection is the word under p, keeping paths and kebab-case whole the
// way terminals' own double-click does.
func (s *Screen) wordSelection(p selPoint) *selRange {
	line := stripANSI(s.sourceLine(p))
	type seg struct {
		start, end int
		word, join bool
	}
	var segs []seg
	col := 0
	cur := seg{start: 0}
	kind := -1 // 0 space/punct, 1 word
	flush := func() {
		if cur.end > cur.start {
			segs = append(segs, cur)
		}
	}
	forEachGrapheme(line, func(g string) {
		w := graphemeWidth(g)
		r := []rune(g)[0]
		isJoin := g == "/" || g == "-"
		isWord := isWordRune(r)
		k := 0
		if isWord {
			k = 1
		}
		if isJoin || k != kind || k == 0 {
			flush()
			cur = seg{start: col, word: isWord || isJoin, join: isJoin}
		}
		kind = k
		if isJoin {
			kind = -1
		}
		col += w
		cur.end = col
	})
	flush()
	hit := -1
	for i, sg := range segs {
		if p.col >= sg.start && p.col < sg.end {
			hit = i
			break
		}
	}
	if hit < 0 {
		return nil
	}
	canJoin := func(a, b seg) bool { return a.word && b.word && (a.join || b.join) }
	start, end := segs[hit].start, segs[hit].end
	for i := hit; i > 0 && canJoin(segs[i-1], segs[i]); i-- {
		start = segs[i-1].start
	}
	for i := hit; i < len(segs)-1 && canJoin(segs[i], segs[i+1]); i++ {
		end = segs[i+1].end
	}
	a, b := p, p
	a.col, b.col, b.boundary = start, end, true
	return &selRange{a, b}
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.Is(unicode.Mn, r)
}

func (s *Screen) lineSelection(p selPoint) *selRange {
	a, b := p, p
	a.col = 0
	b.col, b.boundary = visibleWidth(s.sourceLine(p)), true
	return &selRange{a, b}
}

func (s *Screen) updateFocus(p selPoint) {
	if s.selGranularity == 0 || s.selInitial == nil {
		s.selFocus = &p
		return
	}
	var r *selRange
	if s.selGranularity == 1 {
		r = s.wordSelection(p)
	} else {
		r = s.lineSelection(p)
	}
	if r == nil {
		return
	}
	init := s.selInitial
	before := r.start.row < init.start.row || (r.start.row == init.start.row && r.start.col < init.start.col)
	if before {
		e, st := init.end, r.start
		s.selAnchor, s.selFocus = &e, &st
	} else {
		st, e := init.start, r.end
		s.selAnchor, s.selFocus = &st, &e
	}
}

func (s *Screen) clickCount(p selPoint, word *selRange) int {
	now := time.Now()
	prev := s.lastClick
	count := 1
	if word != nil && prev != nil && now.Sub(prev.at) <= doubleClickInterval && prev.row == p.row &&
		prev.view == p.view && prev.wordStart == word.start.col && prev.wordEnd == word.end.col {
		count = prev.count%3 + 1
	}
	if word != nil {
		s.lastClick = &clickTarget{now, count, p.row, p.view, word.start.col, word.end.col}
	} else {
		s.lastClick = nil
	}
	return count
}

func (s *Screen) handleSelection(ev MouseEvent) {
	button := ev.Button & 3
	if button != 0 && !(ev.Release && button == 3) {
		return
	}
	var anchorView *ScrollView
	if s.selAnchor != nil {
		anchorView = s.selAnchor.view
	}
	p := s.pointFor(ev, anchorView)
	if ev.Release {
		if !s.selPressActive {
			return
		}
		s.selPressActive = false
		s.stopAutoScroll()
		if s.selAnchor == nil {
			return
		}
		s.updateFocus(p)
		same := !s.selDragged && s.selAnchor.view == p.view && s.selAnchor.row == p.row && s.selAnchor.col == p.col
		url := ""
		if same {
			url = s.pressedURL
		}
		s.pressedURL = ""
		if url != "" && s.OpenURL != nil {
			s.selAnchor, s.selFocus = nil, nil
			s.OpenURL(url)
			s.requestRender()
			return
		}
		if same && s.OnClick != nil {
			s.OnClick(ev.X, ev.Y)
		}
		if s.CopyOnSelect {
			s.copySelection()
		}
		s.requestRender()
		return
	}
	if ev.Button&32 != 0 { // motion
		if !s.selPressActive || s.selAnchor == nil {
			return
		}
		s.selDragged = true
		s.lastClick = nil
		s.pressedURL = ""
		s.updateFocus(p)
		s.updateAutoScroll(ev)
		s.requestRender()
		return
	}
	s.stopAutoScroll()
	s.selPressActive = true
	var view *ScrollView
	if !s.HasOverlay() && s.current != nil {
		if vs := s.current.scrollViewsAt(ev.X, ev.Y); len(vs) > 0 {
			view = vs[0]
		}
	}
	anchor := s.pointFor(ev, view)
	word := s.wordSelection(anchor)
	n := s.clickCount(anchor, word)
	var r *selRange
	switch n {
	case 2:
		r = word
		s.selGranularity = 1
	case 3:
		r = s.lineSelection(anchor)
		s.selGranularity = 2
	default:
		s.selGranularity = 0
	}
	if r == nil {
		s.selGranularity = 0
	}
	s.selInitial = r
	if r != nil {
		st, e := r.start, r.end
		s.selAnchor, s.selFocus = &st, &e
		s.pressedURL = ""
	} else {
		a, f := anchor, anchor
		s.selAnchor, s.selFocus = &a, &f
		_, th := s.size()
		tw, _ := s.size()
		row := max(0, min(th-1, ev.Y))
		line := ""
		if row < len(s.prev) {
			line = s.prev[row]
		}
		s.pressedURL = osc8LinkAt(line, max(0, min(tw-1, ev.X)))
	}
	s.selDragged = false
	s.requestRender()
}

func (s *Screen) updateAutoScroll(ev MouseEvent) {
	if s.selAnchor == nil || s.selAnchor.view == nil || s.current == nil {
		s.stopAutoScroll()
		return
	}
	box := s.current.boxOf(s.selAnchor.view)
	if box == nil || box.rect.H <= 0 || box.clip.H <= 0 {
		s.stopAutoScroll()
		return
	}
	_, th := s.size()
	top := max(0, box.rect.Y, box.clip.Y)
	bottom := min(th-1, box.rect.Y+box.rect.H-1, box.clip.Y+box.clip.H-1)
	s.autoScrollPointer = [2]int{ev.X, ev.Y}
	switch {
	case ev.Y <= top:
		s.autoScrollDir = -1
	case ev.Y >= bottom:
		s.autoScrollDir = 1
	default:
		s.stopAutoScroll()
		return
	}
	if s.autoScrollTicker != nil {
		return
	}
	s.autoScrollTicker = time.NewTicker(50 * time.Millisecond)
	s.autoScrollStop = make(chan struct{})
	t, stop := s.autoScrollTicker, s.autoScrollStop
	go func() {
		for {
			select {
			case <-t.C:
				s.requestRender()
			case <-stop:
				return
			}
		}
	}()
}

// TickAutoScroll advances a drag-selection past the edge of its view; the
// owner calls it once per frame.
func (s *Screen) TickAutoScroll() {
	if s.autoScrollDir == 0 || s.selAnchor == nil || s.selAnchor.view == nil {
		return
	}
	v := s.selAnchor.view
	if v.ScrollBy(s.autoScrollDir) == s.autoScrollDir {
		s.stopAutoScroll()
		return
	}
	if p, ok := s.scrollPoint(v, s.autoScrollPointer[0], s.autoScrollPointer[1]); ok {
		s.updateFocus(p)
	}
}

func (s *Screen) stopAutoScroll() {
	if s.autoScrollTicker != nil {
		s.autoScrollTicker.Stop()
		close(s.autoScrollStop)
		s.autoScrollTicker = nil
	}
	s.autoScrollDir = 0
}

func (s *Screen) selectionBounds() (selPoint, selPoint, bool) {
	a, f := s.selAnchor, s.selFocus
	if a == nil || f == nil || a.view != f.view {
		return selPoint{}, selPoint{}, false
	}
	if a.row == f.row && a.col == f.col {
		return selPoint{}, selPoint{}, false
	}
	if a.row < f.row || (a.row == f.row && a.col < f.col) {
		return *a, *f, true
	}
	return *f, *a, true
}

// HasSelection reports a non-empty selection.
func (s *Screen) HasSelection() bool { _, _, ok := s.selectionBounds(); return ok }

// ClearSelection drops the selection.
func (s *Screen) ClearSelection() {
	if s.selAnchor == nil && s.selFocus == nil {
		return
	}
	s.selAnchor, s.selFocus, s.selInitial = nil, nil, nil
	s.requestRender()
}

func selectionColumns(line string, row int, start, end selPoint, minCol, maxCol int) (int, int) {
	w := visibleWidth(line)
	a, b := max(0, minCol), min(w, maxCol)
	if row == start.row {
		if st, _, ok := graphemeCellRange(line, start.col); ok {
			a = st
		} else {
			a = min(start.col, w)
		}
	}
	if row == end.row {
		if end.boundary {
			b = min(end.col, w)
		} else if _, e, ok := graphemeCellRange(line, end.col); ok {
			b = e
		} else {
			b = min(end.col+1, w)
		}
	}
	return max(minCol, a), min(maxCol, b)
}

// SelectionText is the selected text, styles stripped.
func (s *Screen) SelectionText() string {
	start, end, ok := s.selectionBounds()
	if !ok {
		return ""
	}
	src := s.prev
	if start.view != nil {
		if s.current == nil {
			return ""
		}
		box := s.current.boxOf(start.view)
		if box == nil {
			return ""
		}
		src = box.scrollLines
	}
	var lines []string
	for row := start.row; row <= end.row; row++ {
		line := ""
		if row < len(src) {
			line = src[row]
		}
		a, b := selectionColumns(line, row, start, end, 0, visibleWidth(line))
		lines = append(lines, strings.TrimRight(stripANSI(sliceByColumn(line, a, max(0, b-a), true)), " "))
	}
	return strings.Join(lines, "\n")
}

func (s *Screen) copySelection() bool {
	text := s.SelectionText()
	if text == "" {
		return false
	}
	return s.CopyText(text)
}

// CopyText puts text on the clipboard and flashes the result.
func (s *Screen) CopyText(text string) bool {
	if s.CopySelection != nil {
		ok := s.CopySelection(text)
		if ok {
			s.Flash("Copied!", 0)
		} else {
			s.Flash("Copy failed", 0)
		}
		return ok
	}
	s.write("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
	s.Flash("Copied!", 0)
	return true
}

func highlightSelection(text string) string {
	var b strings.Builder
	b.WriteString("\x1b[7m")
	for i := 0; i < len(text); {
		if code, n := extractAnsiCode(text, i); n > 0 {
			b.WriteString(code)
			if strings.HasSuffix(code, "m") {
				b.WriteString("\x1b[7m")
			}
			i += n
			continue
		}
		b.WriteByte(text[i])
		i++
	}
	b.WriteString("\x1b[27m")
	return b.String()
}

func (s *Screen) applySelection(screen []string) []string {
	start, end, ok := s.selectionBounds()
	if !ok {
		return screen
	}
	tw, _ := s.size()
	minRow, maxRow, minCol, maxCol := 0, len(screen)-1, 0, tw
	if start.view != nil {
		if s.current == nil {
			return screen
		}
		box := s.current.boxOf(start.view)
		if box == nil {
			return screen
		}
		minRow = max(0, box.rect.Y, box.clip.Y)
		maxRow = min(len(screen)-1, box.rect.Y+box.rect.H-1, box.clip.Y+box.clip.H-1)
		minCol = max(0, box.rect.X, box.clip.X)
		maxCol = min(tw, box.rect.X+box.rect.W, box.clip.X+box.clip.W)
		top := start.view.scrollTop
		start.row, start.col = box.rect.Y+start.row-top, box.rect.X+start.col
		end.row, end.col = box.rect.Y+end.row-top, box.rect.X+end.col
	}
	out := make([]string, len(screen))
	for row, line := range screen {
		out[row] = line
		if row < minRow || row > maxRow || row < start.row || row > end.row {
			continue
		}
		w := visibleWidth(line)
		a, b := selectionColumns(line, row, start, end, minCol, maxCol)
		if b <= a {
			continue
		}
		out[row] = sliceByColumn(line, 0, a, true) + highlightSelection(sliceByColumn(line, a, b-a, true)) +
			sliceByColumn(line, b, max(0, w-b), true)
	}
	return out
}

// ---- wheel acceleration ----

// wheelAccel converts wheel events into line counts. Local macOS terminals
// get OS-accelerated deltas and send one event per line; elsewhere one event
// is one notch, so a fast spin is accelerated by event velocity.
type wheelAccel struct {
	accelerate bool
	last       time.Time
	lastDir    int
	avgGap     float64
	carry      float64
}

func newWheelAccel() wheelAccel {
	mac := os.Getenv("SSH_CONNECTION") == "" && os.Getenv("SSH_CLIENT") == "" && os.Getenv("SSH_TTY") == "" && runtime.GOOS == "darwin"
	return wheelAccel{accelerate: !mac}
}

func (w *wheelAccel) next(dir int, now time.Time) int {
	if !w.accelerate {
		return 1
	}
	gap := float64(now.Sub(w.last).Milliseconds())
	same := dir == w.lastDir && !w.last.IsZero() && gap <= 200
	w.last, w.lastDir = now, dir
	if !same {
		w.avgGap, w.carry = 0, 0
		return 1
	}
	if gap < 5 {
		return 1
	}
	if w.avgGap == 0 {
		w.avgGap = gap
	} else {
		w.avgGap = (w.avgGap + gap) / 2
	}
	lines := math.Min(6, math.Max(1, 100/w.avgGap)) + w.carry
	whole := math.Floor(lines)
	w.carry = lines - whole
	return int(whole)
}
