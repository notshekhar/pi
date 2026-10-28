package anthropic

import (
	"strings"
)

// capabilities records the model traits that change how a request is built.
// Anthropic exposes no capability endpoint, so this is matched on model id,
// the same way the TypeScript SDK does it.
type capabilities struct {
	// MaxOutputTokens is the model's ceiling, used when the caller sets none.
	MaxOutputTokens int64

	// SupportsStructuredOutput reports native JSON-schema-constrained output.
	SupportsStructuredOutput bool

	// SupportsAdaptiveThinking reports the newer thinking shape:
	// {"type":"adaptive","display":"summarized"} plus output_config.effort,
	// instead of an explicit token budget.
	SupportsAdaptiveThinking bool

	// RejectsSamplingParameters reports models that 400 when temperature,
	// top_p or top_k are sent.
	RejectsSamplingParameters bool

	// SupportsXHighEffort reports whether "xhigh" is accepted; models without
	// it take "max" as the top level.
	SupportsXHighEffort bool

	// RejectsThinkingDisabledAboveHighEffort reports models that 400 when
	// thinking is disabled while effort is above "high".
	RejectsThinkingDisabledAboveHighEffort bool

	// Known reports whether the id matched the table rather than falling
	// through to defaults.
	Known bool
}

// isLegacyClaude reports Claude Instant, Claude 2 and Claude 3 ids, which
// share a 4096-token output ceiling.
//
// This is a hand-written matcher rather than a regexp because the boundary
// checks are lookaheads, which Go's RE2 engine does not support. The family
// name must be followed by end-of-string or a separator so that "claude-3"
// does not also match a hypothetical "claude-30".
func isLegacyClaude(id string) bool {
	families := []struct {
		prefix     string
		separators string
	}{
		{"claude-instant", "-"},
		{"claude-v2", "-.:"},
		{"claude-2", "-.:"},
		{"claude-3", "-."},
	}

	for _, f := range families {
		i := strings.Index(id, f.prefix)
		if i < 0 {
			continue
		}
		rest := id[i+len(f.prefix):]
		if rest == "" || strings.IndexByte(f.separators, rest[0]) >= 0 {
			return true
		}
	}
	return false
}

// modelCapabilities returns the traits of a model id.
//
// Matching is by substring so that dated ids ("claude-opus-4-5-20251101"),
// aliases ("claude-opus-4-5") and gateway-prefixed ids
// ("us.anthropic.claude-opus-4-5-v1:0") all resolve to the same entry.
// Unknown ids get conservative defaults rather than an error, so a model
// released after this build still works.
func modelCapabilities(modelID string) capabilities {
	id := strings.ToLower(modelID)
	has := func(s string) bool { return strings.Contains(id, s) }

	switch {
	case has("claude-opus-5"):
		return capabilities{
			MaxOutputTokens:                        128000,
			SupportsStructuredOutput:               true,
			SupportsAdaptiveThinking:               true,
			RejectsSamplingParameters:              true,
			SupportsXHighEffort:                    true,
			RejectsThinkingDisabledAboveHighEffort: true,
			Known:                                  true,
		}

	case has("claude-opus-4-8"), has("claude-opus-4-7"),
		has("claude-fable-5"), has("claude-sonnet-5"):
		return capabilities{
			MaxOutputTokens:           128000,
			SupportsStructuredOutput:  true,
			SupportsAdaptiveThinking:  true,
			RejectsSamplingParameters: true,
			SupportsXHighEffort:       true,
			Known:                     true,
		}

	case has("claude-sonnet-4-6"), has("claude-opus-4-6"):
		return capabilities{
			MaxOutputTokens:          128000,
			SupportsStructuredOutput: true,
			SupportsAdaptiveThinking: true,
			Known:                    true,
		}

	case has("claude-sonnet-4-5"), has("claude-opus-4-5"), has("claude-haiku-4-5"):
		return capabilities{
			MaxOutputTokens:          64000,
			SupportsStructuredOutput: true,
			Known:                    true,
		}

	case has("claude-opus-4-1"):
		return capabilities{
			MaxOutputTokens:          32000,
			SupportsStructuredOutput: true,
			Known:                    true,
		}

	case has("claude-sonnet-4-"):
		return capabilities{MaxOutputTokens: 64000, Known: true}

	case has("claude-opus-4-"):
		return capabilities{MaxOutputTokens: 32000, Known: true}

	case has("claude-3-haiku"):
		return capabilities{MaxOutputTokens: 4096, Known: true}

	case isLegacyClaude(id):
		return capabilities{MaxOutputTokens: 4096, Known: true}

	default:
		// An unrecognised id is assumed to be a recent model, but without
		// claiming features whose absence would produce a 400.
		return capabilities{MaxOutputTokens: 64000}
	}
}
