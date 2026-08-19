// Package providerutil holds the machinery every provider implementation
// needs: JSON-over-HTTP calls, server-sent event parsing, retries, and
// credential loading.
//
// It is not part of the public API surface of package ai. Application code
// should not need it; provider authors should not need to reimplement it.
package providerutil

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// maxErrorBodyBytes caps how much of an error response is read. Provider
// errors are small; an unbounded read here would let a misbehaving endpoint
// exhaust memory on a failure path.
const maxErrorBodyBytes = 1 << 20 // 1 MiB

// Client performs JSON HTTP calls against one provider API.
type Client struct {
	// BaseURL is the API root, without a trailing slash.
	BaseURL string

	// Headers returns the headers sent with every request. It is a function
	// so that rotating credentials are resolved per call rather than captured
	// once at construction.
	Headers func(ctx context.Context) (provider.Headers, error)

	// HTTPClient defaults to http.DefaultClient.
	//
	// Streaming calls need a client with no overall timeout: a Timeout on
	// http.Client covers reading the body too, so it cuts long generations
	// off mid-stream. Bound the setup phase with a context instead.
	HTTPClient *http.Client
}

// httpClient returns the configured client or the default.
func (c *Client) httpClient() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return http.DefaultClient
}

// PostJSON sends body as JSON to path and returns the raw response.
//
// A non-2xx status is converted to an *provider.APICallError and the body is
// closed. On success the caller owns resp.Body and must close it.
func (c *Client) PostJSON(
	ctx context.Context,
	path string,
	body any,
	extraHeaders provider.Headers,
) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request body: %w", err)
	}

	url := c.BaseURL + path

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	if err := c.applyHeaders(ctx, req, extraHeaders); err != nil {
		return nil, err
	}

	resp, err := c.httpClient().Do(req)
	if err != nil {
		// The request never completed, so there is no status to judge
		// retryability by. Treat transport failures as retryable: the common
		// causes (connection reset, DNS blip, TLS handshake timeout) are
		// transient, and context cancellation is filtered out by Retry.
		return nil, &APICallError{
			Message:     "request failed",
			URL:         url,
			RequestBody: string(payload),
			IsRetryable: true,
			Cause:       err,
		}
	}

	if resp.StatusCode >= 300 {
		return nil, c.errorFromResponse(resp, url, string(payload))
	}

	return resp, nil
}

// applyHeaders merges the client's headers and per-call overrides onto a
// request. An empty value removes a header, which is how a caller suppresses
// a default the client would otherwise send.
func (c *Client) applyHeaders(ctx context.Context, req *http.Request, extra provider.Headers) error {
	if c.Headers != nil {
		base, err := c.Headers(ctx)
		if err != nil {
			return err
		}
		for k, v := range base {
			if v != "" {
				req.Header.Set(k, v)
			}
		}
	}
	for k, v := range extra {
		if v == "" {
			req.Header.Del(k)
			continue
		}
		req.Header.Set(k, v)
	}
	return nil
}

// errorFromResponse converts a non-2xx response into an APICallError and
// closes the body.
func (c *Client) errorFromResponse(resp *http.Response, url, requestBody string) error {
	return ErrorFromResponse(resp, url, requestBody)
}

// ErrorFromResponse converts a non-2xx response into an *provider.APICallError
// and closes the body.
//
// It is exported for providers that cannot use Client — Bedrock signs its own
// requests — so that every provider reports failures the same way.
func ErrorFromResponse(resp *http.Response, url, requestBody string) error {
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	text := string(raw)

	apiErr := &APICallError{
		Message:         extractErrorMessage(text, resp.Status),
		URL:             url,
		RequestBody:     requestBody,
		StatusCode:      resp.StatusCode,
		ResponseHeaders: FlattenHeaders(resp.Header),
		ResponseBody:    text,
		IsRetryable:     provider.IsRetryableStatus(resp.StatusCode),
	}

	// Keep the structured payload when there is one: provider error bodies
	// carry codes and retry hints that the flat message loses.
	var data any
	if json.Unmarshal(raw, &data) == nil {
		apiErr.Data = data
	}

	return apiErr
}

// extractErrorMessage digs the human-readable message out of a provider error
// body, falling back to the HTTP status. Providers agree on neither the
// wrapper key nor the field, so several shapes are tried:
//
//	{"error": {"message": "..."}}   Anthropic, OpenAI
//	{"error": "..."}                several OpenAI-compatible gateways
//	{"message": "..."}              Cohere and others
func extractErrorMessage(body, status string) string {
	var envelope struct {
		Error   json.RawMessage `json:"error"`
		Message string          `json:"message"`
		Detail  string          `json:"detail"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		return fallbackMessage(body, status)
	}

	if len(envelope.Error) > 0 {
		var nested struct {
			Message string `json:"message"`
		}
		if json.Unmarshal(envelope.Error, &nested) == nil && nested.Message != "" {
			return nested.Message
		}
		var plain string
		if json.Unmarshal(envelope.Error, &plain) == nil && plain != "" {
			return plain
		}
	}
	if envelope.Message != "" {
		return envelope.Message
	}
	if envelope.Detail != "" {
		return envelope.Detail
	}

	return fallbackMessage(body, status)
}

// fallbackMessage uses a short raw body if there is one, else the status line.
func fallbackMessage(body, status string) string {
	body = strings.TrimSpace(body)
	if body != "" && len(body) <= 200 {
		return body
	}
	return status
}

// DecodeJSON reads and closes a response body into v.
func DecodeJSON(resp *http.Response, v any) error {
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("reading response body: %w", err)
	}
	if err := json.Unmarshal(raw, v); err != nil {
		return &provider.InvalidResponseError{
			Message:  "could not decode provider response",
			Response: string(raw),
			Cause:    err,
		}
	}
	return nil
}

// FlattenHeaders converts http.Header to the flat map the spec uses, keeping
// the first value of any repeated header.
func FlattenHeaders(h http.Header) provider.Headers {
	if len(h) == 0 {
		return nil
	}
	out := make(provider.Headers, len(h))
	for k, v := range h {
		if len(v) > 0 {
			out[strings.ToLower(k)] = v[0]
		}
	}
	return out
}

// APICallError is provider.APICallError, re-exported so provider packages
// need only one import for error construction.
type APICallError = provider.APICallError
