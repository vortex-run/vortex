package cluster

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"testing"
)

func TestNodeConfig_NodeIDMatchesFormula(t *testing.T) {
	nc, err := NewNodeConfig("prod", "127.0.0.1", 7946)
	if err != nil {
		t.Fatal(err)
	}
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte("prod/" + host))
	want := hex.EncodeToString(sum[:])[:16]
	if nc.NodeID != want {
		t.Errorf("NodeID = %q, want %q (SHA-256 of cluster/hostname)", nc.NodeID, want)
	}
	if len(nc.NodeID) != 16 {
		t.Errorf("NodeID len = %d, want 16", len(nc.NodeID))
	}
}

func TestNodeConfig_NodeIDStable(t *testing.T) {
	a, _ := NewNodeConfig("prod", "127.0.0.1", 7946)
	b, _ := NewNodeConfig("prod", "10.0.0.1", 9999)
	if a.NodeID != b.NodeID {
		t.Errorf("NodeID should be stable for the same cluster+host: %q vs %q", a.NodeID, b.NodeID)
	}
}

func TestNodeConfig_AdvertiseDefaultsToBind(t *testing.T) {
	nc, err := NewNodeConfig("c", "192.168.1.5", 7946)
	if err != nil {
		t.Fatal(err)
	}
	if nc.AdvertiseAddr != "192.168.1.5" {
		t.Errorf("AdvertiseAddr = %q, want it to default to BindAddr", nc.AdvertiseAddr)
	}
}

func TestNodeConfig_EmptyBindAddrErrors(t *testing.T) {
	if _, err := NewNodeConfig("c", "", 7946); err == nil {
		t.Error("empty BindAddr should error")
	}
}

func TestNodeConfig_DefaultGossipPort(t *testing.T) {
	nc, err := NewNodeConfig("c", "127.0.0.1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if nc.BindPort != defaultGossipPort {
		t.Errorf("BindPort = %d, want default %d", nc.BindPort, defaultGossipPort)
	}
}
