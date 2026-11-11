package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/core/artifacts"
)

// The model gets the summary and the path to write; only the UI gets the
// card payload with its file:// URL.
func TestArtifactCreateThenRetitle(t *testing.T) {
	artifacts.SetDirForTests(t.TempDir())
	t.Cleanup(func() { artifacts.SetDirForTests("") })
	c := &Context{SessionID: "s1"}
	res, err := Artifact(c).Execute(context.Background(), json.RawMessage(`{"title":"Report","kind":"markdown"}`))
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(res)
	if !strings.Contains(text, "Write the content to: ") || !strings.HasSuffix(strings.SplitN(text, "\n", 3)[1], "index.md") || strings.Contains(text, "file:///") {
		t.Fatalf("model text = %q", text)
	}
	display, _ := res.Display()
	card, _, _ := strings.Cut(strings.SplitN(display.(string), artifactResultSeparator, 2)[1], "\n")
	var payload artifactPayload
	if json.Unmarshal([]byte(card), &payload) != nil || !strings.HasPrefix(payload.URL, "file://") || payload.Title != "Report" {
		t.Fatalf("card = %q", card)
	}

	input, _ := json.Marshal(map[string]string{"title": "Final report", "id": payload.ID})
	res, _ = Artifact(c).Execute(context.Background(), input)
	if !strings.HasPrefix(resultText(res), `Updated artifact "Final report"`) {
		t.Errorf("retitle = %q", resultText(res))
	}
}

func TestArtifactRefusedInPlanMode(t *testing.T) {
	artifacts.SetDirForTests(t.TempDir())
	t.Cleanup(func() { artifacts.SetDirForTests("") })
	out, failed := runTool(t, Artifact(&Context{PlanMode: fixedPlanMode(true)}), map[string]any{"title": "x"})
	if !failed || !strings.Contains(out, "Plan mode is active") {
		t.Errorf("got %q (failed=%v)", out, failed)
	}
}
