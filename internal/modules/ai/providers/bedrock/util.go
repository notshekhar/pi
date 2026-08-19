package bedrock

import (
	"encoding/json"
	"strconv"
	"strings"
	"unicode"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// optionBlock returns the caller's Bedrock-specific options, accepting both
// the current "bedrock" key and the TypeScript SDK's "amazonBedrock" spelling
// so a prompt built for one works against the other.
func optionBlock(opts provider.ProviderOptions) provider.JSONObject {
	if block := opts.Get(providerID); block != nil {
		return block
	}
	return opts.Get("amazonBedrock")
}

// stringOpt returns the first string-valued key that is set.
func stringOpt(block provider.JSONObject, keys ...string) (string, bool) {
	if block == nil {
		return "", false
	}
	for _, key := range keys {
		if v, ok := block[key].(string); ok && v != "" {
			return v, true
		}
	}
	return "", false
}

// reasoningMeta builds provider metadata written under both keys, so a later
// turn can replay thinking regardless of which spelling the caller reads.
func reasoningMeta(values provider.JSONObject) provider.ProviderMetadata {
	return provider.ProviderMetadata{
		providerID:      values,
		"amazonBedrock": values,
	}
}

// normalizeToolCallID rewrites a tool-call id for Mistral, which requires
// exactly nine alphanumeric characters. Bedrock issues ids like
// "tooluse_bpe71yCfRu2b5i-nKGDr5g", which 400 if sent back unchanged.
func normalizeToolCallID(id string, isMistral bool) string {
	if !isMistral || id == "" {
		return id
	}
	var b strings.Builder
	for _, r := range id {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			if b.Len() == 9 {
				break
			}
		}
	}
	return b.String()
}

// sanitizeToolName strips characters Converse rejects in a tool name.
func sanitizeToolName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), r == '_', r == '-':
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return "_"
	}
	return b.String()
}

// toInt64 coerces a JSON number, which decodes as float64, to an int64.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case float32:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}

// ptr returns a pointer to v.
func ptr[T any](v T) *T { return &v }
