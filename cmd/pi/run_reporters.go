package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/core/agent"
)

// How `pi run` reports itself — loop's print/reporters.ts.
//
// text is for a person: the reply on stdout, activity on stderr. json and
// stream-json are for scripts, and in both stdout carries JSON and nothing
// else: model text, hook output and escape sequences become fields or
// events rather than being written through, so a hook printing a colour
// code cannot corrupt the parse.

// runContext is what a run knows before its first token.
type runContext struct {
	sessionID, model, cwd string
}

// runResult is what it knows once it is over.
type runResult struct {
	cost     agent.SessionCost
	duration time.Duration
}

// runReporter owns every write to stdout and stderr for one run.
type runReporter interface {
	// emit is the turn's event sink; it may be called from several goroutines.
	emit(event string, data any)
	begin(runContext)
	end(runResult)
	errored() bool
}

func newRunReporter(format string, stdout, stderr io.Writer) runReporter {
	base := &reportState{stdout: stdout, stderr: stderr}
	switch format {
	case "json":
		return &jsonReporter{base}
	case "stream-json":
		return &streamJSONReporter{base}
	}
	return &textReporter{base}
}

// reportState is the bookkeeping every format shares: the reply text, the
// step count, and the errors that make the exit code nonzero.
type reportState struct {
	mu             sync.Mutex
	stdout, stderr io.Writer
	text           strings.Builder
	steps          int
	errors         []any
	ctx            runContext
}

// track records an event; callers hold mu.
func (s *reportState) track(event string, data any) {
	switch event {
	case "text-delta":
		if t, ok := data.(string); ok {
			s.text.WriteString(t)
		}
	case "step-usage":
		s.steps++
	case "error":
		s.errors = append(s.errors, data)
	}
}

func (s *reportState) errored() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.errors) > 0
}

// runUsage is the token half of the result object.
type runUsage struct {
	InputTokens       int64 `json:"input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
}

// runResultObject is the final JSON line, in loop's key order. errors is
// present only when something went wrong, so its presence always means so.
type runResultObject struct {
	Type       string   `json:"type"`
	IsError    bool     `json:"is_error"`
	SessionID  string   `json:"session_id,omitempty"`
	Model      string   `json:"model,omitempty"`
	CWD        string   `json:"cwd,omitempty"`
	Result     string   `json:"result"`
	Steps      int      `json:"steps"`
	DurationMs int64    `json:"duration_ms"`
	USD        float64  `json:"usd"`
	Usage      runUsage `json:"usage"`
	Errors     []any    `json:"errors,omitempty"`
}

func (s *reportState) resultObject(r runResult) runResultObject {
	return runResultObject{
		Type: "result", IsError: len(s.errors) > 0,
		SessionID: s.ctx.sessionID, Model: s.ctx.model, CWD: s.ctx.cwd,
		Result: s.text.String(), Steps: s.steps, DurationMs: r.duration.Milliseconds(), USD: r.cost.USD,
		Usage:  runUsage{r.cost.InputTokens, r.cost.OutputTokens, r.cost.CachedInputTokens},
		Errors: s.errors,
	}
}

// writeJSONLine writes v as one line. HTML escaping is off: JSON.stringify
// leaves < and > alone, and a script comparing text must see the same bytes.
func writeJSONLine(w io.Writer, v any) {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		_ = enc.Encode(streamEvent{Type: "error", Data: err.Error()})
	}
}

// textReporter is the human stream, plus the session id up front so a run
// that dies part way still says what to pass to --session.
type textReporter struct{ *reportState }

func (r *textReporter) emit(event string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.track(event, data)
	switch event {
	case "text-delta", "hook-terminal-sequence":
		fmt.Fprint(r.stdout, data)
	case "tool-call":
		if c, ok := data.(agent.ToolCallEvent); ok {
			fmt.Fprintf(r.stderr, "\n[tool:%s] %s\n", c.ToolName, compactJSON(c.Input))
		}
	case "hook-message":
		fmt.Fprintf(r.stderr, "\n[hook] %v\n", data)
	case "stream-retry":
		if e, ok := data.(agent.StreamRetryEvent); ok {
			fmt.Fprintf(r.stderr, "\n[retry %d/%d] %s\n", e.Attempt, e.Max, e.Reason)
		}
	case "error":
		fmt.Fprintf(r.stderr, "\n[error] %s\n", errorText(data))
	case "finish":
		fmt.Fprintln(r.stdout)
	}
}

func (r *textReporter) begin(c runContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = c
	fmt.Fprintf(r.stderr, "[session] %s\n", c.sessionID)
}

func (r *textReporter) end(res runResult) {
	fmt.Fprintf(r.stderr, "\n%s\n", formatRunCost(res.cost))
}

// jsonReporter prints one result object when the run is over. Hook messages
// are the one thing a person still wants live, and stderr is free for them.
type jsonReporter struct{ *reportState }

func (r *jsonReporter) emit(event string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.track(event, data)
	if event == "hook-message" {
		fmt.Fprintf(r.stderr, "[hook] %v\n", data)
	}
}

func (r *jsonReporter) begin(c runContext) {
	r.mu.Lock()
	r.ctx = c
	r.mu.Unlock()
}

func (r *jsonReporter) end(res runResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	writeJSONLine(r.stdout, r.resultObject(res))
}

// streamJSONReporter prints every event as it happens, one JSON object per
// line, between an init line and the result object.
type streamJSONReporter struct{ *reportState }

// streamEvent is one NDJSON line; a nil payload is loop's void event.
type streamEvent struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

func (r *streamJSONReporter) emit(event string, data any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.track(event, data)
	writeJSONLine(r.stdout, streamEvent{Type: event, Data: data})
}

// streamInit is the first line: the session a follow-up resumes.
type streamInit struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	CWD       string `json:"cwd"`
}

func (r *streamJSONReporter) begin(c runContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ctx = c
	writeJSONLine(r.stdout, streamInit{"init", c.sessionID, c.model, c.cwd})
}

func (r *streamJSONReporter) end(res runResult) {
	r.mu.Lock()
	defer r.mu.Unlock()
	writeJSONLine(r.stdout, r.resultObject(res))
}

// compactJSON is a tool input as one line, the way JSON.stringify prints it.
func compactJSON(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// errorText is an error payload as loop's String(err) shows it:
// "Name: message" for an error object, the text itself for a string.
func errorText(data any) string {
	if m, ok := data.(map[string]any); ok {
		if msg, ok := m["message"].(string); ok {
			name, _ := m["name"].(string)
			if name == "" {
				return msg
			}
			return name + ": " + msg
		}
	}
	return fmt.Sprint(data)
}

// formatRunCost is the line the text format ends on; "~" marks an estimate.
func formatRunCost(c agent.SessionCost) string {
	prefix := ""
	if c.Estimated {
		prefix = "~"
	}
	return fmt.Sprintf("%s$%.4f · in:%d out:%d cache:%d", prefix, c.USD, c.InputTokens, c.OutputTokens, c.CachedInputTokens)
}
