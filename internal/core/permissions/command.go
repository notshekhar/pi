package permissions

import (
	"path"
	"regexp"
	"strings"
)

// Reading a bash command line the way loop's denylist and rules engine do
// (tools/utils/command-deny.ts): split it into the commands it would run,
// peel the setup prefix off each, and match the real command.
//
// A guardrail, NOT a security boundary. It stops an honest model and
// ordinary invocations; it cannot stop a determined bypass (base64 piped to
// sh, a script written then run). That is what the OS sandbox is for.

// wrappers run another command; the real one is looked past, so `sudo rm`
// resolves to rm. `rtk` is a token proxy (`rtk <cmd>`, `rtk proxy <cmd>`).
var wrappers = map[string]bool{
	"sudo": true, "env": true, "command": true, "nohup": true, "time": true, "timeout": true,
	"nice": true, "ionice": true, "chrt": true, "stdbuf": true, "xargs": true, "rtk": true,
}

// wrapperValueOptions are wrapper options whose value is the NEXT token
// (`nice -n 10`, `timeout -s KILL 5`): peeled with the option, not mistaken
// for the real command.
var wrapperValueOptions = map[string][]string{
	"sudo":    {"-u", "--user", "-g", "--group", "-C", "-D", "-h", "-p", "-r", "-t", "-U"},
	"env":     {"-u", "--unset", "-C", "--chdir", "-S", "--split-string"},
	"timeout": {"-s", "--signal", "-k", "--kill-after"},
	"nice":    {"-n", "--adjustment"},
	"ionice":  {"-c", "--class", "-n", "--classdata", "-p", "--pid"},
	"chrt":    {"-p", "--pid"},
	"stdbuf":  {"-i", "-o", "-e"},
	"xargs":   {"-n", "-I", "-L", "-P", "-s", "-d", "-E", "-a"},
}

var (
	envAssignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	// wrapperNumber is a wrapper's bare numeric argument: `timeout 5`, `1.5m`.
	wrapperNumber     = regexp.MustCompile(`^\d+(\.\d+)?[smhd]?$`)
	segmentSeparators = regexp.MustCompile(`\|\||&&|[;|&\n]`)
	// shellDashC is `sh -c "<script>"` (bash, zsh, …): an inline script.
	shellDashC   = regexp.MustCompile(`\b(?:sh|bash|zsh|dash|ksh)\s+-c\s+(?:'([\s\S]*?)'|"([\s\S]*?)")`)
	substitution = regexp.MustCompile(`\$\(([\s\S]*?)\)`)
)

// splitSegments breaks a command line into the commands it would run, with
// `sh -c` bodies and command substitutions flattened into their own
// segments so a command hidden inside one is still seen.
func splitSegments(command string) []string {
	flat := shellDashC.ReplaceAllString(command, " ; $1$2 ; ")
	flat = substitution.ReplaceAllString(flat, " ; $1 ; ")
	flat = strings.ReplaceAll(flat, "`", " ; ")
	var out []string
	for _, segment := range segmentSeparators.Split(flat, -1) {
		if segment = strings.TrimSpace(segment); segment != "" {
			out = append(out, segment)
		}
	}
	return out
}

// normalizeToken strips surrounding quotes and reduces a path to its
// basename: /bin/rm → rm.
func normalizeToken(token string) string {
	unquoted := strings.Trim(token, `'"`)
	if base := path.Base(unquoted); base != "." && base != "/" {
		return base
	}
	return unquoted
}

// peelWrappers drops the prefix that only sets up the real command: env
// assignments, wrappers, and each wrapper's own options and numeric
// arguments, in any interleaving. Tokens come back as written.
func peelWrappers(tokens []string) []string {
	wrapper := ""
	i := 0
	for ; i < len(tokens); i++ {
		token := tokens[i]
		if envAssignment.MatchString(token) {
			continue
		}
		if head := normalizeToken(token); wrappers[head] {
			wrapper = head
			if head == "rtk" && i+1 < len(tokens) && normalizeToken(tokens[i+1]) == "proxy" {
				i++
			}
			continue
		}
		if wrapper != "" && strings.HasPrefix(token, "-") {
			for _, option := range wrapperValueOptions[wrapper] {
				if token == option {
					i++
					break
				}
			}
			continue
		}
		if wrapper != "" && wrapperNumber.MatchString(token) {
			continue
		}
		break
	}
	return tokens[i:]
}

// resolvedCommand is a segment's real command and the tokens after it.
type resolvedCommand struct {
	name string
	rest []string
}

func resolveSegment(segment string) (resolvedCommand, bool) {
	tokens := peelWrappers(strings.Fields(segment))
	if len(tokens) == 0 {
		return resolvedCommand{}, false
	}
	rest := make([]string, len(tokens)-1)
	for i, t := range tokens[1:] {
		rest[i] = normalizeToken(t)
	}
	return resolvedCommand{name: normalizeToken(tokens[0]), rest: rest}, true
}

// matches reports a denylist pattern ("rm", "git commit") naming this
// command: the name, then each further word as a prefix of its arguments.
func (c resolvedCommand) matches(pattern string) bool {
	words := strings.Fields(pattern)
	if len(words) == 0 || normalizeToken(words[0]) != c.name || len(words)-1 > len(c.rest) {
		return false
	}
	for i, word := range words[1:] {
		if c.rest[i] != normalizeToken(word) {
			return false
		}
	}
	return true
}

// FindDeniedCommand is the first denylist pattern any command in the line
// would trigger, or "".
func FindDeniedCommand(command string, denylist []string) string {
	resolved := resolveAll(command)
	for _, pattern := range denylist {
		for _, c := range resolved {
			if c.matchesAny([]string{pattern}) {
				return pattern
			}
		}
	}
	return ""
}

// DenyRefusal is loop's formatDenyRefusal: the user's settled decision, not
// an error, with equivalents ruled out so the model redirects.
func DenyRefusal(pattern string) string {
	return "`" + pattern + "` is blocked by the user's bash policy — intentional, not an error. " +
		"Don't retry it or run an equivalent (different flags, full path, piping, a script). " +
		"If it's truly required, ask the user to run it or to remove it from their denylist; otherwise continue without it."
}

// bashSubjects is every form a deny or ask rule is matched against: the
// whole line, each command in it, and each with its setup prefix peeled.
func bashSubjects(command string) []string {
	subjects := []string{command}
	for _, segment := range splitSegments(command) {
		subjects = append(subjects, segment)
		if peeled := strings.Join(peelWrappers(strings.Fields(segment)), " "); peeled != "" && peeled != segment {
			subjects = append(subjects, peeled)
		}
	}
	return subjects
}

// resolveAll is every command in the line, setup prefixes peeled.
func resolveAll(command string) []resolvedCommand {
	var out []resolvedCommand
	for _, segment := range splitSegments(command) {
		if c, ok := resolveSegment(segment); ok {
			out = append(out, c)
		}
	}
	return out
}

// IsCommandAllowed is loop's isCommandAllowed: every command in the line
// matches some remembered pattern. Fails closed on an empty list or a line
// with no command in it.
func IsCommandAllowed(command string, allowlist []string) bool {
	resolved := resolveAll(command)
	if len(allowlist) == 0 || len(resolved) == 0 {
		return false
	}
	for _, c := range resolved {
		if !c.matchesAny(allowlist) {
			return false
		}
	}
	return true
}

// dangerousCommands prompt even when a remembered grant covers them — a
// saved `git` grant must not silently approve `git push`. An explicit allow
// RULE still approves them: that is a written decision.
var dangerousCommands = []string{"rm", "chmod", "chown", "chgrp", "chattr", "pkill", "kill", "killall", "git push"}

// IsDangerousCommand reports any command in the line on that list.
func IsDangerousCommand(command string) bool {
	for _, c := range resolveAll(command) {
		if c.matchesAny(dangerousCommands) {
			return true
		}
	}
	return false
}

func (c resolvedCommand) matchesAny(patterns []string) bool {
	for _, pattern := range patterns {
		if strings.TrimSpace(pattern) != "" && c.matches(pattern) {
			return true
		}
	}
	return false
}
