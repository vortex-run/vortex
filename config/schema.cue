// VORTEX master configuration schema (build plan M1.2).
//
// This file defines the shape, types, constraints, and defaults for the
// user-edited vortex.cue. The config engine unifies a user's vortex.cue against
// #Config below; any field that violates a type or constraint, or any unknown
// field, is rejected at startup with a file:line and field path
// (Non-Negotiable Rule #3: config is always valid or rejected).
//
// Secrets never appear here or in vortex.cue — only secret *names* (Rule #2).
package vortex

// #Config is the root schema. The user's vortex.cue is unified with this.
#Config: {
	cluster:       #Cluster
	tls:           #TLS
	routes: [...#Route]
	security:      #Security
	secrets:       #Secrets
	observability: #Observability
}

// #Cluster describes this node's membership in a VORTEX cluster.
#Cluster: {
	// name is the cluster identifier; required, non-empty.
	name: string & !=""
	// nodes is the list of peer node addresses (IP or host). May be empty for
	// a single-node deployment.
	nodes: [...string] | *[]
	// gossip_port is the UDP SWIM gossip port — internal only, never public.
	gossip_port: int & >0 & <=65535 | *7946
	// raft_port is the TCP Raft consensus port — internal only, never public.
	raft_port: int & >0 & <=65535 | *7947
}

// #TLS configures certificate acquisition and the minimum protocol version.
#TLS: {
	// acme_email is the contact email for the ACME account (Let's Encrypt).
	// Required when provider is an ACME CA; we require it generally for clarity.
	acme_email: string & =~"^[^@\\s]+@[^@\\s]+\\.[^@\\s]+$"
	// provider selects the certificate source.
	provider: "letsencrypt" | "zerossl" | "internal" | *"letsencrypt"
	// min_version is the minimum negotiated TLS version.
	min_version: "TLS1.2" | "TLS1.3" | *"TLS1.2"
}

// #Backend is a single upstream target for a route.
#Backend: {
	host:    string & !=""
	port:    int & >0 & <=65535
	// weight biases load balancing across backends; defaults to 1.
	weight: int & >=0 | *1
}

// #HealthCheck describes how a route's backends are probed.
#HealthCheck: {
	path:     string & !="" | *"/health"
	interval: #Duration | *"10s"
}

// #RateLimit bounds request rate for a route.
#RateLimit: {
	rpm:   int & >0
	burst: int & >0 | *(rpm div 60 + 1)
}

// #Route is one routing rule. The protocol determines which other fields apply.
#Route: {
	// name is a unique, human-readable identifier for the route; required.
	name: string & !=""
	// protocol selects the proxy behavior for this route.
	protocol: "https" | "http" | "h3" | "tcp" | "udp" | "ws" | "grpc"

	// host is the matched Host header / SNI for L7 protocols.
	host?: string & !=""
	// listen is the local port for L4 (tcp/udp) routes.
	listen?: int & >0 & <=65535

	// backends are the upstream targets; at least one is required.
	backends: [#Backend, ...#Backend]

	// Optional cross-cutting settings.
	health_check?: #HealthCheck
	rate_limit?:   #RateLimit
	timeout?:      #Duration
	mtls:          bool | *false
	plugins: [...string] | *[]
	// namespace_id assigns the route to a tenant namespace for quota
	// enforcement and isolation; empty means no tenancy.
	namespace_id: string | *""
}

// #Security configures edge-level protections.
#Security: {
	block_tor:    bool | *false
	block_clouds: bool | *false
	// ip_allowlist, when non-null, restricts access to the listed CIDRs/IPs.
	ip_allowlist: [...string] | *null
}

// #Secrets declares secret *names* only. Values live in the encrypted store
// and are injected at runtime (Rule #2).
#Secrets: {
	store: "local" | "vault" | "aws-ssm" | "gcp-sm" | *"local"
	keys: [...string] | *[]
	inject_env: bool | *true
}

// #Observability configures metrics, tracing, and logging.
#Observability: {
	metrics_path:   string & =~"^/" | *"/metrics"
	tracing:        bool | *false
	trace_endpoint: string | *""
	log_level:      "debug" | "info" | "warn" | "error" | *"info"

	// Logging output and rotation (M1.5).
	log_sink:     "auto" | "stdout" | "stderr" | "file" | *"auto"
	log_file:     string | *"/var/log/vortex/vortex.log"
	log_sampling: bool | *false
	log_rotate:   #LogRotate
}

// #LogRotate configures file-based log rotation (used when log_sink is "file").
#LogRotate: {
	enabled:      bool | *true
	max_size_mb:  int & >=1 & <=10000 | *100
	max_age_days: int & >=1 | *7
	max_backups:  int & >=1 | *5
	compress:     bool | *true
}

// #Duration is a Go time.Duration string, e.g. "10s", "500ms", "30s", "1m".
#Duration: =~"^[0-9]+(ns|us|µs|ms|s|m|h)$"
