// Package cluster implements VORTEX's clustering and high-availability layer
// (build plan M4): node identity, SWIM gossip membership (hashicorp/memberlist),
// Raft consensus for config replication (hashicorp/raft), and a manager that
// orchestrates the two. A single-node deployment runs with no cluster overhead;
// multi-node deployments form a gossip mesh and replicate config through Raft.
package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
)

// NodeConfig is a node's identity and network coordinates for cluster
// membership. NodeID is derived the same way as the M3.1 vtls identity so a
// node's cluster identity and its mTLS identity match.
type NodeConfig struct {
	NodeID        string // 16-char hex, SHA-256 derived (matches vtls)
	ClusterName   string
	BindAddr      string // gossip bind address
	BindPort      int    // gossip port (default 7946)
	AdvertiseAddr string // externally reachable address; defaults to BindAddr
}

// defaultGossipPort is the SWIM gossip port used when none is supplied.
const defaultGossipPort = 7946

// NewNodeConfig builds a NodeConfig, deriving NodeID with the same SHA-256
// method as M3.1 (SHA-256(clusterName + "/" + hostname), first 16 hex chars).
// AdvertiseAddr defaults to BindAddr. BindAddr is required.
func NewNodeConfig(clusterName, bindAddr string, bindPort int) (*NodeConfig, error) {
	if bindAddr == "" {
		return nil, errors.New("cluster: BindAddr is required")
	}
	if bindPort <= 0 {
		bindPort = defaultGossipPort
	}

	hostname, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("cluster: resolving hostname: %w", err)
	}
	sum := sha256.Sum256([]byte(clusterName + "/" + hostname))
	nodeID := hex.EncodeToString(sum[:])[:16]

	return &NodeConfig{
		NodeID:        nodeID,
		ClusterName:   clusterName,
		BindAddr:      bindAddr,
		BindPort:      bindPort,
		AdvertiseAddr: bindAddr,
	}, nil
}
