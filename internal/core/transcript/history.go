package transcript

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/core/session"
)

// The saved half (history.ts): a branch, root to leaf, as a transcript.

type contentPart struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ToolName   string          `json:"toolName"`
	ToolCallID string          `json:"toolCallId"`
	Input      json.RawMessage `json:"input"`
	Output     json.RawMessage `json:"output"`
	Image      json.RawMessage `json:"image"`
	MediaType  string          `json:"mediaType"`
}

func decodeAny(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return v
}

// unwrapOutput is a saved tool output as the live `tool-result` carries it.
func unwrapOutput(raw json.RawMessage) (any, string, bool) {
	out := decodeAny(raw)
	if o, ok := out.(map[string]any); ok {
		if typ, ok := o["type"].(string); ok {
			switch typ {
			case "error-text", "error-json":
				v := o["value"]
				if s, ok := v.(string); ok {
					return v, s, true
				}
				b, _ := json.Marshal(v)
				return v, string(b), true
			case "text", "json", "content":
				v := o["value"]
				if f, failed := ResultFailure(v); failed {
					return v, f, true
				}
				return v, "", false
			}
		}
	}
	if f, failed := ResultFailure(out); failed {
		return out, f, true
	}
	return out, "", false
}

func userParts(raw json.RawMessage) []*Part {
	parts := []*Part{}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s != "" {
			parts = append(parts, &Part{Type: "text", Text: strPtr(s), State: Done})
		}
		return parts
	}
	var cps []contentPart
	if json.Unmarshal(raw, &cps) != nil {
		return parts
	}
	for _, p := range cps {
		switch {
		case p.Type == "text" && p.Text != "":
			if n := len(parts); n > 0 && parts[n-1].Type == "text" {
				parts[n-1].Text = strPtr(parts[n-1].text() + p.Text)
			} else {
				parts = append(parts, &Part{Type: "text", Text: strPtr(p.Text), State: Done})
			}
		case p.Type == "image" || p.Type == "file":
			var url string
			if json.Unmarshal(p.Image, &url) == nil {
				mt := p.MediaType
				if mt == "" {
					mt = "image/*"
				}
				parts = append(parts, &Part{Type: "file", URL: url, MediaType: mt})
			}
		}
	}
	return parts
}

func extraString(e session.Entry, key string) string {
	var s string
	if raw, ok := e.Extra[key]; ok {
		_ = json.Unmarshal(raw, &s)
	}
	return s
}

func subagentRun(e session.Entry) *SubagentRun {
	run := &SubagentRun{Agent: e.Agent, Steps: []SubagentStep{}, Finished: true}
	if run.Agent == "" {
		run.Agent = "agent"
	}
	var activity []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Name    string `json:"name"`
		Summary any    `json:"summary"`
	}
	if raw, ok := e.Extra["activity"]; ok {
		_ = json.Unmarshal(raw, &activity)
	}
	for _, a := range activity {
		switch {
		case a.Type == "text" && a.Text != "":
			run.Steps = append(run.Steps, SubagentStep{Type: "text", Text: a.Text})
		case a.Type == "reasoning" && a.Text != "":
			run.Steps = append(run.Steps, SubagentStep{Type: "reasoning", Text: a.Text})
		case a.Type == "tool":
			name := a.Name
			if name == "" {
				name = "tool"
			}
			run.Steps = append(run.Steps, SubagentStep{Type: "tool", Name: name, Input: a.Summary})
		}
	}
	if raw, ok := e.Extra["steps"]; ok {
		var n int
		if json.Unmarshal(raw, &n) == nil {
			run.StepCount = &n
		}
	}
	if e.Usage != nil && e.Usage.USD != nil {
		v := *e.Usage.USD
		run.USD = &v
	} else if raw, ok := e.Extra["usd"]; ok {
		var v float64
		if json.Unmarshal(raw, &v) == nil {
			run.USD = &v
		}
	}
	if raw, ok := e.Extra["durationMs"]; ok {
		var v int64
		if json.Unmarshal(raw, &v) == nil {
			run.DurationMs = &v
		}
	}
	return run
}

func payloadKind(raw json.RawMessage) (string, map[string]any) {
	var p map[string]any
	if json.Unmarshal(raw, &p) != nil {
		return "", nil
	}
	k, _ := p["kind"].(string)
	return k, p
}

// FromEntries is the branch as a transcript. `running` is the host's word on
// whether a turn is still going.
func FromEntries(entries []session.Entry, running bool, todos []Todo) *Transcript {
	t := Empty()
	var reply *Message
	var branchTodos []Todo

	var latestCompact *session.Entry
	for i := range entries {
		if entries[i].Type == session.TypeCompact {
			latestCompact = &entries[i]
		}
	}
	cutAt := 0
	if latestCompact != nil {
		cutAt = latestCompact.CutAt
	}
	boundaryDrawn := cutAt == 0
	messageIndex := 0

	type pending struct {
		entry session.Entry
		at    int
	}
	var pendingRuns []pending

	currentReply := func(at int64) *Message {
		if reply != nil {
			return reply
		}
		id := fmt.Sprintf("reply-%d", at)
		if n := len(t.Messages); n > 0 && t.Messages[n-1].Role == "user" {
			id = t.Messages[n-1].ID + ":reply"
		}
		reply = &Message{ID: id, Role: "assistant", Parts: []*Part{}, Metadata: Metadata{CreatedAt: at}}
		t.Messages = append(t.Messages, reply)
		return reply
	}
	currentLength := func() int {
		if reply == nil {
			return 0
		}
		return len(reply.Parts)
	}
	attachRuns := func() {
		if len(pendingRuns) == 0 {
			return
		}
		var at int64
		if reply != nil {
			at = reply.Metadata.CreatedAt
		}
		target := currentReply(at)
		for _, pr := range pendingRuns {
			run := pr.entry
			callID := extraString(run, "toolCallId")
			idx := -1
			if callID != "" {
				idx = toolIndex(target.Parts, callID)
			}
			if idx >= 0 {
				tool := target.Parts[idx]
				tool.Subagent = subagentRun(run)
				if tool.Output == nil && tool.State != OutputError && run.Result != "" {
					tool.State, tool.Output = OutputAvailable, run.Result
				}
				continue
			}
			if callID == "" {
				callID = fmt.Sprintf("task-%d", run.TS)
			}
			input := map[string]any{"agent": run.Agent}
			if run.Prompt != "" {
				input["prompt"] = run.Prompt
			}
			p := &Part{Type: "tool-task", ToolCallID: callID, ToolName: "task", State: OutputAvailable,
				Input: input, Output: run.Result, Subagent: subagentRun(run)}
			pos := min(pr.at, len(target.Parts))
			target.Parts = append(target.Parts[:pos], append([]*Part{p}, target.Parts[pos:]...)...)
		}
		pendingRuns = nil
	}
	drawBoundary := func() {
		if boundaryDrawn {
			return
		}
		boundaryDrawn = true
		attachRuns()
		if latestCompact == nil {
			return
		}
		reply = nil
		d := map[string]any{"running": false}
		if extraString(*latestCompact, "trigger") == "idle" {
			d["reason"] = "idle"
		}
		if latestCompact.Summary != "" {
			d["summary"] = latestCompact.Summary
		}
		d["tokensBefore"] = latestCompact.TokensBefore
		if latestCompact.TokensAfter != nil {
			d["tokensAfter"] = *latestCompact.TokensAfter
		}
		if latestCompact.Handoff != "" {
			d["handoff"] = latestCompact.Handoff
		}
		id := latestCompact.ID
		if id == "" {
			id = "compact"
		}
		t.Messages = append(t.Messages, &Message{ID: id + ":boundary", Role: "system",
			Parts: []*Part{{Type: "data-compaction", Data: d}}, Metadata: Metadata{CreatedAt: latestCompact.TS}})
	}

	for position, e := range entries {
		at := e.TS
		switch e.Type {
		case session.TypeMessage:
			if messageIndex >= cutAt {
				drawBoundary()
			}
			messageIndex++
			var team *Part
			if e.Role == "user" {
				var meta map[string]any
				if raw, ok := e.Extra["team"]; ok {
					_ = json.Unmarshal(raw, &meta)
				}
				var text string
				_ = json.Unmarshal(e.Content, &text)
				team = teamPartOf(meta, text)
			}
			switch {
			case team != nil && team.Data["midTurn"] == true:
				r := currentReply(at)
				r.Parts = append(r.Parts, team)
			case e.Role == "user":
				attachRuns()
				reply = nil
				id := e.ID
				if id == "" {
					id = fmt.Sprintf("entry-%d", position)
				}
				parts := userParts(e.Content)
				if team != nil {
					parts = []*Part{team}
				}
				t.Messages = append(t.Messages, &Message{ID: id, Role: "user", Parts: parts, Metadata: Metadata{CreatedAt: at}})
			case e.Role == "assistant":
				target := currentReply(at)
				if e.Interrupted {
					target.Metadata.Interrupted = true
				}
				durations := append([]int64(nil), e.ReasoningMs...)
				var cps []contentPart
				if json.Unmarshal(e.Content, &cps) != nil {
					var s string
					_ = json.Unmarshal(e.Content, &s)
					cps = []contentPart{{Type: "text", Text: s}}
				}
				for _, p := range cps {
					switch {
					case p.Type == "text" && p.Text != "":
						if n := len(target.Parts); n > 0 && target.Parts[n-1].Type == "text" {
							target.Parts[n-1].Text = strPtr(target.Parts[n-1].text() + p.Text)
						} else {
							target.Parts = append(target.Parts, &Part{Type: "text", Text: strPtr(p.Text), State: Done})
						}
					case p.Type == "reasoning" && p.Text != "":
						part := &Part{Type: "reasoning", Text: strPtr(p.Text), State: Done}
						if len(durations) > 0 {
							d := durations[0]
							durations = durations[1:]
							part.DurationMs = &d
						}
						target.Parts = append(target.Parts, part)
					case p.Type == "tool-call" && p.ToolCallID != "":
						if toolIndex(target.Parts, p.ToolCallID) >= 0 {
							continue
						}
						name := p.ToolName
						if name == "" {
							name = "tool"
						}
						input := decodeAny(p.Input)
						if input == nil {
							input = map[string]any{}
						}
						target.Parts = append(target.Parts, &Part{Type: "tool-" + name, ToolCallID: p.ToolCallID,
							ToolName: name, State: InputAvailable, Input: input})
					}
				}
				attachRuns()
			case e.Role == "tool":
				target := currentReply(at)
				var cps []contentPart
				if json.Unmarshal(e.Content, &cps) != nil {
					continue
				}
				for _, p := range cps {
					if p.Type != "tool-result" || p.ToolCallID == "" {
						continue
					}
					idx := toolIndex(target.Parts, p.ToolCallID)
					if idx < 0 {
						continue
					}
					value, errText, failed := unwrapOutput(p.Output)
					tool := target.Parts[idx]
					if failed {
						tool.State, tool.ErrorText = OutputError, errText
					} else {
						tool.State, tool.Output = OutputAvailable, value
					}
				}
			}
		case session.TypeSubagent:
			pendingRuns = append(pendingRuns, pending{entry: e, at: currentLength()})
		case session.TypeBranchSummary:
			if e.Summary == "" {
				continue
			}
			attachRuns()
			reply = nil
			id := e.ID
			if id == "" {
				id = fmt.Sprintf("entry-%d", position)
			}
			t.Messages = append(t.Messages, &Message{ID: id, Role: "system",
				Parts:    []*Part{{Type: "data-branch-summary", Data: map[string]any{"summary": e.Summary}}},
				Metadata: Metadata{CreatedAt: at}})
		case session.TypeCustom:
			kind, payload := payloadKind(e.Payload)
			switch kind {
			case "todos":
				var p struct {
					Items []Todo `json:"items"`
				}
				if json.Unmarshal(e.Payload, &p) == nil && p.Items != nil {
					branchTodos = p.Items
				}
			case "recap":
				if text, ok := payload["text"].(string); ok && strings.TrimSpace(text) != "" {
					r := currentReply(at)
					r.Parts = append(r.Parts, &Part{Type: "data-recap", Data: map[string]any{"text": strings.TrimSpace(text)}})
				}
			}
		}
	}
	drawBoundary()
	attachRuns()

	if !running {
		for _, m := range t.Messages {
			if m.Role != "assistant" {
				continue
			}
			for _, p := range m.Parts {
				if p.IsTool() && (p.State == InputStreaming || p.State == InputAvailable) {
					p.State, p.ErrorText = OutputError, Interrupted
				}
			}
		}
	}
	t.Running = running
	switch {
	case todos != nil:
		t.Todos = todos
	case branchTodos != nil:
		t.Todos = branchTodos
	}
	return t
}

// LatestTodos is the branch's most recent checklist, nil if it never had one.
func LatestTodos(entries []session.Entry) []Todo {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Type != session.TypeCustom {
			continue
		}
		var p struct {
			Kind  string `json:"kind"`
			Items []Todo `json:"items"`
		}
		if json.Unmarshal(e.Payload, &p) == nil && p.Kind == "todos" && p.Items != nil {
			return p.Items
		}
	}
	return nil
}
