package mcp

import (
	"context"
	"encoding/json/jsontext"
	"strings"
	"testing"
)

func resourceURIs(m *Manager) []string {
	var uris []string
	for _, r := range m.Resources() {
		uris = append(uris, r.Server+":"+r.URI)
	}
	return uris
}

// A ready server's resources and templates are listed, qualified by server.
func TestManagerListsResources(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	eventually(t, "the resource listing", func() bool { return len(m.Resources()) == 2 })
	if got := strings.Join(resourceURIs(m), " "); got != "fake:note://readme fake:note://logo" {
		t.Errorf("resources = %s", got)
	}
	templates := m.ResourceTemplates()
	if len(templates) != 1 || templates[0].Server != "fake" || templates[0].URITemplate != "note://{name}" {
		t.Errorf("templates = %+v", templates)
	}
	if !m.HasResources() {
		t.Error("HasResources is false with resources listed")
	}
}

// Text is read as text; a blob is described, never inlined.
func TestManagerReadsResources(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	eventually(t, "ready", func() bool { return state(m, "fake") == StateReady })

	parts, err := m.ReadResource(context.Background(), "fake", "note://readme")
	if err != nil || len(parts) != 1 || parts[0].Text != "contents of note://readme" || parts[0].Binary {
		t.Fatalf("text read = %+v, %v", parts, err)
	}
	parts, err = m.ReadResource(context.Background(), "fake", "note://logo")
	if err != nil || len(parts) != 1 || !parts[0].Binary || parts[0].Text != "[binary image/png, 6 bytes — not inlined]" {
		t.Fatalf("blob read = %+v, %v", parts, err)
	}
	if _, err := m.ReadResource(context.Background(), "nope", "x"); err == nil || !strings.Contains(err.Error(), "unknown MCP server") {
		t.Errorf("unknown server error = %v", err)
	}
}

// resources/list_changed re-lists, as tools/list_changed does for tools.
func TestManagerFollowsResourceListChanged(t *testing.T) {
	configs := []ServerConfig{fakeConfig(t, "fake")}
	m := managerWith(t, &configs)
	eventually(t, "the resource listing", func() bool { return len(m.Resources()) == 2 })
	_, _ = toolNamed(m, "mcp__fake__publish").Execute(context.Background(), jsontext.Value(`{}`))
	eventually(t, "the new resource", func() bool { return len(m.Resources()) == 3 })
}
