//go:build demo

// Command backends runs the throwaway upstream services that the VORTEX demo
// environment proxies to (scripts/demo/README.md).
//
// It exists so a recorded demo shows a working system. The checked-in
// vortex.cue points at 10.0.0.2 and myapp.com, which resolve to nothing, so
// every health check renders red and the Routes view is a wall of failures —
// accurate for a config template, useless for a screen recording.
//
// These backends are deliberately trivial: no dependencies, no state, and a
// /health endpoint that always succeeds, so the demo's green status reflects
// real health checks against real listeners rather than a mocked UI.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

// service is one fake upstream: an HTTP listener with a health endpoint and a
// body identifying which backend served the request, so a demo that shows
// load-balancing can show which instance answered.
type service struct {
	name string
	port int
}

var services = []service{
	{"frontend", 4000},
	{"api", 3000},
	{"staging", 4100},
}

// tcpEchoPort backs the L4 route in the demo config. TCP routes have no health
// endpoint, so this just has to accept connections for the route to be usable.
const tcpEchoPort = 15433

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var wg sync.WaitGroup
	servers := make([]*http.Server, 0, len(services))

	for _, svc := range services {
		mux := http.NewServeMux()
		mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","service":%q}`, svc.name)
		})
		mux.HandleFunc("/api/health", func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{"status":"ok","service":%q}`, svc.name)
		})
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			fmt.Fprintf(w, "%s backend — %s %s\n", svc.name, r.Method, r.URL.Path)
		})

		// Timeouts on a throwaway demo server are not ceremony: without them a
		// half-open connection from the traffic generator keeps a goroutine
		// alive for the whole recording session.
		srv := &http.Server{
			Addr:              fmt.Sprintf("127.0.0.1:%d", svc.port),
			Handler:           mux,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		servers = append(servers, srv)

		wg.Add(1)
		go func(s *http.Server, name string) {
			defer wg.Done()
			log.Printf("%s backend listening on %s", name, s.Addr)
			if err := s.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Printf("%s backend stopped: %v", name, err)
			}
		}(srv, svc.name)
	}

	// Bind to 127.0.0.1, not :port. Binding a wildcard address is what triggers
	// the Windows Firewall prompt, and the whole point of the demo harness is
	// that it starts without an interactive click.
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", tcpEchoPort))
	if err != nil {
		log.Printf("tcp echo backend unavailable on %d: %v", tcpEchoPort, err)
	} else {
		log.Printf("tcp echo backend listening on %s", ln.Addr())
		wg.Add(1)
		go func() {
			defer wg.Done()
			acceptEcho(ln)
		}()
		defer func() { _ = ln.Close() }()
	}

	log.Println("demo backends ready — Ctrl+C to stop")
	<-ctx.Done()
	log.Println("shutting down demo backends")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, srv := range servers {
		_ = srv.Shutdown(shutdownCtx)
	}
	if ln != nil {
		_ = ln.Close()
	}
	wg.Wait()
}

// acceptEcho serves the TCP route's upstream. It returns when the listener is
// closed, which is how shutdown reaches it.
func acceptEcho(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return // listener closed during shutdown
		}
		go func(c net.Conn) {
			defer func() { _ = c.Close() }()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			buf := make([]byte, 4096)
			for {
				n, rerr := c.Read(buf)
				if n > 0 {
					if _, werr := c.Write(buf[:n]); werr != nil {
						return
					}
				}
				if rerr != nil {
					return
				}
			}
		}(conn)
	}
}
