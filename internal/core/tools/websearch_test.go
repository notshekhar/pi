package tools

import (
	"strings"
	"testing"
)

// A trimmed sample of the real page shape: a redirector link, a snippet, an
// ad, and an entity-escaped title.
const samplePage = `
<div class="result results_links">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F&amp;rut=abc">
    Go &amp; Documentation
  </a>
  <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc%2F">
    The <b>Go</b> programming language docs.
  </a>
</div>
<div class="result results_links result--ad">
  <a class="result__a" href="https://duckduckgo.com/y.js?ad_provider=bing">Buy Go Now</a>
  <a class="result__snippet" href="#">An advert.</a>
</div>
<div class="result results_links">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fpkg.go.dev%2F">pkg.go.dev</a>
  <a class="result__snippet" href="#">Package index.</a>
</div>
`

func TestParseResults(t *testing.T) {
	got := parseResults(samplePage)
	if len(got) != 2 {
		t.Fatalf("got %d results, want 2 (the ad should be dropped): %+v", len(got), got)
	}

	// The redirector is unwrapped to the real target.
	if got[0].URL != "https://go.dev/doc/" {
		t.Errorf("url = %q, want the unwrapped target", got[0].URL)
	}
	// Entities and tags are decoded away.
	if got[0].Title != "Go & Documentation" {
		t.Errorf("title = %q", got[0].Title)
	}
	if got[0].Snippet != "The Go programming language docs." {
		t.Errorf("snippet = %q", got[0].Snippet)
	}
	if got[1].URL != "https://pkg.go.dev/" {
		t.Errorf("second url = %q", got[1].URL)
	}
}

// Scraping is fragile by construction; every failure mode must be soft.
func TestParseResultsOnJunkReturnsNothing(t *testing.T) {
	for _, page := range []string{"", "<html><body>nothing here</body></html>", "not html at all"} {
		if got := parseResults(page); len(got) != 0 {
			t.Errorf("page %q yielded %+v, want nothing", page, got)
		}
	}
}

func TestParseResultsCaps(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 40; i++ {
		b.WriteString(`<a class="result__a" href="https://example.com/">Result</a>`)
	}
	if got := parseResults(b.String()); len(got) != maxResults {
		t.Errorf("got %d results, want the %d cap", len(got), maxResults)
	}
}

func TestResolveURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2F", "https://go.dev/"},
		{"https://example.com/page", "https://example.com/page"},
		{"//example.com/page", "https://example.com/page"},
		// Non-http schemes are not results.
		{"javascript:alert(1)", ""},
		{"mailto:a@b.c", ""},
	}
	for _, c := range cases {
		if got := resolveURL(c.in); got != c.want {
			t.Errorf("resolveURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestIsAd(t *testing.T) {
	ads := []string{
		"https://duckduckgo.com/y.js?ad_provider=bing",
		"https://example.com/x?ad_provider=y",
		"https://duckduckgo.com/l/?ad_domain=x",
	}
	for _, link := range ads {
		if !isAd(link) {
			t.Errorf("%q should be treated as an ad", link)
		}
	}
	for _, link := range []string{"https://go.dev/doc/", "https://pkg.go.dev/"} {
		if isAd(link) {
			t.Errorf("%q is a real result", link)
		}
	}
}

func TestFormatResults(t *testing.T) {
	got := formatResults([]SearchResult{
		{Title: "Go Docs", URL: "https://go.dev/doc/", Snippet: "docs"},
		{Title: "pkg", URL: "https://pkg.go.dev/"},
	})
	for _, want := range []string{"1. Go Docs", "https://go.dev/doc/", "docs", "2. pkg"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// Off by default; only registered when switched on.
func TestWebSearchIsOptIn(t *testing.T) {
	off := &Context{CWD: "/repo"}
	for _, tool := range All(off) {
		if tool.Name() == "websearch" {
			t.Fatal("websearch is registered without being enabled")
		}
	}

	on := &Context{CWD: "/repo", WebSearch: true}
	found := false
	for _, tool := range All(on) {
		if tool.Name() == "websearch" {
			found = true
		}
	}
	if !found {
		t.Error("websearch is missing when enabled")
	}
}

func TestCleanHTML(t *testing.T) {
	got := cleanHTML("  <b>Go</b> &amp; \n  more  ")
	if got != "Go & more" {
		t.Errorf("cleanHTML = %q", got)
	}
}

func TestTruncateRunes(t *testing.T) {
	// Must cut on runes, not bytes, or a multi-byte snippet is corrupted.
	if got := truncateRunes("日本語です", 3); got != "日本語…" {
		t.Errorf("truncateRunes = %q", got)
	}
	if got := truncateRunes("short", 100); got != "short" {
		t.Errorf("short text was altered: %q", got)
	}
}
