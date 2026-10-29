package mcp

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
)

// sseTransport is the pre-2025 HTTP+SSE transport: a GET stream that names
// an endpoint and carries every server message, and POSTs to that endpoint.
type sseTransport struct {
	*remote

	mu       sync.Mutex
	version  string
	endpoint *url.URL
	cancel   context.CancelFunc
	closed   bool
}

func newSSETransport(rawURL string, headers map[string]string, auth authorizer) (*sseTransport, error) {
	r, err := newRemote(rawURL, headers, auth)
	if err != nil {
		return nil, err
	}
	return &sseTransport{remote: r, version: LatestLegacyProtocolVersion}, nil
}

func (t *sseTransport) supportsDiscovery() bool { return false }

func (t *sseTransport) setProtocolVersion(v string) {
	t.mu.Lock()
	t.version = v
	t.mu.Unlock()
}

func (t *sseTransport) protocolVersion() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version
}

// start opens the stream and returns once the server has named its
// endpoint.
func (t *sseTransport) start(ctx context.Context, h transportHandlers) error {
	streamCtx, cancel := context.WithCancel(context.Background())
	t.mu.Lock()
	t.cancel = cancel
	t.mu.Unlock()
	resp, err := t.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(streamCtx, http.MethodGet, t.url.String(), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "text/event-stream")
		t.stamp(req, t.protocolVersion())
		return req, nil
	})
	if err == nil && !ok2xx(resp) {
		hint := ""
		if resp.StatusCode == http.StatusMethodNotAllowed {
			hint = ". This server does not support SSE transport. Try using `http` transport instead"
		}
		err = statusError(resp, "MCP SSE Transport Error", hint)
	}
	if err != nil {
		cancel()
		return err
	}
	ready := make(chan error, 1)
	go t.read(resp, h, ready)
	select {
	case err := <-ready:
		if err != nil {
			cancel()
		}
		return err
	case <-ctx.Done():
		cancel()
		return ctx.Err()
	}
}

// read runs the stream: the first endpoint event signals ready, message
// events are delivered, and an end that was not our close is reported.
func (t *sseTransport) read(resp *http.Response, h transportHandlers, ready chan<- error) {
	defer resp.Body.Close()
	connected := false
	err := readSSE(resp.Body, func(ev sseEvent) bool {
		if ev.Event != "endpoint" {
			for _, m := range messagesIn(ev) {
				h.onMessage(m)
			}
			return true
		}
		if connected {
			return true
		}
		if err := t.setEndpoint(ev.Data); err != nil {
			ready <- err
			return false
		}
		connected = true
		ready <- nil
		return true
	})
	if !connected {
		if err == nil {
			err = errors.New("MCP SSE Transport Error: stream ended before naming an endpoint")
		}
		ready <- err
		return
	}
	t.mu.Lock()
	closed := t.closed
	t.endpoint = nil
	t.mu.Unlock()
	if !closed {
		if err == nil {
			err = errors.New("MCP SSE Transport Error: Connection closed unexpectedly")
		}
		h.onClose(err)
	}
}

// setEndpoint accepts the endpoint only on the stream's own origin.
func (t *sseTransport) setEndpoint(raw string) error {
	endpoint, err := t.url.Parse(strings.TrimSpace(raw))
	if err != nil || origin(endpoint) != origin(t.url) {
		return errors.New("MCP SSE Transport Error: Endpoint origin does not match connection origin")
	}
	t.mu.Lock()
	t.endpoint = endpoint
	t.mu.Unlock()
	return nil
}

func (t *sseTransport) send(ctx context.Context, m *message, _ map[string]string) error {
	t.mu.Lock()
	endpoint := t.endpoint
	t.mu.Unlock()
	if endpoint == nil {
		return errors.New("MCP SSE Transport Error: Not connected")
	}
	data, err := encode(m)
	if err != nil {
		return err
	}
	resp, err := t.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		t.stamp(req, t.protocolVersion())
		return req, nil
	})
	if err != nil {
		return err
	}
	if !ok2xx(resp) {
		return statusError(resp, "MCP SSE Transport Error: POSTing to endpoint", "")
	}
	drain(resp)
	return nil
}

func (t *sseTransport) close() error {
	t.mu.Lock()
	t.closed = true
	cancel := t.cancel
	t.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}
