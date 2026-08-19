// Package session holds one conversation, in memory and in the database.
//
// The STORED form (see store.go) is authoritative: it preserves tool calls,
// tool results, and the signed reasoning some providers require, so a session
// can be RESUMED and continued rather than merely read back. `Text` renders
// the human-readable form for /export.
package session

import (
	"strings"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Session is one conversation.
type Session struct {
	ID       string
	Path     string
	Meta     Meta
	Messages []provider.Message
}

// New creates an empty persisted session.

// Add appends messages and persists them.
//
// A session with no title takes one from the first thing the user said, so a
// listing reads as a list of tasks rather than a column of timestamps.
func (s *Session) Add(messages ...provider.Message) error {
	if s.Meta.Title == "" {
		for _, msg := range messages {
			m, ok := msg.(provider.UserMessage)
			if !ok {
				continue
			}
			if title := titleFrom(flattenUser(m)); title != "" {
				s.Meta.Title = title
				break
			}
		}
	}
	s.Messages = append(s.Messages, messages...)
	return s.appendWire(messages)
}

// titleFrom derives a session title from a prompt: its first line, trimmed to
// something that fits a picker row.
func titleFrom(prompt string) string {
	line := strings.TrimSpace(strings.SplitN(prompt, "\n", 2)[0])
	if line == "" {
		return ""
	}
	const max = 60
	r := []rune(line)
	if len(r) > max {
		return strings.TrimSpace(string(r[:max])) + "…"
	}
	return line
}

// Reset clears history for /new, keeping the same file.
func (s *Session) Reset() error {
	s.Messages = nil
	s.Meta.Title = ""
	return s.rewrite()
}

// Replace swaps the whole history. Used by compaction, which stands a summary
// in for the messages it replaces.
func (s *Session) Replace(messages ...provider.Message) error {
	s.Messages = append([]provider.Message{}, messages...)
	return s.rewrite()
}

// Text is the conversation as markdown, for /export and /copy.
func (s *Session) Text() string {
	var b strings.Builder
	for _, msg := range s.Messages {
		switch m := msg.(type) {
		case provider.UserMessage:
			if t := flattenUser(m); t != "" {
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

// LastAssistant is the most recent assistant reply, for /copy.
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

// Chars is the total size of the conversation's text.
//
// Tool results are counted too: they are usually the bulk of a long session,
// and a context estimate that ignored them would be reassuring and wrong.
func (s *Session) Chars() int {
	n := 0
	for _, msg := range s.Messages {
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
