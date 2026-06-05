package cluster

import (
	"errors"
	"net"
	"strconv"
	"testing"
	"time"
)

// freeTCPAddr returns a free 127.0.0.1:port address for a Raft transport.
func freeTCPAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return "127.0.0.1:" + strconv.Itoa(port)
}

// newRaftNode builds a Raft node with the given ID; bootstrap makes it a
// single-node leader.
func newRaftNode(t *testing.T, id string, bootstrap bool) *RaftNode {
	t.Helper()
	n, err := NewRaftNode(RaftConfig{NodeID: id, BindAddr: freeTCPAddr(t), Bootstrap: bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = n.Shutdown() })
	return n
}

// waitLeader blocks until n reports leadership or the timeout elapses.
func waitLeader(t *testing.T, n *RaftNode, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if n.IsLeader() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return n.IsLeader()
}

func TestRaft_SingleNodeBecomesLeader(t *testing.T) {
	n := newRaftNode(t, "node-a", true)
	if !waitLeader(t, n, 5*time.Second) {
		t.Fatal("single bootstrap node should become leader")
	}
	if n.LeaderAddr() == "" {
		t.Error("LeaderAddr should be set once a leader is elected")
	}
}

func TestRaft_ApplyOnNonLeaderErrors(t *testing.T) {
	// A non-bootstrapped node has no cluster and is not leader.
	n := newRaftNode(t, "node-follower", false)
	if err := n.Apply([]byte("cmd"), time.Second); !errors.Is(err, ErrNotLeader) {
		t.Errorf("Apply on non-leader = %v, want ErrNotLeader", err)
	}
}

func TestRaft_LeaderCanApply(t *testing.T) {
	n := newRaftNode(t, "node-a", true)
	if !waitLeader(t, n, 5*time.Second) {
		t.Fatal("node should be leader")
	}
	if err := n.Apply([]byte(`{"config":"v1"}`), 5*time.Second); err != nil {
		t.Errorf("leader Apply = %v, want nil", err)
	}
}

func TestRaft_TwoNodesLeaderAndFollower(t *testing.T) {
	leader := newRaftNode(t, "leader", true)
	if !waitLeader(t, leader, 5*time.Second) {
		t.Fatal("bootstrap node should become leader")
	}
	follower := newRaftNode(t, "follower", false)

	// Leader adds the follower as a voter.
	if err := leader.AddVoter("follower", follower.Addr(), 5*time.Second); err != nil {
		t.Fatalf("AddVoter: %v", err)
	}

	// The follower must converge to a non-leader state with a known leader.
	ok := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !follower.IsLeader() && follower.LeaderAddr() != "" {
			ok = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !ok {
		t.Error("follower should see a leader and not be leader itself")
	}
	if !leader.IsLeader() {
		t.Error("original leader should remain leader")
	}
}

func TestRaft_AddAndRemoveServer(t *testing.T) {
	leader := newRaftNode(t, "leader", true)
	if !waitLeader(t, leader, 5*time.Second) {
		t.Fatal("leader not elected")
	}
	follower := newRaftNode(t, "follower", false)

	if err := leader.AddVoter("follower", follower.Addr(), 5*time.Second); err != nil {
		t.Fatalf("AddVoter: %v", err)
	}
	if err := leader.RemoveServer("follower", 5*time.Second); err != nil {
		t.Errorf("RemoveServer: %v", err)
	}
}

func TestRaft_AddVoterOnNonLeaderErrors(t *testing.T) {
	n := newRaftNode(t, "lonely", false)
	if err := n.AddVoter("x", "127.0.0.1:1", time.Second); !errors.Is(err, ErrNotLeader) {
		t.Errorf("AddVoter on non-leader = %v, want ErrNotLeader", err)
	}
}
