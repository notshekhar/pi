package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeHTTP is a remote MCP server for one protocol era. modern answers
// server/discover and stamps resultType; legacy refuses it and runs a
// session, answering tools/call over an SSE stream.
type fakeHTTP struct {
	modern bool
	mu     sync.Mutex
	seen   []http.Header
	bodies []map[string]any
	// deleted is set when the client ends its legacy session.
	deleted bool
}

func (f *fakeHTTP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodDelete {
		f.mu.Lock()
		f.deleted = r.Header.Get("Mcp-Session-Id") == "sess-1"
		f.mu.Unlock()
		return
	}
	if r.Method == http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	raw, _ := io.ReadAll(r.Body)
	var req map[string]any
	_ = json.Unmarshal(raw, &req)
	f.mu.Lock()
	f.seen, f.bodies = append(f.seen, r.Header.Clone()), append(f.bodies, req)
	f.mu.Unlock()
	method, _ := req["method"].(string)
	id := req["id"]
	if id == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result := func(v map[string]any) {
		if f.modern {
			v["resultType"] = "complete"
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": id, "result": v})
	}
	tools := []map[string]any{{"name": "lookup", "inputSchema": map[string]any{"type": "object",
		"properties": map[string]any{"region": map[string]any{"type": "string", "x-mcp-header": "Region"}}}}}
	switch method {
	case "server/discover":
		if !f.modern {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%v,"error":{"code":-32601,"message":"Method not found"}}`, id)
			return
		}
		result(map[string]any{"supportedVersions": []string{LatestProtocolVersion}, "capabilities": map[string]any{"tools": map[string]any{}},
			"_meta": map[string]any{metaServerInfo: map[string]any{"name": "modern-fake", "version": "2"}}})
	case "initialize":
		w.Header().Set("Mcp-Session-Id", "sess-1")
		result(map[string]any{"protocolVersion": LatestLegacyProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{}},
			"serverInfo": map[string]any{"name": "legacy-fake", "version": "1"}})
	case "tools/list":
		result(map[string]any{"tools": tools})
	case "tools/call":
		if f.modern {
			result(map[string]any{"content": []map[string]any{{"type": "text", "text": "modern ok"}}})
			return
		}
		// Legacy: answer as an SSE stream with a notification first.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/progress\",\"params\":{}}\n\n")
		fmt.Fprintf(w, "data: {\"jsonrpc\":\"2.0\",\"id\":%v,\"result\":{\"content\":[{\"type\":\"text\",\"text\":\"streamed ok\"}]}}\n\n", id)
	default:
		result(map[string]any{})
	}
}

func (f *fakeHTTP) headers(method string) http.Header {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.bodies {
		if b["method"] == method {
			return f.seen[i]
		}
	}
	return nil
}

func (f *fakeHTTP) body(method string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, b := range f.bodies {
		if b["method"] == method {
			return b
		}
	}
	return nil
}

func connectHTTP(t *testing.T, url, typ string) *Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c, err := Connect(ctx, ServerConfig{Name: "remote", Type: typ, URL: url}, "", ConnectOptions{})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	return c
}

// The modern era: server/discover instead of initialize, _meta on every
// request, the routing headers, and a tool argument bound to a header.
func TestHTTPModernEra(t *testing.T) {
	f := &fakeHTTP{modern: true}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := connectHTTP(t, srv.URL, "http")
	defer c.Close()
	if c.ProtocolVersion() != LatestProtocolVersion || c.Info().Name != "modern-fake" {
		t.Fatalf("version=%q info=%+v", c.ProtocolVersion(), c.Info())
	}
	if _, err := c.ListTools(context.Background()); err != nil {
		t.Fatal(err)
	}
	res, err := c.CallTool(context.Background(), "lookup", json.RawMessage(`{"region":"eu-west"}`))
	if err != nil || textOf(res) != "modern ok" {
		t.Fatalf("call = %+v, %v", res, err)
	}
	if f.body("initialize") != nil {
		t.Error("a modern server was sent initialize")
	}
	h := f.headers("tools/call")
	if h.Get("Mcp-Method") != "tools/call" || h.Get("Mcp-Name") != "lookup" || h.Get("Mcp-Param-Region") != "eu-west" {
		t.Errorf("headers = %v", h)
	}
	if h.Get("Mcp-Protocol-Version") != LatestProtocolVersion || h.Get("Mcp-Session-Id") != "" {
		t.Errorf("version/session headers = %v", h)
	}
	meta, _ := f.body("tools/call")["params"].(map[string]any)["_meta"].(map[string]any)
	if meta[metaProtocolVersion] != LatestProtocolVersion || meta[metaClientInfo] == nil {
		t.Errorf("_meta = %v", meta)
	}
}

// A server that does not know server/discover gets the legacy handshake, a
// session carried on every request, SSE replies read past notifications,
// and its session ended on close.
func TestHTTPLegacyFallback(t *testing.T) {
	f := &fakeHTTP{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := connectHTTP(t, srv.URL, "http")
	if c.ProtocolVersion() != LatestLegacyProtocolVersion || c.Info().Name != "legacy-fake" {
		t.Fatalf("version=%q info=%+v", c.ProtocolVersion(), c.Info())
	}
	res, err := c.CallTool(context.Background(), "lookup", nil)
	if err != nil || textOf(res) != "streamed ok" {
		t.Fatalf("call = %+v, %v", res, err)
	}
	if got := f.headers("tools/call").Get("Mcp-Session-Id"); got != "sess-1" {
		t.Errorf("session id not carried: %q", got)
	}
	if f.headers("tools/call").Get("Mcp-Param-Region") != "" {
		t.Error("legacy request carried modern headers")
	}
	c.Close()
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.deleted {
		t.Error("the session was not ended on close")
	}
}

// fakeSSE is the old HTTP+SSE transport: POST to the URL is refused, the GET
// stream names an endpoint and carries every reply.
func fakeSSE(t *testing.T) *httptest.Server {
	var mu sync.Mutex
	var stream chan string
	mux := http.NewServeMux()
	mux.HandleFunc("/sse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		ch := make(chan string, 16)
		mu.Lock()
		stream = ch
		mu.Unlock()
		fmt.Fprint(w, "event: endpoint\ndata: /messages?session=1\n\n")
		w.(http.Flusher).Flush()
		for {
			select {
			case msg := <-ch:
				fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
				w.(http.Flusher).Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	mux.HandleFunc("/messages", func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		w.WriteHeader(http.StatusAccepted)
		if req["id"] == nil {
			return
		}
		var result string
		switch req["method"] {
		case "initialize":
			result = `{"protocolVersion":"2024-11-05","capabilities":{"tools":{}},"serverInfo":{"name":"sse-fake","version":"0"}}`
		case "tools/list":
			result = `{"tools":[{"name":"hello","inputSchema":{"type":"object"}}]}`
		default:
			result = `{"content":[{"type":"text","text":"over sse"}]}`
		}
		id, _ := json.Marshal(req["id"])
		mu.Lock()
		ch := stream
		mu.Unlock()
		ch <- fmt.Sprintf(`{"jsonrpc":"2.0","id":%s,"result":%s}`, id, result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// An entry that says http for a server that only speaks SSE connects anyway:
// "http" vs "sse" is a guess, and getting it wrong must not look like a
// dead server.
func TestHTTPFallsBackToSSE(t *testing.T) {
	srv := fakeSSE(t)
	c := connectHTTP(t, srv.URL+"/sse", "http")
	defer c.Close()
	if c.Info().Name != "sse-fake" {
		t.Fatalf("info = %+v", c.Info())
	}
	res, err := c.CallTool(context.Background(), "hello", nil)
	if err != nil || textOf(res) != "over sse" {
		t.Fatalf("call = %+v, %v", res, err)
	}
}

// A 401 with no sign-in is needs-auth, not a broken server.
func TestHTTPUnauthorizedIsRecognised(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+"http://"+r.Host+`/.well-known/oauth-protected-resource"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()
	_, err := Connect(context.Background(), ServerConfig{Name: "locked", Type: "http", URL: srv.URL}, "", ConnectOptions{})
	if !IsUnauthorized(err) {
		t.Fatalf("err = %v, want unauthorized", err)
	}
}

func TestEncodeHeaderValue(t *testing.T) {
	for in, want := range map[string]string{
		"plain":          "plain",
		" pad":           "=?base64?IHBhZA==?=",
		"héllo":          "=?base64?aMOpbGxv?=",
		"=?x?=":          "=?x?=",
		"a\tb":           "a\tb",
		"=?base64?abc?=": "=?base64?PT9iYXNlNjQ/YWJjPz0=?=",
	} {
		if got := encodeHeaderValue(in); got != want {
			t.Errorf("encodeHeaderValue(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHeaderBindingsRejectUnreachable(t *testing.T) {
	var schema any
	_ = json.Unmarshal([]byte(`{"type":"object","properties":{"a":{"type":"array","items":{"type":"string","x-mcp-header":"A"}}}}`), &schema)
	if _, err := headerBindings(schema); err == nil || !strings.Contains(err.Error(), "statically reachable") {
		t.Errorf("err = %v", err)
	}
}
