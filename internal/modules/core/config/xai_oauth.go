package config

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Signing in to xAI with a SuperGrok subscription, rather than with an API
// key.
//
// The distinction matters commercially: an API key is pay-as-you-go, while
// the OAuth path bills against a subscription the user already has. Offering
// only the key means a SuperGrok subscriber pays twice, which is why loop
// offers both and why this exists.
//
// It is the standard authorization-code flow with PKCE, and the parts that
// are not standard are the parts that keep it safe on a developer's machine:
//
//   - The redirect is a loopback listener on 127.0.0.1, so the code never
//     leaves the machine. There is no client secret — a public client cannot
//     keep one — and PKCE is what stands in for it.
//   - Every endpoint comes from OIDC DISCOVERY and is then checked to be
//     HTTPS and to belong to x.ai. Discovery means a URL the user's DNS
//     resolves, and following one of those to a token endpoint without
//     checking it is how an access token ends up somewhere else.
//   - `state` is compared on the way back. Without it the callback accepts a
//     code from anyone who can reach the listener.

const (
	xaiIssuer    = "https://auth.x.ai"
	xaiDiscovery = xaiIssuer + "/.well-known/openid-configuration"
	// The public client id loop registered. A public client's id is not a
	// secret — PKCE is what makes the flow safe without one.
	xaiClientID     = "b1a00492-073a-47ea-816f-4c329264a828"
	xaiScope        = "openid profile email offline_access grok-cli:access api:access"
	xaiCallbackHost = "127.0.0.1"
	// The port xAI has registered for the loopback redirect. A different one
	// is only used if this is taken, and then the redirect must still be
	// accepted by the authorization server.
	xaiCallbackPort = 56121
	xaiCallbackPath = "/callback"
	// refreshSkew renews a token before it actually expires, so a request
	// cannot lose a race with the clock.
	refreshSkew = 2 * time.Minute
)

// XaiCreds is what the flow yields and what is stored.
type XaiCreds struct {
	Access        string `json:"access"`
	Refresh       string `json:"refresh"`
	Expires       int64  `json:"expires"`
	TokenEndpoint string `json:"tokenEndpoint"`
	TokenType     string `json:"tokenType,omitempty"`
}

type xaiEndpoints struct {
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
}

// XaiLogin runs the browser flow and stores the credentials.
//
// present is called with the URL to open and a line explaining what is about
// to happen — the caller owns the terminal, and this package must not print.
func XaiLogin(ctx context.Context, present func(url, instructions string)) error {
	endpoints, err := xaiDiscover(ctx)
	if err != nil {
		return err
	}
	verifier, challenge, err := pkce()
	if err != nil {
		return err
	}
	state, err := randomToken(16)
	if err != nil {
		return err
	}
	nonce, err := randomToken(16)
	if err != nil {
		return err
	}

	cb, err := startCallbackServer()
	if err != nil {
		return err
	}
	defer cb.close()

	auth, err := url.Parse(endpoints.Authorization)
	if err != nil {
		return fmt.Errorf("xai: bad authorization endpoint: %w", err)
	}
	q := auth.Query()
	q.Set("response_type", "code")
	q.Set("client_id", xaiClientID)
	q.Set("redirect_uri", cb.redirectURI)
	q.Set("scope", xaiScope)
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("nonce", nonce)
	q.Set("plan", "generic")
	q.Set("referrer", "pi-agent")
	auth.RawQuery = q.Encode()

	present(auth.String(), "Authorize xAI in the browser, then come back here. Listening on "+cb.redirectURI)

	result, err := cb.wait(ctx, 3*time.Minute)
	if err != nil {
		return err
	}
	if result.state != state {
		return fmt.Errorf("xai: state mismatch — the callback did not come from the sign-in that was started")
	}
	creds, err := xaiExchange(ctx, endpoints.Token, result.code, cb.redirectURI, verifier)
	if err != nil {
		return err
	}
	return saveXaiCreds(creds)
}

// XaiAccessToken returns a usable bearer token, refreshing it when it has
// expired and writing the new one back.
func XaiAccessToken(ctx context.Context) (string, error) {
	creds, ok := loadXaiCreds()
	if !ok {
		return "", fmt.Errorf("not signed in to xAI")
	}
	if time.Now().UnixMilli() < creds.Expires {
		return creds.Access, nil
	}
	fresh, err := xaiRefresh(ctx, creds)
	if err != nil {
		return "", err
	}
	if err := saveXaiCreds(fresh); err != nil {
		return "", err
	}
	return fresh.Access, nil
}

// XaiSignedIn reports whether a subscription login is stored.
func XaiSignedIn() bool {
	_, ok := loadXaiCreds()
	return ok
}

func xaiDiscover(ctx context.Context) (xaiEndpoints, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, xaiDiscovery, nil)
	if err != nil {
		return xaiEndpoints{}, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return xaiEndpoints{}, fmt.Errorf("xai: discovery failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return xaiEndpoints{}, fmt.Errorf("xai: discovery returned %d", resp.StatusCode)
	}
	var e xaiEndpoints
	if err := json.NewDecoder(resp.Body).Decode(&e); err != nil {
		return xaiEndpoints{}, fmt.Errorf("xai: discovery is not readable: %w", err)
	}
	if err := validateXaiEndpoint(e.Authorization, "authorization_endpoint"); err != nil {
		return xaiEndpoints{}, err
	}
	if err := validateXaiEndpoint(e.Token, "token_endpoint"); err != nil {
		return xaiEndpoints{}, err
	}
	return e, nil
}

// validateXaiEndpoint refuses anything that is not HTTPS and not x.ai.
//
// Discovery is a document fetched over the network. Following whatever it
// names — to a token endpoint, with an authorization code in hand — is how a
// compromised resolver walks off with an account.
func validateXaiEndpoint(raw, field string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("xai: discovery returned an invalid %s: %q", field, raw)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("xai: %s must be https: %q", field, raw)
	}
	host := strings.ToLower(u.Hostname())
	if host != "x.ai" && !strings.HasSuffix(host, ".x.ai") {
		return fmt.Errorf("xai: refusing a non-x.ai %s: %q", field, raw)
	}
	return nil
}

type callbackResult struct {
	code, state string
	err         string
}

type callbackServer struct {
	srv         *http.Server
	ln          net.Listener
	redirectURI string
	results     chan callbackResult
}

func startCallbackServer() (*callbackServer, error) {
	ln, err := net.Listen("tcp", fmt.Sprintf("%s:%d", xaiCallbackHost, xaiCallbackPort))
	if err != nil {
		// The registered port is taken — another sign-in, or another tool.
		// Any port still works provided the server accepts the redirect.
		ln, err = net.Listen("tcp", xaiCallbackHost+":0")
		if err != nil {
			return nil, fmt.Errorf("xai: cannot listen for the callback: %w", err)
		}
	}
	cb := &callbackServer{
		ln:      ln,
		results: make(chan callbackResult, 1),
		redirectURI: fmt.Sprintf("http://%s:%d%s",
			xaiCallbackHost, ln.Addr().(*net.TCPAddr).Port, xaiCallbackPath),
	}
	mux := http.NewServeMux()
	mux.HandleFunc(xaiCallbackPath, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		res := callbackResult{code: q.Get("code"), state: q.Get("state")}
		if e := q.Get("error"); e != "" {
			res.err = e
			if d := q.Get("error_description"); d != "" {
				res.err = d
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if res.err != "" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte("<h1>xAI authorization failed.</h1><p>You can close this tab.</p>"))
		} else {
			w.Write([]byte("<h1>xAI authorization received.</h1><p>You can close this tab and return to pi-agent.</p>"))
		}
		select {
		case cb.results <- res:
		default:
		}
	})
	cb.srv = &http.Server{Handler: mux}
	go cb.srv.Serve(ln)
	return cb, nil
}

func (c *callbackServer) wait(ctx context.Context, timeout time.Duration) (callbackResult, error) {
	select {
	case res := <-c.results:
		if res.err != "" {
			return res, fmt.Errorf("xai: authorization failed: %s", res.err)
		}
		if res.code == "" {
			return res, fmt.Errorf("xai: the callback carried no authorization code")
		}
		return res, nil
	case <-ctx.Done():
		return callbackResult{}, ctx.Err()
	case <-time.After(timeout):
		return callbackResult{}, fmt.Errorf("xai: timed out waiting for the browser")
	}
}

func (c *callbackServer) close() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = c.srv.Shutdown(ctx)
}

func xaiExchange(ctx context.Context, tokenEndpoint, code, redirectURI, verifier string) (XaiCreds, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"client_id":     {xaiClientID},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"code_verifier": {verifier},
	}
	payload, err := postForm(ctx, tokenEndpoint, form)
	if err != nil {
		return XaiCreds{}, fmt.Errorf("xai: token exchange failed: %w", err)
	}
	access, _ := payload["access_token"].(string)
	refresh, _ := payload["refresh_token"].(string)
	if access == "" {
		return XaiCreds{}, fmt.Errorf("xai: token exchange returned no access token")
	}
	if refresh == "" {
		// Without one the session dies at the first expiry and the user is
		// sent back through the browser with no explanation.
		return XaiCreds{}, fmt.Errorf("xai: token exchange returned no refresh token")
	}
	return XaiCreds{
		Access:        access,
		Refresh:       refresh,
		Expires:       expiryFrom(payload),
		TokenEndpoint: tokenEndpoint,
		TokenType:     stringOr(payload["token_type"], "Bearer"),
	}, nil
}

func xaiRefresh(ctx context.Context, creds XaiCreds) (XaiCreds, error) {
	endpoint := creds.TokenEndpoint
	if endpoint == "" {
		e, err := xaiDiscover(ctx)
		if err != nil {
			return XaiCreds{}, err
		}
		endpoint = e.Token
	}
	// Re-checked even though it was checked when stored: the file is on disk
	// and editable, and this is the request that carries the refresh token.
	if err := validateXaiEndpoint(endpoint, "token_endpoint"); err != nil {
		return XaiCreds{}, err
	}
	if creds.Refresh == "" {
		return XaiCreds{}, fmt.Errorf("xai: no refresh token — sign in again")
	}
	payload, err := postForm(ctx, endpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {xaiClientID},
		"refresh_token": {creds.Refresh},
	})
	if err != nil {
		return XaiCreds{}, fmt.Errorf("xai: token refresh failed: %w", err)
	}
	access, _ := payload["access_token"].(string)
	if access == "" {
		return XaiCreds{}, fmt.Errorf("xai: token refresh returned no access token")
	}
	out := creds
	out.Access = access
	out.Expires = expiryFrom(payload)
	out.TokenEndpoint = endpoint
	// A rotated refresh token replaces the old one; a server that does not
	// rotate simply omits it, and the existing one stays valid.
	if r, ok := payload["refresh_token"].(string); ok && r != "" {
		out.Refresh = r
	}
	return out, nil
}

func postForm(ctx context.Context, endpoint string, form url.Values) (map[string]any, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var payload map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("status %d, and the body is not readable", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		// The provider's own message, which is the only one that says what is
		// actually wrong — an expired grant, a revoked client.
		if msg, ok := payload["error_description"].(string); ok && msg != "" {
			return nil, fmt.Errorf("status %d: %s", resp.StatusCode, msg)
		}
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return payload, nil
}

func expiryFrom(payload map[string]any) int64 {
	seconds := 3600.0
	if v, ok := payload["expires_in"].(float64); ok && v > 0 {
		seconds = v
	}
	return time.Now().Add(time.Duration(seconds)*time.Second - refreshSkew).UnixMilli()
}

func stringOr(v any, fallback string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return fallback
}

func pkce() (verifier, challenge string, err error) {
	verifier, err = randomToken(32)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// loadXaiCreds reads the OAuth entry from the shared auth file.
func loadXaiCreds() (XaiCreds, bool) {
	doc, err := readAuthDoc()
	if err != nil {
		return XaiCreds{}, false
	}
	providers, _ := doc["providers"].(map[string]any)
	entry, _ := providers["xai"].(map[string]any)
	if entry == nil || entry["mode"] != "oauth" {
		return XaiCreds{}, false
	}
	raw, err := json.Marshal(entry["xai"])
	if err != nil {
		return XaiCreds{}, false
	}
	var creds XaiCreds
	if err := json.Unmarshal(raw, &creds); err != nil || creds.Access == "" {
		return XaiCreds{}, false
	}
	return creds, true
}

func saveXaiCreds(creds XaiCreds) error {
	doc, err := readAuthDoc()
	if err != nil {
		doc = map[string]any{}
	}
	providers, _ := doc["providers"].(map[string]any)
	if providers == nil {
		providers = map[string]any{}
	}
	providers["xai"] = map[string]any{
		"mode": "oauth", "provider": "xai", "xai": creds,
	}
	doc["providers"] = providers
	if _, ok := doc["active"]; !ok {
		doc["active"] = "xai"
	}
	return writeAuthDoc(doc)
}

// LogoutXai removes the subscription credentials.
func LogoutXai() error {
	doc, err := readAuthDoc()
	if err != nil {
		return fmt.Errorf("no stored credentials")
	}
	providers, _ := doc["providers"].(map[string]any)
	if providers == nil {
		return fmt.Errorf("no stored credentials")
	}
	if _, ok := providers["xai"]; !ok {
		return fmt.Errorf("not signed in to xAI")
	}
	delete(providers, "xai")
	doc["providers"] = providers
	return writeAuthDoc(doc)
}
