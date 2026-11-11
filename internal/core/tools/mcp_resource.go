package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/core/mcp"
)

// `mcp_resource` — loop's tools/mcp-resource.ts: one tool with a list and a
// read over every connected server's resources, rather than one tool per
// resource, which would spend the tool budget describing them.

// ResourceSource is where resources come from: the MCP manager, or a fake.
type ResourceSource interface {
	Resources() []mcp.Resource
	ResourceTemplates() []mcp.ResourceTemplate
	ReadResource(ctx context.Context, server, uri string) ([]mcp.ResourcePart, error)
}

// maxResourceReadChars caps one read, so a resource that turns out to be a
// 40MB log can't eat the context window.
const maxResourceReadChars = 100_000

// maxResourceListEntries caps a listing, for the same reason.
const maxResourceListEntries = 200

type mcpResourceArgs struct {
	Action string `json:"action"`
	Server string `json:"server,omitempty"`
	URI    string `json:"uri,omitempty"`
	Query  string `json:"query,omitempty"`
}

// MCPResource returns the mcp_resource tool over src.
func MCPResource(src ResourceSource) ai.Tool {
	return LoopTool("mcp_resource", func(ctx context.Context, a mcpResourceArgs) (ai.ToolResult, error) {
		if a.Action == "list" {
			return ai.ToolText(listResources(src, a.Server, a.Query)), nil
		}
		if a.Server == "" || a.URI == "" {
			return fail(errors.New("mcp_resource: `server` and `uri` are both required for action \"read\" — run `list` first."))
		}
		parts, err := src.ReadResource(ctx, a.Server, a.URI)
		if err != nil {
			return fail(err)
		}
		return ai.ToolText(readBody(a.Server, a.URI, parts)), nil
	})
}

func readBody(server, uri string, parts []mcp.ResourcePart) string {
	if len(parts) == 0 {
		return fmt.Sprintf("%s:%s returned no contents.", server, uri)
	}
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.Text
		if len(parts) > 1 {
			label := p.URI
			if label == "" {
				label = uri
			}
			texts[i] = fmt.Sprintf("--- %s ---\n%s", label, p.Text)
		}
	}
	body := strings.Join(texts, "\n\n")
	if len(body) <= maxResourceReadChars {
		return body
	}
	return fmt.Sprintf("%s\n\n[truncated at %d characters]", body[:maxResourceReadChars], maxResourceReadChars)
}

func listResources(src ResourceSource, server, query string) string {
	var resources []mcp.Resource
	for _, r := range src.Resources() {
		if (server == "" || r.Server == server) && (query == "" || resourceMatches(r, query)) {
			resources = append(resources, r)
		}
	}
	var templates []mcp.ResourceTemplate
	for _, t := range src.ResourceTemplates() {
		if server == "" || t.Server == server {
			templates = append(templates, t)
		}
	}
	if len(resources) == 0 && len(templates) == 0 {
		return noResources(server, query)
	}
	var sections []string
	if len(resources) > 0 {
		sections = append(sections, resourceSection(resources))
	}
	if len(templates) > 0 {
		lines := make([]string, len(templates))
		for i, t := range templates {
			lines[i] = describeTemplate(t)
		}
		sections = append(sections, fmt.Sprintf("Templates (%d):\n%s", len(templates), strings.Join(lines, "\n")))
	}
	return strings.Join(sections, "\n\n")
}

func noResources(server, query string) string {
	scope, filter := "", ""
	if server != "" {
		scope = fmt.Sprintf(" on %q", server)
	}
	if query != "" {
		filter = fmt.Sprintf(" matching %q", query)
	}
	return "No MCP resources" + scope + filter + "."
}

func resourceSection(resources []mcp.Resource) string {
	shown := resources[:min(len(resources), maxResourceListEntries)]
	lines := make([]string, len(shown))
	for i, r := range shown {
		lines[i] = describeResource(r)
	}
	out := fmt.Sprintf("Resources (%d):\n%s", len(resources), strings.Join(lines, "\n"))
	if more := len(resources) - len(shown); more > 0 {
		out += fmt.Sprintf("\n… %d more — narrow with `query`", more)
	}
	return out
}

func describeResource(r mcp.Resource) string {
	var meta []string
	if r.MimeType != "" {
		meta = append(meta, r.MimeType)
	}
	if r.Size != nil {
		meta = append(meta, fmt.Sprintf("%dB", *r.Size))
	}
	suffix := ""
	if len(meta) > 0 {
		suffix = " (" + strings.Join(meta, ", ") + ")"
	}
	return fmt.Sprintf("%s:%s  %s%s%s", r.Server, r.URI, firstNonEmpty(r.Title, r.Name), suffix, dashed(r.Description))
}

func describeTemplate(t mcp.ResourceTemplate) string {
	return fmt.Sprintf("%s:%s  %s%s", t.Server, t.URITemplate, firstNonEmpty(t.Title, t.Name), dashed(t.Description))
}

// resourceMatches is a substring match over everything a user would
// plausibly search by.
func resourceMatches(r mcp.Resource, query string) bool {
	hay := strings.ToLower(r.URI + " " + r.Name + " " + r.Title + " " + r.Description)
	return strings.Contains(hay, strings.ToLower(query))
}

func dashed(s string) string {
	if s == "" {
		return ""
	}
	return " — " + s
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
