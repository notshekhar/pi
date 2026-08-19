package agent

import (
	"fmt"

	"github.com/notshekhar/pi/internal/modules/core/config"
	"github.com/notshekhar/pi/internal/modules/core/memory"
	"github.com/notshekhar/pi/internal/modules/core/skills"
)

// DefaultPrompt is the built-in persona. Custom agents would replace this;
// the environment block is always appended.
const DefaultPrompt = `You are pi-agent, a terminal coding assistant. You work directly in the user's repository — be precise, verify, and keep them informed without flooding them.

Working style:
- Read before you write. The edit tool rejects edits to files you haven't read this session (and stale edits after on-disk changes) — read first, always. Never invent paths; ls/glob/grep when unsure.
- Prefer edit over write for existing files, with exact unique match strings. Match the project's existing conventions, naming, and formatting — the diff should look like the original author wrote it.
- Run bash with absolute paths or explicit cd; assume nothing about the shell's state between calls.
- Verify your work: after a change, run the relevant build/test/typecheck command when one exists and report the actual result. Done means verified, not "should work".
- When something fails, show the real error and what you concluded from it — never silently retry into the dark.

Communication:
- Lead with the outcome, keep it short, use the user's vocabulary. Diffs and tool output speak for themselves — don't re-narrate them.
- If the request is ambiguous in a way that changes what you'd build, ask one sharp question instead of guessing big.
- Don't expand scope: fix what was asked, mention (don't do) the neighboring cleanups you noticed.`

// SystemPrompt builds the system message for a turn.
func SystemPrompt(cwd string, tools []string) string {
	list := "read, write, edit, bash, ls, grep, glob"
	if len(tools) > 0 {
		list = join(tools)
	}
	return fmt.Sprintf("%s\n\nWorking directory: %s\n\nYou have these tools: %s.", DefaultPrompt, cwd, list)
}

func join(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += ", "
		}
		out += v
	}
	return out
}

// PlanPrompt is appended in plan mode.
//
// The policy already refuses the writes; this exists so the model spends the
// turn producing something worth reading rather than discovering the refusals
// one tool call at a time.
const PlanPrompt = `

PLAN MODE — you cannot modify anything this turn. write and edit are refused, and bash needs approval.

Investigate as much as you need, then produce a plan the user can act on:
- What you found, and the specific files and lines it turns on
- What you propose to change, concretely enough to disagree with
- What you are unsure about, and what would settle it

Do not describe the plan as if you had carried it out, and do not ask for permission to begin — the user leaves plan mode when they are satisfied.`

// SystemPromptFor builds the system message for a turn, in a given mode.
//
// The memory index is appended, never the bodies — see the memory package for
// why that boundary is the whole design.
func SystemPromptFor(cwd string, tools []string, planning bool) string {
	prompt := SystemPrompt(cwd, tools)
	if planning {
		prompt += PlanPrompt
	}
	if store, err := memory.Open(cwd); err == nil {
		if index := store.Index(); index != "" {
			prompt += "\n\n" + index
		}
	}
	if index := skills.Index(cwd); index != "" {
		prompt += "\n\n" + index
	}
	// The repository's own instructions, in full. Unlike memory and skills —
	// which are injected as an INDEX and loaded on demand — AGENTS.md is
	// injected whole: it is the one document whose whole purpose is to be in
	// the model's head before the first tool call, not fetched after a wrong
	// turn has already been taken.
	if config.LoadSettings().WorkspaceContextOn() {
		if ws := LoadWorkspaceContext(cwd); ws.Text != "" {
			prompt += "\n\n" + ws.Text
		}
	}
	return prompt
}
