package session

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf16"
)

// buildSessionTreeView (sessions/tree-view.ts): every branch of a session,
// pre-order, for `session.tree`.

const maxTreeText = 200

// TreeTool is a tool call an entry carried.
type TreeTool struct {
	Name  string `json:"name"`
	Input any    `json:"input"`
}

// TreeRow is loop's SessionTreeRow.
type TreeRow struct {
	ID          string     `json:"id"`
	ParentID    *string    `json:"parentId"`
	TS          int64      `json:"ts"`
	Type        string     `json:"type"`
	Role        string     `json:"role,omitempty"`
	Text        string     `json:"text,omitempty"`
	Truncated   bool       `json:"truncated,omitempty"`
	Tools       []TreeTool `json:"tools,omitempty"`
	Label       *string    `json:"label,omitempty"`
	Indent      int        `json:"indent"`
	Depth       int        `json:"depth"`
	BranchStart bool       `json:"branchStart,omitempty"`
	LastSibling bool       `json:"lastSibling,omitempty"`
	OnPath      bool       `json:"onPath"`
	ChildCount  int        `json:"childCount"`
	Interrupted bool       `json:"interrupted,omitempty"`
}

// TreeView is loop's SessionTreeView.
type TreeView struct {
	LeafID         *string   `json:"leafId"`
	Rows           []TreeRow `json:"rows"`
	BranchPointIDs []string  `json:"branchPointIds"`
}

type block struct {
	Type       string          `json:"type"`
	Text       *string         `json:"text"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Input      json.RawMessage `json:"input"`
}

func blocksOf(raw json.RawMessage) []block {
	var out []block
	_ = json.Unmarshal(raw, &out)
	return out
}

func treeText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b strings.Builder
	for _, bl := range blocksOf(raw) {
		if bl.Type == "text" && bl.Text != nil {
			b.WriteString(*bl.Text)
		}
	}
	return b.String()
}

func inputValue(raw json.RawMessage) any {
	if len(raw) == 0 || string(raw) == "null" {
		return map[string]any{}
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return map[string]any{}
	}
	return v
}

var whitespace = regexp.MustCompile(`\s+`)

func childIndent(indent int, justBranched, multiple bool) int {
	if multiple {
		return indent + 1
	}
	if justBranched && indent > 0 {
		return indent + 1
	}
	return indent
}

// BuildTreeView flattens the session's tree into rows a client lists.
func (s *Session) BuildTreeView() TreeView {
	roots := s.GetTree()
	calls := map[string]TreeTool{}
	stack := append([]*TreeNode{}, roots...)
	var all []*TreeNode
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		all = append(all, n)
		e := n.Entry
		if e.Type == TypeMessage && e.Role == "assistant" {
			for _, b := range blocksOf(e.Content) {
				if b.Type == "tool-call" && b.ToolCallID != "" {
					name := b.ToolName
					if name == "" {
						name = "tool"
					}
					calls[b.ToolCallID] = TreeTool{Name: name, Input: inputValue(b.Input)}
				}
			}
		}
		for i := len(n.Children) - 1; i >= 0; i-- {
			stack = append(stack, n.Children[i])
		}
	}
	onPath := map[string]bool{}
	for _, e := range s.CurrentBranch() {
		if e.ID != "" {
			onPath[e.ID] = true
		}
	}
	leaf := s.leaf
	contains := map[*TreeNode]bool{}
	for i := len(all) - 1; i >= 0; i-- {
		n := all[i]
		has := leaf != "" && n.Entry.ID == leaf
		for _, c := range n.Children {
			if contains[c] {
				has = true
			}
		}
		contains[n] = has
	}
	order := func(nodes []*TreeNode) []*TreeNode {
		var active, rest []*TreeNode
		for _, n := range nodes {
			if contains[n] {
				active = append(active, n)
			} else {
				rest = append(rest, n)
			}
		}
		return append(active, rest...)
	}
	type frame struct {
		node                                   *TreeNode
		depth, indent                          int
		justBranched, branchStart, lastSibling bool
	}
	view := TreeView{Rows: []TreeRow{}, BranchPointIDs: []string{}}
	if leaf != "" {
		l := leaf
		view.LeafID = &l
	}
	multipleRoots := len(roots) > 1
	ordered := order(roots)
	var fs []frame
	for i := len(ordered) - 1; i >= 0; i-- {
		fs = append(fs, frame{node: ordered[i], justBranched: multipleRoots, branchStart: multipleRoots, lastSibling: i == len(ordered)-1})
	}
	for len(fs) > 0 {
		f := fs[len(fs)-1]
		fs = fs[:len(fs)-1]
		e := f.node.Entry
		if e.ID == "" {
			continue
		}
		raw := ""
		if e.Type == TypeMessage {
			raw = StripHookContext(treeText(e.Content))
		}
		trimmed := strings.TrimSpace(whitespace.ReplaceAllString(raw, " "))
		units := utf16.Encode([]rune(trimmed))
		row := TreeRow{ID: e.ID, TS: e.TS, Type: e.Type, Indent: f.indent, Depth: f.depth,
			OnPath: onPath[e.ID], ChildCount: len(f.node.Children), Interrupted: e.Interrupted}
		if e.ParentID != "" {
			p := e.ParentID
			row.ParentID = &p
		}
		if e.Type == TypeMessage {
			row.Role = e.Role
			if e.Role != "user" {
				for _, b := range blocksOf(e.Content) {
					switch b.Type {
					case "tool-call":
						name := b.ToolName
						if name == "" {
							name = "tool"
						}
						row.Tools = append(row.Tools, TreeTool{Name: name, Input: inputValue(b.Input)})
					case "tool-result":
						if call, ok := calls[b.ToolCallID]; ok && b.ToolCallID != "" {
							row.Tools = append(row.Tools, call)
						} else {
							name := b.ToolName
							if name == "" {
								name = "tool"
							}
							row.Tools = append(row.Tools, TreeTool{Name: name, Input: map[string]any{}})
						}
					}
				}
			}
		}
		if trimmed != "" {
			cut := units
			if len(cut) > maxTreeText {
				cut = cut[:maxTreeText]
			}
			row.Text = string(utf16.Decode(cut))
		}
		row.Truncated = len(units) > maxTreeText
		if f.node.Label != "" {
			l := f.node.Label
			row.Label = &l
		}
		row.BranchStart = f.branchStart
		row.LastSibling = f.branchStart && f.lastSibling
		view.Rows = append(view.Rows, row)
		if len(f.node.Children) > 1 {
			view.BranchPointIDs = append(view.BranchPointIDs, e.ID)
		}
		children := order(f.node.Children)
		branches := len(children) > 1
		next := childIndent(f.indent, f.justBranched, branches)
		for i := len(children) - 1; i >= 0; i-- {
			fs = append(fs, frame{node: children[i], depth: f.depth + 1, indent: next, justBranched: branches,
				branchStart: branches, lastSibling: i == len(children)-1})
		}
	}
	return view
}
