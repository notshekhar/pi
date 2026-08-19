package google

import (
	"context"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// maxEmbeddingsPerCall is Google's documented batch ceiling.
const maxEmbeddingsPerCall = 100

// TaskType tells the model what the embedding is for. Google trains asymmetric
// embeddings, so a document indexed as RETRIEVAL_DOCUMENT and a query embedded
// as RETRIEVAL_QUERY match better than either would against itself.
type TaskType string

// Task types accepted by the embedding models.
const (
	TaskRetrievalQuery     TaskType = "RETRIEVAL_QUERY"
	TaskRetrievalDocument  TaskType = "RETRIEVAL_DOCUMENT"
	TaskSemanticSimilarity TaskType = "SEMANTIC_SIMILARITY"
	TaskClassification     TaskType = "CLASSIFICATION"
	TaskClustering         TaskType = "CLUSTERING"
	TaskQuestionAnswering  TaskType = "QUESTION_ANSWERING"
	TaskFactVerification   TaskType = "FACT_VERIFICATION"
	TaskCodeRetrievalQuery TaskType = "CODE_RETRIEVAL_QUERY"
)

// EmbeddingModel returns an embedding model handle.
//
//	model := google.New(google.Options{}).EmbeddingModel("gemini-embedding-001")
//
// Set the task type through provider options, which is worth doing for search:
//
//	ai.EmbedMany(ctx, docs, ai.EmbedOptions{
//	    Model: model,
//	    ProviderOptions: provider.ProviderOptions{
//	        "google": {"taskType": string(google.TaskRetrievalDocument)},
//	    },
//	})
func (p *Provider) EmbeddingModel(modelID string) provider.EmbeddingModel {
	return &embeddingModel{modelID: modelID, provider: p}
}

// embeddingModel implements provider.EmbeddingModel over batchEmbedContents.
type embeddingModel struct {
	modelID  string
	provider *Provider
}

func (m *embeddingModel) SpecificationVersion() string { return provider.EmbeddingSpecificationVersion }
func (m *embeddingModel) Provider() string             { return providerID }
func (m *embeddingModel) ModelID() string              { return m.modelID }
func (m *embeddingModel) MaxEmbeddingsPerCall() int    { return maxEmbeddingsPerCall }
func (m *embeddingModel) SupportsParallelCalls() bool  { return true }

// batchEmbedRequest is the batchEmbedContents body. Google takes a list of
// complete requests rather than a list of strings, so the model id is repeated
// on every entry.
type batchEmbedRequest struct {
	Requests []embedContentRequest `json:"requests"`
}

// embedContentRequest is one value to embed.
type embedContentRequest struct {
	// Model must be the full resource path, even though the same id is already
	// in the URL.
	Model                string  `json:"model"`
	Content              content `json:"content"`
	TaskType             string  `json:"taskType,omitempty"`
	Title                string  `json:"title,omitempty"`
	OutputDimensionality *int    `json:"outputDimensionality,omitempty"`
}

// batchEmbedResponse is the reply. Order matches the requests.
type batchEmbedResponse struct {
	Embeddings []struct {
		Values []float32 `json:"values"`
	} `json:"embeddings"`
}

// DoEmbed implements provider.EmbeddingModel.
func (m *embeddingModel) DoEmbed(ctx context.Context, opts provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	if len(opts.Values) == 0 {
		return &provider.EmbeddingResult{}, nil
	}
	if len(opts.Values) > maxEmbeddingsPerCall {
		return nil, fmt.Errorf("google: %d values exceeds the %d per call limit",
			len(opts.Values), maxEmbeddingsPerCall)
	}

	var (
		taskType string
		title    string
	)
	if block := opts.ProviderOptions.Get(providerID); block != nil {
		taskType, _ = block["taskType"].(string)
		title, _ = block["title"].(string)
	}

	req := &batchEmbedRequest{Requests: make([]embedContentRequest, 0, len(opts.Values))}
	for _, value := range opts.Values {
		entry := embedContentRequest{
			Model:    modelPath(m.modelID),
			Content:  content{Parts: []part{{Text: value}}},
			TaskType: taskType,
			Title:    title,
		}
		if opts.Dimensions > 0 {
			dims := opts.Dimensions
			entry.OutputDimensionality = &dims
		}
		req.Requests = append(req.Requests, entry)
	}

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*batchEmbedResponse, error) {
		httpResp, err := m.provider.client.PostJSON(
			ctx, m.provider.path(m.modelID, "batchEmbedContents", false), req, opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded batchEmbedResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	embeddings := make([]provider.Embedding, 0, len(resp.Embeddings))
	for _, e := range resp.Embeddings {
		embeddings = append(embeddings, provider.Embedding(e.Values))
	}

	// Google reports no token usage for embeddings, and a nil count says
	// "not reported" rather than claiming zero.
	return &provider.EmbeddingResult{Embeddings: embeddings}, nil
}
