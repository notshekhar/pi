package ai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/notshekhar/pi/internal/modules/ai/provider"
)

// maxDownloadBytes caps a single fetched file. A model's context is far
// smaller than what a URL can point at, and an unbounded read turns a bad link
// into an out-of-memory crash.
const maxDownloadBytes = 32 << 20 // 32 MiB

// Downloader fetches a file the provider cannot fetch itself.
//
// The default uses http.DefaultClient. Replace it to add auth, a proxy, or a
// cache — a CLI attaching the same screenshot every turn wants the last one.
type Downloader interface {
	Download(ctx context.Context, u *url.URL) (data []byte, mediaType string, err error)
}

// DownloaderFunc adapts a function to Downloader.
type DownloaderFunc func(ctx context.Context, u *url.URL) ([]byte, string, error)

// Download implements Downloader.
func (f DownloaderFunc) Download(ctx context.Context, u *url.URL) ([]byte, string, error) {
	return f(ctx, u)
}

// httpDownloader is the default Downloader.
type httpDownloader struct{ client *http.Client }

// Download fetches a URL and reports the server's media type.
func (d httpDownloader) Download(ctx context.Context, u *url.URL) ([]byte, string, error) {
	client := d.client
	if client == nil {
		client = http.DefaultClient
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("fetching %s: HTTP %d", u, resp.StatusCode)
	}

	// LimitReader is given one extra byte so an oversized file is detected
	// rather than silently truncated into a corrupt image.
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDownloadBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", u, err)
	}
	if len(data) > maxDownloadBytes {
		return nil, "", fmt.Errorf("%s is larger than the %d MiB limit", u, maxDownloadBytes>>20)
	}

	// The media type may carry parameters ("image/png; charset=binary"), which
	// providers reject.
	mediaType, _, _ := strings.Cut(resp.Header.Get("Content-Type"), ";")
	return data, strings.TrimSpace(mediaType), nil
}

// resolveFileURLs replaces every FileDataURL the model cannot fetch itself
// with the downloaded bytes.
//
// Providers advertise what they fetch through SupportedURLs; anything else has
// to arrive inline or the call fails on the provider's side with a message
// that does not mention the URL. Downloads run in parallel because a turn
// routinely carries several attachments.
//
// The prompt is copied before any edit, so a caller holding the conversation
// never sees it mutated.
func resolveFileURLs(ctx context.Context, model provider.LanguageModel, prompt provider.Prompt, downloader Downloader) (provider.Prompt, error) {
	patterns, err := model.SupportedURLs(ctx)
	if err != nil {
		return nil, fmt.Errorf("pi: reading supported URLs for %s: %w", model.ModelID(), err)
	}

	targets := collectDownloadTargets(prompt, patterns)
	if len(targets) == 0 {
		return prompt, nil
	}

	if downloader == nil {
		downloader = httpDownloader{}
	}

	fetched := make([]downloadedFile, len(targets))
	if err := forEachParallel(ctx, len(targets), defaultDownloadParallelism, func(ctx context.Context, i int) error {
		data, mediaType, err := downloader.Download(ctx, targets[i].url)
		if err != nil {
			return fmt.Errorf("pi: downloading %s: %w", targets[i].url, err)
		}
		fetched[i] = downloadedFile{data: data, mediaType: mediaType}
		return nil
	}); err != nil {
		return nil, err
	}

	byURL := make(map[string]downloadedFile, len(targets))
	for i, t := range targets {
		byURL[t.url.String()] = fetched[i]
	}
	return rewritePrompt(prompt, byURL), nil
}

// defaultDownloadParallelism bounds concurrent fetches. Attachments are few
// and large, so a wide fan-out buys nothing.
const defaultDownloadParallelism = 4

// downloadTarget is one URL that has to be fetched.
type downloadTarget struct{ url *url.URL }

// downloadedFile is a fetched file.
type downloadedFile struct {
	data      []byte
	mediaType string
}

// collectDownloadTargets finds the URLs the provider will not fetch, without
// duplicates: the same image attached twice is downloaded once.
func collectDownloadTargets(prompt provider.Prompt, patterns map[string][]*regexp.Regexp) []downloadTarget {
	var targets []downloadTarget
	seen := map[string]bool{}

	visitFileURLs(prompt, func(u *url.URL, mediaType string) {
		key := u.String()
		if seen[key] || providerFetches(u, mediaType, patterns) {
			return
		}
		seen[key] = true
		targets = append(targets, downloadTarget{url: u})
	})

	return targets
}

// providerFetches reports whether the provider handles this URL itself.
//
// Patterns are keyed by media type, with "*/*" matching anything. Matching is
// done against the lower-cased URL, per the spec.
func providerFetches(u *url.URL, mediaType string, patterns map[string][]*regexp.Regexp) bool {
	if len(patterns) == 0 {
		return false
	}

	lowered := strings.ToLower(u.String())
	for key, regexps := range patterns {
		if !mediaTypeMatches(mediaType, key) {
			continue
		}
		for _, re := range regexps {
			if re.MatchString(lowered) {
				return true
			}
		}
	}
	return false
}

// mediaTypeMatches compares a part's media type against a SupportedURLs key,
// which may be "*/*", "image/*", or a full type.
func mediaTypeMatches(mediaType, key string) bool {
	if key == "*/*" || key == "*" {
		return true
	}

	// A part may carry just the top-level segment ("image"), which the spec
	// says is equivalent to "image/*".
	normalize := func(s string) (string, string) {
		s = strings.ToLower(strings.TrimSpace(s))
		top, sub, found := strings.Cut(s, "/")
		if !found {
			return top, "*"
		}
		return top, sub
	}

	partTop, partSub := normalize(mediaType)
	keyTop, keySub := normalize(key)

	if partTop != keyTop {
		return false
	}
	return keySub == "*" || partSub == "*" || partSub == keySub
}

// visitFileURLs calls fn for every URL-backed file in the prompt.
func visitFileURLs(prompt provider.Prompt, fn func(u *url.URL, mediaType string)) {
	visitPart := func(data provider.FileData, mediaType string) {
		if v, ok := data.(provider.FileDataURL); ok && v.URL != nil {
			fn(v.URL, mediaType)
		}
	}

	for _, message := range prompt {
		switch m := message.(type) {
		case provider.UserMessage:
			for _, part := range m.Content {
				if p, ok := part.(provider.FilePart); ok {
					visitPart(p.Data, p.MediaType)
				}
			}

		case provider.AssistantMessage:
			for _, part := range m.Content {
				switch p := part.(type) {
				case provider.FilePart:
					visitPart(p.Data, p.MediaType)
				case provider.ReasoningFilePart:
					visitPart(p.Data, p.MediaType)
				}
			}
		}
	}
}

// rewritePrompt returns a copy of the prompt with fetched URLs replaced by
// their bytes. Messages without a rewritten part are shared rather than
// copied, so an ordinary turn allocates nothing.
func rewritePrompt(prompt provider.Prompt, fetched map[string]downloadedFile) provider.Prompt {
	out := make(provider.Prompt, len(prompt))
	copy(out, prompt)

	replace := func(data provider.FileData, mediaType string) (provider.FileData, string, bool) {
		v, ok := data.(provider.FileDataURL)
		if !ok || v.URL == nil {
			return nil, "", false
		}
		file, ok := fetched[v.URL.String()]
		if !ok {
			return nil, "", false
		}
		// The server's media type wins only when the part left it unset or
		// gave just the top-level segment: the caller may know better than a
		// generic application/octet-stream.
		if mediaType == "" || !strings.Contains(mediaType, "/") {
			if file.mediaType != "" {
				mediaType = file.mediaType
			}
		}
		return provider.FileDataBytes{Data: file.data}, mediaType, true
	}

	for i, message := range out {
		switch m := message.(type) {
		case provider.UserMessage:
			var content []provider.UserPart
			for j, part := range m.Content {
				p, ok := part.(provider.FilePart)
				if !ok {
					continue
				}
				data, mediaType, replaced := replace(p.Data, p.MediaType)
				if !replaced {
					continue
				}
				if content == nil {
					content = make([]provider.UserPart, len(m.Content))
					copy(content, m.Content)
				}
				p.Data, p.MediaType = data, mediaType
				content[j] = p
			}
			if content != nil {
				m.Content = content
				out[i] = m
			}

		case provider.AssistantMessage:
			var content []provider.AssistantPart
			ensure := func() {
				if content == nil {
					content = make([]provider.AssistantPart, len(m.Content))
					copy(content, m.Content)
				}
			}
			for j, part := range m.Content {
				switch p := part.(type) {
				case provider.FilePart:
					data, mediaType, replaced := replace(p.Data, p.MediaType)
					if !replaced {
						continue
					}
					ensure()
					p.Data, p.MediaType = data, mediaType
					content[j] = p

				case provider.ReasoningFilePart:
					data, mediaType, replaced := replace(p.Data, p.MediaType)
					if !replaced {
						continue
					}
					ensure()
					p.Data, p.MediaType = data, mediaType
					content[j] = p
				}
			}
			if content != nil {
				m.Content = content
				out[i] = m
			}
		}
	}

	return out
}

// urlCache is a Downloader that remembers what it fetched, which is what a CLI
// re-attaching the same file every turn wants.
type urlCache struct {
	inner Downloader

	mu    sync.Mutex
	files map[string]downloadedFile
}

// CachingDownloader wraps a Downloader with an unbounded in-memory cache.
//
// It is unbounded on purpose: it is meant to live for one run, where the set
// of attachments is small and known. A long-lived process should supply its
// own Downloader with a policy that suits it.
func CachingDownloader(inner Downloader) Downloader {
	return &urlCache{inner: inner, files: map[string]downloadedFile{}}
}

// Download implements Downloader.
func (c *urlCache) Download(ctx context.Context, u *url.URL) ([]byte, string, error) {
	key := u.String()

	c.mu.Lock()
	file, ok := c.files[key]
	c.mu.Unlock()
	if ok {
		return file.data, file.mediaType, nil
	}

	inner := c.inner
	if inner == nil {
		inner = httpDownloader{}
	}
	data, mediaType, err := inner.Download(ctx, u)
	if err != nil {
		return nil, "", err
	}

	c.mu.Lock()
	c.files[key] = downloadedFile{data: data, mediaType: mediaType}
	c.mu.Unlock()

	return data, mediaType, nil
}
