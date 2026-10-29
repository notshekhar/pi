package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"
)

// httpTransport is streamable HTTP (@ai-sdk/mcp's HttpMCPTransport): one
// POST per message, answered with JSON or an SSE stream. In the legacy era
// the server also issues a session id, and may keep a GET stream open for
// what it sends unprompted.
type httpTransport struct {
	*remote

	mu      sync.Mutex
	h       transportHandlers
	ctx     context.Context // lives until close
	cancel  context.CancelFunc
	version string
	session string
	inbound *inboundStream
}

func newHTTPTransport(rawURL string, headers map[string]string, auth authorizer) (*httpTransport, error) {
	r, err := newRemote(rawURL, headers, auth)
	if err != nil {
		return nil, err
	}
	return &httpTransport{remote: r, version: LatestLegacyProtocolVersion}, nil
}

func (t *httpTransport) supportsDiscovery() bool { return true }

func (t *httpTransport) modern() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version == LatestProtocolVersion
}

func (t *httpTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
	// The modern era has no server-initiated stream.
	if v == LatestProtocolVersion {
		t.stopInbound()
	}
}

func (t *httpTransport) start(_ context.Context, h transportHandlers) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.ctx != nil {
		return errors.New("transport already started")
	}
	t.h = h
	t.ctx, t.cancel = context.WithCancel(context.Background())
	return nil
}

// newRequest builds a request with everything this era puts on it.
func (t *httpTransport) newRequest(ctx context.Context, method string, body []byte, withSession bool) (*http.Request, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.url.String(), rd)
	if err != nil {
		return nil, err
	}
	t.mu.Lock()
	version, session := t.version, t.session
	t.mu.Unlock()
	t.stamp(req, version)
	if withSession && session != "" && version != LatestProtocolVersion {
		req.Header.Set("Mcp-Session-Id", session)
	}
	return req, nil
}

func (t *httpTransport) send(ctx context.Context, m *message, extra map[string]string) error {
	t.mu.Lock()
	base := t.ctx
	t.mu.Unlock()
	if base == nil {
		return errNotConnected
	}
	data, err := encode(m)
	if err != nil {
		return err
	}
	// The request lives as long as both the caller and the transport.
	ctx, stop := mergeContexts(ctx, base)
	resp, err := t.do(ctx, func() (*http.Request, error) {
		req, err := t.newRequest(ctx, http.MethodPost, data, m.Method != "initialize")
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if t.modern() {
			setHeaders(req, extra)
			if m.isRequest() {
				setHeaders(req, standardRequestHeaders(m))
			}
		}
		return req, nil
	})
	if err != nil {
		stop()
		return err
	}
	t.rememberSession(resp)
	if t.answeredByStream(resp, m) {
		go t.readAnswerStream(ctx, stop, resp, m)
		return nil
	}
	defer stop()
	return t.handleResponse(resp, m)
}

func setHeaders(req *http.Request, h map[string]string) {
	for k, v := range h {
		req.Header.Set(k, v)
	}
}

// rememberSession keeps the legacy session id the server issued.
func (t *httpTransport) rememberSession(resp *http.Response) {
	if t.modern() {
		return
	}
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		t.mu.Lock()
		t.session = id
		t.mu.Unlock()
	}
}

func (t *httpTransport) answeredByStream(resp *http.Response, m *message) bool {
	return ok2xx(resp) && resp.StatusCode != http.StatusAccepted && m.isRequest() &&
		strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream")
}

// handleResponse handles every answer that is not an SSE stream.
func (t *httpTransport) handleResponse(resp *http.Response, m *message) error {
	switch {
	case resp.StatusCode == http.StatusAccepted:
		drain(resp)
		t.startInbound()
		return nil
	case !ok2xx(resp):
		return t.handleError(resp, m)
	case !m.isRequest():
		drain(resp)
		return nil
	case strings.Contains(resp.Header.Get("Content-Type"), "application/json"):
		return t.deliverJSON(resp)
	default:
		drain(resp)
		return fmt.Errorf("MCP HTTP Transport Error: Unexpected content type: %s", resp.Header.Get("Content-Type"))
	}
}

// handleError turns a non-2xx answer into the request's JSON-RPC error when
// the body carries one, and into an *HTTPError otherwise.
func (t *httpTransport) handleError(resp *http.Response, m *message) error {
	err := statusError(resp, "MCP HTTP Transport Error: POSTing to endpoint", t.errorHint(resp.StatusCode, m))
	if m.isRequest() {
		if msgs, perr := decodeMessages([]byte(err.Body)); perr == nil && len(msgs) == 1 && msgs[0].Error != nil {
			if len(msgs[0].ID) == 0 || string(msgs[0].ID) == "null" {
				msgs[0].ID = m.ID
			}
			t.h.onMessage(msgs[0])
			return nil
		}
	}
	return err
}

// errorHint explains a 404 in the legacy era: an expired session, or a
// server that speaks the other transport.
func (t *httpTransport) errorHint(status int, m *message) string {
	if status != http.StatusNotFound || t.modern() {
		return ""
	}
	t.mu.Lock()
	expired := t.session != "" && m.Method != "initialize"
	if expired {
		t.session = ""
	}
	t.mu.Unlock()
	if expired {
		return ". The MCP session expired"
	}
	return ". This server does not support HTTP transport. Try using `sse` transport instead"
}

func (t *httpTransport) deliverJSON(resp *http.Response) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	resp.Body.Close()
	if err != nil {
		return err
	}
	msgs, err := decodeMessages(body)
	if err != nil {
		return fmt.Errorf("MCP HTTP Transport Error: Failed to parse message: %w", err)
	}
	for _, msg := range msgs {
		t.h.onMessage(msg)
	}
	return nil
}

// readAnswerStream delivers everything on a request's SSE answer — the
// server's own notifications and requests, then the answer. A stream that
// ends without the answer says so, rather than leave the caller waiting out
// its timeout.
func (t *httpTransport) readAnswerStream(ctx context.Context, stop context.CancelFunc, resp *http.Response, m *message) {
	defer stop()
	defer resp.Body.Close()
	answered := false
	_ = readSSE(resp.Body, func(ev sseEvent) bool {
		for _, msg := range messagesIn(ev) {
			answered = answered || (msg.isResponse() && bytes.Equal(msg.ID, m.ID))
			t.h.onMessage(msg)
		}
		return !answered
	})
	if !answered && ctx.Err() == nil {
		t.h.onMessage(&message{ID: m.ID, Error: &RPCError{Code: -32000,
			Message: "the server closed the response stream before answering"}})
	}
}

// messagesIn is the JSON-RPC messages a message event carries.
func messagesIn(ev sseEvent) []*message {
	if !isMessageEvent(ev.Event) {
		return nil
	}
	msgs, _ := decodeMessages([]byte(ev.Data))
	return msgs
}

func (t *httpTransport) close() error {
	t.mu.Lock()
	cancel, session, legacy := t.cancel, t.session, t.version != LatestProtocolVersion
	t.ctx = nil
	t.mu.Unlock()
	t.stopInbound()
	if cancel != nil {
		cancel()
	}
	if legacy && session != "" {
		t.endSession()
	}
	return nil
}

// endSession tells the server the legacy session is over, so a server that
// counts sessions is not left holding ours.
func (t *httpTransport) endSession() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := t.newRequest(ctx, http.MethodDelete, nil, true)
	if err != nil {
		return
	}
	if resp, err := httpClient.Do(req); err == nil {
		drain(resp)
	}
}

// standardRequestHeaders are the modern era's routing headers: the method,
// and the name a tools/call, prompts/get or resources/read is about.
func standardRequestHeaders(m *message) map[string]string {
	h := map[string]string{"Mcp-Method": m.Method}
	var p struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
	}
	_ = json.Unmarshal(m.Params, &p)
	name := p.Name
	switch m.Method {
	case "resources/read":
		name = p.URI
	case "tools/call", "prompts/get":
	default:
		name = ""
	}
	if name != "" {
		h["Mcp-Name"] = encodeHeaderValue(name)
	}
	return h
}

// --- the legacy server-initiated stream --------------------------------------

// inboundStream is the legacy era's optional GET stream, over which a server
// sends what nobody asked for: list_changed, progress, requests of its own.
// Best-effort and resumable.
type inboundStream struct {
	cancel      context.CancelFunc
	lastEventID string
}

const inboundMaxRetries = 2

func (t *httpTransport) startInbound() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inbound != nil || t.ctx == nil || t.version == LatestProtocolVersion {
		return
	}
	ctx, cancel := context.WithCancel(t.ctx)
	t.inbound = &inboundStream{cancel: cancel}
	go t.runInbound(ctx, t.inbound)
}

func (t *httpTransport) stopInbound() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.inbound != nil {
		t.inbound.cancel()
		t.inbound = nil
	}
}

func (t *httpTransport) runInbound(ctx context.Context, s *inboundStream) {
	for attempt := 0; ctx.Err() == nil && attempt <= inboundMaxRetries; attempt++ {
		if t.readInbound(ctx, s) {
			attempt = 0 // it connected: retries count from here
		} else if attempt == 0 {
			return // refused outright (405 is "no such stream", which is allowed)
		}
		delay := time.Duration(math.Min(1000*math.Pow(1.5, float64(attempt)), 30_000)) * time.Millisecond
		select {
		case <-ctx.Done():
		case <-time.After(delay):
		}
	}
}

// readInbound opens the stream once and reads it until it ends. It reports
// whether it connected at all.
func (t *httpTransport) readInbound(ctx context.Context, s *inboundStream) bool {
	resp, err := t.do(ctx, func() (*http.Request, error) {
		req, err := t.newRequest(ctx, http.MethodGet, nil, true)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		if s.lastEventID != "" {
			req.Header.Set("Last-Event-ID", s.lastEventID)
		}
		return req, nil
	})
	if err != nil {
		return false
	}
	if !ok2xx(resp) {
		drain(resp)
		return false
	}
	defer resp.Body.Close()
	_ = readSSE(resp.Body, func(ev sseEvent) bool {
		if ev.ID != "" {
			s.lastEventID = ev.ID
		}
		for _, m := range messagesIn(ev) {
			t.h.onMessage(m)
		}
		return ctx.Err() == nil
	})
	return true
}
