package extension

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// wayfinder orients you in a codebase you have not seen before.
//
// A COMMAND rather than a persona, deliberately: the question "how does this
// repo work" is asked once, at the start, and a persona would keep answering
// it for the rest of the session.
type wayfinder struct{}

func init() { Register(wayfinder{}) }

func (wayfinder) Name() string { return "wayfinder" }
func (wayfinder) About() string {
	return "orient in an unfamiliar codebase — /wayfinder [what you are looking for]"
}

func (w wayfinder) Commands() []Command {
	return []Command{{
		Name:  "wayfinder",
		About: "Map an unfamiliar codebase — /wayfinder [what you are looking for]",
		Run: func(_ context.Context, cwd, rest string) (string, string, error) {
			// The survey is gathered HERE rather than asked of the model,
			// because the model would spend three tool calls discovering
			// what one directory read already knows — and would sometimes
			// guess instead.
			survey := surveyRepo(cwd)
			goal := strings.TrimSpace(rest)
			if goal == "" {
				goal = "Give me the map: what this project is, how it is laid out, where the entry points are, and where I should start reading."
			}
			prompt := fmt.Sprintf(`You are orienting someone in a codebase they have not seen.

%s

Their question: %s

Read the files that matter before answering — do not guess from names alone.
Answer with: what this project IS, the handful of directories that matter and
what each is for, the entry point, and the first three files to read in order.
Be specific about paths. Skip anything you did not verify.`, survey, goal)
			return "", prompt, nil
		},
	}}
}

// surveyRepo describes the top of a tree: the manifest, the readme, and the
// directories, which is what a person glances at first.
func surveyRepo(cwd string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Working directory: %s\n", cwd)

	entries, err := os.ReadDir(cwd)
	if err != nil {
		return b.String()
	}
	var dirs, manifests []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if e.IsDir() {
			dirs = append(dirs, name+"/")
			continue
		}
		switch strings.ToLower(name) {
		case "go.mod", "package.json", "cargo.toml", "pyproject.toml", "makefile",
			"readme.md", "agents.md", "claude.md":
			manifests = append(manifests, name)
		}
	}
	if len(dirs) > 0 {
		fmt.Fprintf(&b, "Top-level directories: %s\n", strings.Join(dirs, " "))
	}
	if len(manifests) > 0 {
		fmt.Fprintf(&b, "Manifests and docs present: %s\n", strings.Join(manifests, " "))
	}
	// The module line names the project better than the directory does.
	if data, err := os.ReadFile(filepath.Join(cwd, "go.mod")); err == nil {
		if line, _, ok := strings.Cut(string(data), "\n"); ok {
			fmt.Fprintf(&b, "%s\n", strings.TrimSpace(line))
		}
	}
	return b.String()
}
