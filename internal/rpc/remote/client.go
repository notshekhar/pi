package remote

import (
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/rpc"
)

// Status is where a client's connection stands.
type Status string

const (
	StatusConnecting   Status = "connecting"
	StatusOpen         Status = "open"
	StatusReconnecting Status = "reconnecting"
	StatusClosed       Status = "closed"
)

// SessionEvent is one event of a host's session, as the host stamps it.
type SessionEvent struct {
	Seq  int64 `json:"seq"`
	Part struct {
		Type string         `json:"type"`
		Data jsontext.Value `json:"data"`
	} `json:"part"`
}

// Socket is the seam a connection runs over; tests swap the dialer.
type Socket interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
	Close() error
}

// Dialer opens a socket to a ws:// or wss:// URL.
type Dialer func(ctx context.Context, url string) (Socket, error)

// Options tune a Client; the zero value is loop's behaviour.
type Options struct {
	HTTP HTTPClient
	Dial Dialer
	// CallTimeout bounds one request (default 30s).
	CallTimeout time.Duration
	// ReconnectBase is the first reconnect delay; it doubles to a 30s ceiling.
	ReconnectBase time.Duration
}

const (
	defaultCallTimeout   = 30 * time.Second
	defaultReconnectBase = time.Second
	maxReconnectDelay    = 30 * time.Second
	dialTimeout          = 10 * time.Second
	// maxFrameBytes lifts the socket library's 32 KiB read cap: one
	// session.history reply carries a whole transcript.
	maxFrameBytes = 64 << 20
)

type reply struct {
	result jsontext.Value
	err    error
}

// attempt is a connect in progress, which later callers wait on.
type attempt struct {
	done chan struct{}
	err  error
}

// Client is one live connection to a paired host — loop's RemoteHostClient.
// Requests are JSON-RPC over the socket; session.event notifications fan out
// to whoever subscribed to that session. A dropped socket reconnects with
// backoff until Close — on each reconnect the status goes "open" again, and
// subscribers re-attach with the last seq they saw (the host replays the gap
// or says resync).
type Client struct {
	Host HostRecord
	opts Options

	mu               sync.Mutex
	socket           Socket
	nextID           int64
	pending          map[int64]chan reply
	sessionListeners map[string]map[int]func(SessionEvent)
	statusListeners  map[int]func(Status)
	nextListener     int
	opening          *attempt
	closed           bool
	retry            time.Duration
	retryTimer       *time.Timer
	status           Status
}

// NewClient is a client for host; nothing connects until the first call.
func NewClient(host HostRecord, opts Options) *Client {
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = defaultCallTimeout
	}
	if opts.ReconnectBase <= 0 {
		opts.ReconnectBase = defaultReconnectBase
	}
	if opts.Dial == nil {
		opts.Dial = dialWebSocket
	}
	return &Client{Host: host, opts: opts, pending: map[int64]chan reply{},
		sessionListeners: map[string]map[int]func(SessionEvent){}, statusListeners: map[int]func(Status){},
		retry: opts.ReconnectBase, status: StatusConnecting}
}

// Status is the connection's state right now.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Connect returns once the socket is open, or with why this attempt failed.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	// The socket is set before hello is answered; only "open" is ready.
	if c.socket != nil && c.status == StatusOpen {
		c.mu.Unlock()
		return nil
	}
	a := c.opening
	if a == nil {
		a = &attempt{done: make(chan struct{})}
		c.opening, c.closed = a, false
		c.mu.Unlock()
		a.err = c.open()
		c.mu.Lock()
		c.opening = nil
		c.mu.Unlock()
		close(a.done)
		return a.err
	}
	c.mu.Unlock()
	select {
	case <-a.done:
		return a.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) open() error {
	ticket, err := c.wsTicket()
	if err != nil {
		return err
	}
	wsURL := "ws" + strings.TrimPrefix(c.Host.URL, "http") + "/ws?wsTicket=" + url.QueryEscape(ticket)
	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	sock, err := c.opts.Dial(ctx, wsURL)
	if err != nil {
		return fmt.Errorf("could not open a socket to %s", c.Host.Label)
	}
	c.mu.Lock()
	c.socket, c.retry = sock, c.opts.ReconnectBase
	c.mu.Unlock()
	go c.readLoop(sock)
	if err := c.hello(sock); err != nil {
		return err
	}
	c.setStatus(StatusOpen)
	return nil
}

// wsTicket is a one-use ticket, so the socket URL (which proxies log) never
// carries the token itself.
func (c *Client) wsTicket() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), ticketTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Host.URL+"/api/auth/websocket-ticket", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("authorization", "Bearer "+c.Host.Token)
	res, err := httpClient(c.opts.HTTP).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("%s no longer accepts this token — pair it again", c.Host.Label)
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return "", fmt.Errorf("%s answered %d", c.Host.Label, res.StatusCode)
	}
	var body struct {
		Ticket string `json:"ticket"`
	}
	if err := json.UnmarshalRead(res.Body, &body, jsonopt.Read); err != nil {
		return "", err
	}
	return body.Ticket, nil
}

var (
	reProtocol       = regexp.MustCompile(`(?i)\bprotocol\b`)
	reUpdate         = regexp.MustCompile(`(?i)update`)
	reMethodNotFound = regexp.MustCompile(`(?i)method not found`)
)

// hello is the handshake, before anything else is asked of the host. A
// host that does not know hello predates it — protocol 1.0, served as is.
// Only the host refusing this protocol is final; a hello that timed out or
// lost its socket is an ordinary dropped connection, and closing for good
// there left a remote session frozen until a restart.
func (c *Client) hello(sock Socket) error {
	_, err := c.request("hello", map[string]any{"protocol": rpc.ProtocolVersion, "client": "pi-tui"})
	if err == nil || reMethodNotFound.MatchString(err.Error()) {
		return nil
	}
	message := err.Error()
	c.mu.Lock()
	incompatible := reProtocol.MatchString(message) && reUpdate.MatchString(message)
	if incompatible {
		c.closed = true
	}
	if c.socket == sock {
		c.socket = nil
	}
	c.mu.Unlock()
	_ = sock.Close()
	if incompatible {
		c.setStatus(StatusClosed)
		return fmt.Errorf("%s: %s", c.Host.Label, forThisPi(message))
	}
	return fmt.Errorf("%s: %s", c.Host.Label, message)
}

func (c *Client) readLoop(sock Socket) {
	for {
		data, err := sock.Read(context.Background())
		if err != nil {
			c.dropped(sock)
			return
		}
		c.handle(data)
	}
}

// dropped fails what was waiting on a socket that died, and reconnects
// unless the client was closed.
func (c *Client) dropped(sock Socket) {
	c.mu.Lock()
	if c.socket != sock {
		c.mu.Unlock()
		return
	}
	c.socket = nil
	waiting := c.pending
	c.pending = map[int64]chan reply{}
	closed := c.closed
	c.mu.Unlock()
	lost := fmt.Errorf("lost the connection to %s", c.Host.Label)
	for _, ch := range waiting {
		ch <- reply{err: lost}
	}
	if closed {
		return
	}
	c.setStatus(StatusReconnecting)
	c.scheduleReconnect()
}

func (c *Client) scheduleReconnect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.retryTimer != nil {
		return
	}
	delay := c.retry
	c.retry = min(c.retry*2, maxReconnectDelay)
	c.retryTimer = time.AfterFunc(delay, func() {
		c.mu.Lock()
		c.retryTimer = nil
		c.mu.Unlock()
		if c.Connect(context.Background()) != nil {
			c.scheduleReconnect()
		}
	})
}

type frame struct {
	ID     *int64         `json:"id"`
	Result jsontext.Value `json:"result"`
	Error  *struct {
		Message string `json:"message"`
	} `json:"error"`
	Method string         `json:"method"`
	Params jsontext.Value `json:"params"`
}

func (c *Client) handle(data []byte) {
	var msg frame
	if json.Unmarshal(data, &msg, jsonopt.Read) != nil {
		return
	}
	if msg.ID != nil && c.answer(*msg.ID, msg) {
		return
	}
	if msg.Method == "session.event" {
		c.fanOut(msg.Params)
	}
}

// answer settles a pending request; false when id is not one of ours.
func (c *Client) answer(id int64, msg frame) bool {
	c.mu.Lock()
	ch, ok := c.pending[id]
	delete(c.pending, id)
	c.mu.Unlock()
	if !ok {
		return false
	}
	if msg.Error != nil {
		message := msg.Error.Message
		if message == "" {
			message = "remote error"
		}
		ch <- reply{err: errors.New(message)}
	} else {
		ch <- reply{result: msg.Result}
	}
	return true
}

func (c *Client) fanOut(params jsontext.Value) {
	var ev struct {
		SessionID string `json:"sessionId"`
		SessionEvent
	}
	if json.Unmarshal(params, &ev, jsonopt.Read) != nil {
		return
	}
	c.mu.Lock()
	listeners := make([]func(SessionEvent), 0, len(c.sessionListeners[ev.SessionID]))
	for _, l := range c.sessionListeners[ev.SessionID] {
		listeners = append(listeners, l)
	}
	c.mu.Unlock()
	for _, l := range listeners {
		l(ev.SessionEvent)
	}
}

// Call connects if it must, then makes one request; out, when non-nil,
// receives the result.
func (c *Client) Call(ctx context.Context, method string, params map[string]any, out any) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}
	result, err := c.request(method, params)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(result, out, jsonopt.Read)
}

// request is one request on the open socket — no connect, since open uses
// it mid-handshake.
func (c *Client) request(method string, params map[string]any) (jsontext.Value, error) {
	if params == nil {
		params = map[string]any{}
	}
	c.mu.Lock()
	sock := c.socket
	if sock == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("lost the connection to %s", c.Host.Label)
	}
	c.nextID++
	id := c.nextID
	ch := make(chan reply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	raw, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}, jsonopt.Write)
	if err == nil {
		err = sock.Write(context.Background(), raw)
	}
	if err != nil {
		c.forget(id)
		return nil, err
	}
	timer := time.NewTimer(c.opts.CallTimeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.result, r.err
	case <-timer.C:
		c.forget(id)
		return nil, fmt.Errorf("%s did not answer %s", c.Host.Label, method)
	}
}

func (c *Client) forget(id int64) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// OnSessionEvent hears one session's events. Subscribing does not attach —
// call session.attach. The returned func unsubscribes.
func (c *Client) OnSessionEvent(sessionID string, f func(SessionEvent)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	set := c.sessionListeners[sessionID]
	if set == nil {
		set = map[int]func(SessionEvent){}
		c.sessionListeners[sessionID] = set
	}
	id := c.nextListener
	c.nextListener++
	set[id] = f
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(set, id)
		if len(set) == 0 {
			delete(c.sessionListeners, sessionID)
		}
	}
}

// OnStatus hears every status change. The returned func unsubscribes.
func (c *Client) OnStatus(f func(Status)) func() {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.nextListener
	c.nextListener++
	c.statusListeners[id] = f
	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.statusListeners, id)
	}
}

func (c *Client) setStatus(s Status) {
	c.mu.Lock()
	c.status = s
	listeners := make([]func(Status), 0, len(c.statusListeners))
	for _, l := range c.statusListeners {
		listeners = append(listeners, l)
	}
	c.mu.Unlock()
	for _, l := range listeners {
		l(s)
	}
}

// Close ends the connection for good: no reconnect, and every pending
// request fails.
func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	if c.retryTimer != nil {
		c.retryTimer.Stop()
		c.retryTimer = nil
	}
	sock := c.socket
	c.socket = nil
	waiting := c.pending
	c.pending = map[int64]chan reply{}
	c.mu.Unlock()
	for _, ch := range waiting {
		ch <- reply{err: errors.New("closed")}
	}
	if sock != nil {
		_ = sock.Close()
	}
	c.setStatus(StatusClosed)
}

// wsSocket is a coder/websocket connection as a Socket.
type wsSocket struct{ conn *websocket.Conn }

func dialWebSocket(ctx context.Context, url string) (Socket, error) {
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(maxFrameBytes)
	return wsSocket{conn}, nil
}

func (s wsSocket) Read(ctx context.Context) ([]byte, error) {
	_, data, err := s.conn.Read(ctx)
	return data, err
}

func (s wsSocket) Write(ctx context.Context, data []byte) error {
	return s.conn.Write(ctx, websocket.MessageText, data)
}

func (s wsSocket) Close() error { return s.conn.Close(websocket.StatusNormalClosure, "") }
