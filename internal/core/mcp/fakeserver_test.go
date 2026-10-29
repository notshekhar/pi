package mcp

import (
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// A real stdio MCP server, compiled once and run as a subprocess: framing,
// interleaved messages, startup failure and shutdown are where a stdio
// transport breaks, and a fake transport exercises none of them.
//
// Behaviour is chosen by the tool called:
//
//	echo      returns its text
//	noisy     writes a log line to STDOUT and a notification before answering
//	pingme    asks the client a ping of its own and answers once it replies
//	grow      adds a tool and announces notifications/tools/list_changed
//	hang      never answers; a notifications/cancelled is logged to FAKE_LOG
//	die       exits without answering
//	spawn     starts `sleep 300` in the background, writing its pid to FAKE_PIDFILE
const fakeServerSrc = `package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
)

var out = bufio.NewWriter(os.Stdout)
var mu sync.Mutex

func send(v any) {
	mu.Lock()
	defer mu.Unlock()
	b, _ := json.Marshal(v)
	out.Write(append(b, '\n'))
	out.Flush()
}

func main() {
	tools := []map[string]any{}
	for _, n := range []string{"echo", "noisy", "pingme", "grow", "hang", "die", "spawn"} {
		tools = append(tools, map[string]any{"name": n, "description": n + " tool",
			"inputSchema": map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}}})
	}
	pongs := make(chan struct{}, 1)
	in := bufio.NewReader(os.Stdin)
	for {
		line, err := in.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID     json.RawMessage ` + "`json:\"id\"`" + `
			Method string          ` + "`json:\"method\"`" + `
			Params struct {
				Name      string ` + "`json:\"name\"`" + `
				Arguments struct {
					Text string ` + "`json:\"text\"`" + `
				} ` + "`json:\"arguments\"`" + `
			} ` + "`json:\"params\"`" + `
			Result json.RawMessage ` + "`json:\"result\"`" + `
		}
		if json.Unmarshal(line, &req) != nil {
			continue
		}
		if req.Method == "" { // a reply to our own request
			pongs <- struct{}{}
			continue
		}
		if req.Method == "notifications/cancelled" {
			if f, err := os.OpenFile(os.Getenv("FAKE_LOG"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				f.Write(line)
				f.Close()
			}
			continue
		}
		if len(req.ID) == 0 {
			continue
		}
		reply := func(result any) { send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}) }
		text := func(s string) map[string]any {
			return map[string]any{"content": []map[string]any{{"type": "text", "text": s}}}
		}
		switch req.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{"listChanged": true}},
				"serverInfo": map[string]any{"name": "fake", "version": "1.0"}, "instructions": "use echo"})
		case "tools/list":
			reply(map[string]any{"tools": tools})
		case "tools/call":
			switch req.Params.Name {
			case "echo":
				reply(text(req.Params.Arguments.Text))
			case "noisy":
				mu.Lock()
				out.WriteString("starting up... not json\n")
				out.Flush()
				mu.Unlock()
				send(map[string]any{"jsonrpc": "2.0", "method": "notifications/message", "params": map[string]any{"level": "info", "data": "hi"}})
				reply(text("quiet now"))
			case "pingme":
				go func(id json.RawMessage) {
					send(map[string]any{"jsonrpc": "2.0", "id": "srv-1", "method": "ping"})
					<-pongs
					send(map[string]any{"jsonrpc": "2.0", "id": id, "result": text("pong received")})
				}(req.ID)
			case "grow":
				tools = append(tools, map[string]any{"name": "extra", "inputSchema": map[string]any{"type": "object"}})
				reply(text("grown"))
				send(map[string]any{"jsonrpc": "2.0", "method": "notifications/tools/list_changed"})
			case "hang":
			case "die":
				os.Exit(3)
			case "spawn":
				cmd := exec.Command("sleep", "300")
				cmd.Start()
				os.WriteFile(os.Getenv("FAKE_PIDFILE"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
				reply(text("spawned"))
			default:
				send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32602, "message": "unknown tool"}})
			}
		case "ping":
			reply(map[string]any{})
		default:
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
		}
		_ = fmt.Sprint
	}
}
`

var (
	fakeOnce sync.Once
	fakeDir  string
	fakeBin  string
	fakeErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if fakeDir != "" {
		os.RemoveAll(fakeDir)
	}
	os.Exit(code)
}

// fakeServer builds the server above once per test binary.
func fakeServer(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	fakeOnce.Do(func() {
		dir, err := os.MkdirTemp("", "pi-mcp-fake-")
		if err != nil {
			fakeErr = err
			return
		}
		fakeDir = dir
		_ = os.WriteFile(filepath.Join(dir, "main.go"), []byte(fakeServerSrc), 0o600)
		_ = os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fakeserver\n\ngo 1.21\n"), 0o600)
		fakeBin = filepath.Join(dir, "server")
		cmd := exec.Command("go", "build", "-o", fakeBin, ".")
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			fakeErr = err
			fakeBin = string(out)
		}
	})
	if fakeErr != nil {
		t.Fatalf("building the fake server: %v\n%s", fakeErr, fakeBin)
	}
	return fakeBin
}

// fakeConfig is a stdio entry for the fake server.
func fakeConfig(t *testing.T, name string) ServerConfig {
	return ServerConfig{Name: name, Command: fakeServer(t)}
}
