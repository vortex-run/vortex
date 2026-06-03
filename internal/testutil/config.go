//go:build integration

package testutil

import (
	"fmt"
	"net"
	"strings"
	"testing"
)

// MinimalConfig is a valid vortex.cue using only localhost backends and ports
// unlikely to collide. It is the baseline config for integration tests.
const MinimalConfig = `cluster: {
	name: "test-cluster"
	nodes: ["127.0.0.1"]
	gossip_port: 17946
	raft_port: 17947
}
tls: {
	acme_email: "test@example.com"
	provider: "internal"
	min_version: "TLS1.2"
}
routes: [
	{
		name: "test-route"
		host: "localhost"
		protocol: "http"
		backends: [{host: "127.0.0.1", port: 18080}]
	}
]
security: {
	block_tor: false
	block_clouds: false
	ip_allowlist: null
}
secrets: {
	store: "local"
	keys: []
	inject_env: false
}
observability: {
	metrics_path: "/metrics"
	tracing: false
	trace_endpoint: ""
	log_level: "debug"
	log_sink: "stderr"
	log_sampling: false
}
`

// InvalidConfig has a deliberate type error: cluster.name must be a string.
const InvalidConfig = `cluster: { name: 12345 }
`

// ConfigWithPort returns MinimalConfig with the route backend port replaced by
// backendPort, so distinct test configs can be generated. (The management API
// port is fixed at :9090 by the server today, so this varies the backend port
// rather than the API port; see testutil/process.go.)
func ConfigWithPort(backendPort int) string {
	return strings.Replace(MinimalConfig,
		"backends: [{host: \"127.0.0.1\", port: 18080}]",
		fmt.Sprintf("backends: [{host: \"127.0.0.1\", port: %d}]", backendPort),
		1,
	)
}

// FreePort returns an OS-assigned free TCP port, releasing the bind before
// returning so the caller can use the number.
func FreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}
