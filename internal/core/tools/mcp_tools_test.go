package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/ai"
)

type echoArgs struct {
	Text string `json:"text"`
}

func fakeMCPTools() []ai.Tool {
	echo := ai.NewTool("mcp__fs__echo", "Echo text back.\nSecond line.", func(_ context.Context, a echoArgs) (ai.ToolResult, error) {
		return ai.ToolText("echo: " + a.Text), nil
	})
	issues := ai.NewTool("mcp__gh__list_issues", "List issues in a repository.", func(context.Context, echoArgs) (ai.ToolResult, error) {
		return ai.ToolText("none"), nil
	})
	return []ai.Tool{echo, issues}
}

func TestMCPToolSearchFlow(t *testing.T) {
	tool := MCPToolSearch(fakeMCPTools)
	out, _ := runTool(t, tool, map[string]any{"action": "search", "query": "issues"})
	if !strings.HasPrefix(out, "1 match:\nmcp__gh__list_issues — List issues") || strings.Contains(out, "echo") {
		t.Errorf("search = %q", out)
	}
	out, _ = runTool(t, tool, map[string]any{"action": "describe", "name": "mcp__fs__echo"})
	if !strings.Contains(out, "Echo text back.") || !strings.Contains(out, `"text"`) {
		t.Errorf("describe = %q", out)
	}
	out, failed := runTool(t, tool, map[string]any{"action": "call", "name": "mcp__fs__echo", "arguments": map[string]any{"text": "hi"}})
	if failed || out != "echo: hi" {
		t.Errorf("call = %q (failed=%v)", out, failed)
	}
	out, failed = runTool(t, tool, map[string]any{"action": "call", "name": "echo"})
	if !failed || !strings.Contains(out, "Did you mean: mcp__fs__echo?") {
		t.Errorf("miss = %q", out)
	}
	if out, _ := runTool(t, tool, map[string]any{"action": "search", "query": "nothing-here"}); !strings.Contains(out, "2 tools are connected") {
		t.Errorf("no match = %q", out)
	}
}

func TestScoreTool(t *testing.T) {
	if scoreTool("mcp__gh__list_issues", "List issues", "issues list") <= scoreTool("mcp__gh__x", "issues list", "issues list") {
		t.Error("a name hit should outrank a description hit")
	}
	if scoreTool("a", "b", "zzz") != 0 {
		t.Error("a missing term must exclude the tool")
	}
}
