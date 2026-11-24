// Package chatimport reads another agent's chats — Claude Code, Codex,
// Cursor — into loop-shaped session entries, so they can be previewed and
// saved as a session of our own. It is loop's chat-import extension
// (core/src/extensions/builtin/chat-import), file for file.
//
// Every source reads its own format and feeds the same five events to a
// Builder, so the rules that make the result a VALID session live in one
// place. Reading never writes: transcripts are read as files and databases
// through db.OpenForeign.
package chatimport

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/core/session"
)

// maxResultChars caps one imported tool result — our own tools cap their
// output the same way.
const maxResultChars = 50_000

// part is one AI SDK content part, in the shape loop stores.
type part struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ToolCallID string `json:"toolCallId,omitempty"`
	ToolName   string `json:"toolName,omitempty"`
	Input      any    `json:"input,omitempty"`
	Output     *struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"output,omitempty"`
}

type openCall struct{ id, name string }

// Builder assembles a transcript one event at a time.
//
//   - an assistant message ends where its tool calls are answered, and the
//     answers follow it as one `tool` message — the shape replay draws and the
//     shape a provider accepts when the chat is continued;
//   - every call gets exactly one result: a lost one gets a placeholder, and a
//     result for a call never seen is dropped, because a provider rejects
//     either.
type Builder struct {
	entries   []session.Entry
	assistant []part
	results   []part
	open      []openCall
	lastTS    int64
}

func (b *Builder) User(text string, ts int64) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	b.closeStep()
	b.push("user", text, ts)
}

func (b *Builder) Text(text string, ts int64) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.beforeAssistantPart(ts)
	// Streamed sources split one answer across records; one block reads as one answer.
	if n := len(b.assistant); n > 0 && b.assistant[n-1].Type == "text" {
		b.assistant[n-1].Text += text
		return
	}
	b.assistant = append(b.assistant, part{Type: "text", Text: text})
}

func (b *Builder) Reasoning(text string, ts int64) {
	if strings.TrimSpace(text) == "" {
		return
	}
	b.beforeAssistantPart(ts)
	b.assistant = append(b.assistant, part{Type: "reasoning", Text: text})
}

func (b *Builder) ToolCall(id, name string, input any, ts int64) {
	b.beforeAssistantPart(ts)
	if input == nil {
		input = map[string]any{}
	}
	b.assistant = append(b.assistant, part{Type: "tool-call", ToolCallID: id, ToolName: name, Input: input})
	b.open = append(b.open, openCall{id, name})
}

func (b *Builder) ToolResult(id, output string, isError bool, ts int64) {
	at := -1
	for i, c := range b.open {
		if c.id == id {
			at = i
			break
		}
	}
	if at < 0 {
		return
	}
	call := b.open[at]
	b.open = append(b.open[:at], b.open[at+1:]...)
	b.flushAssistant(ts)
	b.results = append(b.results, resultPart(call, output, isError))
}

// Build is the finished transcript.
func (b *Builder) Build() []session.Entry {
	b.closeStep()
	return b.entries
}

// beforeAssistantPart: new assistant content after results starts a new step.
func (b *Builder) beforeAssistantPart(ts int64) {
	if len(b.results) > 0 {
		b.closeStep()
	}
	b.lastTS = max(b.lastTS, ts)
}

// closeStep ends the step: its assistant message, then every result —
// placeholders for the lost ones.
func (b *Builder) closeStep() {
	b.flushAssistant(0)
	for _, c := range b.open {
		b.results = append(b.results, resultPart(c, "(no result was recorded for this call)", true))
	}
	b.open = nil
	if len(b.results) == 0 {
		return
	}
	b.push("tool", b.results, 0)
	b.results = nil
}

func (b *Builder) flushAssistant(ts int64) {
	if len(b.assistant) == 0 {
		return
	}
	b.push("assistant", b.assistant, ts)
	b.assistant = nil
}

func (b *Builder) push(role string, content any, ts int64) {
	raw, err := json.Marshal(content, jsonopt.Write)
	if err != nil {
		return
	}
	b.entries = append(b.entries, session.Entry{
		Type: session.TypeMessage, Role: role, Content: jsontext.Value(raw), TS: b.stamp(ts),
	})
}

// stamp keeps timestamps from going backwards where a source omits them.
func (b *Builder) stamp(ts int64) int64 {
	b.lastTS = max(b.lastTS, ts)
	if b.lastTS == 0 {
		return time.Now().UnixMilli()
	}
	return b.lastTS
}

func resultPart(c openCall, output string, isError bool) part {
	if len(output) > maxResultChars {
		output = fmt.Sprintf("%s\n… (%d more characters not imported)", output[:maxResultChars], len(output)-maxResultChars)
	}
	kind := "text"
	if isError {
		kind = "error-text"
	}
	p := part{Type: "tool-result", ToolCallID: c.id, ToolName: c.name}
	p.Output = &struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}{kind, output}
	return p
}

// ParseTime reads an ISO timestamp or epoch ms; 0 when missing or junk.
func ParseTime(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, t); err == nil {
			return parsed.UnixMilli()
		}
	}
	return 0
}
