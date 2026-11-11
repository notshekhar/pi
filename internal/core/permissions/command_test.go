package permissions

import (
	"strings"
	"testing"
)

// loop's command-deny cases.
func TestFindDeniedCommand(t *testing.T) {
	deny := []string{"rm", "git commit", "sudo"}
	cases := map[string]string{
		"ls -la":                       "",
		"/bin/rm -rf build":            "rm",
		"FOO=1 sudo rm -rf /tmp/x":     "rm",
		"cat list.txt && rm list.txt":  "rm",
		"echo $(git commit -m hi)":     "git commit",
		"rtk git commit -m x":          "git commit",
		"rtk proxy git commit":         "git commit",
		"bash -c 'git commit -m hi'":   "git commit",
		`sh -c "rm -rf build"`:         "rm",
		"env FOO=1 sudo rtk rm x":      "rm",
		"timeout 5 git commit -m x":    "git commit",
		"nice -n 10 git commit":        "git commit",
		"timeout -s KILL 5 rm x":       "rm",
		"sudo -u root rm x":            "rm",
		"git status && git commit -am": "git commit",
		"git committed":                "",
	}
	for command, want := range cases {
		if got := FindDeniedCommand(command, deny); got != want {
			t.Errorf("FindDeniedCommand(%q) = %q, want %q", command, got, want)
		}
	}
	if FindDeniedCommand("rm -rf /", nil) != "" {
		t.Error("an empty denylist denied something")
	}
}

// The denylist refuses before any rule, with loop's refusal text — even when
// a rule allows bash.
func TestPolicyBashDeny(t *testing.T) {
	p := Policy{Default: Allow, BashDeny: []string{"git push"}, Rules: []Rule{{Tool: "bash", Mode: Allow}}}
	d := p.Decide("bash", map[string]any{"command": "cd repo && git push origin main"})
	if d.Mode != Deny || !strings.Contains(d.Reason, "`git push` is blocked by the user's bash policy") {
		t.Errorf("decision = %+v", d)
	}
}

// Deny and ask rules see every command in the line; allow only the whole.
func TestBashRulesPerSegment(t *testing.T) {
	deny := Policy{Default: Allow, Rules: FromSetting(nil, nil, []string{"Bash(git push*)"})}
	for _, command := range []string{"ls && git push", "RUST_LOG=debug timeout 5 git push", "nice -n 10 git push", `bash -c "git push"`} {
		if d := deny.Decide("bash", map[string]any{"command": command}); d.Mode != Deny {
			t.Errorf("%q: %s, want deny", command, d.Mode)
		}
	}
	allow := Policy{Default: Allow, BashApprove: true, Rules: FromSetting([]string{"Bash(git *)"}, nil, nil)}
	if d := allow.Decide("bash", map[string]any{"command": "git status && rm -rf x"}); d.Mode == Allow {
		t.Error("an allow rule let a second command through")
	}
	if d := allow.Decide("bash", map[string]any{"command": "git status"}); d.Mode != Allow {
		t.Errorf("git status: %s, want allow", d.Mode)
	}
}

// bashApprove: an unremembered command asks; a remembered one runs, unless
// it is dangerous.
func TestBashApproveAndGrants(t *testing.T) {
	p := Policy{Default: Allow, BashApprove: true, BashGrants: []string{"npm test", "git"}}
	cases := map[string]Mode{
		"npm test -- --watch":  Allow,
		"npm install":          Ask,
		"git status":           Allow,
		"git push":             Ask,
		"npm test && rm -rf x": Ask,
	}
	for command, want := range cases {
		if d := p.Decide("bash", map[string]any{"command": command}); d.Mode != want {
			t.Errorf("%q: %s, want %s", command, d.Mode, want)
		}
	}
}
