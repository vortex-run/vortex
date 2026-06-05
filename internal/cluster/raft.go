package cluster

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"github.com/hashicorp/raft"
)

// ErrNotLeader is returned by Apply when this node is not the Raft leader.
var ErrNotLeader = errors.New("cluster: not the leader")

// defaultRaftPort is the Raft TCP port used when none is supplied.
const defaultRaftPort = 7947

// raftApplyTimeout bounds a single Apply round trip when none is given.
const raftApplyTimeout = 10 * time.Second

// RaftConfig configures a RaftNode.
type RaftConfig struct {
	NodeID    string // Raft server ID (use the cluster NodeID)
	DataDir   string // log/snapshot storage; "" uses in-memory stores (tests)
	BindAddr  string // Raft TCP bind address (host:port); port defaults to 7947
	Bootstrap bool   // true for the first node, which bootstraps the cluster
}

// RaftNode wraps a hashicorp/raft instance plus its transport and FSM, providing
// the cluster-config replication primitives VORTEX needs.
type RaftNode struct {
	cfg   RaftConfig
	raft  *raft.Raft
	trans *raft.NetworkTransport
	fsm   *configFSM
	addr  string
}

// NewRaftNode builds and starts a Raft node. With an empty DataDir it uses
// in-memory log/stable/snapshot stores (suitable for tests); otherwise it would
// use on-disk stores (wired in a later milestone). When Bootstrap is set it
// bootstraps a single-node cluster so this node becomes leader.
func NewRaftNode(cfg RaftConfig) (*RaftNode, error) {
	if cfg.NodeID == "" {
		return nil, errors.New("cluster: RaftConfig.NodeID is required")
	}
	bindAddr := cfg.BindAddr
	if bindAddr == "" {
		bindAddr = fmt.Sprintf("127.0.0.1:%d", defaultRaftPort)
	}

	rc := raft.DefaultConfig()
	rc.LocalID = raft.ServerID(cfg.NodeID)
	rc.LogOutput = io.Discard

	advertise, err := net.ResolveTCPAddr("tcp", bindAddr)
	if err != nil {
		return nil, fmt.Errorf("cluster: resolving raft bind addr: %w", err)
	}
	trans, err := raft.NewTCPTransport(bindAddr, advertise, 3, 10*time.Second, io.Discard)
	if err != nil {
		return nil, fmt.Errorf("cluster: creating raft transport: %w", err)
	}

	// In-memory stores keep tests hermetic and fast. On-disk BoltDB stores are
	// wired when DataDir is set in a later milestone.
	logStore := raft.NewInmemStore()
	stableStore := raft.NewInmemStore()
	snapStore := raft.NewInmemSnapshotStore()
	if cfg.DataDir != "" {
		if mkErr := os.MkdirAll(cfg.DataDir, 0o700); mkErr != nil {
			_ = trans.Close()
			return nil, fmt.Errorf("cluster: creating raft data dir: %w", mkErr)
		}
	}

	fsm := &configFSM{}
	r, err := raft.NewRaft(rc, fsm, logStore, stableStore, snapStore, trans)
	if err != nil {
		_ = trans.Close()
		return nil, fmt.Errorf("cluster: creating raft: %w", err)
	}

	if cfg.Bootstrap {
		fut := r.BootstrapCluster(raft.Configuration{
			Servers: []raft.Server{{
				ID:      rc.LocalID,
				Address: trans.LocalAddr(),
			}},
		})
		if err := fut.Error(); err != nil && !errors.Is(err, raft.ErrCantBootstrap) {
			_ = trans.Close()
			return nil, fmt.Errorf("cluster: bootstrapping cluster: %w", err)
		}
	}

	return &RaftNode{cfg: cfg, raft: r, trans: trans, fsm: fsm, addr: string(trans.LocalAddr())}, nil
}

// Apply submits a command to the Raft log. It returns ErrNotLeader when this
// node is not the leader. timeout=0 uses raftApplyTimeout.
func (n *RaftNode) Apply(cmd []byte, timeout time.Duration) error {
	if n.raft.State() != raft.Leader {
		return ErrNotLeader
	}
	if timeout <= 0 {
		timeout = raftApplyTimeout
	}
	fut := n.raft.Apply(cmd, timeout)
	if err := fut.Error(); err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			return ErrNotLeader
		}
		return fmt.Errorf("cluster: applying command: %w", err)
	}
	return nil
}

// IsLeader reports whether this node is the current Raft leader.
func (n *RaftNode) IsLeader() bool {
	return n.raft.State() == raft.Leader
}

// LeaderAddr returns the current leader's address, or "" if unknown.
func (n *RaftNode) LeaderAddr() string {
	return string(n.raft.Leader())
}

// AddVoter adds a voting member to the cluster (leader only).
func (n *RaftNode) AddVoter(id, addr string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = raftApplyTimeout
	}
	fut := n.raft.AddVoter(raft.ServerID(id), raft.ServerAddress(addr), 0, timeout)
	if err := fut.Error(); err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			return ErrNotLeader
		}
		return fmt.Errorf("cluster: adding voter %s: %w", id, err)
	}
	return nil
}

// RemoveServer removes a member from the cluster (leader only).
func (n *RaftNode) RemoveServer(id string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = raftApplyTimeout
	}
	fut := n.raft.RemoveServer(raft.ServerID(id), 0, timeout)
	if err := fut.Error(); err != nil {
		if errors.Is(err, raft.ErrNotLeader) {
			return ErrNotLeader
		}
		return fmt.Errorf("cluster: removing server %s: %w", id, err)
	}
	return nil
}

// Addr returns this node's Raft transport address.
func (n *RaftNode) Addr() string { return n.addr }

// Shutdown stops the Raft node and closes its transport.
func (n *RaftNode) Shutdown() error {
	if err := n.raft.Shutdown().Error(); err != nil {
		return fmt.Errorf("cluster: shutting down raft: %w", err)
	}
	return n.trans.Close()
}

// configFSM is a minimal Raft FSM that holds the latest replicated config bytes.
// Config replication is last-write-wins: each Apply replaces the stored state.
type configFSM struct {
	mu    sync.Mutex
	state []byte
}

// Apply stores the committed command as the current config state.
func (f *configFSM) Apply(log *raft.Log) interface{} {
	f.mu.Lock()
	f.state = append([]byte(nil), log.Data...)
	f.mu.Unlock()
	return nil
}

// Snapshot returns a point-in-time copy of the config state.
func (f *configFSM) Snapshot() (raft.FSMSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &configSnapshot{state: append([]byte(nil), f.state...)}, nil
}

// Restore replaces the config state from a snapshot.
func (f *configFSM) Restore(rc io.ReadCloser) error {
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.state = data
	f.mu.Unlock()
	return nil
}

// configSnapshot persists the config state to a snapshot sink.
type configSnapshot struct {
	state []byte
}

func (s *configSnapshot) Persist(sink raft.SnapshotSink) error {
	if _, err := sink.Write(s.state); err != nil {
		_ = sink.Cancel()
		return err
	}
	return sink.Close()
}

func (s *configSnapshot) Release() {}
