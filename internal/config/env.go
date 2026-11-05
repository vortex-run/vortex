package config

import (
	"os"
	"strconv"
	"strings"

	"github.com/vortex-run/vortex/internal/legacyenv"
)

// envPrefix is prepended to every Gubern environment override.
const envPrefix = "GUBERN_"

// osEnv snapshots the current process environment into a map for override
// lookup. Taken once per load so a reload sees a consistent view.
func osEnv() map[string]string {
	m := make(map[string]string)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	legacyenv.Fill(m) // pre-rename VORTEX_* names still work
	return m
}

// applyEnvOverrides applies GUBERN_* environment variables on top of the
// decoded config. The naming convention is GUBERN_<SECTION>_<FIELD>, e.g.
// GUBERN_CLUSTER_NAME overrides cluster.name and GUBERN_OBSERVABILITY_LOG_LEVEL
// overrides observability.log_level.
//
// Only scalar top-level fields of each section are overridable — this is the
// operational escape hatch for tuning a single node without editing the
// committed gubern.cue, not a full config language. List/struct fields (routes,
// backends) are intentionally not overridable via env.
func applyEnvOverrides(cfg *Config, env map[string]string) {
	str := func(key string, dst *string) {
		if v, ok := env[envPrefix+key]; ok {
			*dst = v
		}
	}
	intv := func(key string, dst *int) {
		if v, ok := env[envPrefix+key]; ok {
			if n, err := strconv.Atoi(v); err == nil {
				*dst = n
			}
		}
	}
	boolv := func(key string, dst *bool) {
		if v, ok := env[envPrefix+key]; ok {
			if b, err := strconv.ParseBool(v); err == nil {
				*dst = b
			}
		}
	}

	// cluster
	str("CLUSTER_NAME", &cfg.Cluster.Name)
	intv("CLUSTER_GOSSIP_PORT", &cfg.Cluster.GossipPort)
	intv("CLUSTER_RAFT_PORT", &cfg.Cluster.RaftPort)

	// tls
	str("TLS_ACME_EMAIL", &cfg.TLS.ACMEEmail)
	str("TLS_PROVIDER", &cfg.TLS.Provider)
	str("TLS_MIN_VERSION", &cfg.TLS.MinVersion)

	// security
	boolv("SECURITY_BLOCK_TOR", &cfg.Security.BlockTor)
	boolv("SECURITY_BLOCK_CLOUDS", &cfg.Security.BlockClouds)

	// secrets
	str("SECRETS_STORE", &cfg.Secrets.Store)
	boolv("SECRETS_INJECT_ENV", &cfg.Secrets.InjectEnv)

	// observability
	str("OBSERVABILITY_METRICS_PATH", &cfg.Observability.MetricsPath)
	boolv("OBSERVABILITY_TRACING", &cfg.Observability.Tracing)
	str("OBSERVABILITY_TRACE_ENDPOINT", &cfg.Observability.TraceEndpoint)
	str("OBSERVABILITY_LOG_LEVEL", &cfg.Observability.LogLevel)

	// agents.local_tools
	str("AGENTS_LOCAL_TOOLS_TRUSTED_ROOT", &cfg.Agents.LocalTools.TrustedRoot)
	boolv("AGENTS_LOCAL_TOOLS_AUTO_MODE", &cfg.Agents.LocalTools.AutoMode)
}
