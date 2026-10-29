package mcp

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/build"
)

// What both HTTP transports share: where the server is, what every request
// carries, and recovery from a 401.

// HTTPError is a remote server's non-2xx answer.
type HTTPError struct {
	StatusCode int
	Body       string
	URL        string
	msg        string
}

func (e *HTTPError) Error() string { return e.msg }

// ErrUnauthorized is a server refusing us for lack of credentials.
var ErrUnauthorized = errors.New("unauthorized")

var errNotConnected = errors.New("not connected")

// authorizer is the OAuth half of a remote transport.
type authorizer interface {
	accessToken() string
	// authorize recovers from a 401: refresh, or (with an opener) a browser
	// login. It reports whether tokens are now in hand.
	authorize(ctx context.Context, serverURL, resourceMetadataURL *url.URL, scope string) (bool, error)
}

// httpClient carries no overall timeout: an SSE response can legitimately
// stay open for a long tool call, and every request is bounded by its
// context instead.
var httpClient = &http.Client{}

type remote struct {
	url     *url.URL
	headers map[string]string
	auth    authorizer
	// authMu makes concurrent 401s share one recovery instead of each
	// opening its own login.
	authMu sync.Mutex
}

func newRemote(rawURL string, headers map[string]string, auth authorizer) (*remote, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("invalid server url %q", rawURL)
	}
	return &remote{url: u, headers: headers, auth: auth}, nil
}

// stamp sets the configured headers, the protocol version, the bearer token
// and the user agent.
func (r *remote) stamp(req *http.Request, protocolVersion string) {
	for k, v := range r.headers {
		req.Header.Set(k, v)
	}
	if protocolVersion != "" {
		req.Header.Set("Mcp-Protocol-Version", protocolVersion)
	}
	if r.auth != nil {
		if tok := r.auth.accessToken(); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
		}
	}
	req.Header.Set("User-Agent", "pi-agent/"+build.Version)
}

// do sends the request build makes. A 401 is recovered from once — by a
// refresh or a login — and the request rebuilt (so it carries the new
// token) and sent again.
func (r *remote) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	for retried := false; ; retried = true {
		req, err := build()
		if err != nil {
			return nil, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusUnauthorized || r.auth == nil || retried {
			return resp, nil
		}
		meta, scope := wwwAuthenticateParams(resp.Header.Get("WWW-Authenticate"))
		drain(resp)
		if err := r.recover(ctx, meta, scope); err != nil {
			return nil, err
		}
	}
}

func (r *remote) recover(ctx context.Context, meta *url.URL, scope string) error {
	r.authMu.Lock()
	defer r.authMu.Unlock()
	ok, err := r.auth.authorize(ctx, r.url, meta, scope)
	switch {
	case err != nil:
		return err
	case !ok:
		return ErrUnauthorized
	}
	return nil
}

// statusError reads a non-2xx response into an *HTTPError; hint is appended
// to the message.
func statusError(resp *http.Response, prefix, hint string) *HTTPError {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	resp.Body.Close()
	text := strings.TrimSpace(string(body))
	return &HTTPError{StatusCode: resp.StatusCode, Body: text, URL: resp.Request.URL.String(),
		msg: fmt.Sprintf("%s (HTTP %d): %s%s", prefix, resp.StatusCode, text, hint)}
}

func ok2xx(resp *http.Response) bool { return resp.StatusCode >= 200 && resp.StatusCode < 300 }

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
}

var (
	wwwResourceMetadata = regexp.MustCompile(`(?i)(?:^|[,\s])resource_metadata="([^"]*)"`)
	wwwScope            = regexp.MustCompile(`(?i)(?:^|[,\s])scope="([^"]*)"`)
)

// wwwAuthenticateParams reads a Bearer challenge's resource_metadata URL
// (RFC 9728) and scope.
func wwwAuthenticateParams(header string) (*url.URL, string) {
	scheme, rest, _ := strings.Cut(header, " ")
	if !strings.EqualFold(scheme, "bearer") || rest == "" {
		return nil, ""
	}
	var meta *url.URL
	if m := wwwResourceMetadata.FindStringSubmatch(header); m != nil {
		if u, err := url.Parse(m[1]); err == nil && u.IsAbs() {
			meta = u
		}
	}
	scope := ""
	if m := wwwScope.FindStringSubmatch(header); m != nil {
		scope = m[1]
	}
	return meta, scope
}

var base64Sentinel = regexp.MustCompile(`^=\?base64\?.*\?=$`)

// encodeHeaderValue sends a value as is when it is plain printable ASCII,
// and as =?base64?…?= otherwise.
func encodeHeaderValue(v string) string {
	plain := true
	for _, r := range v {
		if r != '\t' && (r < 32 || r > 126) {
			plain = false
			break
		}
	}
	if plain && strings.TrimSpace(v) == v && !base64Sentinel.MatchString(v) {
		return v
	}
	return "=?base64?" + base64.StdEncoding.EncodeToString([]byte(v)) + "?="
}

// mergeContexts is a context done when either parent is.
func mergeContexts(a, b context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(a)
	stop := context.AfterFunc(b, cancel)
	return ctx, func() { stop(); cancel() }
}
