package openaicompat

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// imagesPath is the endpoint OpenAI-compatible gateways serve generation on.
const imagesPath = "/images/generations"

// maxImagesPerCall is OpenAI's ceiling on n for the images endpoint.
const maxImagesPerCall = 10

// ImageModel returns an image model handle.
//
//	client := openaicompat.New(openaicompat.Options{Name: "openai"})
//	model := client.ImageModel("gpt-image-1")
func (p *Provider) ImageModel(modelID string) provider.ImageModel {
	return &imageModel{modelID: modelID, provider: p}
}

// imageModel implements provider.ImageModel over /images/generations.
type imageModel struct {
	modelID  string
	provider *Provider
}

func (m *imageModel) SpecificationVersion() string { return provider.ImageSpecificationVersion }
func (m *imageModel) Provider() string             { return m.provider.name }
func (m *imageModel) ModelID() string              { return m.modelID }
func (m *imageModel) MaxImagesPerCall() int        { return maxImagesPerCall }

// imageRequest is the /images/generations body.
type imageRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	N      int    `json:"n,omitempty"`
	Size   string `json:"size,omitempty"`
	// ResponseFormat asks for inline bytes. gpt-image-1 always returns base64
	// and rejects the field, so it is only sent when the caller's gateway
	// needs it — see Options.ImageResponseFormat.
	ResponseFormat string `json:"response_format,omitempty"`

	// Extra carries provider-specific fields, merged at the top level.
	Extra provider.JSONObject `json:"-"`
}

// imageResponse is the reply.
type imageResponse struct {
	Data []struct {
		B64JSON string `json:"b64_json"`
		URL     string `json:"url"`
	} `json:"data"`
}

// DoGenerate implements provider.ImageModel.
func (m *imageModel) DoGenerate(ctx context.Context, opts provider.ImageCallOptions) (*provider.ImageResult, error) {
	var warnings []provider.Warning

	if opts.AspectRatio != "" {
		// The endpoint takes pixel sizes only. Converting a ratio would mean
		// inventing dimensions the caller did not ask for.
		warnings = append(warnings, provider.Unsupported(
			"aspectRatio", "the images endpoint takes a size in pixels; use Size instead"))
	}
	if opts.Seed != nil {
		warnings = append(warnings, provider.Unsupported(
			"seed", "the images endpoint has no seed parameter"))
	}

	n := opts.N
	if n <= 0 {
		n = 1
	}
	if n > maxImagesPerCall {
		return nil, fmt.Errorf("openaicompat: %d images exceeds the %d per call limit",
			n, maxImagesPerCall)
	}

	req := &imageRequest{
		Model:          m.modelID,
		Prompt:         opts.Prompt,
		N:              n,
		Size:           opts.Size,
		ResponseFormat: m.provider.imageResponseFormat,
	}
	if extra := opts.ProviderOptions.Get(m.provider.optionsKey); extra != nil {
		req.Extra = extra
	}

	body, err := marshalImageRequest(req)
	if err != nil {
		return nil, err
	}

	resp, err := providerutil.Retry(ctx, m.provider.retry, func(ctx context.Context) (*imageResponse, error) {
		httpResp, err := m.provider.client.PostJSON(ctx, imagesPath, json.RawMessage(body), opts.Headers)
		if err != nil {
			return nil, err
		}
		var decoded imageResponse
		if err := providerutil.DecodeJSON(httpResp, &decoded); err != nil {
			return nil, err
		}
		return &decoded, nil
	})
	if err != nil {
		return nil, err
	}

	images := make([]provider.GeneratedImage, 0, len(resp.Data))
	for _, d := range resp.Data {
		images = append(images, provider.GeneratedImage{
			Base64: d.B64JSON,
			URL:    d.URL,
			// The endpoint does not report a media type; PNG is what every
			// compatible gateway returns for a generation.
			MediaType: mediaTypeFor(d.B64JSON, d.URL),
		})
	}

	return &provider.ImageResult{Images: images, Warnings: warnings}, nil
}

// marshalImageRequest renders the body, merging provider extras as siblings
// of the standard fields.
func marshalImageRequest(req *imageRequest) ([]byte, error) {
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

// mediaTypeFor reports the type of a returned image, which the endpoint leaves
// unstated. Only inline data can be sniffed; a URL is left unlabelled rather
// than guessed.
func mediaTypeFor(b64, url string) string {
	if b64 == "" {
		return ""
	}
	// "iVBORw0KGgo" is the base64 prefix of the PNG magic bytes, and
	// "/9j/" is JPEG's.
	switch {
	case len(b64) >= 11 && b64[:11] == "iVBORw0KGgo":
		return "image/png"
	case len(b64) >= 4 && b64[:4] == "/9j/":
		return "image/jpeg"
	case len(b64) >= 4 && b64[:4] == "UklG":
		return "image/webp"
	default:
		return ""
	}
}
