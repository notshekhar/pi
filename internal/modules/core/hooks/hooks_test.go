package hooks

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func short(event Event, commands ...string) Config {
	cfg := Config{}
	for _, c := range commands {
		cfg = Add(cfg, event, "", c)
	}
	return cfg
}

func TestRunCollectsEveryHooksOutput(t *testing.T) {
	cfg := short(Stop, "echo first", "echo second")
	out := Run(context.Background(), cfg, Context{Event: Stop, CWD: t.TempDir()})
	if len(out.Messages) != 2 {
		t.Fatalf("got %d messages: %+v", len(out.Messages), out.Messages)
	}
	// Merged in CONFIG order even though the commands run in parallel.
	if out.Messages[0] != "first" || out.Messages[1] != "second" {
		t.Errorf("messages = %+v", out.Messages)
	}
}

func TestRunWithNoHooksDoesNothing(t *testing.T) {
	if got := Run(context.Background(), Config{}, Context{Event: Stop}); got.Block || len(got.Messages) > 0 {
		t.Errorf("got %+v", got)
	}
	// A hook bound to a different event must not fire.
	cfg := short(SessionStart, "echo nope")
	if got := Run(context.Background(), cfg, Context{Event: Stop}); len(got.Messages) > 0 {
		t.Errorf("got %+v", got)
	}
}

// The event's details arrive as environment variables, so one command can
// serve several events and read only what it cares about.
func TestHooksSeeTheEventInTheEnvironment(t *testing.T) {
	cfg := short(PostToolUse, "echo $PI_EVENT $PI_TOOL $PI_SUCCESS")
	out := Run(context.Background(), cfg, Context{
		Event: PostToolUse, ToolName: "edit", Success: true, CWD: t.TempDir(),
	})
	if len(out.Messages) != 1 || out.Messages[0] != "PostToolUse edit 1" {
		t.Errorf("messages = %+v", out.Messages)
	}
}

// The payload also arrives as JSON on stdin, which is the form Claude Code
// hook scripts read.
func TestHooksSeeTheJsonPayloadOnStdin(t *testing.T) {
	dir := t.TempDir()
	// Written to a file rather than echoed back: the payload IS valid JSON,
	// so a hook that prints it would be read as a decision object and
	// silently contribute nothing.
	cfg := short(PreToolUse, "cat > payload.json")
	Run(context.Background(), cfg, Context{
		Event: PreToolUse, ToolName: "bash", SessionID: "s1",
		ToolInput: map[string]any{"command": "ls"}, CWD: dir,
	})

	body, err := os.ReadFile(filepath.Join(dir, "payload.json"))
	if err != nil {
		t.Fatalf("the hook got no stdin: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("stdin was not JSON: %q", body)
	}
	if payload["hook_event_name"] != "PreToolUse" || payload["tool_name"] != "bash" {
		t.Errorf("payload = %+v", payload)
	}
	if payload["session_id"] != "s1" {
		t.Errorf("session_id missing: %+v", payload)
	}
	input, _ := payload["tool_input"].(map[string]any)
	if input["command"] != "ls" {
		t.Errorf("tool_input missing: %+v", payload)
	}
}

// Exit 2 is the block signal, and stderr is the reason. This is the whole
// difference between a hook that observes and one that enforces.
func TestExitTwoBlocksWithStderrAsTheReason(t *testing.T) {
	cfg := short(PreToolUse, "echo 'not while tests are red' >&2; exit 2")
	out := Run(context.Background(), cfg, Context{Event: PreToolUse, CWD: t.TempDir()})
	if !out.Block {
		t.Fatal("exit 2 did not block")
	}
	if !strings.Contains(out.Reason, "not while tests are red") {
		t.Errorf("reason = %q", out.Reason)
	}
}

// A block is FIRST-WINS in config order, so which hook refuses does not
// depend on which one finished first.
func TestFirstConfiguredBlockWins(t *testing.T) {
	cfg := short(PreToolUse,
		"sleep 0.2; echo first >&2; exit 2",
		"echo second >&2; exit 2")
	out := Run(context.Background(), cfg, Context{Event: PreToolUse, CWD: t.TempDir()})
	if !strings.Contains(out.Reason, "first") {
		t.Errorf("reason = %q, want the first configured hook's", out.Reason)
	}
}

// JSON on stdout is the richer contract.
func TestJsonDecisionBlocks(t *testing.T) {
	cfg := short(PreToolUse, `echo '{"decision":"block","reason":"nope"}'`)
	out := Run(context.Background(), cfg, Context{Event: PreToolUse, CWD: t.TempDir()})
	if !out.Block || out.Reason != "nope" {
		t.Errorf("out = %+v", out)
	}
}

func TestPermissionDecisionDenyBlocks(t *testing.T) {
	cfg := short(PreToolUse,
		`echo '{"hookSpecificOutput":{"permissionDecision":"deny","permissionDecisionReason":"read-only day"}}'`)
	out := Run(context.Background(), cfg, Context{Event: PreToolUse, CWD: t.TempDir()})
	if !out.Block || out.Reason != "read-only day" {
		t.Errorf("out = %+v", out)
	}
}

// "ask" has no prompt to raise here, and allowing it would grant exactly the
// call the hook wanted gated.
func TestPermissionDecisionAskDenies(t *testing.T) {
	cfg := short(PreToolUse, `echo '{"hookSpecificOutput":{"permissionDecision":"ask"}}'`)
	out := Run(context.Background(), cfg, Context{Event: PreToolUse, CWD: t.TempDir()})
	if !out.Block {
		t.Fatal("ask did not fail closed")
	}
}

func TestAdditionalContextIsCollected(t *testing.T) {
	cfg := short(UserPromptSubmit, `echo '{"hookSpecificOutput":{"additionalContext":"the build is red"}}'`)
	out := Run(context.Background(), cfg, Context{Event: UserPromptSubmit, CWD: t.TempDir()})
	if out.AdditionalContext != "the build is red" {
		t.Errorf("context = %q", out.AdditionalContext)
	}
}

// Plain text on the two model-facing events is context; anywhere else it is
// something to show the user.
func TestPlainStdoutIsContextOnlyOnModelFacingEvents(t *testing.T) {
	out := Run(context.Background(), short(SessionStart, "echo remember X"),
		Context{Event: SessionStart, CWD: t.TempDir()})
	if out.AdditionalContext != "remember X" || len(out.Messages) != 0 {
		t.Errorf("SessionStart: %+v", out)
	}
	out = Run(context.Background(), short(Stop, "echo done"), Context{Event: Stop, CWD: t.TempDir()})
	if len(out.Messages) != 1 || out.AdditionalContext != "" {
		t.Errorf("Stop: %+v", out)
	}
}

// A broken hook must not break a session, and must not block.
func TestFailingHookIsReportedNotFatal(t *testing.T) {
	cfg := short(Stop, "exit 3", "echo still ran")
	out := Run(context.Background(), cfg, Context{Event: Stop, CWD: t.TempDir()})
	if out.Block {
		t.Error("a failing hook blocked")
	}
	if len(out.Messages) != 2 || !strings.Contains(out.Messages[0], "exit 3") {
		t.Errorf("messages = %+v", out.Messages)
	}
}

func TestHookRunsInTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	out := Run(context.Background(), short(Stop, "pwd"), Context{Event: Stop, CWD: dir})
	// macOS reports /private/var for /var, so compare the suffix.
	if !strings.HasSuffix(out.Messages[0], strings.TrimPrefix(dir, "/private")) {
		t.Errorf("ran in %q, want %q", out.Messages[0], dir)
	}
}

// A cancelled context must stop hooks promptly.
func TestCancelledContextStopsAHook(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	Run(ctx, short(Stop, "sleep 5"), Context{Event: Stop, CWD: t.TempDir()})
	if time.Since(start) > 2*time.Second {
		t.Error("a cancelled hook was waited on")
	}
}

// Matchers decide which tools a hook fires for.
func TestMatcherTest(t *testing.T) {
	cases := []struct {
		matcher, value string
		want           bool
	}{
		{"", "bash", true},
		{"*", "bash", true},
		{"bash", "bash", true},
		{"bash", "edit", false},
		{"bash|edit", "edit", true},
		{"bash|edit", "read", false},
		{"^(bash|ed)", "edit", true},
		{"^(bash|ed)", "read", false},
		// An unparseable matcher matches NOTHING: a typo must not fire a
		// hook against every tool.
		{"[unclosed", "bash", false},
	}
	for _, c := range cases {
		if got := MatcherTest(c.matcher, c.value); got != c.want {
			t.Errorf("MatcherTest(%q, %q) = %v", c.matcher, c.value, got)
		}
	}
}

func TestMatcherGatesTheRun(t *testing.T) {
	cfg := Config{PreToolUse: {{Matcher: "bash", Hooks: []Command{{Command: "echo fired"}}}}}
	if out := Run(context.Background(), cfg, Context{Event: PreToolUse, ToolName: "edit", CWD: t.TempDir()}); len(out.Messages) > 0 {
		t.Errorf("a non-matching tool fired the hook: %+v", out.Messages)
	}
	if out := Run(context.Background(), cfg, Context{Event: PreToolUse, ToolName: "bash", CWD: t.TempDir()}); len(out.Messages) != 1 {
		t.Errorf("the matching tool did not fire: %+v", out.Messages)
	}
}

func TestParseBothShapes(t *testing.T) {
	cfg, err := Parse(map[string]json.RawMessage{
		"Stop":         json.RawMessage(`["go test ./..."]`),
		"SessionStart": json.RawMessage(`["echo hi", "  "]`), // blank entries are dropped
		"PreToolUse":   json.RawMessage(`[{"matcher":"bash","hooks":[{"type":"command","command":"./check.sh","timeout":60}]}]`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg[Stop]) != 1 || len(cfg[Stop][0].Hooks) != 1 {
		t.Errorf("short form: %+v", cfg[Stop])
	}
	if len(cfg[SessionStart][0].Hooks) != 1 {
		t.Errorf("blank entry kept: %+v", cfg[SessionStart])
	}
	group := cfg[PreToolUse][0]
	if group.Matcher != "bash" || group.Hooks[0].Timeout != 60 {
		t.Errorf("group form: %+v", group)
	}
}

// The names this package used before it adopted Claude Code's still load.
func TestParseAcceptsLegacyEventNames(t *testing.T) {
	cfg, err := Parse(map[string]json.RawMessage{"turnEnd": json.RawMessage(`["echo x"]`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg[Stop]) != 1 {
		t.Errorf("turnEnd did not map to Stop: %+v", cfg)
	}
}

// A typo must be reported, not silently bound to nothing.
func TestParseRejectsUnknownEvent(t *testing.T) {
	_, err := Parse(map[string]json.RawMessage{"onEverything": json.RawMessage(`["echo x"]`)})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "onEverything") {
		t.Errorf("error does not name the bad event: %v", err)
	}
	if !strings.Contains(err.Error(), "PreToolUse") {
		t.Errorf("error does not list the valid events: %v", err)
	}
}

func TestAddRemoveRoundTrip(t *testing.T) {
	cfg := Add(Config{}, PreToolUse, "bash", "./check.sh")
	cfg = Add(cfg, PreToolUse, "bash", "./lint.sh")
	if len(cfg[PreToolUse]) != 1 || len(cfg[PreToolUse][0].Hooks) != 2 {
		t.Fatalf("same matcher did not merge: %+v", cfg[PreToolUse])
	}
	if got := List(cfg); len(got) != 2 || got[0].Matcher != "bash" {
		t.Errorf("List = %+v", got)
	}

	cfg = Remove(cfg, PreToolUse, "bash", "./check.sh")
	if len(cfg[PreToolUse][0].Hooks) != 1 {
		t.Errorf("remove failed: %+v", cfg[PreToolUse])
	}
	cfg = Remove(cfg, PreToolUse, "bash", "./lint.sh")
	if _, still := cfg[PreToolUse]; still {
		t.Errorf("an emptied event was left behind: %+v", cfg)
	}

	// Marshal round-trips through Parse without losing the matcher.
	cfg = Add(Config{}, PostToolUse, "edit|write", "./format.sh")
	raw, err := Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if back[PostToolUse][0].Matcher != "edit|write" {
		t.Errorf("matcher lost in round trip: %+v", back[PostToolUse])
	}
}

func TestEventValid(t *testing.T) {
	for _, e := range Events {
		if !e.Valid() {
			t.Errorf("%q should be valid", e)
		}
	}
	if Event("nope").Valid() {
		t.Error("an unknown event reported valid")
	}
}
