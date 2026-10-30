package openairesponses

import (
	"context"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providertest"
)

// A stream that drops at any point still keeps the part contract, in both
// dialects.
func TestStreamCutAnywhere(t *testing.T) {
	for _, c := range []struct {
		fixture string
		xai     bool
	}{
		{"openai-reasoning-encrypted-content.1.chunks.txt", false},
		{"xai-text-with-reasoning-streaming.1.chunks.txt", true},
	} {
		for _, cut := range providertest.Cuts(fixtureSSE(t, c.fixture)) {
			p, _ := serve(t, cut)
			p.xai = c.xai
			res, err := p.LanguageModel("m").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
			if err != nil {
				t.Fatal(err)
			}
			parts(t, res)
		}
	}
}
