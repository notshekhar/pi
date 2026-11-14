package tools

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/ai"
)

// Web search, by scraping DuckDuckGo's HTML endpoint.
//
// No API key and no dependency, which is the whole reason for the approach —
// but it is scraping, so it is FRAGILE BY CONSTRUCTION: DuckDuckGo owes us no
// stable markup. Every extraction below fails soft, and the tool reports "no
// results" rather than inventing them, because a search tool that quietly
// returns nothing teaches the model the web is empty.
//
// Off by default. It is the only tool here that talks to the network, and a
// coding agent should not reach the internet because someone forgot to look
// at a setting.

const (
	searchEndpoint = "https://html.duckduckgo.com/html/"
	searchTimeout  = 20 * time.Second
	// A search result set the model has to read; more is noise it pays for.
	defaultSearchResults = 8
	maxSearchResults     = 10
)

// searchUA names the client honestly, as loop's does.
const searchUA = "pi-agent/1.0 (+https://github.com/notshekhar/pi)"

var (
	// The result anchor and its snippet. Written loosely on purpose: the
	// markup shifts, and a tight pattern fails silently on the next change.
	// \b keeps result__a / results_links from splitting: only the block
	// div's leading `result` class is followed by a non-word character.
	reResultBlock = regexp.MustCompile(`<div[^>]+class="result\b`)
	reResult      = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reSnippet     = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	reTags        = regexp.MustCompile(`<[^>]*>`)
	reSpaces      = regexp.MustCompile(`\s+`)
)

// SearchResult is one hit.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

type searchArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitzero"`
}

// WebSearch returns the websearch tool. Failures come back as bracketed text
// rather than errors, like a failed page fetch: "the search failed" answers
// the question the model asked.
func WebSearch(t *Context) ai.Tool {
	return LoopTool("websearch", func(ctx context.Context, a searchArgs) (ai.ToolResult, error) {
		limit := a.Limit
		if limit <= 0 {
			limit = defaultSearchResults
		}
		return ai.ToolText(searchReport(ctx, a.Query, min(limit, maxSearchResults))), nil
	})
}

func searchReport(ctx context.Context, query string, limit int) string {
	results, err := Search(ctx, query, limit)
	switch {
	case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
		return fmt.Sprintf("[websearch timed out or aborted: %q]", query)
	case err != nil:
		return fmt.Sprintf("[websearch %v]", err)
	case len(results) == 0:
		// Zero results also covers a challenge page or changed markup, so
		// say so instead of a bare "nothing".
		return fmt.Sprintf("[no results for %q — either nothing matched, or DuckDuckGo rate-limited the request]", query)
	}
	return formatResults(results)
}

// Search runs one query against the HTML endpoint.
func Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, searchEndpoint+"?q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", searchUA)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("error: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed: %s", resp.Status)
	}

	// Cap the read: a scraper with no ceiling is a memory bug waiting for a
	// bad day at the other end.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("error: %w", err)
	}
	return parseResults(string(body), limit), nil
}

// parseResults is loop's parseSearchResults: the page is cut into its
// `<div class="result …">` blocks and each block read on its own, so a
// snippet always belongs to its own title. Sponsored blocks (`result--ad`,
// or a duckduckgo.com/y.js redirect) are skipped.
func parseResults(page string, limit int) []SearchResult {
	var out []SearchResult
	blocks := reResultBlock.Split(page, -1)
	for _, block := range blocks[1:] {
		if len(out) >= limit {
			break
		}
		classRest, _, _ := strings.Cut(block, `"`)
		if strings.Contains(classRest, "result--ad") {
			continue
		}
		link := reResult.FindStringSubmatch(block)
		if link == nil {
			continue
		}
		target := resolveURL(link[1])
		title := cleanHTML(link[2])
		if target == "" || isAd(target) || title == "" {
			continue
		}
		snippet := ""
		if m := reSnippet.FindStringSubmatch(block); m != nil {
			snippet = cleanHTML(m[1])
		}
		out = append(out, SearchResult{Title: title, URL: target, Snippet: snippet})
	}
	return out
}

// resolveURL unwraps DuckDuckGo's redirector, which hides the real target in
// a `uddg` parameter. A raw link is returned as-is.
func resolveURL(raw string) string {
	raw = html.UnescapeString(raw)
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" {
		return target
	}
	if !strings.HasPrefix(u.Scheme, "http") {
		return ""
	}
	return u.String()
}

// isAd reports DuckDuckGo's own ad and tracking links, which are not results.
func isAd(link string) bool {
	return strings.Contains(link, "duckduckgo.com/y.js") ||
		strings.Contains(link, "/y.js?") ||
		strings.Contains(link, "ad_provider=") ||
		strings.Contains(link, "duckduckgo.com/l/?ad")
}

// cleanHTML strips tags and entities down to readable text.
func cleanHTML(s string) string {
	s = reTags.ReplaceAllString(s, "")
	s = html.UnescapeString(s)
	s = reSpaces.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// formatResults renders hits for the model: numbered, with the URL on its own
// line so it survives wrapping intact.
func formatResults(results []SearchResult) string {
	var b strings.Builder
	for i, r := range results {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s\n   %s", i+1, r.Title, r.URL)
		if r.Snippet != "" {
			fmt.Fprintf(&b, "\n   %s", r.Snippet)
		}
	}
	return b.String()
}
