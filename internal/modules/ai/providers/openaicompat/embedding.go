package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// defaultEmbeddingsPath is the endpoint every chat-completions gateway serves
// embeddings on.
const defaultEmbeddingsPath = "/embeddings"

// maxEmbeddingsPerCall is OpenAI's documented ceiling on inputs per request,
// which the compatible gateways match or exceed.
const maxEmbeddingsPerCall = 2048

// EmbeddingModel returns an embedding model handle.
//
//	client := openaicompat.New(openaicompat.Options{Name: "openai"})
//	model := client.EmbeddingModel("text-embedding-3-small")
func (p *Provider) EmbeddingModel(modelID string) provider.EmbeddingModel {
	return &embeddingModel{modelID: modelID, provider: p}
}

// embeddingModel implements provider.EmbeddingModel over /embeddings.
type embeddingModel struct {
	modelID  string
	provider *Provider
}

func (m *embeddingModel) SpecificationVersion() string { return provider.EmbeddingSpecificationVersion }
func (m *embeddingModel) Provider() string             { return m.provider.name }
func (m *embeddingModel) ModelID() string              { return m.modelID }
func (m *embeddingModel) MaxEmbeddingsPerCall() int    { return maxEmbeddingsPerCall }
func (m *embeddingModel) SupportsParallelCalls() bool  { return true }

// embeddingRequest is the /embeddings body.
type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
	// Dimensions truncates the vector on models that support it.
	Dimensions *int `json:"dimensions,omitempty"`
	// EncodingFormat is left at the default "float": base64 would be smaller
	// on the wire but needs a decode step for no real gain here.
	EncodingFormat string `json:"encoding_format,omitempty"`

	// Extra carries provider-specific fields, merged at the top level.
	Extra provider.JSONObject `json:"-"`
}

// embeddingResponse is the /embeddings reply.
type embeddingResponse struct {
	Data []struct {
		// Index is authoritative: the API does not promise the array is in
		// input order, and a reordered batch would misalign every vector.
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`

	Usage *struct {
		PromptTokens int64 `json:"prompt_tokens"`
		TotalTokens  int64 `json:"total_tokens"`
	} `json:"usage"`
}

// DoEmbed implements provider.EmbeddingModel.
func (m *embeddingModel) DoEmbed(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	if len(opts.Values) == 0 {
		return &provider.EmbeddingResult{}, nil
	}
	if len(opts.Values) > maxEmbeddingsPerCall {
		return nil, fmt.Errorf("openaicompat: %d values exceeds the %d per call limit",
			len(opts.Values), maxEmbeddingsPerCall)
	}

	req := &embeddingRequest{Model: m.modelID, Input: opts.Values}
	if opts.Dimensions > 0 {
		req.Dimensions = &opts.Dimensions
	}
	if extra := opts.ProviderOptions.Get(m.provider.optionsKey); extra != nil {
		req.Extra = extra
	}

	body, err := marshalEmbeddingRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*embeddingResponse, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, defaultEmbeddingsPath, json.RawMessage(body), opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded embeddingResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	// Sort by the reported index rather than trusting arrival order.
	sort.SliceStable(resp.Data, func(i, j int) bool { return resp.Data[i].Index < resp.Data[j].Index })

	embeddings := make([]provider.Embedding, 0, len(resp.Data))
	for _, d := range resp.Data {
		embeddings = append(embeddings, provider.Embedding(d.Embedding))
	}

	result := &provider.EmbeddingResult{Embeddings: embeddings}
	if resp.Usage != nil {
		tokens := resp.Usage.PromptTokens
		if tokens == 0 {
			tokens = resp.Usage.TotalTokens
		}
		result.Usage.Tokens = &tokens
	}
	return result, nil
}

// marshalEmbeddingRequest renders the body, merging provider extras as
// siblings of the standard fields.
func marshalEmbeddingRequest(req *embeddingRequest) ([]byte, error) {
	encoded, err := json.Marshal(*req)
	if err != nil {
		return nil, err
	}
	if len(req.Extra) == 0 {
		return encoded, nil
	}

	var merged map[string]any
	if err := json.Unmarshal(encoded, &merged); err != nil {
		return nil, err
	}
	for k, v := range req.Extra {
		merged[k] = v
	}
	return json.Marshal(merged)
}
