package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSkill creates a skill directory under root.
func writeSkill(t *testing.T, root, name, frontmatter, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := body
	if frontmatter != "" {
		content = "---\n" + frontmatter + "\n---\n\n" + body
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setup points HOME at a temp dir and returns (userSkills, cwd).
func setup(t *testing.T) (string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	cwd := t.TempDir()
	return filepath.Join(home, ".pi-agent", "skills"), cwd
}

func TestLoadReadsUserAndProjectSkills(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "review", "name: review\ndescription: how to review", "Review carefully.")
	writeSkill(t, ProjectDir(cwd), "deploy", "name: deploy\ndescription: how to deploy", "Deploy carefully.")

	all := Load(cwd)
	if len(all) != 2 {
		t.Fatalf("loaded %d skills: %+v", len(all), all)
	}
	// Sorted by name.
	if all[0].Name != "deploy" || all[1].Name != "review" {
		t.Errorf("order = %q, %q", all[0].Name, all[1].Name)
	}
	if !all[0].Project {
		t.Error("the project skill is not marked as one")
	}
	if all[1].Project {
		t.Error("the user skill is marked as a project skill")
	}
}

// A repository shipping a skill is making a statement about how work in it
// should be done, and that beats a personal preference.
func TestProjectSkillWinsOnNameClash(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "review", "description: personal", "PERSONAL")
	writeSkill(t, ProjectDir(cwd), "review", "description: project", "PROJECT")

	all := Load(cwd)
	if len(all) != 1 {
		t.Fatalf("expected one skill, got %d", len(all))
	}
	if all[0].Body != "PROJECT" || !all[0].Project {
		t.Errorf("skill = %+v", all[0])
	}
}

// One malformed directory must not hide the rest.
func TestLoadSkipsMalformedSkills(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "good", "description: fine", "Body.")
	// No description: cannot be indexed, so the model could never choose it.
	writeSkill(t, userDir, "nodesc", "name: nodesc", "Body.")
	// No body: nothing to load.
	writeSkill(t, userDir, "nobody", "description: empty", "")
	// A directory with no manifest at all.
	if err := os.MkdirAll(filepath.Join(userDir, "empty"), 0o700); err != nil {
		t.Fatal(err)
	}

	all := Load(cwd)
	if len(all) != 1 || all[0].Name != "good" {
		t.Errorf("loaded %+v", all)
	}
}

func TestFind(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "review", "description: d", "Body.")

	got, err := Find(cwd, "review")
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Body." {
		t.Errorf("body = %q", got.Body)
	}
	// The model will not be careful about case.
	if _, err := Find(cwd, "REVIEW"); err != nil {
		t.Errorf("lookup should ignore case: %v", err)
	}
	if _, err := Find(cwd, "missing"); err == nil {
		t.Error("expected an error for an unknown skill")
	}
}

// The whole economy: names and descriptions travel, bodies do not.
func TestIndexCarriesNoBodies(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "review", "description: how to review code",
		"THIS_BODY_MUST_NOT_BE_INJECTED")

	index := Index(cwd)
	if !strings.Contains(index, "review") || !strings.Contains(index, "how to review code") {
		t.Errorf("index missing the entry:\n%s", index)
	}
	if strings.Contains(index, "THIS_BODY_MUST_NOT_BE_INJECTED") {
		t.Errorf("the index leaked a body:\n%s", index)
	}
}

func TestIndexEmptyWithNoSkills(t *testing.T) {
	_, cwd := setup(t)
	if got := Index(cwd); got != "" {
		t.Errorf("empty index = %q", got)
	}
}

// The directory name is the fallback identity.
func TestNameFallsBackToDirectory(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "dir-name", "description: d", "Body.")
	all := Load(cwd)
	if len(all) != 1 || all[0].Name != "dir-name" {
		t.Errorf("skills = %+v", all)
	}
}

func TestDirIsReported(t *testing.T) {
	userDir, cwd := setup(t)
	writeSkill(t, userDir, "review", "description: d", "Body.")
	// A skill's instructions routinely reference its own files, so the model
	// needs a path to read them from.
	if got := Load(cwd)[0].Dir; got != filepath.Join(userDir, "review") {
		t.Errorf("dir = %q", got)
	}
}

func TestDescriptionIsFlattened(t *testing.T) {
	s := parse("x", "---\ndescription:   spread   over    spaces\n---\n\nbody")
	if s.Description != "spread over spaces" {
		t.Errorf("description = %q", s.Description)
	}
}

// Authoring writes a file the LOADER accepts — which is the whole point of
// having a Create at all, since hand-written frontmatter that is subtly wrong
// is skipped in silence.
func TestCreateWritesALoadableSkill(t *testing.T) {
	dir := t.TempDir()
	path, err := Create(dir, "commit-style", "Use when writing a commit message.", "Body first.\n\n- be terse")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, filepath.Join("commit-style", FileName)) {
		t.Errorf("wrote %q", path)
	}
	got := scan(dir, false)
	if len(got) != 1 {
		t.Fatalf("the loader found %d skills in what Create wrote", len(got))
	}
	if got[0].Name != "commit-style" {
		t.Errorf("name = %q", got[0].Name)
	}
	if got[0].Description != "Use when writing a commit message." {
		t.Errorf("description = %q", got[0].Description)
	}
	if !strings.Contains(got[0].Body, "be terse") {
		t.Errorf("body lost: %q", got[0].Body)
	}
}

// A description is the ONLY thing the model sees before deciding to load a
// skill, so one without it can never be chosen. That makes an empty
// description an error rather than a default.
func TestCreateRefusesWhatCannotBeChosen(t *testing.T) {
	dir := t.TempDir()
	cases := []struct{ name, desc, body, why string }{
		{"", "d", "b", "no name"},
		{"n", "", "b", "no description"},
		{"n", "d", "  ", "no instructions"},
		{"a/b", "d", "b", "a name that is not a directory name"},
	}
	for _, c := range cases {
		if _, err := Create(dir, c.name, c.desc, c.body); err == nil {
			t.Errorf("accepted %s", c.why)
		}
	}
}

// A skill is something the user wrote. Clobbering it because a name collided
// is not a recoverable mistake.
func TestCreateNeverOverwrites(t *testing.T) {
	dir := t.TempDir()
	if _, err := Create(dir, "dup", "Use when X.", "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, "dup", "Use when Y.", "second"); err == nil {
		t.Fatal("overwrote an existing skill")
	}
	got := scan(dir, false)
	if len(got) != 1 || !strings.Contains(got[0].Body, "first") {
		t.Errorf("the original was disturbed: %+v", got)
	}
}

// The whole point of progressive disclosure: a new skill appears in the INDEX
// (name + description only), and its body stays out until it is loaded.
func TestNewSkillEntersTheIndexButNotTheContext(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir, err := UserDir()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Create(dir, "changelog", "Use when writing release notes.", "SECRET-BODY-TEXT"); err != nil {
		t.Fatal(err)
	}
	index := Index(t.TempDir())
	if !strings.Contains(index, "changelog") || !strings.Contains(index, "Use when writing release notes.") {
		t.Errorf("a new skill did not reach the index:\n%s", index)
	}
	if strings.Contains(index, "SECRET-BODY-TEXT") {
		t.Errorf("the index carried the body — that is not progressive disclosure:\n%s", index)
	}
}
