// Package googlevertex implements the pi language model spec against Vertex
// AI, Google Cloud's hosted Gemini endpoint.
//
// Vertex speaks the same generateContent wire format as the Generative
// Language API, so this package reuses providers/google entirely and supplies
// only the two things that differ: Google Cloud credentials, and Vertex's
// resource-path layout.
//
//	model := googlevertex.New(googlevertex.Options{
//	    Project:  "my-project",
//	    Location: "us-central1",
//	}).LanguageModel("gemini-3-pro")
//
// Credentials come from Application Default Credentials, resolved without any
// third-party dependency: GOOGLE_APPLICATION_CREDENTIALS, then the gcloud
// application-default file, then the GCE metadata server, then
// `gcloud auth print-access-token`. Service accounts and gcloud user
// credentials are both supported; workload identity federation is not, and
// needs Options.TokenSource.
//
// Project and Location default to GOOGLE_CLOUD_PROJECT and
// GOOGLE_CLOUD_LOCATION, with the project also read from the credentials file
// when it names one.
package googlevertex

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

const providerID = "google.vertex"

// defaultLocation is used when none is configured. "global" routes to the
// nearest region and is the safest default for a model that may not be
// deployed everywhere.
const defaultLocation = "global"

// Options configures a Provider.
type Options struct {
	// Project is the Google Cloud project id. Defaults to
	// GOOGLE_CLOUD_PROJECT, then the project named in the credentials file.
	Project string

	// Location is the Vertex region, e.g. "us-central1", or "global".
	// Defaults to GOOGLE_CLOUD_LOCATION, then "global".
	Location string

	// TokenSource overrides credential resolution. Supply one for workload
	// identity federation, impersonation, or any flow this package does not
	// implement natively.
	TokenSource TokenSource

	// BaseURL overrides the endpoint, for a private service connect setup.
	BaseURL string

	// Headers are sent with every request.
	Headers provider.Headers

	// HTTPClient overrides the default client. Do not set a Timeout on a
	// client used for streaming; bound calls with a context instead.
	HTTPClient *http.Client

	// Retry overrides the backoff policy.
	Retry providerutil.RetryConfig
}

// Provider creates Vertex AI language models.
type Provider struct {
	inner *google.Provider
}

// New returns a Provider.
//
// Configuration is resolved eagerly enough to fail usefully: a missing project
// is reported on the first call rather than producing a 404 against a
// malformed URL. Credentials are still resolved lazily, on first use.
func New(opts Options) *Provider {
	location := opts.Location
	if location == "" {
		location = providerutil.LoadSetting("", "GOOGLE_CLOUD_LOCATION", "")
	}
	if location == "" {
		location = providerutil.LoadSetting("", "GOOGLE_VERTEX_LOCATION", defaultLocation)
	}

	project := opts.Project
	if project == "" {
		project = providerutil.LoadSetting("", "GOOGLE_CLOUD_PROJECT", "")
	}
	if project == "" {
		project = providerutil.LoadSetting("", "GOOGLE_VERTEX_PROJECT", "")
	}
	if project == "" {
		project = projectFromCredentials()
	}

	baseURL := opts.BaseURL
	if baseURL == "" {
		baseURL = providerutil.LoadSetting("", "GOOGLE_VERTEX_BASE_URL", defaultBaseURL(location))
	}
	baseURL = providerutil.TrimTrailingSlash(baseURL)

	tokenSource := opts.TokenSource
	if tokenSource == nil {
		tokenSource = defaultTokenSource(opts.HTTPClient)
	}

	retry := opts.Retry
	if retry == (providerutil.RetryConfig{}) {
		retry = providerutil.DefaultRetryConfig
	}

	client := &providerutil.Client{
		BaseURL:    baseURL,
		HTTPClient: opts.HTTPClient,
		Headers: func(ctx context.Context) (provider.Headers, error) {
			if project == "" {
				return nil, &provider.LoadAPIKeyError{
					Message: "Vertex AI project is missing: set Options.Project or GOOGLE_CLOUD_PROJECT",
				}
			}
			token, err := tokenSource(ctx)
			if err != nil {
				return nil, err
			}
			headers := provider.Headers{"Authorization": "Bearer " + token}
			for k, v := range opts.Headers {
				headers[k] = v
			}
			return headers, nil
		},
	}

	pathFor := func(modelID, method string, stream bool) string {
		path := fmt.Sprintf(
			"/v1/projects/%s/locations/%s/publishers/google/models/%s:%s",
			project, location, vertexModelID(modelID), method,
		)
		if stream {
			// Without alt=sse the streaming endpoint returns a JSON array
			// rather than server-sent events.
			path += "?alt=sse"
		}
		return path
	}

	return &Provider{inner: google.NewWithTransport(providerID, client, retry, pathFor)}
}

// defaultBaseURL returns the regional endpoint for a location.
func defaultBaseURL(location string) string {
	if location == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return "https://" + location + "-aiplatform.googleapis.com"
}

// vertexModelID strips any "models/" prefix, since Vertex puts the bare id
// after publishers/google/models/.
func vertexModelID(modelID string) string {
	return strings.TrimPrefix(modelID, "models/")
}

// LanguageModel returns a model handle for the given model id.
func (p *Provider) LanguageModel(modelID string) provider.LanguageModel {
	return p.inner.LanguageModel(modelID)
}

// Name returns the provider id.
func (p *Provider) Name() string { return providerID }
