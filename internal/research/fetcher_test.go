package research

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const samplePage = `<html>
<head><title>Go Web Frameworks</title></head>
<body>
<nav>Home About Contact</nav>
<script>console.log("ignore me");</script>
<style>.x{color:red}</style>
<article>
  <h1>Choosing a Framework</h1>
  <p>Gin is a fast HTTP web framework written in Go. It is widely used.</p>
</article>
<footer>Copyright 2026</footer>
</body>
</html>`

// loopbackFetcher returns a fetcher that may reach httptest (loopback) servers.
func loopbackFetcher() *Fetcher {
	return newFetcher(true)
}

func TestFetch_ExtractsCleanText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, samplePage)
	}))
	t.Cleanup(srv.Close)

	r, err := loopbackFetcher().Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if r.Title != "Go Web Frameworks" {
		t.Errorf("title = %q", r.Title)
	}
	if !strings.Contains(r.Content, "Gin is a fast HTTP web framework") {
		t.Errorf("content missing article text: %q", r.Content)
	}
	// script/style/nav/footer content must be stripped.
	for _, junk := range []string{"ignore me", "color:red", "Copyright 2026", "Home About Contact"} {
		if strings.Contains(r.Content, junk) {
			t.Errorf("content should not contain %q: %s", junk, r.Content)
		}
	}
	if r.WordCount == 0 {
		t.Error("word count should be > 0")
	}
}

func TestFetch_WordCountAccurate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "<html><body>one two three four five</body></html>")
	}))
	t.Cleanup(srv.Close)
	r, err := loopbackFetcher().Fetch(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if r.WordCount != 5 {
		t.Errorf("word count = %d, want 5", r.WordCount)
	}
}

func TestFetch_SSRFBlocksPrivateIPs(t *testing.T) {
	f := NewFetcher()
	for _, url := range []string{
		"http://127.0.0.1/secret",
		"http://localhost/x",
		"http://10.0.0.5/x",
		"http://192.168.1.1/x",
		"http://169.254.169.254/latest/meta-data/",
		"ftp://example.com/x",
	} {
		if _, err := f.Fetch(context.Background(), url); !errors.Is(err, ErrSSRFBlocked) {
			t.Errorf("Fetch(%q) err = %v, want ErrSSRFBlocked", url, err)
		}
	}
}

func TestFetch_TimeoutRespected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(w, samplePage)
	}))
	t.Cleanup(srv.Close)
	f := loopbackFetcher()
	f.client.Timeout = 30 * time.Millisecond
	if _, err := f.Fetch(context.Background(), srv.URL); err == nil {
		t.Error("slow server should hit the client timeout")
	}
}

func TestFetch_Non200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	if _, err := loopbackFetcher().Fetch(context.Background(), srv.URL); err == nil {
		t.Error("a 404 should error")
	}
}

func TestFetchMultiple_Concurrent(t *testing.T) {
	var concurrent, maxConcurrent atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := concurrent.Add(1)
		for {
			m := maxConcurrent.Load()
			if n <= m || maxConcurrent.CompareAndSwap(m, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		concurrent.Add(-1)
		_, _ = io.WriteString(w, samplePage)
	}))
	t.Cleanup(srv.Close)

	urls := make([]string, 8)
	for i := range urls {
		urls[i] = srv.URL
	}
	results, err := loopbackFetcher().FetchMultiple(context.Background(), urls)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 8 {
		t.Errorf("got %d results, want 8", len(results))
	}
	if maxConcurrent.Load() < 2 {
		t.Errorf("fetches did not run concurrently (max=%d)", maxConcurrent.Load())
	}
	if maxConcurrent.Load() > 5 {
		t.Errorf("concurrency cap exceeded: max=%d, want <= 5", maxConcurrent.Load())
	}
}

func TestFetchMultiple_SkipsErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, samplePage)
	}))
	t.Cleanup(srv.Close)
	// One good URL + one SSRF-blocked URL → only the good one returns.
	results, err := loopbackFetcher().FetchMultiple(context.Background(), []string{srv.URL, "http://10.0.0.9/x"})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("blocked URL should be skipped, got %d results", len(results))
	}
}
