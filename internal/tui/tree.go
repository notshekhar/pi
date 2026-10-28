package tui

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

// The /tree selector — loop's ui/tree-selector.ts with ui/tree/{layout,
// tree-list,entry-display}.ts: the CURRENT session's entry tree, every branch
// of it, to jump to any point (optionally summarising the branch left).

// TreeItem is one entry of the session tree, as the selector needs it.
type TreeItem struct {
	ID, ParentID string
	Type         string // message | subagent | compact | branch-summary | model-change | session-info | custom | label
	Role         string
	Content      json.RawMessage
	// UserText is a user message's text with hook context stripped.
	UserText string
	Agent    string
	Prompt   string
	Summary  string
	To       string
	// TokensBefore / Fresh describe a compaction (Fresh = rollover/handoff).
	TokensBefore int
	Fresh        bool
	// EntryLabel is a label entry's own value; nil = cleared.
	EntryLabel *string
	// Label is the label ON this node ("" = none), and when it was set.
	Label   string
	LabelTS int64

	Children []*TreeItem
}

// TreeFilter is loop's FilterMode.
type TreeFilter string

const (
	TreeFilterDefault TreeFilter = "default"
	TreeFilterNoTools TreeFilter = "no-tools"
	TreeFilterUser    TreeFilter = "user-only"
	TreeFilterLabeled TreeFilter = "labeled-only"
	TreeFilterAll     TreeFilter = "all"
)

var treeFilterModes = []TreeFilter{TreeFilterDefault, TreeFilterNoTools, TreeFilterUser, TreeFilterLabeled, TreeFilterAll}

var treeFilterStatus = map[TreeFilter]string{TreeFilterNoTools: " [no-tools]", TreeFilterUser: " [user]",
	TreeFilterLabeled: " [labeled]", TreeFilterAll: " [all]"}

// ── layout (layout.ts) ──────────────────────────────────────────────────

type treeGutter struct {
	position int
	show     bool
}

type flatNode struct {
	node               *TreeItem
	indent             int
	showConnector      bool
	isLast             bool
	gutters            []treeGutter
	isVirtualRootChild bool
}

type treeFrame struct {
	id                                                  string
	node                                                *TreeItem
	indent                                              int
	justBranched, showConnector, isLast, isVirtualChild bool
	gutters                                             []treeGutter
}

func childLayout(indent int, justBranched, multipleChildren, showConnector, isVirtualRootChild, isLast bool,
	gutters []treeGutter, multipleRoots bool) (int, []treeGutter) {
	childIndent := indent
	if multipleChildren || (justBranched && indent > 0) {
		childIndent = indent + 1
	}
	if showConnector && !isVirtualRootChild {
		display := indent
		if multipleRoots {
			display = max(0, indent-1)
		}
		g := append(append([]treeGutter{}, gutters...), treeGutter{position: max(0, display-1), show: !isLast})
		return childIndent, g
	}
	return childIndent, gutters
}

func flattenTree(roots []*TreeItem, leaf string) ([]*flatNode, bool) {
	multipleRoots := len(roots) > 1
	contains := map[*TreeItem]bool{}
	var all []*TreeItem
	stack := append([]*TreeItem{}, roots...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		all = append(all, n)
		for i := len(n.Children) - 1; i >= 0; i-- {
			stack = append(stack, n.Children[i])
		}
	}
	for i := len(all) - 1; i >= 0; i-- {
		n := all[i]
		has := leaf != "" && n.ID == leaf
		for _, c := range n.Children {
			if contains[c] {
				has = true
			}
		}
		contains[n] = has
	}
	ordered := append([]*TreeItem{}, roots...)
	stableActiveFirst(ordered, contains)

	var frames []treeFrame
	for i := len(ordered) - 1; i >= 0; i-- {
		in := 0
		if multipleRoots {
			in = 1
		}
		frames = append(frames, treeFrame{node: ordered[i], indent: in, justBranched: multipleRoots,
			showConnector: multipleRoots, isLast: i == len(ordered)-1, isVirtualChild: multipleRoots})
	}
	var out []*flatNode
	for len(frames) > 0 {
		f := frames[len(frames)-1]
		frames = frames[:len(frames)-1]
		out = append(out, &flatNode{node: f.node, indent: f.indent, showConnector: f.showConnector, isLast: f.isLast,
			gutters: f.gutters, isVirtualRootChild: f.isVirtualChild})
		kids := append([]*TreeItem{}, f.node.Children...)
		stableActiveFirst(kids, contains)
		multi := len(kids) > 1
		ci, cg := childLayout(f.indent, f.justBranched, multi, f.showConnector, f.isVirtualChild, f.isLast, f.gutters, multipleRoots)
		for i := len(kids) - 1; i >= 0; i-- {
			frames = append(frames, treeFrame{node: kids[i], indent: ci, justBranched: multi, showConnector: multi,
				isLast: i == len(kids)-1, gutters: cg})
		}
	}
	return out, multipleRoots
}

// stableActiveFirst moves branches containing the active leaf first.
func stableActiveFirst(list []*TreeItem, contains map[*TreeItem]bool) {
	var a, b []*TreeItem
	for _, n := range list {
		if contains[n] {
			a = append(a, n)
		} else {
			b = append(b, n)
		}
	}
	copy(list, append(a, b...))
}

// ── the list (tree-list.ts) ─────────────────────────────────────────────

// TreeSelector is the /tree component.
type TreeSelector struct {
	Theme *Theme
	// Result carries the chosen entry id; "" = cancelled.
	Result chan string
	// OnLabel is called when a label is edited ("" removes it).
	OnLabel func(entryID, label string)

	flat, filtered   []*flatNode
	selected         int
	leaf             string
	maxVisible       int
	filter           TreeFilter
	query            string
	multipleRoots    bool
	showLabelTimes   bool
	activePath       map[string]bool
	visibleParent    map[string]string // "" = root
	visibleChildren  map[string][]string
	lastSelected     string
	folded           map[string]bool
	toolCalls        map[string]toolCallInfo
	cwd              string
	labelEditing     string // entry id under edit, "" = none
	labelValue       string
	keyHintCollapsed bool
}

type toolCallInfo struct {
	name  string
	input map[string]any
}

// NewTreeSelector builds the selector over roots.
func NewTreeSelector(th *Theme, roots []*TreeItem, leaf string, termRows int, cwd, initialSelected string, filter TreeFilter) *TreeSelector {
	if filter == "" {
		filter = TreeFilterDefault
	}
	s := &TreeSelector{Theme: th, Result: make(chan string, 1), leaf: leaf, maxVisible: max(5, termRows/2),
		filter: filter, folded: map[string]bool{}, cwd: cwd, toolCalls: buildToolCallMap(roots)}
	s.flat, s.multipleRoots = flattenTree(roots, leaf)
	s.buildActivePath()
	s.applyFilter()
	target := initialSelected
	if target == "" {
		target = leaf
	}
	s.selected = s.nearestVisible(target)
	if s.selected < len(s.filtered) {
		s.lastSelected = s.filtered[s.selected].node.ID
	}
	return s
}

func (s *TreeSelector) byID() map[string]*flatNode {
	m := make(map[string]*flatNode, len(s.flat))
	for _, f := range s.flat {
		m[f.node.ID] = f
	}
	return m
}

func (s *TreeSelector) nearestVisible(id string) int {
	if len(s.filtered) == 0 {
		return 0
	}
	idx := map[string]int{}
	for i, f := range s.filtered {
		idx[f.node.ID] = i
	}
	m := s.byID()
	for cur := id; cur != ""; {
		if i, ok := idx[cur]; ok {
			return i
		}
		n := m[cur]
		if n == nil {
			break
		}
		cur = n.node.ParentID
	}
	return len(s.filtered) - 1
}

func (s *TreeSelector) buildActivePath() {
	s.activePath = map[string]bool{}
	m := s.byID()
	for cur := s.leaf; cur != ""; {
		s.activePath[cur] = true
		n := m[cur]
		if n == nil {
			break
		}
		cur = n.node.ParentID
	}
}

func (s *TreeSelector) passesFilter(f *flatNode) bool {
	e := f.node
	settings := e.Type == "label" || e.Type == "custom" || e.Type == "model-change" || e.Type == "session-info"
	switch s.filter {
	case TreeFilterUser:
		return e.Type == "message" && e.Role == "user"
	case TreeFilterNoTools:
		return !settings && !(e.Type == "message" && e.Role == "tool")
	case TreeFilterLabeled:
		return e.Label != ""
	case TreeFilterAll:
		return true
	}
	return !settings
}

func (s *TreeSelector) applyFilter() {
	if len(s.filtered) > 0 && s.selected < len(s.filtered) {
		s.lastSelected = s.filtered[s.selected].node.ID
	}
	tokens := strings.Fields(strings.ToLower(s.query))
	s.filtered = s.filtered[:0:0]
	for _, f := range s.flat {
		e := f.node
		if e.Type == "message" && e.Role == "assistant" && e.ID != s.leaf && !hasTextContent(e.Content) {
			continue
		}
		if !s.passesFilter(f) {
			continue
		}
		if len(tokens) > 0 {
			text := strings.ToLower(s.searchable(e))
			ok := true
			for _, t := range tokens {
				if !strings.Contains(text, t) {
					ok = false
					break
				}
			}
			if !ok {
				continue
			}
		}
		s.filtered = append(s.filtered, f)
	}
	if len(s.folded) > 0 {
		skip := map[string]bool{}
		for _, f := range s.flat {
			if p := f.node.ParentID; p != "" && (s.folded[p] || skip[p]) {
				skip[f.node.ID] = true
			}
		}
		kept := s.filtered[:0:0]
		for _, f := range s.filtered {
			if !skip[f.node.ID] {
				kept = append(kept, f)
			}
		}
		s.filtered = kept
	}
	multi := s.recalcStructure()
	if len(s.filtered) > 0 {
		s.multipleRoots = multi
	}
	if s.lastSelected != "" {
		s.selected = s.nearestVisible(s.lastSelected)
	} else if s.selected >= len(s.filtered) {
		s.selected = max(0, len(s.filtered)-1)
	}
	if len(s.filtered) > 0 {
		s.lastSelected = s.filtered[s.selected].node.ID
	}
}

// recalcStructure is recalculateVisualStructure: descendants of hidden
// entries attach to the nearest visible ancestor, indentation re-derived.
func (s *TreeSelector) recalcStructure() bool {
	s.visibleParent = map[string]string{}
	s.visibleChildren = map[string][]string{"": nil}
	if len(s.filtered) == 0 {
		return false
	}
	visible := map[string]*flatNode{}
	for _, f := range s.filtered {
		visible[f.node.ID] = f
	}
	m := s.byID()
	for _, f := range s.filtered {
		anc := ""
		for cur := f.node.ParentID; cur != ""; {
			if visible[cur] != nil {
				anc = cur
				break
			}
			n := m[cur]
			if n == nil {
				break
			}
			cur = n.node.ParentID
		}
		s.visibleParent[f.node.ID] = anc
		s.visibleChildren[anc] = append(s.visibleChildren[anc], f.node.ID)
	}
	roots := s.visibleChildren[""]
	multipleRoots := len(roots) > 1
	var frames []treeFrame
	for i := len(roots) - 1; i >= 0; i-- {
		in := 0
		if multipleRoots {
			in = 1
		}
		frames = append(frames, treeFrame{id: roots[i], indent: in, justBranched: multipleRoots,
			showConnector: multipleRoots, isLast: i == len(roots)-1, isVirtualChild: multipleRoots})
	}
	for len(frames) > 0 {
		fr := frames[len(frames)-1]
		frames = frames[:len(frames)-1]
		f := visible[fr.id]
		if f == nil {
			continue
		}
		f.indent, f.showConnector, f.isLast, f.gutters, f.isVirtualRootChild = fr.indent, fr.showConnector, fr.isLast, fr.gutters, fr.isVirtualChild
		kids := s.visibleChildren[fr.id]
		multi := len(kids) > 1
		ci, cg := childLayout(fr.indent, fr.justBranched, multi, fr.showConnector, fr.isVirtualChild, fr.isLast, fr.gutters, multipleRoots)
		for i := len(kids) - 1; i >= 0; i-- {
			frames = append(frames, treeFrame{id: kids[i], indent: ci, justBranched: multi, showConnector: multi,
				isLast: i == len(kids)-1, gutters: cg})
		}
	}
	return multipleRoots
}

func (s *TreeSelector) isFoldable(id string) bool {
	kids := s.visibleChildren[id]
	if len(kids) == 0 {
		return false
	}
	parent, ok := s.visibleParent[id]
	if !ok || parent == "" {
		return true
	}
	return len(s.visibleChildren[parent]) > 1
}

func (s *TreeSelector) branchSegmentStart(down bool) int {
	if s.selected >= len(s.filtered) {
		return s.selected
	}
	idx := map[string]int{}
	for i, f := range s.filtered {
		idx[f.node.ID] = i
	}
	cur := s.filtered[s.selected].node.ID
	if down {
		for {
			kids := s.visibleChildren[cur]
			if len(kids) == 0 {
				return idx[cur]
			}
			if len(kids) > 1 {
				return idx[kids[0]]
			}
			cur = kids[0]
		}
	}
	for {
		parent := s.visibleParent[cur]
		if parent == "" {
			return idx[cur]
		}
		if len(s.visibleChildren[parent]) > 1 {
			if start := idx[cur]; start < s.selected {
				return start
			}
		}
		cur = parent
	}
}

func (s *TreeSelector) setFilter(f TreeFilter) {
	s.filter = f
	s.folded = map[string]bool{}
	s.applyFilter()
}

func (s *TreeSelector) toggleFilter(f TreeFilter) {
	if s.filter == f {
		s.setFilter(TreeFilterDefault)
	} else {
		s.setFilter(f)
	}
}

func (s *TreeSelector) cycleFilter(step int) {
	i := 0
	for j, m := range treeFilterModes {
		if m == s.filter {
			i = j
		}
	}
	n := len(treeFilterModes)
	s.setFilter(treeFilterModes[((i+step)%n+n)%n])
}

// Filter is the active filter mode (persisted by the caller).
func (s *TreeSelector) Filter() TreeFilter { return s.filter }

// Handle routes one key; true when the selector closed.
func (s *TreeSelector) Handle(k Key, paste string) bool {
	if s.labelEditing != "" {
		return s.handleLabel(k, paste)
	}
	n := len(s.filtered)
	switch {
	case k.Kind == KeyUp && !k.Ctrl && !k.Alt:
		if n > 0 {
			s.selected = (s.selected - 1 + n) % n
		}
	case k.Kind == KeyDown && !k.Ctrl && !k.Alt:
		if n > 0 {
			s.selected = (s.selected + 1) % n
		}
	case k.Kind == KeyLeft && (k.Ctrl || k.Alt):
		if n > 0 {
			id := s.filtered[s.selected].node.ID
			if s.isFoldable(id) && !s.folded[id] {
				s.folded[id] = true
				s.applyFilter()
			} else {
				s.selected = s.branchSegmentStart(false)
			}
		}
	case k.Kind == KeyRight && (k.Ctrl || k.Alt):
		if n > 0 {
			id := s.filtered[s.selected].node.ID
			if s.folded[id] {
				delete(s.folded, id)
				s.applyFilter()
			} else {
				s.selected = s.branchSegmentStart(true)
			}
		}
	case k.Kind == KeyLeft || k.Kind == KeyPageUp:
		s.selected = max(0, s.selected-s.maxVisible)
	case k.Kind == KeyRight || k.Kind == KeyPageDown:
		s.selected = max(0, min(n-1, s.selected+s.maxVisible))
	case k.Kind == KeyEnter:
		if n > 0 {
			s.Result <- s.filtered[s.selected].node.ID
			return true
		}
	case k.Kind == KeyEsc || k.Kind == KeyCtrlC:
		if s.query != "" {
			s.query = ""
			s.folded = map[string]bool{}
			s.applyFilter()
		} else {
			s.Result <- ""
			return true
		}
	case k.Kind == KeyCtrlD:
		s.setFilter(TreeFilterDefault)
	case k.Kind == KeyCtrlT:
		s.toggleFilter(TreeFilterNoTools)
	case k.Kind == KeyCtrlU:
		s.toggleFilter(TreeFilterUser)
	case k.Kind == KeyCtrlL:
		s.toggleFilter(TreeFilterLabeled)
	case k.Kind == KeyCtrlA:
		s.toggleFilter(TreeFilterAll)
	case k.Kind == KeyCtrlO && k.Shift:
		s.cycleFilter(-1)
	case k.Kind == KeyCtrlO:
		s.cycleFilter(1)
	case k.Kind == KeyBackspace:
		if s.query != "" {
			r := []rune(s.query)
			s.query = string(r[:len(r)-1])
			s.folded = map[string]bool{}
			s.applyFilter()
		}
	case k.Kind == KeyRune && k.Rune == 'L' && !k.Alt:
		if n > 0 {
			s.labelEditing = s.filtered[s.selected].node.ID
			s.labelValue = s.filtered[s.selected].node.Label
		}
	case k.Kind == KeyRune && k.Rune == 'T' && !k.Alt:
		s.showLabelTimes = !s.showLabelTimes
	case k.Kind == KeyRune && !k.Alt:
		s.query += string(k.Rune)
		s.folded = map[string]bool{}
		s.applyFilter()
	case k.Kind == KeyPaste:
		if paste != "" && !strings.ContainsAny(paste, "\x00\r\n\t\x1b") {
			s.query += paste
			s.folded = map[string]bool{}
			s.applyFilter()
		}
	}
	return false
}

func (s *TreeSelector) handleLabel(k Key, paste string) bool {
	switch k.Kind {
	case KeyEnter:
		id, v := s.labelEditing, strings.TrimSpace(s.labelValue)
		for _, f := range s.flat {
			if f.node.ID == id {
				f.node.Label = v
				f.node.LabelTS = 0
				if v != "" {
					f.node.LabelTS = time.Now().UnixMilli()
				}
			}
		}
		if s.OnLabel != nil {
			s.OnLabel(id, v)
		}
		s.labelEditing = ""
	case KeyEsc, KeyCtrlC:
		s.labelEditing = ""
	case KeyBackspace:
		if r := []rune(s.labelValue); len(r) > 0 {
			s.labelValue = string(r[:len(r)-1])
		}
	case KeyRune:
		s.labelValue += string(k.Rune)
	case KeyPaste:
		s.labelValue += strings.ReplaceAll(paste, "\n", " ")
	}
	return false
}

// ── render (tree-selector.ts + tree-list.ts) ────────────────────────────

const treeKeyHint = "↑/↓: move. ←/→: page. ctrl+left/ctrl+right: fold/branch. shift+l: label. ctrl+d/ctrl+t/ctrl+u/ctrl+l/ctrl+a: filters (ctrl+o/shift+ctrl+o cycle). shift+t: label time"

// View draws the whole selector.
func (s *TreeSelector) View(width int) []string {
	t := s.Theme
	border := t.Fg(SlotBorder, strings.Repeat("─", max(1, width)))
	trunc := func(l string) string { return truncateToWidth(l, width, "...") }
	out := []string{"", border, trunc(" " + t.Bold("  Session Tree")), trunc(t.Fg(SlotMuted, "  "+treeKeyHint))}
	if s.query != "" {
		out = append(out, trunc("  "+t.Fg(SlotMuted, "Type to search:")+" "+t.Fg(SlotAccent, s.query)))
	} else {
		out = append(out, trunc("  "+t.Fg(SlotMuted, "Type to search:")))
	}
	out = append(out, border, "")
	if s.labelEditing != "" {
		out = append(out, trunc("  "+t.Fg(SlotMuted, "Label (empty to remove):")),
			trunc("  > "+s.labelValue+"\x1b[7m \x1b[27m"),
			trunc("  "+t.Fg(SlotMuted, "enter: save  esc: cancel")))
	} else {
		out = append(out, s.listView(width)...)
	}
	return append(out, "", border)
}

func (s *TreeSelector) statusLabels() string {
	l := treeFilterStatus[s.filter]
	if s.showLabelTimes {
		l += " [+label time]"
	}
	return l
}

func (s *TreeSelector) listView(width int) []string {
	t := s.Theme
	trunc := func(l string) string { return truncateToWidth(l, width, "...") }
	if len(s.filtered) == 0 {
		return []string{trunc(t.Fg(SlotMuted, "  No entries found")), trunc(t.Fg(SlotMuted, "  (0/0)"+s.statusLabels()))}
	}
	start := max(0, min(s.selected-s.maxVisible/2, len(s.filtered)-s.maxVisible))
	end := min(start+s.maxVisible, len(s.filtered))
	var out []string
	for i := start; i < end; i++ {
		out = append(out, trunc(s.row(s.filtered[i], i == s.selected)))
	}
	return append(out, trunc(t.Fg(SlotMuted, fmt.Sprintf("  (%d/%d)%s", s.selected+1, len(s.filtered), s.statusLabels()))))
}

func (s *TreeSelector) row(f *flatNode, selected bool) string {
	t := s.Theme
	e := f.node
	cursor := "  "
	if selected {
		cursor = t.Fg(SlotAccent, "› ")
	}
	display := f.indent
	if s.multipleRoots {
		display = max(0, f.indent-1)
	}
	connector := f.showConnector && !f.isVirtualRootChild
	connPos := -1
	if connector {
		connPos = display - 1
	}
	isFolded := s.folded[e.ID]
	var b strings.Builder
	for c := 0; c < display*3; c++ {
		level, pos := c/3, c%3
		var g *treeGutter
		for i := range f.gutters {
			if f.gutters[i].position == level {
				g = &f.gutters[i]
				break
			}
		}
		switch {
		case g != nil:
			if pos == 0 && g.show {
				b.WriteString("│")
			} else {
				b.WriteString(" ")
			}
		case connector && level == connPos:
			switch pos {
			case 0:
				if f.isLast {
					b.WriteString("└")
				} else {
					b.WriteString("├")
				}
			case 1:
				switch {
				case isFolded:
					b.WriteString("⊞")
				case s.isFoldable(e.ID):
					b.WriteString("⊟")
				default:
					b.WriteString("─")
				}
			default:
				b.WriteString(" ")
			}
		default:
			b.WriteString(" ")
		}
	}
	foldMarker := ""
	if isFolded && !connector {
		foldMarker = t.Fg(SlotAccent, "⊞ ")
	}
	pathMarker := ""
	if s.activePath[e.ID] {
		pathMarker = t.Fg(SlotAccent, "• ")
	}
	label := ""
	if e.Label != "" {
		label = t.Fg(SlotWarning, "["+e.Label+"] ")
	}
	labelTime := ""
	if s.showLabelTimes && e.Label != "" && e.LabelTS != 0 {
		labelTime = t.Fg(SlotMuted, formatLabelTimestamp(e.LabelTS)+" ")
	}
	line := cursor + t.Fg(SlotDim, b.String()) + foldMarker + pathMarker + label + labelTime + s.displayText(e, selected)
	if selected {
		return t.Bg(SlotSelected, line)
	}
	return line
}

// ── entry display (entry-display.ts) ────────────────────────────────────

const treeMaxContent = 200

func buildToolCallMap(roots []*TreeItem) map[string]toolCallInfo {
	m := map[string]toolCallInfo{}
	stack := append([]*TreeItem{}, roots...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n.Type == "message" && n.Role == "assistant" {
			for _, p := range contentParts(n.Content) {
				if p.Type == "tool-call" && p.ToolCallID != "" {
					name := p.ToolName
					if name == "" {
						name = "tool"
					}
					input := map[string]any{}
					_ = json.Unmarshal(p.Input, &input)
					m[p.ToolCallID] = toolCallInfo{name: name, input: input}
				}
			}
		}
		stack = append(stack, n.Children...)
	}
	return m
}

type contentPart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Input      json.RawMessage `json:"input"`
}

func contentParts(raw json.RawMessage) []contentPart {
	var ps []contentPart
	if json.Unmarshal(raw, &ps) != nil {
		return nil
	}
	return ps
}

// FormatToolInvocation is loop's formatToolInvocation: `read src/x.ts:1-20`,
// `bash git status`, `task agent: prompt`.
func FormatToolInvocation(name string, args map[string]any, cwd string) string {
	if name == "task" {
		agent, _ := args["agent"].(string)
		if agent == "" {
			agent = "default"
		}
		prompt, _ := args["prompt"].(string)
		if prompt = strings.SplitN(prompt, "\n", 2)[0]; prompt != "" {
			return "task " + agent + ": " + clip(prompt, 1000)
		}
		return "task " + agent
	}
	summary := FormatToolArgs(name, args, cwd)
	rng := ""
	if name == "read" {
		rng = ReadLineRangeText(args)
	}
	if summary != "" {
		return name + " " + summary + rng
	}
	return name
}

func (s *TreeSelector) describeTools(raw json.RawMessage) string {
	var labels []string
	for _, p := range contentParts(raw) {
		switch p.Type {
		case "tool-call":
			name := p.ToolName
			if name == "" {
				name = "tool"
			}
			input := map[string]any{}
			_ = json.Unmarshal(p.Input, &input)
			labels = append(labels, FormatToolInvocation(name, input, s.cwd))
		case "tool-result":
			if c, ok := s.toolCalls[p.ToolCallID]; ok && p.ToolCallID != "" {
				labels = append(labels, FormatToolInvocation(c.name, c.input, s.cwd))
			} else if p.ToolName != "" {
				labels = append(labels, p.ToolName)
			} else {
				labels = append(labels, "tool")
			}
		}
	}
	return strings.Join(labels, ", ")
}

func sliceUTF16(s string, n int) string {
	if len(s) <= n {
		return s
	}
	count := 0
	for i, r := range s {
		w := 1
		if r >= 0x10000 {
			w = 2
		}
		if count+w > n {
			return s[:i]
		}
		count += w
	}
	return s
}

func extractContent(raw json.RawMessage) string {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return sliceUTF16(str, treeMaxContent)
	}
	out := ""
	for _, p := range contentParts(raw) {
		if p.Type == "text" {
			out += p.Text
			if len(out) >= treeMaxContent {
				return sliceUTF16(out, treeMaxContent)
			}
		}
	}
	return out
}

func hasTextContent(raw json.RawMessage) bool {
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.TrimSpace(str) != ""
	}
	for _, p := range contentParts(raw) {
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			return true
		}
	}
	return false
}

func normalizeTree(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\n", " ", "\t", " ").Replace(s))
}

func (s *TreeSelector) displayText(e *TreeItem, selected bool) string {
	t := s.Theme
	var r string
	switch e.Type {
	case "message":
		switch e.Role {
		case "user":
			r = t.Fg(SlotAccent, "user: ") + normalizeTree(sliceUTF16(e.UserText, treeMaxContent))
		case "assistant":
			if text := normalizeTree(extractContent(e.Content)); text != "" {
				r = t.Fg(SlotSuccess, "assistant: ") + text
			} else {
				tools := s.describeTools(e.Content)
				if tools == "" {
					tools = "(no content)"
				}
				r = t.Fg(SlotSuccess, "assistant: ") + t.Fg(SlotMuted, normalizeTree(tools))
			}
		default:
			desc := s.describeTools(e.Content)
			if desc == "" {
				desc = normalizeTree(extractContent(e.Content))
			}
			if desc == "" {
				desc = "tool"
			}
			r = t.Fg(SlotMuted, desc)
		}
	case "subagent":
		r = t.Fg(SlotMuted, "[subagent "+e.Agent+"]: ") + sliceUTF16(normalizeTree(e.Prompt), treeMaxContent)
	case "compact":
		label := "compaction"
		if e.Fresh {
			label = "fresh context"
		}
		r = t.Fg(SlotBorderAccent, fmt.Sprintf("[%s: %dk tokens]", label, int(math.Floor(float64(e.TokensBefore)/1000+0.5))))
	case "branch-summary":
		r = t.Fg(SlotWarning, "[branch summary]: ") + normalizeTree(e.Summary)
	case "model-change":
		r = t.Fg(SlotDim, "[model: "+e.To+"]")
	case "session-info":
		r = t.Fg(SlotDim, "[session start]")
	case "custom":
		r = t.Fg(SlotDim, "[custom]")
	case "label":
		v := "(cleared)"
		if e.EntryLabel != nil {
			v = *e.EntryLabel
		}
		r = t.Fg(SlotDim, "[label: "+v+"]")
	}
	if selected {
		return t.Bold(r)
	}
	return r
}

func (s *TreeSelector) searchable(e *TreeItem) string {
	parts := []string{}
	if e.Label != "" {
		parts = append(parts, e.Label)
	}
	switch e.Type {
	case "message":
		switch e.Role {
		case "user":
			parts = append(parts, "user", sliceUTF16(e.UserText, treeMaxContent))
		case "assistant":
			parts = append(parts, "assistant", extractContent(e.Content), s.describeTools(e.Content))
		default:
			parts = append(parts, "tool", s.describeTools(e.Content))
		}
	case "subagent":
		parts = append(parts, "subagent", e.Agent, e.Prompt)
	case "compact":
		parts = append(parts, "compaction")
	case "branch-summary":
		parts = append(parts, "branch summary", e.Summary)
	case "session-info":
		parts = append(parts, "session start")
	case "model-change":
		parts = append(parts, "model", e.To)
	case "custom":
		parts = append(parts, "custom")
	case "label":
		v := ""
		if e.EntryLabel != nil {
			v = *e.EntryLabel
		}
		parts = append(parts, "label", v)
	}
	return strings.Join(parts, " ")
}

// formatLabelTimestamp: today HH:MM, this year M/D HH:MM, else YY/M/D HH:MM.
func formatLabelTimestamp(ms int64) string {
	d, now := time.UnixMilli(ms), time.Now()
	hm := d.Format("15:04")
	if d.Year() == now.Year() && d.YearDay() == now.YearDay() {
		return hm
	}
	if d.Year() == now.Year() {
		return fmt.Sprintf("%d/%d %s", int(d.Month()), d.Day(), hm)
	}
	return fmt.Sprintf("%s/%d/%d %s", d.Format("06"), int(d.Month()), d.Day(), hm)
}
