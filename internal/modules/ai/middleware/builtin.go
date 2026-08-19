package middleware

import (
	"context"
	"log/slog"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Settings are call options applied when the caller left them unset. A nil
// field changes nothing.
type Settings struct {
	MaxOutputTokens *int64
	Temperature     *float64
	TopP            *float64
	TopK            *int64
	Reasoning       provider.ReasoningEffort
	Headers         provider.Headers
	ProviderOptions provider.ProviderOptions
}

// DefaultSettings fills in options the caller did not set, which is how a
// program applies house defaults without threading them through every call.
//
// It never overrides an explicit value, so a caller asking for temperature 0
// gets 0 rather than the default.
func DefaultSettings(s Settings) Middleware {
	return Middleware{
		Name: "default-settings",
		TransformOptions: func(_ context.Context, _ Info, opts provider.CallOptions) (provider.CallOptions, error) {
			if opts.MaxOutputTokens == nil {
				opts.MaxOutputTokens = s.MaxOutputTokens
			}
			if opts.Temperature == nil {
				opts.Temperature = s.Temperature
			}
			if opts.TopP == nil {
				opts.TopP = s.TopP
			}
			if opts.TopK == nil {
				opts.TopK = s.TopK
			}
			if opts.Reasoning == "" && s.Reasoning != "" {
				opts.Reasoning = s.Reasoning
			}

			opts.Headers = mergeHeaders(s.Headers, opts.Headers)
			opts.ProviderOptions = mergeProviderOptions(s.ProviderOptions, opts.ProviderOptions)
			return opts, nil
		},
	}
}

// mergeHeaders combines defaults with per-call headers, the call winning. The
// result is a fresh map: editing the caller's would leak across calls.
func mergeHeaders(defaults, call provider.Headers) provider.Headers {
	if len(defaults) == 0 {
		return call
	}
	merged := make(provider.Headers, len(defaults)+len(call))
	for k, v := range defaults {
		merged[k] = v
	}
	for k, v := range call {
		merged[k] = v
	}
	return merged
}

// mergeProviderOptions combines defaults with per-call provider options, per
// field, so a call setting one field of a block keeps the defaults for the
// rest.
func mergeProviderOptions(defaults, call provider.ProviderOptions) provider.ProviderOptions {
	if len(defaults) == 0 {
		return call
	}
	merged := make(provider.ProviderOptions, len(defaults)+len(call))
	for providerKey, block := range defaults {
		copied := make(provider.JSONObject, len(block))
		for k, v := range block {
			copied[k] = v
		}
		merged[providerKey] = copied
	}
	for providerKey, block := range call {
		existing, ok := merged[providerKey]
		if !ok {
			existing = provider.JSONObject{}
			merged[providerKey] = existing
		}
		for k, v := range block {
			existing[k] = v
		}
	}
	return merged
}

// Record is one completed model call, as reported to an Observe callback.
type Record struct {
	Provider string
	ModelID  string

	// Stream reports whether the call was streaming. For a streaming call the
	// record arrives when the stream ends, not when it starts.
	Stream bool

	// Duration is wall time for the whole call, including the body read on a
	// streaming call.
	Duration time.Duration

	Usage        provider.Usage
	FinishReason provider.FinishReason

	// Err is set when the call failed. Usage is whatever was reported before
	// the failure, which is usually nothing.
	Err error
}

// Observe reports every completed call to fn, which is the hook for cost
// tracking and metrics.
//
// fn runs on the goroutine that finished the call, so a slow callback slows
// the run. Hand off to a channel if the work is not trivial.
func Observe(fn func(Record)) Middleware {
	return Middleware{
		Name: "observe",

		WrapGenerate: func(ctx context.Context, info Info, opts provider.CallOptions, next Next) (*provider.GenerateResult, error) {
			start := time.Now()
			res, err := next.Generate(ctx, opts)

			record := Record{
				Provider: info.Provider,
				ModelID:  info.ModelID,
				Duration: time.Since(start),
				Err:      err,
			}
			if res != nil {
				record.Usage = res.Usage
				record.FinishReason = res.FinishReason
			}
			fn(record)

			return res, err
		},

		WrapStream: func(ctx context.Context, info Info, opts provider.CallOptions, next Next) (*provider.StreamResult, error) {
			start := time.Now()
			res, err := next.Stream(ctx, opts)
			if err != nil {
				fn(Record{
					Provider: info.Provider,
					ModelID:  info.ModelID,
					Stream:   true,
					Duration: time.Since(start),
					Err:      err,
				})
				return nil, err
			}

			// The totals only exist once the stream ends, so the record is
			// assembled from the Finish part on the way past.
			out := make(chan provider.StreamPart, streamBufferSize)
			source := res.Stream

			go func() {
				defer close(out)

				record := Record{
					Provider: info.Provider,
					ModelID:  info.ModelID,
					Stream:   true,
				}

				for part := range source {
					switch v := part.(type) {
					case provider.Finish:
						record.Usage = v.Usage
						record.FinishReason = v.FinishReason
					case provider.ErrorPart:
						if record.Err == nil {
							record.Err = v.Err
						}
					}

					select {
					case out <- part:
					case <-ctx.Done():
						record.Err = ctx.Err()
						record.Duration = time.Since(start)
						fn(record)
						return
					}
				}

				record.Duration = time.Since(start)
				fn(record)
			}()

			forwarded := *res
			forwarded.Stream = out
			return &forwarded, nil
		},
	}
}

// streamBufferSize decouples a middleware's forwarding goroutine from its
// consumer, matching the buffer the providers use.
const streamBufferSize = 64

// Logging logs every call at debug level and every failure at error level,
// through the standard library's structured logger.
//
// A nil logger uses slog.Default().
func Logging(logger *slog.Logger) Middleware {
	if logger == nil {
		logger = slog.Default()
	}

	log := func(ctx context.Context, r Record) {
		attrs := []any{
			"provider", r.Provider,
			"model", r.ModelID,
			"stream", r.Stream,
			"duration", r.Duration,
		}
		if in := r.Usage.InputTokens.Total; in != nil {
			attrs = append(attrs, "input_tokens", *in)
		}
		if out := r.Usage.OutputTokens.Total; out != nil {
			attrs = append(attrs, "output_tokens", *out)
		}
		if r.FinishReason.Unified != "" {
			attrs = append(attrs, "finish_reason", string(r.FinishReason.Unified))
		}

		if r.Err != nil {
			logger.ErrorContext(ctx, "model call failed", append(attrs, "error", r.Err)...)
			return
		}
		logger.DebugContext(ctx, "model call", attrs...)
	}

	// Logging is Observe with a fixed callback, so the two cannot drift. The
	// callback is built per call because it needs that call's context.
	return Middleware{
		Name: "logging",

		WrapGenerate: func(ctx context.Context, info Info, opts provider.CallOptions, next Next) (*provider.GenerateResult, error) {
			return Observe(func(r Record) { log(ctx, r) }).WrapGenerate(ctx, info, opts, next)
		},

		WrapStream: func(ctx context.Context, info Info, opts provider.CallOptions, next Next) (*provider.StreamResult, error) {
			return Observe(func(r Record) { log(ctx, r) }).WrapStream(ctx, info, opts, next)
		},
	}
}
