package googlevertex

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// cloudPlatformScope is the OAuth scope Vertex AI requires.
const cloudPlatformScope = "https://www.googleapis.com/auth/cloud-platform"

// tokenEndpoint exchanges credentials for an access token.
const tokenEndpoint = "https://oauth2.googleapis.com/token"

// metadataHost is the GCE metadata server, used when running on Google Cloud.
const metadataHost = "http://169.254.169.254"

// tokenExpiryMargin renews a token slightly before it expires, so a request
// that is slow to reach Google does not arrive with a dead credential.
const tokenExpiryMargin = 60 * time.Second

// TokenSource returns an OAuth access token for Vertex AI.
type TokenSource func(ctx context.Context) (string, error)

// cachedToken memoises a token until shortly before it expires.
//
// Token minting costs a network round trip (or a gcloud subprocess), so doing
// it per request would add noticeable latency to every model call.
type cachedToken struct {
	mu      sync.Mutex
	token   string
	expires time.Time
	mint    func(ctx context.Context) (string, time.Time, error)
}

// get returns a valid token, minting a new one when the cached one is stale.
func (c *cachedToken) get(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.token != "" && time.Now().Before(c.expires.Add(-tokenExpiryMargin)) {
		return c.token, nil
	}

	token, expires, err := c.mint(ctx)
	if err != nil {
		return "", err
	}
	c.token, c.expires = token, expires
	return token, nil
}

// defaultTokenSource resolves Application Default Credentials the way
// Google's own libraries do, in this order:
//
//  1. the service account or user credentials named by
//     GOOGLE_APPLICATION_CREDENTIALS;
//  2. the gcloud application-default credentials file;
//  3. the GCE metadata server, when running on Google Cloud;
//  4. `gcloud auth print-access-token`, which covers a developer machine
//     whose credentials gcloud holds but has not written out.
//
// Each step is tried only if the previous one found no credentials at all; a
// credential that exists but fails to mint a token is reported rather than
// skipped, so a misconfiguration is visible instead of silently falling
// through to a different identity.
func defaultTokenSource(httpClient *http.Client) TokenSource {
	cache := &cachedToken{}
	cache.mint = func(ctx context.Context) (string, time.Time, error) {
		if path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS"); path != "" {
			return tokenFromFile(ctx, httpClient, path)
		}
		if path := wellKnownCredentialsPath(); path != "" {
			if _, err := os.Stat(path); err == nil {
				return tokenFromFile(ctx, httpClient, path)
			}
		}
		if onGCE(ctx, httpClient) {
			return tokenFromMetadata(ctx, httpClient)
		}
		return tokenFromGcloudCLI(ctx)
	}

	return cache.get
}

// wellKnownCredentialsPath returns the path gcloud writes application-default
// credentials to.
func wellKnownCredentialsPath() string {
	const filename = "application_default_credentials.json"

	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return ""
		}
		return filepath.Join(appData, "gcloud", filename)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".config", "gcloud", filename)
}

// credentialsFile is the shape of an ADC JSON file. Which fields are present
// depends on Type.
type credentialsFile struct {
	Type string `json:"type"`

	// service_account
	ClientEmail string `json:"client_email"`
	PrivateKey  string `json:"private_key"`
	TokenURI    string `json:"token_uri"`
	ProjectID   string `json:"project_id"`

	// authorized_user
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	RefreshToken string `json:"refresh_token"`

	// impersonated_service_account and external_account
	ServiceAccountImpersonationURL string `json:"service_account_impersonation_url"`
}

// tokenFromFile mints a token from a credentials file.
func tokenFromFile(ctx context.Context, client *http.Client, path string) (string, time.Time, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("googlevertex: reading credentials %s: %w", path, err)
	}

	var creds credentialsFile
	if err := json.Unmarshal(raw, &creds); err != nil {
		return "", time.Time{}, fmt.Errorf("googlevertex: parsing credentials %s: %w", path, err)
	}

	switch creds.Type {
	case "service_account":
		return tokenFromServiceAccount(ctx, client, &creds)
	case "authorized_user":
		return tokenFromRefreshToken(ctx, client, &creds)
	default:
		// Workload identity federation and impersonation need flows this
		// package does not implement. Say so plainly and point at the
		// supported escape hatch.
		return "", time.Time{}, fmt.Errorf(
			"googlevertex: credential type %q is not supported; "+
				"pass Options.TokenSource or run `gcloud auth application-default login`",
			creds.Type,
		)
	}
}

// tokenFromServiceAccount runs the JWT bearer flow: sign an assertion with the
// account's private key, then exchange it for an access token.
func tokenFromServiceAccount(ctx context.Context, client *http.Client, creds *credentialsFile) (string, time.Time, error) {
	key, err := parsePrivateKey(creds.PrivateKey)
	if err != nil {
		return "", time.Time{}, err
	}

	tokenURI := creds.TokenURI
	if tokenURI == "" {
		tokenURI = tokenEndpoint
	}

	now := time.Now()
	claims := map[string]any{
		"iss":   creds.ClientEmail,
		"scope": cloudPlatformScope,
		"aud":   tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}

	assertion, err := signJWT(claims, key)
	if err != nil {
		return "", time.Time{}, err
	}

	return exchange(ctx, client, tokenURI, url.Values{
		"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"},
		"assertion":  {assertion},
	})
}

// tokenFromRefreshToken exchanges gcloud user credentials for an access token.
func tokenFromRefreshToken(ctx context.Context, client *http.Client, creds *credentialsFile) (string, time.Time, error) {
	return exchange(ctx, client, tokenEndpoint, url.Values{
		"grant_type":    {"refresh_token"},
		"client_id":     {creds.ClientID},
		"client_secret": {creds.ClientSecret},
		"refresh_token": {creds.RefreshToken},
	})
}

// exchange posts a token request and reads the response.
func exchange(ctx context.Context, client *http.Client, endpoint string, form url.Values) (string, time.Time, error) {
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("googlevertex: token request failed: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
		Error       string `json:"error"`
		ErrorDesc   string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", time.Time{}, fmt.Errorf("googlevertex: decoding token response: %w", err)
	}

	if body.AccessToken == "" {
		detail := body.ErrorDesc
		if detail == "" {
			detail = body.Error
		}
		if detail == "" {
			detail = resp.Status
		}
		return "", time.Time{}, fmt.Errorf("googlevertex: token request rejected: %s", detail)
	}

	expires := time.Now().Add(time.Duration(body.ExpiresIn) * time.Second)
	return body.AccessToken, expires, nil
}

// onGCE reports whether the metadata server is reachable.
func onGCE(ctx context.Context, client *http.Client) bool {
	if os.Getenv("GCE_METADATA_HOST") != "" {
		return true
	}
	if client == nil {
		client = http.DefaultClient
	}

	// The probe is short: off Google Cloud the address is unroutable and would
	// otherwise stall startup until the connection times out.
	ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataHost, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.Header.Get("Metadata-Flavor") == "Google"
}

// tokenFromMetadata reads a token from the GCE metadata server.
func tokenFromMetadata(ctx context.Context, client *http.Client) (string, time.Time, error) {
	host := metadataHost
	if h := os.Getenv("GCE_METADATA_HOST"); h != "" {
		host = "http://" + h
	}
	endpoint := host + "/computeMetadata/v1/instance/service-accounts/default/token"

	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", time.Time{}, err
	}
	req.Header.Set("Metadata-Flavor", "Google")

	resp, err := client.Do(req)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("googlevertex: metadata token request failed: %w", err)
	}
	defer resp.Body.Close()

	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", time.Time{}, fmt.Errorf("googlevertex: decoding metadata token: %w", err)
	}
	if body.AccessToken == "" {
		return "", time.Time{}, fmt.Errorf("googlevertex: metadata server returned no token")
	}

	return body.AccessToken, time.Now().Add(time.Duration(body.ExpiresIn) * time.Second), nil
}

// gcloudTokenLifetime is how long a token from the CLI is cached. The CLI does
// not report an expiry, and Google's tokens last an hour.
const gcloudTokenLifetime = 55 * time.Minute

// tokenFromGcloudCLI shells out to gcloud, which covers a developer machine
// with no credentials file written out.
func tokenFromGcloudCLI(ctx context.Context) (string, time.Time, error) {
	if _, err := exec.LookPath("gcloud"); err != nil {
		return "", time.Time{}, fmt.Errorf(
			"googlevertex: no Google Cloud credentials found. Run " +
				"`gcloud auth application-default login`, set " +
				"GOOGLE_APPLICATION_CREDENTIALS, or pass Options.TokenSource",
		)
	}

	out, err := exec.CommandContext(ctx, "gcloud", "auth", "print-access-token").Output()
	if err != nil {
		return "", time.Time{}, fmt.Errorf(
			"googlevertex: `gcloud auth print-access-token` failed: %w "+
				"(try `gcloud auth application-default login`)", err,
		)
	}

	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", time.Time{}, fmt.Errorf("googlevertex: gcloud returned an empty token")
	}
	return token, time.Now().Add(gcloudTokenLifetime), nil
}

// parsePrivateKey decodes a PEM-encoded RSA private key in either PKCS#1 or
// PKCS#8 form; Google issues PKCS#8.
func parsePrivateKey(pemKey string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		return nil, fmt.Errorf("googlevertex: private key is not valid PEM")
	}

	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("googlevertex: private key is %T, want RSA", key)
		}
		return rsaKey, nil
	}

	key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("googlevertex: parsing private key: %w", err)
	}
	return key, nil
}

// signJWT builds an RS256 JSON Web Token.
func signJWT(claims map[string]any, key *rsa.PrivateKey) (string, error) {
	header, err := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT"})
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}

	encode := base64.RawURLEncoding.EncodeToString
	signingInput := encode(header) + "." + encode(payload)

	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("googlevertex: signing assertion: %w", err)
	}

	return signingInput + "." + encode(signature), nil
}

// projectFromCredentials reads the project id out of a credentials file, so a
// caller that has already configured ADC need not repeat it.
func projectFromCredentials() string {
	path := os.Getenv("GOOGLE_APPLICATION_CREDENTIALS")
	if path == "" {
		path = wellKnownCredentialsPath()
	}
	if path == "" {
		return ""
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var creds struct {
		ProjectID      string `json:"project_id"`
		QuotaProjectID string `json:"quota_project_id"`
	}
	if json.Unmarshal(raw, &creds) != nil {
		return ""
	}
	if creds.ProjectID != "" {
		return creds.ProjectID
	}
	return creds.QuotaProjectID
}
