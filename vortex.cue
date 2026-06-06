// vortex.cue — validated at startup, hot-reload on SIGHUP.
//
// This is the file you edit on your server. It is validated against the master
// schema at startup; a wrong key name or type is a startup error with an exact
// line number, never a runtime surprise.
//
// Secrets NEVER appear here — only their names. Set values with:
//   vortex secret set jwt_secret "$(openssl rand -hex 32)"
// This file is safe to commit to Git.

cluster: {
	name:        "prod-cluster-1"
	nodes:       ["10.0.0.1", "10.0.0.2"]
	gossip_port: 7946 // UDP — internal only, never public
	raft_port:   7947 // TCP — internal only, never public
}

tls: {
	acme_email:  "you@example.com"
	provider:    "letsencrypt" // or "zerossl" or "internal"
	min_version: "TLS1.2"
}

// Plugins are opt-in. Install with:
//   vortex plugin install <path>
// Then add: plugins: ["plugin-name"] to any route.
routes: [
	{
		name:         "frontend"
		host:         "myapp.com"
		protocol:     "https"
		backends:     [{host: "127.0.0.1", port: 4000}]
		health_check: {path: "/health", interval: "10s"}
	},
	{
		name:       "api"
		host:       "api.myapp.com"
		protocol:   "https"
		backends:   [{host: "127.0.0.1", port: 3000}]
		rate_limit: {rpm: 600, burst: 50}
		timeout:    "30s"
		health_check: {path: "/api/health", interval: "10s"}
	},
	{
		name:     "postgres"
		protocol: "tcp"
		listen:   5432
		backends: [{host: "10.0.0.2", port: 5432}]
		mtls:     true
	},
	{
		name:     "redis"
		protocol: "tcp"
		listen:   6379
		backends: [{host: "10.0.0.2", port: 6379}]
		mtls:     true
	},
]

security: {
	block_tor:    true
	block_clouds: false
	ip_allowlist: null
}

secrets: {
	store:      "local" // or "vault", "aws-ssm", "gcp-sm"
	keys:       ["db_password", "jwt_secret", "redis_password"]
	inject_env: true
}

observability: {
	metrics_path:   "/metrics"
	tracing:        true
	trace_endpoint: "http://localhost:4318"
	log_level:      "info"
}
