package providerutil

import (
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

// The connection every provider call rides on, and the two ways a stream
// goes quiet without failing.
//
// http.DefaultClient waits forever. A provider that accepts the request and
// then says nothing, or a connection that died under a sleeping laptop or a
// network switch, left the turn spinning with nothing to retry: no error
// ever arrived. Loop's runtime fetch has header and body timeouts; these are
// pi's, and both end in an error RunTurn knows to resume from.

// responseHeaderTimeout bounds the wait for the response to start. Generous:
// some gateways send nothing until the first token, and a reasoning model
// over a long prompt can take minutes to produce one. Five minutes is
// undici's headersTimeout.
const responseHeaderTimeout = 5 * time.Minute

// HTTP/2 health checks. A connection that has received no frame for
// http2PingAfter is pinged, and one that does not answer within
// http2PingTimeout is closed — which fails the stream on it within half a
// minute of the network going, rather than never.
const (
	http2PingAfter   = 15 * time.Second
	http2PingTimeout = 15 * time.Second
)

// StreamIdleTimeout is how long a response body may go without a byte
// before it is abandoned: undici's bodyTimeout. A live provider is never
// silent this long — Anthropic pings, SSE gateways send keep-alive
// comments, and every token is bytes. A var so tests need not wait.
var StreamIdleTimeout = 5 * time.Minute

// ErrStreamStalled is a response body that went silent for StreamIdleTimeout.
var ErrStreamStalled = errors.New("the provider's stream went silent and was abandoned")

// DefaultHTTPClient is the client providers use when none is configured. It
// has no overall Timeout: that would cover reading the body too, and cut
// long generations off mid-stream.
var DefaultHTTPClient = &http.Client{Transport: newTransport()}

func newTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = responseHeaderTimeout
	t.HTTP2 = &http.HTTP2Config{SendPingTimeout: http2PingAfter, PingTimeout: http2PingTimeout}
	return t
}

// Send performs req and watches the response body for silence: a body that
// yields no byte for StreamIdleTimeout fails its next read with
// ErrStreamStalled. Closing the body stops the watch.
func Send(client *http.Client, req *http.Request) (*http.Response, error) {
	ctx, cancel := context.WithCancelCause(req.Context())
	resp, err := client.Do(req.WithContext(ctx))
	if err != nil {
		cancel(nil)
		return nil, err
	}
	resp.Body = watchBody(ctx, cancel, resp.Body)
	return resp, nil
}

// idleBody is a response body with a silence watchdog.
type idleBody struct {
	body   io.ReadCloser
	ctx    context.Context
	cancel context.CancelCauseFunc
	timer  *time.Timer
	once   sync.Once
}

func watchBody(ctx context.Context, cancel context.CancelCauseFunc, body io.ReadCloser) *idleBody {
	b := &idleBody{body: body, ctx: ctx, cancel: cancel}
	b.timer = time.AfterFunc(StreamIdleTimeout, func() { cancel(ErrStreamStalled) })
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.body.Read(p)
	if n > 0 {
		b.timer.Reset(StreamIdleTimeout)
	}
	// The read failed because the watchdog cancelled it: say so, rather
	// than as the bare cancellation a caller would take for the user's.
	if err != nil && errors.Is(context.Cause(b.ctx), ErrStreamStalled) {
		err = ErrStreamStalled
	}
	return n, err
}

func (b *idleBody) Close() error {
	err := b.body.Close()
	b.once.Do(func() {
		b.timer.Stop()
		b.cancel(nil)
	})
	return err
}
