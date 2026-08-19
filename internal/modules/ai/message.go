package ai

import (
	"net/url"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// Message is the conversation message type. It is the provider spec's message
// so that history assembled by the agent loop can be replayed without
// conversion, which is what keeps reasoning signatures and provider metadata
// intact across turns.
type Message = provider.Message

// SystemText builds a system message.
func SystemText(text string) provider.SystemMessage {
	return provider.SystemMessage{Content: text}
}

// UserText builds a user message containing one text part.
func UserText(text string) provider.UserMessage {
	return provider.UserMessage{
		Content: []provider.UserPart{provider.TextPart{Text: text}},
	}
}

// UserParts builds a user message from arbitrary parts.
func UserParts(parts ...provider.UserPart) provider.UserMessage {
	return provider.UserMessage{Content: parts}
}

// AssistantText builds an assistant message containing one text part.
//
// Placing one at the end of a prompt prefills the model's reply, constraining
// how it starts.
func AssistantText(text string) provider.AssistantMessage {
	return provider.AssistantMessage{
		Content: []provider.AssistantPart{provider.TextPart{Text: text}},
	}
}

// ImageBytes builds an image part from raw bytes.
func ImageBytes(data []byte, mediaType string) provider.FilePart {
	return provider.FilePart{
		Data:      provider.FileDataBytes{Data: data},
		MediaType: mediaType,
	}
}

// ImageURL builds an image part from a URL. Providers that declare support for
// the URL through SupportedURLs receive it unfetched; for the rest, core
// downloads it and substitutes the bytes before the call. Options.Downloader
// controls how, and Options.DisableURLDownload turns it off.
func ImageURL(rawURL string) (provider.FilePart, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return provider.FilePart{}, err
	}
	return provider.FilePart{
		Data:      provider.FileDataURL{URL: u},
		MediaType: "image",
	}, nil
}

// FileBytes builds a document part from raw bytes.
func FileBytes(data []byte, mediaType, filename string) provider.FilePart {
	return provider.FilePart{
		Data:      provider.FileDataBytes{Data: data},
		MediaType: mediaType,
		Filename:  filename,
	}
}

// CacheBreakpoint returns provider options that mark a cache breakpoint at
// this point in the prompt.
//
// Anthropic charges a premium to write the cache and a large discount to read
// it, so a breakpoint belongs after content that repeats across turns — a
// system prompt or a tool list — and not after content that changes every
// turn. At most four breakpoints are honoured per request.
func CacheBreakpoint() provider.ProviderOptions {
	return provider.ProviderOptions{
		"anthropic": {"cacheControl": map[string]any{"type": "ephemeral"}},
	}
}

// CacheBreakpointTTL is CacheBreakpoint with an explicit lifetime, either
// "5m" or "1h".
func CacheBreakpointTTL(ttl string) provider.ProviderOptions {
	return provider.ProviderOptions{
		"anthropic": {"cacheControl": map[string]any{"type": "ephemeral", "ttl": ttl}},
	}
}
