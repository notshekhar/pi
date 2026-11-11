// Package memory is loop's agent memory (agent/memory.ts): one markdown file
// per fact under ~/.loop/agent/memory/<repo-slug>/, plus a MEMORY.md index.
// Scoped to the repository, because almost everything worth remembering is
// true of one codebase and false of the next.
//
// The load-bearing decision is INDEX-ONLY INJECTION: every turn carries
// MEMORY.md, never the bodies. The agent reads a fact when its index line
// looks relevant.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/core/home"
)

// Fact is one remembered thing.
type Fact struct {
	// Name is the slug, and the filename without its extension.
	Name string
	// Description is the one line that goes in the index.
	Description string
	// Body is the fact itself. Not injected — read on demand.
	Body string
}

// Store is the memory for one repository.
type Store struct{ Dir string }

// Dir is loop's memoryDir: keyed by repo root, so every subdirectory shares
// one memory. It may not exist yet.
func Dir(cwd string) string {
	return home.AgentDir("memory", home.SlugCwd(repoRoot(cwd)))
}

// Open returns the store for a working directory, creating it on demand.
func Open(cwd string) (*Store, error) {
	dir := Dir(cwd)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

var unsafeChars = regexp.MustCompile(`[^a-z0-9._-]+`)

// sanitize reduces a string to a safe filename fragment.
func sanitize(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = unsafeChars.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if s == "" {
		return "unnamed"
	}
	return s
}

func (s *Store) path(name string) string {
	// Sanitised: the name comes from a /memory argument, and must not reach
	// outside the memory directory.
	return filepath.Join(s.Dir, sanitize(name)+".md")
}

// Get reads one fact.
func (s *Store) Get(name string) (Fact, error) {
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return Fact{}, fmt.Errorf("memory: no fact named %q", name)
	}
	return parse(name, string(data)), nil
}

// Delete removes a fact.
func (s *Store) Delete(name string) error {
	if err := os.Remove(s.path(name)); err != nil {
		return fmt.Errorf("memory: no fact named %q", name)
	}
	return nil
}

// List returns every fact, sorted by name. Bodies are included; callers that
// only need the index use Index.
func (s *Store) List() ([]Fact, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Fact
	for _, e := range entries {
		// MEMORY.md is the index, not a fact.
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" || e.Name() == indexFile {
			continue
		}
		name := strings.TrimSuffix(e.Name(), ".md")
		data, err := os.ReadFile(filepath.Join(s.Dir, e.Name()))
		if err != nil {
			// One unreadable file must not hide the rest.
			continue
		}
		out = append(out, parse(name, string(data)))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

var reFrontmatter = regexp.MustCompile(`(?s)\A---\n(.*?)\n---\n?`)

// parse splits frontmatter from body. A file without frontmatter is still a
// fact — its whole contents are the body — so a hand-written note works.
func parse(name, content string) Fact {
	f := Fact{Name: name, Body: strings.TrimSpace(content)}
	m := reFrontmatter.FindStringSubmatch(content)
	if m == nil {
		return f
	}
	f.Body = strings.TrimSpace(content[len(m[0]):])
	for _, line := range strings.Split(m[1], "\n") {
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		switch strings.TrimSpace(key) {
		case "name":
			if value != "" {
				f.Name = value
			}
		case "description":
			f.Description = value
		}
	}
	return f
}

// repoRoot is loop's findRepoRoot: the nearest ancestor holding .git, or cwd.
func repoRoot(start string) string {
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
