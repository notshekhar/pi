package mcp

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonschema"
	"github.com/notshekhar/pi/internal/ai/provider"
)

// Bridging a server's tools into the agent (loop's mcp/client.ts naming and
// withTimeout).
//
// Names are `mcp__<server>__<tool>`, the Claude Code convention, in the
// charset every provider accepts and at most 64 characters: a provider
// rejects the WHOLE request over one bad tool name, so one verbose server
// would otherwise break every turn, not just its own tools.

const (
	maxToolName = 64
	// minToolChars is what a tool is guaranteed to keep, and so the longest
	// server segment a prefix may carry: the prefix is how a server's tools
	// are found to withdraw or attribute, and must survive any name.
	minToolChars   = 8
	maxServerChars = maxToolName - minToolChars - len("mcp____")
)

var unsafeNameChars = regexp.MustCompile(`[^a-zA-Z0-9_]`)

func sanitize(s string) string { return unsafeNameChars.ReplaceAllString(s, "_") }

// ServerPrefix is the shared prefix of one server's tools: mcp__<server>__.
func ServerPrefix(server string) string {
	s := sanitize(server)
	if len(s) > maxServerChars {
		s = s[:maxServerChars]
	}
	return "mcp__" + s + "__"
}

// ToolName is the agent-facing name for a server's tool.
func ToolName(server, tool string) string {
	prefix := ServerPrefix(server)
	t := sanitize(tool)
	if len(prefix)+len(t) <= maxToolName {
		return prefix + t
	}
	// Shortened from the middle, keeping the tool's head and tail readable.
	budget := maxToolName - len(prefix)
	head := budget / 2 // ceil((budget-1)/2)
	tail := budget - 1 - head
	return prefix + t[:head] + "_" + t[len(t)-tail:]
}

// uniqueToolName is ToolName, disambiguated against names already taken:
// sanitizing folds get-issue and get_issue together and shortening folds
// names that differ only in the middle, and the second must not silently
// replace the first. The digest keeps the name stable across reconnects.
func uniqueToolName(server, tool string, taken map[string]bool) string {
	base := ToolName(server, tool)
	if !taken[base] {
		return base
	}
	sum := sha1.Sum([]byte(tool))
	tag := hex.EncodeToString(sum[:])[:4]
	for attempt := 0; ; attempt++ {
		suffix := "_" + tag
		if attempt > 0 {
			suffix = fmt.Sprintf("_%s%d", tag, attempt)
		}
		cut := min(len(base), maxToolName-len(suffix))
		if candidate := base[:cut] + suffix; !taken[candidate] {
			return candidate
		}
	}
}

// toolTimeout is the ceiling on one call: a wedged server must cost one
// tool error the model can recover from, not a frozen turn.
func toolTimeout() time.Duration {
	return positiveEnvDuration("MCP_TOOL_TIMEOUT_MS", 120*time.Second)
}

// bridgedTool adapts one server tool to ai.Tool. It holds the server's
// name, not its connection, so it keeps working across a reconnect and says
// plainly when the server is gone.
type bridgedTool struct {
	m      *Manager
	server string
	remote string
	name   string
	desc   string
	schema *jsonschema.Schema
}

func (t *bridgedTool) Name() string                    { return t.name }
func (t *bridgedTool) Description() string             { return t.desc }
func (t *bridgedTool) InputSchema() *jsonschema.Schema { return t.schema }

// Server is the server this tool belongs to.
func (t *bridgedTool) Server() string { return t.server }

func (t *bridgedTool) Execute(ctx context.Context, input json.RawMessage) (ai.ToolResult, error) {
	client, gen := t.m.liveClient(t.server)
	if client == nil {
		return ai.ToolErrorf("MCP server %q is not connected — /mcp to see why", t.server), nil
	}
	timeout := toolTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := client.CallTool(ctx, t.remote, input)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("MCP tool %s timed out after %s", t.name, timeout)
		}
		if isDisconnect(err) {
			t.m.markDisconnected(t.server, gen, err)
		}
		// A transport failure is reported to the MODEL, not returned as a Go
		// error: a flaky server should cost one tool call, not the run.
		return ai.ToolErrorf("%s failed: %v", t.name, err), nil
	}
	return toolResult(result), nil
}

var closedConnection = regexp.MustCompile(`(?i)connection closed|closed client|transport closed|not connected`)

func isDisconnect(err error) bool { return err != nil && closedConnection.MatchString(err.Error()) }

// toolResult maps an MCP result onto what the model is sent.
func toolResult(r CallToolResult) ai.ToolResult {
	var parts []provider.ToolContentPart
	var texts []string
	hasMedia := false
	for _, c := range r.Content {
		switch c.Type {
		case "text", "":
			if c.Text != "" {
				texts = append(texts, c.Text)
				parts = append(parts, provider.ToolContentText{Text: c.Text})
			}
		case "image":
			if _, err := base64.StdEncoding.DecodeString(c.Data); err == nil && c.Data != "" {
				hasMedia = true
				parts = append(parts, provider.ToolContentFile{Data: provider.FileDataBytes{Base64: c.Data}, MediaType: c.MimeType})
			}
		case "resource":
			if c.Resource != nil && c.Resource.Text != "" {
				texts = append(texts, c.Resource.Text)
				parts = append(parts, provider.ToolContentText{Text: c.Resource.Text})
			} else if c.Resource != nil {
				note := "[resource " + c.Resource.URI + "]"
				texts = append(texts, note)
				parts = append(parts, provider.ToolContentText{Text: note})
			}
		case "resource_link":
			note := strings.TrimSpace("[resource link " + c.Name + " " + c.URI + "]")
			texts = append(texts, note)
			parts = append(parts, provider.ToolContentText{Text: note})
		default:
			kind := c.Type
			if c.MimeType != "" {
				kind += " (" + c.MimeType + ")"
			}
			note := "[" + kind + " omitted]"
			texts = append(texts, note)
			parts = append(parts, provider.ToolContentText{Text: note})
		}
	}
	// A server that returns its real payload as structuredContent with no
	// text block would otherwise hand the model nothing at all.
	if len(texts) == 0 && len(r.StructuredContent) > 0 && string(r.StructuredContent) != "null" {
		var v any
		if json.Unmarshal(r.StructuredContent, &v) == nil {
			pretty, _ := json.MarshalIndent(v, "", "  ")
			texts = append(texts, string(pretty))
			parts = append(parts, provider.ToolContentText{Text: string(pretty)})
		}
	}
	text := strings.Join(texts, "\n")
	if r.IsError {
		if text == "" {
			text = "(the tool reported an error with no message)"
		}
		return ai.ToolError(text)
	}
	if hasMedia {
		return ai.ToolContent(parts...)
	}
	if strings.TrimSpace(text) == "" {
		text = "(no output)"
	}
	return ai.ToolText(text)
}

// bridge wraps a server's tool list, filtered by its policy on the
// server's OWN names (a policy is written against what the server
// documents, not against shortened mcp__ keys).
func (m *Manager) bridge(server string, cfg ServerConfig, defs []ToolDef) []ai.Tool {
	taken := map[string]bool{}
	out := make([]ai.Tool, 0, len(defs))
	for _, def := range defs {
		if def.Name == "" || !cfg.ToolAllowed(def.Name) {
			continue
		}
		name := uniqueToolName(server, def.Name, taken)
		taken[name] = true
		desc := def.Description
		if desc == "" {
			desc = def.Title
		}
		out = append(out, &bridgedTool{m: m, server: server, remote: def.Name, name: name, desc: desc, schema: parseSchema(def.InputSchema)})
	}
	return out
}

// parseSchema decodes a server-supplied JSON schema. An unusable one yields
// a permissive object rather than nil: nil means "takes no arguments",
// which would make the model call it wrongly forever.
func parseSchema(raw json.RawMessage) *jsonschema.Schema {
	if len(raw) == 0 {
		return &jsonschema.Schema{Type: jsonschema.Object}
	}
	var s jsonschema.Schema
	if err := json.Unmarshal(raw, &s); err != nil {
		return &jsonschema.Schema{Type: jsonschema.Object}
	}
	if s.Type == "" {
		s.Type = jsonschema.Object
	}
	return &s
}
