package tools

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/notshekhar/pi/internal/modules/ai"
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
	searchTimeout  = 15 * time.Second
	// A search result set the model has to read; more is noise it pays for.
	maxResults = 8
)

// searchUA identifies the client. DuckDuckGo serves a stripped page to
// clients it does not recognise as browsers, so this is required, not polite.
const searchUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/124.0 Safari/537.36"

var (
	// The result anchor and its snippet. Written loosely on purpose: the
	// markup shifts, and a tight pattern fails silently on the next change.
	reResult  = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)
	reSnippet = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)
	reTags    = regexp.MustCompile(`<[^>]*>`)
	reSpaces  = regexp.MustCompile(`\s+`)
)

// SearchResult is one hit.
type SearchResult struct {
	Title   string
	URL     string
	Snippet string
}

type searchArgs struct {
	Query string `json:"query" jsonschema:"description=What to search for"`
}

// WebSearch returns the websearch tool.
func WebSearch(t *Context) ai.Tool {
	return ai.NewTool("websearch",
		"Search the web and return titles, URLs, and snippets. Use it for things "+
			"that changed after your training cutoff, for library documentation, and "+
			"for error messages you do not recognise. Snippets are short — fetch a "+
			"page with bash+curl when you need the detail.",
		func(ctx context.Context, a searchArgs) (ai.ToolResult, error) {
			if aborted(ctx) {
				return ai.ToolError("aborted"), nil
			}
			query := strings.TrimSpace(a.Query)
			if query == "" {
				return ai.ToolError("query is empty"), nil
			}

			results, err := Search(ctx, query)
			if err != nil {
				return ai.ToolErrorf("search failed: %v", err), nil
			}
			if len(results) == 0 {
				return ai.ToolText("no results"), nil
			}
			return ai.ToolText(formatResults(results)), nil
		})
}

// Search runs one query against the HTML endpoint.
func Search(ctx context.Context, query string) ([]SearchResult, error) {
	ctx, cancel := context.WithTimeout(ctx, searchTimeout)
	defer cancel()

	form := url.Values{"q": {query}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, searchEndpoint,
		strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", searchUA)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo returned %s", resp.Status)
	}

	// Cap the read: a scraper with no ceiling is a memory bug waiting for a
	// bad day at the other end.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseResults(string(body)), nil
}

// parseResults extracts hits from the HTML.
func parseResults(page string) []SearchResult {
	snippets := reSnippet.FindAllStringSubmatch(page, -1)
	matches := reResult.FindAllStringSubmatch(page, -1)

	var out []SearchResult
	for i, m := range matches {
		link := resolveURL(m[1])
		if link == "" || isAd(link) {
			continue
		}
		title := cleanHTML(m[2])
		if title == "" {
			continue
		}
		snippet := ""
		// Snippets and results appear in the same order, so index alignment
		// is the best available join — and a mismatch costs a snippet, never
		// a wrong one, because a missing index simply yields nothing.
		if i < len(snippets) {
			snippet = cleanHTML(snippets[i][1])
		}
		out = append(out, SearchResult{Title: title, URL: link, Snippet: snippet})
		if len(out) >= maxResults {
			break
		}
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
			fmt.Fprintf(&b, "\n   %s", truncateRunes(r.Snippet, 300))
		}
	}
	return b.String()
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return strings.TrimSpace(string(r[:max])) + "…"
}
