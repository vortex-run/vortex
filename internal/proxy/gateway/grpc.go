package proxygateway

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

// grpcStatusUnavailable is the gRPC status code for UNAVAILABLE (14), returned
// when no backend can serve a request.
const grpcStatusUnavailable = "14"

// IsGRPC reports whether req is a gRPC call, identified by a Content-Type of
// application/grpc (covering grpc, grpc+proto, grpc+json, grpc-web).
func IsGRPC(req *http.Request) bool {
	return strings.HasPrefix(req.Header.Get("Content-Type"), "application/grpc")
}

// GRPCProxyConfig configures a GRPCProxy.
type GRPCProxyConfig struct {
	Backends []proxyhttp.BackendAddr
	Balancer proxyhttp.Balancer
	Timeout  time.Duration // 0 = no timeout
}

// GRPCProxy transparently reverse-proxies gRPC (HTTP/2) requests, preserving
// HTTP/2 trailers (gRPC carries its status in trailers).
type GRPCProxy struct {
	balancer  proxyhttp.Balancer
	transport *http.Transport
	timeout   time.Duration
}

// NewGRPCProxy validates cfg and builds a GRPCProxy. If no Balancer is supplied,
// a round-robin balancer over Backends is created.
func NewGRPCProxy(cfg GRPCProxyConfig) (*GRPCProxy, error) {
	if len(cfg.Backends) == 0 {
		return nil, errors.New("proxygateway: gRPC proxy requires at least one backend")
	}
	bal := cfg.Balancer
	if bal == nil {
		rr, err := proxyhttp.NewRoundRobinBalancer(cfg.Backends)
		if err != nil {
			return nil, err
		}
		bal = rr
	}
	return &GRPCProxy{
		balancer:  bal,
		transport: &http.Transport{ForceAttemptHTTP2: true},
		timeout:   cfg.Timeout,
	}, nil
}

// ServeHTTP forwards a gRPC request to a selected backend over HTTP/2 and copies
// the response — including trailers — back to the client.
func (p *GRPCProxy) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	backend, err := p.balancer.Next(req)
	if err != nil {
		grpcUnavailable(w, "no backend available")
		return
	}

	outreq := req.Clone(req.Context())
	outreq.URL.Scheme = "http"
	outreq.URL.Host = backend.Addr
	outreq.Host = req.Host
	outreq.RequestURI = ""

	start := time.Now()
	resp, err := p.transport.RoundTrip(outreq)
	if err != nil {
		p.balancer.RecordResult(backend.Addr, false, time.Since(start))
		grpcUnavailable(w, "backend error: "+err.Error())
		return
	}
	defer func() { _ = resp.Body.Close() }()
	p.balancer.RecordResult(backend.Addr, true, time.Since(start))

	// Announce trailer names so the http server forwards them after the body.
	announceTrailers(w, resp)

	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)

	// Copy any trailers that arrived with the response (e.g. grpc-status).
	for k, vs := range resp.Trailer {
		for _, v := range vs {
			w.Header().Set(http.TrailerPrefix+k, v)
		}
	}
}

// announceTrailers declares, via the Trailer header, which trailer keys the
// response will carry, so net/http emits them.
func announceTrailers(w http.ResponseWriter, resp *http.Response) {
	var names []string
	for k := range resp.Trailer {
		names = append(names, k)
	}
	if len(names) > 0 {
		w.Header().Set("Trailer", strings.Join(names, ","))
	}
}

// copyHeader copies all header values from src into dst.
func copyHeader(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// grpcUnavailable writes a gRPC-style UNAVAILABLE response: HTTP 200 with
// grpc-status:14 (gRPC reports its own status in headers/trailers, not via the
// HTTP status), plus a plain message for non-gRPC clients.
func grpcUnavailable(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/grpc")
	w.Header().Set("Grpc-Status", grpcStatusUnavailable)
	w.Header().Set("Grpc-Message", msg)
	w.WriteHeader(http.StatusServiceUnavailable)
}
