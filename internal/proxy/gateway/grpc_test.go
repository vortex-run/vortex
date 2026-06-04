package proxygateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	proxyhttp "github.com/vortex-run/vortex/internal/proxy/http"
)

func grpcReq(t *testing.T, ct string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://svc/pkg.Service/Method", nil)
	req.Header.Set("Content-Type", ct)
	req.RemoteAddr = "203.0.113.5:1111"
	return req
}

func beAddr(t *testing.T, srv *httptest.Server) proxyhttp.BackendAddr {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return proxyhttp.BackendAddr{Addr: u.Host, Weight: 1}
}

func TestIsGRPC_Grpc(t *testing.T) {
	if !IsGRPC(grpcReq(t, "application/grpc")) {
		t.Error("application/grpc should be detected as gRPC")
	}
}

func TestIsGRPC_GrpcProto(t *testing.T) {
	if !IsGRPC(grpcReq(t, "application/grpc+proto")) {
		t.Error("application/grpc+proto should be gRPC")
	}
}

func TestIsGRPC_GrpcWeb(t *testing.T) {
	if !IsGRPC(grpcReq(t, "application/grpc-web")) {
		t.Error("application/grpc-web should be gRPC")
	}
}

func TestIsGRPC_JSON(t *testing.T) {
	if IsGRPC(grpcReq(t, "application/json")) {
		t.Error("application/json is not gRPC")
	}
}

func TestIsGRPC_Empty(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "http://svc/", nil)
	if IsGRPC(req) {
		t.Error("empty Content-Type is not gRPC")
	}
}

func TestGRPCProxy_NoBackendsError(t *testing.T) {
	if _, err := NewGRPCProxy(GRPCProxyConfig{}); err == nil {
		t.Error("expected error with no backends")
	}
}

func TestGRPCProxy_ForwardsRequest(t *testing.T) {
	var gotCT string
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
	}))
	defer be.Close()

	p, err := NewGRPCProxy(GRPCProxyConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, grpcReq(t, "application/grpc"))

	if gotCT != "application/grpc" {
		t.Errorf("backend Content-Type = %q, want application/grpc", gotCT)
	}
	if rec.Result().StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Result().StatusCode)
	}
}

func TestGRPCProxy_PreservesTrailers(t *testing.T) {
	// Backend declares trailers, then sets them after writing the body.
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Trailer", "Grpc-Status, Grpc-Message")
		w.Header().Set("Content-Type", "application/grpc")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "response")
		w.Header().Set("Grpc-Status", "0")
		w.Header().Set("Grpc-Message", "OK")
	}))
	defer be.Close()

	p, _ := NewGRPCProxy(GRPCProxyConfig{Backends: []proxyhttp.BackendAddr{beAddr(t, be)}})

	// Drive through a real server so trailers are transmitted on the wire.
	front := httptest.NewServer(p)
	defer front.Close()

	req, _ := http.NewRequest(http.MethodPost, front.URL+"/pkg.Service/M", nil)
	req.Header.Set("Content-Type", "application/grpc")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.ReadAll(resp.Body) // trailers are populated after the body is read

	if got := resp.Trailer.Get("Grpc-Status"); got != "0" {
		t.Errorf("Grpc-Status trailer = %q, want 0", got)
	}
	if got := resp.Trailer.Get("Grpc-Message"); got != "OK" {
		t.Errorf("Grpc-Message trailer = %q, want OK", got)
	}
}

func TestGRPCProxy_BackendUnreachable(t *testing.T) {
	// Reserve a port with nothing listening.
	probe := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := beAddr(t, probe)
	probe.Close() // now nothing is listening on dead.Addr

	p, _ := NewGRPCProxy(GRPCProxyConfig{Backends: []proxyhttp.BackendAddr{dead}})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, grpcReq(t, "application/grpc"))

	if got := rec.Header().Get("Grpc-Status"); got != grpcStatusUnavailable {
		t.Errorf("Grpc-Status = %q, want %s (UNAVAILABLE)", got, grpcStatusUnavailable)
	}
}
