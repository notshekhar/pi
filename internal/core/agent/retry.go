package agent

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/ai/providerutil"
)

// Loop's retry.ts: when a broken stream is worth picking back up.
//
// The provider retries the *request* that opens a stream. Nothing retried a
// stream that died once it was running — so an overload or a dropped socket
// at step 18 of 20 ended the turn, though every finished step was already
// persisted and billed. RunTurn uses this to open a fresh stream over the
// same conversation and carry on.

// DefaultMaxStreamResumes is how many times one turn may reopen its stream.
const DefaultMaxStreamResumes = 2

// resumeBaseDelay is the first backoff step (each resume doubles it), and
// resumeMaxDelay the ceiling past which waiting is worse than reporting. A
// var so tests need not sleep.
var (
	resumeBaseDelay = time.Second
	resumeMaxDelay  = 15 * time.Second
)

// retryableNetwork is transport failures in Go's wording and loop's (a
// gateway's error text often quotes the upstream's Node error).
var retryableNetwork = regexp.MustCompile(`(?i)\b(ECONNRESET|ETIMEDOUT|ECONNREFUSED|EPIPE|ENETDOWN|ENETUNREACH|EHOSTUNREACH|EAI_AGAIN|connection reset|broken pipe|connection refused|unexpected EOF|i/o timeout|TLS handshake timeout|server sent GOAWAY|stream error|INTERNAL_ERROR|socket hang up|fetch failed|network error|premature close|other side closed|use of closed network connection)\b`)

// retryableMessage is provider wording for "busy, try again". "try again
// LATER" only: a bare "try again" also appears in permanent errors ("reduce
// the length of the messages and try again").
var retryableMessage = regexp.MustCompile(`(?i)\b(overloaded(?:_error)?|rate.?limit(?:_error|ed)?|service unavailable|temporarily unavailable|try again later)\b`)

var retryableStatus = map[int]bool{408: true, 409: true, 425: true, 429: true, 500: true, 502: true, 503: true, 504: true, 529: true}

// IsRetryableStreamError is loop's isRetryableStreamError. Conservative by
// construction: an unrecognised error is NOT retryable, so a bad request, a
// bad key or an over-long context fails once and says so instead of being
// tried three times at full price.
func IsRetryableStreamError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	// The provider's verdict wins when the call got a response: it knows
	// something the status table does not.
	var apiErr *provider.APICallError
	if errors.As(err, &apiErr) && apiErr.StatusCode != 0 {
		return apiErr.IsRetryable || retryableStatus[apiErr.StatusCode]
	}
	// Nothing streamed before the connection closed: a fresh attempt loses
	// nothing.
	if errors.Is(err, providerutil.ErrStreamEndedEarly) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	for _, errno := range []syscall.Errno{syscall.ECONNRESET, syscall.EPIPE, syscall.ETIMEDOUT, syscall.ECONNREFUSED, syscall.ENETUNREACH, syscall.EHOSTUNREACH} {
		if errors.Is(err, errno) {
			return true
		}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	text := err.Error()
	if apiErr != nil {
		text += " " + apiErr.ResponseBody
		if s, ok := apiErr.Data.(string); ok {
			text += " " + s
		}
	}
	return retryableNetwork.MatchString(text) || retryableMessage.MatchString(text)
}

// resumeDelay is the backoff before resume attempt (0-based), with full
// jitter: a provider-wide overload means every client saw it at once, and a
// fixed backoff walks them all back in step.
func resumeDelay(attempt int) time.Duration {
	ceiling := min(resumeBaseDelay<<attempt, resumeMaxDelay)
	return ceiling/2 + time.Duration(rand.Int64N(int64(ceiling/2)+1))
}

// waitOrDone waits d, or less if ctx ends first.
func waitOrDone(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
	case <-ctx.Done():
	}
}

// describeRetry is a one-line reason for the UI, so a retry is never silent.
func describeRetry(err error) string {
	text := strings.Join(strings.Fields(err.Error()), " ")
	if text == "" {
		return "stream failed"
	}
	if r := []rune(text); len(r) > 120 {
		return string(r[:117]) + "…"
	}
	return text
}

// contextOverflow is provider wording for "this request is bigger than I
// take" — a context-window overflow or a body-size limit, one problem for a
// caller that can shrink its request (loop's CONTEXT_OVERFLOW_MESSAGE).
var contextOverflow = regexp.MustCompile(`(?i)context.?(length|window)|maximum context|too many (input )?tokens|prompt is too long|input is too long|request entity too large|payload too large|body (is )?too (large|big)|request too large|reduce the length`)

// isContextOverflow is loop's isContextOverflowError: did the provider
// refuse the request for its size?
func isContextOverflow(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *provider.APICallError
	text := err.Error()
	if errors.As(err, &apiErr) {
		if apiErr.StatusCode == 413 {
			return true
		}
		text += " " + apiErr.ResponseBody
	}
	return contextOverflow.MatchString(text)
}

// keepSteps is what a resumed turn carries forward from the attempt that
// failed: the steps it finished and their usage.
func keepSteps(res *ai.Result) ([]ai.Step, provider.Usage) {
	if res == nil {
		return nil, provider.Usage{}
	}
	return res.Steps, res.Usage
}

// withEarlierSteps makes an attempt's result the whole turn's: the steps and
// usage of the attempts before it come first, so a resumed turn reports
// everything it ran.
func withEarlierSteps(res *ai.Result, steps []ai.Step, usage provider.Usage) *ai.Result {
	if len(steps) == 0 {
		return res
	}
	if res == nil {
		res = &ai.Result{}
	}
	out := *res
	out.Steps = append(append([]ai.Step{}, steps...), res.Steps...)
	out.Usage = sumProviderUsage(usage, res.Usage)
	return &out
}

func sumProviderUsage(a, b provider.Usage) provider.Usage {
	add := func(x, y *int64) *int64 {
		if x == nil && y == nil {
			return nil
		}
		v := deref(x) + deref(y)
		return &v
	}
	out := b
	out.InputTokens.Total = add(a.InputTokens.Total, b.InputTokens.Total)
	out.InputTokens.NoCache = add(a.InputTokens.NoCache, b.InputTokens.NoCache)
	out.InputTokens.CacheRead = add(a.InputTokens.CacheRead, b.InputTokens.CacheRead)
	out.InputTokens.CacheWrite = add(a.InputTokens.CacheWrite, b.InputTokens.CacheWrite)
	out.OutputTokens.Total = add(a.OutputTokens.Total, b.OutputTokens.Total)
	out.OutputTokens.Text = add(a.OutputTokens.Text, b.OutputTokens.Text)
	out.OutputTokens.Reasoning = add(a.OutputTokens.Reasoning, b.OutputTokens.Reasoning)
	return out
}
