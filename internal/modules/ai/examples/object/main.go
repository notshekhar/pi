// Command object asks a model for a typed value instead of prose, which is
// what GenerateObject and StreamObject are for.
//
//	go run ./examples/object -provider anthropic "the Go programming language"
//	go run ./examples/object -provider google -stream "SQLite"
//
// The schema comes from the Go type: whatever the provider needs underneath —
// a response format, a response schema, or a forced tool — the result is the
// same struct.
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
	"github.com/notshekhar/pi/internal/modules/ai/providers/deepseek"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
)

// Assessment is the shape the model has to produce.
type Assessment struct {
	Name      string   `json:"name" jsonschema:"description=The subject being assessed"`
	Summary   string   `json:"summary" jsonschema:"description=Two sentences on what it is and why it matters"`
	Strengths []string `json:"strengths" jsonschema:"description=Between two and four strengths"`
	Year      int      `json:"year" jsonschema:"description=Year it first appeared"`
}

func main() {
	providerName := flag.String("provider", "anthropic", "anthropic, google, deepseek, openai, or compat")
	modelID := flag.String("model", "", "model id (defaults per provider)")
	baseURL := flag.String("base-url", "", "override the provider's API root")
	keyEnv := flag.String("key-env", "", "environment variable holding the credential")
	stream := flag.Bool("stream", false, "fill the value in as it arrives")
	flag.Parse()

	subject := strings.Join(flag.Args(), " ")
	if subject == "" {
		fmt.Fprintln(os.Stderr, "usage: object [flags] <subject>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	model, err := resolveModel(*providerName, *modelID, *baseURL, *keyEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	opts := ai.Options{
		Model:             model,
		System:            "You assess software honestly and briefly.",
		Messages:          []provider.Message{ai.UserText("Assess: " + subject)},
		ObjectName:        "assessment",
		ObjectDescription: "A short, factual assessment of a piece of software.",
	}

	if err := run(context.Background(), opts, *stream); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, opts ai.Options, stream bool) error {
	if !stream {
		res, err := ai.GenerateObject[Assessment](ctx, opts)
		if err != nil {
			return err
		}
		printWarnings(res.Warnings)
		return print(res.Object)
	}

	res, err := ai.StreamObject[Assessment](ctx, opts)
	if err != nil {
		return err
	}

	// Redraw the value on every update, so the fields visibly fill in.
	for part := range res.Stream {
		fmt.Printf("\033[2J\033[H")
		if err := print(part.Snapshot); err != nil {
			return err
		}
	}

	final, err := res.Final()
	if err != nil {
		return err
	}
	fmt.Printf("\033[2J\033[H")
	printWarnings(final.Warnings)
	return print(final.Object)
}

func print(v Assessment) error {
	encoded, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

func printWarnings(warnings []provider.Warning) {
	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "warning: %s %s %s\n", w.Type, w.Feature, w.Details)
	}
}

// resolveModel builds a model handle for the named provider.
func resolveModel(name, modelID, baseURL, keyEnv string) (provider.LanguageModel, error) {
	switch name {
	case "anthropic":
		return anthropic.New(anthropic.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			LanguageModel(orDefault(modelID, "claude-opus-5")), nil

	case "google", "gemini":
		return google.New(google.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			LanguageModel(orDefault(modelID, "gemini-3-pro")), nil

	case "deepseek":
		return deepseek.New(deepseek.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			LanguageModel(orDefault(modelID, deepseek.Chat)), nil

	case "openai":
		return openaicompat.New(openaicompat.Options{
			Name:                   "openai",
			BaseURL:                baseURL,
			APIKeyEnv:              keyEnv,
			UseMaxCompletionTokens: true,
		}).LanguageModel(orDefault(modelID, "gpt-5")), nil

	case "compat":
		return openaicompat.New(openaicompat.Options{
			Name:      "compat",
			BaseURL:   baseURL,
			APIKeyEnv: keyEnv,
		}).LanguageModel(modelID), nil

	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
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
