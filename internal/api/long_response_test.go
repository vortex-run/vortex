package api

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestAllowLongResponse_SurvivesWriteTimeout proves that a streaming handler
// that calls allowLongResponse keeps writing past the server's WriteTimeout,
// while one that does not is severed. This is the regression test for the
// production-audit-I4 timeout cutting off SSE streams and slow AI responses.
func TestAllowLongResponse_SurvivesWriteTimeout(t *testing.T) {
	stream := func(opt bool) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			if opt {
				allowLongResponse(w)
			}
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			// Write chunks for ~3x the server's WriteTimeout.
			for i := 0; i < 6; i++ {
				if _, err := fmt.Fprintf(w, "data: {\"chunk\":\"c%d\"}\n\n", i); err != nil {
					return
				}
				fl.Flush()
				time.Sleep(100 * time.Millisecond)
			}
			_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
			fl.Flush()
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/opted", stream(true))
	mux.HandleFunc("/naive", stream(false))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: mux, WriteTimeout: 200 * time.Millisecond}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	read := func(path string) (chunks int, done bool) {
		resp, err := http.Get("http://" + ln.Addr().String() + path)
		if err != nil {
			return 0, false
		}
		defer func() { _ = resp.Body.Close() }()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			line := sc.Text()
			if strings.Contains(line, "[DONE]") {
				done = true
			} else if strings.HasPrefix(line, "data:") {
				chunks++
			}
		}
		return chunks, done
	}

	chunks, done := read("/opted")
	if !done || chunks != 6 {
		t.Errorf("opted-out stream: chunks=%d done=%v, want all 6 chunks and [DONE] past the WriteTimeout", chunks, done)
	}

	// Sanity check that the timeout is real: the naive handler must be cut
	// off before completing (otherwise this test proves nothing).
	chunks, done = read("/naive")
	if done && chunks == 6 {
		t.Errorf("naive stream survived the WriteTimeout — the regression this test guards is not being exercised")
	}
}
