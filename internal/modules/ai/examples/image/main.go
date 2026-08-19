// Command image generates images and writes them to disk.
//
//	go run ./examples/image -provider google -model imagen-4.0-generate-001 \
//	  -aspect 16:9 "a lighthouse in a storm, oil painting"
//
//	go run ./examples/image -provider openai -model gpt-image-1 \
//	  -size 1024x1024 -n 2 "a lighthouse in a storm, oil painting"
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providers/google"
	"github.com/notshekhar/pi/internal/modules/ai/providers/openaicompat"
)

func main() {
	providerName := flag.String("provider", "google", "google or openai")
	modelID := flag.String("model", "", "model id (defaults per provider)")
	baseURL := flag.String("base-url", "", "override the provider's API root")
	keyEnv := flag.String("key-env", "", "environment variable holding the credential")
	n := flag.Int("n", 1, "how many images to generate")
	size := flag.String("size", "", "pixel size, e.g. 1024x1024")
	aspect := flag.String("aspect", "", "aspect ratio, e.g. 16:9")
	outDir := flag.String("out", ".", "directory to write the images to")
	flag.Parse()

	prompt := strings.Join(flag.Args(), " ")
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: image [flags] <prompt>")
		flag.PrintDefaults()
		os.Exit(2)
	}

	model, err := resolve(*providerName, *modelID, *baseURL, *keyEnv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}

	if err := run(context.Background(), model, prompt, *n, *size, *aspect, *outDir); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func resolve(providerName, modelID, baseURL, keyEnv string) (provider.ImageModel, error) {
	switch providerName {
	case "google", "gemini":
		return google.New(google.Options{BaseURL: baseURL, APIKey: keyFromEnv(keyEnv)}).
			ImageModel(orDefault(modelID, "imagen-4.0-generate-001")), nil

	case "openai", "compat":
		return openaicompat.New(openaicompat.Options{
			Name:      "openai",
			BaseURL:   baseURL,
			APIKeyEnv: keyEnv,
		}).ImageModel(orDefault(modelID, "gpt-image-1")), nil

	default:
		return nil, fmt.Errorf("unknown provider %q", providerName)
	}
}

func run(ctx context.Context, model provider.ImageModel, prompt string, n int, size, aspect, outDir string) error {
	res, err := ai.GenerateImage(ctx, prompt, ai.ImageOptions{
		Model:       model,
		N:           n,
		Size:        size,
		AspectRatio: aspect,
	})
	if err != nil {
		return err
	}

	for _, w := range res.Warnings {
		fmt.Fprintf(os.Stderr, "warning: %s %s %s\n", w.Type, w.Feature, w.Details)
	}
	if len(res.Images) == 0 {
		return fmt.Errorf("the model returned no images")
	}

	for i, img := range res.Images {
		if img.URL != "" && img.Base64 == "" {
			fmt.Printf("%d: %s\n", i, img.URL)
			continue
		}

		data, err := ai.Bytes(img)
		if err != nil {
			return err
		}

		name := filepath.Join(outDir, fmt.Sprintf("image-%d%s", i, extensionFor(img.MediaType)))
		if err := os.WriteFile(name, data, 0o644); err != nil {
			return err
		}
		fmt.Printf("%s  %d KB  %s\n", name, len(data)/1024, img.MediaType)
	}
	return nil
}

// extensionFor picks a file extension from a media type, defaulting to .png
// because that is what every one of these endpoints returns unasked.
func extensionFor(mediaType string) string {
	switch mediaType {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
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
