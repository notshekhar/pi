package mcp

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// A minimal MCP server, compiled and run as a real subprocess. The stdio
// transport is where the real risk lives — framing, startup failure, the
// handshake order — and none of that is exercised by a fake transport.
const fakeServer = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func main() {
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     *int64          ` + "`json:\"id\"`" + `
			Method string          ` + "`json:\"method\"`" + `
			Params json.RawMessage ` + "`json:\"params\"`" + `
		}
		if json.Unmarshal(line, &req) != nil || req.ID == nil {
			continue // a notification: no reply
		}
		var result string
		switch req.Method {
		case "initialize":
			result = ` + "`" + `{"protocolVersion":"2024-11-05","capabilities":{},"serverInfo":{"name":"fake","version":"1.0"}}` + "`" + `
		case "tools/list":
			result = ` + "`" + `{"tools":[{"name":"echo","description":"Echo the input","inputSchema":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]}}]}` + "`" + `
		case "tools/call":
			var p struct {
				Arguments struct {
					Text string ` + "`json:\"text\"`" + `
				} ` + "`json:\"arguments\"`" + `
			}
			json.Unmarshal(req.Params, &p)
			out, _ := json.Marshal(p.Arguments.Text)
			result = ` + "`" + `{"content":[{"type":"text","text":` + "`" + ` + string(out) + ` + "`" + `}]}` + "`" + `
		default:
			result = "{}"
		}
		fmt.Printf(` + "`" + `{"jsonrpc":"2.0","id":%d,"result":%s}` + "`" + `+"\n", *req.ID, result)
	}
}
`

// buildFakeServer compiles the server above and returns its path.
func buildFakeServer(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "main.go")
	if err := os.WriteFile(src, []byte(fakeServer), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakeserver\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "server")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("building the fake server failed: %v\n%s", err, out)
	}
	return bin
}

func TestStdioEndToEnd(t *testing.T) {
	bin := buildFakeServer(t)
	ctx := context.Background()

	client, err := Connect(ctx, ServerConfig{Name: "fake", Command: bin}, t.TempDir())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer client.Close()

	if client.Info().Name != "fake" {
		t.Errorf("server info = %+v", client.Info())
	}

	tools, err := client.Tools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools) != 1 {
		t.Fatalf("got %d tools", len(tools))
	}
	// Namespaced for the model, and the schema survived the round trip.
	if tools[0].Name() != "fake__echo" {
		t.Errorf("tool name = %q", tools[0].Name())
	}
	if s := tools[0].InputSchema(); s == nil || s.Properties == nil {
		t.Errorf("schema lost: %+v", s)
	}

	res, err := tools[0].Execute(ctx, json.RawMessage(`{"text":"hello there"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.Output().(provider.ToolOutputText)
	if !ok || !strings.Contains(out.Value, "hello there") {
		t.Errorf("result = %#v", res.Output())
	}
}

// A server that dies on startup must report WHY, not just "EOF".
func TestStdioReportsStartupFailure(t *testing.T) {
	_, err := Connect(context.Background(), ServerConfig{
		Name: "broken", Command: "sh", Args: []string{"-c", "echo 'boom: missing API key' >&2; exit 1"},
	}, t.TempDir())
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "missing API key") {
		t.Errorf("stderr was not surfaced: %v", err)
	}
}
