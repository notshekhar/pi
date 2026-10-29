package session

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// What the MODEL is sent, as opposed to what the session holds — a port of
// loop's toModelMessages (agent/model-messages.ts) and compactedContextEntries
// (agent/compact.ts). The texts below are loop's, verbatim: a session resumed
// in either program must produce the same prompt, or the provider's prompt
// cache misses and the two programs answer from different contexts.

const (
	interruptNote = "[The user interrupted this response before it finished.]"

	branchSummaryPreamble = "The user explored a different conversation branch before returning here.\nSummary of that exploration:\n\n"

	compactionPrefix = "The conversation history before this point was compacted into the following summary:\n\n<summary>\n"
	compactionSuffix = "\n</summary>"

	rolloverPreamble = "A fresh context window starts here. The conversation before this point left your active context WITHOUT a summary. It is intact in this session and recoverable with the history tool.\n\n" +
		"What follows is a recovery record of inputs and state — NOT a record of progress. The previous window may already have finished some or all of its work. Restore the todo list, read the notes it names, and verify live state before continuing any stateful or external work.\n\n<handoff>"
	rolloverSuffix = "\n</handoff>"

	todosKind       = "todos"
	nativeAgentKey  = "nativeAgent"
	nativeResultMax = 4000
)

var nativeAgentLabel = map[string]string{
	"claude-code":  "Claude Code",
	"cursor-agent": "Cursor",
	"codex-cli":    "Codex",
	"antigravity":  "Antigravity",
}

// TodoItem is one row of the todo checklist as loop stores it.
type TodoItem struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// CompactionBlockText is the user message standing in for a compacted span:
// the summary, or for a rollover the handoff record.
func CompactionBlockText(summary, handoff string) string {
	if handoff != "" {
		return rolloverPreamble + "\n" + handoff + rolloverSuffix
	}
	return compactionPrefix + summary + compactionSuffix
}

// contextEntry is one item of the compacted context.
type contextEntry struct {
	subagent      bool
	agent, result string
	role          string
	content       json.RawMessage
	interrupted   bool
}

func stringContent(s string) json.RawMessage {
	raw, _ := json.Marshal(s)
	return raw
}

// latestCompact is the last compaction ON THE CURRENT BRANCH — one on an
// abandoned branch must not apply after /tree moved the leaf.
func latestCompact(branch []Entry) *Entry {
	var latest *Entry
	for i := range branch {
		if branch[i].Type == TypeCompact {
			latest = &branch[i]
		}
	}
	return latest
}

func todosOf(payload json.RawMessage) ([]TodoItem, bool) {
	var p struct {
		Kind  string     `json:"kind"`
		Items []TodoItem `json:"items"`
	}
	if json.Unmarshal(payload, &p) != nil || p.Kind != todosKind || p.Items == nil {
		return nil, false
	}
	return p.Items, true
}

func hasActiveTodos(items []TodoItem) bool {
	for _, t := range items {
		if t.Status == "pending" || t.Status == "in_progress" {
			return true
		}
	}
	return false
}

func formatTodoList(items []TodoItem) string {
	lines := make([]string, len(items))
	for i, t := range items {
		lines[i] = fmt.Sprintf("%d. [%s] %s", i+1, t.Status, t.Content)
	}
	return strings.Join(lines, "\n")
}

// compactedContext is loop's compactedContextEntries.
func (s *Session) compactedContext() []contextEntry {
	branch := s.CurrentBranch()
	compact := latestCompact(branch)
	var out []contextEntry
	messageIndex := 0
	var preCutTodos []TodoItem
	for _, e := range branch {
		switch {
		case e.Type == TypeMessage:
			idx := messageIndex
			messageIndex++
			if compact != nil && idx < compact.CutAt {
				continue
			}
			out = append(out, contextEntry{role: e.Role, content: e.Content, interrupted: e.Interrupted})
		case e.Type == TypeSubagent:
			if compact != nil && messageIndex < compact.CutAt {
				continue
			}
			out = append(out, contextEntry{subagent: true, agent: e.Agent, result: e.Result})
		case e.Type == TypeBranchSummary && e.Summary != "":
			if compact != nil && messageIndex < compact.CutAt {
				continue
			}
			out = append(out, contextEntry{role: "user", content: stringContent(branchSummaryPreamble + e.Summary)})
		case e.Type == TypeCustom && compact != nil:
			if items, ok := todosOf(e.Payload); ok {
				if messageIndex < compact.CutAt {
					preCutTodos = items
				} else {
					preCutTodos = nil
				}
			}
		}
	}
	if compact != nil {
		if preCutTodos != nil && hasActiveTodos(preCutTodos) {
			where := "the compaction above"
			if compact.Handoff != "" {
				where = "the context rollover above"
			}
			text := "Your todo checklist was active before " + where + ". Current list:\n" +
				formatTodoList(preCutTodos) + "\n" +
				"Keep maintaining it with the todo tool — resend the full list, updated, as you make progress."
			out = append([]contextEntry{{role: "user", content: stringContent(text)}}, out...)
		}
		out = append([]contextEntry{{role: "user", content: stringContent(CompactionBlockText(compact.Summary, compact.Handoff))}}, out...)
	}
	return out
}

// ModelMessages is the current branch as the model must see it — loop's
// toModelMessages.
func (s *Session) ModelMessages() []provider.Message {
	var out []provider.Message
	push := func(role string, content json.RawMessage) {
		if m, ok := DecodeContent(role, content); ok {
			out = append(out, m)
		}
	}
	for _, m := range s.compactedContext() {
		// Subagent reports stay in context across resumes.
		if m.subagent {
			push("assistant", stringContent("[subagent "+m.agent+" report]\n"+m.result))
			continue
		}
		if m.role == "tool" {
			push("tool", m.content)
			continue
		}
		var str string
		if json.Unmarshal(m.content, &str) == nil {
			// Anthropic rejects empty text blocks: an empty string turn is
			// dropped unless it was interrupted, when it becomes the note.
			if strings.TrimSpace(str) == "" {
				if m.interrupted {
					push("assistant", stringContent(interruptNote))
				}
				continue
			}
			if m.interrupted {
				str += "\n\n" + interruptNote
			}
			push(m.role, stringContent(str))
			continue
		}
		var parts []json.RawMessage
		if json.Unmarshal(m.content, &parts) == nil && len(parts) > 0 {
			// A native agent's tool calls already ran inside it; no other
			// model can answer them, so they travel as text.
			if hasNativeAgentParts(parts) {
				parts = flattenNativeAgentParts(parts)
			}
			if m.interrupted {
				parts = append(parts, json.RawMessage(mustJSON(map[string]string{"type": "text", "text": interruptNote})))
			}
			push(m.role, json.RawMessage(mustJSON(parts)))
		} else if m.interrupted {
			push("assistant", stringContent(interruptNote))
		}
	}
	return out
}

func mustJSON(v any) string {
	raw, _ := json.Marshal(v)
	return string(raw)
}

type nativePart struct {
	Type            string                     `json:"type"`
	ToolName        string                     `json:"toolName"`
	Input           json.RawMessage            `json:"input"`
	Output          json.RawMessage            `json:"output"`
	ProviderOptions map[string]json.RawMessage `json:"providerOptions"`
}

func nativeProvider(p nativePart) string {
	raw, ok := p.ProviderOptions[nativeAgentKey]
	if !ok {
		return ""
	}
	var tag struct {
		Provider string `json:"provider"`
	}
	_ = json.Unmarshal(raw, &tag)
	return tag.Provider
}

func hasNativeAgentParts(parts []json.RawMessage) bool {
	for _, raw := range parts {
		var p nativePart
		if json.Unmarshal(raw, &p) == nil && nativeProvider(p) != "" {
			return true
		}
	}
	return false
}

// stringifyJSON is loop's stringify: a string as-is, anything else as JSON.
func stringifyJSON(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func flattenNativeAgentParts(parts []json.RawMessage) []json.RawMessage {
	var out []json.RawMessage
	for _, raw := range parts {
		var p nativePart
		if json.Unmarshal(raw, &p) != nil {
			out = append(out, raw)
			continue
		}
		prov := nativeProvider(p)
		if prov == "" {
			out = append(out, raw)
			continue
		}
		label := nativeAgentLabel[prov]
		if label == "" {
			label = prov
		}
		switch p.Type {
		case "tool-call":
			text := "[" + label + " ran " + p.ToolName + ": " + stringifyJSON(p.Input) + "]"
			out = append(out, json.RawMessage(mustJSON(map[string]string{"type": "text", "text": text})))
		case "tool-result":
			var o struct {
				Value json.RawMessage `json:"value"`
			}
			value := p.Output
			if json.Unmarshal(p.Output, &o) == nil && len(o.Value) > 0 {
				value = o.Value
			}
			result := stringifyJSON(value)
			if len(result) > nativeResultMax {
				result = result[:nativeResultMax] + "…"
			}
			text := "[" + p.ToolName + " result]\n" + result
			out = append(out, json.RawMessage(mustJSON(map[string]string{"type": "text", "text": text})))
		}
	}
	return out
}

// BranchMessages is the current branch's message entries, oldest first —
// loop's session.messages(): the raw stored shape compaction reads.
func (s *Session) BranchMessages() []Entry {
	var out []Entry
	for _, e := range s.CurrentBranch() {
		if e.Type == TypeMessage {
			out = append(out, e)
		}
	}
	return out
}

// LatestCompact is the last compaction on the current branch, if any.
func (s *Session) LatestCompact() (Entry, bool) {
	if c := latestCompact(s.CurrentBranch()); c != nil {
		return *c, true
	}
	return Entry{}, false
}

// FileLists is a compaction's record of the files its span read and changed.
type FileLists struct {
	ReadFiles     []string `json:"readFiles"`
	ModifiedFiles []string `json:"modifiedFiles"`
}

// CompactDetails is the file lists a compact entry carries, if any.
func (e Entry) CompactDetails() FileLists {
	var f FileLists
	if raw, ok := e.Extra["details"]; ok {
		_ = json.Unmarshal(raw, &f)
	}
	return f
}

// TodosOf is the checklist a todo custom entry's payload carries.
func TodosOf(payload json.RawMessage) ([]TodoItem, bool) { return todosOf(payload) }

// HasActiveTodos reports whether any item is still pending or in progress.
func HasActiveTodos(items []TodoItem) bool { return hasActiveTodos(items) }

// FormatTodoList is the checklist as the model reads it: "1. [status] content".
func FormatTodoList(items []TodoItem) string { return formatTodoList(items) }
