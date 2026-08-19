package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/jsonschema"
)

// Bridging discovered tools into the agent.
//
// A bridged tool is an `ai.Tool` like any other, which is the whole point: it
// is dispatched by the same runner, rendered by the same rows, and — the part
// that matters — gated by the same permissions policy. A transport that let a
// server's tools skip the policy would make the policy decorative, since
// anyone who can edit a settings file could then run anything.
//
// Names are namespaced `server__tool`. Two servers routinely both offer
// `search`, and a collision would silently shadow one of them.

// NameSeparator joins a server name to a tool name.
const NameSeparator = "__"

// ToolName is the agent-facing name for a server's tool.
func ToolName(server, tool string) string {
	return sanitizeName(server) + NameSeparator + tool
}

// SplitToolName recovers the server and tool from a bridged name.
func SplitToolName(name string) (server, tool string, ok bool) {
	server, tool, ok = strings.Cut(name, NameSeparator)
	if !ok || server == "" || tool == "" {
		return "", "", false
	}
	return server, tool, true
}

// sanitizeName reduces a server name to what providers accept in a tool name:
// letters, digits, underscores and dashes.
func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// bridgedTool adapts one server tool to the agent's Tool interface.
type bridgedTool struct {
	client *Client
	// remote is the name the server knows; name is what the model sees.
	remote string
	name   string
	desc   string
	schema *jsonschema.Schema
}

// Name implements ai.Tool.
func (t *bridgedTool) Name() string { return t.name }

// Description implements ai.Tool.
func (t *bridgedTool) Description() string { return t.desc }

// InputSchema implements ai.Tool.
func (t *bridgedTool) InputSchema() *jsonschema.Schema { return t.schema }

// Execute implements ai.Tool.
func (t *bridgedTool) Execute(ctx context.Context, input json.RawMessage) (ai.ToolResult, error) {
	result, err := t.client.CallTool(ctx, t.remote, input)
	if err != nil {
		// A transport failure is reported to the MODEL, not returned as a Go
		// error: a flaky server should cost one tool call, not the whole run.
		return ai.ToolErrorf("%s failed: %v", t.name, err), nil
	}
	text := result.Text()
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	if result.IsError {
		return ai.ToolError(text), nil
	}
	return ai.ToolText(text), nil
}

// Tools discovers a server's tools and bridges them.
func (c *Client) Tools(ctx context.Context) ([]ai.Tool, error) {
	defs, err := c.ListTools(ctx)
	if err != nil {
		return nil, fmt.Errorf("mcp %s: %w", c.Name, err)
	}
	out := make([]ai.Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, &bridgedTool{
			client: c,
			remote: def.Name,
			name:   ToolName(c.Name, def.Name),
			desc:   def.Description,
			schema: parseSchema(def.InputSchema),
		})
	}
	return out, nil
}

// parseSchema decodes a server-supplied JSON schema.
//
// An unusable schema yields a permissive object rather than nil: nil means
// "this tool takes no arguments", which would make the model call it wrongly
// forever. An empty object at least lets it pass something through.
func parseSchema(raw json.RawMessage) *jsonschema.Schema {
	if len(raw) == 0 {
		return emptyObjectSchema()
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return emptyObjectSchema()
	}
	if s.Type == "" {
		s.Type = jsonschema.Object
	}
	return &s
}

func emptyObjectSchema() *jsonschema.Schema {
	return &jsonschema.Schema{Type: jsonschema.Object}
}
