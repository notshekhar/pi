package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonschema"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/session"
)

// The conversation a compaction summarizes, as real messages (loop's
// agent/summary-messages.ts).
//
// Sending the span's own messages rather than one block of serialized text
// keeps what text flattened away — which call a result answers, who said
// what, where a turn ends — and the model reads it the way it reads every
// turn. The instructions follow as one last user message. Three things are
// still trimmed, as the text form trimmed them: reasoning is dropped, tool
// results keep their first 2,000 characters, and media is named.

// summaryMessage is one stored message, filtered for the summarizer and
// still in stored form, so its size can be measured and it can be clipped
// before it is decoded.
type summaryMessage struct {
	role  string
	text  string           // string content
	parts []map[string]any // structured content
}

// toSummaryMessages filters a span; messages left with nothing are dropped.
func toSummaryMessages(span []session.Entry) []summaryMessage {
	var out []summaryMessage
	for _, e := range span {
		if m, ok := toSummaryMessage(e.Role, e.Content); ok {
			out = append(out, m)
		}
	}
	return out
}

func toSummaryMessage(role string, raw json.RawMessage) (summaryMessage, bool) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return summaryMessage{role: role, text: s}, role != "tool" && strings.TrimSpace(s) != ""
	}
	var parts []map[string]any
	if json.Unmarshal(raw, &parts) != nil {
		return summaryMessage{}, false
	}
	var keep []map[string]any
	for _, p := range parts {
		if kept, ok := summaryPart(role, p); ok {
			keep = append(keep, kept)
		}
	}
	return summaryMessage{role: role, parts: keep}, len(keep) > 0
}

// summaryPart is what of one part the summarizer is sent.
func summaryPart(role string, p map[string]any) (map[string]any, bool) {
	typ, _ := p["type"].(string)
	switch {
	case typ == "text":
		text, _ := p["text"].(string)
		return p, text != ""
	case role == "user" && (typ == "image" || typ == "file"):
		return map[string]any{"type": "text", "text": "[" + typ + "]"}, true
	case role == "assistant" && typ == "tool-call":
		return p, true
	case role == "tool" && typ == "tool-result":
		raw, _ := json.Marshal(p["output"])
		kind := "text"
		if out, _ := p["output"].(map[string]any); out["type"] == "error-text" || out["type"] == "error-json" {
			kind = "error-text"
		}
		capped := map[string]any{}
		for k, v := range p {
			capped[k] = v
		}
		capped["output"] = map[string]any{"type": kind, "value": truncateForSummary(toolOutputText(raw), toolResultMaxChars)}
		return capped, true
	}
	return nil, false
}

// size is the message's weight on the summary budget, in characters.
func (m summaryMessage) size() int {
	if m.parts == nil {
		return len(m.text)
	}
	raw, _ := json.Marshal(m.parts)
	return len(raw)
}

// clipped is the message with every text clipped from the middle to max.
func (m summaryMessage) clipped(max int) summaryMessage {
	clip := func(s string) string { return clipMiddle(s, max/2, max/2) }
	if m.parts == nil {
		return summaryMessage{role: m.role, text: clip(m.text)}
	}
	out := summaryMessage{role: m.role}
	for _, p := range m.parts {
		c := map[string]any{}
		for k, v := range p {
			c[k] = v
		}
		if text, ok := c["text"].(string); ok {
			c["text"] = clip(text)
		}
		if o, ok := c["output"].(map[string]any); ok {
			if v, ok := o["value"].(string); ok {
				c["output"] = map[string]any{"type": o["type"], "value": clip(v)}
			}
		}
		out.parts = append(out.parts, c)
	}
	return out
}

// message decodes it into what the provider is sent.
func (m summaryMessage) message() (provider.Message, bool) {
	var raw []byte
	if m.parts == nil {
		raw, _ = json.Marshal(m.text)
	} else {
		raw, _ = json.Marshal(m.parts)
	}
	return session.DecodeContent(m.role, raw)
}

// chunkMessages packs messages into chunks of at most budget characters,
// never separating a tool call from its results: a chunk only ever starts
// on a user or assistant message. A group bigger than a whole chunk has its
// texts clipped to fit.
func chunkMessages(messages []summaryMessage, budget int) [][]summaryMessage {
	var groups [][]summaryMessage
	for _, m := range messages {
		if m.role == "tool" && len(groups) > 0 {
			groups[len(groups)-1] = append(groups[len(groups)-1], m)
		} else {
			groups = append(groups, []summaryMessage{m})
		}
	}
	var chunks [][]summaryMessage
	var current []summaryMessage
	size := 0
	for _, g := range groups {
		g = fitGroup(g, budget)
		gs := groupSize(g)
		if len(current) > 0 && size+gs > budget {
			chunks, current, size = append(chunks, current), nil, 0
		}
		current = append(current, g...)
		size += gs
	}
	if len(current) > 0 {
		chunks = append(chunks, current)
	}
	return chunks
}

func groupSize(g []summaryMessage) int {
	n := 0
	for _, m := range g {
		n += m.size()
	}
	return n
}

func fitGroup(g []summaryMessage, budget int) []summaryMessage {
	if groupSize(g) <= budget {
		return g
	}
	texts := 0
	for _, m := range g {
		texts += max(1, len(m.parts))
	}
	share := max(200, budget/(2*max(1, texts)))
	out := make([]summaryMessage, len(g))
	for i, m := range g {
		out[i] = m.clipped(share)
	}
	return out
}

// summaryRequest is one summarizer request: an opening user message
// (carrying the summary so far, when there is one), the span, then the
// instructions. The opening message also means the request never starts on
// an assistant turn, which some providers refuse.
func summaryRequest(span []summaryMessage, instructions, previousSummary string) []provider.Message {
	opening := "The conversation to summarize follows."
	if previousSummary != "" {
		opening = "<previous-summary>\n" + previousSummary + "\n</previous-summary>\n\nThe conversation to fold into it follows."
	}
	out := []provider.Message{ai.UserText(opening)}
	for _, m := range span {
		if msg, ok := m.message(); ok {
			out = append(out, msg)
		}
	}
	return append(out, ai.UserText(instructions))
}

// toolStubs declares every tool the messages call. A provider refuses a
// history with tool calls and no tools declared (Anthropic: "Requests which
// include tool_use or tool_result blocks must define tools"); the summary is
// requested with tool choice none, so a bare object schema is all a stub
// needs.
func toolStubs(messages []provider.Message) []ai.Tool {
	seen := map[string]bool{}
	var out []ai.Tool
	add := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			out = append(out, stubTool{name: name})
		}
	}
	for _, m := range messages {
		switch msg := m.(type) {
		case provider.AssistantMessage:
			for _, p := range msg.Content {
				if call, ok := p.(provider.ToolCallPart); ok {
					add(call.ToolName)
				}
			}
		case provider.ToolMessage:
			for _, p := range msg.Content {
				if res, ok := p.(provider.ToolResultPart); ok {
					add(res.ToolName)
				}
			}
		}
	}
	return out
}

// stubTool is a declared, never-run tool.
type stubTool struct{ name string }

func (t stubTool) Name() string        { return t.name }
func (t stubTool) Description() string { return "" }
func (t stubTool) InputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: jsonschema.Object}
}

func (t stubTool) Execute(context.Context, json.RawMessage) (ai.ToolResult, error) {
	return ai.ToolError("tools are not available while summarizing"), nil
}
