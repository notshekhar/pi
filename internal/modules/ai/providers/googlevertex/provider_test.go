package googlevertex

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

const emptyStream = `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1}}

`

func TestVertexRequestPathAndAuth(t *testing.T) {
	var path, auth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.RequestURI()
		auth = r.Header.Get("Authorization")
		io.WriteString(w, emptyStream)
	}))
	defer srv.Close()

	p := New(Options{
		Project:  "my-project",
		Location: "us-central1",
		BaseURL:  srv.URL,
		// A supplied token source keeps the test off Application Default
		// Credentials, which are machine-specific.
		TokenSource: func(context.Context) (string, error) { return "tok-123", nil },
	})

	res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	want := "/v1/projects/my-project/locations/us-central1/publishers/google/models/gemini-3-pro:streamGenerateContent?alt=sse"
	if path != want {
		t.Errorf("path  = %q\nwant = %q", path, want)
	}
	if auth != "Bearer tok-123" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestVertexStripsModelsPrefix(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		io.WriteString(w, emptyStream)
	}))
	defer srv.Close()

	p := New(Options{
		Project: "p", Location: "global", BaseURL: srv.URL,
		TokenSource: func(context.Context) (string, error) { return "t", nil },
	})

	// Vertex puts the bare id after publishers/google/models/, so a
	// "models/" prefix carried over from the other API must be dropped.
	res, err := p.LanguageModel("models/gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for range res.Stream {
	}

	if strings.Contains(path, "models/models/") {
		t.Errorf("path = %q, want the models/ prefix stripped", path)
	}
	if !strings.HasSuffix(path, "publishers/google/models/gemini-3-pro:streamGenerateContent") {
		t.Errorf("path = %q", path)
	}
}

func TestMissingProjectIsReportedClearly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, emptyStream)
	}))
	defer srv.Close()

	t.Setenv("GOOGLE_CLOUD_PROJECT", "")
	t.Setenv("GOOGLE_VERTEX_PROJECT", "")
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", "")

	p := New(Options{
		BaseURL:     srv.URL,
		TokenSource: func(context.Context) (string, error) { return "t", nil },
	})

	_, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{
		Prompt: provider.Prompt{
			provider.UserMessage{Content: []provider.UserPart{provider.TextPart{Text: "hi"}}},
		},
	})
	if err == nil {
		t.Skip("a project was resolved from ambient credentials on this machine")
	}
	if !strings.Contains(err.Error(), "project") {
		t.Errorf("error = %v, want it to name the missing project", err)
	}
}

func TestDefaultBaseURLByLocation(t *testing.T) {
	cases := map[string]string{
		"global":       "https://aiplatform.googleapis.com",
		"us-central1":  "https://us-central1-aiplatform.googleapis.com",
		"europe-west4": "https://europe-west4-aiplatform.googleapis.com",
	}
	for location, want := range cases {
		if got := defaultBaseURL(location); got != want {
			t.Errorf("%s: got %q, want %q", location, got, want)
		}
	}
}
