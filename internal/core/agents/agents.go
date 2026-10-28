// Package agents is loop's custom agents (agent/agents.ts): named system
// prompts under ~/.loop/agents/<name>.md with optional `tools:` and `model:`
// frontmatter, plus the built-ins (default, plan, data-analyst). The files are
// loop's — an agent written in either program runs in both.
package agents

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/home"
)

const (
	DefaultAgentName     = "default"
	PlanAgentName        = "plan"
	DataAnalystAgentName = "data-analyst"
)

// ToolNames is loop's TOOL_NAMES.
var ToolNames = []string{"read", "write", "edit", "bash", "ls", "grep", "find", "sql", "shells"}

// AgentToolNames is loop's AGENT_TOOL_NAMES.
var AgentToolNames = append(append([]string{}, ToolNames...), "task", "ask", "websearch", "plan", "todo", "skill", "artifact")

// ExtraToolNames lets the extension layer add tool names an agent may list.
var ExtraToolNames func() []string

// sandboxSupported is loop's isSandboxSupported: Seatbelt / bubblewrap.
func sandboxSupported() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "linux" }

var readonlyTools = []string{"read", "ls", "grep", "find", "sql"}

func planTools() []string {
	tools := append([]string{}, readonlyTools...)
	if sandboxSupported() {
		return append(tools, "bash", "task", "plan", "skill")
	}
	return append(tools, "task", "plan", "skill")
}

type builtin struct {
	prompt string
	tools  []string
	hidden bool
}

func builtins() map[string]builtin {
	return map[string]builtin{
		DefaultAgentName:     {prompt: DefaultBasePrompt},
		PlanAgentName:        {prompt: PlanBasePrompt, tools: planTools()},
		DataAnalystAgentName: {prompt: AnalystBasePrompt, tools: append(append([]string{}, readonlyTools...), "task"), hidden: true},
	}
}

var builtinOrder = []string{DefaultAgentName, PlanAgentName, DataAnalystAgentName}

// Info is loop's AgentInfo.
type Info struct {
	Name    string   `json:"name"`
	Prompt  string   `json:"prompt"`
	Builtin bool     `json:"builtin"`
	Tools   []string `json:"tools,omitempty"`
	Model   string   `json:"model,omitempty"`
	Hidden  bool     `json:"hidden,omitempty"`
}

// Dir is ~/.loop/agents.
func Dir() string { return home.Path("agents") }

func path(name string) string { return filepath.Join(Dir(), name+".md") }

var validName = regexp.MustCompile(`(?i)^[a-z0-9][a-z0-9_-]{0,31}$`)

// IsValidName is loop's isValidAgentName.
func IsValidName(name string) bool { return validName.MatchString(name) }

// ResolveSaved is loop's resolveSavedAgent: plan is never restored.
func ResolveSaved(saved string) string {
	if saved == PlanAgentName {
		return ""
	}
	return saved
}

// ValidToolNames is loop's agentToolNames.
func ValidToolNames() []string {
	out := append([]string{}, AgentToolNames...)
	if ExtraToolNames != nil {
		out = append(out, ExtraToolNames()...)
	}
	return out
}

var optionalForExhaustive = map[string]bool{"artifact": true, "shells": true}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// sanitizeTools drops unknown names; a list naming every tool means "all".
func sanitizeTools(tools []string, valid []string) []string {
	if tools == nil {
		return nil
	}
	var kept []string
	for _, t := range tools {
		if contains(valid, t) {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	for _, t := range valid {
		if optionalForExhaustive[t] {
			continue
		}
		if !contains(kept, t) {
			return kept
		}
	}
	return nil
}

var (
	frontmatter = regexp.MustCompile(`^---\n([\s\S]*?)\n---\n?`)
	toolsLine   = regexp.MustCompile(`(?m)^tools:\s*(.+)$`)
	modelLine   = regexp.MustCompile(`(?m)^model:\s*(.+)$`)
)

// File is a parsed agent file.
type File struct {
	Prompt string
	Tools  []string
	Model  string
}

// ParseFile is loop's parseAgentFile.
func ParseFile(raw string) File {
	m := frontmatter.FindStringSubmatchIndex(raw)
	if m == nil {
		return File{Prompt: strings.TrimSpace(raw)}
	}
	fm := raw[m[2]:m[3]]
	out := File{Prompt: strings.TrimSpace(raw[m[1]:])}
	if t := toolsLine.FindStringSubmatch(fm); t != nil {
		var tools []string
		for _, s := range strings.Split(t[1], ",") {
			tools = append(tools, strings.TrimSpace(s))
		}
		out.Tools = sanitizeTools(tools, ValidToolNames())
	}
	if mm := modelLine.FindStringSubmatch(fm); mm != nil {
		out.Model = strings.TrimSpace(mm[1])
	}
	return out
}

func readFile(name string) (File, bool) {
	raw, err := os.ReadFile(path(name))
	if err != nil {
		return File{}, false
	}
	f := ParseFile(string(raw))
	return f, f.Prompt != ""
}

// List is every agent: built-ins (prompt/model overridable), then files.
func List() []Info {
	b := builtins()
	var out []Info
	for _, name := range builtinOrder {
		bi := b[name]
		info := Info{Name: name, Prompt: bi.prompt, Builtin: true, Tools: bi.tools, Hidden: bi.hidden}
		if f, ok := readFile(name); ok {
			info.Prompt, info.Model = f.Prompt, f.Model
		}
		out = append(out, info)
	}
	entries, err := os.ReadDir(Dir())
	if err == nil {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		sort.Strings(names)
		for _, f := range names {
			if !strings.HasSuffix(f, ".md") {
				continue
			}
			name := strings.TrimSuffix(f, ".md")
			if _, isBuiltin := b[name]; isBuiltin || !IsValidName(name) {
				continue
			}
			if parsed, ok := readFile(name); ok {
				out = append(out, Info{Name: name, Prompt: parsed.Prompt, Tools: parsed.Tools, Model: parsed.Model})
			}
		}
	}
	return out
}

// Prompt is an agent's prompt, "" when unknown.
func Prompt(name string) string {
	if f, ok := readFile(name); ok {
		return f.Prompt
	}
	return builtins()[name].prompt
}

// Model is the model an agent runs on as a subagent, "" = inherit.
func Model(name string) string {
	f, _ := readFile(name)
	return f.Model
}

// Tools is an agent's allowed tools; nil = all.
func Tools(name string) []string {
	if b, ok := builtins()[name]; ok {
		return b.tools
	}
	f, _ := readFile(name)
	return f.Tools
}

// Exists reports a known agent.
func Exists(name string) bool {
	if _, ok := builtins()[name]; ok {
		return true
	}
	_, err := os.Stat(path(name))
	return err == nil
}

// IsReadOnlyBash is loop's isReadOnlyBashAgent.
func IsReadOnlyBash(tools []string) bool {
	return tools != nil && contains(tools, "bash") && !contains(tools, "write") && !contains(tools, "edit")
}

// IsBuiltin reports a built-in.
func IsBuiltin(name string) bool { _, ok := builtins()[name]; return ok }

// IsHidden reports a built-in kept out of the Tab cycle.
func IsHidden(name string) bool { return builtins()[name].hidden }

// HasBuiltinOverride reports a built-in with a prompt override file.
func HasBuiltinOverride(name string) bool {
	if !IsBuiltin(name) {
		return false
	}
	_, err := os.Stat(path(name))
	return err == nil
}

// Save writes an agent file in loop's format.
func Save(name, prompt string, tools []string, model string) error {
	if !IsValidName(name) {
		return fmt.Errorf("invalid agent name: %s", name)
	}
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	var effective []string
	if !IsBuiltin(name) {
		effective = sanitizeTools(tools, ValidToolNames())
	}
	var fm []string
	if effective != nil {
		fm = append(fm, "tools: "+strings.Join(effective, ", "))
	}
	if m := strings.TrimSpace(model); m != "" {
		fm = append(fm, "model: "+m)
	}
	head := ""
	if len(fm) > 0 {
		head = "---\n" + strings.Join(fm, "\n") + "\n---\n\n"
	}
	return os.WriteFile(path(name), []byte(head+strings.TrimSpace(prompt)+"\n"), 0o644)
}

// Delete removes the file; for a built-in this resets its prompt.
func Delete(name string) (bool, error) {
	err := os.Remove(path(name))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}
