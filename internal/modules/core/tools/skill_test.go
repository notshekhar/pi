package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/core/skills"
)

func callSkill(t *testing.T, c *Context, name string) (string, bool) {
	t.Helper()
	input, _ := json.Marshal(skillArgs{Name: name})
	res, err := Skill(c).Execute(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	switch out := res.Output().(type) {
	case provider.ToolOutputText:
		return out.Value, false
	case provider.ToolOutputErrorText:
		return out.Value, true
	}
	t.Fatalf("unexpected output %T", res.Output())
	return "", false
}

func skillContext(t *testing.T) *Context {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return &Context{CWD: t.TempDir(), Registry: NewRegistry(), Todos: &TodoList{}}
}

func installSkill(t *testing.T, cwd, name, desc, body string) {
	t.Helper()
	dir := filepath.Join(skills.ProjectDir(cwd), name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body
	if err := os.WriteFile(filepath.Join(dir, skills.FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSkillToolLoadsInstructions(t *testing.T) {
	c := skillContext(t)
	installSkill(t, c.CWD, "review", "how to review", "Read the diff twice.")

	out, isErr := callSkill(t, c, "review")
	if isErr {
		t.Fatalf("load failed: %s", out)
	}
	if !strings.Contains(out, "Read the diff twice.") {
		t.Errorf("instructions missing: %q", out)
	}
	// The directory travels with it, because a skill's instructions routinely
	// reference its own files.
	if !strings.Contains(out, "Skill directory:") {
		t.Errorf("directory not reported: %q", out)
	}
}

// An unknown name must list what IS available, or the model guesses again.
func TestSkillToolListsAlternativesOnMiss(t *testing.T) {
	c := skillContext(t)
	installSkill(t, c.CWD, "review", "d", "b")
	installSkill(t, c.CWD, "deploy", "d", "b")

	out, isErr := callSkill(t, c, "nonexistent")
	if !isErr {
		t.Fatal("expected an error")
	}
	for _, want := range []string{"review", "deploy"} {
		if !strings.Contains(out, want) {
			t.Errorf("error does not list %q: %q", want, out)
		}
	}
}

func TestSkillToolWithNoSkillsInstalled(t *testing.T) {
	c := skillContext(t)
	out, isErr := callSkill(t, c, "anything")
	if !isErr || !strings.Contains(out, "no skills") {
		t.Errorf("out = %q isErr=%v", out, isErr)
	}
}

func TestSkillToolRejectsEmptyName(t *testing.T) {
	if out, isErr := callSkill(t, skillContext(t), ""); !isErr {
		t.Errorf("empty name accepted: %q", out)
	}
}

func TestSkillToolIsRegistered(t *testing.T) {
	for _, tool := range All(skillContext(t)) {
		if tool.Name() == "skill" {
			return
		}
	}
	t.Error("the skill tool is not registered")
}
