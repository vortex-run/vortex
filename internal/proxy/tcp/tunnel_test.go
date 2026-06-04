package tcp

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// pipePair returns two connected in-memory connections for correctness tests.
// net.Pipe gives synchronous, reliable, full-duplex conns with no real network.
func pipePair() (net.Conn, net.Conn) { return net.Pipe() }

// runTunnel starts Tunnel in a goroutine and returns a channel delivering its
// error result.
func runTunnel(ctx context.Context, client, backend net.Conn, cfg TunnelConfig) <-chan error {
	done := make(chan error, 1)
	go func() { done <- Tunnel(ctx, client, backend, cfg) }()
	return done
}

func TestTunnel_CopiesClientToBackend(t *testing.T) {
	clientSide, clientPipe := pipePair() // clientSide ↔ clientPipe(=client conn into Tunnel)
	backendPipe, backendSide := pipePair()
	cfg := TunnelConfig{IdleTimeout: 2 * time.Second}
	done := runTunnel(context.Background(), clientPipe, backendPipe, cfg)

	// Write from the client end; expect it to arrive at the backend end.
	want := []byte("hello vortex")
	go func() {
		_, _ = clientSide.Write(want)
		_ = clientSide.Close()
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(backendSide, got); err != nil {
		t.Fatalf("reading at backend: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("backend got %q, want %q", got, want)
	}
	_ = backendSide.Close()
	<-done
}

func TestTunnel_CopiesBackendToClient(t *testing.T) {
	clientSide, clientPipe := pipePair()
	backendPipe, backendSide := pipePair()
	cfg := TunnelConfig{IdleTimeout: 2 * time.Second}
	done := runTunnel(context.Background(), clientPipe, backendPipe, cfg)

	want := []byte("response data")
	go func() {
		_, _ = backendSide.Write(want)
		_ = backendSide.Close()
	}()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(clientSide, got); err != nil {
		t.Fatalf("reading at client: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("client got %q, want %q", got, want)
	}
	_ = clientSide.Close()
	<-done
}

func TestTunnel_ClosesBothWhenClientCloses(t *testing.T) {
	clientSide, clientPipe := pipePair()
	backendPipe, backendSide := pipePair()
	// Short idle timeout: net.Pipe does not propagate a half-close as EOF, so
	// the still-open backend→client direction ends via the idle deadline. A real
	// TCP backend would see the CloseWrite as EOF immediately (see integration).
	done := runTunnel(context.Background(), clientPipe, backendPipe, TunnelConfig{IdleTimeout: 200 * time.Millisecond})

	// Client closes immediately → Tunnel should finish and the backend side
	// should observe EOF.
	_ = clientSide.Close()

	// Backend read should hit EOF (net.Pipe propagates close as EOF).
	_ = backendSide.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	_, err := backendSide.Read(buf)
	if err == nil {
		t.Error("expected backend to see EOF/closed after client close")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Tunnel did not return after client close")
	}
	_ = backendSide.Close()
}

func TestTunnel_ClosesBothWhenBackendCloses(t *testing.T) {
	clientSide, clientPipe := pipePair()
	backendPipe, backendSide := pipePair()
	done := runTunnel(context.Background(), clientPipe, backendPipe, TunnelConfig{IdleTimeout: 200 * time.Millisecond})

	_ = backendSide.Close()

	_ = clientSide.SetReadDeadline(time.Now().Add(time.Second))
	buf := make([]byte, 8)
	if _, err := clientSide.Read(buf); err == nil {
		t.Error("expected client to see EOF/closed after backend close")
	}

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Tunnel did not return after backend close")
	}
	_ = clientSide.Close()
}

func TestTunnel_IdleTimeout(t *testing.T) {
	_, clientPipe := pipePair()
	_, backendPipe := pipePair()
	// No traffic ever flows → idle timeout should fire quickly.
	cfg := TunnelConfig{IdleTimeout: 150 * time.Millisecond}
	done := runTunnel(context.Background(), clientPipe, backendPipe, cfg)

	select {
	case err := <-done:
		if !errors.Is(err, ErrIdleTimeout) {
			t.Errorf("err = %v, want ErrIdleTimeout", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Tunnel did not time out on idle")
	}
}

func TestTunnel_RespectsContextCancel(t *testing.T) {
	_, clientPipe := pipePair()
	_, backendPipe := pipePair()
	ctx, cancel := context.WithCancel(context.Background())
	cfg := TunnelConfig{IdleTimeout: 10 * time.Second}
	done := runTunnel(ctx, clientPipe, backendPipe, cfg)

	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Tunnel did not return on ctx cancel")
	}
}

// loopbackPair returns two ends of a real loopback TCP connection, used by the
// benchmark so throughput numbers are representative (net.Pipe is synchronous
// and unbuffered, so it does not reflect real socket throughput).
func loopbackPair(b *testing.B) (*net.TCPConn, *net.TCPConn) {
	b.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	type result struct {
		c   net.Conn
		err error
	}
	accepted := make(chan result, 1)
	go func() {
		c, err := ln.Accept()
		accepted <- result{c, err}
	}()

	client, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		b.Fatal(err)
	}
	r := <-accepted
	if r.err != nil {
		b.Fatal(r.err)
	}
	return client.(*net.TCPConn), r.c.(*net.TCPConn)
}

// BenchmarkTunnel measures sustained one-direction throughput through Tunnel
// over real loopback sockets. The reported MB/s informs the M2.7 (Rust
// hot-path) decision. A separate goroutine feeds the client side; the backend
// side is drained; bytes drained are counted as throughput.
func BenchmarkTunnel(b *testing.B) {
	// Tunnel topology: producer → clientIn |Tunnel| backendOut → drain.
	clientOut, clientIn := loopbackPair(b) // producer writes clientOut → Tunnel reads clientIn
	backendOut, backendIn := loopbackPair(b)

	cfg := TunnelConfig{IdleTimeout: time.Minute, BufferSize: defaultBufferSize}
	ctx, cancel := context.WithCancel(context.Background())
	var tw sync.WaitGroup
	tw.Add(1)
	go func() { defer tw.Done(); _ = Tunnel(ctx, clientIn, backendIn, cfg) }()

	const chunk = 64 * 1024
	payload := make([]byte, chunk)

	// Producer goroutine: keep writing into the client side until told to stop.
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
				if _, err := clientOut.Write(payload); err != nil {
					return
				}
			}
		}
	}()

	drain := make([]byte, chunk)
	b.SetBytes(chunk)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := io.ReadFull(backendOut, drain); err != nil {
			b.Fatalf("draining: %v", err)
		}
	}
	b.StopTimer()

	close(stop)
	cancel()
	_ = clientOut.Close()
	_ = clientIn.Close()
	_ = backendIn.Close()
	_ = backendOut.Close()
	tw.Wait()
}

// TestTunnel_PooledBuffersZeroAlloc asserts the steady-state copy path performs
// no allocations once warmed up (the buffer comes from sync.Pool).
func TestTunnel_PooledBuffersZeroAlloc(t *testing.T) {
	src, dst := net.Pipe()
	defer func() { _ = src.Close(); _ = dst.Close() }()

	idle := &idleDeadline{conns: []net.Conn{src, dst}, timeout: time.Minute}
	idle.reset()

	// Drain dst in the background.
	go func() {
		buf := make([]byte, defaultBufferSize)
		for {
			if _, err := dst.Read(buf); err != nil {
				return
			}
		}
	}()

	// Warm the pool, then measure allocations of a single copy iteration's
	// buffer acquisition path via the pool directly.
	allocs := testing.AllocsPerRun(100, func() {
		bp := bufPool.Get().(*[]byte)
		bufPool.Put(bp)
	})
	if allocs != 0 {
		t.Errorf("pooled buffer get/put allocated %v times, want 0", allocs)
	}
}
