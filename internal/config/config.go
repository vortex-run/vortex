// Package config implements VORTEX's CUE-based configuration engine (build
// plan M1.2).
//
// The user edits a single vortex.cue file. At startup it is unified against the
// embedded master schema (config/schema.cue). If it violates any type or
// constraint, or declares an unknown field, loading fails with the offending
// file, line, and field path and the process exits 1 — config is always valid
// or rejected, never validated lazily at first use (Non-Negotiable Rule #3).
//
// A loaded Config is exposed as plain typed Go structs. A Holder wraps the
// active *Config behind an atomic pointer so it can be read from many
// goroutines while SIGHUP-driven reloads swap it atomically (see reload.go).
//
// Secrets never appear in config — only secret names (Rule #2).
package config

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync/atomic"
)

// DefaultPath is where VORTEX looks for the config file when none is specified.
const DefaultPath = "vortex.cue"

// Config is the fully-typed, validated configuration. It is produced by Load
// and is safe to treat as immutable; reloads create a new Config rather than
// mutating an existing one.
type Config struct {
	Cluster       Cluster       `json:"cluster"`
	TLS           TLS           `json:"tls"`
	Routes        []Route       `json:"routes"`
	Security      Security      `json:"security"`
	Secrets       Secrets       `json:"secrets"`
	Observability Observability `json:"observability"`

	// hash is the SHA-256 of the canonical JSON encoding of the config,
	// computed once at load time. It lets /health report which config is live
	// so a reload can be verified externally.
	hash string
}

// Cluster mirrors #Cluster in schema.cue.
type Cluster struct {
	Name       string   `json:"name"`
	Nodes      []string `json:"nodes"`
	GossipPort int      `json:"gossip_port"`
	RaftPort   int      `json:"raft_port"`
}

// TLS mirrors #TLS.
type TLS struct {
	ACMEEmail  string `json:"acme_email"`
	Provider   string `json:"provider"`
	MinVersion string `json:"min_version"`
}

// Backend mirrors #Backend.
type Backend struct {
	Host   string `json:"host"`
	Port   int    `json:"port"`
	Weight int    `json:"weight"`
}

// HealthCheck mirrors #HealthCheck.
type HealthCheck struct {
	Path     string `json:"path"`
	Interval string `json:"interval"`
}

// RateLimit mirrors #RateLimit.
type RateLimit struct {
	RPM   int `json:"rpm"`
	Burst int `json:"burst"`
}

// Route mirrors #Route. Pointer fields are optional in the schema.
type Route struct {
	Name        string       `json:"name"`
	Protocol    string       `json:"protocol"`
	Host        string       `json:"host,omitempty"`
	Listen      int          `json:"listen,omitempty"`
	Backends    []Backend    `json:"backends"`
	HealthCheck *HealthCheck `json:"health_check,omitempty"`
	RateLimit   *RateLimit   `json:"rate_limit,omitempty"`
	Timeout     string       `json:"timeout,omitempty"`
	MTLS        bool         `json:"mtls"`
	Plugins     []string     `json:"plugins"`
}

// Security mirrors #Security.
type Security struct {
	BlockTor    bool     `json:"block_tor"`
	BlockClouds bool     `json:"block_clouds"`
	IPAllowlist []string `json:"ip_allowlist"`
}

// Secrets mirrors #Secrets — names only, never values.
type Secrets struct {
	Store     string   `json:"store"`
	Keys      []string `json:"keys"`
	InjectEnv bool     `json:"inject_env"`
}

// Observability mirrors #Observability.
type Observability struct {
	MetricsPath   string    `json:"metrics_path"`
	Tracing       bool      `json:"tracing"`
	TraceEndpoint string    `json:"trace_endpoint"`
	LogLevel      string    `json:"log_level"`
	LogSink       string    `json:"log_sink"`
	LogFile       string    `json:"log_file"`
	LogSampling   bool      `json:"log_sampling"`
	LogRotate     LogRotate `json:"log_rotate"`
}

// LogRotate mirrors #LogRotate.
type LogRotate struct {
	Enabled    bool `json:"enabled"`
	MaxSizeMB  int  `json:"max_size_mb"`
	MaxAgeDays int  `json:"max_age_days"`
	MaxBackups int  `json:"max_backups"`
	Compress   bool `json:"compress"`
}

// Hash returns the SHA-256 hex digest of this config's canonical encoding.
func (c *Config) Hash() string { return c.hash }

// computeHash sets c.hash from the canonical JSON encoding of c.
func (c *Config) computeHash() error {
	b, err := json.Marshal(c)
	if err != nil {
		return fmt.Errorf("hashing config: %w", err)
	}
	sum := sha256.Sum256(b)
	c.hash = fmt.Sprintf("%x", sum[:])
	return nil
}

// Holder is a thread-safe container for the active *Config. Many goroutines may
// call Get concurrently; Store atomically swaps in a new Config during reload.
type Holder struct {
	v atomic.Pointer[Config]
}

// NewHolder returns a Holder pre-loaded with cfg.
func NewHolder(cfg *Config) *Holder {
	h := &Holder{}
	h.v.Store(cfg)
	return h
}

// Get returns the currently active config. Never nil once initialized.
func (h *Holder) Get() *Config { return h.v.Load() }

// Store atomically replaces the active config.
func (h *Holder) Store(cfg *Config) { h.v.Store(cfg) }
