package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
)

// loadRoutes writes a config with TCP routes on the given ports (all pointing
// at backend) and loads it, so it carries a real hash.
func loadRoutes(t *testing.T, backend int, ports ...int) *config.Config {
	t.Helper()
	src := "cluster: { name: \"dp\" }\ntls: { provider: \"internal\", acme_email: \"dp@example.com\" }\nroutes: [\n"
	for i, p := range ports {
		src += fmt.Sprintf("  { name: \"r%d\", protocol: \"tcp\", listen: %d, backends: [{ host: \"127.0.0.1\", port: %d }] },\n", i, p, backend)
	}
	src += "]\n"
	path := filepath.Join(t.TempDir(), "vortex.cue")
	if err := os.WriteFile(path, []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// freeTCPPort returns a loopback port that was free a moment ago.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

// dialOK reports whether something accepts connections on 127.0.0.1:port.
func dialOK(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

// TestDataPlane_FailedRebuildRestoresPreviousRoutes: a reload that adds a
// route on a port already in use must not take the existing routes down.
func TestDataPlane_FailedRebuildRestoresPreviousRoutes(t *testing.T) {
	t.Setenv("VORTEX_EDGE_BIND_HOST", "127.0.0.1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	busy, err := net.Listen("tcp", "127.0.0.1:0") // someone else's port
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = busy.Close() }()
	busyPort := busy.Addr().(*net.TCPAddr).Port

	pool := tcp.NewPool(tcp.PoolConfig{})
	defer func() { _ = pool.Close() }()
	dp := &dataPlane{ctx: ctx, pool: pool, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	defer func() { _ = dp.stop(context.Background()) }()

	a := freeTCPPort(t)
	good := loadRoutes(t, busyPort, a)
	holder := config.NewHolder(good)
	if err := dp.rebuild(holder); err != nil {
		t.Fatalf("initial rebuild: %v", err)
	}
	if !dialOK(a) {
		t.Fatal("route r0 not listening after initial rebuild")
	}

	// Reload: keep r0, add r1 on the busy port.
	holder.Store(loadRoutes(t, busyPort, a, busyPort))
	if err := dp.rebuild(holder); err == nil {
		t.Fatal("rebuild with a busy port returned nil, want the bind error")
	}
	if !dialOK(a) {
		t.Error("route r0 is down after a failed reload; previous routes were not restored")
	}
	if holder.Get().Hash() != good.Hash() {
		t.Error("holder still points at the rejected config; /health would report a config that isn't running")
	}
	if dp.lastRebuildError() == nil {
		t.Error("lastRebuildError is nil after a failed rebuild")
	}

	// A rebuild with the running config (a reload whose file failed
	// validation) is a no-op: the listener isn't restarted.
	before := dp.current
	if err := dp.rebuild(holder); err != nil {
		t.Fatalf("no-op rebuild: %v", err)
	}
	if dp.current != before {
		t.Error("rebuild with an unchanged config restarted the listeners")
	}
	if dp.lastRebuildError() != nil {
		t.Error("lastRebuildError not cleared by a successful (no-op) rebuild")
	}
}
