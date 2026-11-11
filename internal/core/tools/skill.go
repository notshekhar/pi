package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/skills"
)

type skillArgs struct {
	Name string `json:"name"`
}

// Skill returns the skill tool. A wrong name comes back as text, not an
// error: the real list is the best possible correction.
func Skill(t *Context) ai.Tool {
	return LoopTool("skill", func(_ context.Context, a skillArgs) (ai.ToolResult, error) {
		return ai.ToolText(loadSkill(invocableSkills(t.CWD), a.Name)), nil
	})
}

// invocableSkills is the skills the model may load: every one that has not
// opted out of model invocation.
func invocableSkills(cwd string) []skills.Skill {
	var visible []skills.Skill
	for _, skill := range skills.Load(cwd) {
		if !skill.DisableModelInvocation {
			visible = append(visible, skill)
		}
	}
	return visible
}

func loadSkill(available []skills.Skill, name string) string {
	names := make([]string, len(available))
	for i, skill := range available {
		if skill.Name == name {
			return fmt.Sprintf("Skill %q (from %s).\nRelative paths in these instructions resolve against %s — use absolute paths in tool commands.\n\n%s",
				skill.Name, skill.FilePath, skill.Dir, strings.TrimSpace(skill.Body))
		}
		names[i] = skill.Name
	}
	list := strings.Join(names, ", ")
	if list == "" {
		list = "(none)"
	}
	return fmt.Sprintf("[unknown skill %q — available: %s]", name, list)
}
