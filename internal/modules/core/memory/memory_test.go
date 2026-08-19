package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func withHome(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
}

func openStore(t *testing.T) *Store {
	t.Helper()
	withHome(t)
	s, err := Open("/repo/project")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSaveGetRoundTrip(t *testing.T) {
	s := openStore(t)
	want := Fact{
		Name:        "build-command",
		Description: "the build needs CGO_ENABLED=0",
		Body:        "Run `CGO_ENABLED=0 go build ./...`.\n\nThe default build links libc and fails in the container.",
	}
	if err := s.Save(want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("build-command")
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != want.Name || got.Description != want.Description {
		t.Errorf("frontmatter lost: %+v", got)
	}
	if got.Body != strings.TrimSpace(want.Body) {
		t.Errorf("body = %q", got.Body)
	}
}

func TestSaveReplaces(t *testing.T) {
	s := openStore(t)
	must(t, s.Save(Fact{Name: "x", Description: "first", Body: "old"}))
	must(t, s.Save(Fact{Name: "x", Description: "second", Body: "new"}))

	facts, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 {
		t.Fatalf("saving the same name twice made %d facts", len(facts))
	}
	if facts[0].Body != "new" || facts[0].Description != "second" {
		t.Errorf("fact = %+v", facts[0])
	}
}

func TestSaveValidates(t *testing.T) {
	s := openStore(t)
	cases := []struct {
		name string
		fact Fact
	}{
		{"no description", Fact{Name: "x", Body: "b"}},
		{"no body", Fact{Name: "x", Description: "d"}},
		{"empty name", Fact{Description: "d", Body: "b"}},
		{"uppercase name", Fact{Name: "Bad", Description: "d", Body: "b"}},
		{"spaces in name", Fact{Name: "two words", Description: "d", Body: "b"}},
		// The one that matters: a name is model-supplied and reaches the
		// filesystem.
		{"path traversal", Fact{Name: "../../escape", Description: "d", Body: "b"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := s.Save(c.fact); err == nil {
				t.Error("expected a rejection")
			}
		})
	}
}

// Belt and braces: even if a bad name got past validation, it must not write
// outside the store.
func TestPathStaysInsideTheStore(t *testing.T) {
	s := openStore(t)
	for _, name := range []string{"../../escape", "/etc/passwd", "a/b/c"} {
		got := s.path(name)
		if filepath.Dir(got) != s.Dir {
			t.Errorf("path(%q) = %q, which escapes %q", name, got, s.Dir)
		}
	}
}

func TestDelete(t *testing.T) {
	s := openStore(t)
	must(t, s.Save(Fact{Name: "x", Description: "d", Body: "b"}))
	if err := s.Delete("x"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get("x"); err == nil {
		t.Error("the fact survived deletion")
	}
	if err := s.Delete("never-existed"); err == nil {
		t.Error("deleting a missing fact should report it")
	}
}

// The whole design: names and descriptions travel, bodies do not.
func TestIndexCarriesNoBodies(t *testing.T) {
	s := openStore(t)
	must(t, s.Save(Fact{
		Name: "trap", Description: "never run format at the root",
		Body: "THIS_BODY_MUST_NOT_BE_INJECTED",
	}))
	index := s.Index()
	if !strings.Contains(index, "trap") || !strings.Contains(index, "never run format") {
		t.Errorf("index is missing the entry:\n%s", index)
	}
	if strings.Contains(index, "THIS_BODY_MUST_NOT_BE_INJECTED") {
		t.Errorf("the index leaked a body:\n%s", index)
	}
}

func TestIndexEmptyWhenNothingRemembered(t *testing.T) {
	if got := openStore(t).Index(); got != "" {
		t.Errorf("empty store produced an index: %q", got)
	}
}

func TestListIsSorted(t *testing.T) {
	s := openStore(t)
	for _, name := range []string{"zebra", "alpha", "middle"} {
		must(t, s.Save(Fact{Name: name, Description: "d", Body: "b"}))
	}
	facts, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	got := []string{facts[0].Name, facts[1].Name, facts[2].Name}
	want := []string{"alpha", "middle", "zebra"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("List = %v, want %v", got, want)
		}
	}
}

// A hand-written note with no frontmatter is still a fact.
func TestParseWithoutFrontmatter(t *testing.T) {
	f := parse("notes", "just some prose\nover two lines")
	if f.Name != "notes" || f.Body != "just some prose\nover two lines" {
		t.Errorf("parse = %+v", f)
	}
}

func TestParseFrontmatter(t *testing.T) {
	f := parse("file-slug", "---\nname: real-name\ndescription: a summary\n---\n\nthe body\n")
	if f.Name != "real-name" || f.Description != "a summary" || f.Body != "the body" {
		t.Errorf("parse = %+v", f)
	}
}

// A multi-line description would break the index's one-line-per-fact shape.
func TestDescriptionIsFlattened(t *testing.T) {
	s := openStore(t)
	must(t, s.Save(Fact{Name: "x", Description: "line one\nline two", Body: "b"}))
	got, err := s.Get("x")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Description, "\n") {
		t.Errorf("description kept its newline: %q", got.Description)
	}
	if lines := strings.Count(s.Index(), "\n- "); lines != 1 {
		t.Errorf("index has %d entries for one fact", lines)
	}
}

// Two checkouts of the same repository must not share a memory.
func TestSlugSeparatesSamedNamedDirectories(t *testing.T) {
	a := Slug("/home/me/work/project")
	b := Slug("/home/me/scratch/project")
	if a == b {
		t.Errorf("both paths slugged to %q", a)
	}
	if !strings.HasPrefix(a, "project-") {
		t.Errorf("slug %q is not readable", a)
	}
	// And it must be stable across calls, or memory is lost on restart.
	if Slug("/home/me/work/project") != a {
		t.Error("Slug is not deterministic")
	}
}

func TestValidName(t *testing.T) {
	for _, ok := range []string{"build", "build-command", "a_b", "x1"} {
		if !ValidName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "Bad", "two words", "../x", "a/b", strings.Repeat("x", 65)} {
		if ValidName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}

// An unreadable file must not hide every other fact.
func TestListSurvivesABadFile(t *testing.T) {
	s := openStore(t)
	must(t, s.Save(Fact{Name: "good", Description: "d", Body: "b"}))
	if err := os.WriteFile(filepath.Join(s.Dir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}
	facts, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].Name != "good" {
		t.Errorf("List = %+v", facts)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
