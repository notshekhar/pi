package bedrock

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

func TestEmbedTitan(t *testing.T) {
	p, sent, path := testProvider(t, jsonHandler(`{
		"embedding": [0.1, 0.2, 0.3],
		"inputTextTokenCount": 4
	}`))

	res, err := p.EmbeddingModel("amazon.titan-embed-text-v2:0").DoEmbed(
		context.Background(), provider.EmbeddingCallOptions{Values: []string{"hello"}})
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasSuffix(*path, "/invoke") {
		t.Errorf("path = %q", *path)
	}
	if len(res.Embeddings) != 1 || len(res.Embeddings[0]) != 3 {
		t.Fatalf("embeddings = %#v", res.Embeddings)
	}
	if res.Usage.Tokens == nil || *res.Usage.Tokens != 4 {
		t.Errorf("tokens = %+v", res.Usage)
	}

	var req map[string]any
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req["inputText"] != "hello" {
		t.Errorf("body = %s", *sent)
	}
}

func TestEmbedCohereBatch(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(`{"embeddings": [[1, 2], [3, 4]]}`))

	res, err := p.EmbeddingModel("cohere.embed-english-v3").DoEmbed(
		context.Background(), provider.EmbeddingCallOptions{Values: []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("embeddings = %#v", res.Embeddings)
	}

	var req map[string]any
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req["input_type"] != "search_query" {
		t.Errorf("cohere requires input_type, body = %s", *sent)
	}
}

func TestEmbedNova(t *testing.T) {
	p, sent, _ := testProvider(t, jsonHandler(`{
		"embeddings": [{"embeddingType": "float", "embedding": [0.5]}],
		"inputTokenCount": 2
	}`))

	res, err := p.EmbeddingModel("amazon.nova-2-multimodal-embed-v1:0").DoEmbed(
		context.Background(), provider.EmbeddingCallOptions{Values: []string{"doc"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 1 || res.Embeddings[0][0] != 0.5 {
		t.Fatalf("embeddings = %#v", res.Embeddings)
	}

	var req map[string]any
	if err := json.Unmarshal(*sent, &req); err != nil {
		t.Fatal(err)
	}
	if req["taskType"] != "SINGLE_EMBEDDING" {
		t.Errorf("body = %s", *sent)
	}
}

func TestEmbedRejectsOversizedBatch(t *testing.T) {
	p, _, _ := testProvider(t, func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not have been called")
	})

	_, err := p.EmbeddingModel("amazon.titan-embed-text-v2:0").DoEmbed(
		context.Background(), provider.EmbeddingCallOptions{Values: []string{"a", "b"}})
	if err == nil {
		t.Fatal("titan accepts one value per call")
	}
}

func TestEmbedCohereV4(t *testing.T) {
	p, _, _ := testProvider(t, jsonHandler(`{"embeddings": {"float": [[9, 8]]}}`))

	res, err := p.EmbeddingModel("us.cohere.embed-v4:0").DoEmbed(
		context.Background(), provider.EmbeddingCallOptions{Values: []string{"q"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 1 || res.Embeddings[0][0] != 9 {
		t.Fatalf("embeddings = %#v", res.Embeddings)
	}
}

func TestMaxEmbeddingsPerCall(t *testing.T) {
	p := New(Options{Credentials: StaticCredentials{AccessKeyID: "A", SecretAccessKey: "B"}})
	if n := p.EmbeddingModel("amazon.titan-embed-text-v2:0").MaxEmbeddingsPerCall(); n != 1 {
		t.Errorf("titan = %d", n)
	}
	if n := p.EmbeddingModel("cohere.embed-v4:0").MaxEmbeddingsPerCall(); n != 96 {
		t.Errorf("cohere = %d", n)
	}
}

func TestEmbedEmpty(t *testing.T) {
	p := New(Options{Credentials: StaticCredentials{AccessKeyID: "A", SecretAccessKey: "B"}})
	res, err := p.EmbeddingModel("amazon.titan-embed-text-v2:0").DoEmbed(
		context.Background(), provider.EmbeddingCallOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Embeddings) != 0 {
		t.Errorf("embeddings = %#v", res.Embeddings)
	}
}
