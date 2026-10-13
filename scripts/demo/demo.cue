// demo.cue — the configuration the demo environment runs (scripts/demo/README.md).
//
// This is NOT a deployment template; use the repository's vortex.cue for that.
// It exists to make a recorded demo show a working system, and it differs from
// the template in three deliberate ways:
//
//   1. Every backend is 127.0.0.1 on a port scripts/demo/backends serves, so
//      health checks actually pass. The template points at 10.0.0.2 and
//      myapp.com, which resolve to nothing, so every route renders red.
//   2. Every route sets an explicit `listen`. A route without one binds :0 (see
//      listenAddr in internal/proxy/manager.go), which picks a random port —
//      fine for a template that is never started, useless for a demo where the
//      URL has to be typed on camera.
//   3. tls.provider is "internal", so startup issues a self-signed certificate
//      instead of reaching ACME. A demo must run offline and must not burn
//      Let's Encrypt rate limits on a throwaway hostname.
//
// Ports are all above 1024 so the demo needs no elevation.

cluster: {
	name:        "demo"
	nodes:       []
	gossip_port: 7946
	raft_port:   7947
}

tls: {
	acme_email:  "demo@vortex.local"
	provider:    "internal" // self-signed: no network, no rate limits
	min_version: "TLS1.2"
}

routes: [
	{
		name:     "frontend"
		host:     "demo.localhost"
		protocol: "https"
		listen:   8443
		backends: [{host: "127.0.0.1", port: 4000}]
		health_check: {path: "/health", interval: "5s"}
	},
	{
		name:       "api"
		host:       "api.demo.localhost"
		protocol:   "https"
		listen:     8444
		backends:   [{host: "127.0.0.1", port: 3000}]
		rate_limit: {rpm: 600, burst: 50}
		timeout:    "30s"
		health_check: {path: "/api/health", interval: "5s"}
	},
	{
		name:     "staging"
		host:     "staging.localhost"
		protocol: "http"
		listen:   8080
		backends: [{host: "127.0.0.1", port: 4100}]
		health_check: {path: "/health", interval: "5s"}
	},
	{
		// An L4 route, so the demo shows VORTEX proxying something that is not
		// HTTP. mtls is false: true requires the cluster identity mesh, which a
		// single-node demo has no peers for.
		name:     "postgres"
		protocol: "tcp"
		listen:   15432
		backends: [{host: "127.0.0.1", port: 15433}]
		mtls:     false
	},
]

security: {
	block_tor:    true
	block_clouds: false
	ip_allowlist: null
}

secrets: {
	// Named here, values set by demo.ps1 with `vortex secret set`, so the
	// Secrets view shows real encrypted entries rather than an empty table.
	store:      "local"
	keys:       ["db_password", "jwt_secret"]
	inject_env: true
}

observability: {
	metrics_path: "/metrics"
	// tracing off: with no collector on 4318 the log fills with export errors,
	// which is the first thing a viewer's eye lands on.
	tracing:   false
	log_level: "info"
}
