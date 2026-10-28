package session

import (
	"strings"

	"github.com/notshekhar/pi/internal/ai/provider"
)

// Read-only views over a session's current branch, for /export, /copy and the
// context estimate.

// Text is the conversation as markdown.
func (s *Session) Text() string {
	var b strings.Builder
	for _, msg := range s.Messages {
		switch m := msg.(type) {
		case provider.UserMessage:
			if t := StripHookContext(flattenUser(m)); t != "" {
				b.WriteString("## User\n\n" + t + "\n\n")
			}
		case provider.AssistantMessage:
			if t := flattenAssistant(m); t != "" {
				b.WriteString("## Assistant\n\n" + t + "\n\n")
			}
		}
	}
	return b.String()
}

// LastAssistant is the most recent assistant reply.
func (s *Session) LastAssistant() string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if m, ok := s.Messages[i].(provider.AssistantMessage); ok {
			if t := flattenAssistant(m); t != "" {
				return t
			}
		}
	}
	return ""
}

// Chars is the size of what the model is sent — tool results included, since
// they are usually the bulk of a long session.
func (s *Session) Chars() int { return Chars(s.ModelMessages()) }

// Chars sizes a message list.
func Chars(messages []provider.Message) int {
	n := 0
	for _, msg := range messages {
		switch m := msg.(type) {
		case provider.UserMessage:
			n += len(flattenUser(m))
		case provider.AssistantMessage:
			n += len(flattenAssistant(m))
		case provider.ToolMessage:
			for _, part := range m.Content {
				r, ok := part.(provider.ToolResultPart)
				if !ok {
					continue
				}
				switch out := r.Output.(type) {
				case provider.ToolOutputText:
					n += len(out.Value)
				case provider.ToolOutputErrorText:
					n += len(out.Value)
				}
			}
		}
	}
	return n
}

// JSONL is messages as one stored-content JSON object per line — the same
// {role, content} shape loop keeps in an entry, for /export.
func JSONL(messages []provider.Message) ([]byte, error) {
	var b strings.Builder
	for _, m := range messages {
		role, content, err := EncodeContent(m)
		if err != nil {
			return nil, err
		}
		b.WriteString(`{"role":` + mustJSON(role) + `,"content":` + string(content) + "}\n")
	}
	return []byte(b.String()), nil
}

func flattenUser(m provider.UserMessage) string {
	var b strings.Builder
	for _, part := range m.Content {
		if t, ok := part.(provider.TextPart); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}

func flattenAssistant(m provider.AssistantMessage) string {
	var b strings.Builder
	for _, part := range m.Content {
		if t, ok := part.(provider.TextPart); ok {
			b.WriteString(t.Text)
		}
	}
	return b.String()
}
