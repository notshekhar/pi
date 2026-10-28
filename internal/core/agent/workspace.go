package agent

import (
	"os"
	"path/filepath"

	"github.com/notshekhar/pi/internal/core/home"
)

// Workspace context: the repository's own instructions to the agent.
//
// AGENTS.md is how a project states its build commands, its conventions, and
// the traps a newcomer falls into. It was being ADVERTISED but never injected
// — the startup banner listed the files it had found and a settings key
// claimed to control them, while the system prompt saw none of it. A setting
// that does nothing is worse than a missing one, because it makes the user
// believe the agent has read something it has not.

// contextFileNames are the instruction files, in the order they are read.
//
// Both spellings, because a repository shared between agents has whichever
// one the other tool wrote, and refusing to read CLAUDE.md would mean the
// agent ignores instructions that are plainly there.
var contextFileNames = []string{
	"AGENTS.md",
	"CLAUDE.md",
	filepath.Join(home.DirName, "AGENTS.md"),
	filepath.Join(home.DirName, "CLAUDE.md"),
}

// maxContextFileBytes caps one file. A generated AGENTS.md can run to
// megabytes, and the whole window is not the project's to spend.
const maxContextFileBytes = 64 * 1024

// WorkspaceContext is the injected text plus the files it came from.
type WorkspaceContext struct {
	Text  string
	Files []string
}

// RepoRoot walks up from start looking for a .git directory, returning start
// when there is none.
func RepoRoot(start string) string {
	dir := start
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return start
		}
		dir = parent
	}
}

// LoadWorkspaceContext reads the instruction files that apply to cwd.
//
// Global files first, then the repository root, then every directory down to
// cwd. The order is the precedence: later text is read after earlier text, so
// a directory's instructions refine the repository's rather than being buried
// under them.
func LoadWorkspaceContext(cwd string) WorkspaceContext {
	root := RepoRoot(cwd)

	// loop's order: root, cwd, then each directory from cwd's parent up to
	// root (a Set, so duplicates collapse to their first position).
	dirs := []string{root}
	if cwd != root {
		dirs = append(dirs, cwd)
	}
	for dir := filepath.Dir(cwd); cwd != root && dir != root && filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
	}

	// Global files first so they read as base rules.
	candidates := []string{home.Path("AGENTS.md"), home.Path("CLAUDE.md")}
	for _, dir := range dirs {
		for _, name := range contextFileNames {
			candidates = append(candidates, filepath.Join(dir, name))
		}
	}

	var out WorkspaceContext
	var blocks []string
	seen := map[string]bool{}
	for _, path := range candidates {
		if seen[path] {
			continue
		}
		seen[path] = true
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		body, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := string(body)
		if len(body) > maxContextFileBytes {
			text = string(body[:maxContextFileBytes]) + "\n...[truncated]"
		}
		// Tagged with its path, so the model can tell a repository rule from
		// a global one and can cite where a convention came from.
		blocks = append(blocks, "<file path=\""+path+"\">\n"+text+"\n</file>")
		out.Files = append(out.Files, path)
	}
	if len(blocks) == 0 {
		return WorkspaceContext{}
	}
	out.Text = "<workspace-context>\n" + join2(blocks, "\n") + "\n</workspace-context>"
	return out
}

func join2(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}
