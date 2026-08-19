package tui

import (
	"fmt"
	"strings"
	"sync"
)

// Tool grouping: a run of consecutive finished calls folds into one row.
//
// `◈ Read 3 files` instead of three near-identical rows nobody reads. This is
// a SECOND fold level, above each call's own expand — and it only earns its
// place because it can be opened again. Hiding detail behind a fold with no
// way back would just be information loss with extra steps, which is why it
// belongs to nav and to the live variant rather than to the plain transcript.
//
// Ported from loop's verb-group vocabulary, which took it from grok. The
// distinction that makes the whole thing work is that a tool is classified
// into a KIND, and the kind decides both the grammar and whether a run folds
// at all:
//
//   - Kinds are bucketed, not tools. `grep` and `glob` are both searches, so
//     a run of them reads "Searched 2 patterns" rather than two segments;
//     `read` and `skill` share a verb but not a noun, so they stay apart as
//     "Read 2 files, Read 1 skill".
//   - Everything folds except the surfaces the user has to ACT on. A question
//     or a plan folded into a count is one nobody answers.
//
// ## Tools we did not write
//
// Extensions and MCP servers introduce tools this file has never heard of,
// and they are classified by RULE rather than by guesswork. Three layers,
// most specific first: an explicit registration, then the builtins by exact
// name, then — for anything else — only what is actually known, which is
// where it came from.
//
// A heuristic reading a leading verb off the name (`list_*` → dir) is
// deliberately absent. loop had one and removed it, because it produced both
// failures grouping exists to avoid: folding became a lottery on how a server
// spelled things, and when it did fire it borrowed the builtin's NOUN along
// with the verb, so a run of Sentry lookups rendered as "Listed 2 dirs". A
// verb travels to a third-party tool; the noun it was paired with does not.

// verbKind is a kind's grammar: a tense-aware verb and a countable noun.
type verbKind struct {
	past, present     string
	nounOne, nounMany string
	// folds is whether a run of these collapses on its own. False keeps the
	// rows visible; the kind is still used to NAME them inside a header that
	// hides them for another reason.
	folds bool
}

const (
	kindFile      = "file"
	kindSkill     = "skill"
	kindDir       = "dir"
	kindSearch    = "search"
	kindWeb       = "web"
	kindMemory    = "memory"
	kindSubagent  = "subagent"
	kindTodo      = "todo"
	kindCommand   = "command"
	kindEdit      = "edit"
	kindMCP       = "mcp"
	kindExtension = "extension"
	kindAsk       = "ask"
	kindPlan      = "plan"
)

var kinds = map[string]verbKind{
	kindFile:     {"Read", "Reading", "file", "files", true},
	kindSkill:    {"Read", "Reading", "skill", "skills", true},
	kindDir:      {"Listed", "Listing", "dir", "dirs", true},
	kindSearch:   {"Searched", "Searching", "pattern", "patterns", true},
	kindWeb:      {"Fetched", "Fetching", "website", "websites", true},
	kindMemory:   {"Searched", "Searching", "memory", "memories", true},
	kindSubagent: {"Ran", "Running", "subagent", "subagents", true},
	kindTodo:     {"Updated", "Updating", "todo list", "todo lists", true},
	kindCommand:  {"Ran", "Running", "command", "commands", true},
	kindEdit:     {"Edited", "Editing", "file", "files", true},

	// Tools we did not write, named by the only thing reliably known about
	// them — where they came from. Both fold.
	kindMCP:       {"Called", "Calling", "MCP tool", "MCP tools", true},
	kindExtension: {"Called", "Calling", "extension tool", "extension tools", true},

	// The only kinds that keep their rows.
	kindAsk:  {"Asked", "Asking", "question", "questions", false},
	kindPlan: {"Planned", "Planning", "plan", "plans", false},
}

// builtins are pi-agent's own tools. Anything absent is somebody else's.
var builtins = map[string]string{
	"read":            kindFile,
	"skill":           kindSkill,
	"ls":              kindDir,
	"tree":            kindDir,
	"glob":            kindSearch,
	"grep":            kindSearch,
	"find":            kindSearch,
	"websearch":       kindSearch,
	"webfetch":        kindWeb,
	"memory":          kindMemory,
	"task":            kindSubagent,
	"todo":            kindTodo,
	"bash":            kindCommand,
	"edit":            kindEdit,
	"write":           kindEdit,
	"ask":             kindAsk,
	"plan":            kindPlan,
	"enter_plan_mode": kindPlan,
}

var (
	registeredMu    sync.RWMutex
	registeredKinds = map[string]string{}
)

// RegisterToolVerbGroup declares how a tool should be grouped and named:
// `RegisterToolVerbGroup("fetch_issues", "web")`.
//
// The ONLY way a third-party tool gets a builtin's grammar, and it beats
// every rule below it — deliberately, because a name is not evidence.
func RegisterToolVerbGroup(tool, kind string) {
	if _, ok := kinds[kind]; !ok {
		return
	}
	registeredMu.Lock()
	registeredKinds[tool] = kind
	registeredMu.Unlock()
}

// ClearToolVerbGroups drops all registrations (extension reload, tests).
func ClearToolVerbGroups() {
	registeredMu.Lock()
	registeredKinds = map[string]string{}
	registeredMu.Unlock()
}

// kindIDOf is the kind for a tool name — see the layering note above.
func kindIDOf(tool string) string {
	registeredMu.RLock()
	explicit, ok := registeredKinds[tool]
	registeredMu.RUnlock()
	if ok {
		return explicit
	}
	if builtin, ok := builtins[tool]; ok {
		return builtin
	}
	// Not ours and not registered: say where it came from and nothing more.
	// MCP tools arrive namespaced as `server__tool`.
	if strings.Contains(tool, "__") {
		return kindMCP
	}
	return kindExtension
}

func kindOf(tool string) verbKind { return kinds[kindIDOf(tool)] }

// foldsEagerly reports whether a run of this tool collapses on its own.
func foldsEagerly(tool string) bool { return kindOf(tool).folds }

// groupMember is one row's contribution to a header.
type groupMember struct {
	tool    string
	isError bool
	running bool
}

// verbGroupLabel is the aggregated header text for a run: one segment per
// kind in first-seen order, joined with ", ", plus the failures.
//
// Kinds are bucketed rather than tools, so `read` + `skill` read as two
// honest segments while `grep` + `glob` merge into one. Tense follows the
// run: any member still running makes the whole label present-tense, because
// the run as a whole is still happening.
func verbGroupLabel(members []groupMember) (string, int) {
	running := false
	for _, m := range members {
		if m.running {
			running = true
			break
		}
	}
	var order []string
	counts := map[string]int{}
	failed := 0
	for _, m := range members {
		id := kindIDOf(m.tool)
		if _, seen := counts[id]; !seen {
			order = append(order, id)
		}
		counts[id]++
		if m.isError {
			failed++
		}
	}
	segments := make([]string, 0, len(order))
	for _, id := range order {
		k, n := kinds[id], counts[id]
		verb, noun := k.past, k.nounMany
		if running {
			verb = k.present
		}
		if n == 1 {
			noun = k.nounOne
		}
		segments = append(segments, fmt.Sprintf("%s %d %s", verb, n, noun))
	}
	return strings.Join(segments, ", "), failed
}

// groupable reports whether an entry may be swallowed into a run.
//
// A RUNNING call never groups. Live mode is the base mode plus folding, not a
// different way to watch a turn — while a call is in flight it renders the
// row noir renders normally, showing which file is being read and its status.
// Grouping mid-flight was tried in loop (it holds the transcript's height
// still) and hid the one thing you look at a transcript mid-turn to see.
//
// An OPEN call also leaves the group, and the kinds the user has to act on
// never join. A FAILURE does group: it is reported on the header rather than
// hidden, which is what the failure count is for.
func (e *entry) groupable() bool {
	return e.kind == entTool &&
		!e.tool.IsPartial &&
		!e.tool.Interrupted &&
		!e.expanded &&
		!e.selected &&
		foldsEagerly(e.tool.Name)
}

// group is a folded run of consecutive tool rows.
type group struct {
	start  int // index of the first member
	count  int
	label  string
	failed int
	opened bool
}

// minGroup is the shortest run worth folding.
//
// ONE, as in grok and loop: "Read 1 file" is already tighter than the row it
// replaces, and folding from the very first call means a second one JOINS an
// existing header rather than the row visibly collapsing under you as the
// turn goes on.
const minGroup = 1

// findGroups scans the entries for runs worth folding.
//
// Returns one group per run, keyed by the index of its first member, so the
// renderer can substitute a header and skip the rest. A run is any stretch of
// adjacent groupable rows — mixed tools included, because the label describes
// each kind in it.
func findGroups(entries []*entry, opened map[int]bool) map[int]group {
	groups := map[int]group{}
	i := 0
	for i < len(entries) {
		if !entries[i].groupable() {
			i++
			continue
		}
		j := i
		var members []groupMember
		for j < len(entries) && entries[j].groupable() {
			members = append(members, groupMember{
				tool:    entries[j].tool.Name,
				isError: entries[j].tool.IsError,
				running: entries[j].tool.IsPartial,
			})
			j++
		}
		if n := j - i; n >= minGroup {
			label, failed := verbGroupLabel(members)
			groups[i] = group{
				start: i, count: n, label: label, failed: failed,
				// Openness is remembered against EVERY member, not just the
				// head: expanding the first call drops it out of the run,
				// which promotes the second to head, and a lookup keyed on
				// the head alone would miss and snap the rest shut — the
				// group appearing to re-collapse the moment you opened
				// something inside it.
				opened: anyOpen(opened, i, j),
			}
		}
		i = j
	}
	return groups
}

// groupOf builds a header for a named run, for demos and tests.
func groupOf(tools ...string) group {
	members := make([]groupMember, 0, len(tools))
	for _, t := range tools {
		members = append(members, groupMember{tool: t})
	}
	label, failed := verbGroupLabel(members)
	return group{count: len(tools), label: label, failed: failed}
}

func anyOpen(opened map[int]bool, start, end int) bool {
	for i := start; i < end; i++ {
		if opened[i] {
			return true
		}
	}
	return false
}

// renderGroupHeader draws a folded run's single row.
func renderGroupHeader(t *Theme, g group, selected, nav bool, width int) []string {
	bg := t.Hex(SlotBgBase)
	spec := RailFor(BlockState{Expanded: false}, railColors(t), bg)

	// A different glyph from a tool row's ◆ on purpose: this row stands for
	// several, and it should not read as one more call.
	diamond := t.Fg(SlotMuted, "◈")
	slot := SlotMuted
	if selected {
		slot = SlotText
	}
	row := diamond + " " + t.Fg(slot, g.label)
	// Folding must never be a way to lose bad news.
	if g.failed > 0 {
		row += t.Fg(SlotError, fmt.Sprintf(" · %d failed", g.failed))
	}
	if selected {
		row += t.Fg(SlotDim, " ("+expandHintFor(nav)+" to open)")
	}
	return append([]string{""}, WithRail(t, []string{fitRow(row, width-RailWidth)}, spec, bg, 0)...)
}
