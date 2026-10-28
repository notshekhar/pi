// Package serve is loop's `loop serve` (rpc/serve.ts): the RPC server over
// WebSocket — one JSON message per text frame, the same protocol as stdio —
// plus the embedded browser UI and the pairing routes other devices use.
//
// Possession of the token is full control of this machine. It is required on
// the socket upgrade AND the page load; a page opened with it keeps it as an
// HttpOnly, SameSite=Strict cookie.
package serve

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/rpc"
)

// DefaultPort is keypad-spellable: 5667 = "loop".
const DefaultPort = 5667

const tokenCookie = "loop_serve"

// Token is the stored serve token (auth.json serveToken), created once so
// the printed URL stays stable across restarts.
func Token() string {
	var t string
	if config.AuthStore().Get("serveToken", &t) && t != "" {
		return t
	}
	t = randomHex(24)
	_ = config.AuthStore().Set("serveToken", t)
	return t
}

func tokenMatches(candidate, token string) bool {
	if candidate == "" {
		return false
	}
	a, b := sha256.Sum256([]byte(candidate)), sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(a[:], b[:]) == 1
}

func cookieToken(r *http.Request) string {
	if c, err := r.Cookie(tokenCookie); err == nil {
		return c.Value
	}
	return ""
}

// RemoteTerminalFor is loop's: on unless the setting says otherwise, with
// --terminal / --no-terminal overriding it for one run (--no-terminal wins).
func RemoteTerminalFor(terminal, noTerminal bool, setting *bool) bool {
	if noTerminal {
		return false
	}
	if terminal {
		return true
	}
	return setting == nil || *setting
}

// Options configures a server.
type Options struct {
	Host           string
	Port           int
	WebAppDir      string
	RemoteTerminal bool
	Version        string
	// RPC is the host the RPC server is built around (Remote is forced on).
	RPC rpc.Host
}

// Handle is a running server (loop's ServeHandle).
type Handle struct {
	Hostname    string
	Port        int
	Token       string
	URL         string
	NetworkURLs []string
	// RPC is the server behind the socket: PublishLive, SetLiveRunning,
	// OpenSession, IsRunning, Watch and CancelOwnTurn are its live feed.
	RPC *rpc.Server

	pairing *pairing
	http    *http.Server
	ws      *workspace
}

// PairingCode is the code still good, or a fresh one (or when fresh).
func (h *Handle) PairingCode(fresh bool) PairingCode {
	if !fresh {
		if c := h.pairing.currentCode(); c != nil {
			return *c
		}
	}
	return h.pairing.issueCode()
}

// Stop closes the sockets and disposes the RPC server.
func (h *Handle) Stop() {
	h.ws.dispose()
	h.RPC.Dispose()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = h.http.Shutdown(ctx)
}

type wsTransport struct {
	mu   sync.Mutex
	conn *websocket.Conn
	dead bool
}

func (t *wsTransport) Send(msg any) {
	raw, err := json.Marshal(msg)
	if err != nil {
		return
	}
	t.send(raw)
}

func (t *wsTransport) send(raw []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.dead {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if t.conn.Write(ctx, websocket.MessageText, raw) != nil {
		t.dead = true
	}
}

// Start is loop's startWebServer.
func Start(o Options) (*Handle, error) {
	host := o.Host
	if host == "" {
		host = "127.0.0.1"
	}
	port := o.Port
	if port == 0 {
		port = DefaultPort
	}
	token := Token()
	version := strings.TrimSpace(o.Version)
	if version == "" {
		version = "dev"
	}
	o.RPC.Remote = true
	o.RPC.Version = version
	server := rpc.NewServer(o.RPC)

	var (
		socketsMu sync.Mutex
		sockets   = map[*wsTransport]bool{}
	)
	ws := newWorkspace(server, func(channel string, payload any) {
		frame, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": workspaceEvent,
			"params": map[string]any{"channel": channel, "payload": payload}})
		socketsMu.Lock()
		list := make([]*wsTransport, 0, len(sockets))
		for s := range sockets {
			list = append(list, s)
		}
		socketsMu.Unlock()
		for _, s := range list {
			s.send(frame)
		}
	})
	pair := newPairing(token, version, func(c string) bool { return tokenMatches(c, token) })

	var (
		appOnce sync.Once
		app     webApp
	)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if pair.handle(w, r) {
			return
		}
		q := r.URL.Query()
		fromQuery := tokenMatches(q.Get("token"), token)
		fromTicket := r.URL.Path == "/ws" && pair.redeemTicket(q.Get("wsTicket"))
		if !fromQuery && !fromTicket && !tokenMatches(cookieToken(r), token) && !tokenMatches(bearerToken(r), token) {
			http.Error(w, "Unauthorized: token required (start with `serve` and use the printed URL)", http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/ws" {
			remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
			terminal := IsLoopbackHost(remoteIP) || o.RemoteTerminal
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			conn.SetReadLimit(256 << 20)
			t := &wsTransport{conn: conn}
			feed, closeFn := server.Attach(t)
			socketsMu.Lock()
			sockets[t] = true
			socketsMu.Unlock()
			defer func() {
				socketsMu.Lock()
				delete(sockets, t)
				socketsMu.Unlock()
				closeFn()
				t.mu.Lock()
				t.dead = true
				t.mu.Unlock()
				_ = conn.CloseNow()
			}()
			for {
				typ, data, err := conn.Read(context.Background())
				if err != nil {
					return
				}
				if typ != websocket.MessageText {
					continue
				}
				// workspace.* is the desktop's host table, answered here;
				// everything else is the RPC server's.
				if req := parseWorkspaceRequest(data); req != nil {
					go func() {
						result, err := ws.call(req.method, req.params, terminal)
						var resp map[string]any
						if err != nil {
							resp = map[string]any{"jsonrpc": "2.0", "id": req.id, "error": map[string]any{"code": -32603, "message": err.Error()}}
						} else {
							resp = map[string]any{"jsonrpc": "2.0", "id": req.id, "result": result}
						}
						raw, _ := json.Marshal(resp)
						t.send(raw)
					}()
					continue
				}
				feed(string(data))
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		appOnce.Do(func() { app = loadWebApp(o.WebAppDir) })
		if app == nil {
			w.Header().Set("content-type", "text/plain; charset=utf-8")
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(webAppMissingText))
			return
		}
		if fromQuery {
			// Set only for HTML; written before the body either way.
			if path := r.URL.Path; strings.HasSuffix(path, ".html") || !strings.Contains(path[strings.LastIndex(path, "/")+1:], ".") {
				http.SetCookie(w, &http.Cookie{Name: tokenCookie, Value: token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			}
		}
		serveWebApp(app, w, r.URL.Path)
	})

	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return nil, err
	}
	hs := &http.Server{Handler: mux, ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = err
		}
	}()
	bound := ln.Addr().(*net.TCPAddr).Port
	wildcard := host == "0.0.0.0" || host == "::"
	local := host
	if wildcard {
		local = "127.0.0.1"
	}
	var networkHosts []string
	switch {
	case wildcard:
		networkHosts = LANAddresses()
	case !IsLoopbackHost(host):
		networkHosts = []string{host}
	}
	h := &Handle{Hostname: host, Port: bound, Token: token, RPC: server, pairing: pair, http: hs, ws: ws,
		URL: fmt.Sprintf("http://%s/?token=%s", net.JoinHostPort(local, strconv.Itoa(bound)), token)}
	for _, nh := range networkHosts {
		h.NetworkURLs = append(h.NetworkURLs, fmt.Sprintf("http://%s/?token=%s", net.JoinHostPort(nh, strconv.Itoa(bound)), token))
	}
	return h, nil
}
