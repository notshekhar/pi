package bedrock

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CredentialSource supplies AWS credentials, refreshing them as needed.
//
// Implement it for anything pigo does not cover — an SSO cache, Vault, a
// role assumed through STS — and pass it as Options.Credentials.
type CredentialSource interface {
	Credentials(ctx context.Context) (Credentials, error)
}

// StaticCredentials is a CredentialSource holding fixed keys.
type StaticCredentials Credentials

// Credentials implements CredentialSource.
func (c StaticCredentials) Credentials(context.Context) (Credentials, error) {
	return Credentials(c), nil
}

// defaultCredentials resolves credentials the way the AWS tools do, in the
// order AWS documents: environment, then the shared credentials file, then the
// container endpoint, then the instance metadata service.
//
// Each source is tried once per refresh and the first complete answer wins.
type defaultCredentials struct {
	profile string
	client  *http.Client

	mu      sync.Mutex
	cached  Credentials
	expires time.Time
}

// Credentials implements CredentialSource.
func (d *defaultCredentials) Credentials(ctx context.Context) (Credentials, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Refresh a minute early: a token that expires mid-request fails the whole
	// call, and a minute of slack costs nothing.
	if d.cached.valid() && (d.expires.IsZero() || time.Now().Add(time.Minute).Before(d.expires)) {
		return d.cached, nil
	}

	if creds, ok := credentialsFromEnv(); ok {
		d.cached, d.expires = creds, time.Time{}
		return creds, nil
	}

	if creds, ok := credentialsFromFile(d.profile); ok {
		d.cached, d.expires = creds, time.Time{}
		return creds, nil
	}

	if creds, expires, ok := d.credentialsFromContainer(ctx); ok {
		d.cached, d.expires = creds, expires
		return creds, nil
	}

	if creds, expires, ok := d.credentialsFromIMDS(ctx); ok {
		d.cached, d.expires = creds, expires
		return creds, nil
	}

	return Credentials{}, fmt.Errorf(
		"bedrock: no AWS credentials found: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY, " +
			"configure ~/.aws/credentials, or run where a container or instance role is available")
}

// credentialsFromEnv reads the standard environment variables.
func credentialsFromEnv() (Credentials, bool) {
	creds := Credentials{
		AccessKeyID:     os.Getenv("AWS_ACCESS_KEY_ID"),
		SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken:    os.Getenv("AWS_SESSION_TOKEN"),
	}
	return creds, creds.valid()
}

// credentialsFromFile reads a profile from the shared credentials file.
func credentialsFromFile(profile string) (Credentials, bool) {
	path := os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return Credentials{}, false
		}
		path = filepath.Join(home, ".aws", "credentials")
	}

	file, err := os.Open(path)
	if err != nil {
		return Credentials{}, false
	}
	defer file.Close()

	if profile == "" {
		profile = os.Getenv("AWS_PROFILE")
	}
	if profile == "" {
		profile = "default"
	}

	var (
		creds     Credentials
		inProfile bool
	)

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			name := strings.TrimSpace(line[1 : len(line)-1])
			// The config file spells non-default sections "profile name"
			// while the credentials file does not; accept both.
			name = strings.TrimPrefix(name, "profile ")
			if inProfile {
				// The next section began, so the profile is fully read.
				break
			}
			inProfile = name == profile
			continue
		}

		if !inProfile {
			continue
		}

		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)

		switch key {
		case "aws_access_key_id":
			creds.AccessKeyID = value
		case "aws_secret_access_key":
			creds.SecretAccessKey = value
		case "aws_session_token":
			creds.SessionToken = value
		}
	}

	return creds, creds.valid()
}

// containerCredentials is the JSON both the container endpoint and IMDS return.
type containerCredentials struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	Token           string `json:"Token"`
	Expiration      string `json:"Expiration"`
}

// toCredentials converts the payload, reporting when it is incomplete.
func (c containerCredentials) toCredentials() (Credentials, time.Time, bool) {
	creds := Credentials{
		AccessKeyID:     c.AccessKeyID,
		SecretAccessKey: c.SecretAccessKey,
		SessionToken:    c.Token,
	}
	if !creds.valid() {
		return Credentials{}, time.Time{}, false
	}

	expires, err := time.Parse(time.RFC3339, c.Expiration)
	if err != nil {
		// No usable expiry: treat it as non-expiring rather than refusing
		// credentials that are otherwise complete.
		expires = time.Time{}
	}
	return creds, expires, true
}

// credentialsFromContainer reads the ECS/EKS task role endpoint.
func (d *defaultCredentials) credentialsFromContainer(ctx context.Context) (Credentials, time.Time, bool) {
	uri := os.Getenv("AWS_CONTAINER_CREDENTIALS_FULL_URI")
	if uri == "" {
		if relative := os.Getenv("AWS_CONTAINER_CREDENTIALS_RELATIVE_URI"); relative != "" {
			uri = "http://169.254.170.2" + relative
		}
	}
	if uri == "" {
		return Credentials{}, time.Time{}, false
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, uri, nil)
	if err != nil {
		return Credentials{}, time.Time{}, false
	}
	if token := os.Getenv("AWS_CONTAINER_AUTHORIZATION_TOKEN"); token != "" {
		req.Header.Set("Authorization", token)
	}

	var payload containerCredentials
	if !d.fetchJSON(req, &payload) {
		return Credentials{}, time.Time{}, false
	}
	return payload.toCredentials()
}

// imdsBaseURL is the link-local address of the instance metadata service.
// It is a variable so tests can point it at a local server.
var imdsBaseURL = "http://169.254.169.254"

// credentialsFromIMDS reads an EC2 instance role, using IMDSv2.
func (d *defaultCredentials) credentialsFromIMDS(ctx context.Context) (Credentials, time.Time, bool) {
	if os.Getenv("AWS_EC2_METADATA_DISABLED") == "true" {
		return Credentials{}, time.Time{}, false
	}

	// IMDSv2 requires a session token obtained with a PUT. Instances
	// configured to require it reject an unauthenticated GET outright.
	tokenReq, err := http.NewRequestWithContext(ctx, http.MethodPut, imdsBaseURL+"/latest/api/token", nil)
	if err != nil {
		return Credentials{}, time.Time{}, false
	}
	tokenReq.Header.Set("x-aws-ec2-metadata-token-ttl-seconds", "21600")

	token, ok := d.fetchString(tokenReq)
	if !ok {
		return Credentials{}, time.Time{}, false
	}

	roleReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		imdsBaseURL+"/latest/meta-data/iam/security-credentials/", nil)
	if err != nil {
		return Credentials{}, time.Time{}, false
	}
	roleReq.Header.Set("x-aws-ec2-metadata-token", token)

	role, ok := d.fetchString(roleReq)
	if !ok || role == "" {
		return Credentials{}, time.Time{}, false
	}
	// The listing may carry several roles, one per line; the first is the one.
	role, _, _ = strings.Cut(strings.TrimSpace(role), "\n")

	credsReq, err := http.NewRequestWithContext(ctx, http.MethodGet,
		imdsBaseURL+"/latest/meta-data/iam/security-credentials/"+role, nil)
	if err != nil {
		return Credentials{}, time.Time{}, false
	}
	credsReq.Header.Set("x-aws-ec2-metadata-token", token)

	var payload containerCredentials
	if !d.fetchJSON(credsReq, &payload) {
		return Credentials{}, time.Time{}, false
	}
	return payload.toCredentials()
}

// maxMetadataBytes caps a metadata response, which is a small JSON document.
const maxMetadataBytes = 1 << 20

// metadataTimeout bounds a metadata lookup. The endpoints are link-local, so a
// slow answer means they are not there and the next source should be tried.
const metadataTimeout = 2 * time.Second

// fetchString performs a metadata request returning plain text.
func (d *defaultCredentials) fetchString(req *http.Request) (string, bool) {
	ctx, cancel := context.WithTimeout(req.Context(), metadataTimeout)
	defer cancel()

	resp, err := d.httpClient().Do(req.WithContext(ctx))
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return "", false
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadataBytes))
	if err != nil {
		return "", false
	}
	return string(body), true
}

// fetchJSON performs a metadata request and decodes the reply.
func (d *defaultCredentials) fetchJSON(req *http.Request, out any) bool {
	body, ok := d.fetchString(req)
	if !ok {
		return false
	}
	return json.Unmarshal([]byte(body), out) == nil
}

// httpClient returns the client used for metadata lookups.
func (d *defaultCredentials) httpClient() *http.Client {
	if d.client != nil {
		return d.client
	}
	return http.DefaultClient
}
