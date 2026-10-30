package google

import (
	"context"
	"testing"

	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providertest"
)

// A stream that drops at any point still keeps the part contract.
func TestStreamCutAnywhere(t *testing.T) {
	for _, body := range []string{textStream, toolStream, thinkingStream, codeExecutionStream} {
		for _, cut := range providertest.Cuts(body) {
			p, _, _ := newTestProvider(t, cut)
			res, err := p.LanguageModel("gemini-3-pro").DoStream(context.Background(), provider.CallOptions{Prompt: userPrompt("hi")})
			if err != nil {
				t.Fatal(err)
			}
			collect(t, res)
		}
	}
}
