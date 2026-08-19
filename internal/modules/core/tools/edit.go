package tools

import (
	"context"
	"os"

	"github.com/notshekhar/pi/internal/modules/ai"
)

type editItem struct {
	OldText string `json:"oldText" jsonschema:"description=Exact text to replace; must be unique in the original file"`
	NewText string `json:"newText" jsonschema:"description=Replacement text"`
}

type editArgs struct {
	Path  string     `json:"path" jsonschema:"description=Path to the file to edit"`
	Edits []editItem `json:"edits" jsonschema:"description=Replacements matched against the original file, not incrementally"`
}

// Edit returns the edit tool.
func Edit(t *Context) ai.Tool {
	return ai.NewTool("edit",
		"Edit a file by applying one or more targeted text replacements. Each replacement matches the ORIGINAL file (not incrementally). oldText must match uniquely. Do not emit overlapping edits. Read the file first.",
		func(ctx context.Context, a editArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			if len(a.Edits) == 0 {
				return ai.ToolError("edits must contain at least one replacement"), nil
			}
			path := Resolve(a.Path, t.CWD)
			if msg := t.Registry.CheckEdit(path, a.Path); msg != "" {
				return ai.ToolError(msg), nil
			}

			raw, err := os.ReadFile(path)
			if err != nil {
				return ai.ToolErrorf("Could not edit file: %s. %v", a.Path, err), nil
			}
			bom, text := stripBOM(string(raw))
			ending := detectLineEnding(text)
			normalized := normalizeToLF(text)

			edits := make([]Replacement, len(a.Edits))
			for i, e := range a.Edits {
				edits[i] = Replacement{OldText: e.OldText, NewText: e.NewText}
			}
			base, next, err := applyEdits(normalized, edits, a.Path)
			if err != nil {
				return ai.ToolError(err.Error()), nil
			}

			final := bom + restoreLineEndings(next, ending)
			if err := os.WriteFile(path, []byte(final), 0o644); err != nil {
				return ai.ToolErrorf("could not write %s: %v", a.Path, err), nil
			}
			t.Registry.RecordModified(path)
			diff := generateDiff(base, next, 4)
			return ai.ToolTextf("Successfully replaced %d block(s) in %s.\n\n%s", len(edits), a.Path, diff), nil
		})
}
