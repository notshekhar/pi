package registry

import (
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// The provider packages return models directly rather than with an error, and
// they implement different subsets: anthropic has no embeddings, deepseek has
// no images. These adapters bridge that without every provider package having
// to know the registry exists.

// languageModelSource is implemented by every provider package.
type languageModelSource interface {
	LanguageModel(modelID string) provider.LanguageModel
}

// embeddingModelSource is implemented by providers offering embeddings.
type embeddingModelSource interface {
	EmbeddingModel(modelID string) provider.EmbeddingModel
}

// imageModelSource is implemented by providers offering image generation.
type imageModelSource interface {
	ImageModel(modelID string) provider.ImageModel
}

// FromProvider adapts a provider package's client to the registry.
//
// The client is inspected for the model kinds it offers; asking for one it
// does not have is an error naming the provider rather than a nil model that
// panics at the first call.
//
//	r.Register("anthropic", registry.FromProvider(anthropic.New(anthropic.Options{})))
func FromProvider(client any) Provider {
	return &adapted{client: client}
}

// adapted wraps a provider package's client.
type adapted struct{ client any }

// name reports the provider's own id where it has one, for error messages.
func (a *adapted) name() string {
	if named, ok := a.client.(interface{ Name() string }); ok {
		return named.Name()
	}
	return fmt.Sprintf("%T", a.client)
}

// unsupported reports that this provider does not offer a kind of model.
func (a *adapted) unsupported(kind string) error {
	return fmt.Errorf("registry: provider %s does not offer %s models", a.name(), kind)
}

// LanguageModel implements Provider.
func (a *adapted) LanguageModel(modelID string) (provider.LanguageModel, error) {
	source, ok := a.client.(languageModelSource)
	if !ok {
		return nil, a.unsupported("language")
	}
	return source.LanguageModel(modelID), nil
}

// EmbeddingModel implements Provider.
func (a *adapted) EmbeddingModel(modelID string) (provider.EmbeddingModel, error) {
	source, ok := a.client.(embeddingModelSource)
	if !ok {
		return nil, a.unsupported("embedding")
	}
	return source.EmbeddingModel(modelID), nil
}

// ImageModel implements Provider.
func (a *adapted) ImageModel(modelID string) (provider.ImageModel, error) {
	source, ok := a.client.(imageModelSource)
	if !ok {
		return nil, a.unsupported("image")
	}
	return source.ImageModel(modelID), nil
}

// Custom builds a provider from explicit model sets, for aliasing and for
// overriding one model of an otherwise ordinary provider.
//
// This is how a program offers "fast" and "smart" as model names, or pins a
// name to a specific dated snapshot:
//
//	r.Register("house", registry.Custom(registry.Models{
//	    Language: map[string]provider.LanguageModel{
//	        "fast":  claude.LanguageModel("claude-sonnet-5"),
//	        "smart": claude.LanguageModel("claude-opus-5"),
//	    },
//	    Fallback: registry.FromProvider(claude),
//	}))
type Models struct {
	// Language, Embedding and Image map an alias to a model.
	Language  map[string]provider.LanguageModel
	Embedding map[string]provider.EmbeddingModel
	Image     map[string]provider.ImageModel

	// Fallback handles ids that match no alias. Nil makes an unknown id an
	// error, which is what a fixed menu of models wants.
	Fallback Provider
}

// Custom returns a Provider backed by explicit model sets.
func Custom(models Models) Provider { return &custom{models: models} }

// custom is a Provider built from maps of aliases.
type custom struct{ models Models }

// LanguageModel implements Provider.
func (c *custom) LanguageModel(modelID string) (provider.LanguageModel, error) {
	if model, ok := c.models.Language[modelID]; ok {
		return model, nil
	}
	if c.models.Fallback == nil {
		return nil, c.unknown("language", modelID, keysOf(c.models.Language))
	}
	return c.models.Fallback.LanguageModel(modelID)
}

// EmbeddingModel implements Provider.
func (c *custom) EmbeddingModel(modelID string) (provider.EmbeddingModel, error) {
	if model, ok := c.models.Embedding[modelID]; ok {
		return model, nil
	}
	if c.models.Fallback == nil {
		return nil, c.unknown("embedding", modelID, keysOf(c.models.Embedding))
	}
	return c.models.Fallback.EmbeddingModel(modelID)
}

// ImageModel implements Provider.
func (c *custom) ImageModel(modelID string) (provider.ImageModel, error) {
	if model, ok := c.models.Image[modelID]; ok {
		return model, nil
	}
	if c.models.Fallback == nil {
		return nil, c.unknown("image", modelID, keysOf(c.models.Image))
	}
	return c.models.Fallback.ImageModel(modelID)
}

// unknown reports an id matching no alias, listing what does exist: a fixed
// menu is usually short, and naming it is more useful than the id alone.
func (c *custom) unknown(kind, modelID string, available []string) error {
	if len(available) == 0 {
		return fmt.Errorf("registry: no %s model %q, and none are configured", kind, modelID)
	}
	return fmt.Errorf("registry: no %s model %q; available: %v", kind, modelID, available)
}

// keysOf lists a map's keys in sorted order, so an error message is stable.
func keysOf[T any](m map[string]T) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// sortStrings sorts in place. It is here rather than a sort import at each use
// site because the registry sorts in exactly two places.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
