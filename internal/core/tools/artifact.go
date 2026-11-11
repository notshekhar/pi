package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/artifacts"
)

// `artifact` — loop's tools/artifact.ts. It reserves a standalone page under
// ~/.loop/artifacts and hands back a real path; the agent writes the page
// with the ordinary write tool and revises it with edit, so the file IS the
// artifact.

// artifactResultSeparator splits the model's summary from the UI's card
// payload, as loop's ARTIFACT_RESULT_SEPARATOR. The model never gets the
// file:// URL — it would paste it into its reply.
const artifactResultSeparator = "\n artifact:"

type artifactArgs struct {
	Title       string  `json:"title"`
	Kind        string  `json:"kind,omitempty"`
	Description *string `json:"description,omitempty"`
	Favicon     *string `json:"favicon,omitempty"`
	ID          string  `json:"id,omitempty"`
}

// artifactPayload is loop's ArtifactResultPayload: what a UI draws a card from.
type artifactPayload struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
	Favicon     string `json:"favicon,omitempty"`
	Path        string `json:"path,omitempty"`
	URL         string `json:"url,omitempty"`
}

// Artifact returns the artifact tool.
func Artifact(t *Context) ai.Tool {
	return LoopTool("artifact", func(ctx context.Context, a artifactArgs) (ai.ToolResult, error) {
		if aborted(ctx) {
			return fail(errAborted)
		}
		// Creating writes into ~/.loop/artifacts: a mutation plan mode refuses.
		if t.planMode() {
			return fail(errors.New(planModeRefusal))
		}
		meta, err := saveArtifact(t, a)
		if err != nil {
			return fail(err)
		}
		summary := artifactSummary(meta, a.ID != "")
		return ai.ToolText(summary).WithDisplay(summary + artifactResultSeparator + artifactCard(meta)), nil
	})
}

func saveArtifact(t *Context, a artifactArgs) (artifacts.Meta, error) {
	if a.ID != "" {
		return artifacts.UpdateMeta(a.ID, &a.Title, a.Description, a.Favicon)
	}
	kind := a.Kind
	if kind == "" {
		kind = "html"
	}
	in := artifacts.CreateInput{Title: a.Title, Kind: kind, SessionID: t.SessionID}
	if a.Description != nil {
		in.Description = *a.Description
	}
	if a.Favicon != nil {
		in.Favicon = *a.Favicon
	}
	return artifacts.Create(in)
}

func artifactSummary(meta artifacts.Meta, updated bool) string {
	path := artifacts.FilePath(meta.Record)
	if updated {
		return fmt.Sprintf("Updated artifact %q (%s). Its content is at %s", meta.Title, meta.ID, path)
	}
	return fmt.Sprintf("Created artifact %q (%s).\nWrite the content to: %s\n"+
		"Keep it under %d MB. Edit that same path to revise it. "+
		"Do not print this path or a file:// link in your reply — the app shows the user an "+
		"openable card for this artifact. Just say what you made.", meta.Title, meta.ID, path, artifacts.MaxBytes/1024/1024)
}

func artifactCard(meta artifacts.Meta) string {
	path := artifacts.FilePath(meta.Record)
	raw, _ := json.Marshal(artifactPayload{
		ID: meta.ID, Title: meta.Title, Kind: meta.Kind, Description: meta.Description, Favicon: meta.Favicon,
		Path: path, URL: (&url.URL{Scheme: "file", Path: path}).String(),
	})
	return string(raw)
}
