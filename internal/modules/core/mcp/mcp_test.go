package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// fakeTransport replays scripted replies, so the client can be exercised
// without a real server.
type fakeTransport struct {
	sent    [][]byte
	replies []string
	at      int
	err     error
}

func (f *fakeTransport) Send(_ context.Context, payload []byte, wantReply bool) ([]byte, error) {
	f.sent = append(f.sent, payload)
	if f.err != nil {
		return nil, f.err
	}
	if !wantReply {
		return nil, nil
	}
	if f.at >= len(f.replies) {
		return nil, errors.New("no scripted reply")
	}
	reply := f.replies[f.at]
	f.at++
	// Echo the request id back, so the client's correlation check passes.
	var req struct {
		ID int64 `json:"id"`
	}
	json.Unmarshal(payload, &req)
	return []byte(strings.ReplaceAll(reply, `"id":0`, `"id":`+itoa(req.ID))), nil
}

func (f *fakeTransport) Close() error { return nil }

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func clientWith(replies ...string) (*Client, *fakeTransport) {
	f := &fakeTransport{replies: replies}
	return &Client{Name: "test", transport: f}, f
}

func TestListToolsFollowsPagination(t *testing.T) {
	c, _ := clientWith(
		`{"jsonrpc":"2.0","id":0,"result":{"tools":[{"name":"a","description":"first"}],"nextCursor":"c1"}}`,
		`{"jsonrpc":"2.0","id":0,"result":{"tools":[{"name":"b","description":"second"}]}}`,
	)
	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" {
		t.Errorf("tools = %+v", got)
	}
}

// A server that returns its own cursor forever would otherwise loop.
func TestListToolsStopsOnRepeatedCursor(t *testing.T) {
	page := `{"jsonrpc":"2.0","id":0,"result":{"tools":[{"name":"a"}],"nextCursor":"same"}}`
	c, _ := clientWith(page, page, page)
	got, err := c.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 2 {
		t.Errorf("looped: got %d tools", len(got))
	}
}

func TestCallToolSurfacesServerErrors(t *testing.T) {
	c, _ := clientWith(`{"jsonrpc":"2.0","id":0,"error":{"code":-32602,"message":"bad arguments"}}`)
	_, err := c.CallTool(context.Background(), "x", nil)
	if err == nil || !strings.Contains(err.Error(), "bad arguments") {
		t.Errorf("err = %v", err)
	}
}

func TestCallRejectsMismatchedID(t *testing.T) {
	// A reply carrying someone else's id must not be accepted as ours.
	f := &fakeTransport{replies: []string{`{"jsonrpc":"2.0","id":999,"result":{}}`}}
	c := &Client{Name: "test", transport: f}
	if err := c.call(context.Background(), "x", nil, &struct{}{}); err == nil {
		t.Error("expected a correlation error")
	}
}

func TestCallToolResultText(t *testing.T) {
	r := CallToolResult{Content: []Content{
		{Type: "text", Text: "first"},
		{Type: "image", MimeType: "image/png"},
		{Type: "text", Text: "second"},
	}}
	got := r.Text()
	if !strings.Contains(got, "first") || !strings.Contains(got, "second") {
		t.Errorf("text lost: %q", got)
	}
	// Non-text content is announced rather than dropped, so the model knows
	// something came back it cannot see.
	if !strings.Contains(got, "image") {
		t.Errorf("image content vanished silently: %q", got)
	}
}

func TestToolNaming(t *testing.T) {
	name := ToolName("github", "search")
	if name != "github__search" {
		t.Errorf("ToolName = %q", name)
	}
	server, tool, ok := SplitToolName(name)
	if !ok || server != "github" || tool != "search" {
		t.Errorf("split = %q/%q ok=%v", server, tool, ok)
	}
	if _, _, ok := SplitToolName("nodelimiter"); ok {
		t.Error("a bare name should not split")
	}
}

// Names go to providers that only accept a restricted character set.
func TestToolNameSanitisesTheServer(t *testing.T) {
	got := ToolName("my server/v2", "run")
	if strings.ContainsAny(got, " /") {
		t.Errorf("unsanitised name %q", got)
	}
}

func TestBridgedToolCallsThrough(t *testing.T) {
	c, f := clientWith(`{"jsonrpc":"2.0","id":0,"result":{"content":[{"type":"text","text":"done"}]}}`)
	tool := &bridgedTool{client: c, remote: "search", name: "test__search"}

	res, err := tool.Execute(context.Background(), json.RawMessage(`{"q":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	out, ok := res.Output().(provider.ToolOutputText)
	if !ok || out.Value != "done" {
		t.Errorf("output = %#v", res.Output())
	}
	// The REMOTE name goes on the wire, not the namespaced one.
	if !strings.Contains(string(f.sent[0]), `"name":"search"`) {
		t.Errorf("wrong tool name sent: %s", f.sent[0])
	}
}

// A flaky server should cost one tool call, not the whole run.
func TestBridgedToolReportsTransportFailureToTheModel(t *testing.T) {
	f := &fakeTransport{err: errors.New("connection reset")}
	tool := &bridgedTool{client: &Client{Name: "t", transport: f}, remote: "x", name: "t__x"}

	res, err := tool.Execute(context.Background(), nil)
	if err != nil {
		t.Fatalf("a transport failure must not abort the run: %v", err)
	}
	out, ok := res.Output().(provider.ToolOutputErrorText)
	if !ok || !strings.Contains(out.Value, "connection reset") {
		t.Errorf("output = %#v", res.Output())
	}
}

func TestBridgedToolPropagatesIsError(t *testing.T) {
	c, _ := clientWith(`{"jsonrpc":"2.0","id":0,"result":{"content":[{"type":"text","text":"nope"}],"isError":true}}`)
	tool := &bridgedTool{client: c, remote: "x", name: "t__x"}
	res, _ := tool.Execute(context.Background(), nil)
	if _, ok := res.Output().(provider.ToolOutputErrorText); !ok {
		t.Errorf("isError was not propagated: %#v", res.Output())
	}
}

// nil schema means "takes no arguments", which would make the model call an
// argument-taking tool wrongly forever.
func TestParseSchemaNeverReturnsNil(t *testing.T) {
	for _, raw := range []json.RawMessage{nil, json.RawMessage(``), json.RawMessage(`{bad`)} {
		got := parseSchema(raw)
		if got == nil {
			t.Fatalf("parseSchema(%q) returned nil", raw)
		}
		if got.Type == "" {
			t.Errorf("parseSchema(%q) has no type", raw)
		}
	}
}

func TestParseSchemaKeepsProperties(t *testing.T) {
	raw := json.RawMessage(`{"type":"object","properties":{"q":{"type":"string"}},"required":["q"]}`)
	got := parseSchema(raw)
	if got.Properties == nil {
		t.Fatal("properties were dropped")
	}
	if len(got.Required) != 1 || got.Required[0] != "q" {
		t.Errorf("required = %v", got.Required)
	}
}

func TestExtractSSEData(t *testing.T) {
	body := []byte("event: message\ndata: {\"jsonrpc\":\"2.0\"}\n\n")
	if got := string(extractSSEData(body)); got != `{"jsonrpc":"2.0"}` {
		t.Errorf("extractSSEData = %q", got)
	}
}

func TestConnectRejectsEmptyConfig(t *testing.T) {
	if _, err := Connect(context.Background(), ServerConfig{Name: "x"}, ""); err == nil {
		t.Error("a config with neither command nor url should fail")
	}
}

// One broken server must not stop a session.
func TestManagerSurvivesABrokenServer(t *testing.T) {
	m := NewManager()
	m.Connect(context.Background(), []ServerConfig{
		{Name: "broken", Command: "definitely-not-a-real-binary-xyz"},
		{Name: "skipped", Command: "echo", Disabled: true},
	}, "")

	status := m.Status()
	if len(status) != 2 {
		t.Fatalf("got %d statuses", len(status))
	}
	var broken, skipped bool
	for _, s := range status {
		if s.Name == "broken" && s.Err != nil {
			broken = true
		}
		if s.Name == "skipped" && s.Skipped {
			skipped = true
		}
	}
	if !broken {
		t.Error("the broken server did not report an error")
	}
	if !skipped {
		t.Error("the disabled server was not skipped")
	}
	if len(m.Tools()) != 0 {
		t.Error("a failed server contributed tools")
	}
}

func TestLimitedWriterCaps(t *testing.T) {
	var sb strings.Builder
	w := &limitedWriter{w: &sb, remaining: 10}
	w.Write([]byte(strings.Repeat("x", 100)))
	w.Write([]byte(strings.Repeat("y", 100)))
	if sb.Len() != 10 {
		t.Errorf("wrote %d bytes, want the 10-byte cap", sb.Len())
	}
}
