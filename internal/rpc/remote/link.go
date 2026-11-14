// Package remote is the client half of serve pairing — loop's
// rpc/remote-host.ts: how THIS pi adds another machine running `loop serve`
// or `pi serve` and talks to it, the same thing the desktop's Settings →
// Connections and the phone's "Add environment" do.
//
//	Pair(link)  the link serve prints → a saved host
//	Client      one WebSocket per host: JSON-RPC calls plus the
//	            session.event stream, reconnecting on its own
//
// The protocol is the one every client already speaks (rpc/server.go):
// session.list / history / attach {afterSeq} / send / cancel / answer.
package remote

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai/jsonopt"
	"github.com/notshekhar/pi/internal/rpc"
	"github.com/notshekhar/pi/internal/rpc/serve"
)

// PairingLink is a host's base URL and the token to pair with.
type PairingLink struct {
	// URL is http://host:port, nothing after it.
	URL   string
	Token string
}

var reScheme = regexp.MustCompile(`(?i)^[a-z][a-z0-9+.-]*://`)

// ParsePairingLink reads what a person pastes: the URL serve prints
// (?token= or #token=), or a bare host[:port] with the token given
// separately. ok is false when there is no host or no token to be found.
func ParsePairingLink(input, separateToken string) (PairingLink, bool) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return PairingLink{}, false
	}
	hadScheme := reScheme.MatchString(raw)
	full := raw
	if !hadScheme {
		full = "http://" + raw
	}
	u, err := url.Parse(full)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return PairingLink{}, false
	}
	token := linkToken(u, separateToken)
	if token == "" {
		return PairingLink{}, false
	}
	port := u.Port()
	// A browser URL drops the scheme's own port, and so does loop's.
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	// A bare host means serve's default port; an explicit URL keeps its own
	// (including none, for a tunnel on 80/443).
	if port == "" && !hadScheme {
		port = strconv.Itoa(serve.DefaultPort)
	}
	host := strings.ToLower(u.Hostname())
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port != "" {
		host += ":" + port
	}
	return PairingLink{URL: u.Scheme + "://" + host, Token: token}, true
}

func linkToken(u *url.URL, separate string) string {
	if t := strings.TrimSpace(separate); t != "" {
		return t
	}
	if t := strings.TrimSpace(u.Query().Get("token")); t != "" {
		return t
	}
	fragment, _ := url.ParseQuery(u.Fragment)
	return strings.TrimSpace(fragment.Get("token"))
}

// Environment is who answers at a host's URL.
type Environment struct {
	EnvironmentID string `json:"environmentId"`
	Label         string `json:"label"`
	ServerVersion string `json:"serverVersion,omitempty"`
	Platform      *struct {
		OS   string `json:"os,omitempty"`
		Arch string `json:"arch,omitempty"`
	} `json:"platform,omitzero"`
	// Protocol is the host's; zero on a host older than the handshake (1.0).
	Protocol          [2]int `json:"-"`
	MinClientProtocol [2]int `json:"-"`
}

// protocolOrDefault: a host that predates the handshake is protocol 1.0 and
// takes any client.
func protocolOrDefault(v [2]int) [2]int {
	if v == ([2]int{}) {
		return [2]int{1, 0}
	}
	return v
}

// ProtocolMismatch is "" when this pi can talk to the host, else the
// sentence saying which side to update.
func ProtocolMismatch(env Environment) string {
	_, err := rpc.NegotiateProtocol(rpc.ProtocolVersion, protocolOrDefault(env.Protocol), protocolOrDefault(env.MinClientProtocol))
	if err == nil {
		return ""
	}
	return forThisPi(err.Error())
}

// forThisPi: the protocol messages are written for an app; here the client
// is a pi.
func forThisPi(message string) string {
	message = strings.Replace(message, "Update the app.", "Update this pi (`pi update`).", 1)
	return strings.ReplaceAll(message, "this app", "this pi")
}

// HTTPClient is the seam requests go through; tests swap it.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Timeouts, loop's.
const (
	probeTimeout  = 4 * time.Second
	ticketTimeout = 5 * time.Second
)

func httpClient(c HTTPClient) HTTPClient {
	if c == nil {
		return http.DefaultClient
	}
	return c
}

// Probe asks who answers at url. Public on the host, so it needs no token.
func Probe(ctx context.Context, c HTTPClient, base string) (Environment, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/.well-known/loop/environment", nil)
	if err != nil {
		return Environment{}, err
	}
	res, err := httpClient(c).Do(req)
	if err != nil {
		return Environment{}, err
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return Environment{}, fmt.Errorf("%s answered %d — is it a loop serve?", base, res.StatusCode)
	}
	return decodeEnvironment(res.Body, base)
}

func decodeEnvironment(body io.Reader, base string) (Environment, error) {
	var raw struct {
		Environment
		Protocol          any `json:"protocol"`
		MinClientProtocol any `json:"minClientProtocol"`
	}
	if json.UnmarshalRead(body, &raw, jsonopt.Read) != nil || raw.EnvironmentID == "" {
		return Environment{}, fmt.Errorf("%s is not a loop serve", base)
	}
	env := raw.Environment
	if env.Label == "" {
		if u, err := url.Parse(base); err == nil {
			env.Label = u.Hostname()
		}
	}
	env.Protocol, _ = rpc.ParseProtocol(raw.Protocol)
	env.MinClientProtocol, _ = rpc.ParseProtocol(raw.MinClientProtocol)
	return env, nil
}

// Pair checks the link against the host — it must be a loop serve, and the
// token must be its token — and saves it. Pairing the same machine again (a
// new address, a rotated token) replaces its entry rather than adding one.
func Pair(ctx context.Context, c HTTPClient, link PairingLink) (HostRecord, error) {
	env, err := Probe(ctx, c, link.URL)
	if err != nil {
		return HostRecord{}, err
	}
	// Refused before it is saved: a host this pi cannot talk to would
	// otherwise sit in /hosts failing on every open.
	if mismatch := ProtocolMismatch(env); mismatch != "" {
		return HostRecord{}, fmt.Errorf("%s: %s", env.Label, mismatch)
	}
	token, err := exchangeToken(ctx, c, link, env.Label)
	if err != nil {
		return HostRecord{}, err
	}
	hosts := loadHosts()
	record := HostRecord{ID: env.EnvironmentID, Label: env.Label, URL: link.URL, Token: token, AddedAt: time.Now().UnixMilli()}
	var kept []HostRecord
	for _, h := range hosts {
		if h.ID == record.ID {
			// A name given here survives re-pairing; so does when it was added.
			record.Label, record.AddedAt = h.Label, h.AddedAt
			continue
		}
		kept = append(kept, h)
	}
	return record, saveHosts(append(kept, record))
}

const tokenExchangeGrant = "urn:ietf:params:oauth:grant-type:token-exchange"

// exchangeToken is what the host hands back: the credential to keep. It is
// the link's own token when pairing by link; pairing by a six-digit code it
// is the token that code stood in for — the code itself is spent.
func exchangeToken(ctx context.Context, c HTTPClient, link PairingLink, label string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	form := url.Values{
		"grant_type":         {tokenExchangeGrant},
		"subject_token":      {link.Token},
		"subject_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, link.URL+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("content-type", "application/x-www-form-urlencoded")
	res, err := httpClient(c).Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var body struct {
		AccessToken      string `json:"access_token"`
		ErrorDescription string `json:"error_description"`
	}
	_ = json.UnmarshalRead(res.Body, &body, jsonopt.Read)
	if res.StatusCode < 200 || res.StatusCode > 299 {
		if body.ErrorDescription != "" {
			return "", fmt.Errorf("%s: %s", label, body.ErrorDescription)
		}
		return "", fmt.Errorf("%s refused that token — copy the link `serve` prints there", label)
	}
	if body.AccessToken != "" {
		return body.AccessToken, nil
	}
	return link.Token, nil
}

// Hosts is every paired machine.
func Hosts() []HostRecord { return loadHosts() }

// ErrUnknownHost is Remove finding nothing to forget.
var ErrUnknownHost = errors.New("no paired host by that name")

// Remove forgets a host by id or (case-insensitive) label, returning it.
func Remove(idOrLabel string) (HostRecord, error) {
	hosts := loadHosts()
	needle := strings.ToLower(strings.TrimSpace(idOrLabel))
	for i, h := range hosts {
		if h.ID == idOrLabel || strings.ToLower(h.Label) == needle {
			return h, saveHosts(append(hosts[:i:i], hosts[i+1:]...))
		}
	}
	return HostRecord{}, ErrUnknownHost
}

// Rename gives a host a name of this pi's choosing.
func Rename(id, label string) error {
	name := strings.TrimSpace(label)
	if name == "" {
		return nil
	}
	hosts := loadHosts()
	for i := range hosts {
		if hosts[i].ID == id {
			hosts[i].Label = name
		}
	}
	return saveHosts(hosts)
}
