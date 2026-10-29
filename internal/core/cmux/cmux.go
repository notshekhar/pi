// Package cmux mirrors this pane's agent activity into cmux's Feed and lets
// cmux answer the prompts pi would otherwise only ask in the TUI
// (https://cmux.com) — loop's interactive/cmux-reporter.ts.
//
// cmux is a terminal that watches the agents running inside it; without a
// report it sees pi as an anonymous shell process, so the sidebar shows
// nothing and its approval cards never appear.
//
// The bridge is cmux's `feed.push` verb over its unix socket, and the frames
// it carries are Claude Code–shaped hook payloads — the exact shape pi's own
// hooks already emit. So this is mostly a pipe: hooks.Watch reports every
// dispatched lifecycle event (configured hook commands or not), and each one
// goes out as a feed event under `_source: "pi"`.
//
// Two request kinds are more than telemetry. An event pushed with a request
// id PARKS the connection: cmux shows an actionable card (permission /
// question / plan), and answering it in the sidebar sends the decision back
// down that same connection. Approvals then race: whichever of cmux and the
// TUI answers first wins. Measured contract, per card kind:
//
//	→ {method:"feed.push", params:{event, wait_timeout_seconds}}
//	← {ok:true, result:{status:"acknowledged"}}                     // telemetry
//	← {ok:true, result:{status:"resolved", decision:{kind:"permission", mode}}}
//	← {ok:true, result:{status:"resolved", decision:{kind:"question", selections}}}
//	← {ok:true, result:{status:"resolved", decision:{kind:"exit_plan", mode, feedback?}}}
//
// The Feed is only half of it. What you actually SEE in cmux — the status
// chip in the sidebar and the notification when a turn finishes — is driven
// by line-delimited V1 commands on the same socket (`set_status`,
// `notify_target`), each prefixed with the capability cmux exports into its
// panes. The states, icons and colours are the ones cmux's built-in agent
// hooks use, so a pi pane reads identically to a Claude Code pane.
//
// Hard-gated on the env cmux injects into every pane (CMUX_SURFACE_ID plus a
// socket path): outside cmux nothing subscribes and no socket is ever opened.
// Telemetry is fire-and-forget over short-lived connections with a hard
// timeout, on an ordered queue — a dead or restarted cmux must never add
// latency to a turn.
package cmux

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/notshekhar/pi/internal/core/hooks"
	"github.com/notshekhar/pi/internal/core/status"
)

// source is how pi names itself to cmux.
const source = "pi"

const (
	// decisionWait: cmux parks a card for at most ~120s, then answers nothing
	// and expects the agent to fall back to its own prompt — which for pi is
	// the TUI menu that was showing the whole time anyway.
	decisionWait = 120 * time.Second
	// ackTimeout: telemetry is not worth waiting on, ack or give up.
	ackTimeout = 500 * time.Millisecond
	// maxFieldBytes caps any single serialized field — cmux stores what we
	// send.
	maxFieldBytes = 8_000
	// maxQueued: cmux being slow must not become pi's memory leak.
	maxQueued = 64
	// maxPending bounds events held back until a session exists.
	maxPending = 32
	// capabilityPrefix authenticates a V1 line.
	capabilityPrefix = "_cmux_capability_v1"
	// statusKey is the key our status entry lives under in the sidebar.
	statusKey = source
)

type statusStyle struct {
	icon, color string
	priority    int
}

// statusStyles: same SF Symbols and colours as cmux's own agent hooks.
// `priority` floats a waiting agent to the top, which is what makes "this
// one needs you" findable across many panes.
var statusStyles = map[status.State]statusStyle{
	status.Working: {icon: "bolt.fill", color: "#4C8DFF"},
	status.Blocked: {icon: "bell.fill", color: "#4C8DFF", priority: 100},
	status.Idle:    {icon: "pause.circle.fill", color: "#8E8E93"},
}

// Decision is what cmux answered a card with.
type Decision struct {
	// Kind is "permission", "question" or "exit_plan".
	Kind string `json:"kind"`
	// Mode is a permission's once | always | all | bypass | deny, or a
	// plan's manual | autoAccept | bypassPermissions | deny | ultraplan.
	Mode       string   `json:"mode,omitempty"`
	Feedback   string   `json:"feedback,omitempty"`
	Selections []string `json:"selections,omitempty"`
}

// Session is the live session, read at send time.
type Session struct {
	ID   string
	Path string
}

// ApprovalRequest is one approval, in cmux's terms rather than the TUI's.
type ApprovalRequest struct {
	// Kind "exit-plan" becomes an ExitPlanMode card; "bash", "path" and
	// "plan" a permission card.
	Kind string
	// ToolName is the card's title ("bash", "file", …).
	ToolName string
	// Body is the command / path / plan the user is asked about.
	Body string
	// Title is an extra context line.
	Title string
	// Patterns are what an "always" decision would remember.
	Patterns []string
}

// Question is one of the ask tool's questions.
type Question struct {
	Header      string
	Question    string
	MultiSelect bool
	Options     []Option
}

// Option is one answer to a Question.
type Option struct{ Label, Description string }

// Options configure the reporter.
type Options struct {
	// Session is read lazily — called at send time, never cached.
	Session func() (Session, bool)
	// CWD is the pane's working directory, read at send time.
	CWD func() string
	// Disabled is the `cmux` setting turned off.
	Disabled bool
	// Env defaults to os.Getenv.
	Env func(string) string
	// ConnectTimeout and DecisionWait are test seams.
	ConnectTimeout time.Duration
	DecisionWait   time.Duration
}

// Reporter is the live subscription. The zero value is inert, which is what
// Attach returns outside a cmux pane.
type Reporter struct {
	active         bool
	socket         string
	surface        string
	workspace      string
	capability     string
	connectTimeout time.Duration
	wait           time.Duration
	opts           Options

	seq      atomic.Int64
	released atomic.Bool

	mu              sync.Mutex
	queue           []func()
	draining        bool
	pending         []hooks.Context
	lastUserMessage string
	unwatch         func()
}

// current is the pane-wide reporter, for the seams that cannot be handed one.
var current atomic.Pointer[Reporter]

// Set installs the pane-wide reporter.
func Set(r *Reporter) { current.Store(r) }

// Current is the pane-wide reporter; inert when none is installed.
func Current() *Reporter {
	if r := current.Load(); r != nil {
		return r
	}
	return &Reporter{}
}

// Active reports whether this is a cmux pane with reporting enabled.
func (r *Reporter) Active() bool { return r != nil && r.active }

// Attach starts reporting when this is a cmux pane, and returns an inert
// reporter when it is not.
func Attach(bus *status.Bus, o Options) *Reporter {
	env := o.Env
	if env == nil {
		env = os.Getenv
	}
	surface := env("CMUX_SURFACE_ID")
	socket := env("CMUX_SOCKET_PATH")
	if socket == "" && surface != "" {
		if home, err := os.UserHomeDir(); err == nil {
			socket = filepath.Join(home, ".config", "cmux", "cmux.sock")
		}
	}
	// CMUX_SURFACE_ID is the pane identity cmux injects; without it we are
	// not in a cmux terminal, whatever else the environment inherited.
	if o.Disabled || surface == "" || socket == "" {
		return &Reporter{}
	}
	if o.Session == nil {
		o.Session = func() (Session, bool) { return Session{}, false }
	}
	if o.CWD == nil {
		o.CWD = func() string { d, _ := os.Getwd(); return d }
	}
	r := &Reporter{
		active:         true,
		socket:         socket,
		surface:        surface,
		workspace:      env("CMUX_WORKSPACE_ID"),
		capability:     env("CMUX_SOCKET_CAPABILITY"),
		connectTimeout: o.ConnectTimeout,
		wait:           o.DecisionWait,
		opts:           o,
	}
	if r.connectTimeout == 0 {
		r.connectTimeout = 2 * time.Second
	}
	if r.wait == 0 {
		r.wait = decisionWait
	}
	r.unwatch = hooks.Watch(r.onHookEvent)
	// The visible half: the sidebar chip follows the working / blocked /
	// idle bus, and a prompt the agent is stuck on also raises a
	// notification.
	if bus != nil {
		bus.On(func(e status.Event) {
			r.reportStatus(e)
			if e.State == status.Blocked {
				label := e.Label
				if label == "" {
					label = "waiting for you"
				}
				r.notify("needs-permission", label, true)
			}
		})
		// Announce presence right away: the pane shows pi from launch
		// instead of staying blank until the first transition.
		r.reportStatus(bus.Current())
	}
	return r
}

// bounded keeps a field JSON-safe and small enough that a 3MB tool result
// cannot ride the socket.
func bounded(value any, maxBytes int) any {
	if value == nil {
		return nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return nil // unserializable — the field is optional
	}
	if len(b) <= maxBytes {
		return value
	}
	if s, ok := value.(string); ok {
		return truncateUTF16(s, maxBytes) + "… [truncated by " + source + "]"
	}
	return map[string]any{"truncated": true, "bytes": len(b), "preview": truncateUTF16(string(b), 1_000) + "…"}
}

// truncateUTF16 is JS's slice(0, n), close enough: n characters.
func truncateUTF16(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// push sends one feed event, one request per connection: cmux answers a
// parked card on the socket it was pushed over, so a decision cannot outlive
// its connection.
func (r *Reporter) push(ctx context.Context, event map[string]any, requestID string, wait time.Duration) *Decision {
	id := requestID
	if id == "" {
		id = fmt.Sprintf("t%d", r.seq.Add(1))
	}
	waitSeconds := 0
	if requestID != "" {
		waitSeconds = int(wait / time.Second)
	}
	frame, err := json.Marshal(map[string]any{
		"id":     source + "-" + id,
		"method": "feed.push",
		"params": map[string]any{"event": event, "wait_timeout_seconds": waitSeconds},
	})
	if err != nil {
		return nil
	}
	// Timeouts are the contract, not a safety net: cmux answers a card it has
	// no decision for by simply never answering.
	deadline := ackTimeout
	if requestID != "" {
		deadline = wait + r.connectTimeout
	}
	conn, err := net.DialTimeout("unix", r.socket, r.connectTimeout)
	if err != nil {
		return nil
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(deadline))
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, err := conn.Write(append(frame, '\n')); err != nil {
		return nil
	}
	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg struct {
			Result *struct {
				Status   string    `json:"status"`
				Decision *Decision `json:"decision"`
			} `json:"result"`
		}
		if json.Unmarshal([]byte(line), &msg) != nil {
			continue // a frame we don't understand is not a reason to hang
		}
		if msg.Result != nil && msg.Result.Status == "resolved" {
			return msg.Result.Decision
		}
		// Anything else ends a telemetry push (that ack is all it was
		// waiting for) but not a parked one: a card cmux acks first and
		// resolves later must still be answerable.
		if requestID == "" {
			return nil
		}
	}
	return nil
}

// v1 sends one line-delimited V1 command carrying the pane capability.
// Fire-and-forget: the reply is "OK" or an "ERROR: …" nobody can act on
// mid-turn.
func (r *Reporter) v1(command string) {
	line := command
	if r.capability != "" {
		line = capabilityPrefix + " " + r.capability + " " + command
	}
	go func() {
		conn, err := net.DialTimeout("unix", r.socket, r.connectTimeout)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(r.connectTimeout))
		if _, err := conn.Write([]byte(line + "\n")); err != nil {
			return
		}
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
	}()
}

// paneTarget says which pane a command is about. cmux calls a workspace a
// tab here.
func (r *Reporter) paneTarget() string {
	t := "--tab=" + r.workspace
	if r.surface != "" {
		t += " --panel=" + r.surface
	}
	return t
}

var wireSpaces = regexp.MustCompile(`\s+`)

// wireText is safe on both wires: the V1 socket is line-delimited and
// notify's payload is |-delimited, so a newline or a pipe would truncate the
// command. `--` would be read as the start of a flag.
func wireText(value string, max int) string {
	flat := wireSpaces.ReplaceAllString(value, " ")
	flat = strings.ReplaceAll(flat, "|", "/")
	flat = strings.ReplaceAll(flat, "--", "—")
	flat = strings.TrimSpace(flat)
	if r := []rune(flat); len(r) > max {
		return string(r[:max-1]) + "…"
	}
	return flat
}

// reportStatus sets the chip in the pane's sidebar: what pi is doing now.
func (r *Reporter) reportStatus(e status.Event) {
	if r.released.Load() {
		return
	}
	style := statusStyles[e.State]
	label := "Idle"
	switch e.State {
	case status.Blocked:
		l := e.Label
		if l == "" {
			l = "needs input"
		}
		label = wireText(l, 60)
	case status.Working:
		label = "Working"
	}
	priority := ""
	if style.priority != 0 {
		priority = fmt.Sprintf(" --priority=%d", style.priority)
	}
	r.v1(fmt.Sprintf("set_status %s %s --icon=%s --color=%s%s %s", statusKey, label, style.icon, style.color, priority, r.paneTarget()))
}

// notify raises a native cmux notification, in the same categories its own
// agents use, so the user's notification switches govern pi's too.
func (r *Reporter) notify(category, body string, pending bool) {
	if r.released.Load() || r.workspace == "" || r.surface == "" {
		return
	}
	cwd := r.opts.CWD()
	subtitle := wireText(cwd[strings.LastIndex(cwd, "/")+1:], 40)
	// EXACTLY `c=<category>;p=<0|1>`: cmux only treats the 4th field as
	// metadata when it parses as that whole grammar, and folds anything else
	// back into the body.
	p := 0
	if pending {
		p = 1
	}
	meta := fmt.Sprintf("c=%s;p=%d", category, p)
	r.v1(fmt.Sprintf("notify_target %s %s %s|%s|%s|%s", r.workspace, r.surface, source, subtitle, wireText(body, 240), meta))
}

// enqueue runs telemetry one connection at a time, in order. Bounded, so a
// cmux that stops accepting connections costs a few dropped rows in its own
// sidebar rather than growing this process.
func (r *Reporter) enqueue(job func()) {
	r.mu.Lock()
	if len(r.queue) >= maxQueued {
		r.queue = r.queue[1:]
	}
	r.queue = append(r.queue, job)
	if r.draining {
		r.mu.Unlock()
		return
	}
	r.draining = true
	r.mu.Unlock()
	go r.drain()
}

func (r *Reporter) drain() {
	for {
		r.mu.Lock()
		if len(r.queue) == 0 {
			r.draining = false
			r.mu.Unlock()
			return
		}
		job := r.queue[0]
		r.queue = r.queue[1:]
		r.mu.Unlock()
		job()
	}
}

// sessionID groups a pane's rows; cmux expects the source's own prefix on it
// (claude-<uuid>, opencode-<id>), so ours reads pi-<id>.
func (r *Reporter) sessionID() (string, bool) {
	s, ok := r.opts.Session()
	if !ok || s.ID == "" {
		return "", false
	}
	return source + "-" + s.ID, true
}

func (r *Reporter) baseEvent() map[string]any {
	id, ok := r.sessionID()
	if !ok {
		id = source + "-" + r.surface
	}
	event := map[string]any{
		"session_id": id,
		"_source":    source,
		"_ppid":      os.Getpid(),
		"cwd":        r.opts.CWD(),
	}
	if r.workspace != "" {
		event["workspace_id"] = r.workspace
	}
	if r.surface != "" {
		event["surface_id"] = r.surface
	}
	r.mu.Lock()
	last := r.lastUserMessage
	r.mu.Unlock()
	if last != "" {
		event["context"] = map[string]any{"lastUserMessage": last}
	}
	return event
}

// forward sends one of pi's own hook events, verbatim.
func (r *Reporter) forward(hc hooks.Context) {
	event := r.baseEvent()
	event["hook_event_name"] = string(hc.Event)
	// cwd travels per event: /cd moves the agent mid-session.
	if hc.CWD != "" {
		event["cwd"] = hc.CWD
	}
	switch hc.Event {
	case hooks.UserPromptSubmit:
		last := truncateUTF16(hc.Prompt, 1_000)
		r.mu.Lock()
		r.lastUserMessage = last
		r.mu.Unlock()
		event["context"] = map[string]any{"lastUserMessage": last}
		event["tool_input"] = map[string]any{"prompt": bounded(hc.Prompt, maxFieldBytes)}
	case hooks.PreToolUse, hooks.PostToolUse:
		event["tool_name"] = hc.ToolName
		if in := bounded(hc.ToolInput, maxFieldBytes); in != nil {
			event["tool_input"] = in
		}
		if hc.Event == hooks.PostToolUse {
			if out := bounded(hc.ToolOut, maxFieldBytes); out != nil {
				event["tool_result"] = out
			}
		}
	case hooks.Notification:
		if m := bounded(hc.Message, 1_000); m != nil && hc.Message != "" {
			event["message"] = m
		}
	case hooks.Stop:
		// What the agent said: cmux titles its turn-complete notification
		// with it.
		said := hc.LastAssistantMessage
		if said != "" {
			event["last_assistant_message"] = bounded(said, 2_000)
		}
		event["stop_hook_active"] = hc.StopHookActive
		// The turn ended — the notification the user is waiting for when
		// they have looked away. A stop-hook continuation is not the end of
		// anything, so it stays quiet.
		if !hc.StopHookActive {
			body := said
			if body == "" {
				body = "Turn finished"
			}
			r.notify("turn-complete", body, false)
		}
	case hooks.SessionEnd:
		if hc.Reason != "" {
			event["reason"] = hc.Reason
		}
	}
	r.enqueue(func() { r.push(context.Background(), event, "", 0) })

	// The todo tool IS cmux's TodoWrite: the same list, under the name its
	// Feed already renders as a checklist. Sent alongside the tool row — the
	// row is the activity, this is state.
	if hc.Event == hooks.PostToolUse && hc.ToolName == "todo" {
		if in, ok := hc.ToolInput.(map[string]any); ok {
			if todos, ok := in["todos"].([]any); ok {
				te := r.baseEvent()
				te["hook_event_name"] = "TodoWrite"
				te["tool_name"] = "TodoWrite"
				te["tool_input"] = bounded(map[string]any{"todos": todos}, maxFieldBytes)
				r.enqueue(func() { r.push(context.Background(), te, "", 0) })
			}
		}
	}
}

// onHookEvent files an event under the live session.
//
// Events from before this launch had a session wait: the session is created
// on the first turn, but SessionStart fires at boot — reporting it against
// the pane id would open a second, near-empty workstream in cmux every
// launch. Held events are replayed the moment a session exists.
func (r *Reporter) onHookEvent(hc hooks.Context) {
	if r.released.Load() {
		return
	}
	if _, ok := r.sessionID(); !ok {
		r.mu.Lock()
		if len(r.pending) < maxPending {
			r.pending = append(r.pending, hc)
		}
		r.mu.Unlock()
		return
	}
	r.mu.Lock()
	held := r.pending
	r.pending = nil
	r.mu.Unlock()
	for _, p := range held {
		r.forward(p)
	}
	r.forward(hc)
}

func (r *Reporter) requestDecision(ctx context.Context, event map[string]any, kind string) *Decision {
	if r.released.Load() || ctx.Err() != nil {
		return nil
	}
	requestID := fmt.Sprintf("%s-%s-%d-%d", source, kind, time.Now().UnixMilli(), r.seq.Add(1))
	// The request id is what cmux keys the parked card by — and the field
	// name is a leftover from its first integration, not a typo.
	event["_opencode_request_id"] = requestID
	return r.push(ctx, event, requestID, r.wait)
}

// RequestApproval asks cmux to decide an approval. nil when it doesn't (not
// a cmux pane, nobody answered, ctx cancelled).
func (r *Reporter) RequestApproval(ctx context.Context, req ApprovalRequest) *Decision {
	if !r.Active() {
		return nil
	}
	event := r.baseEvent()
	if req.Kind == "exit-plan" {
		event["hook_event_name"] = "ExitPlanMode"
		event["tool_name"] = "exit_plan_mode"
		event["tool_input"] = map[string]any{"plan": bounded(req.Body, 32_000)}
		c, _ := event["context"].(map[string]any)
		merged := map[string]any{"permissionMode": "plan"}
		for k, v := range c {
			merged[k] = v
		}
		merged["permissionMode"] = "plan"
		event["context"] = merged
		if d := r.requestDecision(ctx, event, "plan"); d != nil && d.Kind == "exit_plan" {
			return d
		}
		return nil
	}
	patterns := req.Patterns
	if patterns == nil {
		patterns = []string{}
	}
	input := map[string]any{"command": req.Body, "permission": req.Kind, "patterns": patterns}
	if req.Title != "" {
		input["title"] = req.Title
	}
	event["hook_event_name"] = "PermissionRequest"
	event["tool_name"] = req.ToolName
	event["tool_input"] = bounded(input, maxFieldBytes)
	if d := r.requestDecision(ctx, event, "permission"); d != nil && d.Kind == "permission" {
		return d
	}
	return nil
}

// RequestQuestions asks cmux to answer the ask tool's questions; nil when it
// doesn't.
func (r *Reporter) RequestQuestions(ctx context.Context, questions []Question) []string {
	if !r.Active() {
		return nil
	}
	qs := make([]map[string]any, len(questions))
	for i, q := range questions {
		opts := make([]map[string]any, len(q.Options))
		for oi, o := range q.Options {
			opt := map[string]any{"id": fmt.Sprintf("opt%d", oi), "label": o.Label}
			if o.Description != "" {
				opt["description"] = o.Description
			}
			opts[oi] = opt
		}
		qs[i] = map[string]any{
			"id": fmt.Sprintf("q%d", i), "header": q.Header, "question": q.Question,
			"multiSelect": q.MultiSelect, "options": opts,
		}
	}
	event := r.baseEvent()
	event["hook_event_name"] = "AskUserQuestion"
	event["tool_name"] = "ask"
	event["tool_input"] = bounded(map[string]any{"questions": qs}, maxFieldBytes)
	if d := r.requestDecision(ctx, event, "question"); d != nil && d.Kind == "question" && d.Selections != nil {
		return d.Selections
	}
	return nil
}

// Release clears the status chip and flushes pending telemetry, in bounded
// time.
func (r *Reporter) Release() {
	if !r.Active() || r.released.Swap(true) {
		return
	}
	if r.unwatch != nil {
		r.unwatch()
	}
	// Leave no stale chip behind: the pane is a plain terminal again.
	line := "clear_status " + statusKey + " " + r.paneTarget()
	if r.capability != "" {
		line = capabilityPrefix + " " + r.capability + " " + line
	}
	if conn, err := net.DialTimeout("unix", r.socket, r.connectTimeout); err == nil {
		_ = conn.SetDeadline(time.Now().Add(r.connectTimeout))
		_, _ = conn.Write([]byte(line + "\n"))
		buf := make([]byte, 256)
		_, _ = conn.Read(buf)
		conn.Close()
	}
	// Let the queue land the SessionEnd row that exit just pushed.
	done := make(chan struct{})
	r.enqueue(func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
	}
}
