package api

import (
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/sandeepbollavaram/vortex/internal/config"
)

// TestListen_BusyPortIsAnError: a taken management port must surface as an
// error at startup, not a log line while the process runs on without an API.
func TestListen_BusyPortIsAnError(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	s := New(busy.Addr().String(), config.NewHolder(nil), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := s.Listen(); err == nil {
		t.Fatal("Listen on a busy port succeeded")
	}

	free := New("127.0.0.1:0", config.NewHolder(nil), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := free.Listen(); err != nil {
		t.Fatal(err)
	}
	_ = free.ln.Close()
}
