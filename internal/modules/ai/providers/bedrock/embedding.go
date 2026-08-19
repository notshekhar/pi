package bedrock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// embeddingModel implements provider.EmbeddingModel over InvokeModel.
//
// Titan, Cohere and Nova each have their own request and response shapes, so
// the public interface stays stable and the adaptation happens here.
type embeddingModel struct {
	modelID  string
	provider *Provider
}

func (m *embeddingModel) SpecificationVersion() string { return provider.EmbeddingSpecificationVersion }
func (m *embeddingModel) Provider() string             { return providerID }
func (m *embeddingModel) ModelID() string              { return m.modelID }

// MaxEmbeddingsPerCall implements provider.EmbeddingModel.
//
// Cohere accepts a batch; Titan and Nova take one value per call.
func (m *embeddingModel) MaxEmbeddingsPerCall() int {
	if isCohereEmbedding(m.modelID) {
		return 96
	}
	return 1
}

func (m *embeddingModel) SupportsParallelCalls() bool { return true }

// DoEmbed implements provider.EmbeddingModel.
func (m *embeddingModel) DoEmbed(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	if len(opts.Values) == 0 {
		return &provider.EmbeddingResult{}, nil
	}

	limit := m.MaxEmbeddingsPerCall()
	if len(opts.Values) > limit {
		return nil, fmt.Errorf("bedrock: %d values exceeds the %d per call limit for %s",
			len(opts.Values), limit, m.modelID)
	}

	body := m.requestBody(opts)
	path := modelPath(m.modelID, "invoke")

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*embeddingResponse, error) {
		httpResp, err := m.provider.postJSONValue(ctx, path, body, opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded embeddingResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		if n := httpResp.Header.Get("X-Amzn-Bedrock-Input-Token-Count"); n != "" {
			if tokens, ok := toInt64(n); ok {
				decoded.headerTokens = tokens
			}
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	embeddings, err := resp.vectors()
	if err != nil {
		return nil, err
	}

	usage := provider.EmbeddingUsage{}
	if tokens := resp.tokenCount(); tokens != nil {
		usage.Tokens = tokens
	}

	return &provider.EmbeddingResult{
		Embeddings: embeddings,
		Usage:      usage,
	}, nil
}

// requestBody picks the payload the model family expects.
func (m *embeddingModel) requestBody(opts provider.EmbeddingCallOptions) any {
	block := optionBlock(opts.ProviderOptions)

	switch {
	case isNovaEmbedding(m.modelID):
		purpose := "GENERIC_INDEX"
		if v, ok := stringOpt(block, "embeddingPurpose"); ok {
			purpose = v
		}
		dims := 1024
		if opts.Dimensions > 0 {
			dims = opts.Dimensions
		} else if n, ok := toInt64(blockValue(block, "embeddingDimension")); ok {
			dims = int(n)
		}
		truncate := "END"
		if v, ok := stringOpt(block, "truncate"); ok {
			truncate = v
		}
		return map[string]any{
			"taskType": "SINGLE_EMBEDDING",
			"singleEmbeddingParams": map[string]any{
				"embeddingPurpose":   purpose,
				"embeddingDimension": dims,
				"text": map[string]any{
					"truncationMode": truncate,
					"value":          opts.Values[0],
				},
			},
		}

	case isCohereEmbedding(m.modelID):
		inputType := "search_query"
		if v, ok := stringOpt(block, "inputType"); ok {
			inputType = v
		}
		req := map[string]any{
			"input_type": inputType,
			"texts":      opts.Values,
		}
		if v, ok := stringOpt(block, "truncate"); ok {
			req["truncate"] = v
		}
		if opts.Dimensions > 0 {
			req["output_dimension"] = opts.Dimensions
		} else if n, ok := toInt64(blockValue(block, "outputDimension")); ok {
			req["output_dimension"] = n
		}
		return req

	default:
		// Titan.
		req := map[string]any{"inputText": opts.Values[0]}
		if opts.Dimensions > 0 {
			req["dimensions"] = opts.Dimensions
		} else if n, ok := toInt64(blockValue(block, "dimensions")); ok {
			req["dimensions"] = n
		}
		if block != nil {
			if v, ok := block["normalize"].(bool); ok {
				req["normalize"] = v
			}
		}
		return req
	}
}

// embeddingResponse covers Titan, Cohere and Nova reply shapes.
type embeddingResponse struct {
	// Titan
	Embedding           []float32 `json:"embedding"`
	InputTextTokenCount *int64    `json:"inputTextTokenCount"`

	// Cohere v3: [][]float; Cohere v4: {float: [][]float}; Nova: [{embedding}]
	Embeddings json.RawMessage `json:"embeddings"`

	// Nova
	InputTokenCount *int64 `json:"inputTokenCount"`

	headerTokens int64
}

// vectors extracts embeddings in input order.
func (r *embeddingResponse) vectors() ([]provider.Embedding, error) {
	if len(r.Embedding) > 0 {
		return []provider.Embedding{provider.Embedding(r.Embedding)}, nil
	}
	if len(r.Embeddings) == 0 {
		return nil, &provider.InvalidResponseError{Message: "bedrock embedding response had no vectors"}
	}

	// Cohere v3: [[...], [...]]
	var asArrays [][]float32
	if json.Unmarshal(r.Embeddings, &asArrays) == nil && len(asArrays) > 0 {
		out := make([]provider.Embedding, len(asArrays))
		for i, v := range asArrays {
			out[i] = provider.Embedding(v)
		}
		return out, nil
	}

	// Cohere v4: {"float": [[...]]}
	var asObject struct {
		Float [][]float32 `json:"float"`
	}
	if json.Unmarshal(r.Embeddings, &asObject) == nil && len(asObject.Float) > 0 {
		out := make([]provider.Embedding, len(asObject.Float))
		for i, v := range asObject.Float {
			out[i] = provider.Embedding(v)
		}
		return out, nil
	}

	// Nova: [{"embeddingType":"...","embedding":[...]}]
	var asNova []struct {
		Embedding []float32 `json:"embedding"`
	}
	if json.Unmarshal(r.Embeddings, &asNova) == nil && len(asNova) > 0 {
		out := make([]provider.Embedding, 0, len(asNova))
		for _, v := range asNova {
			out = append(out, provider.Embedding(v.Embedding))
		}
		return out, nil
	}

	return nil, &provider.InvalidResponseError{
		Message:  "unrecognised bedrock embedding response",
		Response: string(r.Embeddings),
	}
}

// tokenCount prefers the body figure, then the response header.
func (r *embeddingResponse) tokenCount() *int64 {
	if r.InputTextTokenCount != nil {
		return r.InputTextTokenCount
	}
	if r.InputTokenCount != nil {
		return r.InputTokenCount
	}
	if r.headerTokens > 0 {
		return &r.headerTokens
	}
	return nil
}

func isCohereEmbedding(modelID string) bool {
	return strings.Contains(strings.ToLower(modelID), "cohere.embed-")
}

func isNovaEmbedding(modelID string) bool {
	id := strings.ToLower(modelID)
	return strings.Contains(id, "amazon.nova-") && strings.Contains(id, "embed")
}

func blockValue(block provider.JSONObject, key string) any {
	if block == nil {
		return nil
	}
	return block[key]
}

// postJSONValue is Provider.post for an already-built value.
func (p *Provider) postJSONValue(ctx context.Context, path string, body any, extra provider.Headers) (*http.Response, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("bedrock: encoding request body: %w", err)
	}
	return p.post(ctx, path, payload, extra)
}
