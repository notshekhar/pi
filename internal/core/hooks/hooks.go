// Package hooks runs shell commands at points in a session's life.
//
// The shape is Claude Code's, deliberately: the event names, the matcher
// semantics, the JSON payload on stdin, and the exit-code contract are all
// theirs, so a hook script someone already wrote works here unchanged. That
// compatibility is the whole value of the feature — a hook system with its
// own private conventions is one every user has to learn before it saves them
// anything.
//
//	{ "hooks": { "PreToolUse": [ { "matcher": "bash",
//	    "hooks": [ { "type": "command", "command": "./check.sh", "timeout": 60 } ] } ] } }
//
// Per command: JSON on stdin. Exit 0 → stdout parsed as JSON (decision,
// hookSpecificOutput.permissionDecision, updatedInput, additionalContext,
// systemMessage). Exit 2 → BLOCK, with stderr as the reason. Any other exit →
// a warning that does not block.
//
// **Hooks CAN veto.** This package used to say they could not, on the
// grounds that gating belongs to `permissions`. That was the wrong call: the
// gate a user wants is routinely one no permission rule can express — "not
// while the tests are red", "not this file today" — and the whole reason to
// import a Claude Code hook is that it already enforces something. A hook's
// refusal is surfaced with its reason, so it is no more invisible than a
// denied permission. A hook's FAILURE is still never fatal: a broken hook is
// reported and the turn continues.
package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Event is a point in a session's life where hooks may run.
type Event string

const (
	// SessionStart fires once, as the session opens.
	SessionStart Event = "SessionStart"
	// UserPromptSubmit fires when the user submits a prompt. Blocking it
	// stops the turn before the model is called.
	UserPromptSubmit Event = "UserPromptSubmit"
	// PreToolUse fires before a tool call. Blocking it refuses the call.
	PreToolUse Event = "PreToolUse"
	// PostToolUse fires after a tool call completes, successfully or not.
	PostToolUse Event = "PostToolUse"
	// Notification fires when the agent needs attention.
	Notification Event = "Notification"
	// PermissionRequest fires when the policy escalates a call to the user.
	PermissionRequest Event = "PermissionRequest"
	// PreCompact fires before a session is compacted.
	PreCompact Event = "PreCompact"
	// SubagentStop fires when delegated work finishes.
	SubagentStop Event = "SubagentStop"
	// Stop fires when a turn finishes.
	Stop Event = "Stop"
	// SessionEnd fires as the session closes.
	SessionEnd Event = "SessionEnd"
)

// Events is every event a hook can bind to.
var Events = []Event{
	SessionStart, UserPromptSubmit, PreToolUse, PostToolUse, Notification,
	PermissionRequest, PreCompact, SubagentStop, Stop, SessionEnd,
}

// legacyEvents map the names this package used before it adopted Claude
// Code's, so a settings file written against the old spelling keeps working.
var legacyEvents = map[string]Event{
	"sessionStart": SessionStart,
	"userPrompt":   UserPromptSubmit,
	"toolDone":     PostToolUse,
	"turnEnd":      Stop,
}

// About is a one-line description of when an event fires, for the picker that
// offers them — an event list is only usable if the names explain themselves.
func About(e Event) string {
	switch e {
	case SessionStart:
		return "once, as the session opens"
	case UserPromptSubmit:
		return "when a prompt is submitted — blocking stops the turn"
	case PreToolUse:
		return "before a tool call — blocking refuses it"
	case PostToolUse:
		return "after a tool call finishes, successfully or not"
	case Notification:
		return "when the agent needs attention"
	case PermissionRequest:
		return "when the policy escalates a call to you"
	case PreCompact:
		return "before a session is compacted"
	case SubagentStop:
		return "when delegated work finishes"
	case Stop:
		return "when a turn finishes"
	case SessionEnd:
		return "as the session closes"
	}
	return ""
}

// Valid reports whether an event name is one that fires.
func (e Event) Valid() bool {
	for _, known := range Events {
		if e == known {
			return true
		}
	}
	return false
}

// DefaultTimeout bounds a hook that does not set its own. A hook that hangs
// would otherwise hang the turn that triggered it, and a formatter that waits
// on a lock is not hypothetical.
const DefaultTimeout = 30 * time.Second

// Command is one hook command.
type Command struct {
	Type string `json:"type,omitempty"`
	// Command is the shell line to run.
	Command string `json:"command"`
	// Timeout is in SECONDS, matching the config format. Zero means the
	// default.
	Timeout int `json:"timeout,omitempty"`
	// Async fires the command without waiting: it cannot block, and its
	// output is discarded. For notifiers, which have nothing to say back.
	Async bool `json:"async,omitempty"`
}

// MatcherGroup binds a set of commands to the tools whose names match.
type MatcherGroup struct {
	// Matcher is empty or "*" for all, a `|`-separated list of exact names,
	// or a regular expression.
	Matcher string    `json:"matcher,omitempty"`
	Hooks   []Command `json:"hooks"`
}

// Config is the hook table, keyed by event.
type Config map[Event][]MatcherGroup

// Context is what a hook is told about the moment it fired.
type Context struct {
	Event     Event
	CWD       string
	SessionID string
	ToolName  string
	ToolInput any
	ToolOut   any
	// Prompt is the user's text on UserPromptSubmit.
	Prompt string
	// Success reports a tool's outcome on PostToolUse.
	Success bool
}

// Outcome is what the hooks for one event decided, merged.
type Outcome struct {
	// Block is set when a hook refused the action.
	Block  bool
	Reason string
	// AdditionalContext is text a hook wants the model to see.
	AdditionalContext string
	// UpdatedInput replaces a tool call's arguments, when a PreToolUse hook
	// rewrote them.
	UpdatedInput any
	// Messages are what the hooks want shown to the user.
	Messages []string
}

// Run executes every hook bound to an event and merges the results.
//
// In PARALLEL, merged in config order: hooks routinely have nothing to do
// with each other (a notifier and a linter), and running them in sequence
// makes every turn wait for the slowest. Ordering is preserved where it
// matters — the FIRST configured block wins, so which hook refuses a call
// does not depend on which one happened to finish first.
func Run(ctx context.Context, cfg Config, hc Context) Outcome {
	groups := cfg[hc.Event]
	if len(groups) == 0 {
		return Outcome{}
	}

	// Flattened first so the merge can walk config order regardless of the
	// order the results arrive in.
	type job struct {
		cmd Command
		res result
	}
	var jobs []*job
	for _, group := range groups {
		if !MatcherTest(group.Matcher, hc.ToolName) {
			continue
		}
		for _, cmd := range group.Hooks {
			if cmd.Type != "" && cmd.Type != "command" {
				continue // other handler types are not supported
			}
			if strings.TrimSpace(cmd.Command) == "" {
				continue
			}
			jobs = append(jobs, &job{cmd: cmd})
		}
	}
	if len(jobs) == 0 {
		return Outcome{}
	}

	payload := payloadFor(hc)
	var wg sync.WaitGroup
	for _, j := range jobs {
		if j.cmd.Async {
			// Fire and forget: an async hook cannot block and is not waited
			// for, so a slow notifier costs the turn nothing.
			go runOne(context.WithoutCancel(ctx), j.cmd, hc, payload)
			continue
		}
		wg.Add(1)
		go func(j *job) {
			defer wg.Done()
			j.res = runOne(ctx, j.cmd, hc, payload)
		}(j)
	}
	wg.Wait()

	var outcome Outcome
	var contexts []string
	for _, j := range jobs {
		if j.cmd.Async {
			continue
		}
		apply(hc.Event, j.cmd, j.res, &outcome, &contexts)
	}
	if len(contexts) > 0 {
		outcome.AdditionalContext = strings.Join(contexts, "\n")
	}
	return outcome
}

// result is one command's raw outcome.
type result struct {
	code     int
	stdout   string
	stderr   string
	timedOut bool
	ran      bool
}

func runOne(ctx context.Context, c Command, hc Context, payload []byte) result {
	timeout := DefaultTimeout
	if c.Timeout > 0 {
		timeout = time.Duration(c.Timeout) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", c.Command)
	cmd.Dir = hc.CWD
	cmd.Stdin = strings.NewReader(string(payload))
	// The payload also arrives as environment variables. Redundant with
	// stdin on purpose: a one-line hook wants "$PI_TOOL", not a JSON parser.
	cmd.Env = append(os.Environ(),
		"PI_EVENT="+string(hc.Event),
		"PI_CWD="+hc.CWD,
		"PI_SESSION="+hc.SessionID,
		"PI_TOOL="+hc.ToolName,
		"PI_PROMPT="+hc.Prompt,
		"PI_SUCCESS="+boolEnv(hc.Success),
	)

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()

	res := result{stdout: stdout.String(), stderr: stderr.String(), ran: true}
	if ctx.Err() != nil {
		res.timedOut = true
		return res
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			res.code = exit.ExitCode()
		} else {
			// Could not be started at all — a missing interpreter, a bad
			// working directory. Reported as a failure rather than a block.
			res.code = -1
			res.stderr = strings.TrimSpace(res.stderr + "\n" + err.Error())
		}
	}
	return res
}

// jsonOutput is the shape a hook may print on stdout.
type jsonOutput struct {
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	Continue      *bool  `json:"continue"`
	StopReason    string `json:"stopReason"`
	SystemMessage string `json:"systemMessage"`
	Specific      *struct {
		PermissionDecision       string `json:"permissionDecision"`
		PermissionDecisionReason string `json:"permissionDecisionReason"`
		AdditionalContext        string `json:"additionalContext"`
		UpdatedInput             any    `json:"updatedInput"`
	} `json:"hookSpecificOutput"`
}

// apply folds one command's result into the merged outcome.
//
// Output is always collected; block decisions are FIRST-WINS in config order,
// so a later hook can never quietly un-block what an earlier one refused.
func apply(event Event, cmd Command, res result, outcome *Outcome, contexts *[]string) {
	if !res.ran {
		return
	}
	block := func(reason string) {
		if !outcome.Block {
			outcome.Block = true
			outcome.Reason = clip(reason, 500)
		}
	}

	if res.timedOut {
		outcome.Messages = append(outcome.Messages,
			fmt.Sprintf("hook timed out (%s): %s", event, clip(cmd.Command, 200)))
		return
	}
	if res.code == 2 {
		reason := strings.TrimSpace(res.stderr)
		if reason == "" {
			reason = fmt.Sprintf("blocked by %s hook", event)
		}
		block(reason)
		return
	}
	if res.code != 0 {
		detail := strings.TrimSpace(res.stderr)
		if detail == "" {
			detail = cmd.Command
		}
		outcome.Messages = append(outcome.Messages,
			fmt.Sprintf("hook failed (%s, exit %d): %s", event, res.code, clip(detail, 500)))
		return
	}

	text := strings.TrimSpace(res.stdout)
	if text == "" {
		return
	}
	var parsed jsonOutput
	if err := json.Unmarshal([]byte(text), &parsed); err != nil {
		// Plain text on the two events that feed the model is CONTEXT;
		// anywhere else it is something to show the user. Claude Code's rule,
		// and the reason `echo "remember X"` works as a SessionStart hook.
		if event == SessionStart || event == UserPromptSubmit {
			*contexts = append(*contexts, text)
		} else {
			outcome.Messages = append(outcome.Messages, clip(text, 1000))
		}
		return
	}

	if parsed.SystemMessage != "" {
		outcome.Messages = append(outcome.Messages, clip(parsed.SystemMessage, 1000))
	}
	if s := parsed.Specific; s != nil {
		if s.AdditionalContext != "" {
			*contexts = append(*contexts, s.AdditionalContext)
		}
		if s.UpdatedInput != nil && outcome.UpdatedInput == nil {
			outcome.UpdatedInput = s.UpdatedInput
		}
	}
	// `continue: false` halts everything in Claude Code — a block here.
	if parsed.Continue != nil && !*parsed.Continue {
		block(firstNonEmpty(parsed.StopReason, parsed.Reason,
			fmt.Sprintf("stopped by %s hook", event)))
		return
	}
	if s := parsed.Specific; s != nil && s.PermissionDecision == "ask" {
		// "ask" wants a prompt this path cannot raise. Denying is the safe
		// fallback: silently allowing would grant exactly the call the hook
		// asked to have gated.
		block(firstNonEmpty(s.PermissionDecisionReason,
			fmt.Sprintf("%s hook requested confirmation", event)) + " (ask unsupported — denied)")
		return
	}
	deny := parsed.Decision == "block"
	if s := parsed.Specific; s != nil && s.PermissionDecision == "deny" {
		deny = true
	}
	if deny {
		reason := parsed.Reason
		if reason == "" && parsed.Specific != nil {
			reason = parsed.Specific.PermissionDecisionReason
		}
		block(firstNonEmpty(reason, fmt.Sprintf("blocked by %s hook", event)))
	}
}

// payloadFor builds the JSON a hook reads on stdin.
func payloadFor(hc Context) []byte {
	payload := map[string]any{
		"cwd":             hc.CWD,
		"hook_event_name": string(hc.Event),
	}
	if hc.SessionID != "" {
		payload["session_id"] = hc.SessionID
	}
	if hc.ToolName != "" {
		payload["tool_name"] = hc.ToolName
	}
	if hc.ToolInput != nil {
		payload["tool_input"] = hc.ToolInput
	}
	if hc.ToolOut != nil {
		payload["tool_output"] = hc.ToolOut
	}
	if hc.Prompt != "" {
		payload["prompt"] = hc.Prompt
	}
	// A payload that cannot be encoded must not stop the hook from running —
	// the event itself is the part that matters.
	encoded, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return encoded
}

// wordMatcher is a matcher written as exact names rather than a pattern.
var wordMatcher = regexp.MustCompile(`^[\w|]+$`)

// MatcherTest applies Claude Code's matcher rules: empty or "*" matches
// everything; word characters and `|` are an exact list; anything else is a
// regular expression.
func MatcherTest(matcher, value string) bool {
	if matcher == "" || matcher == "*" {
		return true
	}
	if value == "" {
		return true // an event with nothing to match against
	}
	if wordMatcher.MatchString(matcher) {
		for _, name := range strings.Split(matcher, "|") {
			if name == value {
				return true
			}
		}
		return false
	}
	re, err := regexp.Compile(matcher)
	if err != nil {
		// An unparseable matcher matches nothing rather than everything: a
		// typo must not silently fire a hook against every tool.
		return false
	}
	return re.MatchString(value)
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "… [truncated]"
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func boolEnv(b bool) string {
	if b {
		return "1"
	}
	return "0"
}

// Parse reads a hook table from settings.
//
// Two shapes are accepted. The full one is Claude Code's, with matchers and
// per-command timeouts; the short one is a bare list of shell lines per
// event, which is what most hooks are and which nobody should have to write
// three levels of JSON for.
func Parse(raw map[string]json.RawMessage) (Config, error) {
	cfg := Config{}
	for name, value := range raw {
		event := Event(name)
		if legacy, ok := legacyEvents[name]; ok {
			event = legacy
		}
		if !event.Valid() {
			return nil, fmt.Errorf("hooks: unknown event %q; use one of %s", name, EventNames())
		}

		var groups []MatcherGroup
		if err := json.Unmarshal(value, &groups); err == nil && looksLikeGroups(groups) {
			for _, g := range groups {
				if len(g.Hooks) > 0 {
					cfg[event] = append(cfg[event], g)
				}
			}
			continue
		}

		var lines []string
		if err := json.Unmarshal(value, &lines); err != nil {
			return nil, fmt.Errorf("hooks: %s must be a list of commands or matcher groups", name)
		}
		var commands []Command
		for _, line := range lines {
			if strings.TrimSpace(line) == "" {
				continue
			}
			commands = append(commands, Command{Command: line})
		}
		if len(commands) > 0 {
			cfg[event] = append(cfg[event], MatcherGroup{Hooks: commands})
		}
	}
	return cfg, nil
}

// looksLikeGroups distinguishes the two accepted shapes.
//
// An empty list decodes cleanly as EITHER, and a list of strings fails to
// decode as groups — but a list of groups with no `hooks` key also decodes
// "successfully" into empty groups, which would silently swallow the short
// form if it ever changed. Requiring at least one group with commands makes
// the fallback to the short form explicit.
func looksLikeGroups(groups []MatcherGroup) bool {
	for _, g := range groups {
		if len(g.Hooks) > 0 {
			return true
		}
	}
	return false
}

// EventNames lists the events, for error messages and help.
func EventNames() string {
	names := make([]string, 0, len(Events))
	for _, e := range Events {
		names = append(names, string(e))
	}
	return strings.Join(names, ", ")
}

// Marshal renders a table back to the settings form.
//
// Always the full matcher-group shape, never the short one: a table that was
// read as short form and is being written back may have had a matcher added,
// and round-tripping through the short form would silently drop it.
func Marshal(cfg Config) (map[string]json.RawMessage, error) {
	out := map[string]json.RawMessage{}
	for event, groups := range cfg {
		if len(groups) == 0 {
			continue
		}
		encoded, err := json.Marshal(groups)
		if err != nil {
			return nil, err
		}
		out[string(event)] = encoded
	}
	return out, nil
}

// Entry is one hook command with the event and matcher it is bound to —
// the flattened view a manager panel lists.
type Entry struct {
	Event   Event
	Matcher string
	Command Command
}

// List flattens a table, in event order so the panel does not reshuffle
// itself between openings (Go map iteration order is deliberately random).
func List(cfg Config) []Entry {
	var out []Entry
	for _, event := range Events {
		for _, group := range cfg[event] {
			for _, cmd := range group.Hooks {
				out = append(out, Entry{Event: event, Matcher: group.Matcher, Command: cmd})
			}
		}
	}
	return out
}

// Add binds a command to an event, returning the updated table.
func Add(cfg Config, event Event, matcher, command string) Config {
	if cfg == nil {
		cfg = Config{}
	}
	// Merged into an existing group with the same matcher, so a panel that
	// adds three hooks for `bash` produces one group rather than three.
	for i, group := range cfg[event] {
		if group.Matcher == matcher {
			cfg[event][i].Hooks = append(group.Hooks, Command{Command: command})
			return cfg
		}
	}
	cfg[event] = append(cfg[event], MatcherGroup{
		Matcher: matcher,
		Hooks:   []Command{{Command: command}},
	})
	return cfg
}

// Remove drops one command, and any group or event left empty by it.
func Remove(cfg Config, event Event, matcher, command string) Config {
	groups := cfg[event]
	kept := make([]MatcherGroup, 0, len(groups))
	for _, group := range groups {
		if group.Matcher != matcher {
			kept = append(kept, group)
			continue
		}
		commands := make([]Command, 0, len(group.Hooks))
		for _, c := range group.Hooks {
			if c.Command != command {
				commands = append(commands, c)
			}
		}
		if len(commands) > 0 {
			group.Hooks = commands
			kept = append(kept, group)
		}
	}
	if len(kept) == 0 {
		delete(cfg, event)
		return cfg
	}
	cfg[event] = kept
	return cfg
}
