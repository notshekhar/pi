package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/skills"
)

type skillArgs struct {
	Name string `json:"name" jsonschema:"description=Which skill to load"`
}

// Skill returns the skill tool.
func Skill(t *Context) ai.Tool {
	return ai.NewTool("skill",
		"Load a skill's instructions. Call this BEFORE doing the kind of work the "+
			"skill covers — its instructions replace your default approach for that "+
			"work, so loading one afterwards is too late to be useful. The skills "+
			"available to you are listed in your context with a description of when "+
			"each applies.",
		func(ctx context.Context, a skillArgs) (ai.ToolResult, error) {
			if ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			name := strings.TrimSpace(a.Name)
			if name == "" {
				return ai.ToolError("name is empty"), nil
			}

			skill, err := skills.Find(t.CWD, name)
			if err != nil {
				available := skills.Load(t.CWD)
				if len(available) == 0 {
					return ai.ToolError("no skills are installed"), nil
				}
				names := make([]string, 0, len(available))
				for _, s := range available {
					names = append(names, s.Name)
				}
				return ai.ToolErrorf("%v; available: %s", err, strings.Join(names, ", ")), nil
			}

			// The directory is included because a skill's instructions
			// routinely reference its own files, and the model needs a path to
			// read them from.
			return ai.ToolText(fmt.Sprintf("%s\n\n---\nSkill directory: %s",
				skill.Body, skill.Dir)), nil
		})
}
