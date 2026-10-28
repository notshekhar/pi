// Package session is loop's session store, ported: one conversation is an
// append-only TREE of entries in ~/.loop/agent.db, shared with loop itself.
//
// Every entry has an id and a parentId. The leaf pointer is the current
// position: an append becomes a child of the leaf and advances it; Branch
// moves the leaf to an earlier entry so the next append starts a new branch.
// Nothing is ever modified or deleted — /tree navigation, compaction and
// branch summaries are all new entries, so loop and pi always agree on what a
// session holds.
package session

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/home"
)

// Session is one conversation.
type Session struct {
	ID   string
	Info Info
	// Path is the session's public address (hooks' transcript_path).
	Path string
	// Meta is the header view pickers and status lines read.
	Meta Meta
	// Messages is the current branch's messages, decoded, oldest first — the
	// conversation as the user sees it (compaction does not hide anything
	// here; see ModelMessages for what the model is sent).
	Messages []provider.Message

	entries    []Entry
	byID       map[string]*Entry
	labels     map[string]string
	labelTS    map[string]int64
	leaf       string
	name       string
	rowID      int64
	seenOnDisk int

	// OnAppend is told after every write (loop's Session.onAppend) — the RPC
	// server marks how far the saved branch reaches in its event stream.
	OnAppend func()

	// mu serializes a running turn's appends against readers on other
	// goroutines (the RPC server reading history mid-turn). Held by callers
	// through Lock/RLock, not inside each method: the methods call each other.
	mu sync.RWMutex
}

// Lock / Unlock guard writes from a goroutine other than the owner's.
func (s *Session) Lock()   { s.mu.Lock() }
func (s *Session) Unlock() { s.mu.Unlock() }

// RLock / RUnlock guard reads against a turn appending on another goroutine.
func (s *Session) RLock()   { s.mu.RLock() }
func (s *Session) RUnlock() { s.mu.RUnlock() }

// Meta is a session's header.
type Meta struct {
	ID      string
	Title   string // first user message, first line
	Name    string // set with /name
	Model   string // the model the session last ran on
	CWD     string
	Created time.Time
	Updated time.Time
	// Messages counts message entries on the current branch.
	Messages int
	// Parent is the session this one was forked or handed off from.
	Parent     string
	ArchivedAt time.Time
	// Spend, from the ledger.
	InputTokens, OutputTokens int64
	CostUSD                   float64
}

// Label is the one-line description shown in a picker.
func (m Meta) Label() string {
	if m.Name != "" {
		return m.Name
	}
	if m.Title == "" {
		return "(untitled)"
	}
	return m.Title
}

// Detail is the dim second column of a picker row.
func (m Meta) Detail() string {
	parts := []string{m.Updated.Format("Jan 2 15:04")}
	if m.Messages > 0 {
		parts = append(parts, fmt.Sprintf("%d msgs", m.Messages))
	}
	if m.Model != "" {
		parts = append(parts, m.Model)
	}
	return strings.Join(parts, " · ")
}

// New is an unsaved session: no row exists until its first append, so a
// launch that never sends anything leaves nothing behind (loop's rule).
func New(model, cwd string) *Session {
	provider, _, _ := strings.Cut(model, "/")
	info := Info{ID: NewULID(), CreatedAt: time.Now().UnixMilli(), CWD: cwd, Provider: provider, Model: model}
	return newSession(info, nil)
}

// Create is New, written immediately — loop's SessionManager.create.
func Create(model, cwd string) (*Session, error) {
	s := New(model, cwd)
	return s, s.ensureStarted()
}

func newSession(info Info, entries []Entry) *Session {
	s := &Session{ID: info.ID, Info: info, Path: home.TranscriptPath(info.CWD, info.ID), entries: entries}
	s.rebuild()
	return s
}

// Load opens a stored session by its id (or its transcript path).
func Load(idOrPath string) (*Session, error) {
	id := idOrPath
	if strings.HasSuffix(id, ".jsonl") {
		id = strings.TrimSuffix(id[strings.LastIndex(id, "/")+1:], ".jsonl")
	}
	rec, err := GetRecord(id)
	if err != nil {
		return nil, err
	}
	if rec == nil {
		return nil, fmt.Errorf("Session not found: %s", idOrPath)
	}
	entries, seen, err := loadEntries(rec.RowID)
	if err != nil {
		return nil, err
	}
	s := newSession(rec.Info, entries)
	s.rowID = rec.RowID
	s.seenOnDisk = seen
	if ensureTreeFields(s.entries) {
		// Legacy flat rows: ids assigned in memory, persisted once.
		if err := replaceEntries(rec.RowID, s.entries); err != nil {
			return nil, err
		}
		s.seenOnDisk = len(s.entries)
		s.rebuild()
	}
	s.Meta.ArchivedAt = msTime(rec.ArchivedAt)
	s.Meta.Updated = msTime(rec.UpdatedAt)
	s.fillSpend()
	return s, nil
}

// ensureTreeFields upgrades legacy entries with no id/parentId to a linear
// chain, in place. It reports whether anything changed.
func ensureTreeFields(entries []Entry) bool {
	ids := map[string]bool{}
	for _, e := range entries {
		if e.ID != "" {
			ids[e.ID] = true
		}
	}
	changed := false
	prev := ""
	for i := range entries {
		e := &entries[i]
		if e.ID == "" {
			e.ID = newEntryID(func(id string) bool { return ids[id] })
			ids[e.ID] = true
			e.ParentID = prev
			changed = true
		}
		prev = e.ID
	}
	return changed
}

// Saved reports whether the session has a row yet.
func (s *Session) Saved() bool { return s.rowID != 0 }

// Entries is every entry in insertion order, abandoned branches included.
func (s *Session) Entries() []Entry { return append([]Entry{}, s.entries...) }

// Entry looks one up by id.
func (s *Session) Entry(id string) (Entry, bool) {
	e, ok := s.byID[id]
	if !ok {
		return Entry{}, false
	}
	return *e, true
}

// LeafID is the current position.
func (s *Session) LeafID() string { return s.leaf }

// GetLabel is an entry's user label.
func (s *Session) GetLabel(id string) string { return s.labels[id] }

// GetLabelTS is when an entry's label last changed (ms), 0 if never.
func (s *Session) GetLabelTS(id string) int64 { return s.labelTS[id] }

// Name is the user-set display name; the latest session-name entry wins.
func (s *Session) Name() string { return s.name }

func (s *Session) rebuild() {
	s.byID = map[string]*Entry{}
	s.labels = map[string]string{}
	s.labelTS = map[string]int64{}
	s.leaf, s.name = "", ""
	for i := range s.entries {
		e := &s.entries[i]
		if e.ID == "" {
			continue
		}
		s.byID[e.ID] = e
		s.leaf = e.ID
		s.applyIndex(e)
	}
	s.refreshView()
}

func (s *Session) applyIndex(e *Entry) {
	switch e.Type {
	case TypeLabel:
		if e.Label != nil && *e.Label != "" {
			s.labels[e.TargetID] = *e.Label
			s.labelTS[e.TargetID] = e.TS
		} else {
			delete(s.labels, e.TargetID)
			delete(s.labelTS, e.TargetID)
		}
	case TypeSessionName:
		s.name = ""
		if e.Name != nil {
			s.name = strings.TrimSpace(*e.Name)
		}
	}
}

// refreshView recomputes Messages and Meta from the current branch.
func (s *Session) refreshView() {
	s.Messages = s.Messages[:0]
	count := 0
	for _, e := range s.GetBranch(s.leaf) {
		if e.Type != TypeMessage {
			continue
		}
		count++
		if m, ok := DecodeContent(e.Role, e.Content); ok {
			s.Messages = append(s.Messages, m)
		}
	}
	s.Meta.ID = s.ID
	s.Meta.CWD = s.Info.CWD
	s.Meta.Created = msTime(s.Info.CreatedAt)
	s.Meta.Parent = s.Info.ParentSession
	s.Meta.Name = s.name
	s.Meta.Model = s.LastModel()
	s.Meta.Messages = count
	s.Meta.Title = ""
	for _, e := range s.entries {
		if e.Type == TypeMessage && e.Role == "user" {
			s.Meta.Title = titleFrom(StripHookContext(TextContent(e.Content)))
			break
		}
	}
	if s.Meta.Updated.IsZero() {
		s.Meta.Updated = s.Meta.Created
	}
}

func (s *Session) fillSpend() {
	if sums, err := SumLedgerForSession(s.ID); err == nil {
		s.Meta.InputTokens, s.Meta.OutputTokens, s.Meta.CostUSD = sums.InputTokens, sums.OutputTokens, sums.USD
	}
}

// ensureStarted writes the row and the session-info root on first use.
func (s *Session) ensureStarted() error {
	if s.rowID != 0 {
		return nil
	}
	if len(s.entries) > 0 {
		id, err := EnsureSession(s.Info)
		if err != nil {
			return err
		}
		s.rowID = id
		return nil
	}
	root := infoEntry(s.Info)
	return s.appendAll([]Entry{root})
}

// AppendAll appends entries as one leaf-chained batch in one transaction.
// An entry with no id (or a taken one) gets a fresh id and the leaf as its
// parent; a copy that arrives with its own id and parent keeps them.
func (s *Session) AppendAll(entries ...Entry) error {
	if err := s.ensureStarted(); err != nil {
		return err
	}
	return s.appendAll(entries)
}

func (s *Session) appendAll(entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	for i := range entries {
		e := &entries[i]
		if e.TS == 0 {
			e.TS = time.Now().UnixMilli()
		}
		if e.ID == "" || s.byID[e.ID] != nil {
			e.ID = newEntryID(func(id string) bool { return s.byID[id] != nil })
			e.ParentID = s.leaf
		}
		s.entries = append(s.entries, *e)
		stored := &s.entries[len(s.entries)-1]
		s.byID[e.ID] = stored
		s.leaf = e.ID
		s.applyIndex(stored)
	}
	// Re-point the index: appending may have moved the slice.
	for i := range s.entries {
		if s.entries[i].ID != "" {
			s.byID[s.entries[i].ID] = &s.entries[i]
		}
	}
	if s.rowID == 0 {
		id, err := EnsureSession(s.Info)
		if err != nil {
			return err
		}
		s.rowID = id
	}
	if err := appendEntries(s.rowID, entries); err != nil {
		return err
	}
	s.seenOnDisk += len(entries)
	s.Meta.Updated = time.Now()
	s.refreshView()
	if s.OnAppend != nil {
		s.OnAppend()
	}
	return nil
}

// IsBehindDisk reports whether another process (loop, a second pi) appended
// to this session since it was loaded.
func (s *Session) IsBehindDisk() bool {
	if s.rowID == 0 {
		return false
	}
	n, err := countEntries(s.rowID)
	return err == nil && n > s.seenOnDisk
}

// Add appends plain messages (no usage) — a user prompt, a tool result.
func (s *Session) Add(messages ...provider.Message) error {
	var batch []Entry
	for _, m := range messages {
		e, err := MessageEntry(m)
		if err != nil {
			return err
		}
		batch = append(batch, e)
	}
	return s.AppendAll(batch...)
}

// MessageEntry is the entry that stores one message.
func MessageEntry(m provider.Message) (Entry, error) {
	role, content, err := EncodeContent(m)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Type: TypeMessage, TS: time.Now().UnixMilli(), Role: role, Content: content}, nil
}

// Branch moves the leaf to an earlier entry; the next append forks there.
func (s *Session) Branch(id string) error {
	if s.byID[id] == nil {
		return fmt.Errorf("Entry %s not found", id)
	}
	s.leaf = id
	s.refreshView()
	return nil
}

// ResetLeaf moves before the root; the next append starts a new root.
func (s *Session) ResetLeaf() {
	s.leaf = ""
	s.refreshView()
}

// GetBranch walks from an entry (the leaf when fromID is "") to the root and
// returns the path root-first.
func (s *Session) GetBranch(fromID string) []Entry {
	var path []Entry
	seen := map[string]bool{}
	for cur := s.byID[fromID]; cur != nil && !seen[cur.ID]; {
		seen[cur.ID] = true
		path = append(path, *cur)
		if cur.ParentID == "" {
			break
		}
		cur = s.byID[cur.ParentID]
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

// Branch returns the current branch, root-first.
func (s *Session) CurrentBranch() []Entry { return s.GetBranch(s.leaf) }

// BranchWithSummary branches to an entry ("" = before the root) and records
// a summary of the path being left, so the model knows what was explored.
func (s *Session) BranchWithSummary(fromID, summary string, usage *Usage, model string) (string, error) {
	if fromID != "" && s.byID[fromID] == nil {
		return "", fmt.Errorf("Entry %s not found", fromID)
	}
	s.leaf = fromID
	from := fromID
	if from == "" {
		from = "root"
	}
	e := Entry{Type: TypeBranchSummary, TS: time.Now().UnixMilli(), Summary: summary, FromID: from,
		Usage: usage, Model: model}
	e.ID = newEntryID(func(id string) bool { return s.byID[id] != nil })
	e.ParentID = fromID
	if err := s.AppendAll(e); err != nil {
		return "", err
	}
	return e.ID, nil
}

// SetName sets (or clears, with "") the display name.
func (s *Session) SetName(name string) error {
	n := strings.TrimSpace(name)
	return s.AppendAll(Entry{Type: TypeSessionName, Name: &n})
}

// SetTitle is SetName under the name pi's commands use.
func (s *Session) SetTitle(title string) error { return s.SetName(title) }

// SetLabel sets or clears ("") a user label on an entry.
func (s *Session) SetLabel(targetID, label string) error {
	if s.byID[targetID] == nil {
		return fmt.Errorf("Entry %s not found", targetID)
	}
	e := Entry{Type: TypeLabel, TargetID: targetID}
	if label != "" {
		e.Label = &label
	}
	return s.AppendAll(e)
}

// TreeNode is one node of GetTree.
type TreeNode struct {
	Entry    Entry
	Children []*TreeNode
	Label    string
	LabelTS  int64
}

// GetTree is the session as a tree. A well-formed session has one root;
// entries whose parent chain is broken come back as extra roots. Children
// are oldest first.
func (s *Session) GetTree() []*TreeNode {
	nodes := map[string]*TreeNode{}
	var order []*TreeNode
	for _, e := range s.entries {
		if e.ID == "" {
			continue
		}
		n := &TreeNode{Entry: e, Label: s.labels[e.ID], LabelTS: s.labelTS[e.ID]}
		nodes[e.ID] = n
		order = append(order, n)
	}
	var roots []*TreeNode
	for _, n := range order {
		p := n.Entry.ParentID
		if parent := nodes[p]; p != "" && p != n.Entry.ID && parent != nil {
			parent.Children = append(parent.Children, n)
		} else {
			roots = append(roots, n)
		}
	}
	stack := append([]*TreeNode{}, roots...)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		sort.SliceStable(n.Children, func(i, j int) bool { return n.Children[i].Entry.TS < n.Children[j].Entry.TS })
		stack = append(stack, n.Children...)
	}
	return roots
}

// UserMessage is a user prompt anywhere in the tree, for /fork.
type UserMessage struct {
	EntryID string
	Text    string
}

// UserMessagesForForking lists every user message, on any branch.
func (s *Session) UserMessagesForForking() []UserMessage {
	var out []UserMessage
	for _, e := range s.entries {
		if e.Type != TypeMessage || e.Role != "user" || e.ID == "" {
			continue
		}
		if t := TextContent(e.Content); t != "" {
			out = append(out, UserMessage{e.ID, t})
		}
	}
	return out
}

// LastModel is the model in effect at the end of the current branch: the
// latest model-change or assistant message's model, else the creation model.
func (s *Session) LastModel() string {
	path := s.GetBranch(s.leaf)
	for i := len(path) - 1; i >= 0; i-- {
		e := path[i]
		if e.Type == TypeModelChange && e.To != "" {
			return e.To
		}
		if e.Type == TypeMessage && e.Role == "assistant" && e.Model != "" {
			return e.Model
		}
	}
	return s.Info.Model
}

// ---- manager ----

// List is the active sessions, newest first.
func List() ([]Meta, error) { return ListScope("", Active) }

// ListScope lists sessions in a scope, in cwd when it is non-empty.
func ListScope(cwd string, scope Scope) ([]Meta, error) {
	recs, err := ListRecords(cwd, scope, 0, 0)
	if err != nil {
		return nil, err
	}
	out := make([]Meta, 0, len(recs))
	for _, r := range recs {
		m := Meta{
			ID: r.Info.ID, Name: r.Name, Model: r.Info.Model, CWD: r.Info.CWD,
			Created: msTime(r.Info.CreatedAt), Updated: msTime(r.UpdatedAt),
			Parent: r.Info.ParentSession, ArchivedAt: msTime(r.ArchivedAt),
		}
		if r.LastModel != "" {
			m.Model = r.LastModel
		}
		if r.FirstUserPayload != "" {
			var e struct {
				Content json.RawMessage `json:"content"`
			}
			if json.Unmarshal([]byte(r.FirstUserPayload), &e) == nil {
				m.Title = titleFrom(StripHookContext(TextContent(e.Content)))
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// Fork copies the current branch into a new session (/clone).
func Fork(s *Session) (*Session, error) { return ForkAtEntry(s, s.leaf) }

// ForkAt forks at the n-th message of the current branch (n messages kept).
func ForkAt(s *Session, n int) (*Session, error) {
	count := 0
	leaf := ""
	for _, e := range s.GetBranch(s.leaf) {
		if e.Type == TypeMessage {
			if count == n {
				break
			}
			count++
		}
		leaf = e.ID
	}
	return ForkAtEntry(s, leaf)
}

// ForkAtEntry forks the path root → leafID into a new session (loop's
// forkAtEntry). Abandoned branches stay behind; the new session's header
// names its source. Labels on copied entries come along.
func ForkAtEntry(source *Session, leafID string) (*Session, error) {
	path := source.GetBranch(leafID)
	if len(path) == 0 {
		return nil, fmt.Errorf("Entry %s not found", leafID)
	}
	var copied []Entry
	for _, e := range path {
		if e.Type != TypeSessionInfo && e.Type != TypeLabel {
			copied = append(copied, e)
		}
	}
	info := source.Info
	info.ID = NewULID()
	info.CreatedAt = time.Now().UnixMilli()
	info.ParentSession = source.Path
	ids := map[string]bool{info.ID: true}
	for _, e := range copied {
		ids[e.ID] = true
	}
	out := []Entry{infoEntry(info)}
	out[0].TS = info.CreatedAt
	for _, e := range copied {
		if e.ParentID == "" || !ids[e.ParentID] {
			e.ParentID = info.ID
		}
		out = append(out, e)
	}
	parent := out[len(out)-1].ID
	for _, e := range copied {
		label := source.GetLabel(e.ID)
		if label == "" {
			continue
		}
		id := newEntryID(func(x string) bool { return ids[x] })
		ids[id] = true
		l := label
		out = append(out, Entry{Type: TypeLabel, TS: time.Now().UnixMilli(), TargetID: e.ID, Label: &l, ID: id, ParentID: parent})
		parent = id
	}
	rowID, err := insertSessionWithEntries(info, out)
	if err != nil {
		return nil, err
	}
	s := newSession(info, out)
	s.rowID = rowID
	s.seenOnDisk = len(out)
	return s, nil
}

// Rehome moves the session to a new working directory (/cd).
func (s *Session) Rehome(cwd string) error {
	if s.rowID != 0 {
		if err := UpdateCwd(s.ID, cwd); err != nil {
			return err
		}
	}
	s.Info.CWD = cwd
	s.Path = home.TranscriptPath(cwd, s.ID)
	s.Meta.CWD = cwd
	return nil
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// titleFrom derives a picker title from a prompt: its first line, trimmed.
func titleFrom(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if line == "" {
		return ""
	}
	const max = 60
	r := []rune(line)
	if len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return line
}

// StripHookContext removes the session-start hook context loop wraps a first
// prompt in, so previews show what the user typed.
func StripHookContext(text string) string {
	const open, close = "<session-start-hook-context>\n", "\n</session-start-hook-context>"
	if !strings.HasPrefix(text, open) {
		return text
	}
	i := strings.Index(text, close)
	if i < 0 {
		return text
	}
	return strings.TrimLeft(text[i+len(close):], "\n")
}

// Leaf is the current position's entry.
func (s *Session) Leaf() (Entry, bool) { return s.Entry(s.leaf) }
