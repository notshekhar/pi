// Package skills loads packaged instruction sets the agent can pull in on
// demand.
//
// A skill is a directory holding a SKILL.md — frontmatter naming it and
// describing when it applies, then the instructions themselves. Supporting
// files sit alongside and are read with the ordinary read tool, because a
// skill is just a directory and needs no special access.
//
// Same economics as `memory`, for the same reason: only the INDEX is injected
// on every turn. A skill's whole point is to be long — a checklist, a
// procedure, a house style — and injecting every one of them would spend the
// context window on instructions for work the turn is not doing.
//
// Two roots, and a project skill beats a personal one of the same name. A
// repository that ships a skill is making a statement about how work in it
// should be done, and that should win over a preference.
package skills

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Skill is one packaged instruction set.
type Skill struct {
	Name        string
	Description string
	// Body is the instructions. Not injected — loaded when invoked.
	Body string
	// Dir is the skill's directory, so its supporting files can be found.
	Dir string
	// Project marks a skill that came from the repository rather than the
	// user's own collection.
	Project bool
}

// FileName is the manifest every skill directory must contain.
const FileName = "SKILL.md"

// UserDir is ~/.pi-agent/skills.
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".pi-agent", "skills"), nil
}

// ProjectDir is <cwd>/.pi-agent/skills.
func ProjectDir(cwd string) string {
	return filepath.Join(cwd, ".pi-agent", "skills")
}

// Load returns every available skill, sorted by name.
//
// A malformed skill is skipped rather than fatal: one bad directory must not
// hide the rest, and the user will notice a missing skill far sooner than
// they would notice a broken session.
func Load(cwd string) []Skill {
	found := map[string]Skill{}

	if dir, err := UserDir(); err == nil {
		for _, s := range scan(dir, false) {
			found[s.Name] = s
		}
	}
	// Second, so a project skill overwrites a personal one of the same name.
	for _, s := range scan(ProjectDir(cwd), true) {
		found[s.Name] = s
	}

	out := make([]Skill, 0, len(found))
	for _, s := range found {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Find looks up one skill by name.
func Find(cwd, name string) (Skill, error) {
	for _, s := range Load(cwd) {
		if strings.EqualFold(s.Name, name) {
			return s, nil
		}
	}
	return Skill{}, fmt.Errorf("no skill named %q", name)
}

// scan reads every skill directory under root.
func scan(root string, project bool) []Skill {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []Skill
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		data, err := os.ReadFile(filepath.Join(dir, FileName))
		if err != nil {
			continue
		}
		s := parse(e.Name(), string(data))
		// A skill with no description cannot be indexed, and a skill the
		// model cannot decide to use is not a skill.
		if s.Description == "" || s.Body == "" {
			continue
		}
		s.Dir = dir
		s.Project = project
		out = append(out, s)
	}
	return out
}

var reFrontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)

// parse splits frontmatter from instructions. The directory name is the
// fallback identity, so a manifest that omits `name` still works.
func parse(dirName, content string) Skill {
	s := Skill{Name: dirName, Body: strings.TrimSpace(content)}
	m := reFrontmatter.FindStringSubmatch(content)
	if m == nil {
		return s
	}
	s.Body = strings.TrimSpace(content[len(m[0]):])
	for _, line := range strings.Split(m[1], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "name":
			if value != "" {
				s.Name = value
			}
		case "description":
			s.Description = oneLine(value)
		}
	}
	return s
}

// Index is the block injected into the system prompt: names and descriptions
// only. Empty when there are no skills.
func Index(cwd string) string {
	all := Load(cwd)
	if len(all) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Skills available to you. Each is a set of instructions for a " +
		"particular kind of work — load one with the skill tool BEFORE starting " +
		"that work, not after. Descriptions say when each applies.\n")
	for _, s := range all {
		fmt.Fprintf(&b, "\n- %s — %s", s.Name, s.Description)
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
