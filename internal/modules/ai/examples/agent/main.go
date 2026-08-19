// Command agent is a minimal streaming agent built on pi, useful both as a
// worked example and as a way to check a provider end to end.
//
//	go run ./examples/agent -provider anthropic -model claude-opus-5 "what is in ./provider?"
//	go run ./examples/agent -provider google    -model gemini-3-pro  "read go.mod and summarise it"
//	go run ./examples/agent -provider deepseek  -model deepseek-chat "list the files here"
//	go run ./examples/agent -provider bedrock   -model us.anthropic.claude-sonnet-4-5-20250929-v1:0 "list the files here"
//
// It registers two read-only tools so the agent loop, tool execution and
// multi-step continuation all get exercised.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/anthropic"
	"github.com/notshekhar/pi/internal/modules/ai/providers/bedrock"
	"github.com/notshekhar/pi/internal/modules/ai/providers/deepseek"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providers/googlevertex"
	"github.com/notshekhar/pi/internal/modules/ai/providers/kimi"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
)

func main() {
	providerName := flag.String("provider", "anthropic",
		"anthropic, google, vertex, deepseek, kimi, openai, or bedrock")
	modelID := flag.String("model", "", "model id (defaults per provider)")
	baseURL := flag.String("base-url", "", "override the provider's API root")
	keyEnv := flag.String("key-env", "", "environment variable holding the credential")
	reasoning := flag.String("reasoning", "", "none, low, medium, high, or xhigh")
	maxSteps := flag.Int("max-steps", 8, "maximum agent steps")
	showReasoning := flag.Bool("show-reasoning", true, "print reasoning as it streams")
	dump := flag.String("dump", "", "append raw provider chunks to this file, one JSON value per line")
	flag.Parse()

	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: agent [flags] <prompt>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	model, err := resolveModel(*providerName, *modelID, *baseURL, *keyEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	// A raw dump turns a live run into test fixtures: the file holds exactly
	// the chunks the provider sent, which is what the stream tests replay.
	var raw *os.File
	if *dump != "" {
		raw, err = os.OpenFile(*dump, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			os.Exit(1)
		}
		defer raw.Close()
	}

	// Ctrl-C cancels the run: the context reaches the provider's HTTP call and
	// the in-flight tools, so nothing is left running.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, model, prompt, *reasoning, *maxSteps, *showReasoning, raw); err != nil {
		fmt.Fprintln(os.Stderr, "\nerror:", err)
		os.Exit(1)
	}
}

// resolveModel builds a model handle for the named provider. baseURL and
// keyEnv are empty for the vendor's own endpoint, and set when pointing the
// same wire format at a gateway.
func resolveModel(name, modelID, baseURL, keyEnv string) (provider.LanguageModel, error) {
	switch name {
	case "anthropic":
		return anthropic.New(anthropic.Options{
			BaseURL: baseURL,
			APIKey:  keyFromEnv(keyEnv),
		}).LanguageModel(orDefault(modelID, "claude-opus-5")), nil

	case "google", "gemini":
		return google.New(google.Options{
			BaseURL: baseURL,
			APIKey:  keyFromEnv(keyEnv),
		}).LanguageModel(orDefault(modelID, "gemini-3-pro")), nil

	case "vertex":
		return googlevertex.New(googlevertex.Options{}).
			LanguageModel(orDefault(modelID, "gemini-3-pro")), nil

	case "deepseek":
		return deepseek.New(deepseek.Options{
			BaseURL: baseURL,
			APIKey:  keyFromEnv(keyEnv),
		}).LanguageModel(orDefault(modelID, deepseek.Chat)), nil

	case "kimi":
		return kimi.New(kimi.Options{
			BaseURL: baseURL,
			APIKey:  keyFromEnv(keyEnv),
		}).LanguageModel(orDefault(modelID, kimi.DefaultModel(keyFromEnv(keyEnv)))), nil

	case "openai":
		return openaicompat.New(openaicompat.Options{
			Name:                   "openai",
			BaseURL:                baseURL,
			APIKeyEnv:              keyEnv,
			UseMaxCompletionTokens: true,
		}).LanguageModel(orDefault(modelID, "gpt-5")), nil

	case "bedrock":
		return bedrock.New(bedrock.Options{BaseURL: baseURL}).
			LanguageModel(orDefault(modelID, "us.anthropic.claude-sonnet-4-5-20250929-v1:0")), nil

	case "compat":
		// Any gateway speaking chat completions: -base-url and -key-env say
		// which one.
		return openaicompat.New(openaicompat.Options{
			Name:      "compat",
			BaseURL:   baseURL,
			APIKeyEnv: keyEnv,
		}).LanguageModel(modelID), nil

	default:
		return nil, fmt.Errorf("unknown provider %q", name)
	}
}

// keyFromEnv reads an alternative credential variable, for providers whose
// Options take the key itself rather than the variable name.
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

// run streams one agent conversation to stdout. A non-nil dump receives the
// provider's raw chunks.
func run(ctx context.Context, model provider.LanguageModel, prompt, reasoning string, maxSteps int, showReasoning bool, dump *os.File) error {
	res, err := ai.StreamText(ctx, ai.Options{
		Model: model,
		System: "You are a concise assistant working in a Go repository. " +
			"Use the tools to inspect files before answering. Keep answers short.",
		Messages:         []provider.Message{ai.UserText(prompt)},
		Tools:            []ai.Tool{readFileTool, listDirTool},
		MaxSteps:         maxSteps,
		Reasoning:        provider.ReasoningEffort(reasoning),
		IncludeRawChunks: dump != nil,
	})
	if err != nil {
		return err
	}

	inReasoning := false

	for part := range res.Stream {
		switch v := part.(type) {
		case provider.StreamStart:
			for _, w := range v.Warnings {
				fmt.Fprintf(os.Stderr, "warning: %s %s %s\n", w.Type, w.Feature, w.Details)
			}

		case provider.ReasoningDelta:
			if showReasoning && v.Delta != "" {
				if !inReasoning {
					fmt.Print("\033[2m")
					inReasoning = true
				}
				fmt.Print(v.Delta)
			}

		case provider.ReasoningEnd:
			if inReasoning {
				fmt.Print("\033[0m\n")
				inReasoning = false
			}

		case provider.TextDelta:
			fmt.Print(v.Delta)

		case provider.Raw:
			if dump != nil {
				if encoded, err := json.Marshal(v.RawValue); err == nil {
					fmt.Fprintf(dump, "%s\n", encoded)
				}
			}

		case provider.ToolInputStart:
			fmt.Printf("\n\033[36m→ %s\033[0m ", v.ToolName)

		case ai.ToolExecuted:
			exec := v.Execution
			if exec.Err != nil {
				fmt.Printf("\033[31m(failed: %v)\033[0m\n", exec.Err)
			} else {
				fmt.Printf("\033[2m(%s)\033[0m\n", summarize(exec.Result))
			}

		case ai.RunFinish:
			printSummary(v)

		case provider.ErrorPart:
			return v.Err
		}
	}

	if _, err := res.Final(); err != nil {
		return err
	}
	return nil
}

// summarize renders a one-line preview of a tool result.
func summarize(result ai.ToolResult) string {
	switch out := result.Output().(type) {
	case provider.ToolOutputText:
		return preview(out.Value)
	case provider.ToolOutputErrorText:
		return "error: " + preview(out.Value)
	default:
		return fmt.Sprintf("%T", out)
	}
}

// preview shortens text to a single readable line.
func preview(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) > 60 {
		return s[:60] + "..."
	}
	return s
}

// printSummary reports the run totals.
func printSummary(f ai.RunFinish) {
	fmt.Printf("\n\n\033[2m%d step(s) · %s", f.Steps, f.FinishReason.Unified)

	if in := f.Usage.InputTokens.Total; in != nil {
		fmt.Printf(" · in %d", *in)
		if cached := f.Usage.InputTokens.CacheRead; cached != nil && *cached > 0 {
			fmt.Printf(" (%d cached)", *cached)
		}
	}
	if out := f.Usage.OutputTokens.Total; out != nil {
		fmt.Printf(" · out %d", *out)
		if r := f.Usage.OutputTokens.Reasoning; r != nil && *r > 0 {
			fmt.Printf(" (%d thinking)", *r)
		}
	}
	fmt.Println("\033[0m")
}

// readFileArgs is the input to the read_file tool.
type readFileArgs struct {
	Path string `json:"path" jsonschema:"description=Path to the file to read, relative to the working directory"`
}

var readFileTool = ai.NewTool("read_file", "Read the contents of a text file",
	func(ctx context.Context, a readFileArgs) (ai.ToolResult, error) {
		path, err := safePath(a.Path)
		if err != nil {
			return ai.ToolError(err.Error()), nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			// Returning the failure as a result rather than an error lets the
			// model correct the path and try again.
			return ai.ToolErrorf("could not read %s: %v", a.Path, err), nil
		}

		const maxBytes = 40_000
		if len(data) > maxBytes {
			return ai.ToolTextf("%s\n\n[truncated at %d bytes]", data[:maxBytes], maxBytes), nil
		}
		return ai.ToolText(string(data)), nil
	})

// listDirArgs is the input to the list_dir tool.
type listDirArgs struct {
	Path string `json:"path,omitempty" jsonschema:"description=Directory to list; defaults to the working directory"`
}

var listDirTool = ai.NewTool("list_dir", "List the entries of a directory",
	func(ctx context.Context, a listDirArgs) (ai.ToolResult, error) {
		target := a.Path
		if target == "" {
			target = "."
		}
		path, err := safePath(target)
		if err != nil {
			return ai.ToolError(err.Error()), nil
		}

		entries, err := os.ReadDir(path)
		if err != nil {
			return ai.ToolErrorf("could not list %s: %v", target, err), nil
		}

		var b strings.Builder
		for _, e := range entries {
			if e.IsDir() {
				fmt.Fprintf(&b, "%s/\n", e.Name())
				continue
			}
			fmt.Fprintf(&b, "%s\n", e.Name())
		}
		return ai.ToolText(b.String()), nil
	})

// safePath keeps the demo tools inside the working directory. A real agent
// needs a proper sandbox; this is only enough to stop an idle model wandering
// into the rest of the filesystem.
func safePath(path string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}

	resolved := path
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Join(cwd, resolved)
	}
	resolved = filepath.Clean(resolved)

	rel, err := filepath.Rel(cwd, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q is outside the working directory", path)
	}
	return resolved, nil
}
