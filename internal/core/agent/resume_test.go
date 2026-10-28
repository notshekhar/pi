package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
	"github.com/notshekhar/pi/internal/core/session"
	"github.com/notshekhar/pi/internal/core/tools"
)

const overloaded = `event: error
data: {"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}

`

// textThenOverloaded streams some text, then dies.
var textThenOverloaded = textStep[:strings.Index(textStep, "event: content_block_stop")] + overloaded

type turnProbe struct {
	mu     sync.Mutex
	events []string
	taps   []ai.StreamPart
}

func (p *turnProbe) emit(name string, _ any) {
	p.mu.Lock()
	p.events = append(p.events, name)
	p.mu.Unlock()
}

func (p *turnProbe) tap(part ai.StreamPart) {
	p.mu.Lock()
	p.taps = append(p.taps, part)
	p.mu.Unlock()
}

func (p *turnProbe) count(name string) int {
	n := 0
	for _, e := range p.events {
		if e == name {
			n++
		}
	}
	return n
}

func (p *turnProbe) tappedErrors() int {
	n := 0
	for _, part := range p.taps {
		if _, ok := part.(provider.ErrorPart); ok {
			n++
		}
	}
	return n
}

func newTurn(t *testing.T, script ...string) (*Run, *gateway, *turnProbe) {
	t.Helper()
	g := &gateway{script: script}
	cfg := withGateway(t, "anthropic", g)
	cfg.CWD = t.TempDir()
	if err := config.SetSetting("autoCompactThreshold", 0); err != nil {
		t.Fatal(err)
	}
	if err := config.SetSetting("idleCompact", false); err != nil {
		t.Fatal(err)
	}
	old := resumeBaseDelay
	resumeBaseDelay = time.Millisecond
	t.Cleanup(func() { resumeBaseDelay = old })
	r := &Run{Config: cfg, Session: session.New(cfg.FullID(), cfg.CWD), Tools: &tools.Context{CWD: cfg.CWD}}
	return r, g, &turnProbe{}
}

// An overload that kills the stream between steps reopens it over what the
// finished step persisted, and the turn completes; the renderer never sees
// the recovered error.
func TestTurnResumesAfterATransientFailureBetweenSteps(t *testing.T) {
	r, g, probe := newTurn(t, toolStep, overloaded, textStep)
	final, err := r.RunTurn(context.Background(), TurnOptions{Input: "go", Emit: probe.emit, Tap: probe.tap})
	if err != nil {
		t.Fatalf("turn failed: %v (events %v)", err, probe.events)
	}
	if len(g.bodies) != 3 || probe.count("stream-retry") != 1 || probe.count("error") != 0 {
		t.Fatalf("requests %d, events %v", len(g.bodies), probe.events)
	}
	if probe.tappedErrors() != 0 {
		t.Error("the recovered error reached the renderer")
	}
	if final == nil || len(final.Steps) != 2 || final.Text != "done" {
		t.Fatalf("final = %+v, want both steps and the answer", final)
	}
	// The resumed request carries the finished step, not a repeat of it,
	// and keeps the cached prefix of the first.
	if n := len(g.bodies[2]["messages"].([]any)); n != 3 {
		t.Errorf("resumed request sends %d messages, want 3 (prompt, tool call, result)", n)
	}
	if bp := breakpoints(g.bodies[2]); len(bp) != 2 {
		t.Errorf("resumed request breakpoints = %v", bp)
	}
	if r.Session.Chars() == 0 {
		t.Error("nothing persisted")
	}
}

// With a step's text already on screen, a resume would make the model say
// it twice: the failure is reported instead.
func TestTurnDoesNotResumeAStepAlreadyStreaming(t *testing.T) {
	r, g, probe := newTurn(t, textThenOverloaded, textStep)
	_, err := r.RunTurn(context.Background(), TurnOptions{Input: "go", Emit: probe.emit, Tap: probe.tap})
	if err == nil || len(g.bodies) != 1 || probe.count("stream-retry") != 0 {
		t.Fatalf("err %v, requests %d, events %v", err, len(g.bodies), probe.events)
	}
	if probe.tappedErrors() == 0 || probe.count("error") != 1 {
		t.Errorf("the failure was not reported: events %v", probe.events)
	}
}

// A permanent failure is reported once, never retried.
func TestTurnDoesNotRetryAPermanentFailure(t *testing.T) {
	r, g, probe := newTurn(t, `HTTP 401 {"type":"error","error":{"type":"authentication_error","message":"invalid x-api-key"}}`)
	if _, err := r.RunTurn(context.Background(), TurnOptions{Input: "go", Emit: probe.emit, Tap: probe.tap}); err == nil {
		t.Fatal("a bad key succeeded")
	}
	if len(g.bodies) != 1 || probe.count("stream-retry") != 0 {
		t.Fatalf("requests %d, events %v", len(g.bodies), probe.events)
	}
}

// A request refused for its size is compacted and retried once.
func TestTurnCompactsAndRetriesAnOverflow(t *testing.T) {
	tooLong := `HTTP 400 {"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 250000 tokens > 200000 maximum"}}`
	r, g, probe := newTurn(t, textStep, textStep, textStep, tooLong)
	for i := 0; i < 3; i++ {
		if _, err := r.RunTurn(context.Background(), TurnOptions{Input: strings.Repeat("history ", 400)}); err != nil {
			t.Fatal(err)
		}
	}
	final, err := r.RunTurn(context.Background(), TurnOptions{Input: "next", Emit: probe.emit, Tap: probe.tap})
	if err != nil {
		t.Fatalf("turn failed: %v (events %v)", err, probe.events)
	}
	if probe.count("compact-start") != 1 || probe.count("stream-retry") != 0 || final.Text != "done" {
		t.Fatalf("events %v, text %q", probe.events, final.Text)
	}
	if _, ok := r.Session.LatestCompact(); !ok {
		t.Error("no compaction saved")
	}
	_ = g
}

func TestRetryClassification(t *testing.T) {
	for _, c := range []struct {
		err  error
		want bool
	}{
		{&provider.APICallError{Message: "Overloaded", StatusCode: 529, IsRetryable: true}, true},
		{&provider.APICallError{Message: "bad", StatusCode: 400}, false},
		{&provider.APICallError{Message: "busy", StatusCode: 503}, true},
		{&provider.APICallError{Message: "dial", Cause: context.DeadlineExceeded}, true}, // a transport timeout
		{errString("read tcp 1.2.3.4:443: read: connection reset by peer"), true},
		{errString("unexpected EOF"), true},
		{errString("please reduce the length of the messages and try again"), false},
		{context.Canceled, false},
	} {
		if got := IsRetryableStreamError(c.err); got != c.want {
			t.Errorf("%v: retryable = %v, want %v", c.err, got, c.want)
		}
	}
	if !isContextOverflow(&provider.APICallError{Message: "x", StatusCode: 413}) ||
		!isContextOverflow(errString("prompt is too long: 250000 tokens")) ||
		isContextOverflow(errString("read 4130 bytes")) {
		t.Error("overflow classification")
	}
}

type errString string

func (e errString) Error() string { return string(e) }
