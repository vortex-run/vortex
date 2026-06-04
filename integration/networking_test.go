//go:build integration

package integration

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/testutil"
)

// netConfig builds a vortex.cue with the given routes block. observability uses
// stderr logging so the started process does not need a log directory.
func netConfig(routes string) string {
	return `cluster: { name: "net-test" }
tls: { acme_email: "a@b.com", provider: "internal", min_version: "TLS1.2" }
routes: [` + routes + `]
security: {}
secrets: {}
observability: { log_level: "info", log_sink: "stderr" }
`
}

// httpRoute renders an http route block targeting backend host:port. No host is
// set so the route matches any Host header (path-only "/*"), which lets tests
// reach it via 127.0.0.1.
func httpRoute(name string, listen int, beHost string, bePort int) string {
	return fmt.Sprintf(`{name: %q, protocol: "http", listen: %d, backends: [{host: %q, port: %d}]}`,
		name, listen, beHost, bePort)
}

// tcpRoute renders a tcp route block.
func tcpRoute(name string, listen, bePort int) string {
	return fmt.Sprintf(`{name: %q, protocol: "tcp", listen: %d, backends: [{host: "127.0.0.1", port: %d}]}`,
		name, listen, bePort)
}

// hostPort splits an httptest server URL into host and port.
func hostPort(t *testing.T, srv *httptest.Server) (string, int) {
	t.Helper()
	u, _ := url.Parse(srv.URL)
	h, p, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(p)
	return h, port
}

// tcpEcho starts a TCP echo server, returning its port.
func tcpEcho(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return ln.Addr().(*net.TCPAddr).Port
}

func TestNetworking_HealthShowsRoutes(t *testing.T) {
	bin := testutil.BuildBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	routes := httpRoute("test-http", testutil.FreePort(t), beHost, bePort) + "," +
		tcpRoute("test-tcp", testutil.FreePort(t), tcpEcho(t))
	cfg := testutil.WriteTestConfig(t, netConfig(routes))

	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	h := p.Health(t)
	raw, ok := h["routes"].([]any)
	if !ok {
		t.Fatalf("health response missing routes array: %v", h["routes"])
	}
	if len(raw) != 2 {
		t.Fatalf("routes len = %d, want 2", len(raw))
	}
	names := map[string]string{}
	for _, r := range raw {
		m := r.(map[string]any)
		names[m["name"].(string)] = m["protocol"].(string)
	}
	if names["test-http"] != "http" {
		t.Errorf("test-http protocol = %q, want http", names["test-http"])
	}
	if names["test-tcp"] != "tcp" {
		t.Errorf("test-tcp protocol = %q, want tcp", names["test-tcp"])
	}
}

func TestNetworking_HTTPRouteServesTraffic(t *testing.T) {
	bin := testutil.BuildBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "traffic ok")
	}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(httpRoute("web", listen, beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	resp, err := http.Get("http://" + addr + "/")
	if err != nil {
		t.Fatalf("GET through vortex: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "traffic ok" {
		t.Errorf("body = %q, want 'traffic ok'", body)
	}
}

func TestNetworking_TCPRouteServesTraffic(t *testing.T) {
	bin := testutil.BuildBinary(t)
	bePort := tcpEcho(t)
	listen := testutil.FreePort(t)
	cfg := testutil.WriteTestConfig(t, netConfig(tcpRoute("db", listen, bePort)))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	addr := "127.0.0.1:" + strconv.Itoa(listen)
	waitListening(t, addr)

	got, err := tcpRoundTrip(addr, []byte("tcp traffic test"))
	if err != nil {
		t.Fatalf("tcp round trip: %v", err)
	}
	if string(got) != "tcp traffic test" {
		t.Errorf("echo = %q, want 'tcp traffic test'", got)
	}
}

func TestNetworking_MultipleRoutesSimultaneous(t *testing.T) {
	bin := testutil.BuildBinary(t)
	httpBE := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "http route ok")
	}))
	defer httpBE.Close()
	beHost, bePort := hostPort(t, httpBE)

	httpListen := testutil.FreePort(t)
	tcpListen := testutil.FreePort(t)
	routes := httpRoute("web", httpListen, beHost, bePort) + "," + tcpRoute("db", tcpListen, tcpEcho(t))
	cfg := testutil.WriteTestConfig(t, netConfig(routes))
	p := testutil.StartVortex(t, bin, cfg)
	defer p.Stop(t)

	httpAddr := "127.0.0.1:" + strconv.Itoa(httpListen)
	tcpAddr := "127.0.0.1:" + strconv.Itoa(tcpListen)
	waitListening(t, httpAddr)
	waitListening(t, tcpAddr)

	var wg sync.WaitGroup
	wg.Add(2)
	var httpErr, tcpErr error
	go func() {
		defer wg.Done()
		resp, err := http.Get("http://" + httpAddr + "/")
		if err != nil {
			httpErr = err
			return
		}
		defer func() { _ = resp.Body.Close() }()
		b, _ := io.ReadAll(resp.Body)
		if string(b) != "http route ok" {
			httpErr = fmt.Errorf("http body = %q", b)
		}
	}()
	go func() {
		defer wg.Done()
		got, err := tcpRoundTrip(tcpAddr, []byte("simul-tcp"))
		if err != nil {
			tcpErr = err
			return
		}
		if string(got) != "simul-tcp" {
			tcpErr = fmt.Errorf("tcp echo = %q", got)
		}
	}()
	wg.Wait()
	if httpErr != nil {
		t.Errorf("HTTP route: %v", httpErr)
	}
	if tcpErr != nil {
		t.Errorf("TCP route: %v", tcpErr)
	}
}

func TestNetworking_ConfigReloadUpdatesRoutes(t *testing.T) {
	bin := testutil.BuildBinary(t)
	be := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer be.Close()
	beHost, bePort := hostPort(t, be)

	cfgPath := testutil.WriteTestConfig(t, netConfig(httpRoute("r1", testutil.FreePort(t), beHost, bePort)))
	p := testutil.StartVortex(t, bin, cfgPath)
	defer p.Stop(t)

	if got := len(p.Health(t)["routes"].([]any)); got != 1 {
		t.Fatalf("initial routes = %d, want 1", got)
	}

	// Rewrite config with two routes and reload.
	two := httpRoute("r1", testutil.FreePort(t), beHost, bePort) + "," +
		httpRoute("r2", testutil.FreePort(t), beHost, bePort)
	if err := writeFile(cfgPath, netConfig(two)); err != nil {
		t.Fatal(err)
	}
	if out, code := p.Run(t, "reload", "--config", cfgPath); code != 0 {
		t.Fatalf("reload failed (%d): %s", code, out)
	}
	time.Sleep(700 * time.Millisecond)

	if got := len(p.Health(t)["routes"].([]any)); got != 2 {
		t.Errorf("after reload routes = %d, want 2", got)
	}
}

// waitListening waits until addr accepts a TCP connection.
func waitListening(t *testing.T, addr string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("tcp", addr, 100*time.Millisecond); err == nil {
			_ = c.Close()
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("nothing listening on %s within 5s", addr)
}

// tcpRoundTrip sends payload to addr and reads the echoed reply.
func tcpRoundTrip(addr string, payload []byte) ([]byte, error) {
	c, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write(payload); err != nil {
		return nil, err
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(c, got); err != nil {
		return nil, err
	}
	return got, nil
}
