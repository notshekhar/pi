// Package registry resolves a model from a string like "anthropic:claude-opus-5".
//
// A CLI stores the user's model choice as text — in a config file, a flag, a
// session record — and has to turn it back into a model. Doing that with a
// switch statement means every caller knows every provider; a registry means
// only the wiring does.
//
//	r := registry.New()
//	r.Register("anthropic", registry.FromProvider(anthropic.New(anthropic.Options{})))
//	r.Register("google", registry.FromProvider(google.New(google.Options{})))
//
//	model, err := r.LanguageModel("anthropic:claude-opus-5")
package registry

import (
	"fmt"
	"sort"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// DefaultSeparator divides the provider id from the model id.
const DefaultSeparator = ":"

// Provider builds models of each kind. A provider package implements as many
// of these as it supports; the registry reports a usable error for the rest.
type Provider interface {
	// LanguageModel returns a language model, or an error the registry passes
	// through unchanged.
	LanguageModel(modelID string) (provider.LanguageModel, error)

	// EmbeddingModel returns an embedding model.
	EmbeddingModel(modelID string) (provider.EmbeddingModel, error)

	// ImageModel returns an image model.
	ImageModel(modelID string) (provider.ImageModel, error)
}

// Registry resolves "provider:model" strings.
//
// A Registry is read-only once built. Register from one goroutine during
// start-up, then resolve from as many as needed.
type Registry struct {
	separator string
	providers map[string]Provider
	// order preserves registration order, so error messages list providers
	// the way the program declared them.
	order []string
}

// Option configures a Registry.
type Option func(*Registry)

// WithSeparator changes the character between the provider and model ids.
//
// Worth changing when model ids contain a colon, as some gateways' do:
// registry.New(registry.WithSeparator(" > ")).
func WithSeparator(separator string) Option {
	return func(r *Registry) { r.separator = separator }
}

// New returns an empty Registry.
func New(opts ...Option) *Registry {
	r := &Registry{separator: DefaultSeparator, providers: map[string]Provider{}}
	for _, opt := range opts {
		opt(r)
	}
	return r
}

// Register adds a provider under an id. Registering the same id twice replaces
// the first, which is what lets a config file override a built-in default.
func (r *Registry) Register(id string, p Provider) {
	if _, exists := r.providers[id]; !exists {
		r.order = append(r.order, id)
	}
	r.providers[id] = p
}

// Providers lists the registered ids in registration order.
func (r *Registry) Providers() []string {
	out := make([]string, len(r.order))
	copy(out, r.order)
	return out
}

// ErrNoSuchProvider reports a model string naming a provider that was never
// registered. It carries the available ids, since the usual cause is a typo or
// a missing Register call.
type ErrNoSuchProvider struct {
	ProviderID string
	Available  []string
}

func (e *ErrNoSuchProvider) Error() string {
	if len(e.Available) == 0 {
		return fmt.Sprintf("registry: no provider %q is registered, and the registry is empty", e.ProviderID)
	}
	return fmt.Sprintf("registry: no provider %q is registered; available: %s",
		e.ProviderID, strings.Join(e.Available, ", "))
}

// ErrInvalidModelID reports a string that is not "provider<sep>model".
type ErrInvalidModelID struct {
	ModelID   string
	Separator string
}

func (e *ErrInvalidModelID) Error() string {
	return fmt.Sprintf("registry: %q is not a valid model id; expected \"provider%smodel\"",
		e.ModelID, e.Separator)
}

// split divides a model string into its provider and model parts.
func (r *Registry) split(id string) (Provider, string, error) {
	// Cut at the first separator: model ids routinely contain one themselves
	// ("openai/gpt-5" on a gateway), while provider ids do not.
	providerID, modelID, found := strings.Cut(id, r.separator)
	if !found || providerID == "" || modelID == "" {
		return nil, "", &ErrInvalidModelID{ModelID: id, Separator: r.separator}
	}

	p, ok := r.providers[providerID]
	if !ok {
		available := r.Providers()
		sort.Strings(available)
		return nil, "", &ErrNoSuchProvider{ProviderID: providerID, Available: available}
	}
	return p, modelID, nil
}

// LanguageModel resolves a language model.
func (r *Registry) LanguageModel(id string) (provider.LanguageModel, error) {
	p, modelID, err := r.split(id)
	if err != nil {
		return nil, err
	}
	return p.LanguageModel(modelID)
}

// EmbeddingModel resolves an embedding model.
func (r *Registry) EmbeddingModel(id string) (provider.EmbeddingModel, error) {
	p, modelID, err := r.split(id)
	if err != nil {
		return nil, err
	}
	return p.EmbeddingModel(modelID)
}

// ImageModel resolves an image model.
func (r *Registry) ImageModel(id string) (provider.ImageModel, error) {
	p, modelID, err := r.split(id)
	if err != nil {
		return nil, err
	}
	return p.ImageModel(modelID)
}
