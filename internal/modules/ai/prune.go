package ai

import "github.com/notshekhar/pi/internal/modules/ai/provider"

// PruneOptions controls how a conversation is shortened.
//
// Pruning is for the case where a conversation has outgrown the model's
// context. It is deliberately mechanical: it drops whole messages from the
// middle and never rewrites their content, because a summary that changes what
// was said is a different feature with different failure modes.
type PruneOptions struct {
	// KeepFirst is how many messages at the start to keep whatever happens.
	// The opening turns usually carry the task, so losing them costs more than
	// losing the middle.
	KeepFirst int

	// KeepLast is how many messages at the end to keep. Recent turns are what
	// the model is actually working from.
	KeepLast int

	// Marker replaces what was dropped, so the model can see that something is
	// missing rather than silently reading a conversation with a hole in it.
	// Empty inserts no marker.
	Marker string
}

// PruneMessages shortens a conversation by dropping messages from the middle.
//
// It never splits a tool call from its result: a tool message whose call has
// been dropped is rejected by every provider, so the boundaries move outward
// until the pairing holds.
//
// The input is not modified.
func PruneMessages(messages []provider.Message, opts PruneOptions) []provider.Message {
	keepFirst := max(opts.KeepFirst, 0)
	keepLast := max(opts.KeepLast, 0)

	if keepFirst+keepLast >= len(messages) {
		out := make([]provider.Message, len(messages))
		copy(out, messages)
		return out
	}

	// Move the boundaries outward until neither cuts between an assistant's
	// tool calls and the tool message answering them.
	head := keepFirst
	for head > 0 && head < len(messages) && isToolMessage(messages[head]) {
		// The message just after the head is a tool result whose call is being
		// dropped; keep one more so the pair survives.
		head++
	}

	tail := len(messages) - keepLast
	for tail > 0 && tail < len(messages) && isToolMessage(messages[tail]) {
		// The first kept message is a tool result; its call is above the cut,
		// so extend the tail back to include it.
		tail--
	}

	if head >= tail {
		out := make([]provider.Message, len(messages))
		copy(out, messages)
		return out
	}

	out := make([]provider.Message, 0, head+keepLast+1)
	out = append(out, messages[:head]...)
	if opts.Marker != "" {
		out = append(out, provider.UserMessage{
			Content: []provider.UserPart{provider.TextPart{Text: opts.Marker}},
		})
	}
	out = append(out, messages[tail:]...)
	return out
}

// isToolMessage reports whether a message carries tool results, which cannot
// be separated from the assistant message that made the calls.
func isToolMessage(m provider.Message) bool {
	_, ok := m.(provider.ToolMessage)
	return ok
}
