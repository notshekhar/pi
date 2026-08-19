package registry_test

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/registry"
)

// fakeClient looks like a provider package's client: it returns models
// directly and implements only some of the model kinds.
type fakeClient struct {
	name      string
	noEmbed   bool
	lastModel string
}

func (c *fakeClient) Name() string { return c.name }

func (c *fakeClient) LanguageModel(modelID string) provider.LanguageModel {
	c.lastModel = modelID
	return &fakeModel{provider: c.name, id: modelID}
}

func (c *fakeClient) EmbeddingModel(modelID string) provider.EmbeddingModel {
	if c.noEmbed {
		panic("EmbeddingModel should not have been called")
	}
	c.lastModel = modelID
	return &fakeEmbedder{provider: c.name, id: modelID}
}

type fakeModel struct {
	provider string
	id       string
}

func (m *fakeModel) SpecificationVersion() string { return provider.SpecificationVersion }
func (m *fakeModel) Provider() string             { return m.provider }
func (m *fakeModel) ModelID() string              { return m.id }

func (m *fakeModel) SupportedURLs(context.Context) (map[string][]*regexp.Regexp, error) {
	return nil, nil
}

func (m *fakeModel) DoGenerate(context.Context, provider.CallOptions) (*provider.GenerateResult, error) {
	return &provider.GenerateResult{}, nil
}

func (m *fakeModel) DoStream(context.Context, provider.CallOptions) (*provider.StreamResult, error) {
	ch := make(chan provider.StreamPart)
	close(ch)
	return &provider.StreamResult{Stream: ch}, nil
}

type fakeEmbedder struct {
	provider string
	id       string
}

func (e *fakeEmbedder) SpecificationVersion() string {
	return provider.EmbeddingSpecificationVersion
}
func (e *fakeEmbedder) Provider() string            { return e.provider }
func (e *fakeEmbedder) ModelID() string             { return e.id }
func (e *fakeEmbedder) MaxEmbeddingsPerCall() int   { return 10 }
func (e *fakeEmbedder) SupportsParallelCalls() bool { return true }

func (e *fakeEmbedder) DoEmbed(context.Context, provider.EmbeddingCallOptions) (*provider.EmbeddingResult, error) {
	return &provider.EmbeddingResult{}, nil
}

func TestResolvesProviderAndModel(t *testing.T) {
	client := &fakeClient{name: "anthropic"}

	r := registry.New()
	r.Register("anthropic", registry.FromProvider(client))

	model, err := r.LanguageModel("anthropic:claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	if model.Provider() != "anthropic" || model.ModelID() != "claude-opus-5" {
		t.Errorf("model = %s/%s", model.Provider(), model.ModelID())
	}
}

func TestModelIDMayContainTheSeparator(t *testing.T) {
	client := &fakeClient{name: "gateway"}

	r := registry.New()
	r.Register("gateway", registry.FromProvider(client))

	// Only the first separator divides; gateway model ids carry their own.
	model, err := r.LanguageModel("gateway:openai/gpt-5:latest")
	if err != nil {
		t.Fatal(err)
	}
	if model.ModelID() != "openai/gpt-5:latest" {
		t.Errorf("model id = %q, want the rest of the string", model.ModelID())
	}
}

func TestUnknownProviderListsWhatIsAvailable(t *testing.T) {
	r := registry.New()
	r.Register("anthropic", registry.FromProvider(&fakeClient{name: "anthropic"}))
	r.Register("google", registry.FromProvider(&fakeClient{name: "google"}))

	_, err := r.LanguageModel("openai:gpt-5")

	var notFound *registry.ErrNoSuchProvider
	if !errors.As(err, &notFound) {
		t.Fatalf("err = %v, want ErrNoSuchProvider", err)
	}
	// The usual cause is a typo or a missing Register call, so the message has
	// to say what was registered.
	if !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "google") {
		t.Errorf("err = %v, want the available providers listed", err)
	}
}

func TestMalformedModelIDIsReported(t *testing.T) {
	r := registry.New()
	r.Register("anthropic", registry.FromProvider(&fakeClient{name: "anthropic"}))

	for _, id := range []string{"claude-opus-5", "anthropic:", ":claude-opus-5", ""} {
		var invalid *registry.ErrInvalidModelID
		if _, err := r.LanguageModel(id); !errors.As(err, &invalid) {
			t.Errorf("LanguageModel(%q) err = %v, want ErrInvalidModelID", id, err)
		}
	}
}

func TestSeparatorIsConfigurable(t *testing.T) {
	r := registry.New(registry.WithSeparator(" > "))
	r.Register("anthropic", registry.FromProvider(&fakeClient{name: "anthropic"}))

	model, err := r.LanguageModel("anthropic > claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	if model.ModelID() != "claude-opus-5" {
		t.Errorf("model id = %q", model.ModelID())
	}
}

func TestMissingModelKindIsAnErrorNotANilModel(t *testing.T) {
	// A client offering only language models, which is anthropic's shape.
	r := registry.New()
	r.Register("anthropic", registry.FromProvider(struct {
		Named
		LanguageOnly
	}{Named{"anthropic"}, LanguageOnly{}}))

	_, err := r.EmbeddingModel("anthropic:whatever")
	if err == nil {
		t.Fatal("err = nil; a nil model would panic at the first call instead")
	}
	if !strings.Contains(err.Error(), "anthropic") || !strings.Contains(err.Error(), "embedding") {
		t.Errorf("err = %v, want the provider and kind named", err)
	}
}

// Named gives a client a reportable id.
type Named struct{ N string }

func (n Named) Name() string { return n.N }

// LanguageOnly offers language models and nothing else.
type LanguageOnly struct{}

func (LanguageOnly) LanguageModel(modelID string) provider.LanguageModel {
	return &fakeModel{provider: "anthropic", id: modelID}
}

func TestCustomProviderAliasesModels(t *testing.T) {
	client := &fakeClient{name: "anthropic"}

	r := registry.New()
	r.Register("house", registry.Custom(registry.Models{
		Language: map[string]provider.LanguageModel{
			"fast":  client.LanguageModel("claude-sonnet-5"),
			"smart": client.LanguageModel("claude-opus-5"),
		},
	}))

	fast, err := r.LanguageModel("house:fast")
	if err != nil {
		t.Fatal(err)
	}
	if fast.ModelID() != "claude-sonnet-5" {
		t.Errorf("fast = %q, want the aliased model", fast.ModelID())
	}

	// With no fallback the menu is fixed, and an unknown alias says what exists.
	_, err = r.LanguageModel("house:enormous")
	if err == nil || !strings.Contains(err.Error(), "fast") {
		t.Errorf("err = %v, want the available aliases listed", err)
	}
}

func TestCustomProviderFallsBackForUnknownIDs(t *testing.T) {
	client := &fakeClient{name: "anthropic"}

	r := registry.New()
	r.Register("house", registry.Custom(registry.Models{
		Language: map[string]provider.LanguageModel{
			"fast": client.LanguageModel("claude-sonnet-5"),
		},
		Fallback: registry.FromProvider(client),
	}))

	// An id that is not an alias passes through to the real provider, so
	// aliasing adds names without removing any.
	model, err := r.LanguageModel("house:claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	if model.ModelID() != "claude-opus-5" {
		t.Errorf("model = %q, want the fallback's", model.ModelID())
	}
}

func TestRegisteringTwiceReplaces(t *testing.T) {
	r := registry.New()
	r.Register("p", registry.FromProvider(&fakeClient{name: "first"}))
	r.Register("p", registry.FromProvider(&fakeClient{name: "second"}))

	model, err := r.LanguageModel("p:m")
	if err != nil {
		t.Fatal(err)
	}
	// Replacing is what lets a config file override a built-in default.
	if model.Provider() != "second" {
		t.Errorf("provider = %q, want the later registration", model.Provider())
	}
	if got := r.Providers(); len(got) != 1 {
		t.Errorf("providers = %v, want one entry", got)
	}
}

func TestEmbeddingModelResolves(t *testing.T) {
	r := registry.New()
	r.Register("google", registry.FromProvider(&fakeClient{name: "google"}))

	model, err := r.EmbeddingModel("google:gemini-embedding-001")
	if err != nil {
		t.Fatal(err)
	}
	if model.ModelID() != "gemini-embedding-001" {
		t.Errorf("model = %q", model.ModelID())
	}
}
