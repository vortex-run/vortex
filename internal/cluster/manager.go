package cluster

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

// Config configures a cluster Manager.
type Config struct {
	Node        *NodeConfig
	RaftDataDir string   // "" = in-memory raft stores
	RaftPort    int      // default 7947
	GossipPort  int      // default 7946 (falls back to Node.BindPort)
	SecretKey   []byte   // gossip encryption key (16/24/32 bytes)
	Bootstrap   bool     // bootstrap a new cluster (first node)
	Peers       []string // gossip peer addresses to join
	Logger      *slog.Logger
}

// Manager orchestrates the gossip membership layer and the Raft consensus layer,
// translating gossip member events into Raft voter membership changes.
type Manager struct {
	cfg    Config
	log    *slog.Logger
	gossip *Gossip
	raft   *RaftNode

	mu      sync.Mutex
	started bool
	closed  bool
	stopCh  chan struct{}
}

// NewManager builds a Manager, creating the Gossip and RaftNode but not yet
// joining peers or starting the reconcile loop (that happens in Start).
func NewManager(cfg Config) (*Manager, error) {
	if cfg.Node == nil {
		return nil, errors.New("cluster: Config.Node is required")
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.RaftPort <= 0 {
		cfg.RaftPort = defaultRaftPort
	}

	gossip, err := NewGossip(GossipConfig{
		NodeConfig: cfg.Node,
		SecretKey:  cfg.SecretKey,
		LogOutput:  io.Discard,
	})
	if err != nil {
		return nil, err
	}

	raftNode, err := NewRaftNode(RaftConfig{
		NodeID:    cfg.Node.NodeID,
		DataDir:   cfg.RaftDataDir,
		BindAddr:  fmt.Sprintf("%s:%d", cfg.Node.BindAddr, cfg.RaftPort),
		Bootstrap: cfg.Bootstrap,
	})
	if err != nil {
		_ = gossip.Shutdown()
		return nil, err
	}

	return &Manager{
		cfg:    cfg,
		log:    cfg.Logger,
		gossip: gossip,
		raft:   raftNode,
		stopCh: make(chan struct{}),
	}, nil
}

// Start joins the configured peers via gossip and launches the reconcile loop
// that keeps Raft voter membership in sync with gossip membership. When
// Bootstrap is set it waits (briefly) to become leader before returning.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	m.mu.Unlock()

	// Joining peers is best-effort and asynchronous: a node must start promptly
	// even if peers are unreachable (a join to a down peer can block on network
	// timeouts). Gossip anti-entropy reconciles membership once peers come up.
	if len(m.cfg.Peers) > 0 {
		go func() {
			if err := m.gossip.Join(m.cfg.Peers); err != nil {
				m.log.Warn("initial gossip join failed, will retry via gossip", "err", err)
			}
		}()
	}

	if m.cfg.Bootstrap {
		// Give leadership a moment to settle so callers can Apply immediately.
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) && !m.raft.IsLeader() {
			time.Sleep(50 * time.Millisecond)
		}
	}

	go m.reconcileLoop(ctx)

	m.log.Info("cluster started",
		"node_id", m.cfg.Node.NodeID,
		"members", len(m.gossip.Members()),
		"leader", m.raft.IsLeader(),
	)
	return nil
}

// reconcileLoop periodically reconciles Raft voters with the live gossip member
// set: alive gossip members become Raft voters; this runs on the leader only.
func (m *Manager) reconcileLoop(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.reconcile()
		}
	}
}

// reconcile adds any alive gossip member that is not yet a Raft voter. Removal of
// confirmed-dead members is handled here too. It is a no-op unless this node is
// the leader (only the leader may change configuration).
func (m *Manager) reconcile() {
	if !m.raft.IsLeader() {
		return
	}
	for _, mem := range m.gossip.Members() {
		if mem.NodeID == m.cfg.Node.NodeID {
			continue
		}
		switch mem.Status {
		case StatusAlive:
			// Best-effort: AddVoter is idempotent for an existing voter.
			if err := m.raft.AddVoter(mem.NodeID, raftAddrFor(mem.Addr, m.cfg.RaftPort), 5*time.Second); err != nil &&
				!errors.Is(err, ErrNotLeader) {
				m.log.Debug("reconcile add voter", "node", mem.NodeID, "err", err)
			}
		case StatusDead, StatusLeft:
			if err := m.raft.RemoveServer(mem.NodeID, 5*time.Second); err != nil &&
				!errors.Is(err, ErrNotLeader) {
				m.log.Debug("reconcile remove server", "node", mem.NodeID, "err", err)
			}
		}
	}
}

// ApplyConfig replicates a config change through Raft. On a follower it returns
// ErrNotLeader (HTTP forwarding to the leader is a later-milestone stub).
func (m *Manager) ApplyConfig(data []byte) error {
	return m.raft.Apply(data, raftApplyTimeout)
}

// Members returns the current gossip view of the cluster.
func (m *Manager) Members() []Member { return m.gossip.Members() }

// IsLeader reports whether this node is the Raft leader.
func (m *Manager) IsLeader() bool { return m.raft.IsLeader() }

// LeaderAddr returns the current Raft leader address.
func (m *Manager) LeaderAddr() string { return m.raft.LeaderAddr() }

// Shutdown stops the reconcile loop and tears down raft and gossip. It is
// idempotent: repeated calls (e.g. an explicit Shutdown plus a test cleanup
// hook) are no-ops after the first.
func (m *Manager) Shutdown() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	if m.started {
		close(m.stopCh)
		m.started = false
	}
	m.mu.Unlock()

	var errs []error
	if err := m.raft.Shutdown(); err != nil {
		errs = append(errs, err)
	}
	if err := m.gossip.Shutdown(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// raftAddrFor derives a peer's Raft address from its gossip address by replacing
// the port with the cluster's Raft port.
func raftAddrFor(gossipAddr string, raftPort int) string {
	host := gossipAddr
	if h, _, err := net.SplitHostPort(gossipAddr); err == nil {
		host = h
	}
	return fmt.Sprintf("%s:%d", host, raftPort)
}
