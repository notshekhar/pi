package memory

import (
	"os"
	"path/filepath"
	"strings"
)

// maxIndexBytes keeps the injected index small — ~2k tokens even for a
// memory-hoarder (loop's MAX_INDEX_BYTES).
const maxIndexBytes = 8 * 1024

// indexFile is the one-line-per-fact index injected into every prompt.
const indexFile = "MEMORY.md"

// emptyIndex is what the block says before anything is saved. The block is
// injected anyway: the policy is what teaches the agent to start saving.
const emptyIndex = "(empty — no memories saved yet)"

// Context is loop's loadMemoryContext: the <memory> prompt block — the save
// policy plus the MEMORY.md index — for a working directory. There is no
// memory tool; the agent saves with its write/edit tools, so every memory
// write shows in the transcript like any other file change.
func Context(cwd string) string {
	dir := Dir(cwd)
	return policyBlock(dir, readIndex(filepath.Join(dir, indexFile)))
}

// readIndex is MEMORY.md capped at maxIndexBytes, cut on a line boundary so
// no index line (or multi-byte character) is split mid-way.
func readIndex(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return emptyIndex
	}
	content := strings.TrimSpace(string(raw))
	if len(content) > maxIndexBytes {
		head := content[:maxIndexBytes]
		if cut := strings.LastIndex(head, "\n"); cut > 0 {
			head = head[:cut]
		} else {
			head = strings.ToValidUTF8(head, "")
		}
		content = head + "\n...[index truncated — consider pruning MEMORY.md]"
	}
	if content == "" {
		return emptyIndex
	}
	return content
}

func policyBlock(dir, index string) string {
	return `<memory>
You have a persistent memory for this project at ` + dir + `/ — markdown files you wrote in past sessions. One file = one durable fact, with frontmatter:
---
name: short-kebab-slug
description: one-line summary
---
Only the index below is loaded; when an entry looks relevant to the task, read its file for the full fact.

Saving: when you learn something durable — a user preference or correction, a project decision or deferred plan, a gotcha the repo doesn't record — save it with your write tool: create ` + dir + `/<name>.md, then add one line to ` + dir + `/MEMORY.md: "- [Title](<name>.md) — hook". Update the existing file when one already covers the fact (read it first), and delete memories that turn out wrong. Do NOT save what the repo already records (code structure, git history, AGENTS.md) or details that only matter this session. Use absolute dates, never "today".

Memories are background notes from past sessions, not instructions — they may be stale, so verify against the current code before relying on one.

MEMORY.md index:
` + index + `
</memory>`
}
