package research

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// maxFetchWords caps extracted content to keep summaries bounded.
const maxFetchWords = 10000

// ErrSSRFBlocked is returned when a fetch targets a private/loopback/link-local
// address (same SSRF protection as the http_get tool).
var ErrSSRFBlocked = fmt.Errorf("research: SSRF target blocked")

// FetchResult is cleaned page content.
type FetchResult struct {
	URL       string    `json:"url"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	WordCount int       `json:"word_count"`
	FetchedAt time.Time `json:"fetched_at"`
}

// Fetcher downloads URLs and extracts clean text.
type Fetcher struct {
	client *http.Client
	// allowLoopback permits fetching loopback addresses; set only by tests so
	// they can reach an httptest server. Production fetches always SSRF-guard.
	allowLoopback bool
}

// NewFetcher constructs a fetcher with a 15s timeout.
func NewFetcher() *Fetcher {
	return &Fetcher{client: &http.Client{Timeout: 15 * time.Second}}
}

// Fetch downloads url and returns its cleaned text content.
func (f *Fetcher) Fetch(ctx context.Context, url string) (*FetchResult, error) {
	if err := checkSSRF(url, f.allowLoopback); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", ddgUserAgent)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("research: fetch %s returned %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	title, content := extractContent(string(body))
	words := strings.Fields(content)
	if len(words) > maxFetchWords {
		words = words[:maxFetchWords]
		content = strings.Join(words, " ")
	}
	return &FetchResult{
		URL: url, Title: title, Content: content,
		WordCount: len(words), FetchedAt: time.Now(),
	}, nil
}

// FetchMultiple fetches up to 5 URLs concurrently, returning all successful
// results (errors on individual URLs are skipped, not fatal).
func (f *Fetcher) FetchMultiple(ctx context.Context, urls []string) ([]*FetchResult, error) {
	const maxConcurrent = 5
	sem := make(chan struct{}, maxConcurrent)
	var (
		mu      sync.Mutex
		results []*FetchResult
		wg      sync.WaitGroup
	)
	for _, u := range urls {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if r, err := f.Fetch(ctx, url); err == nil {
				mu.Lock()
				results = append(results, r)
				mu.Unlock()
			}
		}(u)
	}
	wg.Wait()
	return results, nil
}

// --- SSRF protection --------------------------------------------------------

// metadataIPs are cloud metadata endpoints that must never be fetched.
var metadataIPs = map[string]bool{
	"169.254.169.254": true,
	"fd00:ec2::254":   true,
}

// blockedIP reports whether ip is loopback, link-local, private, or metadata.
func blockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	if metadataIPs[ip.String()] {
		return true
	}
	return ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsPrivate() || ip.IsUnspecified()
}

// checkSSRF rejects non-http(s) schemes and private/loopback hosts. When
// allowLoopback is true (tests only) loopback addresses are permitted.
func checkSSRF(rawURL string, allowLoopback bool) error {
	u, err := neturl.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: invalid URL: %v", ErrSSRFBlocked, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only http/https URLs allowed", ErrSSRFBlocked)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrSSRFBlocked)
	}
	blocked := func(ip net.IP) bool {
		if allowLoopback && ip != nil && ip.IsLoopback() {
			return false
		}
		return blockedIP(ip)
	}
	if ip := net.ParseIP(host); ip != nil {
		if blocked(ip) {
			return fmt.Errorf("%w: %s", ErrSSRFBlocked, host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrSSRFBlocked, host, err)
	}
	for _, ip := range ips {
		if blocked(ip) {
			return fmt.Errorf("%w: %s resolves to %s", ErrSSRFBlocked, host, ip)
		}
	}
	return nil
}

// --- HTML → clean text ------------------------------------------------------

var (
	titleRe   = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)
	dropRe    = regexp.MustCompile(`(?is)<(script|style|nav|footer|header|aside|noscript|svg)[^>]*>.*?</(script|style|nav|footer|header|aside|noscript|svg)>`)
	articleRe = regexp.MustCompile(`(?is)<(article|main)[^>]*>(.*?)</(article|main)>`)
	bodyRe    = regexp.MustCompile(`(?is)<body[^>]*>(.*?)</body>`)
	htmlTagRe = regexp.MustCompile(`<[^>]+>`)
)

// extractContent returns the page title and clean body text.
func extractContent(html string) (title, content string) {
	if m := titleRe.FindStringSubmatch(html); len(m) == 2 {
		title = cleanText(m[1])
	}
	// Remove non-content elements first.
	stripped := dropRe.ReplaceAllString(html, " ")
	// Prefer <article>/<main>, then <body>, else the whole document.
	region := stripped
	if m := articleRe.FindStringSubmatch(stripped); len(m) == 4 {
		region = m[2]
	} else if m := bodyRe.FindStringSubmatch(stripped); len(m) == 2 {
		region = m[1]
	}
	text := htmlTagRe.ReplaceAllString(region, " ")
	text = htmlUnescape(text)
	content = strings.Join(strings.Fields(text), " ")
	return title, content
}
