package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The interactive OAuth login for one server (loop's mcp/authorize.ts):
// discovery → registration → browser consent → token exchange, against a
// one-shot callback server on loopback.

const loginTimeout = 180 * time.Second

// Authorize signs in to a remote server, opening the browser through open.
// It returns once tokens are saved. A login that does not finish puts back
// whatever session was there before it started: "re-authorize" is offered on
// servers that are working, and a cancelled attempt must not sign them out.
func Authorize(ctx context.Context, name string, cfg ServerConfig, open func(url string)) (err error) {
	if !cfg.IsHTTP() {
		return fmt.Errorf("server %q is not an HTTP server — OAuth only applies to http/sse servers", name)
	}
	serverURL, err := url.Parse(ResolveSecrets(cfg.URL))
	if err != nil {
		return err
	}
	if reason := UnsupportedRemoteServer(cfg.URL); reason != "" {
		return errors.New(reason)
	}
	hints := discoverResourceHints(ctx, serverURL, resolveSecretMap(cfg.Headers))
	if len(cfg.Scopes) == 0 && len(hints.scopes) > 0 {
		cfg.Scopes = hints.scopes
	}
	callback, err := startCallbackServer()
	if err != nil {
		return err
	}
	defer callback.close()

	previous := readAuth(name)
	authorized := false
	defer func() {
		if !authorized && !previous.empty() {
			_ = restoreAuth(name, previous)
		}
	}()
	// Start clean: a dynamically registered client is bound to the redirect
	// URI it registered with, and this run's callback port may differ.
	if err := ClearAuth(name); err != nil {
		return err
	}
	provider := newOAuthProvider(name, cfg, callback.redirectURI, open)
	dynamic := cfg.ClientID == ""

	first, err := runAuth(ctx, provider, authOptions{serverURL: serverURL, resourceMetadataURL: hints.resourceMetadataURL})
	if err != nil {
		return registrationError(err, dynamic, cfg.URL)
	}
	if first == authAuthorized {
		authorized = true
		return nil
	}
	code, state, err := callback.wait(ctx, loginTimeout)
	if err != nil {
		return err
	}
	res, err := runAuth(ctx, provider, authOptions{
		serverURL: serverURL, resourceMetadataURL: hints.resourceMetadataURL,
		authorizationCode: code, callbackState: state,
	})
	if err != nil {
		return registrationError(err, dynamic, cfg.URL)
	}
	if res != authAuthorized {
		return errors.New("authorization did not complete")
	}
	authorized = true
	return nil
}

var registrationRefusal = regexp.MustCompile(`(?i)\b(403|forbidden|registration|invalid oauth error response)\b`)

// registrationError turns a server refusing anonymous client registration
// into something the user can act on.
func registrationError(err error, dynamic bool, serverURL string) error {
	if !dynamic || !registrationRefusal.MatchString(err.Error()) {
		return err
	}
	if reason := UnsupportedRemoteServer(serverURL); reason != "" {
		return errors.New(reason)
	}
	return fmt.Errorf("%w\n\nThis server blocks automatic OAuth client registration. Register an OAuth app with the provider, "+
		`then add "clientId" (and "clientSecret" for confidential clients) to the server entry in ~/.loop/settings.json.`, err)
}

type resourceHints struct {
	resourceMetadataURL *url.URL
	scopes              []string
}

// discoverResourceHints asks the server, unauthenticated, what it wants:
// the 401's resource_metadata URL and scope, or the metadata document's
// scopes_supported. Best-effort throughout.
func discoverResourceHints(ctx context.Context, server *url.URL, headers map[string]string) resourceHints {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.String(),
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	if err != nil {
		return resourceHints{}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := httpClient.Do(req)
	if err != nil {
		return resourceHints{}
	}
	drain(resp)
	meta, scope := wwwAuthenticateParams(resp.Header.Get("WWW-Authenticate"))
	hints := resourceHints{resourceMetadataURL: meta, scopes: strings.Fields(scope)}
	if len(hints.scopes) == 0 && meta != nil {
		if r, err := get(ctx, meta); err == nil {
			var body struct {
				Scopes []string `json:"scopes_supported"`
			}
			if r.StatusCode == http.StatusOK {
				_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
			}
			drain(r)
			for _, s := range body.Scopes {
				if s != "" {
					hints.scopes = append(hints.scopes, s)
				}
			}
		}
	}
	return hints
}

// --- callback server ------------------------------------------------------------

type callbackServer struct {
	redirectURI string
	srv         *http.Server
	result      chan callbackResult
}

type callbackResult struct {
	code, state string
	err         error
}

// callbackHost and callbackPort are where the login callback listens:
// loopback, on a stable port (nicer for servers that allow-list the redirect
// URI) unless overridden.
func callbackHost() string {
	if h := os.Getenv("LOOP_MCP_OAUTH_CALLBACK_HOST"); h != "" {
		return h
	}
	return "127.0.0.1"
}

func callbackPort() int {
	if p, err := strconv.Atoi(os.Getenv("LOOP_MCP_OAUTH_CALLBACK_PORT")); err == nil && p > 0 {
		return p
	}
	return 8976
}

func callbackURI(port int) string {
	return fmt.Sprintf("http://%s/callback", net.JoinHostPort(callbackHost(), strconv.Itoa(port)))
}

// startCallbackServer listens on the callback port, or any free one when it
// is taken.
func startCallbackServer() (*callbackServer, error) {
	ln, err := net.Listen("tcp", net.JoinHostPort(callbackHost(), strconv.Itoa(callbackPort())))
	if err != nil {
		if ln, err = net.Listen("tcp", net.JoinHostPort(callbackHost(), "0")); err != nil {
			return nil, err
		}
	}
	cb := &callbackServer{result: make(chan callbackResult, 1)}
	cb.redirectURI = callbackURI(ln.Addr().(*net.TCPAddr).Port)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "text/html")
		var res callbackResult
		switch {
		case q.Get("error") != "":
			res.err = fmt.Errorf("OAuth error: %s", q.Get("error"))
			fmt.Fprint(w, callbackPage("Authorization failed: "+q.Get("error")))
		case q.Get("code") == "":
			res.err = errors.New("OAuth callback missing authorization code")
			fmt.Fprint(w, callbackPage("Authorization failed: no code returned"))
		default:
			res.code, res.state = q.Get("code"), q.Get("state")
			fmt.Fprint(w, callbackPage("Authorized. You can close this tab and return to pi."))
		}
		select {
		case cb.result <- res:
		default:
		}
	})
	cb.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = cb.srv.Serve(ln) }()
	return cb, nil
}

func (cb *callbackServer) wait(ctx context.Context, timeout time.Duration) (code, state string, err error) {
	select {
	case r := <-cb.result:
		return r.code, r.state, r.err
	case <-time.After(timeout):
		return "", "", errors.New("timed out waiting for OAuth callback")
	case <-ctx.Done():
		return "", "", ctx.Err()
	}
}

func (cb *callbackServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = cb.srv.Shutdown(ctx)
}

func callbackPage(msg string) string {
	return `<!doctype html><meta charset="utf-8"><body style="font-family:system-ui;padding:3rem;text-align:center"><p>` +
		html.EscapeString(msg) + `</p></body>`
}

// --- providers that cannot work -------------------------------------------------

// unsupportedProviders are remote servers that allow-list which products
// may connect, so no configuration reaches them (loop's mcp/providers.ts).
var unsupportedProviders = []struct {
	host   *regexp.Regexp
	reason string
}{{
	host: regexp.MustCompile(`(?i)(^|\.)figma\.com$`),
	reason: "Figma's remote MCP server only accepts clients in the Figma MCP Catalog. Its registration endpoint " +
		"matches on client name and refuses every other, so pi cannot obtain credentials for it.\n" +
		"  What does work is Figma's LOCAL server, which needs no auth at all. In the Figma desktop app open " +
		"a design file, press Shift+D for Dev Mode, enable the desktop MCP server in the inspect panel, then add " +
		"an http server at http://127.0.0.1:3845/mcp.\n" +
		"  It needs a Dev or Full seat — a View or Collab seat is capped at 6 tool calls a month.",
}}

// UnsupportedRemoteServer is why this URL cannot be used as a remote MCP
// server, or "" when it can.
func UnsupportedRemoteServer(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	for _, p := range unsupportedProviders {
		if p.host.MatchString(u.Hostname()) {
			return p.reason
		}
	}
	return ""
}
