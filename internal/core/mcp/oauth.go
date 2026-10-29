package mcp

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/core/home"
	"github.com/notshekhar/pi/internal/core/jsonstore"
)

// OAuth 2.1 for remote MCP servers — @ai-sdk/mcp's auth() and loop's
// McpOAuthProvider (mcp/oauth.ts), in Go.
//
// Tokens live in ~/.loop/mcp-auth.json in loop's exact shape, so a server
// signed in from loop is signed in for pi and the other way round. The flow:
// protected-resource metadata (RFC 9728) names the authorization server, its
// metadata (RFC 8414 / OIDC discovery) names the endpoints, dynamic client
// registration (RFC 7591) mints a client unless one is configured, and the
// login is authorization code + PKCE S256 with a resource indicator
// (RFC 8707). A 401 on a live connection first tries the refresh token; only
// an interactive login may open a browser.

// --- storage ------------------------------------------------------------------

// StoredAuth is everything kept for one server's session (loop's StoredAuth).
// Client information and tokens are kept as raw objects so fields this
// client does not know survive a round trip through it.
type StoredAuth struct {
	ClientInformation   map[string]any       `json:"clientInformation,omitempty"`
	Tokens              map[string]any       `json:"tokens,omitempty"`
	CodeVerifier        string               `json:"codeVerifier,omitempty"`
	State               string               `json:"state,omitempty"`
	AuthorizationServer *authorizationServer `json:"authorizationServer,omitempty"`
}

func (a StoredAuth) empty() bool {
	return len(a.ClientInformation) == 0 && len(a.Tokens) == 0 && a.CodeVerifier == "" && a.State == "" && a.AuthorizationServer == nil
}

// authorizationServer is which authorization server issued a session.
type authorizationServer struct {
	Issuer                 string `json:"issuer,omitempty"`
	AuthorizationServerURL string `json:"authorizationServerUrl"`
	TokenEndpoint          string `json:"tokenEndpoint"`
}

// authMu serializes read-modify-write of the token file within the
// process; the store re-reads the file before every change, so a write
// another process made meanwhile is not lost.
var (
	authMu    sync.Mutex
	authStore *jsonstore.Store
)

// AuthStorePath is ~/.loop/mcp-auth.json.
func AuthStorePath() string { return home.Path("mcp-auth.json") }

// store is the token file under the current HOME (callers hold authMu).
func store() *jsonstore.Store {
	if path := AuthStorePath(); authStore == nil || authStore.Path() != path {
		authStore = jsonstore.New(path, nil)
	}
	return authStore
}

// Configstore reads a key with a dot as a nested path, so loop files a
// server named "a.b" under {"a": {"b": …}}; reading and writing it here the
// same way keeps the two symmetric.
func authPath(server string) []string { return strings.Split(server, ".") }

func readAuth(server string) StoredAuth {
	authMu.Lock()
	defer authMu.Unlock()
	return readAuthLocked(server)
}

func readAuthLocked(server string) StoredAuth {
	s := store()
	// Another process (loop, another pi) may have refreshed the tokens.
	s.Refresh()
	path := authPath(server)
	raw := s.Raw(path[0])
	for _, key := range path[1:] {
		var m map[string]json.RawMessage
		if json.Unmarshal(raw, &m) != nil {
			return StoredAuth{}
		}
		raw = m[key]
	}
	var out StoredAuth
	_ = json.Unmarshal(raw, &out)
	return out
}

func writeAuthLocked(server string, a *StoredAuth) error {
	path := authPath(server)
	return store().Mutate(func(o *jsonstore.Object) {
		if len(path) == 1 {
			if a == nil {
				o.Delete(path[0])
			} else {
				_ = o.Set(path[0], a)
			}
			return
		}
		var root map[string]any
		_ = json.Unmarshal(o.Raw(path[0]), &root)
		if root == nil {
			root = map[string]any{}
		}
		node := root
		for _, key := range path[1 : len(path)-1] {
			next, _ := node[key].(map[string]any)
			if next == nil {
				next = map[string]any{}
				node[key] = next
			}
			node = next
		}
		last := path[len(path)-1]
		if a == nil {
			delete(node, last)
		} else {
			raw, _ := json.Marshal(a)
			var v any
			_ = json.Unmarshal(raw, &v)
			node[last] = v
		}
		_ = o.Set(path[0], root)
	})
}

func patchAuth(server string, change func(*StoredAuth)) error {
	authMu.Lock()
	defer authMu.Unlock()
	a := readAuthLocked(server)
	change(&a)
	return writeAuthLocked(server, &a)
}

// HasStoredTokens is true once a server has completed a login.
func HasStoredTokens(server string) bool {
	_, ok := readAuth(server).Tokens["access_token"].(string)
	return ok
}

// ClearAuth forgets a server's OAuth session entirely (delete, re-login).
func ClearAuth(server string) error {
	authMu.Lock()
	defer authMu.Unlock()
	return writeAuthLocked(server, nil)
}

func restoreAuth(server string, a StoredAuth) error {
	authMu.Lock()
	defer authMu.Unlock()
	return writeAuthLocked(server, &a)
}

// IsOAuthServer is whether signing in applies to a server (loop's
// isOAuthServer): a remote one marked oauth, one that answered 401, or one
// already holding a session.
func IsOAuthServer(server string, cfg ServerConfig, needsAuth bool) bool {
	if !cfg.IsHTTP() {
		return false
	}
	return cfg.Auth == "oauth" || needsAuth || HasStoredTokens(server)
}

// --- provider ---------------------------------------------------------------------

// ErrAuthRequired is a server that needs an interactive login before it can
// connect.
type ErrAuthRequired struct{ Server string }

func (e *ErrAuthRequired) Error() string {
	return fmt.Sprintf("MCP server %q requires authorization — run /mcp and choose Authorize", e.Server)
}

// oauthProvider is loop's McpOAuthProvider: one server's OAuth client,
// persisted in the shared token file.
type oauthProvider struct {
	server       string
	redirectURI  string
	clientID     string
	clientSecret string
	scopes       []string
	// openURL is the browser opener for an interactive login; nil during a
	// background connect, where a needed login surfaces as ErrAuthRequired
	// instead of a browser the user did not ask for.
	openURL func(string)
}

func newOAuthProvider(server string, cfg ServerConfig, redirectURI string, open func(string)) *oauthProvider {
	p := &oauthProvider{server: server, redirectURI: redirectURI, clientID: cfg.ClientID, scopes: cfg.Scopes, openURL: open}
	if cfg.ClientSecret != "" {
		p.clientSecret = ResolveSecrets(cfg.ClientSecret)
	}
	return p
}

func (p *oauthProvider) clientMetadata() map[string]any {
	method := "none"
	// A configured secret means a confidential client; otherwise a public
	// one, authenticating the token request with PKCE alone.
	if p.clientSecret != "" {
		method = "client_secret_post"
	}
	md := map[string]any{
		"client_name":                productName,
		"redirect_uris":              []string{p.redirectURI},
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": method,
	}
	if len(p.scopes) > 0 {
		md["scope"] = strings.Join(p.scopes, " ")
	}
	return md
}

// productName is the client_name sent at registration — loop's, so a
// server that allow-lists clients by name treats the two alike.
const productName = "loop"

func (p *oauthProvider) clientInformation() map[string]any {
	// A configured client_id short-circuits dynamic registration.
	if p.clientID != "" {
		ci := map[string]any{"client_id": p.clientID}
		if p.clientSecret != "" {
			ci["client_secret"] = p.clientSecret
		}
		return ci
	}
	return readAuth(p.server).ClientInformation
}

func (p *oauthProvider) tokens() map[string]any { return readAuth(p.server).Tokens }

func (p *oauthProvider) accessToken() string {
	tok, _ := p.tokens()["access_token"].(string)
	return tok
}

// saveTokens carries the previous refresh_token forward when a refresh
// response omits it (RFC 6749 §6: omitted means "keep using it"). Saving
// verbatim drops it, and the next restart has nothing to refresh with.
func (p *oauthProvider) saveTokens(t map[string]any) error {
	return patchAuth(p.server, func(a *StoredAuth) {
		if _, ok := t["refresh_token"]; !ok {
			if prev, ok := a.Tokens["refresh_token"]; ok {
				t["refresh_token"] = prev
			}
		}
		a.Tokens = t
	})
}

func (p *oauthProvider) invalidate(scope string) error {
	if scope == "all" {
		return ClearAuth(p.server)
	}
	return patchAuth(p.server, func(a *StoredAuth) {
		switch scope {
		case "tokens":
			a.Tokens = nil
		case "verifier":
			a.CodeVerifier = ""
		default:
			a.ClientInformation = nil
		}
	})
}

// authorize implements authorizer for the transports: a 401 recovery.
func (p *oauthProvider) authorize(ctx context.Context, serverURL, resourceMetadataURL *url.URL, scope string) (bool, error) {
	res, err := runAuth(ctx, p, authOptions{serverURL: serverURL, resourceMetadataURL: resourceMetadataURL, scope: scope})
	if err != nil {
		return false, err
	}
	return res == authAuthorized, nil
}

// --- the flow -----------------------------------------------------------------------

type authResult int

const (
	authAuthorized authResult = iota
	authRedirect
)

type authOptions struct {
	serverURL           *url.URL
	resourceMetadataURL *url.URL
	scope               string
	authorizationCode   string
	callbackState       string
}

// OAuthError is an error response from an authorization server.
type OAuthError struct {
	Code        string
	Description string
	Status      int
}

func (e *OAuthError) Error() string {
	if e.Description != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Description)
	}
	return e.Code
}

// runAuth is auth(): one attempt, and a second one after a credential the
// server says is dead has been dropped.
func runAuth(ctx context.Context, p *oauthProvider, o authOptions) (authResult, error) {
	res, err := authInternal(ctx, p, o)
	var oe *OAuthError
	if errors.As(err, &oe) {
		switch oe.Code {
		case "invalid_client", "unauthorized_client":
			_ = p.invalidate("all")
			return authInternal(ctx, p, o)
		case "invalid_grant":
			_ = p.invalidate("tokens")
			return authInternal(ctx, p, o)
		}
	}
	return res, err
}

type resourceMetadata struct {
	Resource             string   `json:"resource"`
	AuthorizationServers []string `json:"authorization_servers"`
	ScopesSupported      []string `json:"scopes_supported"`
}

type asMetadata struct {
	Issuer                            string   `json:"issuer"`
	AuthorizationEndpoint             string   `json:"authorization_endpoint"`
	TokenEndpoint                     string   `json:"token_endpoint"`
	RegistrationEndpoint              string   `json:"registration_endpoint"`
	ResponseTypesSupported            []string `json:"response_types_supported"`
	GrantTypesSupported               []string `json:"grant_types_supported"`
	CodeChallengeMethodsSupported     []string `json:"code_challenge_methods_supported"`
	TokenEndpointAuthMethodsSupported []string `json:"token_endpoint_auth_methods_supported"`
}

// authFlow is one pass of auth(): what discovery found, and the client
// the session runs as.
type authFlow struct {
	ctx      context.Context
	p        *oauthProvider
	o        authOptions
	as       *url.URL            // the authorization server
	metadata *asMetadata         // nil when it publishes none
	current  authorizationServer // which server this pass talks to
	resource *url.URL            // RFC 8707 resource, when known
	scope    string
	client   map[string]any
}

func authInternal(ctx context.Context, p *oauthProvider, o authOptions) (authResult, error) {
	f, err := discover(ctx, p, o)
	if err != nil {
		return 0, err
	}
	if err := f.ensureClient(); err != nil {
		return 0, err
	}
	if o.authorizationCode != "" {
		return f.exchangeCode()
	}
	if refreshed, err := f.tryRefresh(); refreshed || err != nil {
		return authAuthorized, err
	}
	return f.startLogin()
}

// discover finds the authorization server (RFC 9728, falling back to the
// server's own URL), its metadata, the resource and the scope to ask for.
func discover(ctx context.Context, p *oauthProvider, o authOptions) (*authFlow, error) {
	if o.resourceMetadataURL != nil && origin(o.resourceMetadataURL) != origin(o.serverURL) {
		return nil, fmt.Errorf("OAuth protected resource metadata URL %s must have the same origin as the MCP server URL %s",
			o.resourceMetadataURL, origin(o.serverURL))
	}
	f := &authFlow{ctx: ctx, p: p, o: o, as: o.serverURL}
	prm, _ := discoverResourceMetadata(ctx, o.serverURL, o.resourceMetadataURL)
	if prm != nil && len(prm.AuthorizationServers) > 0 {
		if u, err := url.Parse(prm.AuthorizationServers[0]); err == nil {
			f.as = u
		}
	}
	var err error
	if f.resource, err = selectResource(o.serverURL, prm); err != nil {
		return nil, err
	}
	if f.metadata, err = discoverASMetadata(ctx, f.as); err != nil {
		return nil, err
	}
	f.current = currentAS(f.as, f.metadata)
	f.scope = selectScope(o.scope, prm, p.clientMetadata())
	return f, nil
}

// selectScope: the challenge's scope, else what the resource lists, else
// the configured one.
func selectScope(challenge string, prm *resourceMetadata, clientMD map[string]any) string {
	if challenge != "" {
		return challenge
	}
	if prm != nil && len(prm.ScopesSupported) > 0 {
		return strings.Join(prm.ScopesSupported, " ")
	}
	scope, _ := clientMD["scope"].(string)
	return scope
}

// ensureClient is the configured or stored client, registering one
// (RFC 7591) when there is neither.
func (f *authFlow) ensureClient() error {
	if f.client = f.p.clientInformation(); f.client != nil {
		return nil
	}
	if f.o.authorizationCode != "" {
		return errors.New("existing OAuth client information is required when exchanging an authorization code")
	}
	reg, err := registerClient(f.ctx, f.as, f.metadata, f.p.clientMetadata(), f.scope)
	if err != nil {
		return err
	}
	stampAS(reg, f.current)
	if err := patchAuth(f.p.server, func(a *StoredAuth) { a.ClientInformation = reg }); err != nil {
		return err
	}
	f.client = reg
	return nil
}

// exchangeCode completes a login: state checked, the same authorization
// server that started it, and the PKCE verifier saved before the redirect.
func (f *authFlow) exchangeCode() (authResult, error) {
	stored := readAuth(f.p.server)
	if stored.State != "" && stored.State != f.o.callbackState {
		return 0, errors.New("OAuth state parameter mismatch - possible CSRF attack")
	}
	prev := storedAS(stored, f.client, nil)
	if prev == nil {
		return 0, errors.New("stored OAuth authorization server metadata is required when exchanging an authorization code")
	}
	if err := f.sameServer(*prev); err != nil {
		return 0, err
	}
	if stored.CodeVerifier == "" {
		return 0, errors.New("no PKCE code verifier stored — restart the authorization flow")
	}
	tokens, err := f.token(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {f.o.authorizationCode},
		"code_verifier": {stored.CodeVerifier},
		"redirect_uri":  {f.p.redirectURI},
	}, "authorization_code")
	if err != nil {
		return 0, err
	}
	return authAuthorized, f.p.saveTokens(tokens)
}

// tryRefresh uses the refresh token, when there is one from this same
// server. A server-side failure falls through to a login; an OAuth error
// that condemns the grant escalates, so runAuth can drop it and retry.
func (f *authFlow) tryRefresh() (bool, error) {
	tokens := f.p.tokens()
	refresh, _ := tokens["refresh_token"].(string)
	if refresh == "" {
		return false, nil
	}
	prev := storedAS(readAuth(f.p.server), f.client, tokens)
	if prev == nil {
		return false, f.p.invalidate("tokens")
	}
	if err := f.sameServer(*prev); err != nil {
		return false, err
	}
	fresh, err := f.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}}, "refresh_token")
	if err != nil {
		var oe *OAuthError
		if errors.As(err, &oe) && oe.Code != "server_error" {
			return false, err
		}
		return false, nil
	}
	if _, ok := fresh["refresh_token"]; !ok {
		fresh["refresh_token"] = refresh
	}
	return true, f.p.saveTokens(fresh)
}

// startLogin saves what the code exchange will need and hands the
// authorization URL to the opener. The state is minted fresh and
// unguessable: it binds the callback to this request, and servers that
// require one refuse a request without it.
func (f *authFlow) startLogin() (authResult, error) {
	state, verifier := randomToken(32), randomToken(32)
	authURL, err := authorizationURL(f.as, f.metadata, f.client, f.p.redirectURI, state, verifier, f.scope, f.resource)
	if err != nil {
		return 0, err
	}
	current := f.current
	if err := patchAuth(f.p.server, func(a *StoredAuth) {
		a.State, a.CodeVerifier, a.AuthorizationServer = state, verifier, &current
	}); err != nil {
		return 0, err
	}
	if f.p.openURL == nil {
		return 0, &ErrAuthRequired{Server: f.p.server}
	}
	f.p.openURL(authURL.String())
	return authRedirect, nil
}

func (f *authFlow) sameServer(prev authorizationServer) error {
	if !sameAS(prev, f.current) {
		return errors.New("OAuth authorization server metadata does not match the metadata that issued the stored credentials")
	}
	return nil
}

// token calls the token endpoint and stamps which server issued the result.
func (f *authFlow) token(form url.Values, grant string) (map[string]any, error) {
	tokens, err := tokenRequest(f.ctx, f.as, f.metadata, f.client, form, f.resource, grant)
	if err != nil {
		return nil, err
	}
	stampAS(tokens, f.current)
	return tokens, nil
}

func origin(u *url.URL) string { return u.Scheme + "://" + u.Host }

func get(ctx context.Context, u *url.URL) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("MCP-Protocol-Version", LatestProtocolVersion)
	req.Header.Set("Accept", "application/json")
	return httpClient.Do(req)
}

func wellKnown(prefix, pathname string) string {
	pathname = strings.TrimSuffix(pathname, "/")
	return "/.well-known/" + prefix + pathname
}

// discoverResourceMetadata is RFC 9728: at the URL the 401 named, else the
// well-known path under the server's path, else at its root.
func discoverResourceMetadata(ctx context.Context, server, metaURL *url.URL) (*resourceMetadata, error) {
	var resp *http.Response
	var err error
	if metaURL != nil {
		resp, err = get(ctx, metaURL)
	} else {
		u := *server
		u.Path, u.RawPath, u.Fragment = wellKnown("oauth-protected-resource", server.Path), "", ""
		resp, err = get(ctx, &u)
		if (err != nil || (resp.StatusCode >= 400 && resp.StatusCode < 500)) && server.Path != "" && server.Path != "/" {
			if resp != nil {
				drain(resp)
			}
			root := &url.URL{Scheme: server.Scheme, Host: server.Host, Path: "/.well-known/oauth-protected-resource"}
			resp, err = get(ctx, root)
		}
	}
	if err != nil {
		return nil, err
	}
	defer drain(resp)
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d loading OAuth protected resource metadata", resp.StatusCode)
	}
	var md resourceMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&md); err != nil {
		return nil, err
	}
	return &md, nil
}

// selectResource is the RFC 8707 resource to ask for: the metadata's, which
// must cover the server's own URL.
func selectResource(server *url.URL, prm *resourceMetadata) (*url.URL, error) {
	if prm == nil || prm.Resource == "" {
		return nil, nil
	}
	configured, err := url.Parse(prm.Resource)
	if err != nil {
		return nil, err
	}
	requested := *server
	requested.Fragment = ""
	withSlash := func(p string) string {
		if strings.HasSuffix(p, "/") {
			return p
		}
		return p + "/"
	}
	if origin(&requested) != origin(configured) || len(requested.Path) < len(configured.Path) ||
		!strings.HasPrefix(withSlash(requested.Path), withSlash(configured.Path)) {
		return nil, fmt.Errorf("protected resource %s does not match expected %s (or origin)", prm.Resource, requested.String())
	}
	return configured, nil
}

// discoverASMetadata tries RFC 8414 then OIDC discovery, path-aware, and
// checks the issuer each document claims. Nil when the server publishes
// none: the default endpoints under the server's URL are used.
func discoverASMetadata(ctx context.Context, as *url.URL) (*asMetadata, error) {
	type candidate struct {
		u      string
		oidc   bool
		issuer string
	}
	root := origin(as)
	path := strings.TrimSuffix(as.Path, "/")
	var tries []candidate
	if path == "" {
		tries = []candidate{
			{root + "/.well-known/oauth-authorization-server", false, root},
			{root + "/.well-known/openid-configuration", true, root},
		}
	} else {
		pathIssuer := root + path
		tries = []candidate{
			{root + "/.well-known/oauth-authorization-server" + path, false, pathIssuer},
			{root + "/.well-known/oauth-authorization-server", false, root},
			{root + "/.well-known/openid-configuration" + path, true, pathIssuer},
			{root + path + "/.well-known/openid-configuration", true, pathIssuer},
		}
	}
	for _, c := range tries {
		u, _ := url.Parse(c.u)
		resp, err := get(ctx, u)
		if err != nil {
			continue
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			drain(resp)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			drain(resp)
			return nil, fmt.Errorf("HTTP %d trying to load OAuth metadata from %s", resp.StatusCode, c.u)
		}
		var md asMetadata
		err = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&md)
		drain(resp)
		if err != nil {
			return nil, fmt.Errorf("malformed OAuth metadata from %s: %w", c.u, err)
		}
		if strings.TrimSuffix(md.Issuer, "/") != strings.TrimSuffix(c.issuer, "/") && md.Issuer != c.issuer {
			return nil, fmt.Errorf("OAuth authorization server metadata issuer %s does not match expected issuer %s", md.Issuer, c.issuer)
		}
		if c.oidc && !contains(md.CodeChallengeMethodsSupported, "S256") {
			return nil, fmt.Errorf("incompatible OIDC provider at %s: does not support the S256 code challenge MCP requires", c.u)
		}
		return &md, nil
	}
	return nil, nil
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func currentAS(as *url.URL, md *asMetadata) authorizationServer {
	out := authorizationServer{Issuer: as.String(), AuthorizationServerURL: as.String()}
	token := as.ResolveReference(&url.URL{Path: "/token"}).String()
	if md != nil {
		if md.Issuer != "" {
			out.Issuer = md.Issuer
		}
		if md.TokenEndpoint != "" {
			token = md.TokenEndpoint
		}
	}
	out.TokenEndpoint = token
	return out
}

func sameAS(a, b authorizationServer) bool {
	if a.Issuer != "" && b.Issuer != "" && a.Issuer != b.Issuer {
		return false
	}
	return a.AuthorizationServerURL == b.AuthorizationServerURL && a.TokenEndpoint == b.TokenEndpoint
}

// storedAS is which authorization server a session came from: stamped on
// the tokens, kept on its own, or stamped on the client information.
func storedAS(a StoredAuth, client, tokens map[string]any) *authorizationServer {
	from := func(m map[string]any) *authorizationServer {
		as, _ := m["authorization_server"].(string)
		te, _ := m["token_endpoint"].(string)
		if as == "" || te == "" {
			return nil
		}
		iss, _ := m["issuer"].(string)
		return &authorizationServer{Issuer: iss, AuthorizationServerURL: as, TokenEndpoint: te}
	}
	if s := from(tokens); s != nil {
		return s
	}
	if a.AuthorizationServer != nil {
		return a.AuthorizationServer
	}
	return from(client)
}

func stampAS(m map[string]any, as authorizationServer) {
	m["issuer"], m["authorization_server"], m["token_endpoint"] = as.Issuer, as.AuthorizationServerURL, as.TokenEndpoint
}

func randomToken(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func resourceString(u *url.URL) string {
	s := u.String()
	if u.Path == "/" {
		s = strings.TrimSuffix(s, "/")
	}
	return s
}

func authorizationURL(as *url.URL, md *asMetadata, client map[string]any, redirect, state, verifier, scope string, resource *url.URL) (*url.URL, error) {
	var u *url.URL
	if md != nil {
		var err error
		if u, err = url.Parse(md.AuthorizationEndpoint); err != nil || md.AuthorizationEndpoint == "" {
			return nil, errors.New("incompatible auth server: no authorization endpoint")
		}
		if len(md.ResponseTypesSupported) > 0 && !contains(md.ResponseTypesSupported, "code") {
			return nil, errors.New("incompatible auth server: does not support response type code")
		}
		if !contains(md.CodeChallengeMethodsSupported, "S256") {
			return nil, errors.New("incompatible auth server: does not support code challenge method S256")
		}
	} else {
		u = as.ResolveReference(&url.URL{Path: "/authorize"})
	}
	if err := safeEndpoint(u); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(verifier))
	clientID, _ := client["client_id"].(string)
	q := u.Query()
	q.Set("response_type", "code")
	q.Set("client_id", clientID)
	q.Set("code_challenge", base64.RawURLEncoding.EncodeToString(sum[:]))
	q.Set("code_challenge_method", "S256")
	q.Set("redirect_uri", redirect)
	q.Set("state", state)
	if scope != "" {
		q.Set("scope", scope)
	}
	if strings.Contains(scope, "offline_access") {
		q.Add("prompt", "consent")
	}
	if resource != nil {
		q.Set("resource", resourceString(resource))
	}
	u.RawQuery = q.Encode()
	return u, nil
}

// safeEndpoint refuses an OAuth endpoint on a private network address,
// except loopback (where a local server legitimately runs its own).
func safeEndpoint(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("OAuth endpoint URL is not allowed: %s", u)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		if ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsLinkLocalMulticast() {
			return fmt.Errorf("OAuth endpoint URL is not allowed: %s", u)
		}
	}
	return nil
}

func registerClient(ctx context.Context, as *url.URL, md *asMetadata, clientMD map[string]any, scope string) (map[string]any, error) {
	var u *url.URL
	if md != nil {
		if md.RegistrationEndpoint == "" {
			return nil, errors.New("incompatible auth server: does not support dynamic client registration")
		}
		var err error
		if u, err = url.Parse(md.RegistrationEndpoint); err != nil {
			return nil, err
		}
	} else {
		u = as.ResolveReference(&url.URL{Path: "/register"})
	}
	if err := safeEndpoint(u); err != nil {
		return nil, err
	}
	body := map[string]any{}
	for k, v := range clientMD {
		body[k] = v
	}
	if scope != "" {
		body["scope"] = scope
	}
	// A loopback redirect is a native app (RFC 8252).
	body["application_type"] = "native"
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return oauthJSON(req)
}

// tokenRequest is the token endpoint: code exchange or refresh.
func tokenRequest(ctx context.Context, as *url.URL, md *asMetadata, client map[string]any, form url.Values, resource *url.URL, grant string) (map[string]any, error) {
	u := as.ResolveReference(&url.URL{Path: "/token"})
	if md != nil && md.TokenEndpoint != "" {
		var err error
		if u, err = url.Parse(md.TokenEndpoint); err != nil {
			return nil, err
		}
	}
	if err := safeEndpoint(u); err != nil {
		return nil, err
	}
	if md != nil && len(md.GrantTypesSupported) > 0 && !contains(md.GrantTypesSupported, grant) {
		return nil, fmt.Errorf("incompatible auth server: does not support grant type %s", grant)
	}
	id, _ := client["client_id"].(string)
	secret, _ := client["client_secret"].(string)
	var methods []string
	if md != nil {
		methods = md.TokenEndpointAuthMethodsSupported
	}
	header := http.Header{}
	switch selectAuthMethod(secret != "", methods) {
	case "client_secret_basic":
		header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(id+":"+secret)))
	case "client_secret_post":
		form.Set("client_id", id)
		form.Set("client_secret", secret)
	default:
		form.Set("client_id", id)
	}
	if resource != nil {
		form.Set("resource", resourceString(resource))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header = header
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	tokens, err := oauthJSON(req)
	if err != nil {
		return nil, err
	}
	if tok, _ := tokens["access_token"].(string); tok == "" {
		return nil, errors.New("token response carried no access_token")
	}
	return tokens, nil
}

func selectAuthMethod(hasSecret bool, supported []string) string {
	switch {
	case len(supported) == 0 && hasSecret:
		return "client_secret_post"
	case len(supported) == 0:
		return "none"
	case hasSecret && contains(supported, "client_secret_basic"):
		return "client_secret_basic"
	case hasSecret && contains(supported, "client_secret_post"):
		return "client_secret_post"
	case contains(supported, "none"):
		return "none"
	case hasSecret:
		return "client_secret_post"
	}
	return "none"
}

// oauthJSON sends an OAuth request (redirects refused) and decodes a JSON
// object, or the error response as an *OAuthError.
func oauthJSON(req *http.Request) (map[string]any, error) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var e struct {
			Error       string `json:"error"`
			Description string `json:"error_description"`
		}
		if json.Unmarshal(body, &e) == nil && e.Error != "" {
			return nil, &OAuthError{Code: e.Error, Description: e.Description, Status: resp.StatusCode}
		}
		return nil, &OAuthError{Code: "server_error", Status: resp.StatusCode,
			Description: fmt.Sprintf("HTTP %d: invalid OAuth error response. Raw body: %s", resp.StatusCode, strings.TrimSpace(string(body)))}
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("malformed OAuth response: %w", err)
	}
	return out, nil
}
