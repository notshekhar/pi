package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/notshekhar/pi/internal/rpc"
)

func start(t *testing.T) *Handle {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LOOP_WEB_APP_DIR", t.TempDir()) // no index.html: 503, never a real UI
	h, err := Start(Options{Host: "127.0.0.1", Port: 47667, Version: "9.9.9", RPC: rpc.Host{}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(h.Stop)
	return h
}

func TestPairingTicketAndSocket(t *testing.T) {
	h := start(t)
	base := "http://127.0.0.1:47667"

	// No token: refused.
	if r, _ := http.Get(base + "/"); r.StatusCode != 401 {
		t.Fatalf("want 401, got %d", r.StatusCode)
	}
	// Environment is public.
	r, _ := http.Get(base + "/.well-known/loop/environment")
	var env map[string]any
	_ = json.NewDecoder(r.Body).Decode(&env)
	if env["environmentId"] != EnvironmentID(h.Token) || env["serverVersion"] != "9.9.9" {
		t.Fatalf("env %v", env)
	}
	// A code pairs once.
	code := h.PairingCode(true)
	form := url.Values{"grant_type": {tokenExchangeGrant}, "subject_token": {code.Code[:3] + " " + code.Code[3:]}}
	r, _ = http.PostForm(base+"/oauth/token", form)
	var tok map[string]any
	_ = json.NewDecoder(r.Body).Decode(&tok)
	if tok["access_token"] != h.Token {
		t.Fatalf("code exchange: %v", tok)
	}
	r, _ = http.PostForm(base+"/oauth/token", form)
	if r.StatusCode != 400 {
		t.Fatal("a used code must not pair again")
	}
	// Ticket → socket → an RPC round trip.
	req, _ := http.NewRequest("POST", base+"/api/auth/websocket-ticket", nil)
	req.Header.Set("authorization", "Bearer "+h.Token)
	r, _ = http.DefaultClient.Do(req)
	var ticket map[string]string
	_ = json.NewDecoder(r.Body).Decode(&ticket)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws://127.0.0.1:47667/ws?wsTicket="+ticket["ticket"], nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":1,"method":"hello","params":{}}`))
	_, data, err := conn.Read(ctx)
	if err != nil || !strings.Contains(string(data), `"id":1`) || strings.Contains(string(data), `"error"`) {
		t.Fatalf("hello: %s %v", data, err)
	}
	// artifact.* never crosses the network.
	_ = conn.Write(ctx, websocket.MessageText, []byte(`{"jsonrpc":"2.0","id":2,"method":"artifact.list","params":{}}`))
	_, data, _ = conn.Read(ctx)
	if !strings.Contains(string(data), "not available over loop serve") {
		t.Fatalf("artifact.list over serve: %s", data)
	}
	// The ticket was single-use.
	if _, _, err := websocket.Dial(ctx, "ws://127.0.0.1:47667/ws?wsTicket="+ticket["ticket"], nil); err == nil {
		t.Fatal("ticket reused")
	}
	// Token page load without a UI: 503 with loop's message.
	r, _ = http.Get(base + "/?token=" + h.Token)
	body, _ := io.ReadAll(r.Body)
	if r.StatusCode != 503 || !strings.Contains(string(body), "web UI is not built") {
		t.Fatalf("%d %s", r.StatusCode, body)
	}
}

func TestTerminalQR(t *testing.T) {
	q := TerminalQR("http://192.168.1.2:5667/?token=abc")
	if !strings.HasPrefix(q, "\x1b[30;47m") || strings.Count(q, "\n") < 10 {
		t.Fatalf("qr %q", q[:40])
	}
}
