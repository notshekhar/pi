package config

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Asking a custom endpoint what it serves, instead of asking the user.
//
// Most gateways — bifrost, litellm, vLLM, Ollama, anything OpenAI-shaped —
// answer `/models` with the list they proxy, and it is a better list than a
// person will type: complete, correctly spelled, and often carrying the
// context window too. Typing model ids by hand is the FALLBACK, for the
// endpoints that cannot answer.
//
// Failure is a nil result, never an error the wizard has to explain. A 404, a
// timeout, an HTML error page, an auth rejection — from the user's point of
// view these are all the same fact ("this endpoint cannot list its models")
// and all lead to the same next step, so distinguishing them would only add
// words to a prompt that already asks the right question.

// discoverTimeout bounds the probe. A gateway that has not answered in ten
// seconds is one the user should not be kept waiting on mid-wizard.
const discoverTimeout = 10 * time.Second

// DiscoverModels asks the endpoint for its model list, or returns nil.
func DiscoverModels(ctx context.Context, p CustomProvider) []CustomModel {
	ctx, cancel := context.WithTimeout(ctx, discoverTimeout)
	defer cancel()

	base := strings.TrimRight(p.BaseURL, "/")
	endpoint := base + "/models"
	headers := map[string]string{}
	for k, v := range p.Headers {
		headers[k] = v
	}
	key := p.CustomKey()

	switch strings.ToLower(p.SDK) {
	case "anthropic":
		// The version header is required, and a gateway fronting Anthropic
		// rejects the request without it.
		setIfAbsent(headers, "anthropic-version", "2023-06-01")
		if key != "" {
			setIfAbsent(headers, "x-api-key", key)
		}
		return decodeAnthropicModels(get(ctx, endpoint, headers))
	case "google", "gemini":
		if key != "" {
			// Both, because gateways differ on which they read: Google's own
			// API takes the query parameter, and proxies usually take the
			// header.
			setIfAbsent(headers, "x-goog-api-key", key)
			endpoint += "?key=" + url.QueryEscape(key)
		}
		return decodeGoogleModels(get(ctx, endpoint, headers))
	default:
		if key != "" {
			setIfAbsent(headers, "Authorization", "Bearer "+key)
		}
		return decodeOpenAIModels(get(ctx, endpoint, headers))
	}
}

// setIfAbsent leaves a header the user set alone.
//
// Case-insensitively: a user who wrote `Authorization` must not then get a
// second `authorization` from us, because which one the server honours is
// then anybody's guess.
func setIfAbsent(headers map[string]string, name, value string) {
	for k := range headers {
		if strings.EqualFold(k, name) {
			return
		}
	}
	headers[name] = value
}

// get fetches a URL and returns the body, or nil on any failure at all.
func get(ctx context.Context, endpoint string, headers map[string]string) []byte {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	// Bounded: an endpoint that answers `/models` with a gigabyte is not one
	// to hand to json.Unmarshal.
	body := make([]byte, 0, 64*1024)
	buf := make([]byte, 32*1024)
	for len(body) < 4<<20 {
		n, err := resp.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	return body
}

func decodeOpenAIModels(body []byte) []CustomModel {
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	out := make([]CustomModel, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID != "" {
			out = append(out, CustomModel{ID: m.ID})
		}
	}
	return nonEmpty(out)
}

func decodeAnthropicModels(body []byte) []CustomModel {
	var doc struct {
		Data []struct {
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			MaxInput    int    `json:"max_input_tokens"`
			MaxTokens   int    `json:"max_tokens"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	out := make([]CustomModel, 0, len(doc.Data))
	for _, m := range doc.Data {
		if m.ID == "" {
			continue
		}
		out = append(out, CustomModel{
			ID: m.ID, Name: m.DisplayName,
			Context: m.MaxInput, MaxOutput: m.MaxTokens,
		})
	}
	return nonEmpty(out)
}

func decodeGoogleModels(body []byte) []CustomModel {
	var doc struct {
		Models []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			InputLimit  int    `json:"inputTokenLimit"`
			OutputLimit int    `json:"outputTokenLimit"`
		} `json:"models"`
	}
	if json.Unmarshal(body, &doc) != nil {
		return nil
	}
	out := make([]CustomModel, 0, len(doc.Models))
	for _, m := range doc.Models {
		// Google returns `models/gemini-2.5-pro`; the id is the last segment.
		id := strings.TrimPrefix(m.Name, "models/")
		if id == "" {
			continue
		}
		out = append(out, CustomModel{
			ID: id, Name: m.DisplayName,
			Context: m.InputLimit, MaxOutput: m.OutputLimit,
		})
	}
	return nonEmpty(out)
}

// nonEmpty turns an empty result into nil, so "answered with no models" and
// "could not answer" reach the caller as the same thing — because they lead
// to the same next step.
func nonEmpty(models []CustomModel) []CustomModel {
	if len(models) == 0 {
		return nil
	}
	return models
}
