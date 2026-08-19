// Command embed ranks a small corpus against a query, which is the shortest
// end-to-end check that embeddings actually carry meaning.
//
//	go run ./examples/embed -provider google -model gemini-embedding-001 "how do I run tests?"
//	go run ./examples/embed -provider openai -model text-embedding-3-small "how do I run tests?"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
)

// corpus is deliberately mixed: two entries are about testing, and the rest
// are near misses that a keyword search would rank badly.
var corpus = []string{
	"Run the suite with `go test ./...`; add -race before pushing.",
	"The retry policy backs off exponentially and gives up after four attempts.",
	"Table-driven cases live next to the code they cover, in _test.go files.",
	"Set ANTHROPIC_API_KEY in the environment; the key is read lazily.",
	"Streaming responses must be drained or the HTTP connection leaks.",
	"Embeddings are batched to whatever the model says it accepts per call.",
}

func main() {
	providerName := flag.String("provider", "google", "google or openai")
	modelID := flag.String("model", "", "model id (defaults per provider)")
	baseURL := flag.String("base-url", "", "override the provider's API root")
	keyEnv := flag.String("key-env", "", "environment variable holding the credential")
	top := flag.Int("top", 3, "how many results to show")
	flag.Parse()

	query := strings.Join(flag.Args(), " ")
	if query == "" {
		fmt.Fprintln(os.Stderr, "usage: embed [flags] <query>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	model, docOpts, queryOpts, err := resolve(*providerName, *modelID, *baseURL, *keyEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := run(context.Background(), model, docOpts, queryOpts, query, *top); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// resolve builds the model plus the two option sets. They differ because
// Google's embeddings are asymmetric: a document and a query are meant to be
// embedded with different task types.
func resolve(providerName, modelID, baseURL, keyEnv string) (provider.EmbeddingModel, ai.EmbedOptions, ai.EmbedOptions, error) {
	var (
		model     provider.EmbeddingModel
		docOpts   ai.EmbedOptions
		queryOpts ai.EmbedOptions
	)

	switch providerName {
	case "google", "gemini":
		model = google.New(google.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			EmbeddingModel(orDefault(modelID, "gemini-embedding-001"))

		docOpts.ProviderOptions = provider.ProviderOptions{
			"google": {"taskType": string(google.TaskRetrievalDocument)},
		}
		queryOpts.ProviderOptions = provider.ProviderOptions{
			"google": {"taskType": string(google.TaskRetrievalQuery)},
		}

	case "openai", "compat":
		model = openaicompat.New(openaicompat.Options{
			Name:      "openai",
			BaseURL:   baseURL,
			APIKeyEnv: keyEnv,
		}).EmbeddingModel(orDefault(modelID, "text-embedding-3-small"))

	default:
		return nil, docOpts, queryOpts, fmt.Errorf("unknown provider %q", providerName)
	}

	docOpts.Model = model
	queryOpts.Model = model
	return model, docOpts, queryOpts, nil
}

func run(ctx context.Context, model provider.EmbeddingModel, docOpts, queryOpts ai.EmbedOptions, query string, top int) error {
	docs, err := ai.EmbedMany(ctx, corpus, docOpts)
	if err != nil {
		return err
	}
	for _, w := range docs.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s %s %s\n", w.Type, w.Feature, w.Details)
	}

	queryVec, err := ai.Embed(ctx, query, queryOpts)
	if err != nil {
		return err
	}

	type scored struct {
		text  string
		score float64
	}
	ranked := make([]scored, 0, len(corpus))

	for i, vec := range docs.Embeddings {
		score, err := ai.CosineSimilarity(queryVec, vec)
		if err != nil {
			return err
		}
		ranked = append(ranked, scored{text: corpus[i], score: score})
	}
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].score > ranked[j].score })

	fmt.Printf("\033[2m%s · %d dimensions · %d documents\033[0m\n\n",
		model.ModelID(), len(queryVec), len(corpus))
	fmt.Printf("query: %s\n\n", query)

	if top > len(ranked) {
		top = len(ranked)
	}
	for _, r := range ranked[:top] {
		fmt.Printf("  \033[36m%.3f\033[0m  %s\n", r.score, r.text)
	}

	if docs.Usage.Tokens != nil {
		fmt.Printf("\n\033[2m%d tokens\033[0m\n", *docs.Usage.Tokens)
	}
	return nil
}

func keyFromEnv(envVar string) string {
	if envVar == "" {
		return ""
	}
	return os.Getenv(envVar)
}

func orDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}
