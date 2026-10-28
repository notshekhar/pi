// Package transcript is loop's transcript (packages/core/src/transcript):
// the conversation as every client draws it — the AI SDK's UIMessage shape,
// a user message and one assistant message per turn whose parts are in the
// order they happened. Built from saved entries (history.go) and from live
// turn events (reduce.go) by one set of rules, so a reply reads the same
// while it streams and after it is saved. The JSON is the wire format the
// desktop and mobile apps render.
package transcript

import "strings"

// Part states and tool states, in the AI SDK's words.
const (
	Streaming = "streaming"
	Done      = "done"

	InputStreaming  = "input-streaming"
	InputAvailable  = "input-available"
	OutputAvailable = "output-available"
	OutputError     = "output-error"
)

// Interrupted is what a call cut off by the end of its turn says.
const Interrupted = "Interrupted"

// MaxSubagentSteps caps a subagent's log; the run counts what it dropped.
const MaxSubagentSteps = 60

// SubagentStep is one step of a subagent's run.
type SubagentStep struct {
	Type  string `json:"type"` // text | reasoning | tool
	Text  string `json:"text,omitempty"`
	Name  string `json:"name,omitempty"`
	Input any    `json:"input,omitempty"`
}

// SubagentRun is the run a `task` call started.
type SubagentRun struct {
	Agent      string         `json:"agent"`
	Steps      []SubagentStep `json:"steps"`
	StepCount  *int           `json:"stepCount,omitempty"`
	USD        *float64       `json:"usd,omitempty"`
	DurationMs *int64         `json:"durationMs,omitempty"`
	Current    string         `json:"current,omitempty"`
	Dropped    int            `json:"dropped,omitempty"`
	Finished   bool           `json:"finished"`
}

// Part is one transcript part. Type decides which fields are meaningful:
// text/reasoning (Text, State, DurationMs, StartedAt), tool-<name> (ToolCallID,
// ToolName, State, Input, InputText, InputRewritten, Output, ErrorText,
// Subagent), file (URL, MediaType), and data-* (Data).
type Part struct {
	Type string `json:"type"`

	Text       *string `json:"text,omitempty"`
	State      string  `json:"state,omitempty"`
	DurationMs *int64  `json:"durationMs,omitempty"`
	StartedAt  *int64  `json:"startedAt,omitempty"`

	ToolCallID     string       `json:"toolCallId,omitempty"`
	ToolName       string       `json:"toolName,omitempty"`
	Input          any          `json:"input,omitempty"`
	InputText      *string      `json:"inputText,omitempty"`
	InputRewritten bool         `json:"inputRewritten,omitempty"`
	Output         any          `json:"output,omitempty"`
	ErrorText      string       `json:"errorText,omitempty"`
	Subagent       *SubagentRun `json:"subagent,omitempty"`

	URL       string `json:"url,omitempty"`
	MediaType string `json:"mediaType,omitempty"`

	Data map[string]any `json:"data,omitempty"`
}

// IsTool reports a tool part.
func (p *Part) IsTool() bool { return strings.HasPrefix(p.Type, "tool-") }

func (p *Part) text() string {
	if p.Text == nil {
		return ""
	}
	return *p.Text
}

func strPtr(s string) *string { return &s }

// Metadata is a message's metadata.
type Metadata struct {
	CreatedAt   int64 `json:"createdAt"`
	Interrupted bool  `json:"interrupted,omitempty"`
}

// Message is one transcript message: user, assistant, or system (markers
// between turns — a compaction boundary, a branch summary).
type Message struct {
	ID       string   `json:"id"`
	Role     string   `json:"role"`
	Parts    []*Part  `json:"parts"`
	Metadata Metadata `json:"metadata"`
}

// Todo is a checklist item.
type Todo struct {
	Content    string `json:"content"`
	Status     string `json:"status"`
	ActiveForm string `json:"activeForm,omitempty"`
}

// Transcript is a session's transcript at one point in its event stream.
type Transcript struct {
	Messages []*Message `json:"messages"`
	Running  bool       `json:"running"`
	Todos    []Todo     `json:"todos"`
}

// Event is one live event (`session.event`'s part).
type Event struct {
	Type string
	Data any
}

// Empty is a transcript with nothing in it.
func Empty() *Transcript {
	return &Transcript{Messages: []*Message{}, Todos: []Todo{}}
}
