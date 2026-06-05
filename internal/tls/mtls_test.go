package vtls

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"math/big"
	"net"
	"path/filepath"
	"testing"
	"time"
)

// testMTLS builds a RotationManager + MTLSConfig for the given cluster name.
func testMTLS(t *testing.T, clusterName string, lifetime, rotateAt time.Duration) (*RotationManager, *MTLSConfig) {
	t.Helper()
	store, err := NewStore(filepath.Join(t.TempDir(), "certs"), []byte("mtls-test-key-"+clusterName))
	if err != nil {
		t.Fatal(err)
	}
	rm, err := NewRotationManager(RotationConfig{
		ClusterName:  clusterName,
		Store:        store,
		CertLifetime: lifetime,
		RotateAt:     rotateAt,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	mc, err := NewMTLSConfig(MTLSConfig{
		RotationMgr: rm,
		TrustDomain: rm.Identity().TrustDomain,
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return rm, mc
}

// hsResult holds the outcome of a TLS handshake between two configs.
type hsResult struct {
	serverErr  error
	clientErr  error
	serverPeer *big.Int // serial of the cert the server saw from the client
	clientPeer *big.Int // serial of the cert the client saw from the server
}

// handshake runs a TLS handshake between serverCfg and clientCfg over a real
// loopback TCP connection (net.Pipe is unbuffered and deadlocks on TLS 1.3
// post-handshake tickets) and reports the result for each side.
func handshake(serverCfg, clientCfg *tls.Config) hsResult {
	var r hsResult
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		r.serverErr, r.clientErr = err, err
		return r
	}
	defer func() { _ = ln.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, aerr := ln.Accept()
		if aerr != nil {
			r.serverErr = aerr
			return
		}
		server := tls.Server(raw, serverCfg)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		r.serverErr = server.HandshakeContext(ctx)
		if r.serverErr == nil {
			if cs := server.ConnectionState(); len(cs.PeerCertificates) > 0 {
				r.serverPeer = cs.PeerCertificates[0].SerialNumber
			}
		}
		_ = server.Close()
	}()

	raw, derr := net.Dial("tcp", ln.Addr().String())
	if derr != nil {
		r.clientErr = derr
		<-done
		return r
	}
	client := tls.Client(raw, clientCfg)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r.clientErr = client.HandshakeContext(ctx)
	if r.clientErr == nil {
		if cs := client.ConnectionState(); len(cs.PeerCertificates) > 0 {
			r.clientPeer = cs.PeerCertificates[0].SerialNumber
		}
	}
	_ = client.Close()
	<-done
	return r
}

func TestMTLS_ServerRequiresClientCert(t *testing.T) {
	_, mc := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	// We require a client cert; authorization is by SPIFFE identity in
	// VerifyPeerCertificate (RequireAnyClientCert + manual chain verify), not by
	// the default hostname verifier.
	if mc.ServerTLSConfig().ClientAuth != tls.RequireAnyClientCert {
		t.Error("ServerTLSConfig.ClientAuth must require a client cert")
	}
	if mc.ServerTLSConfig().VerifyPeerCertificate == nil {
		t.Error("ServerTLSConfig must set VerifyPeerCertificate for SPIFFE auth")
	}
}

func TestMTLS_ClientVerifiesPeer(t *testing.T) {
	_, mc := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	// Client identifies peers by SPIFFE identity, so it sets
	// VerifyPeerCertificate and presents its own cert via GetClientCertificate.
	cc := mc.ClientTLSConfig()
	if cc.VerifyPeerCertificate == nil {
		t.Error("ClientTLSConfig must set VerifyPeerCertificate")
	}
	if cc.GetClientCertificate == nil {
		t.Error("ClientTLSConfig must set GetClientCertificate")
	}
}

func TestMTLS_BuildCAPoolValid(t *testing.T) {
	rm, _ := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	pool, err := BuildCAPool(rm.ClusterCA())
	if err != nil || pool == nil {
		t.Errorf("BuildCAPool = %v, %v; want non-nil pool", pool, err)
	}
}

func TestMTLS_BuildCAPoolNilError(t *testing.T) {
	if _, err := BuildCAPool(nil); err == nil {
		t.Error("BuildCAPool(nil) should error")
	}
}

func TestMTLS_HandshakeSucceeds(t *testing.T) {
	_, mc := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	r := handshake(mc.ServerTLSConfig(), mc.ClientTLSConfig())
	if r.serverErr != nil || r.clientErr != nil {
		t.Fatalf("handshake failed: server=%v client=%v", r.serverErr, r.clientErr)
	}
	if r.serverPeer == nil {
		t.Error("server did not observe a client peer certificate")
	}
	if r.clientPeer == nil {
		t.Error("client did not observe a server peer certificate")
	}
}

func TestMTLS_HandshakeFailsNoClientCert(t *testing.T) {
	_, mc := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	// Client trusts the CA but presents NO certificate.
	pool, _ := BuildCAPool(mc.RotationMgr.ClusterCA())
	plainClient := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: pool} //nolint:gosec // intentional: no client cert
	r := handshake(mc.ServerTLSConfig(), plainClient)
	if r.serverErr == nil && r.clientErr == nil {
		t.Error("handshake should fail when client presents no certificate")
	}
}

func TestMTLS_HandshakeFailsWrongTrustDomain(t *testing.T) {
	// Server expects prod.vortex; client presents a staging.vortex identity.
	_, serverMC := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	_, stagingMC := testMTLS(t, "staging", time.Hour, 30*time.Minute)

	r := handshake(serverMC.ServerTLSConfig(), stagingMC.ClientTLSConfig())
	if r.serverErr == nil && r.clientErr == nil {
		t.Error("handshake should fail: client cert from wrong cluster/trust domain")
	}
}

func TestMTLS_AtomicCertSwapAcrossHandshakes(t *testing.T) {
	// Short lifetime so the rotation loop swaps the cert mid-test.
	rm, mc := testMTLS(t, "prod", 2*time.Second, 1*time.Second)

	firstServerCert := handshake(mc.ServerTLSConfig(), mc.ClientTLSConfig()).clientPeer
	if firstServerCert == nil {
		t.Fatal("first handshake produced no server cert")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rm.StartRotation(ctx)

	// Wait for a rotation (serial of Current changes).
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if rm.Current().Leaf.SerialNumber.Cmp(firstServerCert) != 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	secondServerCert := handshake(mc.ServerTLSConfig(), mc.ClientTLSConfig()).clientPeer
	if secondServerCert == nil {
		t.Fatal("second handshake produced no server cert")
	}
	if firstServerCert.Cmp(secondServerCert) == 0 {
		t.Error("server cert should differ after rotation (atomic swap not visible)")
	}
}

func TestMTLS_NilRotationMgrError(t *testing.T) {
	if _, err := NewMTLSConfig(MTLSConfig{TrustDomain: "prod.vortex"}); err == nil {
		t.Error("expected error for nil RotationManager")
	}
}

func TestMTLS_EmptyTrustDomainError(t *testing.T) {
	rm, _ := testMTLS(t, "prod", time.Hour, 30*time.Minute)
	if _, err := NewMTLSConfig(MTLSConfig{RotationMgr: rm}); err == nil {
		t.Error("expected error for empty TrustDomain")
	}
}
