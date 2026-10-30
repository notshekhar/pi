// Package providertest checks a provider's stream against the part contract
// the AI SDK's providers keep, which the run's accumulator and every
// renderer rely on.
package providertest

import (
	"fmt"
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// Violations lists every way parts break the contract:
//
//   - a text or reasoning delta or end belongs to a block started and not
//     yet ended, and every started block ends before the finish;
//   - a tool input delta or end belongs to an input started and not yet
//     ended, and an input still open when its call arrives is a call
//     whose arguments never finished;
//   - the stream ends with exactly one Finish, and nothing follows it.
func Violations(parts []provider.StreamPart) []string {
	c := checker{text: map[string]bool{}, reasoning: map[string]bool{}, input: map[string]bool{}}
	for i, p := range parts {
		c.at = i
		c.check(p)
	}
	if c.finishes == 0 {
		c.fail("the stream has no Finish")
	}
	for id := range c.text {
		c.fail("text %q never ended", id)
	}
	for id := range c.reasoning {
		c.fail("reasoning %q never ended", id)
	}
	for id := range c.input {
		c.fail("tool input %q never ended", id)
	}
	return c.out
}

type checker struct {
	at                     int
	finishes               int
	text, reasoning, input map[string]bool
	out                    []string
}

func (c *checker) fail(format string, args ...any) {
	c.out = append(c.out, fmt.Sprintf("part %d: ", c.at)+fmt.Sprintf(format, args...))
}

func (c *checker) check(part provider.StreamPart) {
	if c.finishes > 0 {
		c.fail("%T after the Finish", part)
	}
	switch p := part.(type) {
	case provider.TextStart:
		c.start(c.text, "text", p.ID)
	case provider.TextDelta:
		c.inside(c.text, "text delta", p.ID)
	case provider.TextEnd:
		c.inside(c.text, "text end", p.ID)
		delete(c.text, p.ID)
	case provider.ReasoningStart:
		c.start(c.reasoning, "reasoning", p.ID)
	case provider.ReasoningDelta:
		c.inside(c.reasoning, "reasoning delta", p.ID)
	case provider.ReasoningEnd:
		c.inside(c.reasoning, "reasoning end", p.ID)
		delete(c.reasoning, p.ID)
	case provider.ToolInputStart:
		c.start(c.input, "tool input", p.ID)
	case provider.ToolInputDelta:
		c.inside(c.input, "tool input delta", p.ID)
	case provider.ToolInputEnd:
		c.inside(c.input, "tool input end", p.ID)
		delete(c.input, p.ID)
	case provider.ToolCall:
		if c.input[p.ToolCallID] {
			c.fail("tool call %q arrived before its input ended", p.ToolCallID)
		}
	case provider.Finish:
		c.finishes++
	}
}

func (c *checker) start(open map[string]bool, what, id string) {
	if open[id] {
		c.fail("%s %q started twice", what, id)
	}
	open[id] = true
}

func (c *checker) inside(open map[string]bool, what, id string) {
	if !open[id] {
		c.fail("%s for %q, which is not open", what, id)
	}
}

// Cuts is body cut short after each of its events, the whole body last: a
// stream that dropped at every point it could have.
func Cuts(body string) []string {
	var out []string
	for i := 0; ; {
		j := strings.Index(body[i:], "\n\n")
		if j < 0 {
			break
		}
		i += j + 2
		out = append(out, body[:i])
	}
	if len(out) == 0 || out[len(out)-1] != body {
		out = append(out, body)
	}
	return out
}
