package proxygateway

import (
	"errors"
	"net/http"
	"sync/atomic"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
)

// GatewayConfig configures a Gateway.
type GatewayConfig struct {
	// HTTPHandler handles plain HTTP requests (the M2.2 proxy handler). Required.
	HTTPHandler http.Handler
	// GRPCProxy handles application/grpc requests. Optional: when nil, gRPC
	// requests fall through to HTTPHandler.
	GRPCProxy *GRPCProxy
	// TCPPool backs WebSocket tunneling. Optional (a direct dial is used if nil).
	TCPPool *tcp.Pool
	// Sticky pins a client IP to a backend across WebSocket reconnects. Optional.
	Sticky *StickySession
	// WSBackends are the upstreams for WebSocket routing. When empty, WebSocket
	// upgrades fall through to HTTPHandler (which returns its WS stub).
	WSBackends []proxyhttp.BackendAddr
}

// GatewayStats counts requests dispatched by protocol.
type GatewayStats struct {
	WebSocketConns int64
	GRPCRequests   int64
	HTTPRequests   int64
}

// Gateway dispatches each request to the WebSocket proxy, gRPC proxy, or plain
// HTTP handler, in that priority order.
type Gateway struct {
	httpHandler http.Handler
	grpcProxy   *GRPCProxy
	pool        *tcp.Pool
	sticky      *StickySession
	wsBalancer  proxyhttp.Balancer

	wsConns  atomic.Int64
	grpcReqs atomic.Int64
	httpReqs atomic.Int64
}

// NewGateway validates cfg and constructs a Gateway.
func NewGateway(cfg GatewayConfig) (*Gateway, error) {
	if cfg.HTTPHandler == nil {
		return nil, errors.New("proxygateway: HTTPHandler is required")
	}
	g := &Gateway{
		httpHandler: cfg.HTTPHandler,
		grpcProxy:   cfg.GRPCProxy,
		pool:        cfg.TCPPool,
		sticky:      cfg.Sticky,
	}
	if len(cfg.WSBackends) > 0 {
		rr, err := proxyhttp.NewRoundRobinBalancer(cfg.WSBackends)
		if err != nil {
			return nil, err
		}
		g.wsBalancer = rr
	}
	return g, nil
}

// ServeHTTP dispatches by protocol: WebSocket upgrade → gRPC → plain HTTP.
func (g *Gateway) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case IsWebSocketUpgrade(req) && g.wsBalancer != nil:
		g.serveWebSocket(w, req)
	case IsGRPC(req) && g.grpcProxy != nil:
		g.grpcReqs.Add(1)
		g.grpcProxy.ServeHTTP(w, req)
	default:
		g.httpReqs.Add(1)
		g.httpHandler.ServeHTTP(w, req)
	}
}

// serveWebSocket routes a WebSocket upgrade to a backend, honouring stickiness
// so a client IP keeps using the same backend across reconnects.
func (g *Gateway) serveWebSocket(w http.ResponseWriter, req *http.Request) {
	ip := clientIP(req.RemoteAddr)

	backend := ""
	if g.sticky != nil {
		if b, ok := g.sticky.Get(ip); ok {
			backend = b
		}
	}
	if backend == "" {
		be, err := g.wsBalancer.Next(req)
		if err != nil {
			http.Error(w, "no websocket backend available", http.StatusBadGateway)
			return
		}
		backend = be.Addr
		if g.sticky != nil {
			g.sticky.Set(ip, backend)
		}
	}

	g.wsConns.Add(1)
	defer g.wsConns.Add(-1)
	if err := ProxyWebSocket(w, req, backend, g.pool); err != nil {
		// On failure, drop the sticky binding so the next attempt re-balances.
		if g.sticky != nil {
			g.sticky.Delete(ip)
		}
	}
}

// Stats returns a snapshot of per-protocol request counts.
func (g *Gateway) Stats() GatewayStats {
	return GatewayStats{
		WebSocketConns: g.wsConns.Load(),
		GRPCRequests:   g.grpcReqs.Load(),
		HTTPRequests:   g.httpReqs.Load(),
	}
}
