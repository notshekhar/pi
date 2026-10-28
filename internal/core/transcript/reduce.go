package transcript

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// The live half (reduce.ts). The rules are the terminal's: text accumulates
// into the open text block; a tool call, a thought or anything else closes it
// and starts the next block below; a turn is everything from a prompt until
// the next one. Nothing is ordered by timestamp.
//
// Unlike loop's immutable reducer this edits the transcript in place — the
// server owns exactly one copy and serializes access to it.

// normalize turns an event payload (a Go struct, a map, a string) into the
// shape the reducer reads.
func normalize(data any) any {
	switch data.(type) {
	case nil, string, map[string]any, []any:
		return data
	}
	raw, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	var out any
	if json.Unmarshal(raw, &out) != nil {
		return nil
	}
	return out
}

func record(data any) map[string]any {
	if m, ok := data.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func num(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// reply is the current assistant message after the last prompt, created
// when there is none yet.
func (t *Transcript) reply(at int64) *Message {
	n := len(t.Messages)
	if n > 0 && t.Messages[n-1].Role == "assistant" {
		return t.Messages[n-1]
	}
	id := fmt.Sprintf("reply-%d", at)
	if n > 0 && t.Messages[n-1].Role == "user" {
		id = t.Messages[n-1].ID + ":reply"
	}
	m := &Message{ID: id, Role: "assistant", Parts: []*Part{}, Metadata: Metadata{CreatedAt: at}}
	t.Messages = append(t.Messages, m)
	return m
}

// closeOpenBlock closes an open text or reasoning block.
func closeOpenBlock(m *Message, at int64) {
	if len(m.Parts) == 0 {
		return
	}
	last := m.Parts[len(m.Parts)-1]
	if (last.Type == "text" || last.Type == "reasoning") && last.State == Streaming {
		last.State = Done
		if last.Type == "reasoning" && last.DurationMs == nil && last.StartedAt != nil {
			d := max(0, at-*last.StartedAt)
			last.DurationMs = &d
		}
	}
}

func appendText(m *Message, text, state string) {
	if text == "" {
		return
	}
	if n := len(m.Parts); n > 0 && m.Parts[n-1].Type == "text" {
		last := m.Parts[n-1]
		last.Text = strPtr(last.text() + text)
		last.State = state
		return
	}
	m.Parts = append(m.Parts, &Part{Type: "text", Text: strPtr(text), State: state})
}

func toolIndex(parts []*Part, id string) int {
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i].IsTool() && parts[i].ToolCallID == id {
			return i
		}
	}
	return -1
}

func ensureTool(m *Message, id, name string, at int64) *Part {
	if i := toolIndex(m.Parts, id); i >= 0 {
		return m.Parts[i]
	}
	closeOpenBlock(m, at)
	p := &Part{Type: "tool-" + name, ToolCallID: id, ToolName: name, State: InputStreaming}
	m.Parts = append(m.Parts, p)
	return p
}

// ResultFailure is the failure a tool result reports (MCP-style
// `{content, isError: true}`), or "" with ok=false.
func ResultFailure(output any) (string, bool) {
	o, ok := output.(map[string]any)
	if !ok || o["isError"] != true {
		return "", false
	}
	if content, ok := o["content"].([]any); ok {
		var b strings.Builder
		for _, c := range content {
			if cm, ok := c.(map[string]any); ok {
				b.WriteString(str(cm["text"]))
			}
		}
		if s := strings.TrimSpace(b.String()); s != "" {
			return s, true
		}
	}
	return "Failed", true
}

func errorText(e any) string {
	switch v := e.(type) {
	case string:
		return v
	case map[string]any:
		if msg, ok := v["message"]; ok {
			return fmt.Sprint(msg)
		}
	}
	raw, err := json.Marshal(e)
	if err != nil {
		return fmt.Sprint(e)
	}
	return string(raw)
}

// settle finishes everything still open: the turn is over.
func settle(m *Message, at int64) {
	for _, p := range m.Parts {
		switch {
		case (p.Type == "text" || p.Type == "reasoning") && p.State == Streaming:
			p.State = Done
			if p.Type == "reasoning" && p.DurationMs == nil && p.StartedAt != nil {
				d := max(0, at-*p.StartedAt)
				p.DurationMs = &d
			}
		case p.IsTool() && (p.State == InputStreaming || p.State == InputAvailable):
			p.State, p.ErrorText = OutputError, Interrupted
		case p.Type == "data-compaction" && p.Data["running"] == true:
			p.Data["running"], p.Data["aborted"] = false, true
		}
	}
}

func pushStep(run *SubagentRun, step SubagentStep) {
	run.Steps = append(run.Steps, step)
	for len(run.Steps) > MaxSubagentSteps {
		run.Steps = run.Steps[1:]
		run.Dropped++
	}
}

var replyContent = map[string]bool{
	"text-delta": true, "reasoning-start": true, "reasoning-delta": true,
	"tool-input-start": true, "tool-call": true, "hook-message": true, "compact-start": true,
}

func (t *Transcript) lastIsReply() bool {
	return len(t.Messages) > 0 && t.Messages[len(t.Messages)-1].Role == "assistant"
}

func (t *Transcript) hasOpenParts() bool {
	if !t.lastIsReply() {
		return false
	}
	for _, p := range t.Messages[len(t.Messages)-1].Parts {
		if (p.Type == "text" || p.Type == "reasoning") && p.State == Streaming {
			return true
		}
		if p.IsTool() && (p.State == InputStreaming || p.State == InputAvailable) {
			return true
		}
	}
	return false
}

// Apply applies one live event observed at `at` (epoch ms; durations only).
func (t *Transcript) Apply(ev Event, at int64) {
	if at == 0 {
		at = time.Now().UnixMilli()
	}
	data := normalize(ev.Data)
	rec := record(data)

	// Idle compaction runs between turns: a marker of its own.
	if ev.Type == "compact-start" && rec["reason"] == "idle" {
		t.Messages = append(t.Messages, &Message{ID: fmt.Sprintf("compact-idle-%d", at), Role: "system",
			Parts: []*Part{{Type: "data-compaction", Data: map[string]any{"running": true, "reason": "idle"}}}, Metadata: Metadata{CreatedAt: at}})
		return
	}
	if ev.Type == "compact-end" {
		for i := len(t.Messages) - 1; i >= 0; i-- {
			m := t.Messages[i]
			if m.Role != "system" || len(m.Parts) == 0 {
				continue
			}
			first := m.Parts[0]
			if first.Type == "data-compaction" && first.Data["running"] == true && first.Data["reason"] == "idle" {
				if rec["aborted"] == true {
					t.Messages = append(t.Messages[:i], t.Messages[i+1:]...)
					return
				}
				d := map[string]any{"running": false, "reason": "idle"}
				copyCompaction(d, rec, false)
				m.Parts = []*Part{{Type: "data-compaction", Data: d}}
				return
			}
		}
	}
	// A reply arriving with no turn running is a new turn whose prompt this
	// stream never carried.
	if replyContent[ev.Type] && !t.Running {
		if t.lastIsReply() {
			t.Messages = append(t.Messages, &Message{ID: fmt.Sprintf("reply-%d", at), Role: "assistant", Parts: []*Part{}, Metadata: Metadata{CreatedAt: at}})
		}
		t.Running = true
	}

	switch ev.Type {
	case "user-message":
		id := str(rec["id"])
		if id == "" {
			id = fmt.Sprintf("user-%d", at)
		}
		for _, m := range t.Messages {
			if m.ID == id {
				return
			}
		}
		createdAt := at
		if n, ok := num(rec["ts"]); ok {
			createdAt = int64(n)
		}
		text := str(rec["text"])
		parts := []*Part{}
		if team := teamPartOf(record(rec["team"]), text); team != nil {
			parts = append(parts, team)
		} else if text != "" {
			parts = append(parts, &Part{Type: "text", Text: strPtr(text), State: Done})
		}
		t.Running = true
		t.Messages = append(t.Messages, &Message{ID: id, Role: "user", Parts: parts, Metadata: Metadata{CreatedAt: createdAt}})
	case "team-message":
		team := teamPartOf(record(rec["team"]), "")
		if team == nil {
			return
		}
		m := t.reply(at)
		closeOpenBlock(m, at)
		team.Data["midTurn"] = true
		m.Parts = append(m.Parts, team)
	case "attached-images":
		urls, _ := data.([]any)
		if len(urls) == 0 {
			return
		}
		for i := len(t.Messages) - 1; i >= 0; i-- {
			if t.Messages[i].Role != "user" {
				continue
			}
			for _, u := range urls {
				if s, ok := u.(string); ok {
					t.Messages[i].Parts = append(t.Messages[i].Parts, &Part{Type: "file", URL: s, MediaType: "image/*"})
				}
			}
			break
		}
	case "session-running":
		running := rec["running"] == true
		if running {
			t.Running = true
			return
		}
		if !t.Running && !t.hasOpenParts() {
			return
		}
		t.Running = false
		if t.lastIsReply() {
			settle(t.reply(at), at)
		}
	case "text-delta":
		text := str(data)
		if text == "" {
			return
		}
		m := t.reply(at)
		if n := len(m.Parts); n > 0 && m.Parts[n-1].Type == "reasoning" && m.Parts[n-1].State == Streaming {
			closeOpenBlock(m, at)
		}
		appendText(m, text, Streaming)
	case "reasoning-start":
		m := t.reply(at)
		closeOpenBlock(m, at)
		start := at
		m.Parts = append(m.Parts, &Part{Type: "reasoning", Text: strPtr(""), State: Streaming, StartedAt: &start})
	case "reasoning-delta":
		text := str(data)
		if text == "" {
			return
		}
		m := t.reply(at)
		if n := len(m.Parts); n > 0 && m.Parts[n-1].Type == "reasoning" && m.Parts[n-1].State == Streaming {
			m.Parts[n-1].Text = strPtr(m.Parts[n-1].text() + text)
		} else {
			closeOpenBlock(m, at)
			start := at
			m.Parts = append(m.Parts, &Part{Type: "reasoning", Text: strPtr(text), State: Streaming, StartedAt: &start})
		}
	case "reasoning-end":
		m := t.reply(at)
		if n := len(m.Parts); n > 0 && m.Parts[n-1].Type == "reasoning" {
			closeOpenBlock(m, at)
		}
	case "tool-input-start":
		id := str(rec["toolCallId"])
		if id == "" {
			return
		}
		name := str(rec["toolName"])
		if name == "" {
			name = "tool"
		}
		ensureTool(t.reply(at), id, name, at)
	case "tool-input-delta":
		id, delta := str(rec["toolCallId"]), str(rec["delta"])
		if id == "" || delta == "" {
			return
		}
		m := t.reply(at)
		if i := toolIndex(m.Parts, id); i >= 0 {
			p := m.Parts[i]
			cur := ""
			if p.InputText != nil {
				cur = *p.InputText
			}
			p.InputText = strPtr(cur + delta)
		}
	case "tool-call", "tool-input-updated":
		id := str(rec["toolCallId"])
		if id == "" {
			return
		}
		name := str(rec["toolName"])
		if name == "" {
			name = "tool"
		}
		rewrite := ev.Type == "tool-input-updated"
		p := ensureTool(t.reply(at), id, name, at)
		if rewrite || !p.InputRewritten {
			if in, ok := rec["input"]; ok && in != nil {
				p.Input = in
			} else if p.Input == nil {
				p.Input = map[string]any{}
			}
		}
		if rewrite {
			p.InputRewritten = true
		}
		if p.State == InputStreaming {
			p.State = InputAvailable
		}
	case "tool-result", "tool-error":
		id := str(rec["toolCallId"])
		if id == "" {
			return
		}
		name := str(rec["toolName"])
		if name == "" {
			name = "tool"
		}
		p := ensureTool(t.reply(at), id, name, at)
		p.InputText = nil
		if ev.Type == "tool-error" {
			p.State, p.ErrorText = OutputError, errorText(rec["error"])
		} else if failure, failed := ResultFailure(rec["output"]); failed {
			p.State, p.ErrorText = OutputError, failure
		} else {
			p.State, p.Output = OutputAvailable, rec["output"]
		}
		if p.Subagent != nil && !p.Subagent.Finished {
			p.Subagent.Current = ""
			p.Subagent.Finished = true
		}
	case "subagent-delta", "subagent-tool", "subagent-step-usage", "subagent-finish":
		id := str(rec["toolCallId"])
		if id == "" {
			return
		}
		agent := str(rec["agent"])
		if agent == "" {
			agent = "agent"
		}
		p := ensureTool(t.reply(at), id, "task", at)
		if p.Subagent == nil {
			p.Subagent = &SubagentRun{Agent: agent, Steps: []SubagentStep{}}
		}
		run := p.Subagent
		switch ev.Type {
		case "subagent-delta":
			text := str(rec["text"])
			if n := len(run.Steps); n > 0 && run.Steps[n-1].Type == "text" {
				run.Steps[n-1].Text += text
			} else if text != "" {
				pushStep(run, SubagentStep{Type: "text", Text: text})
			}
		case "subagent-tool":
			name := str(rec["toolName"])
			if name == "" {
				name = "tool"
			}
			pushStep(run, SubagentStep{Type: "tool", Name: name, Input: rec["input"]})
			run.Current = name
		case "subagent-step-usage":
			if n, ok := num(rec["steps"]); ok {
				v := int(n)
				run.StepCount = &v
			}
			if n, ok := num(rec["usd"]); ok {
				run.USD = &n
			}
		default:
			run.Current = "finishing"
		}
	case "hook-message":
		text := str(data)
		if strings.TrimSpace(text) == "" {
			return
		}
		m := t.reply(at)
		closeOpenBlock(m, at)
		m.Parts = append(m.Parts, &Part{Type: "data-hook", Data: map[string]any{"text": text}})
	case "compact-start":
		m := t.reply(at)
		closeOpenBlock(m, at)
		d := map[string]any{"running": true}
		if r, ok := rec["reason"].(string); ok {
			d["reason"] = r
		}
		m.Parts = append(m.Parts, &Part{Type: "data-compaction", Data: d})
	case "compact-end":
		m := t.reply(at)
		var target *Part
		for i := len(m.Parts) - 1; i >= 0; i-- {
			if m.Parts[i].Type == "data-compaction" && m.Parts[i].Data["running"] == true {
				target = m.Parts[i]
				break
			}
		}
		if target == nil {
			target = &Part{Type: "data-compaction", Data: map[string]any{"running": true}}
			m.Parts = append(m.Parts, target)
		}
		target.Data["running"] = false
		copyCompaction(target.Data, rec, true)
	case "stream-retry":
		m := t.reply(at)
		closeOpenBlock(m, at)
		attempt, _ := num(rec["attempt"])
		mx, _ := num(rec["max"])
		m.Parts = append(m.Parts, &Part{Type: "data-retry", Data: map[string]any{"attempt": attempt, "max": mx, "reason": str(rec["reason"])}})
	case "data-recap":
		text := strings.TrimSpace(str(rec["text"]))
		if text == "" {
			return
		}
		m := t.reply(at)
		closeOpenBlock(m, at)
		m.Parts = append(m.Parts, &Part{Type: "data-recap", Data: map[string]any{"text": text}})
	case "todo-update":
		var items []Todo
		raw, _ := json.Marshal(rec["items"])
		_ = json.Unmarshal(raw, &items)
		if items == nil {
			items = []Todo{}
		}
		t.Todos = items
	case "error":
		m := t.reply(at)
		closeOpenBlock(m, at)
		m.Parts = append(m.Parts, &Part{Type: "data-error", Data: map[string]any{"message": errorText(data)}})
	case "finish":
		settle(t.reply(at), at)
	}
}

// copyCompaction carries a compact-end's fields onto a compaction part.
func copyCompaction(d, rec map[string]any, withHandoff bool) {
	if s, ok := rec["summary"].(string); ok && s != "" {
		d["summary"] = s
	}
	if n, ok := num(rec["tokensBefore"]); ok {
		d["tokensBefore"] = n
	}
	if n, ok := num(rec["tokensAfter"]); ok {
		d["tokensAfter"] = n
	}
	if withHandoff {
		if s, ok := rec["handoff"].(string); ok {
			d["handoff"] = s
		}
	}
	if s, ok := rec["error"].(string); ok {
		d["error"] = s
	}
	if withHandoff && rec["aborted"] == true {
		d["aborted"] = true
	}
}

// teamPartOf is a team entry as its card (transcript/team.ts).
func teamPartOf(meta map[string]any, text string) *Part {
	kind := str(meta["kind"])
	teamID, ok := meta["teamId"].(string)
	if (kind != "spawn" && kind != "mail") || !ok {
		return nil
	}
	d := map[string]any{"kind": kind, "teamId": teamID}
	if from, ok := meta["from"].(map[string]any); ok {
		d["from"] = map[string]any{"id": from["id"], "title": from["title"]}
	}
	if title := str(meta["title"]); title != "" {
		d["title"] = title
	}
	if kind == "spawn" {
		d["text"] = text
	}
	if mail, ok := meta["mail"].([]any); ok {
		out := make([]any, 0, len(mail))
		for _, item := range mail {
			m := record(item)
			k := str(m["kind"])
			if k != "report" && k != "update" {
				k = "message"
			}
			from := record(m["from"])
			out = append(out, map[string]any{"id": m["id"], "from": map[string]any{"id": from["id"], "title": from["title"]},
				"kind": k, "text": m["text"], "ts": m["ts"]})
		}
		d["mail"] = out
	}
	if meta["midTurn"] == true {
		d["midTurn"] = true
	}
	return &Part{Type: "data-team", Data: d}
}
