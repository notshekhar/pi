// Package bedrock implements the pi model specs against Amazon Bedrock's
// Converse API.
//
// Converse is the vendor-neutral shape: the same request drives Claude, Llama,
// Mistral and Nova, so this package is one implementation rather than one per
// model family. Per-model settings that Converse has no field for — Anthropic's
// extended thinking, for instance — ride in additionalModelRequestFields.
//
//	model := bedrock.New(bedrock.Options{Region: "us-east-1"}).
//	    LanguageModel("us.anthropic.claude-sonnet-4-5-20250929-v1:0")
//
// Credentials are resolved the way the AWS tools do — environment, then
// ~/.aws/credentials, then the container endpoint, then instance metadata —
// with no third-party dependency: SigV4 signing and the binary event-stream
// decoder are both implemented here on the standard library.
//
// Supported: streaming and non-streaming text, tool calling, extended
// thinking, prompt caching, images and documents, and Titan/Cohere embeddings.
//
// Not supported: provider-hosted tools and image generation, both of which
// Bedrock exposes through separate per-model APIs rather than Converse.
package bedrock

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// providerID is the key under which this provider reads and writes
// provider-specific options and metadata.
const providerID = "bedrock"

// serviceName is what SigV4 signs Bedrock requests as.
const serviceName = "bedrock"

// defaultRegion is used when nothing in the environment names one.
const defaultRegion = "us-east-1"

// Options configures a Provider. The zero value works wherever the AWS tools
// would: with credentials in the environment, a configured profile, or an
// instance role.
type Options struct {
	// Region is the AWS region. Empty reads AWS_REGION, then AWS_DEFAULT_REGION,
	// then falls back to us-east-1.
	Region string

	// Profile names a profile in the shared credentials file. Empty reads
	// AWS_PROFILE, then uses "default".
	Profile string

	// Credentials overrides credential resolution entirely. Use
	// StaticCredentials for fixed keys, or implement CredentialSource for
	// anything else.
	Credentials CredentialSource

	// BaseURL overrides the endpoint, for a VPC endpoint or a proxy. Empty
	// builds the regional endpoint.
	BaseURL string

	// Headers are sent with every request. They are signed along with the
	// rest, so a header added here reaches Bedrock intact.
	Headers provider.Headers

	// HTTPClient overrides the default client. Do not set a Timeout on a
	// client used for streaming; bound calls with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy.
	Retry providerutil.RetryConfig
}

// Provider creates Bedrock models.
type Provider struct {
	region  string
	baseURL string
	headers provider.Headers
	creds   CredentialSource
	client  *http.Client
	retry   providerutil.RetryConfig
	signer  signer
}

// New returns a Provider.
//
// Credentials are resolved lazily on the first request, so constructing a
// provider never fails and a missing credential surfaces as a call error
// saying what to configure.
func New(opts Options) *Provider {
	region := opts.Region
	if region == "" {
		region = providerutil.LoadSetting("", "AWS_REGION", "")
	}
	if region == "" {
		region = providerutil.LoadSetting("", "AWS_DEFAULT_REGION", defaultRegion)
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("https://bedrock-runtime.%s.amazonaws.com", region)
	}
	baseURL = providerutil.TrimTrailingSlash(baseURL)

	creds := opts.Credentials
	if creds == nil {
		creds = &defaultCredentials{profile: opts.Profile, client: opts.HTTPClient}
	}

	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}

	return &Provider{
		region:  region,
		baseURL: baseURL,
		headers: opts.Headers,
		creds:   creds,
		client:  opts.HTTPClient,
		retry:   retry,
		signer:  signer{region: region, service: serviceName},
	}
}

// Name returns the provider id.
func (p *Provider) Name() string { return providerID }

// Region returns the region requests are sent to.
func (p *Provider) Region() string { return p.region }

// LanguageModel returns a model handle for a Bedrock model id.
//
// Ids are either bare ("anthropic.claude-sonnet-4-5-20250929-v1:0") or
// prefixed with a cross-region inference profile ("us.", "eu.", "apac."),
// which is what most accounts have to use for the newer Claude models.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return &languageModel{modelID: modelID, provider: p, caps: modelCapabilities(modelID)}
}

// EmbeddingModel returns an embedding model handle.
//
// Titan takes one value per call; Cohere takes up to 96. EmbedMany batches
// accordingly.
//
//	model := bedrock.New(bedrock.Options{}).EmbeddingModel("amazon.titan-embed-text-v2:0")
func (p *Provider) EmbeddingModel(modelID string) provider.EmbeddingModel {
	return &embeddingModel{modelID: modelID, provider: p}
}

// modelPath builds /model/{id}/{action}, encoding characters Converse rejects
// unescaped — model ids contain colons ("…v1:0") and inference-profile
// prefixes are dotted, not slashed, but a custom id can still carry a slash.
func modelPath(modelID, action string) string {
	return "/model/" + escapePathSegment(modelID) + "/" + action
}

// httpClient returns the client to use.
func (p *Provider) httpClient() *http.Client {
	if p.client != nil {
		return p.client
	}
	return http.DefaultClient
}

// post sends a signed request and returns the raw response.
//
// The body is signed as bytes rather than streamed, because SigV4 hashes the
// payload and so needs all of it before the first byte goes out.
func (p *Provider) post(ctx context.Context, path string, body []byte, extra provider.Headers) (*http.Response, error) {
	creds, err := p.creds.Credentials(ctx)
	if err != nil {
		return nil, &provider.LoadAPIKeyError{Message: err.Error()}
	}

	url := p.baseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("bedrock: building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	accept := "application/json"
	if extra != nil {
		if v := extra["Accept"]; v != "" {
			accept = v
		}
	}
	req.Header.Set("Accept", accept)

	for k, v := range p.headers {
		req.Header.Set(k, v)
	}
	for k, v := range extra {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}

	if err := p.signer.sign(req, body, creds, timeNow()); err != nil {
		return nil, err
	}

	resp, err := p.httpClient().Do(req)
	if err != nil {
		return nil, &providerutil.APICallError{
			Message:     "request failed",
			URL:         url,
			RequestBody: string(body),
			IsRetryable: true,
			Cause:       err,
		}
	}

	if resp.StatusCode >= 300 {
		return nil, providerutil.ErrorFromResponse(resp, url, string(body))
	}
	return resp, nil
}

// timeNow is a variable so signing can be tested against a fixed clock.
var timeNow = defaultNow

// defaultNow reports the current time, honouring a fake clock in tests.
func defaultNow() time.Time { return time.Now() }
