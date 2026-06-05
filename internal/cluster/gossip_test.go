package cluster

import (
	"io"
	"net"
	"strconv"
	"testing"
	"time"
)

// freeUDPPort returns a free port (memberlist uses both TCP and UDP on it).
func freeUDPPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// newGossipNode builds a single gossip node bound to loopback on a free port.
func newGossipNode(t *testing.T, cluster string) *Gossip {
	t.Helper()
	port := freeUDPPort(t)
	nc, err := NewNodeConfig(cluster, "127.0.0.1", port)
	if err != nil {
		t.Fatal(err)
	}
	// Distinct NodeIDs per node despite the shared hostname: override with the
	// port so the two test nodes do not collide on Name.
	nc.NodeID = nc.NodeID + "-" + strconv.Itoa(port)
	g, err := NewGossip(GossipConfig{NodeConfig: nc, LogOutput: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = g.Shutdown() })
	return g
}

func TestGossip_SingleNodeSeesSelf(t *testing.T) {
	g := newGossipNode(t, "solo")
	members := g.Members()
	if len(members) != 1 {
		t.Fatalf("single node sees %d members, want 1", len(members))
	}
	if members[0].NodeID != g.LocalNodeID() {
		t.Errorf("self member = %q, want %q", members[0].NodeID, g.LocalNodeID())
	}
	if members[0].Status != StatusAlive {
		t.Errorf("self status = %q, want alive", members[0].Status)
	}
}

func TestGossip_TwoNodesJoin(t *testing.T) {
	a := newGossipNode(t, "pair")
	b := newGossipNode(t, "pair")

	// b joins a.
	aAddr := a.Members()[0].Addr
	if err := b.Join([]string{aAddr}); err != nil {
		t.Fatalf("join: %v", err)
	}

	// Both should converge to seeing 2 members.
	if !eventually(t, 5*time.Second, func() bool {
		return len(a.Members()) == 2 && len(b.Members()) == 2
	}) {
		t.Errorf("nodes did not converge: a=%d b=%d", len(a.Members()), len(b.Members()))
	}
}

func TestGossip_LeaveRemovesNode(t *testing.T) {
	a := newGossipNode(t, "leave")
	b := newGossipNode(t, "leave")
	if err := b.Join([]string{a.Members()[0].Addr}); err != nil {
		t.Fatalf("join: %v", err)
	}
	if !eventually(t, 5*time.Second, func() bool { return len(a.Members()) == 2 }) {
		t.Fatal("did not converge to 2 members")
	}

	// b leaves; a should drop back to a 1-member alive view.
	if err := b.Leave(2 * time.Second); err != nil {
		t.Fatalf("leave: %v", err)
	}
	_ = b.Shutdown()

	if !eventually(t, 5*time.Second, func() bool {
		alive := 0
		for _, m := range a.Members() {
			if m.Status == StatusAlive {
				alive++
			}
		}
		return alive == 1
	}) {
		t.Errorf("a still sees more than one alive member after b left: %+v", a.Members())
	}
}

func TestGossip_EncryptedGossipSameKey(t *testing.T) {
	key := []byte("0123456789abcdef") // 16 bytes
	mk := func() *Gossip {
		port := freeUDPPort(t)
		nc, _ := NewNodeConfig("enc", "127.0.0.1", port)
		nc.NodeID = nc.NodeID + "-" + strconv.Itoa(port)
		g, err := NewGossip(GossipConfig{NodeConfig: nc, SecretKey: key, LogOutput: io.Discard})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = g.Shutdown() })
		return g
	}
	a := mk()
	b := mk()
	if err := b.Join([]string{a.Members()[0].Addr}); err != nil {
		t.Fatalf("encrypted join: %v", err)
	}
	if !eventually(t, 5*time.Second, func() bool {
		return len(a.Members()) == 2 && len(b.Members()) == 2
	}) {
		t.Error("encrypted nodes with the same key should communicate")
	}
}

func TestGossip_InvalidSecretKeyRejected(t *testing.T) {
	nc, _ := NewNodeConfig("x", "127.0.0.1", freeUDPPort(t))
	if _, err := NewGossip(GossipConfig{NodeConfig: nc, SecretKey: []byte("tooshort")}); err == nil {
		t.Error("a non-16/24/32-byte SecretKey should be rejected")
	}
}

func TestGossip_ShutdownCleansUp(t *testing.T) {
	g := newGossipNode(t, "shut")
	if err := g.Shutdown(); err != nil {
		t.Errorf("Shutdown = %v, want nil", err)
	}
}

// eventually polls cond until it is true or the timeout elapses.
func eventually(t *testing.T, timeout time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}
