package serve

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/notshekhar/pi/internal/rpc"
)

// Pairing is loop's serve-pairing.ts: how another device (the phone, a
// browser with a second machine, another loop) adds THIS machine as a host.
//
//	GET  /.well-known/loop/environment   who this host is
//	POST /oauth/token                    pairing token (or code) → access token
//	POST /api/auth/websocket-ticket      access token → one-use socket ticket
//	GET  /api/auth/session               is this access token still good
//
// The access token handed out IS the serve token: possession of it was
// already full control. Tickets are the one new secret — random, single-use,
// short-lived — so a socket URL a proxy may log never carries the token. A
// six-digit pairing code is only ever exchanged for the token.

const (
	wsTicketTTL        = 60 * time.Second
	pairingCodeTTL     = 5 * time.Minute
	pairingMaxFailures = 5
	accessTokenTTL     = 365 * 24 * 60 * 60
	tokenExchangeGrant = "urn:ietf:params:oauth:grant-type:token-exchange"
	accessTokenType    = "urn:ietf:params:oauth:token-type:access_token"
)

var standardScopes = []string{"orchestration:read", "orchestration:operate", "terminal:operate", "review:write"}

var pairingPaths = map[string]bool{
	"/.well-known/loop/environment": true, "/oauth/token": true,
	"/api/auth/session": true, "/api/auth/websocket-ticket": true,
}

var codeRE = regexp.MustCompile(`^\d{6}$`)

// NormalizePairingCode is digits only, so "123 456" and "123-456" both work.
func NormalizePairingCode(in string) string {
	d := strings.NewReplacer(" ", "", "\t", "", "\n", "", "-", "").Replace(in)
	if codeRE.MatchString(d) {
		return d
	}
	return ""
}

// PairingCode is a code to show.
type PairingCode struct {
	Code      string
	ExpiresAt time.Time
}

// EnvironmentID is stable for as long as the token is, derived one-way.
func EnvironmentID(token string) string {
	sum := sha256.Sum256([]byte("loop-environment:" + token))
	return "loop-" + hex.EncodeToString(sum[:])[:24]
}

func platformInfo() map[string]string {
	os := map[string]string{"darwin": "darwin", "linux": "linux", "windows": "windows"}[runtime.GOOS]
	if os == "" {
		os = "unknown"
	}
	arch := map[string]string{"arm64": "arm64", "amd64": "x64"}[runtime.GOARCH]
	if arch == "" {
		arch = "other"
	}
	return map[string]string{"os": os, "arch": arch}
}

type pairing struct {
	token, version, label string
	matches               func(string) bool

	mu       sync.Mutex
	tickets  map[string]time.Time
	code     *PairingCode
	failures int
}

func newPairing(token, version string, matches func(string) bool) *pairing {
	h, _ := os.Hostname()
	label := strings.TrimSuffix(h, ".local")
	if label == "" {
		label = "loop"
	}
	return &pairing{token: token, version: version, label: label, matches: matches, tickets: map[string]time.Time{}}
}

func cors(h http.Header) {
	h.Set("access-control-allow-origin", "*")
	h.Set("access-control-allow-methods", "GET, POST, OPTIONS")
	h.Set("access-control-allow-headers", "authorization, content-type, dpop")
	h.Set("access-control-max-age", "600")
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	cors(w.Header())
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func bearerToken(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("authorization"))
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (p *pairing) liveCode() *PairingCode {
	if p.code != nil && !time.Now().Before(p.code.ExpiresAt) {
		p.code = nil
	}
	return p.code
}

func (p *pairing) redeemCode(candidate string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	cur := p.liveCode()
	if cur == nil {
		return false
	}
	if candidate == cur.Code {
		p.code = nil
		return true
	}
	p.failures++
	if p.failures >= pairingMaxFailures {
		p.code = nil
	}
	return false
}

// handle answers a pairing route; false when r is not one.
func (p *pairing) handle(w http.ResponseWriter, r *http.Request) bool {
	if !pairingPaths[r.URL.Path] {
		return false
	}
	if r.Method == http.MethodOptions {
		cors(w.Header())
		if req := r.Header.Get("access-control-request-headers"); req != "" {
			w.Header().Set("access-control-allow-headers", req)
		}
		w.WriteHeader(http.StatusNoContent)
		return true
	}
	switch r.URL.Path {
	case "/.well-known/loop/environment":
		writeJSON(w, 200, map[string]any{
			"environmentId": EnvironmentID(p.token), "label": p.label, "platform": platformInfo(),
			"serverVersion": p.version, "protocol": rpc.ProtocolVersion, "minClientProtocol": rpc.MinClientProtocol,
			"capabilities": map[string]bool{"repositoryIdentity": false, "connectionProbe": true},
		})
	case "/oauth/token":
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
			return true
		}
		_ = r.ParseForm()
		form := r.PostForm
		if form.Get("grant_type") != tokenExchangeGrant {
			writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type", "error_description": "Only token exchange is supported."})
			return true
		}
		subject := form.Get("subject_token")
		code := NormalizePairingCode(subject)
		if !p.matches(subject) && !(code != "" && p.redeemCode(code)) {
			desc := "That pairing link is not valid for this loop."
			if code != "" {
				desc = "That code is wrong, used or expired. Show a new one on the computer (it prints one with loop serve, or /rc) and try again."
			}
			writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": desc})
			return true
		}
		writeJSON(w, 200, map[string]any{"access_token": p.token, "issued_token_type": accessTokenType,
			"token_type": "Bearer", "expires_in": accessTokenTTL, "scope": strings.Join(standardScopes, " ")})
	case "/api/auth/session":
		ok := p.matches(bearerToken(r))
		body := map[string]any{"authenticated": ok, "auth": map[string]any{
			"policy": "remote-reachable", "bootstrapMethods": []string{"one-time-token"},
			"sessionMethods": []string{"bearer-access-token"}, "sessionCookieName": tokenCookie}}
		if ok {
			body["scopes"] = standardScopes
			body["sessionMethod"] = "bearer-access-token"
		}
		writeJSON(w, 200, body)
	case "/api/auth/websocket-ticket":
		if r.Method != http.MethodPost {
			writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
			return true
		}
		if !p.matches(bearerToken(r)) {
			writeJSON(w, 401, map[string]string{"_tag": "EnvironmentAuthenticationError", "message": "Not paired."})
			return true
		}
		p.mu.Lock()
		now := time.Now()
		for t, exp := range p.tickets {
			if !now.Before(exp) {
				delete(p.tickets, t)
			}
		}
		ticket := randomHex(24)
		exp := now.Add(wsTicketTTL)
		p.tickets[ticket] = exp
		p.mu.Unlock()
		writeJSON(w, 200, map[string]string{"ticket": ticket, "expiresAt": exp.UTC().Format("2006-01-02T15:04:05.000Z")})
	}
	return true
}

// issueCode replaces any earlier code: there is only ever one to guess at.
func (p *pairing) issueCode() PairingCode {
	n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
	p.mu.Lock()
	defer p.mu.Unlock()
	p.code = &PairingCode{Code: fmt.Sprintf("%06d", n.Int64()), ExpiresAt: time.Now().Add(pairingCodeTTL)}
	p.failures = 0
	return *p.code
}

func (p *pairing) currentCode() *PairingCode {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.liveCode(); c != nil {
		cp := *c
		return &cp
	}
	return nil
}

func (p *pairing) redeemTicket(t string) bool {
	if t == "" {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	exp, ok := p.tickets[t]
	if !ok {
		return false
	}
	delete(p.tickets, t)
	return time.Now().Before(exp)
}
