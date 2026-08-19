package ai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// urlModel is a model that declares which URLs it fetches itself and records
// the prompt it was handed.
type urlModel struct {
	patterns map[string][]*regexp.Regexp
	seen     provider.Prompt
}

func (m *urlModel) SpecificationVersion() string { return provider.SpecificationVersion }
func (m *urlModel) Provider() string             { return "url" }
func (m *urlModel) ModelID() string              { return "url-1" }

func (m *urlModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return m.patterns, nil
}

func (m *urlModel) DoGenerate(_ context.Context, opts provider.CallOptions) (*provider.GenerateResult, error) {
	m.seen = opts.Prompt
	return &provider.GenerateResult{}, nil
}

func (m *urlModel) DoStream(_ context.Context, opts provider.CallOptions) (*provider.StreamResult, error) {
	m.seen = opts.Prompt
	ch := make(chan provider.StreamPart)
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

// fileServer serves one image and counts the requests for it.
func fileServer(t *testing.T, body string, contentType string) (*httptest.Server, *atomic.Int64) {
	t.Helper()

	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", contentType)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	return srv, &hits
}

// promptWithImage builds a one-message prompt carrying an image URL.
func promptWithImage(t *testing.T, rawURL, mediaType string) provider.Prompt {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return provider.Prompt{provider.UserMessage{Content: []provider.UserPart{
		provider.FilePart{Data: provider.FileDataURL{URL: u}, MediaType: mediaType},
	}}}
}

// fileData returns the file part of a converted prompt.
func fileData(t *testing.T, prompt provider.Prompt) provider.FilePart {
	t.Helper()
	msg, ok := prompt[len(prompt)-1].(provider.UserMessage)
	if !ok {
		t.Fatalf("last message is %T, want a user message", prompt[len(prompt)-1])
	}
	part, ok := msg.Content[0].(provider.FilePart)
	if !ok {
		t.Fatalf("first part is %T, want a file part", msg.Content[0])
	}
	return part
}

func TestURLIsDownloadedWhenTheProviderCannotFetchIt(t *testing.T) {
	srv, hits := fileServer(t, "png bytes", "image/png; charset=binary")

	// The model declares no supported URLs, so core has to fetch this one.
	model := &urlModel{}
	prompt := promptWithImage(t, srv.URL+"/a.png", "image")

	resolved, err := resolveFileURLs(context.Background(), model, prompt, nil)
	if err != nil {
		t.Fatal(err)
	}

	part := fileData(t, resolved)
	bytes, ok := part.Data.(provider.FileDataBytes)
	if !ok {
		t.Fatalf("data is %T, want inline bytes", part.Data)
	}
	if string(bytes.Data) != "png bytes" {
		t.Errorf("data = %q", bytes.Data)
	}
	// The server's parameters must be stripped; providers reject them.
	if part.MediaType != "image/png" {
		t.Errorf("media type = %q, want image/png with the charset dropped", part.MediaType)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hits.Load())
	}
}

func TestURLIsLeftAloneWhenTheProviderFetchesIt(t *testing.T) {
	srv, hits := fileServer(t, "png bytes", "image/png")

	model := &urlModel{patterns: map[string][]*regexp.Regexp{
		"image/*": {regexp.MustCompile(`^http://127\.0\.0\.1`)},
	}}
	prompt := promptWithImage(t, srv.URL+"/a.png", "image/png")

	resolved, err := resolveFileURLs(context.Background(), model, prompt, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := fileData(t, resolved).Data.(provider.FileDataURL); !ok {
		t.Error("the URL was downloaded even though the provider fetches it itself")
	}
	// Downloading anyway would waste the round trip and lose the provider's
	// own handling of the URL.
	if hits.Load() != 0 {
		t.Errorf("requests = %d, want none", hits.Load())
	}
}

func TestSupportedURLsAreMatchedPerMediaType(t *testing.T) {
	srv, hits := fileServer(t, "pdf bytes", "application/pdf")

	// The model fetches images, but not documents.
	model := &urlModel{patterns: map[string][]*regexp.Regexp{
		"image/*": {regexp.MustCompile(`.*`)},
	}}
	prompt := promptWithImage(t, srv.URL+"/a.pdf", "application/pdf")

	resolved, err := resolveFileURLs(context.Background(), model, prompt, nil)
	if err != nil {
		t.Fatal(err)
	}

	if _, ok := fileData(t, resolved).Data.(provider.FileDataBytes); !ok {
		t.Error("a document matched an image-only pattern")
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1", hits.Load())
	}
}

func TestTheSameURLIsDownloadedOnce(t *testing.T) {
	srv, hits := fileServer(t, "png bytes", "image/png")

	u, err := url.Parse(srv.URL + "/a.png")
	if err != nil {
		t.Fatal(err)
	}
	file := provider.FilePart{Data: provider.FileDataURL{URL: u}, MediaType: "image/png"}

	// The same attachment twice in one turn, which is what a replayed
	// conversation looks like.
	prompt := provider.Prompt{
		provider.UserMessage{Content: []provider.UserPart{file}},
		provider.UserMessage{Content: []provider.UserPart{file}},
	}

	if _, err := resolveFileURLs(context.Background(), &urlModel{}, prompt, nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Errorf("requests = %d, want 1 for a repeated URL", hits.Load())
	}
}

func TestDownloadDoesNotMutateTheCallersPrompt(t *testing.T) {
	srv, _ := fileServer(t, "png bytes", "image/png")

	prompt := promptWithImage(t, srv.URL+"/a.png", "image/png")

	if _, err := resolveFileURLs(context.Background(), &urlModel{}, prompt, nil); err != nil {
		t.Fatal(err)
	}

	// The caller is still holding this conversation; rewriting it in place
	// would replace their URL with bytes behind their back.
	if _, ok := fileData(t, prompt).Data.(provider.FileDataURL); !ok {
		t.Error("the caller's prompt was rewritten in place")
	}
}

func TestDownloadFailureNamesTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	prompt := promptWithImage(t, srv.URL+"/missing.png", "image/png")

	_, err := resolveFileURLs(context.Background(), &urlModel{}, prompt, nil)
	if err == nil {
		t.Fatal("err = nil, want the fetch failure")
	}
	// The provider's own error would not mention the URL at all.
	if !strings.Contains(err.Error(), "missing.png") || !strings.Contains(err.Error(), "404") {
		t.Errorf("err = %v, want the URL and status", err)
	}
}

func TestRunDownloadsThroughTheAgentLoop(t *testing.T) {
	srv, hits := fileServer(t, "png bytes", "image/png")

	model := &urlModel{}
	u, err := url.Parse(srv.URL + "/a.png")
	if err != nil {
		t.Fatal(err)
	}

	_, err = GenerateText(context.Background(), Options{
		Model: model,
		Messages: []provider.Message{provider.UserMessage{Content: []provider.UserPart{
			provider.TextPart{Text: "what is this?"},
			provider.FilePart{Data: provider.FileDataURL{URL: u}, MediaType: "image"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if hits.Load() != 1 {
		t.Fatalf("requests = %d, want the loop to have fetched the image", hits.Load())
	}

	msg := model.seen[len(model.seen)-1].(provider.UserMessage)
	part := msg.Content[1].(provider.FilePart)
	if _, ok := part.Data.(provider.FileDataBytes); !ok {
		t.Errorf("the model was handed %T, want inline bytes", part.Data)
	}
	// The sibling text part has to survive the rewrite untouched.
	if text, ok := msg.Content[0].(provider.TextPart); !ok || text.Text != "what is this?" {
		t.Errorf("sibling part = %+v, want the original text", msg.Content[0])
	}
}

func TestDisableURLDownloadPassesTheURLThrough(t *testing.T) {
	srv, hits := fileServer(t, "png bytes", "image/png")

	model := &urlModel{}
	u, err := url.Parse(srv.URL + "/a.png")
	if err != nil {
		t.Fatal(err)
	}

	_, err = GenerateText(context.Background(), Options{
		Model:              model,
		DisableURLDownload: true,
		Messages: []provider.Message{provider.UserMessage{Content: []provider.UserPart{
			provider.FilePart{Data: provider.FileDataURL{URL: u}, MediaType: "image"},
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if hits.Load() != 0 {
		t.Errorf("requests = %d, want none when downloading is disabled", hits.Load())
	}
}

func TestOversizedDownloadIsRefused(t *testing.T) {
	// A body one byte over the cap. Truncating it silently would hand the
	// provider a corrupt file.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		chunk := make([]byte, 1<<20)
		for range (maxDownloadBytes >> 20) + 1 {
			w.Write(chunk)
		}
	}))
	t.Cleanup(srv.Close)

	prompt := promptWithImage(t, srv.URL+"/big.png", "image/png")

	_, err := resolveFileURLs(context.Background(), &urlModel{}, prompt, nil)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want the size limit to be reported", err)
	}
}

func TestMediaTypeMatches(t *testing.T) {
	cases := []struct {
		mediaType, key string
		want           bool
	}{
		{"image/png", "*/*", true},
		{"image/png", "image/*", true},
		{"image/png", "image/png", true},
		{"image/png", "image/jpeg", false},
		{"image/png", "application/pdf", false},
		// A part may carry just the top-level segment.
		{"image", "image/*", true},
		{"image", "image/png", true},
		{"application/pdf", "image/*", false},
	}

	for _, tc := range cases {
		if got := mediaTypeMatches(tc.mediaType, tc.key); got != tc.want {
			t.Errorf("mediaTypeMatches(%q, %q) = %v, want %v", tc.mediaType, tc.key, got, tc.want)
		}
	}
}
