package research

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ddgHTML is a minimal DuckDuckGo HTML result page (2 results).
const ddgHTML = `<html><body>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fdoc&rut=x">The Go <b>Documentation</b></a>
  <a class="result__snippet" href="#">Official Go language <b>documentation</b> and tutorials.</a>
</div>
<div class="result">
  <a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgin-gonic.com">Gin Web Framework</a>
  <a class="result__snippet" href="#">A fast HTTP web framework written in Go.</a>
</div>
</body></html>`

// fakeDDG returns an httptest server serving ddgHTML and records the query.
func fakeDDG(t *testing.T) (*Searcher, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery = r.URL.Query().Get("q")
		_, _ = io.WriteString(w, ddgHTML)
	}))
	t.Cleanup(srv.Close)
	s := NewSearcher()
	s.endpoint = srv.URL
	return s, &lastQuery
}

func TestSearch_ReturnsParsedResults(t *testing.T) {
	s, lastQuery := fakeDDG(t)
	res, err := s.Search(context.Background(), "golang web frameworks", 5)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if *lastQuery != "golang web frameworks" {
		t.Errorf("query sent = %q", *lastQuery)
	}
	if len(res) != 2 {
		t.Fatalf("got %d results, want 2", len(res))
	}
	// First result: title tags stripped, uddg unwrapped to the real URL.
	if res[0].Title != "The Go Documentation" {
		t.Errorf("title = %q", res[0].Title)
	}
	if res[0].URL != "https://go.dev/doc" {
		t.Errorf("url = %q, want unwrapped https://go.dev/doc", res[0].URL)
	}
	if !strings.Contains(res[0].Snippet, "documentation and tutorials") {
		t.Errorf("snippet = %q", res[0].Snippet)
	}
	if res[0].Source != "web" {
		t.Errorf("source = %q, want web", res[0].Source)
	}
}

func TestSearch_RespectsLimit(t *testing.T) {
	s, _ := fakeDDG(t)
	res, err := s.Search(context.Background(), "go", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 {
		t.Errorf("limit=1 should return 1 result, got %d", len(res))
	}
}

func TestSearchNews_SetsNewsParam(t *testing.T) {
	var iaParam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		iaParam = r.URL.Query().Get("ia")
		_, _ = io.WriteString(w, ddgHTML)
	}))
	t.Cleanup(srv.Close)
	s := NewSearcher()
	s.endpoint = srv.URL

	res, err := s.SearchNews(context.Background(), "ai breakthroughs")
	if err != nil {
		t.Fatal(err)
	}
	if iaParam != "news" {
		t.Errorf("ia param = %q, want news", iaParam)
	}
	if len(res) > 0 && res[0].Source != "news" {
		t.Errorf("news result source = %q, want news", res[0].Source)
	}
}

func TestSearch_EmptyQueryErrors(t *testing.T) {
	s := NewSearcher()
	if _, err := s.Search(context.Background(), "   ", 5); err == nil {
		t.Error("empty query should error")
	}
}

func TestSearch_TimeoutRespected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, ddgHTML)
	}))
	t.Cleanup(srv.Close)
	s := NewSearcher()
	s.endpoint = srv.URL
	s.client.Timeout = 30 * time.Millisecond

	if _, err := s.Search(context.Background(), "go", 5); err == nil {
		t.Error("a slow server should trigger the client timeout")
	}
}

func TestSearch_HandlesMissingFields(t *testing.T) {
	// A result with a title but no snippet should still parse (empty snippet).
	html := `<a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com">Example</a>`
	res := parseDDGResults(html, 5)
	if len(res) != 1 || res[0].Title != "Example" || res[0].Snippet != "" {
		t.Errorf("missing snippet should parse gracefully: %+v", res)
	}
}

func TestResolveDDGURL(t *testing.T) {
	cases := map[string]string{
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev&rut=z": "https://go.dev",
		"//example.com/page":         "https://example.com/page",
		"https://direct.example.com": "https://direct.example.com",
	}
	for in, want := range cases {
		if got := resolveDDGURL(in); got != want {
			t.Errorf("resolveDDGURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTrending_ReturnsTopics(t *testing.T) {
	s := NewSearcher()
	topics, err := s.Trending()
	if err != nil || len(topics) == 0 {
		t.Errorf("Trending should return topics, got %v / %v", topics, err)
	}
}
