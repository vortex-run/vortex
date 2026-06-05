package cluster

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newManager builds a single-node bootstrap cluster Manager on free ports.
func newManager(t *testing.T, bootstrap bool) *Manager {
	t.Helper()
	gossipPort := freeUDPPort(t)
	raftPort := freeUDPPort(t)
	nc, err := NewNodeConfig("mgr-test", "127.0.0.1", gossipPort)
	if err != nil {
		t.Fatal(err)
	}
	// Unique NodeID per node despite shared hostname.
	nc.NodeID = nc.NodeID + "-" + strconv.Itoa(gossipPort)

	m, err := NewManager(Config{
		Node:      nc,
		RaftPort:  raftPort,
		Bootstrap: bootstrap,
		Logger:    discardLogger(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Shutdown() })
	return m
}

func TestManager_SingleNodeBecomesLeader(t *testing.T) {
	m := newManager(t, true)
	if err := m.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !m.IsLeader() {
		time.Sleep(50 * time.Millisecond)
	}
	if !m.IsLeader() {
		t.Error("single-node bootstrap cluster should elect itself leader")
	}
}

func TestManager_MembersReturnsSelf(t *testing.T) {
	m := newManager(t, true)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	members := m.Members()
	if len(members) != 1 {
		t.Fatalf("Members = %d, want 1 (self)", len(members))
	}
	if members[0].NodeID != m.cfg.Node.NodeID {
		t.Errorf("self member = %q, want %q", members[0].NodeID, m.cfg.Node.NodeID)
	}
}

func TestManager_ApplyConfigOnLeader(t *testing.T) {
	m := newManager(t, true)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && !m.IsLeader() {
		time.Sleep(50 * time.Millisecond)
	}
	if err := m.ApplyConfig([]byte(`{"cluster":"x"}`)); err != nil {
		t.Errorf("ApplyConfig on leader = %v, want nil", err)
	}
}

func TestManager_ApplyConfigOnFollowerErrors(t *testing.T) {
	// A non-bootstrap manager has no cluster and is not leader.
	m := newManager(t, false)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.ApplyConfig([]byte("data")); !errors.Is(err, ErrNotLeader) {
		t.Errorf("ApplyConfig on follower = %v, want ErrNotLeader", err)
	}
}

func TestManager_ShutdownCleansUp(t *testing.T) {
	m := newManager(t, true)
	if err := m.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := m.Shutdown(); err != nil {
		t.Errorf("Shutdown = %v, want nil", err)
	}
	// A second shutdown via the cleanup hook must not panic; guard by marking.
}
