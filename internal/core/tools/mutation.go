package tools

import (
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/ai"
)

// What edit and write share: plan mode's refusal, one-at-a-time access to a
// file, and a result whose diff is for the user rather than the model.

// planModeRefusal is what edit and write tell the model while plan mode is on.
const planModeRefusal = "Plan mode is active — file edits are disabled in every permission mode until the user approves a plan. " +
	"Keep investigating with the read-only tools and deliver the plan with the plan tool; " +
	"implementation starts after the user accepts it."

// diffSeparator splits a mutation result: the head before it is what the
// model is told, the diff after it is for human eyes only. A diff never
// contains a blank line — every line it emits starts with a marker and a
// line number — so the first blank line is always the cut.
const diffSeparator = "\n\n"

// mutationResult is a file-mutation result. The model already supplied the
// content as the call's own argument, so a diff sent back is that content a
// second time, kept for the rest of the conversation. The model gets the one
// line it needs; the UI gets the whole thing.
func mutationResult(output string) ai.ToolResult {
	head, _, hasDiff := strings.Cut(output, diffSeparator)
	if !hasDiff {
		return ai.ToolText(output)
	}
	return ai.ToolText(head).WithDisplay(output)
}

var fileLocks sync.Map // absolute path → *sync.Mutex

// withFileLock runs fn while holding the path's lock, so two tool calls in
// one step cannot interleave their read-modify-write of the same file.
func withFileLock[T any](path string, fn func() (T, error)) (T, error) {
	lock, _ := fileLocks.LoadOrStore(path, &sync.Mutex{})
	mu := lock.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	return fn()
}
