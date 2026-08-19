package tui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai"
	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Printer renders a streaming turn to a writer for the one-shot `run` path.
//
// Deliberately plainer than the interactive transcript: this output is
// routinely piped, so it stays line-oriented with no cursor moves, no alt
// screen, and no animation. It still goes through the theme, and drops colour
// entirely when the destination is not a terminal.
type Printer struct {
	Out           io.Writer
	Theme         *Theme
	ShowReasoning bool
	// Color is off when writing to a pipe: escape codes in a redirected file
	// are noise, not styling.
	Color bool

	inReasoning bool
	md          *Markdown
}

// NewPrinter writes to stdout with the night theme, colouring only when
// stdout is a terminal.
func NewPrinter() *Printer {
	return &Printer{
		Out:           os.Stdout,
		Theme:         NewTheme(NightPalette),
		ShowReasoning: true,
		Color:         isTerminal(os.Stdout),
	}
}

// paint styles text unless colour is off.
func (p *Printer) paint(slot Slot, text string) string {
	if !p.Color {
		return text
	}
	return p.Theme.Fg(slot, text)
}

// Consume drains a stream and renders it.
//
// An ErrorPart is remembered but the channel is still emptied: abandoning it
// mid-run blocks the producer and leaks the provider connection.
func (p *Printer) Consume(stream <-chan provider.StreamPart) error {
	if p.Out == nil {
		p.Out = os.Stdout
	}
	if p.Theme == nil {
		p.Theme = NewTheme(NightPalette)
	}
	var first error
	for part := range stream {
		if err := p.part(part); err != nil && first == nil {
			first = err
		}
	}
	return first
}

func (p *Printer) part(part provider.StreamPart) error {
	switch v := part.(type) {
	case provider.StreamStart:
		for _, w := range v.Warnings {
			fmt.Fprintf(os.Stderr, "warning: %s %s\n", w.Feature, w.Details)
		}

	case provider.ReasoningDelta:
		if p.ShowReasoning && v.Delta != "" {
			if !p.inReasoning {
				p.inReasoning = true
			}
			fmt.Fprint(p.Out, p.paint(SlotThinkingText, v.Delta))
		}

	case provider.ReasoningEnd:
		if p.inReasoning {
			fmt.Fprintln(p.Out)
			p.inReasoning = false
		}

	case provider.TextDelta:
		fmt.Fprint(p.Out, v.Delta)

	case provider.ToolInputStart:
		fmt.Fprintf(p.Out, "\n%s %s", p.paint(SlotWarning, bulletGlyph), p.paint(SlotText, v.ToolName))

	case ai.ToolExecuted:
		exec := v.Execution
		if exec.Err != nil {
			fmt.Fprintf(p.Out, " %s\n", p.paint(SlotToolError, "failed: "+exec.Err.Error()))
		} else {
			fmt.Fprintf(p.Out, " %s\n", p.paint(SlotMuted, preview(exec.Result)))
		}

	case ai.RunFinish:
		var parts []string
		parts = append(parts, fmt.Sprintf("%d step(s)", v.Steps), string(v.FinishReason.Unified))
		if in := v.Usage.InputTokens.Total; in != nil {
			parts = append(parts, fmt.Sprintf("in %d", *in))
		}
		if out := v.Usage.OutputTokens.Total; out != nil {
			parts = append(parts, fmt.Sprintf("out %d", *out))
		}
		fmt.Fprintf(p.Out, "\n\n%s\n", p.paint(SlotDim, strings.Join(parts, " · ")))

	case provider.ErrorPart:
		return v.Err
	}
	return nil
}

func preview(result ai.ToolResult) string {
	switch out := result.Output().(type) {
	case provider.ToolOutputText:
		return oneLine(out.Value)
	case provider.ToolOutputErrorText:
		return "error: " + oneLine(out.Value)
	default:
		return fmt.Sprintf("%T", out)
	}
}

func oneLine(s string) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	return truncate(s, 80)
}
