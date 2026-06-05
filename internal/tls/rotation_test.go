package vtls

import (
	"context"
	"crypto/x509"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"
)

func rotStore(t *testing.T) *Store {
	t.Helper()
	s, err := NewStore(filepath.Join(t.TempDir(), "certs"), []byte("rotation-test-key"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s
}

func newRotMgr(t *testing.T, store *Store, lifetime, rotateAt time.Duration) *RotationManager {
	t.Helper()
	r, err := NewRotationManager(RotationConfig{
		ClusterName:  "testcluster",
		Store:        store,
		CertLifetime: lifetime,
		RotateAt:     rotateAt,
		Logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("NewRotationManager: %v", err)
	}
	return r
}

func TestRotation_GeneratesCAFirstCall(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	if r.ClusterCA() == nil || r.ClusterCA().Leaf == nil {
		t.Fatal("cluster CA not generated")
	}
	if !r.ClusterCA().Leaf.IsCA {
		t.Error("cluster CA cert should have IsCA=true")
	}
}

func TestRotation_LoadsExistingCA(t *testing.T) {
	store := rotStore(t)
	r1 := newRotMgr(t, store, 24*time.Hour, 4*time.Hour)
	serial1 := r1.ClusterCA().Leaf.SerialNumber

	// Second manager over the same store must reuse the CA.
	r2 := newRotMgr(t, store, 24*time.Hour, 4*time.Hour)
	serial2 := r2.ClusterCA().Leaf.SerialNumber

	if serial1.Cmp(serial2) != 0 {
		t.Errorf("CA serial changed across managers: %s vs %s", serial1, serial2)
	}
}

func TestRotation_IssuesNodeCert(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	if r.Current() == nil {
		t.Fatal("node cert not issued")
	}
}

func TestRotation_CurrentNonNil(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	if r.Current() == nil || r.Current().Leaf == nil {
		t.Fatal("Current() returned nil cert or leaf")
	}
}

func TestRotation_CurrentVerifiesAgainstCA(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	pool := x509.NewCertPool()
	pool.AddCert(r.ClusterCA().Leaf)
	if _, err := r.Current().Leaf.Verify(x509.VerifyOptions{
		Roots:     pool,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Errorf("node cert does not verify against cluster CA: %v", err)
	}
}

func TestRotation_CurrentHasSPIFFEURI(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	uri, err := ExtractSPIFFEID(r.Current().Leaf)
	if err != nil {
		t.Fatal(err)
	}
	if uri != r.Identity().SPIFFEURI {
		t.Errorf("cert SPIFFE URI = %q, want %q", uri, r.Identity().SPIFFEURI)
	}
}

func TestRotation_RotatesUnderShortLifetime(t *testing.T) {
	// 2s lifetime, rotate when < 1s remains. The 0.5s check interval should
	// swap in a new cert within a couple of seconds.
	r := newRotMgr(t, rotStore(t), 2*time.Second, 1*time.Second)
	before := r.Current().Leaf.SerialNumber

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r.StartRotation(ctx)

	deadline := time.Now().Add(4 * time.Second)
	rotated := false
	for time.Now().Before(deadline) {
		if r.Current().Leaf.SerialNumber.Cmp(before) != 0 {
			rotated = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !rotated {
		t.Error("cert was not rotated before its short lifetime expired")
	}
}

func TestRotation_StartStopsOnCancel(t *testing.T) {
	r := newRotMgr(t, rotStore(t), time.Hour, 30*time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	r.StartRotation(ctx) // must not panic
	cancel()
	// Give the goroutine a moment to observe cancellation; nothing to assert
	// beyond "no panic / clean exit", which the race detector also guards.
	time.Sleep(50 * time.Millisecond)
}

func TestRotation_ClusterCAIsCA(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	if !r.ClusterCA().Leaf.IsCA {
		t.Error("ClusterCA().Leaf.IsCA should be true")
	}
}

func TestRotation_IdentityTrustDomain(t *testing.T) {
	r := newRotMgr(t, rotStore(t), 24*time.Hour, 4*time.Hour)
	if td := r.Identity().TrustDomain; td != "testcluster.vortex" {
		t.Errorf("trust domain = %q, want testcluster.vortex", td)
	}
}

func TestRotation_NilStoreError(t *testing.T) {
	if _, err := NewRotationManager(RotationConfig{ClusterName: "x"}); err == nil {
		t.Error("expected error for nil Store")
	}
}

func TestRotation_EmptyClusterError(t *testing.T) {
	if _, err := NewRotationManager(RotationConfig{Store: rotStore(t)}); err == nil {
		t.Error("expected error for empty ClusterName")
	}
}
