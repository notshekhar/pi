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

// writeFact saves a fact the way the agent does: a plain file write.
func writeFact(t *testing.T, s *Store, name, description, body string) {
	t.Helper()
	content := "---\nname: " + name + "\ndescription: " + description + "\n---\n\n" + body + "\n"
	must(t, os.WriteFile(filepath.Join(s.Dir, name+".md"), []byte(content), 0o600))
}

func TestPathStaysInsideTheStore(t *testing.T) {
	s := openStore(t)
	for _, name := range []string{"../../escape", "/etc/passwd", "a/b/c"} {
		got := s.path(name)
		if filepath.Dir(got) != s.Dir {
			t.Errorf("path(%q) = %q, which escapes %q", name, got, s.Dir)
		}
	}
}

func TestGetAndDelete(t *testing.T) {
	s := openStore(t)
	writeFact(t, s, "x", "d", "b")
	if f, err := s.Get("x"); err != nil || f.Description != "d" || f.Body != "b" {
		t.Fatalf("Get = %+v, %v", f, err)
	}
	must(t, s.Delete("x"))
	if _, err := s.Get("x"); err == nil {
		t.Error("the fact survived deletion")
	}
	if err := s.Delete("never-existed"); err == nil {
		t.Error("deleting a missing fact should report it")
	}
}

// MEMORY.md is the index, not a fact, and non-markdown files are ignored.
func TestListSkipsTheIndexAndOtherFiles(t *testing.T) {
	s := openStore(t)
	for _, name := range []string{"zebra", "alpha"} {
		writeFact(t, s, name, "d", "b")
	}
	must(t, os.WriteFile(filepath.Join(s.Dir, "MEMORY.md"), []byte("- [A](alpha.md) — a"), 0o600))
	must(t, os.WriteFile(filepath.Join(s.Dir, "notes.txt"), []byte("ignored"), 0o600))
	facts, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 || facts[0].Name != "alpha" || facts[1].Name != "zebra" {
		t.Errorf("List = %+v", facts)
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

// loop's memoryDir: ~/.loop/agent/memory/<slugCwd(repo root)>, shared by
// every subdirectory of the repository.
func TestDirIsKeyedByRepoRoot(t *testing.T) {
	withHome(t)
	repo := t.TempDir()
	must(t, os.Mkdir(filepath.Join(repo, ".git"), 0o700))
	sub := filepath.Join(repo, "a", "b")
	must(t, os.MkdirAll(sub, 0o700))
	if Dir(sub) != Dir(repo) {
		t.Errorf("Dir(sub) = %q, Dir(repo) = %q", Dir(sub), Dir(repo))
	}
	if want := "--" + strings.ReplaceAll(strings.Trim(repo, "/"), "/", "-") + "--"; filepath.Base(Dir(repo)) != want {
		t.Errorf("slug = %q, want %q", filepath.Base(Dir(repo)), want)
	}
}

// The policy travels even with nothing saved: it is what teaches the agent
// to start saving.
func TestContextWithoutIndex(t *testing.T) {
	withHome(t)
	block := Context("/repo/project")
	for _, want := range []string{"<memory>", Dir("/repo/project") + "/", emptyIndex, "</memory>"} {
		if !strings.Contains(block, want) {
			t.Errorf("block lacks %q:\n%s", want, block)
		}
	}
}

func TestContextCarriesTheIndexNotBodies(t *testing.T) {
	s := openStore(t)
	writeFact(t, s, "build", "how to build", "SECRET-BODY")
	must(t, os.WriteFile(filepath.Join(s.Dir, "MEMORY.md"), []byte("- [Build](build.md) — how to build\n"), 0o600))
	block := Context("/repo/project")
	if !strings.Contains(block, "- [Build](build.md) — how to build") || strings.Contains(block, "SECRET-BODY") {
		t.Errorf("block:\n%s", block)
	}
}

// The cap cuts on a line boundary, never mid-line.
func TestContextCapsTheIndex(t *testing.T) {
	s := openStore(t)
	line := "- [Fact](fact.md) — " + strings.Repeat("é", 40) + "\n"
	must(t, os.WriteFile(filepath.Join(s.Dir, "MEMORY.md"), []byte(strings.Repeat(line, 400)), 0o600))
	block := Context("/repo/project")
	if !strings.Contains(block, "\n...[index truncated — consider pruning MEMORY.md]") {
		t.Fatal("no truncation note")
	}
	index := block[strings.Index(block, "MEMORY.md index:\n")+len("MEMORY.md index:\n") : strings.Index(block, "\n...[index truncated")]
	if len(index) > maxIndexBytes {
		t.Errorf("index is %d bytes, over %d", len(index), maxIndexBytes)
	}
	for _, l := range strings.Split(index, "\n") {
		if l+"\n" != line {
			t.Fatalf("a line was cut: %q", l)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
