package tools

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// editArgs accepts the shapes models actually send: edits as an array, edits
// as a JSON string holding that array (Opus 4.6, GLM-5.1), and the older flat
// oldText/newText pair.
type editArgs struct {
	Path    string         `json:"path"`
	Edits   jsontext.Value `json:"edits"`
	OldText *string        `json:"oldText"`
	NewText *string        `json:"newText"`
}

func (a editArgs) replacements() ([]Replacement, error) {
	var edits []Replacement
	raw := a.Edits
	var asString string
	if json.Unmarshal(raw, &asString, jsonopt.Read) == nil {
		raw = jsontext.Value(asString)
	}
	// A value that is not an array of edits leaves the list empty, and the
	// check below reports it.
	_ = json.Unmarshal(raw, &edits, jsonopt.Read)
	if a.OldText != nil && a.NewText != nil {
		edits = append(edits, Replacement{OldText: *a.OldText, NewText: *a.NewText})
	}
	if len(edits) == 0 {
		return nil, errors.New("Edit tool input is invalid. edits must contain at least one replacement.")
	}
	return edits, nil
}

// Edit returns the edit tool.
func Edit(t *Context) ai.Tool {
	return LoopTool("edit", func(ctx context.Context, a editArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		if t.planMode() {
			return fail(errors.New(planModeRefusal))
		}
		edits, err := a.replacements()
		if err != nil {
			return fail(err)
		}
		path := Resolve(a.Path, t.CWD)
		output, err := withFileLock(path, func() (string, error) { return editFile(t, path, a.Path, edits) })
		if err != nil {
			return fail(err)
		}
		return mutationResult(output), nil
	})
}

func editFile(t *Context, path, displayPath string, edits []Replacement) (string, error) {
	// Never modify a file the agent has not seen, or has only seen a stale
	// version of, this session.
	if msg := t.Registry.CheckEdit(path, displayPath); msg != "" {
		return "", errors.New(msg)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("Could not edit file: %s. Error code: %s.", displayPath, errorCode(err))
	}
	bom, text := stripBOM(string(raw))
	ending := detectLineEnding(text)
	applied, err := applyEdits(normalizeToLF(text), edits, displayPath)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(bom+restoreLineEndings(applied.next, ending)), 0o644); err != nil {
		return "", fmt.Errorf("Could not edit file: %s. Error code: %s.", displayPath, errorCode(err))
	}
	t.Registry.RecordModified(path)
	return editSummary(len(edits), displayPath, applied), nil
}

func editSummary(count int, displayPath string, applied appliedEdits) string {
	summary := fmt.Sprintf("Successfully replaced %d block(s) in %s.", count, displayPath)
	diff := generateDiff(applied.base, applied.next)
	if len(applied.fuzzyIndexes) == 0 {
		return summary + diffSeparator + diff
	}
	// What got replaced is not what was asked for, so the model is shown the
	// diff: joined with single newlines, the whole result stays on the
	// model's side of the cut.
	which := make([]string, len(applied.fuzzyIndexes))
	for i, index := range applied.fuzzyIndexes {
		which[i] = fmt.Sprintf("edits[%d]", index)
	}
	return strings.Join([]string{
		summary,
		strings.Join(which, ", ") + " matched text that differs from the oldText you sent (whitespace or unicode normalization), so the file does not read exactly the way you assumed. What actually changed:",
		diff,
	}, "\n")
}

// errorCode names a file error the way Node does, which is what loop reports.
func errorCode(err error) string {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return "ENOENT"
	case errors.Is(err, os.ErrPermission):
		return "EACCES"
	}
	return err.Error()
}
