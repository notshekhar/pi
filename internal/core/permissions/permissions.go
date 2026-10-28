// Package permissions decides whether a tool call may run.
//
// Three modes — allow, ask, deny — matched by rules against a call's tool
// name and its subject (a bash command, a file path). The policy is layered
// and ORDER-INDEPENDENT: every matching rule is evaluated and the strictest
// wins. A firewall's first-match-wins is easy to get subtly wrong when rules
// come from three places; "deny beats ask beats allow" cannot be.
//
// The defaults follow loop: read-only tools run freely, edits run freely
// inside the working directory, and a small set of genuinely destructive
// shell commands is denied outright. Prompting on everything trains people to
// approve without reading, which is worse than not asking.
package permissions

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Mode is what may happen to a call.
type Mode string

const (
	// Allow runs the call without asking.
	Allow Mode = "allow"
	// Ask puts the call to the user.
	Ask Mode = "ask"
	// Deny refuses the call and tells the model why.
	Deny Mode = "deny"
)

// rank orders modes by strictness, so the strictest match wins.
func (m Mode) rank() int {
	switch m {
	case Deny:
		return 3
	case Ask:
		return 2
	case Allow:
		return 1
	}
	return 0
}

// ParseMode reads a mode name.
func ParseMode(s string) (Mode, bool) {
	switch Mode(strings.ToLower(strings.TrimSpace(s))) {
	case Allow:
		return Allow, true
	case Ask:
		return Ask, true
	case Deny:
		return Deny, true
	}
	return "", false
}

// Rule matches calls to a tool, optionally narrowed to a subject glob.
type Rule struct {
	// Tool is a tool name, or "*" for any.
	Tool string
	// Pattern is a glob over the call's subject. Empty matches any subject.
	Pattern string
	Mode    Mode
	// Reason is shown to the model when this rule denies a call.
	Reason string
}

// String renders the rule in the syntax ParseRule accepts.
func (r Rule) String() string {
	if r.Pattern == "" {
		return fmt.Sprintf("%s %s", r.Mode, r.Tool)
	}
	return fmt.Sprintf("%s %s(%s)", r.Mode, r.Tool, r.Pattern)
}

// ParseRule reads `<mode> <tool>` or `<mode> <tool>(<glob>)`.
func ParseRule(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	mode, rest, ok := strings.Cut(s, " ")
	if !ok {
		return Rule{}, fmt.Errorf("permissions: %q is not `<mode> <tool>[(<glob>)]`", s)
	}
	m, ok := ParseMode(mode)
	if !ok {
		return Rule{}, fmt.Errorf("permissions: unknown mode %q", mode)
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return Rule{}, fmt.Errorf("permissions: %q names no tool", s)
	}
	if open := strings.Index(rest, "("); open >= 0 {
		if !strings.HasSuffix(rest, ")") {
			return Rule{}, fmt.Errorf("permissions: unclosed pattern in %q", s)
		}
		return Rule{
			Tool:    strings.TrimSpace(rest[:open]),
			Pattern: rest[open+1 : len(rest)-1],
			Mode:    m,
		}, nil
	}
	return Rule{Tool: rest, Mode: m}, nil
}

// matches reports whether the rule applies to a call.
//
// The tool name is GLOBBED, not compared. `*` alone still means every tool,
// and a plain name is still an exact match (a pattern with no wildcards
// compares equal), but it also makes `github__*` gate a whole MCP server —
// which is the only practical way to write a rule about a server whose tool
// names you have not seen yet.
func (r Rule) matches(tool, subject string) bool {
	if !Glob(r.Tool, tool) {
		return false
	}
	return r.Pattern == "" || Glob(r.Pattern, subject)
}

// Decision is the outcome of evaluating a policy.
type Decision struct {
	Mode Mode
	// Rule is the rule that decided, zero when the default applied.
	Rule Rule
	// Reason explains a deny, for the model.
	Reason string
}

// Policy is a rule set plus the fallback for calls nothing matches.
type Policy struct {
	Rules   []Rule
	Default Mode
	// CWD confines file writes. Empty disables the confinement check.
	CWD string
}

// Default is the shipped policy: read freely, edit inside the working
// directory, and refuse the handful of shell commands that destroy a machine.
func Default(cwd string) Policy {
	return Policy{
		CWD:     cwd,
		Default: Allow,
		Rules:   append([]Rule{}, destructive...),
	}
}

// destructive is the always-on deny list.
//
// Deliberately short. It covers commands whose damage is immediate,
// irreversible, and outside the repository — the cases where a confirmation
// prompt is not good enough because a person will approve it by reflex. Every
// other judgement call is left to the user's own rules.
var destructive = []Rule{
	{Tool: "bash", Pattern: "*rm -rf /*", Mode: Deny, Reason: "refuses to delete the filesystem root"},
	{Tool: "bash", Pattern: "*rm -rf ~*", Mode: Deny, Reason: "refuses to delete the home directory"},
	{Tool: "bash", Pattern: "*rm -rf --no-preserve-root*", Mode: Deny, Reason: "refuses --no-preserve-root"},
	{Tool: "bash", Pattern: "*mkfs*", Mode: Deny, Reason: "refuses to format a filesystem"},
	{Tool: "bash", Pattern: "*dd if=* of=/dev/*", Mode: Deny, Reason: "refuses to write directly to a device"},
	{Tool: "bash", Pattern: "*:(){*", Mode: Deny, Reason: "refuses a fork bomb"},
	{Tool: "bash", Pattern: "*> /dev/sd*", Mode: Deny, Reason: "refuses to write directly to a disk"},
	{Tool: "bash", Pattern: "*chmod -R 777 /*", Mode: Deny, Reason: "refuses to strip permissions from the filesystem"},
}

// Decide evaluates the policy for one call. The strictest matching rule wins.
func (p Policy) Decide(tool string, args map[string]any) Decision {
	subject := Subject(tool, args)

	best := Decision{Mode: p.Default}
	if best.Mode == "" {
		best.Mode = Allow
	}
	for _, rule := range p.Rules {
		if !rule.matches(tool, subject) {
			continue
		}
		if rule.Mode.rank() > best.Mode.rank() {
			best = Decision{Mode: rule.Mode, Rule: rule, Reason: rule.Reason}
		}
	}

	// A write outside the working directory is escalated, never silently
	// allowed: the agent was pointed at one repository, and touching anything
	// else is a decision its operator should make.
	if best.Mode == Allow && writesOutside(tool, args, p.CWD) {
		return Decision{
			Mode:   Ask,
			Reason: "writes outside the working directory",
		}
	}
	return best
}

// writesOutside reports a file-modifying call targeting a path outside cwd.
func writesOutside(tool string, args map[string]any, cwd string) bool {
	if cwd == "" {
		return false
	}
	switch tool {
	case "write", "edit":
	default:
		return false
	}
	path := pathArg(args)
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(cwd, path)
	}
	rel, err := filepath.Rel(cwd, filepath.Clean(path))
	if err != nil {
		return true
	}
	return rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Subject is the part of a call a rule's glob matches against: the command
// for a shell call, the path for a file call, the pattern for a search.
func Subject(tool string, args map[string]any) string {
	switch tool {
	case "bash":
		if cmd, ok := args["command"].(string); ok {
			return cmd
		}
	case "read", "write", "edit", "ls":
		return pathArg(args)
	case "grep", "glob":
		if p, ok := args["pattern"].(string); ok {
			return p
		}
	}
	return ""
}

func pathArg(args map[string]any) string {
	for _, key := range []string{"path", "file_path", "filePath"} {
		if v, ok := args[key].(string); ok {
			return v
		}
	}
	return ""
}

// Glob matches a pattern against a string, where `*` spans any run of
// characters including separators.
//
// Not filepath.Match: its `*` stops at a path separator, which is wrong for
// both the subjects here. A rule reading `rm -rf /*` has to match
// `sudo rm -rf /home/x`, and under filepath.Match it would not.
func Glob(pattern, s string) bool {
	if pattern == "" {
		return true
	}
	parts := strings.Split(pattern, "*")

	// No wildcards: an exact match.
	if len(parts) == 1 {
		return pattern == s
	}

	// A leading segment must sit at the start; a trailing one at the end.
	if parts[0] != "" {
		if !strings.HasPrefix(s, parts[0]) {
			return false
		}
		s = s[len(parts[0]):]
	}
	last := parts[len(parts)-1]
	middle := parts[1 : len(parts)-1]
	if last != "" {
		if !strings.HasSuffix(s, last) {
			return false
		}
		s = s[:len(s)-len(last)]
	}
	// Every interior segment must appear in order.
	for _, part := range middle {
		if part == "" {
			continue
		}
		i := strings.Index(s, part)
		if i < 0 {
			return false
		}
		s = s[i+len(part):]
	}
	return true
}

// Parse reads a list of rule strings, reporting the first bad one.
func Parse(lines []string) ([]Rule, error) {
	var out []Rule
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rule, err := ParseRule(line)
		if err != nil {
			return nil, err
		}
		out = append(out, rule)
	}
	return out, nil
}

// Plan is the read-only policy: the agent may investigate but not change
// anything, so it has to come back with a proposal instead of a fait
// accompli.
//
// Writes are denied outright rather than asked about. The point of the mode
// is a plan you can read and argue with BEFORE anything happens, and a
// prompt mid-investigation is exactly the interruption that erodes it into
// "approve, approve, approve".
//
// `bash` is asked rather than denied, because it is the one tool that cannot
// be classified statically: `git log` and `git push` arrive through the same
// door. Denying it outright would make the mode useless for planning against
// a real repository; asking keeps the read-only promise honest while letting
// an investigation proceed.
func Plan(cwd string) Policy {
	p := Default(cwd)
	p.Rules = append(p.Rules,
		Rule{Tool: "write", Mode: Deny, Reason: "plan mode is read-only — propose the change instead of making it"},
		Rule{Tool: "edit", Mode: Deny, Reason: "plan mode is read-only — propose the change instead of making it"},
		Rule{Tool: "bash", Mode: Ask, Reason: "plan mode is read-only"},
	)
	return p
}
