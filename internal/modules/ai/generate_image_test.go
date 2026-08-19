package ai_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// mockImager records the calls it was given and returns images labelled with
// the call and index they came from, so a shuffled result is visible.
type mockImager struct {
	perCall int

	mu    sync.Mutex
	calls []provider.ImageCallOptions
}

func (m *mockImager) SpecificationVersion() string { return provider.ImageSpecificationVersion }
func (m *mockImager) Provider() string             { return "mock" }
func (m *mockImager) ModelID() string              { return "mock-image" }
func (m *mockImager) MaxImagesPerCall() int        { return m.perCall }

func (m *mockImager) DoGenerate(_ context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error) {
	m.mu.Lock()
	call := len(m.calls)
	m.calls = append(m.calls, opts)
	m.mu.Unlock()

	res := &provider.ImageResult{}
	for i := range opts.N {
		res.Images = append(res.Images, provider.GeneratedImage{
			Base64:    base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("call%d-img%d", call, i))),
			MediaType: "image/png",
		})
	}
	return res, nil
}

func TestGenerateImageSplitsAcrossCalls(t *testing.T) {
	model := &mockImager{perCall: 4}

	res, err := ai.GenerateImage(context.Background(), "a lighthouse", ai.ImageOptions{
		Model: model,
		N:     10,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(model.calls) != 3 {
		t.Errorf("calls = %d, want 3 (4+4+2)", len(model.calls))
	}
	for i, c := range model.calls {
		if c.N > 4 {
			t.Errorf("call %d asked for %d images, over the model's limit of 4", i, c.N)
		}
	}
	if len(res.Images) != 10 {
		t.Fatalf("images = %d, want 10", len(res.Images))
	}
}

func TestGenerateImageDerivesADistinctSeedPerCall(t *testing.T) {
	model := &mockImager{perCall: 1}
	seed := int64(100)

	if _, err := ai.GenerateImage(context.Background(), "a lighthouse", ai.ImageOptions{
		Model: model,
		N:     3,
		Seed:  &seed,
	}); err != nil {
		t.Fatal(err)
	}

	// Reusing one seed across calls would return the same image three times.
	seen := map[int64]bool{}
	for i, c := range model.calls {
		if c.Seed == nil {
			t.Fatalf("call %d got no seed", i)
		}
		if seen[*c.Seed] {
			t.Fatalf("seed %d was reused; every call would return the same image", *c.Seed)
		}
		seen[*c.Seed] = true
	}
}

func TestGenerateImageRejectsSizeAndAspectTogether(t *testing.T) {
	_, err := ai.GenerateImage(context.Background(), "a lighthouse", ai.ImageOptions{
		Model:       &mockImager{perCall: 1},
		Size:        "1024x1024",
		AspectRatio: "16:9",
	})
	// Which one wins would differ per provider, so neither is chosen silently.
	if err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("err = %v, want a refusal to accept both", err)
	}
}

func TestGenerateImageDefaultsToOneImage(t *testing.T) {
	model := &mockImager{perCall: 4}

	res, err := ai.GenerateImage(context.Background(), "a lighthouse", ai.ImageOptions{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Images) != 1 {
		t.Errorf("images = %d, want 1", len(res.Images))
	}
	if len(model.calls) != 1 || model.calls[0].N != 1 {
		t.Errorf("calls = %+v, want a single call for one image", model.calls)
	}
}

func TestGenerateImageAssumesOneWhenTheLimitIsUnknown(t *testing.T) {
	// A model reporting no limit is asked for one at a time: too many in a
	// request fails, while too few only costs a round trip.
	model := &mockImager{perCall: 0}

	if _, err := ai.GenerateImage(context.Background(), "a lighthouse", ai.ImageOptions{
		Model: model,
		N:     3,
	}); err != nil {
		t.Fatal(err)
	}
	if len(model.calls) != 3 {
		t.Errorf("calls = %d, want one per image", len(model.calls))
	}
}

func TestBytesDecodesInlineDataAndRefusesURLs(t *testing.T) {
	data, err := ai.Bytes(provider.GeneratedImage{
		Base64: base64.StdEncoding.EncodeToString([]byte("png bytes")),
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "png bytes" {
		t.Errorf("data = %q", data)
	}

	// Fetching a URL is an HTTP call with its own failure modes, so it stays
	// the caller's decision rather than happening inside a decode.
	_, err = ai.Bytes(provider.GeneratedImage{URL: "https://example.com/a.png"})
	if err == nil || !strings.Contains(err.Error(), "URL") {
		t.Errorf("err = %v, want a refusal naming the URL", err)
	}
}
