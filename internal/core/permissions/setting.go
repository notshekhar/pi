package permissions

import (
	"regexp"
	"strings"
)

// loop's settings.json `permissions` ({allow, ask, deny} of Claude-shape rule
// strings: "Bash(git *)", "Read(src/**)", "Read", "*") read into pi rules.
// Tool names map through loop's tool classes (TOOL_CLASS_BY_NAME): read also
// covers ls, edit covers write, grep covers glob and find.

var classTools = map[string][]string{
	"bash":      {"bash"},
	"read":      {"read", "ls"},
	"edit":      {"edit", "write"},
	"grep":      {"grep", "glob", "find"},
	"websearch": {"websearch"},
	"mcp":       {"*__*"},
	"*":         {"*"},
}

var toolClassByName = map[string]string{
	"bash": "bash", "read": "read", "notebookread": "read", "ls": "read",
	"edit": "edit", "write": "edit", "notebookedit": "edit",
	"grep": "grep", "glob": "grep", "find": "grep",
	"websearch": "websearch", "mcptool": "mcp", "mcp": "mcp", "*": "*",
}

var ruleString = regexp.MustCompile(`^\s*([A-Za-z*]+)\s*(?:\(([\s\S]*)\))?\s*$`)

// ParseRuleString reads one loop rule string; false when unrecognised.
func ParseRuleString(mode Mode, raw string) ([]Rule, bool) {
	m := ruleString.FindStringSubmatch(raw)
	if m == nil {
		return nil, false
	}
	class, ok := toolClassByName[strings.ToLower(m[1])]
	if !ok {
		return nil, false
	}
	pattern := strings.TrimSpace(m[2])
	if class == "bash" && pattern != "" {
		// Bash patterns are prefixes; a trailing ":*" reduces to one.
		pattern = strings.TrimSpace(strings.TrimSuffix(pattern, ":*"))
		if !strings.ContainsAny(pattern, "*?[") {
			pattern += "*"
		}
	}
	var out []Rule
	for _, tool := range classTools[class] {
		out = append(out, Rule{Tool: tool, Pattern: pattern, Mode: mode, Reason: "permission rule `" + strings.TrimSpace(raw) + "`"})
	}
	return out, true
}

// FromSetting turns loop's three lists into rules, skipping what it cannot
// read.
func FromSetting(allow, ask, deny []string) []Rule {
	var out []Rule
	for _, section := range []struct {
		mode Mode
		list []string
	}{{Deny, deny}, {Ask, ask}, {Allow, allow}} {
		for _, raw := range section.list {
			if rules, ok := ParseRuleString(section.mode, raw); ok {
				out = append(out, rules...)
			}
		}
	}
	return out
}

// RuleString renders a pi rule the way loop stores it ("Bash(git *)").
func RuleString(r Rule) string {
	name := r.Tool
	for class, tools := range classTools {
		if tools[0] == r.Tool {
			name = class
		}
	}
	if name == "*" {
		return "*"
	}
	name = strings.ToUpper(name[:1]) + name[1:]
	if name == "Websearch" {
		name = "WebSearch"
	}
	if r.Pattern == "" {
		return name
	}
	return name + "(" + r.Pattern + ")"
}
