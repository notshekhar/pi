// Package memory stores facts that outlive a session.
//
// One markdown file per fact, under ~/.pi-agent/memory/<repo-slug>/. Scoped
// to the repository, because almost everything worth remembering — the build
// command, a convention, a trap someone already paid for — is true of one
// codebase and false of the next.
//
// The load-bearing decision is INDEX-ONLY INJECTION. Every turn carries the
// list of names and one-line descriptions; no bodies. A memory that injected
// its full contents would grow until it crowded out the conversation it was
// meant to help, and the failure would look like the model getting worse
// rather than the memory getting fatter. The agent reads a body when the
// index tells it there is one worth reading.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
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

// Open returns the store for a working directory, creating it on demand.
func Open(cwd string) (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".pi-agent", "memory", Slug(cwd))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{Dir: dir}, nil
}

// Slug turns a path into a stable directory name.
//
// The basename plus a short hash of the full path: readable when you go
// looking, and still distinct between two checkouts of the same repository.
func Slug(cwd string) string {
	clean := filepath.Clean(cwd)
	base := filepath.Base(clean)
	if base == "." || base == string(filepath.Separator) || base == "" {
		base = "root"
	}
	return sanitize(base) + "-" + shortHash(clean)
}

// shortHash is FNV-1a over the path, hex-encoded. Not cryptographic — it only
// has to separate directories that share a basename.
func shortHash(s string) string {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return fmt.Sprintf("%08x", h)
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

// ValidName reports whether a name is usable as a memory slug.
func ValidName(name string) bool {
	if name == "" || len(name) > 64 {
		return false
	}
	for _, r := range name {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '-' && r != '_' {
			return false
		}
	}
	return true
}

func (s *Store) path(name string) string {
	// Sanitised even though ValidName has already run: this is the only thing
	// standing between a model-supplied name and the filesystem, and defence
	// in depth costs one function call.
	return filepath.Join(s.Dir, sanitize(name)+".md")
}

// Save writes a fact, replacing any with the same name.
func (s *Store) Save(f Fact) error {
	if !ValidName(f.Name) {
		return fmt.Errorf("memory: %q is not a valid name (lowercase, digits, - and _)", f.Name)
	}
	if strings.TrimSpace(f.Description) == "" {
		return fmt.Errorf("memory: a fact needs a one-line description for the index")
	}
	if strings.TrimSpace(f.Body) == "" {
		return fmt.Errorf("memory: a fact needs a body")
	}

	content := fmt.Sprintf("---\nname: %s\ndescription: %s\n---\n\n%s\n",
		f.Name, oneLine(f.Description), strings.TrimSpace(f.Body))

	tmp := s.path(f.Name) + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(f.Name))
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
		if e.IsDir() || filepath.Ext(e.Name()) != ".md" {
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

// Index is the block injected into the system prompt: names and descriptions
// only, never bodies. Empty when there is nothing to say.
func (s *Store) Index() string {
	facts, err := s.List()
	if err != nil || len(facts) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Things you have remembered about this repository. " +
		"These are one-line summaries — use the memory tool to read one in full " +
		"when it looks relevant, and to record anything new worth keeping.\n")
	for _, f := range facts {
		fmt.Fprintf(&b, "\n- %s — %s", f.Name, f.Description)
	}
	return b.String()
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

// oneLine flattens text so a description cannot break the index's shape.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
