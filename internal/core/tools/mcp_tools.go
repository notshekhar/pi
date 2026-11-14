package tools

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/jsonopt"
)

// `mcp_tools` — loop's tool-search mode (tools/mcp-tools.ts). With more MCP
// tools than are worth listing in every request, they are reached through
// this one tool instead: search, describe, call.

// maxSearchResultsMCP caps one search's list.
const maxSearchResultsMCP = 25

// summaryChars caps a search result's one-line summary.
const summaryChars = 160

type mcpToolsArgs struct {
	Action    string         `json:"action"`
	Query     string         `json:"query,omitempty"`
	Name      string         `json:"name,omitempty"`
	Arguments jsontext.Value `json:"arguments,omitzero"`
}

// MCPToolSearch returns the mcp_tools tool over the live MCP tool set.
func MCPToolSearch(list func() []ai.Tool) ai.Tool {
	return LoopTool("mcp_tools", func(ctx context.Context, a mcpToolsArgs) (ai.ToolResult, error) {
		all := list()
		if a.Action == "search" {
			return ai.ToolText(searchMCPTools(all, a.Query)), nil
		}
		tool, err := findMCPTool(all, a.Action, a.Name)
		if err != nil {
			return fail(err)
		}
		if a.Action == "describe" {
			return ai.ToolText(describeMCPTool(tool)), nil
		}
		args := a.Arguments
		if len(args) == 0 {
			args = jsontext.Value("{}")
		}
		return tool.Execute(ctx, args)
	})
}

type scoredTool struct {
	name, summary string
	score         int
}

func searchMCPTools(all []ai.Tool, query string) string {
	var scored []scoredTool
	for _, t := range all {
		if score := scoreTool(t.Name(), t.Description(), query); score > 0 {
			scored = append(scored, scoredTool{t.Name(), summarize(t.Description()), score})
		}
	}
	if len(scored) == 0 {
		return fmt.Sprintf("No MCP tools match %q. %d tools are connected — try a broader term.", query, len(all))
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].name < scored[j].name
	})
	shown := scored[:min(len(scored), maxSearchResultsMCP)]
	lines := make([]string, len(shown))
	for i, s := range shown {
		lines[i] = s.name
		if s.summary != "" {
			lines[i] += " — " + s.summary
		}
	}
	plural := "es"
	if len(scored) == 1 {
		plural = ""
	}
	out := fmt.Sprintf("%d match%s:\n%s", len(scored), plural, strings.Join(lines, "\n"))
	if more := len(scored) - len(shown); more > 0 {
		out += fmt.Sprintf("\n… %d more — narrow the query", more)
	}
	return out
}

// scoreTool is loop's: every term must appear in the name or description;
// a name hit counts double, an exact name more.
func scoreTool(name, description, query string) int {
	terms := strings.Fields(strings.ToLower(query))
	if len(terms) == 0 {
		return 1
	}
	lowerName, lowerDescription := strings.ToLower(name), strings.ToLower(description)
	score := 0
	for _, term := range terms {
		inName, inDescription := strings.Contains(lowerName, term), strings.Contains(lowerDescription, term)
		switch {
		case !inName && !inDescription:
			return 0
		case inName:
			score += 2
		default:
			score++
		}
		if lowerName == term {
			score += 3
		}
	}
	return score
}

// summarize is a description's first non-blank line, capped.
func summarize(description string) string {
	for _, line := range strings.Split(description, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if r := []rune(line); len(r) > summaryChars {
			return string(r[:summaryChars-1]) + "…"
		}
		return line
	}
	return ""
}

func findMCPTool(all []ai.Tool, action, name string) (ai.Tool, error) {
	if name == "" {
		return nil, fmt.Errorf("mcp_tools: `name` is required for action %q — run `search` first.", action)
	}
	var near []string
	for _, t := range all {
		if t.Name() == name {
			return t, nil
		}
		if len(near) < 5 && (strings.Contains(t.Name(), name) || strings.Contains(name, t.Name())) {
			near = append(near, t.Name())
		}
	}
	msg := fmt.Sprintf("mcp_tools: no tool named %q.", name)
	if len(near) > 0 {
		msg += " Did you mean: " + strings.Join(near, ", ") + "?"
	}
	return nil, fmt.Errorf("%s", msg)
}

func describeMCPTool(t ai.Tool) string {
	description := t.Description()
	if description == "" {
		description = "(no description)"
	}
	schema := "(no parameters)"
	if t.InputSchema() != nil {
		if raw, err := json.Marshal(t.InputSchema(), jsonopt.Write, jsontext.WithIndent("  ")); err == nil {
			schema = string(raw)
		}
	}
	return strings.Join([]string{t.Name(), description, "", schema}, "\n")
}
