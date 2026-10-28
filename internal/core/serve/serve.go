// Package serve exposes a session over HTTP, so a run can be watched from a
// browser or driven by a script.
//
// HTTP + Server-Sent Events, not WebSocket. loop uses WS; here that would
// mean implementing RFC 6455's handshake and frame masking by hand, because
// the standard library has no WebSocket and this repo takes no dependencies.
// SSE gets the same thing — a server-pushed stream of events — out of plain
// `http.Flusher`, and the direction it lacks (client→server) is a POST.
//
// It binds to LOOPBACK by default and requires a token. An agent that can run
// shell commands must not be reachable from the network because someone typed
// a port number.
package serve

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// DefaultPort is what `/serve` binds when none is given.
const DefaultPort = 4517

// Event is one thing that happened, pushed to every listener.
type Event struct {
	Kind string `json:"kind"`
	Text string `json:"text,omitempty"`
	At   string `json:"at"`
}

// Server is a running listener.
type Server struct {
	Token string
	Addr  string

	http     *http.Server
	mu       sync.Mutex
	clients  map[chan Event]struct{}
	onPrompt func(string)
}

// New builds a server. `onPrompt` receives prompts posted by a client.
func New(onPrompt func(string)) *Server {
	return &Server{
		Token:    newToken(),
		clients:  map[chan Event]struct{}{},
		onPrompt: onPrompt,
	}
}

// newToken makes a bearer token.
//
// crypto/rand, not math/rand: this token is the only thing between a stranger
// on the machine and a shell.
func newToken() string {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		// Refusing to serve beats serving with a guessable token.
		return ""
	}
	return hex.EncodeToString(buf)
}

// Start binds and serves.
//
// `host` empty means loopback. `port` 0 means the default; a negative port
// means any free one.
func (s *Server) Start(host string, port int) error {
	if s.Token == "" {
		return fmt.Errorf("serve: could not generate a token")
	}
	if host == "" {
		host = "127.0.0.1"
	}
	// 0 means "the caller did not choose", which is the default port. A
	// NEGATIVE port means "any free one" — the distinction matters because
	// tests need an ephemeral port, and a test that asks for 0 and silently
	// gets 4517 collides with every other test in the package the moment two
	// run close together.
	switch {
	case port == 0:
		port = DefaultPort
	case port < 0:
		port = 0 // what the OS reads as "pick one"
	}
	addr := net.JoinHostPort(host, fmt.Sprint(port))

	mux := http.NewServeMux()
	mux.HandleFunc("/events", s.auth(s.handleEvents))
	mux.HandleFunc("/prompt", s.auth(s.handlePrompt))
	mux.HandleFunc("/health", s.auth(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}
	s.Addr = ln.Addr().String()
	s.http = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go s.http.Serve(ln)
	return nil
}

// Stop shuts the listener down and releases every stream.
func (s *Server) Stop() {
	if s.http != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		s.http.Shutdown(ctx)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		close(ch)
		delete(s.clients, ch)
	}
}

// Publish pushes an event to every listener.
//
// Non-blocking per client: a browser that has stopped reading must not stall
// the agent. A client that cannot keep up loses events, which is the right
// trade for a view onto a run.
func (s *Server) Publish(e Event) {
	e.At = time.Now().Format(time.RFC3339)
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.clients {
		select {
		case ch <- e:
		default:
		}
	}
}

// auth requires the bearer token, compared in constant time.
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("Authorization")
		if token == "" {
			token = "Bearer " + r.URL.Query().Get("token")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte("Bearer "+s.Token)) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// handleEvents streams events until the client goes away.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := make(chan Event, 64)
	s.mu.Lock()
	s.clients[ch] = struct{}{}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if _, live := s.clients[ch]; live {
			delete(s.clients, ch)
			close(ch)
		}
		s.mu.Unlock()
	}()

	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case e, open := <-ch:
			if !open {
				return
			}
			data, err := json.Marshal(e)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// handlePrompt accepts a prompt to run.
func (s *Server) handlePrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Prompt string `json:"prompt"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if body.Prompt == "" {
		http.Error(w, "prompt is empty", http.StatusBadRequest)
		return
	}
	if s.onPrompt != nil {
		s.onPrompt(body.Prompt)
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
}

// URL is the address a client should use, token included.
func (s *Server) URL() string {
	return fmt.Sprintf("http://%s/events?token=%s", s.Addr, s.Token)
}
