package openaicompat

import (
	"context"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providertest"
)

// A stream that drops at any point still keeps the part contract.
func TestStreamCutAnywhere(t *testing.T) {
	for _, body := range []string{textChunks, toolChunks, parallelToolChunks, reasoningChunks} {
		for _, cut := range providertest.Cuts(body) {
			p, _ := newTestProvider(t, cut)
			res, err := p.LanguageModel("gpt-x").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
			if err != nil {
				t.Fatal(err)
			}
			collect(t, res)
		}
	}
}
