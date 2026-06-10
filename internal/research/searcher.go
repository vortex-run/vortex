// Package research implements VORTEX's research agent (build plan M15): web
// search, content fetching, AI summarization, and report generation. It is
// stdlib-only — searches scrape the DuckDuckGo HTML interface (no API key) and
// all HTTP uses net/http.
//
// This file implements the web searcher.
package research

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ddgUserAgent is sent on search requests.
const ddgUserAgent = "VORTEX Research Agent/1.0"

// ddgHTMLEndpoint is the DuckDuckGo HTML (no-JS) search endpoint.
const ddgHTMLEndpoint = "https://html.duckduckgo.com/html/"

// SearchResult is one search hit.
type SearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
	Source  string `json:"source"`
}

// Searcher performs web searches against the DuckDuckGo HTML interface.
type Searcher struct {
	client   *http.Client
	endpoint string // overridable for tests
}

// NewSearcher constructs a searcher with a 10s HTTP timeout.
func NewSearcher() *Searcher {
	return &Searcher{
		client:   &http.Client{Timeout: 10 * time.Second},
		endpoint: ddgHTMLEndpoint,
	}
}

// Search returns up to limit results for query. An empty query is an error.
func (s *Searcher) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	return s.search(ctx, query, limit, false)
}

// SearchNews returns news results for query (DDG &ia=news).
func (s *Searcher) SearchNews(ctx context.Context, query string) ([]SearchResult, error) {
	return s.search(ctx, query, 10, true)
}

// search performs the HTTP request and parses the HTML.
func (s *Searcher) search(ctx context.Context, query string, limit int, news bool) ([]SearchResult, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("research: empty search query")
	}
	if limit <= 0 {
		limit = 10
	}

	q := url.Values{}
	q.Set("q", query)
	if news {
		q.Set("ia", "news")
	}
	reqURL := s.endpoint + "?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ddgUserAgent)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("research: search returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	results := parseDDGResults(string(body), limit)
	source := "web"
	if news {
		source = "news"
	}
	for i := range results {
		results[i].Source = source
	}
	return results, nil
}

// Trending returns a small list of trending topics. DDG has no stable public
// trending feed, so this returns a fixed seed set — useful as a fallback when no
// query is supplied. (Stdlib-only; no third-party trends API.)
func (s *Searcher) Trending() ([]string, error) {
	return []string{
		"artificial intelligence",
		"open source software",
		"cloud infrastructure",
		"cybersecurity",
		"go programming",
	}, nil
}

// --- HTML parsing -----------------------------------------------------------

// resultLinkRe matches a DDG result anchor: class result__a, captures the href
// (DDG wraps it in a uddg redirect) and the visible title text.
var resultLinkRe = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__a[^"]*"[^>]+href="([^"]+)"[^>]*>(.*?)</a>`)

// snippetRe matches the result snippet block.
var snippetRe = regexp.MustCompile(`(?s)<a[^>]+class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>`)

// tagRe strips HTML tags.
var tagRe = regexp.MustCompile(`<[^>]+>`)

// parseDDGResults extracts up to limit results from the DDG HTML page.
func parseDDGResults(html string, limit int) []SearchResult {
	links := resultLinkRe.FindAllStringSubmatch(html, -1)
	snips := snippetRe.FindAllStringSubmatch(html, -1)

	out := make([]SearchResult, 0, len(links))
	for i, m := range links {
		if len(out) >= limit {
			break
		}
		rawHref := htmlUnescape(m[1])
		title := cleanText(m[2])
		if title == "" {
			continue
		}
		snippet := ""
		if i < len(snips) {
			snippet = cleanText(snips[i][1])
		}
		out = append(out, SearchResult{
			Title:   title,
			URL:     resolveDDGURL(rawHref),
			Snippet: snippet,
		})
	}
	return out
}

// resolveDDGURL unwraps DuckDuckGo's /l/?uddg=<encoded> redirect to the real URL.
func resolveDDGURL(href string) string {
	// DDG HTML wraps results as //duckduckgo.com/l/?uddg=<encoded-url>&...
	idx := strings.Index(href, "uddg=")
	if idx < 0 {
		if strings.HasPrefix(href, "//") {
			return "https:" + href
		}
		return href
	}
	enc := href[idx+len("uddg="):]
	if amp := strings.IndexByte(enc, '&'); amp >= 0 {
		enc = enc[:amp]
	}
	if dec, err := url.QueryUnescape(enc); err == nil {
		return dec
	}
	return href
}

// cleanText strips tags and collapses whitespace from an HTML fragment.
func cleanText(s string) string {
	s = tagRe.ReplaceAllString(s, "")
	s = htmlUnescape(s)
	return strings.Join(strings.Fields(s), " ")
}

// htmlUnescape replaces the few HTML entities DDG emits.
func htmlUnescape(s string) string {
	r := strings.NewReplacer(
		"&amp;", "&", "&lt;", "<", "&gt;", ">", "&quot;", `"`, "&#39;", "'", "&#x27;", "'", "&nbsp;", " ",
	)
	return r.Replace(s)
}
