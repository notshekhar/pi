package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fakeOAuth is a protected MCP server and its authorization server on one
// origin: RFC 9728 metadata, RFC 8414 metadata, dynamic registration, PKCE
// code exchange and refresh.
type fakeOAuth struct {
	srv *httptest.Server

	mu        sync.Mutex
	valid     string // the access token /mcp accepts
	challenge string
	refreshes int
}

func newFakeOAuth(t *testing.T) *fakeOAuth {
	f := &fakeOAuth{valid: "tok-1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-protected-resource/mcp", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"resource": f.srv.URL + "/mcp", "authorization_servers": []string{f.srv.URL}, "scopes_supported": []string{"mcp"}})
	})
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"issuer": f.srv.URL, "authorization_endpoint": f.srv.URL + "/authorize",
			"token_endpoint": f.srv.URL + "/token", "registration_endpoint": f.srv.URL + "/register",
			"response_types_supported": []string{"code"}, "code_challenge_methods_supported": []string{"S256"}})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{"client_id": "cid-1", "redirect_uris": []string{"x"}})
	})
	mux.HandleFunc("/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("client_id") != "cid-1" || q.Get("code_challenge_method") != "S256" || q.Get("resource") != f.srv.URL+"/mcp" || q.Get("state") == "" {
			http.Error(w, "bad authorize request: "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.challenge = q.Get("code_challenge")
		f.mu.Unlock()
		http.Redirect(w, r, q.Get("redirect_uri")+"?code=code-1&state="+url.QueryEscape(q.Get("state")), http.StatusFound)
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "code-1" || base64.RawURLEncoding.EncodeToString(sum[:]) != f.challenge {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": "invalid_grant"})
				return
			}
			writeJSON(w, map[string]any{"access_token": "tok-1", "token_type": "Bearer", "refresh_token": "ref-1"})
		case "refresh_token":
			if r.Form.Get("refresh_token") != "ref-1" {
				w.WriteHeader(http.StatusBadRequest)
				writeJSON(w, map[string]any{"error": "invalid_grant"})
				return
			}
			f.refreshes++
			// No refresh_token in the answer: "keep using the one you have".
			writeJSON(w, map[string]any{"access_token": "tok-2", "token_type": "Bearer"})
		}
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok := r.Header.Get("Authorization") == "Bearer "+f.valid
		f.mu.Unlock()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="`+f.srv.URL+`/.well-known/oauth-protected-resource/mcp", scope="mcp"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["id"] == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any = map[string]any{}
		switch req["method"] {
		case "server/discover":
			writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req["id"], "error": map[string]any{"code": -32601, "message": "no"}})
			return
		case "initialize":
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}},
				"serverInfo": map[string]any{"name": "locked", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{}}
		}
		writeJSON(w, map[string]any{"jsonrpc": "2.0", "id": req["id"], "result": result})
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// isolateAuth points HOME at a scratch directory and the login callback at
// a free high port, so a test never touches the real ~/.loop or a port a
// live session may hold.
func isolateAuth(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	t.Setenv("LOOP_MCP_OAUTH_CALLBACK_PORT", strconv.Itoa(port))
}

// follow plays the browser: open the authorization URL and follow the
// redirect back to the callback server.
func follow(t *testing.T) func(string) {
	return func(u string) {
		go func() {
			resp, err := http.Get(u)
			if err != nil {
				t.Errorf("browser: %v", err)
				return
			}
			resp.Body.Close()
		}()
	}
}

func TestOAuthLoginRefreshAndLoopShape(t *testing.T) {
	isolateAuth(t)
	f := newFakeOAuth(t)
	cfg := ServerConfig{Name: "locked", Type: "http", URL: f.srv.URL + "/mcp"}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Unsigned: needs auth, and nothing was registered behind our back.
	if _, err := Connect(ctx, cfg, "", ConnectOptions{}); !IsUnauthorized(err) {
		t.Fatalf("anonymous connect: %v", err)
	}
	if HasStoredTokens("locked") {
		t.Fatal("tokens before any login")
	}

	if err := Authorize(ctx, "locked", cfg, follow(t)); err != nil {
		t.Fatalf("authorize: %v", err)
	}
	stored := readAuth("locked")
	if stored.ClientInformation["client_id"] != "cid-1" || stored.Tokens["access_token"] != "tok-1" ||
		stored.Tokens["authorization_server"] == nil || stored.Tokens["refresh_token"] != "ref-1" {
		t.Fatalf("stored session = %+v", stored)
	}
	// loop reads the same file: the session sits under the server's name in
	// ~/.loop/mcp-auth.json.
	raw, err := os.ReadFile(AuthStorePath())
	if err != nil {
		t.Fatal(err)
	}
	var file map[string]map[string]any
	if json.Unmarshal(raw, &file) != nil || file["locked"]["tokens"] == nil {
		t.Fatalf("token file = %s", raw)
	}

	c, err := Connect(ctx, cfg, "", ConnectOptions{})
	if err != nil {
		t.Fatalf("connect after login: %v", err)
	}
	c.Close()

	// The server rotates the token: the next connect refreshes silently and
	// keeps the refresh token it was not sent again.
	f.mu.Lock()
	f.valid = "tok-2"
	f.mu.Unlock()
	c, err = Connect(ctx, cfg, "", ConnectOptions{})
	if err != nil {
		t.Fatalf("connect after rotation: %v", err)
	}
	c.Close()
	after := readAuth("locked")
	if after.Tokens["access_token"] != "tok-2" || after.Tokens["refresh_token"] != "ref-1" || f.refreshes != 1 {
		t.Errorf("after refresh: %+v (refreshes %d)", after.Tokens, f.refreshes)
	}
}

// A login that does not finish puts the previous session back.
func TestOAuthFailedLoginRestoresTheSession(t *testing.T) {
	isolateAuth(t)
	if err := restoreAuth("svc", StoredAuth{Tokens: map[string]any{"access_token": "old"}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Authorize(ctx, "svc", ServerConfig{Name: "svc", Type: "http", URL: srv.URL}, func(string) {})
	if err == nil {
		t.Fatal("a login against a server with no OAuth succeeded")
	}
	if readAuth("svc").Tokens["access_token"] != "old" {
		t.Error("the previous session was not restored")
	}
}

// A dotted server name nests the way loop's configstore files it.
func TestOAuthDottedNamesNest(t *testing.T) {
	isolateAuth(t)
	if err := restoreAuth("a.b", StoredAuth{Tokens: map[string]any{"access_token": "x"}}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(AuthStorePath())
	var file map[string]map[string]map[string]any
	if err := json.Unmarshal(raw, &file); err != nil || file["a"]["b"]["tokens"] == nil {
		t.Fatalf("file = %s (%v)", raw, err)
	}
	if !HasStoredTokens("a.b") {
		t.Error("dotted name not read back")
	}
	if err := ClearAuth("a.b"); err != nil || HasStoredTokens("a.b") {
		t.Errorf("clear: %v", err)
	}
}

func TestWWWAuthenticateParams(t *testing.T) {
	meta, scope := wwwAuthenticateParams(`Bearer error="invalid_token", resource_metadata="https://x.test/.well-known/oauth-protected-resource", scope="a b"`)
	if meta == nil || meta.Host != "x.test" || scope != "a b" {
		t.Errorf("meta=%v scope=%q", meta, scope)
	}
	if m, s := wwwAuthenticateParams(`Basic realm="x"`); m != nil || s != "" {
		t.Errorf("basic challenge parsed: %v %q", m, s)
	}
	_ = fmt.Sprint
}
