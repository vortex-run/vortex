package proxyhttp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// Handler defaults.
const (
	defaultRetries       = 2
	maxRetries           = 3
	defaultTimeout       = 30 * time.Second
	defaultFlushInterval = 100 * time.Millisecond
)

// defaultRetryStatuses are the 5xx codes retried by default.
var defaultRetryStatuses = []int{
	http.StatusBadGateway,         // 502
	http.StatusServiceUnavailable, // 503
	http.StatusGatewayTimeout,     // 504
}

// HandlerConfig configures a reverse-proxy Handler.
type HandlerConfig struct {
	Backends      []BackendAddr
	Balancer      string // "round-robin" | "least-conn"
	RoundTripper  http.RoundTripper
	Retries       int           // default 2, capped at 3
	RetryOn       []int         // HTTP status codes to retry; default 502/503/504
	Timeout       time.Duration // per-request; default 30s
	FlushInterval time.Duration // streaming flush cadence; default 100ms
}

// Handler is an http.Handler that reverse-proxies requests to backends with
// load balancing, retry-on-5xx, per-request timeout, and streaming support.
type Handler struct {
	balancer      Balancer
	rt            http.RoundTripper
	retries       int
	retryOn       map[int]bool
	timeout       time.Duration
	flushInterval time.Duration
}

// NewHandler validates cfg and builds a Handler.
func NewHandler(cfg HandlerConfig) (*Handler, error) {
	if len(cfg.Backends) == 0 {
		return nil, ErrNoBackends
	}
	bal, err := NewBalancer(cfg.Balancer, cfg.Backends)
	if err != nil {
		return nil, err
	}
	rt := cfg.RoundTripper
	if rt == nil {
		rt = NewRoundTripper(RoundTripperConfig{})
	}

	retries := cfg.Retries
	if retries <= 0 {
		retries = defaultRetries
	}
	if retries > maxRetries {
		retries = maxRetries
	}

	retryOn := cfg.RetryOn
	if len(retryOn) == 0 {
		retryOn = defaultRetryStatuses
	}
	retrySet := make(map[int]bool, len(retryOn))
	for _, c := range retryOn {
		retrySet[c] = true
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	flush := cfg.FlushInterval
	if flush <= 0 {
		flush = defaultFlushInterval
	}

	return &Handler{
		balancer:      bal,
		rt:            rt,
		retries:       retries,
		retryOn:       retrySet,
		timeout:       timeout,
		flushInterval: flush,
	}, nil
}

// ServeHTTP proxies req to a selected backend, retrying on transport errors and
// configured 5xx statuses up to the retry budget.
func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if isWebSocketUpgrade(req) {
		// Full WebSocket proxying lands in M2.6; until then, signal clearly.
		writeJSONError(w, http.StatusNotImplemented, "websocket support coming in M2.6")
		return
	}

	ctx, cancel := context.WithTimeout(req.Context(), h.timeout)
	defer cancel()

	attempts := h.retries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		backend, err := h.balancer.Next(req)
		if err != nil {
			writeJSONError(w, http.StatusBadGateway, "no backend available")
			return
		}

		outreq := req.Clone(ctx)
		outreq.URL.Scheme = "http"
		outreq.URL.Host = backend.Addr
		outreq.Host = req.Host
		outreq.RequestURI = ""

		start := time.Now()
		resp, err := h.rt.RoundTrip(outreq)
		if err != nil {
			lastErr = err
			h.balancer.RecordResult(backend.Addr, false, time.Since(start))
			if errors.Is(err, context.DeadlineExceeded) {
				writeJSONError(w, http.StatusGatewayTimeout, "backend timed out")
				return
			}
			continue // retry on transport error if budget remains
		}

		// Retry on configured 5xx statuses if we still have budget.
		if h.retryOn[resp.StatusCode] && attempt < attempts-1 {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			h.balancer.RecordResult(backend.Addr, false, time.Since(start))
			continue
		}

		h.balancer.RecordResult(backend.Addr, resp.StatusCode < 500, time.Since(start))
		h.writeResponse(w, resp)
		return
	}

	// Exhausted retries on transport errors.
	if errors.Is(lastErr, context.DeadlineExceeded) {
		writeJSONError(w, http.StatusGatewayTimeout, "backend timed out")
		return
	}
	writeJSONError(w, http.StatusBadGateway, "all backends failed")
}

// writeResponse copies a backend response to the client, stripping hop-by-hop
// headers and flushing periodically for streaming bodies.
func (h *Handler) writeResponse(w http.ResponseWriter, resp *http.Response) {
	defer func() { _ = resp.Body.Close() }()

	removeHopByHop(resp.Header)
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if isStreaming(resp) {
		h.streamBody(w, resp.Body)
		return
	}
	_, _ = io.Copy(w, resp.Body)
}

// streamBody copies the body to w incrementally, flushing as data arrives so
// streaming responses (SSE, chunked) reach the client without buffering. All
// writes and flushes happen on this single goroutine — the http.ResponseWriter
// is not safe for concurrent Write/Flush, so we must not copy in a background
// goroutine while flushing here.
func (h *Handler) streamBody(w http.ResponseWriter, body io.Reader) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		_, _ = io.Copy(w, body)
		return
	}
	buf := make([]byte, 32*1024)
	for {
		n, err := body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			flusher.Flush() // push each chunk to the client as it arrives
		}
		if err != nil {
			return // io.EOF or read error: done streaming
		}
	}
}

func isWebSocketUpgrade(req *http.Request) bool {
	return strings.EqualFold(req.Header.Get("Upgrade"), "websocket")
}

func isStreaming(resp *http.Response) bool {
	if strings.Contains(resp.Header.Get("Content-Type"), "text/event-stream") {
		return true
	}
	for _, te := range resp.TransferEncoding {
		if te == "chunked" {
			return true
		}
	}
	return false
}

func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func writeJSONError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
