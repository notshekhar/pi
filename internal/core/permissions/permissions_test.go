package permissions

import "testing"

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"", "anything", true},
		{"exact", "exact", true},
		{"exact", "other", false},
		{"git *", "git status", true},
		{"git *", "npm install", false},
		{"*test*", "go test ./...", true},
		{"*test*", "go build", false},
		{"*.go", "main.go", true},
		{"*.go", "main.rs", false},
		// The reason this is not filepath.Match: `*` must span separators, or
		// a rule like `rm -rf /*` misses `sudo rm -rf /home/x`.
		{"*rm -rf /*", "sudo rm -rf /home/user", true},
		{"src/*", "src/a/b/c.go", true},
		{"a*b*c", "axxbyyc", true},
		{"a*b*c", "acb", false},
	}
	for _, c := range cases {
		if got := Glob(c.pattern, c.s); got != c.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestParseRule(t *testing.T) {
	r, err := ParseRule("deny bash(rm -rf *)")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != Deny || r.Tool != "bash" || r.Pattern != "rm -rf *" {
		t.Errorf("parsed %+v", r)
	}

	r, err = ParseRule("allow read")
	if err != nil {
		t.Fatal(err)
	}
	if r.Mode != Allow || r.Tool != "read" || r.Pattern != "" {
		t.Errorf("parsed %+v", r)
	}

	for _, bad := range []string{"", "bash", "maybe bash", "deny bash(unclosed", "deny"} {
		if _, err := ParseRule(bad); err == nil {
			t.Errorf("expected an error for %q", bad)
		}
	}
}

func TestRuleStringRoundTrips(t *testing.T) {
	for _, s := range []string{"deny bash(rm -rf *)", "allow read", "ask write(/etc/*)"} {
		r, err := ParseRule(s)
		if err != nil {
			t.Fatal(err)
		}
		if r.String() != s {
			t.Errorf("round trip: %q → %q", s, r.String())
		}
	}
}

// Order-independence is the whole point: rules arrive from defaults, settings
// and flags, and the strictest must win regardless of which came first.
func TestStrictestRuleWinsRegardlessOfOrder(t *testing.T) {
	allow := Rule{Tool: "bash", Mode: Allow}
	ask := Rule{Tool: "bash", Pattern: "git *", Mode: Ask}
	deny := Rule{Tool: "bash", Pattern: "git push*", Mode: Deny}

	orders := [][]Rule{
		{allow, ask, deny},
		{deny, ask, allow},
		{ask, deny, allow},
	}
	for i, rules := range orders {
		p := Policy{Rules: rules, Default: Allow}
		if got := p.Decide("bash", map[string]any{"command": "git push origin main"}); got.Mode != Deny {
			t.Errorf("order %d: got %v, want deny", i, got.Mode)
		}
		if got := p.Decide("bash", map[string]any{"command": "git status"}); got.Mode != Ask {
			t.Errorf("order %d: got %v, want ask", i, got.Mode)
		}
		if got := p.Decide("bash", map[string]any{"command": "ls"}); got.Mode != Allow {
			t.Errorf("order %d: got %v, want allow", i, got.Mode)
		}
	}
}

func TestDefaultAllowsOrdinaryWork(t *testing.T) {
	p := Default("/repo")
	allowed := []struct {
		tool string
		args map[string]any
	}{
		{"read", map[string]any{"path": "/repo/main.go"}},
		{"ls", map[string]any{"path": "/repo"}},
		{"grep", map[string]any{"pattern": "func"}},
		{"write", map[string]any{"path": "/repo/new.go"}},
		{"edit", map[string]any{"path": "main.go"}},
		{"bash", map[string]any{"command": "go test ./..."}},
		{"bash", map[string]any{"command": "git status"}},
		{"bash", map[string]any{"command": "rm -rf ./build"}},
	}
	for _, c := range allowed {
		if got := p.Decide(c.tool, c.args); got.Mode != Allow {
			t.Errorf("%s %v → %v (%s), want allow", c.tool, c.args, got.Mode, got.Reason)
		}
	}
}

func TestDefaultDeniesTheIrreversible(t *testing.T) {
	p := Default("/repo")
	denied := []string{
		"rm -rf /",
		"sudo rm -rf /",
		"rm -rf ~",
		"rm -rf --no-preserve-root /",
		"mkfs.ext4 /dev/sda1",
		"dd if=/dev/zero of=/dev/sda",
		":(){ :|:& };:",
		"chmod -R 777 /",
	}
	for _, cmd := range denied {
		got := p.Decide("bash", map[string]any{"command": cmd})
		if got.Mode != Deny {
			t.Errorf("%q → %v, want deny", cmd, got.Mode)
		}
		if got.Reason == "" {
			t.Errorf("%q denied with no reason for the model", cmd)
		}
	}
}

// A write outside the working directory is escalated, not silently allowed.
func TestWritesOutsideCWDAreEscalated(t *testing.T) {
	p := Default("/repo")
	outside := []map[string]any{
		{"path": "/etc/passwd"},
		{"path": "../secrets.txt"},
		{"path": "/repo/../elsewhere/x.go"},
	}
	for _, args := range outside {
		for _, tool := range []string{"write", "edit"} {
			if got := p.Decide(tool, args); got.Mode != Ask {
				t.Errorf("%s %v → %v, want ask", tool, args, got.Mode)
			}
		}
	}

	// Reading outside is fine; it is writes that are confined.
	if got := p.Decide("read", map[string]any{"path": "/etc/hosts"}); got.Mode != Allow {
		t.Errorf("read outside cwd → %v, want allow", got.Mode)
	}
	// And inside is untouched.
	if got := p.Decide("write", map[string]any{"path": "/repo/sub/x.go"}); got.Mode != Allow {
		t.Errorf("write inside cwd → %v, want allow", got.Mode)
	}
}

// An explicit deny must not be softened by the cwd escalation, which only
// ever upgrades an Allow.
func TestEscalationNeverWeakensADeny(t *testing.T) {
	p := Default("/repo")
	p.Rules = append(p.Rules, Rule{Tool: "write", Mode: Deny, Reason: "read-only session"})
	if got := p.Decide("write", map[string]any{"path": "/elsewhere/x.go"}); got.Mode != Deny {
		t.Errorf("got %v, want deny", got.Mode)
	}
}

func TestSubject(t *testing.T) {
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"bash", map[string]any{"command": "ls -la"}, "ls -la"},
		{"read", map[string]any{"path": "a.go"}, "a.go"},
		{"write", map[string]any{"file_path": "b.go"}, "b.go"},
		{"grep", map[string]any{"pattern": "func"}, "func"},
		{"unknown", map[string]any{"x": 1}, ""},
		{"bash", map[string]any{}, ""},
	}
	for _, c := range cases {
		if got := Subject(c.tool, c.args); got != c.want {
			t.Errorf("Subject(%q, %v) = %q, want %q", c.tool, c.args, got, c.want)
		}
	}
}

func TestParseSkipsBlanksAndComments(t *testing.T) {
	rules, err := Parse([]string{"", "# a comment", "deny bash(x)", "  "})
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 1 {
		t.Errorf("parsed %d rules, want 1", len(rules))
	}
}

func TestParseReportsBadRule(t *testing.T) {
	if _, err := Parse([]string{"deny bash(x)", "nonsense"}); err == nil {
		t.Error("expected an error for a malformed rule")
	}
}

// The wildcard tool applies to everything.
func TestWildcardTool(t *testing.T) {
	p := Policy{Rules: []Rule{{Tool: "*", Mode: Ask}}, Default: Allow}
	for _, tool := range []string{"bash", "read", "write"} {
		if got := p.Decide(tool, nil); got.Mode != Ask {
			t.Errorf("%s → %v, want ask", tool, got.Mode)
		}
	}
}

// Plan mode is read-only: investigation runs, changes do not.
func TestPlanModeIsReadOnly(t *testing.T) {
	p := Plan("/repo")

	for _, tool := range []string{"read", "ls", "grep", "glob"} {
		if got := p.Decide(tool, map[string]any{"path": "/repo/a.go", "pattern": "x"}); got.Mode != Allow {
			t.Errorf("%s → %v, want allow (investigation must run freely)", tool, got.Mode)
		}
	}
	for _, tool := range []string{"write", "edit"} {
		got := p.Decide(tool, map[string]any{"path": "/repo/a.go"})
		if got.Mode != Deny {
			t.Errorf("%s → %v, want deny", tool, got.Mode)
		}
		if got.Reason == "" {
			t.Errorf("%s denied with no reason for the model", tool)
		}
	}
	// bash cannot be classified statically — `git log` and `git push` come
	// through the same door — so it asks rather than being denied outright.
	if got := p.Decide("bash", map[string]any{"command": "git log"}); got.Mode != Ask {
		t.Errorf("bash → %v, want ask", got.Mode)
	}
	// The always-on deny list still applies underneath.
	if got := p.Decide("bash", map[string]any{"command": "rm -rf /"}); got.Mode != Deny {
		t.Errorf("destructive command in plan mode → %v, want deny", got.Mode)
	}
}

// A rule has to be able to gate a whole MCP server, whose tool names the user
// has not necessarily seen.
func TestToolNamesAreGlobbed(t *testing.T) {
	p := Policy{Default: Allow, Rules: []Rule{
		{Tool: "github__*", Mode: Deny, Reason: "no github writes"},
	}}
	for _, tool := range []string{"github__search", "github__create_issue"} {
		if got := p.Decide(tool, nil); got.Mode != Deny {
			t.Errorf("%s → %v, want deny", tool, got.Mode)
		}
	}
	// And it must not reach past the server it names.
	if got := p.Decide("gitlab__search", nil); got.Mode != Allow {
		t.Errorf("gitlab__search → %v, want allow", got.Mode)
	}
	// A plain name still means exactly that name.
	exact := Policy{Default: Allow, Rules: []Rule{{Tool: "read", Mode: Deny}}}
	if got := exact.Decide("read", nil); got.Mode != Deny {
		t.Errorf("exact rule broke: %v", got.Mode)
	}
	if got := exact.Decide("readfile", nil); got.Mode != Allow {
		t.Errorf("exact rule over-matched: %v", got.Mode)
	}
}

// MCP tools are ordinary tools: the policy gates them like everything else.
func TestMCPToolsGoThroughThePolicy(t *testing.T) {
	p := Default("/repo")
	// Allowed by default, like any tool the user chose to configure.
	if got := p.Decide("github__search", nil); got.Mode != Allow {
		t.Errorf("configured server tool → %v, want allow", got.Mode)
	}
	// And gateable.
	p.Rules = append(p.Rules, Rule{Tool: "*__delete*", Mode: Ask})
	if got := p.Decide("files__delete_file", nil); got.Mode != Ask {
		t.Errorf("→ %v, want ask", got.Mode)
	}
}
