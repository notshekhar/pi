package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/core/agent"
	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/skills"
	"github.com/notshekhar/pi/internal/modules/tui"
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
//
// The body goes in wrapped in a <skill> element naming its directory: a skill
// routinely refers to files beside it ("see references/palette.md"), and
// without the location those paths resolve against the wrong directory.
func (t *repl) runSkill(ctx context.Context, skill skills.Skill, rest string) {
	block := fmt.Sprintf("<skill name=%q location=%q>\nReferences are relative to %s.\n\n%s\n</skill>",
		skill.Name, filepath.Join(skill.Dir, skills.FileName), skill.Dir, skill.Body)
	text := block
	if rest = strings.TrimSpace(rest); rest != "" {
		text += "\n\n" + rest
	}
	t.submit(ctx, text)
}

// agentCommands exposes each agent as `/<name> <message>` — one message under
// that agent's prompt, without switching the session.
func agentCommands() []command {
	out := make([]command, 0, len(agent.TaskAgents))
	for _, a := range agent.TaskAgents {
		persona := a
		// A name that collides with a real command is skipped, not shadowed:
		// an agent must never take over `/model`.
		if lookupCommand(persona.Name) != nil {
			continue
		}
		out = append(out, command{
			name:        persona.Name,
			description: fmt.Sprintf("Run one message with agent %q: /%s <message>", persona.Name, persona.Name),
			run: func(t *repl, ctx context.Context, rest string) {
				t.runAsAgent(ctx, persona, rest)
			},
		})
	}
	return out
}

// runAsAgent runs a single turn under a persona and restores the session's
// own agent afterwards.
func (t *repl) runAsAgent(ctx context.Context, persona agent.TaskAgent, rest string) {
	if strings.TrimSpace(rest) == "" {
		t.dim("usage: /%s <message>", persona.Name)
		return
	}
	// The persona is put back by startTurn's cleanup, not here: the turn runs
	// on its own goroutine, so restoring on the way out of this function
	// would undo the switch before the model was ever asked.
	previous := t.run.Persona
	t.restorePersona = &previous
	t.run.Persona = persona.Prompt
	t.submit(ctx, rest)
}

// submit runs text as a turn, exactly as though it had been typed.
func (t *repl) submit(ctx context.Context, text string) {
	t.app.Do(func() { t.app.UserEcho(text) })
	t.startTurn(ctx, text)
}

// promptCommands turns each markdown file under the prompts directory into a
// command that submits its contents.
func promptCommands() []command {
	dir, err := config.Dir()
	if err != nil {
		return nil
	}
	dir = filepath.Join(dir, promptsDirName)
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
