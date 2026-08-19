// Command hosted exercises provider-hosted tools: search, fetch and code
// execution that the provider runs on its own side.
//
//	go run ./examples/hosted -provider google -tool search "what shipped in Go 1.26?"
//	go run ./examples/hosted -provider anthropic -tool search "latest Go release"
//	go run ./examples/hosted -provider google -tool code "what is 2^64?"
//
// Nothing runs locally: the call and its result both arrive inside the model's
// turn, which is what makes these different from ordinary tools.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/anthropic"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
)

func main() {
	providerName := flag.String("provider", "google", "google or anthropic")
	modelID := flag.String("model", "", "model id (defaults per provider)")
	baseURL := flag.String("base-url", "", "override the provider's API root")
	keyEnv := flag.String("key-env", "", "environment variable holding the credential")
	toolName := flag.String("tool", "search", "search, fetch, or code")
	flag.Parse()

	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: hosted [flags] <prompt>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	model, tools, err := resolve(*providerName, *modelID, *baseURL, *keyEnv, *toolName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := run(context.Background(), model, tools, prompt); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

func resolve(providerName, modelID, baseURL, keyEnv, toolName string) (provider.LanguageModel, []provider.ProviderTool, error) {
	switch providerName {
	case "google", "gemini":
		model := google.New(google.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			LanguageModel(orDefault(modelID, "gemini-3-pro"))

		switch toolName {
		case "search":
			return model, []provider.ProviderTool{google.Search()}, nil
		case "fetch":
			return model, []provider.ProviderTool{google.URLContext()}, nil
		case "code":
			return model, []provider.ProviderTool{google.CodeExecution()}, nil
		}

	case "anthropic":
		model := anthropic.New(anthropic.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			LanguageModel(orDefault(modelID, "claude-opus-5"))

		switch toolName {
		case "search":
			return model, []provider.ProviderTool{
				anthropic.WebSearch(anthropic.WebSearchOptions{MaxUses: 3}),
			}, nil
		case "fetch":
			return model, []provider.ProviderTool{
				anthropic.WebFetch(anthropic.WebFetchOptions{MaxUses: 2}),
			}, nil
		case "code":
			return model, []provider.ProviderTool{anthropic.CodeExecution()}, nil
		}

	default:
		return nil, nil, fmt.Errorf("unknown provider %q", providerName)
	}

	return nil, nil, fmt.Errorf("unknown tool %q: want search, fetch, or code", toolName)
}

func run(ctx context.Context, model provider.LanguageModel, tools []provider.ProviderTool, prompt string) error {
	res, err := ai.StreamText(ctx, ai.Options{
		Model:         model,
		System:        "Answer using the tools available to you. Be brief and cite what you used.",
		Messages:      []provider.Message{ai.UserText(prompt)},
		ProviderTools: tools,
	})
	if err != nil {
		return err
	}

	var sources []provider.Source

	for part := range res.Stream {
		switch v := part.(type) {
		case provider.StreamStart:
			for _, w := range v.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s %s %s\n", w.Type, w.Feature, w.Details)
			}

		case provider.TextDelta:
			fmt.Print(v.Delta)

		case provider.ToolCall:
			fmt.Printf("\n\033[36m→ %s\033[0m \033[2m%s\033[0m\n", v.ToolName, oneLine(v.Input, 90))

		case provider.ToolResult:
			encoded, _ := json.Marshal(v.Result)
			marker := "←"
			if v.IsError {
				marker = "\033[31m← failed\033[0m"
			}
			fmt.Printf("\033[2m%s %s %s\033[0m\n", marker, v.ToolName, oneLine(string(encoded), 90))

		case provider.Source:
			sources = append(sources, v)

		case provider.ErrorPart:
			return v.Err
		}
	}

	if len(sources) > 0 {
		fmt.Printf("\n\n\033[2msources:\033[0m\n")
		for _, s := range sources {
			fmt.Printf("  %s — %s\n", s.Title, s.URL)
		}
	}

	final, err := res.Final()
	if err != nil {
		return err
	}
	fmt.Printf("\n\033[2m%d step(s) · %s\033[0m\n", len(final.Steps), final.FinishReason.Unified)
	return nil
}

// oneLine flattens a payload for a single-line preview.
func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return s[:limit] + "..."
	}
	return s
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
