package middleware

import (
	"context"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
	"github.com/notshekhar/pi/internal/modules/ai/providerutil"
)

// SimulateStreaming makes a model that cannot stream look as though it can, by
// running a normal call and replaying the finished result as stream parts.
//
// Nothing arrives sooner — the whole answer still lands at once — but a
// consumer written against the streaming API works unchanged, which is what
// lets a program keep one render path across every model it supports.
func SimulateStreaming() Middleware {
	return Middleware{
		Name: "simulate-streaming",

		WrapStream: func(ctx context.Context, _ Info, opts provider.CallOptions, next Next) (*provider.StreamResult, error) {
			res, err := next.Generate(ctx, opts)
			if err != nil {
				return nil, err
			}
			return replayAsStream(ctx, res), nil
		},
	}
}

// replayAsStream renders a finished result as the stream that would have
// produced it.
func replayAsStream(ctx context.Context, res *provider.GenerateResult) *provider.StreamResult {
	out := make(chan provider.StreamPart, streamBufferSize)

	go func() {
		defer close(out)

		emit := func(part provider.StreamPart) bool {
			select {
			case out <- part:
				return true
			case <-ctx.Done():
				return false
			}
		}

		if !emit(provider.StreamStart{Warnings: res.Warnings}) {
			return
		}

		for _, c := range res.Content {
			switch v := c.(type) {
			case provider.Text:
				id := providerutil.GenerateID("text", 12)
				if !emit(provider.TextStart{ID: id}) ||
					!emit(provider.TextDelta{ID: id, Delta: v.Text}) ||
					!emit(provider.TextEnd{ID: id, ProviderMetadata: v.ProviderMetadata}) {
					return
				}

			case provider.Reasoning:
				id := providerutil.GenerateID("reasoning", 12)
				if !emit(provider.ReasoningStart{ID: id}) ||
					!emit(provider.ReasoningDelta{ID: id, Delta: v.Text}) ||
					!emit(provider.ReasoningEnd{ID: id, ProviderMetadata: v.ProviderMetadata}) {
					return
				}

			default:
				// Tool calls, files and sources are already whole, and double
				// as stream parts, so they pass through untouched.
				if part, ok := c.(provider.StreamPart); ok && !emit(part) {
					return
				}
			}
		}

		emit(provider.Finish{
			Usage:            res.Usage,
			FinishReason:     res.FinishReason,
			ProviderMetadata: res.ProviderMetadata,
		})
	}()

	return &provider.StreamResult{
		Stream:   out,
		Request:  res.Request,
		Response: res.Response,
	}
}
