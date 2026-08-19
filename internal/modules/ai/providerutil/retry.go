package providerutil

import (
	"context"
	"errors"
	"math"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// RetryConfig controls exponential backoff.
type RetryConfig struct {
	// MaxRetries is the number of retries after the first attempt. Zero means
	// a single attempt.
	MaxRetries int
	// InitialDelay is the wait before the first retry. Defaults to 2s.
	InitialDelay time.Duration
	// MaxDelay caps the backoff. Defaults to 60s.
	MaxDelay time.Duration
	// BackoffFactor multiplies the delay each attempt. Defaults to 2.
	BackoffFactor float64
	// Jitter randomises delays by up to +/-25% so that concurrent callers hit
	// a rate limit and then retry in a spread rather than in lockstep.
	// Defaults to true; set DisableJitter to turn it off.
	DisableJitter bool
}

// DefaultRetryConfig is the backoff used when a provider does not override it.
var DefaultRetryConfig = RetryConfig{
	MaxRetries:    2,
	InitialDelay:  2 * time.Second,
	MaxDelay:      60 * time.Second,
	BackoffFactor: 2,
}

// withDefaults fills unset fields.
func (c RetryConfig) withDefaults() RetryConfig {
	if c.InitialDelay <= 0 {
		c.InitialDelay = 2 * time.Second
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = 60 * time.Second
	}
	if c.BackoffFactor <= 0 {
		c.BackoffFactor = 2
	}
	return c
}

// Retry runs fn until it succeeds, its error is not retryable, or the budget
// is exhausted, and returns the result of the last attempt.
//
// A provider's Retry-After header takes precedence over the computed backoff:
// the server knows when it will accept traffic again, and ignoring it just
// burns the remaining attempts.
func Retry[T any](ctx context.Context, cfg RetryConfig, fn func(ctx context.Context) (T, error)) (T, error) {
	cfg = cfg.withDefaults()

	var zero T
	delay := cfg.InitialDelay

	for attempt := 0; ; attempt++ {
		result, err := fn(ctx)
		if err == nil {
			return result, nil
		}

		// A cancelled context is a decision, not a transient failure.
		if ctx.Err() != nil {
			return zero, err
		}
		if attempt >= cfg.MaxRetries || !isRetryable(err) {
			return zero, err
		}

		wait := delay
		if after, ok := retryAfter(err); ok {
			wait = after
		} else if !cfg.DisableJitter {
			wait = applyJitter(wait)
		}
		if wait > cfg.MaxDelay {
			wait = cfg.MaxDelay
		}

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}

		delay = time.Duration(math.Min(
			float64(delay)*cfg.BackoffFactor,
			float64(cfg.MaxDelay),
		))
	}
}

// isRetryable reports whether another attempt could plausibly succeed.
func isRetryable(err error) bool {
	var apiErr *provider.APICallError
	if errors.As(err, &apiErr) {
		return apiErr.IsRetryable
	}
	return false
}

// retryAfter reads the provider's Retry-After header, which may be either a
// delay in seconds or an HTTP date.
func retryAfter(err error) (time.Duration, bool) {
	var apiErr *provider.APICallError
	if !errors.As(err, &apiErr) {
		return 0, false
	}

	value := apiErr.ResponseHeaders["retry-after"]
	if value == "" {
		return 0, false
	}

	if secs, err := strconv.ParseFloat(value, 64); err == nil && secs >= 0 {
		return time.Duration(secs * float64(time.Second)), true
	}
	if t, err := http.ParseTime(value); err == nil {
		if d := time.Until(t); d > 0 {
			return d, true
		}
		// A past deadline means retry now.
		return 0, true
	}
	return 0, false
}

// applyJitter spreads a delay by up to +/-25%.
func applyJitter(d time.Duration) time.Duration {
	factor := 0.75 + rand.Float64()*0.5
	return time.Duration(float64(d) * factor)
}
