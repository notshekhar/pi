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
	"strings"

	"github.com/notshekhar/pi/internal/core/home"
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
	// FilePath is the skill's file (SKILL.md or a root .md).
	FilePath string
	// DisableModelInvocation keeps it out of the prompt: /skill:name only.
	DisableModelInvocation bool
}

// FileName is the manifest every skill directory must contain.
const FileName = "SKILL.md"

// UserDir is loop's ~/.loop/agent/skills.
func UserDir() (string, error) { return home.AgentDir("skills"), nil }

// ProjectDir is loop's <cwd>/.loop/skills.
func ProjectDir(cwd string) string {
	return filepath.Join(cwd, home.DirName, "skills")
}

// Load is loop's loadProjectSkills (agent/skills.ts): ~/.loop/agent/skills
// first, then <cwd>/.loop/skills; on a name collision the FIRST found wins.
// Within a directory, one holding SKILL.md is a skill root (no recursion);
// otherwise its direct *.md files are skills (top level only) and its
// subdirectories are searched.
func Load(cwd string) []Skill {
	var out []Skill
	seen := map[string]bool{}
	add := func(list []Skill) {
		for _, s := range list {
			if !seen[s.Name] {
				seen[s.Name] = true
				out = append(out, s)
			}
		}
	}
	if dir, err := UserDir(); err == nil {
		add(scan(dir, true, false))
	}
	add(scan(ProjectDir(cwd), true, true))
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

func isFile(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.Mode().IsRegular()
}

// scan is loop's loadSkillsFromDir.
func scan(dir string, includeRootFiles, project bool) []Skill {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	if root := filepath.Join(dir, FileName); isFile(root) {
		if s, ok := loadFile(root, project); ok {
			return []Skill{s}
		}
		return nil
	}
	var out []Skill
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			continue
		}
		full := filepath.Join(dir, name)
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		if st.IsDir() {
			out = append(out, scan(full, false, project)...)
			continue
		}
		if !st.Mode().IsRegular() || !includeRootFiles || !strings.HasSuffix(name, ".md") {
			continue
		}
		if s, ok := loadFile(full, project); ok {
			out = append(out, s)
		}
	}
	return out
}

// parseFrontmatter is loop's minimal YAML subset.
func parseFrontmatter(content string) (map[string]any, string) {
	n := strings.ReplaceAll(content, "\r\n", "\n")
	out := map[string]any{}
	if !strings.HasPrefix(n, "---") {
		return out, n
	}
	end := strings.Index(n[3:], "\n---")
	if end == -1 {
		return out, n
	}
	end += 3
	for _, line := range strings.Split(n[min(4, end):end], "\n") {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if key == "" {
			continue
		}
		switch value {
		case "true":
			out[key] = true
			continue
		case "false":
			out[key] = false
			continue
		}
		if len(value) >= 2 && ((value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		out[key] = value
	}
	body := n[end+4:]
	if i := strings.IndexByte(body, '\n'); i >= 0 && strings.TrimSpace(body[:i]) == "" {
		body = body[i+1:]
	}
	return out, body
}

// loadFile is loop's loadSkillFromFile: a description is required; the name
// falls back to the containing directory.
func loadFile(path string, project bool) (Skill, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, false
	}
	fm, body := parseFrontmatter(string(data))
	desc, _ := fm["description"].(string)
	if strings.TrimSpace(desc) == "" {
		return Skill{}, false
	}
	dir := filepath.Dir(path)
	name, _ := fm["name"].(string)
	if name == "" {
		name = filepath.Base(dir)
	}
	return Skill{Name: name, Description: desc, Body: strings.TrimSpace(body), Dir: dir, FilePath: path,
		Project: project, DisableModelInvocation: fm["disable-model-invocation"] == true}, true
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&apos;")

// FormatForPrompt is loop's formatSkillsForPrompt; viaTool points the model
// at the skill tool instead of the read tool.
func FormatForPrompt(all []Skill, viaTool bool) string {
	var visible []Skill
	for _, s := range all {
		if !s.DisableModelInvocation {
			visible = append(visible, s)
		}
	}
	if len(visible) == 0 {
		return ""
	}
	how := "Use the read tool to load a skill's file when the task matches its description."
	if viaTool {
		how = "Invoke the skill tool with a skill's name to load its instructions when the task matches its description."
	}
	lines := []string{
		"\n\nThe following skills provide specialized instructions for specific tasks.",
		how,
		"When a skill file references a relative path, resolve it against the skill directory (parent of SKILL.md / dirname of the path) and use that absolute path in tool commands.",
		"",
		"<available_skills>",
	}
	for _, s := range visible {
		lines = append(lines, "  <skill>", "    <name>"+xmlEscaper.Replace(s.Name)+"</name>",
			"    <description>"+xmlEscaper.Replace(s.Description)+"</description>",
			"    <location>"+xmlEscaper.Replace(s.FilePath)+"</location>", "  </skill>")
	}
	lines = append(lines, "</available_skills>")
	return strings.Join(lines, "\n")
}

// Index is the prompt block for cwd's skills (loop's promptBlock).
func Index(cwd string) string { return FormatForPrompt(Load(cwd), false) }

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// Create scaffolds a new skill directory with a SKILL.md in it.
//
// Authoring is the half that was missing. A skill is only three things — a
// name, a description saying WHEN it applies, and the instructions — but
// getting the frontmatter shape right by hand is exactly the sort of detail
// that turns a good idea into a file the loader silently skips.
//
// The description is not optional and not cosmetic: it is the only thing the
// model sees until it decides to load the skill, so a skill without one can
// never be chosen. That is why an empty description is an error here rather
// than a default.
func Create(dir, name, description, body string) (string, error) {
	name = strings.TrimSpace(name)
	description = oneLine(description)
	if name == "" {
		return "", fmt.Errorf("a skill needs a name")
	}
	if strings.ContainsAny(name, `/\:`) {
		return "", fmt.Errorf("a skill name is a directory name: no slashes or colons")
	}
	if description == "" {
		return "", fmt.Errorf("a skill needs a description — it is the only thing the model sees when deciding whether to load it")
	}
	if strings.TrimSpace(body) == "" {
		return "", fmt.Errorf("a skill needs instructions")
	}

	target := filepath.Join(dir, name)
	path := filepath.Join(target, FileName)
	// Never overwrite. A skill is something the user wrote, and clobbering it
	// because a name collided is not a recoverable mistake.
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("a skill named %q already exists at %s", name, path)
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		return "", err
	}
	doc := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n",
		name, description, strings.TrimRight(body, "\n"))
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		return "", err
	}
	// Read it back through the ordinary parser, so a skill this function
	// wrote can never be one the loader silently skips — which is the exact
	// failure mode hand-written frontmatter has.
	if got, ok := loadFile(path, false); !ok || got.Description == "" || got.Body == "" {
		return "", fmt.Errorf("wrote %s but it does not parse as a skill", path)
	}
	return path, nil
}
