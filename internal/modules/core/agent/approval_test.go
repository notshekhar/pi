package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/permissions"
)

func call(tool string, args map[string]any) ai.ToolCall {
	raw, _ := json.Marshal(args)
	return ai.ToolCall{ToolCallID: "c1", ToolName: tool, Input: raw}
}

// run builds a Run with a policy and an answer for any `ask`.
func run(t *testing.T, policy permissions.Policy, ask Ask) *Run {
	t.Helper()
	return &Run{
		Config:      config.Config{CWD: "/repo"},
		Permissions: policy,
		Ask:         ask,
	}
}

func TestAllowedCallsNeedNoApproval(t *testing.T) {
	r := run(t, permissions.Default("/repo"), nil)
	needs, _ := r.approvalHooks()

	got, err := needs(context.Background(), call("read", map[string]any{"path": "/repo/a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an allowed call should not need approval")
	}
}

// A deny is refused in the policy layer and must never reach the person.
func TestDeniedCallNeverReachesTheUser(t *testing.T) {
	asked := false
	r := run(t, permissions.Default("/repo"), func(context.Context, string, map[string]any, string) bool {
		asked = true
		return true // even a "yes" must not save it
	})
	needs, approve := r.approvalHooks()

	c := call("bash", map[string]any{"command": "rm -rf /"})
	if ok, _ := needs(context.Background(), c); !ok {
		t.Fatal("a denied call must be routed through approval")
	}
	d, err := approve(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Error("a denied call was approved")
	}
	if asked {
		t.Error("a denied call was put to the user")
	}
	if d.Reason == "" {
		t.Error("a denial must tell the model why")
	}
}

// Nothing to ask means nothing gets approved. A non-interactive run has
// nobody listening, and running the call anyway is the exact failure the
// package exists to prevent.
func TestAskWithNoAskerDenies(t *testing.T) {
	policy := permissions.Policy{
		CWD: "/repo", Default: permissions.Allow,
		Rules: []permissions.Rule{{Tool: "bash", Mode: permissions.Ask}},
	}
	r := run(t, policy, nil)
	_, approve := r.approvalHooks()

	d, err := approve(context.Background(), call("bash", map[string]any{"command": "ls"}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Error("approved a call with nobody to ask")
	}
	if !strings.Contains(d.Reason, "nobody") {
		t.Errorf("reason should say why: %q", d.Reason)
	}
}

func TestAskHonoursTheAnswer(t *testing.T) {
	policy := permissions.Policy{
		CWD: "/repo", Default: permissions.Allow,
		Rules: []permissions.Rule{{Tool: "bash", Mode: permissions.Ask}},
	}
	for _, answer := range []bool{true, false} {
		r := run(t, policy, func(context.Context, string, map[string]any, string) bool { return answer })
		_, approve := r.approvalHooks()
		d, err := approve(context.Background(), call("bash", map[string]any{"command": "ls"}))
		if err != nil {
			t.Fatal(err)
		}
		if d.Approved != answer {
			t.Errorf("answer %v produced Approved=%v", answer, d.Approved)
		}
	}
}

// The user sees the real arguments, not a re-encoded guess.
func TestAskReceivesTheCallsArguments(t *testing.T) {
	policy := permissions.Policy{
		CWD: "/repo", Default: permissions.Allow,
		Rules: []permissions.Rule{{Tool: "bash", Mode: permissions.Ask}},
	}
	var gotTool, gotCmd string
	r := run(t, policy, func(_ context.Context, tool string, args map[string]any, _ string) bool {
		gotTool = tool
		gotCmd, _ = args["command"].(string)
		return false
	})
	_, approve := r.approvalHooks()
	if _, err := approve(context.Background(), call("bash", map[string]any{"command": "git push"})); err != nil {
		t.Fatal(err)
	}
	if gotTool != "bash" || gotCmd != "git push" {
		t.Errorf("asker saw tool=%q command=%q", gotTool, gotCmd)
	}
}

// The zero policy must be the safe default, not "allow everything".
func TestZeroPolicyUsesTheDefaults(t *testing.T) {
	r := &Run{Config: config.Config{CWD: "/repo"}}
	_, approve := r.approvalHooks()
	d, err := approve(context.Background(), call("bash", map[string]any{"command": "rm -rf /"}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Error("the zero policy approved a destructive command")
	}
}

// /cd moves the working directory, and the confinement check has to follow.
func TestPolicyFollowsTheWorkingDirectory(t *testing.T) {
	r := &Run{Config: config.Config{CWD: "/repo"}}
	needs, _ := r.approvalHooks()
	if ok, _ := needs(context.Background(), call("write", map[string]any{"path": "/other/x.go"})); !ok {
		t.Error("a write outside cwd should be escalated")
	}

	r.Config.CWD = "/other"
	needs, _ = r.approvalHooks()
	if ok, _ := needs(context.Background(), call("write", map[string]any{"path": "/other/x.go"})); ok {
		t.Error("after moving cwd the same write should be allowed")
	}
}

// Malformed arguments must fall through to the policy, never dodge a rule.
func TestUnparseableArgumentsDoNotBypassPolicy(t *testing.T) {
	policy := permissions.Policy{
		CWD: "/repo", Default: permissions.Ask,
		Rules: []permissions.Rule{{Tool: "bash", Mode: permissions.Ask}},
	}
	r := run(t, policy, nil)
	needs, _ := r.approvalHooks()

	broken := ai.ToolCall{ToolCallID: "c1", ToolName: "bash", Input: json.RawMessage("{not json")}
	ok, err := needs(context.Background(), broken)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("a call with unparseable arguments escaped the policy")
	}
}

// Plan mode replaces the policy. Layering it would let a stored `allow write`
// cancel the very restriction the mode exists to impose.
func TestPlanModeCannotBeWeakenedByStoredRules(t *testing.T) {
	r := &Run{
		Config:   config.Config{CWD: "/repo"},
		Planning: true,
		Permissions: permissions.Policy{
			CWD: "/repo", Default: permissions.Allow,
			Rules: []permissions.Rule{{Tool: "write", Mode: permissions.Allow}},
		},
	}
	_, approve := r.approvalHooks()
	d, err := approve(context.Background(), call("write", map[string]any{"path": "/repo/a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if d.Approved {
		t.Error("a stored allow-rule overrode plan mode")
	}
}

func TestPlanModeAllowsReading(t *testing.T) {
	r := &Run{Config: config.Config{CWD: "/repo"}, Planning: true}
	needs, _ := r.approvalHooks()
	if ok, _ := needs(context.Background(), call("read", map[string]any{"path": "/repo/a.go"})); ok {
		t.Error("reading should run freely in plan mode")
	}
}

func TestPlanPromptOnlyAppearsWhenPlanning(t *testing.T) {
	if got := SystemPromptFor("/repo", nil, false); strings.Contains(got, "PLAN MODE") {
		t.Error("the plan prompt leaked into a normal turn")
	}
	if got := SystemPromptFor("/repo", nil, true); !strings.Contains(got, "PLAN MODE") {
		t.Error("the plan prompt is missing in plan mode")
	}
}

// A PreToolUse hook must see calls the POLICY allows outright — refusing
// something the rules are happy with is the only reason to have one.
func TestPreToolHookSeesAllowedCalls(t *testing.T) {
	seen := ""
	r := run(t, permissions.Default("/repo"), nil)
	r.PreTool = func(_ context.Context, tool string, _ map[string]any) (map[string]any, string) {
		seen = tool
		return nil, ""
	}

	needs, approve := r.approvalHooks()
	read := call("read", map[string]any{"path": "/repo/a.go"})
	got, err := needs(context.Background(), read)
	if err != nil {
		t.Fatal(err)
	}
	if !got {
		t.Fatal("an allowed call skipped the hook")
	}
	decision, err := approve(context.Background(), read)
	if err != nil {
		t.Fatal(err)
	}
	if seen != "read" {
		t.Errorf("the hook was not consulted, saw %q", seen)
	}
	if !decision.Approved {
		t.Errorf("a hook that allowed the call still refused it: %+v", decision)
	}
}

// And its refusal has to stop the call, with its own reason.
func TestPreToolHookRefusesTheCall(t *testing.T) {
	r := run(t, permissions.Default("/repo"), nil)
	r.PreTool = func(context.Context, string, map[string]any) (map[string]any, string) {
		return nil, "not while the tests are red"
	}
	_, approve := r.approvalHooks()

	decision, err := approve(context.Background(), call("read", map[string]any{"path": "/repo/a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if decision.Approved {
		t.Fatal("the hook's refusal was ignored")
	}
	if !strings.Contains(decision.Reason, "not while the tests are red") {
		t.Errorf("reason = %q, want the hook's own", decision.Reason)
	}
}

// With no hook installed, nothing changes: an allowed call still skips the
// approval path entirely.
func TestWithoutPreToolHookAllowedCallsStillSkipApproval(t *testing.T) {
	r := run(t, permissions.Default("/repo"), nil)
	needs, _ := r.approvalHooks()
	got, err := needs(context.Background(), call("read", map[string]any{"path": "/repo/a.go"}))
	if err != nil {
		t.Fatal(err)
	}
	if got {
		t.Error("an allowed call needed approval with no hook installed")
	}
}

// A rewritten call is re-judged by the POLICY as what will actually run.
// Otherwise the rewrite seam is a way around the permission rules: ask for
// something harmless, rewrite it into something the policy would refuse.
func TestRewrittenCallIsJudgedAsRewritten(t *testing.T) {
	r := run(t, permissions.Default("/repo"), nil)
	r.RewriteCall = func(_ string, args map[string]any) map[string]any {
		return map[string]any{"command": "rm -rf /"}
	}
	_, approve := r.approvalHooks()

	decision, err := approve(context.Background(), call("bash", map[string]any{"command": "ls"}))
	if err != nil {
		t.Fatal(err)
	}
	// The always-on deny list refuses `rm -rf /`. A policy that judged the
	// ORIGINAL `ls` would have let it through.
	if decision.Approved {
		t.Error("the policy judged the original call, not the rewritten one")
	}
}

// And a rewrite the policy is happy with runs as the rewritten call.
func TestRewriteReachesTheDecision(t *testing.T) {
	r := run(t, permissions.Default("/repo"), nil)
	r.RewriteCall = func(_ string, args map[string]any) map[string]any {
		return map[string]any{"command": "rtk git status"}
	}
	_, approve := r.approvalHooks()

	decision, err := approve(context.Background(), call("bash", map[string]any{"command": "git status"}))
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Approved {
		t.Fatalf("an allowed rewrite was refused: %s", decision.Reason)
	}
	if !strings.Contains(string(decision.UpdatedInput), "rtk git status") {
		t.Errorf("the rewrite did not reach the decision: %s", decision.UpdatedInput)
	}
}

// Hooks run before extensions: a hook's refusal is the user's own rule, and a
// rewrite must never slip past it.
func TestHookRefusalBeatsAnExtensionRewrite(t *testing.T) {
	rewrote := false
	r := run(t, permissions.Default("/repo"), nil)
	r.PreTool = func(context.Context, string, map[string]any) (map[string]any, string) {
		return nil, "blocked"
	}
	r.RewriteCall = func(_ string, args map[string]any) map[string]any {
		rewrote = true
		return args
	}
	_, approve := r.approvalHooks()

	decision, _ := approve(context.Background(), call("read", map[string]any{"path": "/repo/a.go"}))
	if decision.Approved {
		t.Error("a refused call was approved")
	}
	if rewrote {
		t.Error("an extension rewrote a call a hook had already refused")
	}
}
