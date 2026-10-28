// reasoning-probe streams one prompt through the ai layer and reports how the
// reasoning arrived: how many deltas, when the first and last landed, and
// whether it was spread over time (streamed) or dumped in one piece.
//
//	go run ./scripts/reasoning-probe -model xai/grok-4.7 -reasoning medium
//	go run ./scripts/reasoning-probe -model anthropic/claude-sonnet-4-6 -v
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	ai "github.com/notshekhar/pi/internal/ai"
	"github.com/notshekhar/pi/internal/ai/provider"
	"github.com/notshekhar/pi/internal/core/config"
)

func main() {
	model := flag.String("model", "xai/grok-4.7", "provider/model")
	effort := flag.String("reasoning", "medium", "reasoning effort")
	prompt := flag.String("prompt", "How many prime numbers are there below 60? Think it through, then answer in one line.", "prompt")
	verbose := flag.Bool("v", false, "print every delta as it arrives")
	flag.Parse()

	cfg, err := config.Resolve(config.Config{ModelID: *model, Reasoning: provider.ReasoningEffort(*effort)})
	if err != nil {
		fail(err)
	}
	lm, err := config.LanguageModel(cfg)
	if err != nil {
		fail(err)
	}
	fmt.Printf("model %s  reasoning %s\n", cfg.FullID(), cfg.Reasoning)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	start := time.Now()
	res, err := ai.StreamText(ctx, ai.Options{
		Model:     lm,
		Messages:  []provider.Message{ai.UserText(*prompt)},
		Reasoning: cfg.Reasoning,
	})
	if err != nil {
		fail(err)
	}

	type stat struct {
		n           int
		bytes       int
		first, last time.Duration
		gaps        []time.Duration
	}
	var reasoning, text stat
	note := func(s *stat, delta string) {
		at := time.Since(start)
		if s.n == 0 {
			s.first = at
		} else {
			s.gaps = append(s.gaps, at-s.last)
		}
		s.last = at
		s.n++
		s.bytes += len(delta)
	}
	var reasoningText strings.Builder
	for part := range res.Stream {
		switch p := part.(type) {
		case provider.ReasoningStart:
			if *verbose {
				fmt.Printf("%7.3fs reasoning-start\n", time.Since(start).Seconds())
			}
		case provider.ReasoningDelta:
			note(&reasoning, p.Delta)
			reasoningText.WriteString(p.Delta)
			if *verbose {
				fmt.Printf("%7.3fs R %q\n", time.Since(start).Seconds(), p.Delta)
			}
		case provider.TextDelta:
			note(&text, p.Delta)
			if *verbose {
				fmt.Printf("%7.3fs T %q\n", time.Since(start).Seconds(), p.Delta)
			}
		case provider.ErrorPart:
			fmt.Printf("stream error: %v\n", p.Err)
		}
	}
	if _, err := res.Final(); err != nil {
		fmt.Println("error:", err)
	}

	report := func(name string, s stat) {
		if s.n == 0 {
			fmt.Printf("%-9s none\n", name)
			return
		}
		spread := s.last - s.first
		fmt.Printf("%-9s %4d deltas  %6d bytes  first %6.2fs  last %6.2fs  spread %6.2fs",
			name, s.n, s.bytes, s.first.Seconds(), s.last.Seconds(), spread.Seconds())
		switch {
		case s.n == 1:
			fmt.Print("  → ONE chunk (not streamed)")
		case spread < 50*time.Millisecond:
			fmt.Print("  → burst (arrived all at once)")
		default:
			fmt.Print("  → streamed")
		}
		fmt.Println()
	}
	report("reasoning", reasoning)
	report("text", text)
	if reasoning.n > 0 && text.n > 0 {
		fmt.Printf("reasoning finished %.2fs before the first text delta\n", (text.first - reasoning.last).Seconds())
	}
	if r := reasoningText.String(); r != "" {
		fmt.Printf("reasoning preview: %q\n", clip(r, 160))
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
