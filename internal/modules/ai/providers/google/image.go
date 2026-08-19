package google

import (
	"context"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// maxImagesPerCall is Imagen's ceiling on sampleCount.
const maxImagesPerCall = 4

// ImageModel returns an image model handle for an Imagen model.
//
//	model := google.New(google.Options{}).ImageModel("imagen-4.0-generate-001")
//
// Gemini's own image-output models are language models that return image
// parts, not Imagen models; drive those with LanguageModel and read the File
// content from the response.
func (p *Provider) ImageModel(modelID string) provider.ImageModel {
	return &imageModel{modelID: modelID, provider: p}
}

// imageModel implements provider.ImageModel over :predict.
type imageModel struct {
	modelID  string
	provider *Provider
}

func (m *imageModel) SpecificationVersion() string { return provider.ImageSpecificationVersion }
func (m *imageModel) Provider() string             { return providerID }
func (m *imageModel) ModelID() string              { return m.modelID }
func (m *imageModel) MaxImagesPerCall() int        { return maxImagesPerCall }

// predictRequest is the Imagen body. Unlike the text API this one nests the
// prompt under an "instances" list, matching Vertex's prediction shape.
type predictRequest struct {
	Instances  []predictInstance `json:"instances"`
	Parameters predictParameters `json:"parameters"`
}

// predictInstance is one prompt.
type predictInstance struct {
	Prompt string `json:"prompt"`
}

// predictParameters carries the generation settings.
type predictParameters struct {
	SampleCount int `json:"sampleCount,omitempty"`
	// AspectRatio is how Imagen sizes an image; it takes no pixel dimensions.
	AspectRatio string `json:"aspectRatio,omitempty"`
	Seed        *int64 `json:"seed,omitempty"`
	// PersonGeneration gates images of people and defaults to Google's own
	// policy when unset.
	PersonGeneration string `json:"personGeneration,omitempty"`
}

// predictResponse is the reply.
type predictResponse struct {
	Predictions []struct {
		BytesBase64Encoded string `json:"bytesBase64Encoded"`
		MimeType           string `json:"mimeType"`
		// RaiFilteredReason is set when a prediction was withheld by the
		// safety filter, in which case there are no bytes.
		RaiFilteredReason string `json:"raiFilteredReason"`
	} `json:"predictions"`
}

// DoGenerate implements provider.ImageModel.
func (m *imageModel) DoGenerate(ctx context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error) {
	var warnings []provider.Warning

	if opts.Size != "" {
		// Imagen takes a ratio, not pixels. Mapping one to the other would
		// mean picking dimensions the caller did not ask for.
		warnings = append(warnings, provider.Unsupported(
			"size", "imagen takes an aspect ratio rather than pixel dimensions; use AspectRatio"))
	}

	n := opts.N
	if n <= 0 {
		n = 1
	}
	if n > maxImagesPerCall {
		return nil, fmt.Errorf("google: %d images exceeds the %d per call limit", n, maxImagesPerCall)
	}

	params := predictParameters{
		SampleCount: n,
		AspectRatio: opts.AspectRatio,
		Seed:        opts.Seed,
	}
	if block := opts.ProviderOptions.Get(providerID); block != nil {
		if v, ok := block["personGeneration"].(string); ok {
			params.PersonGeneration = v
		}
	}

	req := &predictRequest{
		Instances:  []predictInstance{{Prompt: opts.Prompt}},
		Parameters: params,
	}

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*predictResponse, error) {
		httpResp, err := m.provider.client.PostJSON(
			ctx, m.provider.path(m.modelID, "predict", false), req, opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded predictResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	images := make([]provider.GeneratedImage, 0, len(resp.Predictions))
	for _, p := range resp.Predictions {
		if p.BytesBase64Encoded == "" {
			// A filtered prediction has no bytes. Reporting it is what tells a
			// caller why they got fewer images than they asked for.
			warnings = append(warnings, provider.Warning{
				Type:    provider.WarningOther,
				Feature: "safety filter",
				Details: "an image was withheld: " + p.RaiFilteredReason,
			})
			continue
		}
		images = append(images, provider.GeneratedImage{
			Base64:    p.BytesBase64Encoded,
			MediaType: p.MimeType,
		})
	}

	return &provider.ImageResult{Images: images, Warnings: warnings}, nil
}
