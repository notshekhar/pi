package main

import (
	"context"
	"fmt"
	"github.com/notshekhar/pi/internal/core/agents"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/skills"
	"github.com/notshekhar/pi/internal/tui"
)

// Commands that are not in the table.
//
// loop's registry is not only the static list — three more sources register
// commands at startup, and each exists because the alternative is a lookup
// step the user should not have to take:
//
//	/skill:<name>   run an installed skill without first asking what its
//	                exact name was
//	/<agent>        speak one message as an agent, without switching the
//	                session over to it and back
//	/<prompt>       a markdown file in the prompts directory, so a prompt
//	                you retype weekly becomes a command
//
// They are resolved fresh on every dispatch rather than snapshotted at boot:
// a skill you just wrote must be callable without restarting, which is the
// whole reason `/reload` was not enough on its own.

// promptsDirName is where custom-prompt commands are read from, under the
// config directory.
const promptsDirName = "prompts"

// dynamicCommands is every command that comes from the filesystem or the
// agent list, in the order `/help` and the palette show them.
func (t *repl) dynamicCommands() []command {
	var out []command
	out = append(out, t.skillCommands()...)
	out = append(out, agentCommands()...)
	out = append(out, promptCommands()...)
	return out
}

// lookupDynamic finds one, or nil. Name may carry the leading slash.
func (t *repl) lookupDynamic(name string) *command {
	name = strings.TrimPrefix(name, "/")
	table := t.dynamicCommands()
	for i := range table {
		if table[i].name == name {
			return &table[i]
		}
	}
	return nil
}

// skillCommands exposes each installed skill as `/skill:<name>`.
func (t *repl) skillCommands() []command {
	found := skills.Load(t.cfg.CWD)
	out := make([]command, 0, len(found))
	for _, s := range found {
		skill := s
		out = append(out, command{
			name:        "skill:" + skill.Name,
			description: elide(skill.Description, 100),
			run: func(t *repl, ctx context.Context, rest string) {
				t.runSkill(ctx, skill, rest)
			},
		})
	}
	return out
}

// runSkill submits the skill's instructions as a turn, with any argument
// appended.
func (t *repl) runSkill(ctx context.Context, skill skills.Skill, rest string) {
	t.submit(ctx, skill.Invocation(rest))
}

// agentCommands is loop's registerAgentCommand per agent: `/<name>
// <message>` runs one message under that agent ("default" gets none — plain
// messages already use it), plus `/data` for the data-analyst.
func agentCommands() []command {
	var out []command
	add := func(name, cmdName, desc string) {
		// A name that collides with a real command is skipped, not shadowed:
		// an agent must never take over `/model`.
		if lookupCommand(cmdName) != nil {
			return
		}
		out = append(out, command{name: cmdName, description: desc,
			run: func(t *repl, ctx context.Context, rest string) { t.useAgent(ctx, name, rest) }})
	}
	for _, a := range agents.List() {
		if a.Name == agents.DefaultAgentName || a.Hidden {
			continue
		}
		add(a.Name, a.Name, fmt.Sprintf("Run one message with the %s agent: /%s <message>", a.Name, a.Name))
	}
	if !hasCommand(out, "data") {
		add(agents.DataAnalystAgentName, "data", "Run one message with the data-analyst agent: /data <message>")
	}
	return out
}

func hasCommand(list []command, name string) bool {
	for _, c := range list {
		if c.name == name {
			return true
		}
	}
	return false
}

// borrowedAgent is the session's own agent while a one-shot borrows the turn.
type borrowedAgent struct {
	persona string
	tools   []string
}

// useAgent is loop's: `/<agent> <message>` runs that message under the
// agent's prompt and tools; the session's selected agent is untouched.
func (t *repl) useAgent(ctx context.Context, name, message string) {
	if !agents.Exists(name) {
		t.fail("unknown agent: %s — /agents to create one", name)
		return
	}
	if strings.TrimSpace(message) == "" {
		t.dim("usage: /%s <message> — one message with this agent. Session switch: /agents", name)
		return
	}
	// Put back by startTurn's cleanup: the turn runs on its own goroutine.
	t.restorePersona = &borrowedAgent{t.run.Persona, t.run.AllowedTools}
	t.run.Persona = agents.Prompt(name)
	if name == agents.DefaultAgentName {
		t.run.Persona = ""
	}
	t.run.AllowedTools = agents.Tools(name)
	t.dim("agent for this message: %s", name)
	t.submit(ctx, message)
}

// submit runs text as a turn, exactly as though it had been typed.
func (t *repl) submit(ctx context.Context, text string) {
	t.do(func() { t.app.UserEcho(text) })
	t.startTurn(ctx, text)
}

// promptCommands turns each markdown file under the prompts directory into a
// command that submits its contents.
func promptCommands() []command {
	dir := home.AgentDir("prompts")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		names = append(names, e.Name())
	}
	// Directory order is filesystem order, which differs between machines and
	// would reshuffle `/help` for no reason.
	sort.Strings(names)

	out := make([]command, 0, len(names))
	for _, file := range names {
		name := strings.TrimSuffix(file, ".md")
		if lookupCommand(name) != nil {
			continue
		}
		path := filepath.Join(dir, file)
		out = append(out, command{
			name:        name,
			description: "Custom prompt from " + file,
			run: func(t *repl, ctx context.Context, rest string) {
				body, err := os.ReadFile(path)
				if err != nil {
					t.fail("%s: %s", file, err)
					return
				}
				text := strings.TrimSpace(string(body))
				if rest = strings.TrimSpace(rest); rest != "" {
					text += "\n\n" + rest
				}
				t.submit(ctx, text)
			},
		})
	}
	return out
}

// commandItems is the whole completion catalog: the table, then the dynamic
// commands, then the extensions — dispatch order, so what the palette offers
// first is what typing the name would actually run.
func (t *repl) commandItems() []tui.Item {
	items := slashItems()
	for _, c := range t.dynamicCommands() {
		items = append(items, tui.Item{Value: "/" + c.name, Label: c.name, Description: c.description})
	}
	return items
}
