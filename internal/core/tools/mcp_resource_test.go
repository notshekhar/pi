package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/core/mcp"
)

type fakeResources struct {
	resources []mcp.Resource
	templates []mcp.ResourceTemplate
	parts     []mcp.ResourcePart
}

func (f fakeResources) Resources() []mcp.Resource                 { return f.resources }
func (f fakeResources) ResourceTemplates() []mcp.ResourceTemplate { return f.templates }
func (f fakeResources) ReadResource(context.Context, string, string) ([]mcp.ResourcePart, error) {
	return f.parts, nil
}

func runResource(t *testing.T, src ResourceSource, args map[string]any) (string, bool) {
	t.Helper()
	return runTool(t, MCPResource(src), args)
}

func TestMCPResourceListsLikeLoop(t *testing.T) {
	size := int64(12)
	src := fakeResources{
		resources: []mcp.Resource{
			{Server: "docs", URI: "doc://a", Name: "a", Title: "Alpha", MimeType: "text/plain", Size: &size, Description: "first"},
			{Server: "other", URI: "doc://b", Name: "b"},
		},
		templates: []mcp.ResourceTemplate{{Server: "docs", URITemplate: "doc://{id}", Name: "doc"}},
	}
	got, _ := runResource(t, src, map[string]any{"action": "list", "server": "docs"})
	want := "Resources (1):\ndocs:doc://a  Alpha (text/plain, 12B) — first\n\nTemplates (1):\ndocs:doc://{id}  doc"
	if got != want {
		t.Errorf("list =\n%s\nwant\n%s", got, want)
	}
	if got, _ := runResource(t, src, map[string]any{"action": "list", "query": "zzz", "server": "other"}); got != `No MCP resources on "other" matching "zzz".` {
		t.Errorf("empty list = %q", got)
	}
}

func TestMCPResourceReadNeedsServerAndURI(t *testing.T) {
	if got, isErr := runResource(t, fakeResources{}, map[string]any{"action": "read"}); !isErr || !strings.Contains(got, "run `list` first") {
		t.Errorf("read without uri = %q (error %v)", got, isErr)
	}
}

func TestMCPResourceReadLabelsPartsAndTruncates(t *testing.T) {
	src := fakeResources{parts: []mcp.ResourcePart{{URI: "doc://a", Text: "one"}, {Text: "two"}}}
	got, _ := runResource(t, src, map[string]any{"action": "read", "server": "docs", "uri": "doc://x"})
	if got != "--- doc://a ---\none\n\n--- doc://x ---\ntwo" {
		t.Errorf("multi-part read = %q", got)
	}
	src.parts = []mcp.ResourcePart{{Text: strings.Repeat("x", maxResourceReadChars+5)}}
	got, _ = runResource(t, src, map[string]any{"action": "read", "server": "docs", "uri": "doc://x"})
	if !strings.HasSuffix(got, "[truncated at 100000 characters]") {
		t.Errorf("long read not truncated: …%s", got[len(got)-40:])
	}
}
