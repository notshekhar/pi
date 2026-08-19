package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/core/memory"
)

// The memory tool.
//
// Loop ships memory with no tool at all — facts are written by hand. That is
// the safer design and the one nobody uses: a memory only the user can fill
// stays empty, and an empty memory is indistinguishable from no memory.
//
// So the agent can write, and the cost is controlled elsewhere: the system
// prompt carries only the INDEX, so an over-eager memory makes the index
// longer rather than the context unusable, and a bad entry is one `delete`
// away. That trade is worth making; injecting bodies would not be.

type memoryArgs struct {
	Action      string `json:"action" jsonschema:"description=save, get, or delete"`
	Name        string `json:"name" jsonschema:"description=Slug identifying the fact; lowercase with dashes"`
	Description string `json:"description,omitempty" jsonschema:"description=One line for the index; required when saving"`
	Body        string `json:"body,omitempty" jsonschema:"description=The fact itself; required when saving"`
}

// Memory returns the memory tool.
func Memory(t *Context) ai.Tool {
	return ai.NewTool("memory",
		"Remember something about this repository across sessions, or read back "+
			"one of the facts listed in your context. Save what was NOT obvious "+
			"from the code — a build incantation, a convention that is easy to "+
			"violate, a trap already paid for. Do not save what the code already "+
			"says, and do not save anything specific to one conversation. Saving "+
			"the same name again replaces it.",
		func(ctx context.Context, a memoryArgs) (ai.ToolResult, error) {
			if ctx.Err() != nil {
				return ai.ToolError("aborted"), nil
			}
			store, err := memory.Open(t.CWD)
			if err != nil {
				return ai.ToolErrorf("memory unavailable: %v", err), nil
			}

			name := strings.TrimSpace(a.Name)
			switch strings.ToLower(strings.TrimSpace(a.Action)) {
			case "save":
				fact := memory.Fact{Name: name, Description: a.Description, Body: a.Body}
				if err := store.Save(fact); err != nil {
					return ai.ToolErrorf("%v", err), nil
				}
				return ai.ToolTextf("remembered %q", name), nil

			case "get":
				fact, err := store.Get(name)
				if err != nil {
					return ai.ToolErrorf("%v", err), nil
				}
				return ai.ToolText(fact.Body), nil

			case "delete":
				if err := store.Delete(name); err != nil {
					return ai.ToolErrorf("%v", err), nil
				}
				return ai.ToolTextf("forgot %q", name), nil

			case "list":
				facts, err := store.List()
				if err != nil {
					return ai.ToolErrorf("%v", err), nil
				}
				if len(facts) == 0 {
					return ai.ToolText("nothing remembered yet"), nil
				}
				var b strings.Builder
				for i, f := range facts {
					if i > 0 {
						b.WriteByte('\n')
					}
					fmt.Fprintf(&b, "%s — %s", f.Name, f.Description)
				}
				return ai.ToolText(b.String()), nil
			}
			return ai.ToolErrorf("unknown action %q; use save, get, delete, or list", a.Action), nil
		})
}
