package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// URL fetching for the read tool, loop's tools/utils/fetch-url.ts: the page
// comes back as readable text. No headless browser — a request and a light
// HTML-to-text pass.

const (
	fetchTimeout  = 20 * time.Second
	maxFetchBytes = 5 * 1024 * 1024
	// fetchUserAgent is loop's, so a site sees one client whichever agent asks.
	fetchUserAgent = "loop-agent/1.0 (+https://github.com/notshekhar/loop)"
)

var httpURL = regexp.MustCompile(`(?i)^https?://`)

// IsHTTPURL reports whether value is an http(s) URL.
func IsHTTPURL(value string) bool { return httpURL.MatchString(strings.TrimSpace(value)) }

var (
	htmlNoise      = regexp.MustCompile(`(?is)<script.*?</script>|<style.*?</style>|<!--.*?-->`)
	htmlBlockClose = regexp.MustCompile(`(?i)</(p|div|h[1-6]|li|tr|br|section|article|header|footer)>`)
	htmlListItem   = regexp.MustCompile(`(?i)<li[^>]*>`)
	htmlTag        = regexp.MustCompile(`<[^>]+>`)
	runsOfBlanks   = regexp.MustCompile(`[ \t]+`)
	looksLikeHTML  = regexp.MustCompile(`(?i)^\s*(<!doctype html|<html)`)
	textualType    = regexp.MustCompile(`(?i)text|json|xml|html|javascript|csv|yaml|markdown`)
	htmlEntities   = strings.NewReplacer("&nbsp;", " ", "&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#x27;", "'")
)

// htmlToText drops script and style, unwraps tags and decodes common entities.
func htmlToText(html string) string {
	text := htmlNoise.ReplaceAllString(html, " ")
	text = htmlBlockClose.ReplaceAllString(text, "\n")
	text = htmlListItem.ReplaceAllString(text, "- ")
	text = htmlEntities.Replace(htmlTag.ReplaceAllString(text, ""))
	var kept []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(runsOfBlanks.ReplaceAllString(line, " ")); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// FetchURLAsText fetches a URL and returns readable text, truncated like a
// file read. Failures come back as bracketed text, not errors: the model
// asked a question and "the fetch failed" is an answer to it.
func FetchURLAsText(ctx context.Context, url string) string {
	ctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Sprintf("[fetch error for %s: %v]", url, err)
	}
	req.Header.Set("User-Agent", fetchUserAgent)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return fmt.Sprintf("[fetch timed out or aborted: %s]", url)
		}
		return fmt.Sprintf("[fetch error for %s: %v]", url, err)
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return fmt.Sprintf("[fetch failed: %s for %s]", res.Status, url)
	}
	contentType := res.Header.Get("Content-Type")
	if contentType != "" && !textualType.MatchString(contentType) {
		return nonTextNotice(contentType, res.Header.Get("Content-Length"), url)
	}
	// One byte past the cap is enough to know the resource is over it.
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxFetchBytes+1))
	if err != nil {
		return fmt.Sprintf("[fetch error for %s: %v]", url, err)
	}
	if len(raw) > maxFetchBytes {
		return fmt.Sprintf("[resource too large: over %s at %s. Limit %s.]", FormatSize(maxFetchBytes), url, FormatSize(maxFetchBytes))
	}
	return fetchedText(string(raw), contentType, url)
}

func nonTextNotice(contentType, contentLength, url string) string {
	size := ""
	if n, err := strconv.ParseInt(contentLength, 10, 64); err == nil {
		size = ", " + FormatSize(n)
	}
	return fmt.Sprintf("[non-text resource: %s%s at %s. Not fetched as text.]", contentType, size, url)
}

func fetchedText(body, contentType, url string) string {
	if strings.Contains(strings.ToLower(contentType), "html") || looksLikeHTML.MatchString(body) {
		body = htmlToText(body)
	}
	if contentType == "" {
		contentType = "unknown type"
	}
	header := fmt.Sprintf("[fetched %s — %s]\n\n", url, contentType)
	cut := TruncateHead(body, DefaultMaxLines, DefaultMaxBytes)
	if cut.Truncated {
		return header + cut.Content + "\n\n[truncated at " + FormatSize(DefaultMaxBytes) + "]"
	}
	return header + cut.Content
}
