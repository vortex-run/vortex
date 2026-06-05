package cluster

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/hashicorp/memberlist"
)

// GossipConfig configures the SWIM gossip layer.
type GossipConfig struct {
	NodeConfig *NodeConfig
	SecretKey  []byte    // 16/24/32 bytes for encrypted gossip; nil = unencrypted
	LogOutput  io.Writer // where memberlist logs go; nil = default
}

// MemberStatus is a node's gossip-observed liveness.
type MemberStatus string

// Member statuses.
const (
	StatusAlive   MemberStatus = "alive"
	StatusSuspect MemberStatus = "suspect"
	StatusDead    MemberStatus = "dead"
	StatusLeft    MemberStatus = "left"
)

// Member is a cluster peer as seen through gossip.
type Member struct {
	NodeID string
	Addr   string
	Status MemberStatus
}

// Gossip wraps a memberlist for SWIM-based node discovery and failure
// detection.
type Gossip struct {
	cfg  GossipConfig
	list *memberlist.Memberlist
}

// NewGossip creates a Gossip from cfg, building the memberlist (which starts
// listening immediately) but not joining any peers.
func NewGossip(cfg GossipConfig) (*Gossip, error) {
	if cfg.NodeConfig == nil {
		return nil, errors.New("cluster: GossipConfig.NodeConfig is required")
	}
	if n := len(cfg.SecretKey); n != 0 && n != 16 && n != 24 && n != 32 {
		return nil, fmt.Errorf("cluster: gossip SecretKey must be 16, 24, or 32 bytes, got %d", n)
	}

	mlCfg := memberlist.DefaultLANConfig()
	mlCfg.Name = cfg.NodeConfig.NodeID
	mlCfg.BindAddr = cfg.NodeConfig.BindAddr
	mlCfg.BindPort = cfg.NodeConfig.BindPort
	mlCfg.AdvertiseAddr = cfg.NodeConfig.AdvertiseAddr
	mlCfg.AdvertisePort = cfg.NodeConfig.BindPort
	if len(cfg.SecretKey) > 0 {
		mlCfg.SecretKey = cfg.SecretKey
	}
	if cfg.LogOutput != nil {
		mlCfg.LogOutput = cfg.LogOutput
	}

	list, err := memberlist.Create(mlCfg)
	if err != nil {
		return nil, fmt.Errorf("cluster: creating memberlist: %w", err)
	}
	return &Gossip{cfg: cfg, list: list}, nil
}

// Join contacts the given peers to merge cluster state. With no peers it is a
// no-op (single-node mode). It returns an error only if a join was attempted and
// reached zero peers.
func (g *Gossip) Join(peers []string) error {
	if len(peers) == 0 {
		return nil
	}
	n, err := g.list.Join(peers)
	if err != nil {
		return fmt.Errorf("cluster: joining peers: %w", err)
	}
	if n == 0 {
		return errors.New("cluster: joined zero peers")
	}
	return nil
}

// Members returns the current view of cluster members.
func (g *Gossip) Members() []Member {
	nodes := g.list.Members()
	out := make([]Member, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, Member{
			NodeID: n.Name,
			Addr:   n.Address(),
			Status: statusOf(n.State),
		})
	}
	return out
}

// LocalNodeID returns this node's gossip name (its NodeID).
func (g *Gossip) LocalNodeID() string {
	return g.list.LocalNode().Name
}

// Leave broadcasts an intent to leave and waits up to timeout for it to
// propagate, so peers mark this node as left rather than dead.
func (g *Gossip) Leave(timeout time.Duration) error {
	if err := g.list.Leave(timeout); err != nil {
		return fmt.Errorf("cluster: leaving gossip: %w", err)
	}
	return nil
}

// Shutdown stops the gossip listener and background tasks.
func (g *Gossip) Shutdown() error {
	if err := g.list.Shutdown(); err != nil {
		return fmt.Errorf("cluster: shutting down gossip: %w", err)
	}
	return nil
}

// statusOf maps a memberlist node state to a MemberStatus.
func statusOf(s memberlist.NodeStateType) MemberStatus {
	switch s {
	case memberlist.StateAlive:
		return StatusAlive
	case memberlist.StateSuspect:
		return StatusSuspect
	case memberlist.StateDead:
		return StatusDead
	case memberlist.StateLeft:
		return StatusLeft
	default:
		return StatusDead
	}
}
