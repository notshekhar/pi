package session

import (
	"fmt"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Replay: a stored conversation flattened into the handful of things a
// transcript actually draws.
//
// This exists so the UI never type-switches over provider types. A renderer
// that knew about `provider.ToolResultPart` would be coupled to the model
// layer's wire shape, and every provider change would reach the screen.

// ReplayPart is one drawable item from a stored conversation.
type ReplayPart interface{ isReplayPart() }

// ReplayUser is something the user said.
type ReplayUser struct{ Text string }

// ReplayAssistant is prose the model produced.
type ReplayAssistant struct{ Text string }

// ReplayToolCall is a tool invocation. Its result is looked up separately,
// because the two live in different messages.
type ReplayToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

func (ReplayUser) isReplayPart()      {}
func (ReplayAssistant) isReplayPart() {}
func (ReplayToolCall) isReplayPart()  {}

// ToolOutcome is a finished tool call's result.
type ToolOutcome struct {
	Text    string
	IsError bool
}

// Parts flattens one message into drawable items.
//
// Reasoning is deliberately absent: providers hand back signed blobs meant
// for the model rather than the reader, and a replayed transcript full of
// them would be noise.
func Parts(msg provider.Message) []ReplayPart {
	var out []ReplayPart
	switch m := msg.(type) {
	case provider.UserMessage:
		if text := flattenUser(m); text != "" {
			out = append(out, ReplayUser{Text: text})
		}
	case provider.AssistantMessage:
		var prose string
		for _, part := range m.Content {
			switch p := part.(type) {
			case provider.TextPart:
				prose += p.Text
			case provider.ToolCallPart:
				// Prose before a call belongs above its row, in order.
				if prose != "" {
					out = append(out, ReplayAssistant{Text: prose})
					prose = ""
				}
				out = append(out, ReplayToolCall{
					ID: p.ToolCallID, Name: p.ToolName, Input: asMap(p.Input),
				})
			}
		}
		if prose != "" {
			out = append(out, ReplayAssistant{Text: prose})
		}
	}
	return out
}

// asMap coerces a decoded tool input to the map a summary expects. Anything
// else — an array, a bare string — yields nil, and the row simply shows no
// summary rather than a mangled one.
func asMap(v provider.JSONValue) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return nil
}

// ToolResults indexes every tool result in the conversation by call id, so a
// replayed call can find the outcome that arrived in a later message.
func (s *Session) ToolResults() map[string]ToolOutcome {
	out := map[string]ToolOutcome{}
	for _, msg := range s.Messages {
		m, ok := msg.(provider.ToolMessage)
		if !ok {
			continue
		}
		for _, part := range m.Content {
			r, ok := part.(provider.ToolResultPart)
			if !ok {
				continue
			}
			out[r.ToolCallID] = outcomeOf(r.Output)
		}
	}
	return out
}

func outcomeOf(o provider.ToolResultOutput) ToolOutcome {
	switch v := o.(type) {
	case provider.ToolOutputText:
		return ToolOutcome{Text: v.Value}
	case provider.ToolOutputErrorText:
		return ToolOutcome{Text: v.Value, IsError: true}
	case provider.ToolOutputJSON:
		return ToolOutcome{Text: fmt.Sprint(v.Value)}
	case provider.ToolOutputErrorJSON:
		return ToolOutcome{Text: fmt.Sprint(v.Value), IsError: true}
	case provider.ToolOutputExecutionDenied:
		return ToolOutcome{Text: "denied: " + v.Reason, IsError: true}
	}
	return ToolOutcome{}
}
