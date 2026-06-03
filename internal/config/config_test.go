package config

import (
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/pkg/lifecycle"
)

// validConfig is a minimal but complete config that satisfies the schema.
const validConfig = `
cluster: {
	name:  "prod-cluster-1"
	nodes: ["10.0.0.1", "10.0.0.2"]
}
tls: {acme_email: "you@example.com"}
routes: [
	{name: "frontend", protocol: "https", host: "myapp.com", backends: [{host: "127.0.0.1", port: 4000}]},
	{name: "postgres", protocol: "tcp", listen: 5432, backends: [{host: "10.0.0.2", port: 5432}], mtls: true},
]
security: {block_tor: true}
secrets: {keys: ["db_password", "jwt_secret"]}
observability: {log_level: "debug"}
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "vortex.cue")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestLoadValidConfig(t *testing.T) {
	cfg, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Cluster.Name != "prod-cluster-1" {
		t.Errorf("cluster.name = %q, want prod-cluster-1", cfg.Cluster.Name)
	}
	// Defaults from schema applied.
	if cfg.Cluster.GossipPort != 7946 {
		t.Errorf("gossip_port default = %d, want 7946", cfg.Cluster.GossipPort)
	}
	if cfg.Cluster.RaftPort != 7947 {
		t.Errorf("raft_port default = %d, want 7947", cfg.Cluster.RaftPort)
	}
	if cfg.TLS.Provider != "letsencrypt" {
		t.Errorf("tls.provider default = %q, want letsencrypt", cfg.TLS.Provider)
	}
	if cfg.TLS.MinVersion != "TLS1.2" {
		t.Errorf("tls.min_version default = %q, want TLS1.2", cfg.TLS.MinVersion)
	}
	if len(cfg.Routes) != 2 {
		t.Fatalf("routes = %d, want 2", len(cfg.Routes))
	}
	if cfg.Routes[0].Backends[0].Weight != 1 {
		t.Errorf("backend weight default = %d, want 1", cfg.Routes[0].Backends[0].Weight)
	}
	if !cfg.Routes[1].MTLS {
		t.Error("postgres route should have mtls=true")
	}
	if cfg.Observability.LogLevel != "debug" {
		t.Errorf("log_level = %q, want debug", cfg.Observability.LogLevel)
	}
	if cfg.Observability.MetricsPath != "/metrics" {
		t.Errorf("metrics_path default = %q, want /metrics", cfg.Observability.MetricsPath)
	}
	if cfg.Hash() == "" {
		t.Error("config hash should be non-empty")
	}
}

func TestLoadMissingRequiredField(t *testing.T) {
	// cluster.name is required (string & !=""); omit it.
	body := `
cluster: {nodes: ["10.0.0.1"]}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("expected error for missing cluster.name")
	}
	if !strings.Contains(err.Error(), "name") {
		t.Errorf("error should mention the field 'name': %v", err)
	}
}

func TestLoadWrongType(t *testing.T) {
	body := `
cluster: {name: 123}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
	var les LoadErrors
	if !errors.As(err, &les) {
		// Single error may be returned as *LoadError; accept either.
		var le *LoadError
		if !errors.As(err, &le) {
			t.Fatalf("error is not a LoadError(s): %T %v", err, err)
		}
		les = LoadErrors{le}
	}
	found := false
	for _, e := range les {
		if strings.Contains(e.Field, "name") || strings.Contains(e.Message, "name") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an error referencing field 'name', got: %v", err)
	}
}

func TestLoadUnknownFieldRejected(t *testing.T) {
	body := `
cluster: {name: "c", bogus_field: "x"}
tls: {acme_email: "a@b.com"}
routes: []
security: {}
secrets: {}
observability: {}
`
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("expected error for unknown field (closed schema)")
	}
}

func TestLoadReportsLineNumber(t *testing.T) {
	// Out-of-range port; CUE attributes a position to the violation.
	body := "cluster: {\n  name: \"c\"\n  gossip_port: 99999\n}\ntls: {acme_email: \"a@b.com\"}\nroutes: []\nsecurity: {}\nsecrets: {}\nobservability: {}\n"
	_, err := Load(writeConfig(t, body))
	if err == nil {
		t.Fatal("expected error for out-of-range port")
	}
	var les LoadErrors
	if !errors.As(err, &les) {
		t.Fatalf("expected LoadErrors, got %T", err)
	}
	hasLine := false
	for _, e := range les {
		if e.Line > 0 {
			hasLine = true
		}
	}
	if !hasLine {
		t.Errorf("expected at least one error with a line number, got: %v", err)
	}
}

func TestEnvOverride(t *testing.T) {
	p := writeConfig(t, validConfig)
	src, _ := os.ReadFile(p)
	env := map[string]string{
		"VORTEX_CLUSTER_NAME":            "overridden",
		"VORTEX_OBSERVABILITY_LOG_LEVEL": "error",
		"VORTEX_CLUSTER_GOSSIP_PORT":     "8000",
		"VORTEX_SECURITY_BLOCK_CLOUDS":   "true",
	}
	cfg, err := loadFromBytes(p, src, schemaSource(t), env)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Cluster.Name != "overridden" {
		t.Errorf("cluster.name override = %q, want overridden", cfg.Cluster.Name)
	}
	if cfg.Observability.LogLevel != "error" {
		t.Errorf("log_level override = %q, want error", cfg.Observability.LogLevel)
	}
	if cfg.Cluster.GossipPort != 8000 {
		t.Errorf("gossip_port override = %d, want 8000", cfg.Cluster.GossipPort)
	}
	if !cfg.Security.BlockClouds {
		t.Error("block_clouds override should be true")
	}
}

func TestHolderConcurrentReadWrite(t *testing.T) {
	cfg1, err := Load(writeConfig(t, validConfig))
	if err != nil {
		t.Fatal(err)
	}
	h := NewHolder(cfg1)

	done := make(chan struct{})
	// Concurrent readers.
	for i := 0; i < 8; i++ {
		go func() {
			for j := 0; j < 1000; j++ {
				_ = h.Get().Cluster.Name
			}
			done <- struct{}{}
		}()
	}
	// Concurrent writer swapping configs.
	go func() {
		for j := 0; j < 1000; j++ {
			h.Store(cfg1)
		}
		done <- struct{}{}
	}()
	for i := 0; i < 9; i++ {
		<-done
	}
	if h.Get() == nil {
		t.Error("holder should never be nil")
	}
}

func TestManagerReloadValidSwapsAtomically(t *testing.T) {
	p := writeConfig(t, validConfig)
	mgr, err := NewManager(p, discardLogger())
	if err != nil {
		t.Fatalf("initial load failed: %v", err)
	}
	oldHash := mgr.Current().Hash()

	// Rewrite the same file with a different cluster name → different hash.
	newBody := strings.Replace(validConfig, "prod-cluster-1", "prod-cluster-2", 1)
	if err := os.WriteFile(p, []byte(newBody), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Reload(); err != nil {
		t.Fatalf("reload of valid config should succeed: %v", err)
	}
	if mgr.Current().Cluster.Name != "prod-cluster-2" {
		t.Errorf("after reload cluster.name = %q, want prod-cluster-2", mgr.Current().Cluster.Name)
	}
	if mgr.Current().Hash() == oldHash {
		t.Error("config hash should change after a meaningful reload")
	}
}

func TestManagerReloadInvalidKeepsOldConfig(t *testing.T) {
	p := writeConfig(t, validConfig)
	mgr, err := NewManager(p, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	oldHash := mgr.Current().Hash()
	oldName := mgr.Current().Cluster.Name

	// Corrupt the file.
	if err := os.WriteFile(p, []byte(`cluster: {name: 123}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := mgr.Reload(); err == nil {
		t.Fatal("reload of invalid config should return an error")
	}
	// Old config preserved, no crash.
	if mgr.Current().Cluster.Name != oldName {
		t.Errorf("after failed reload cluster.name = %q, want preserved %q", mgr.Current().Cluster.Name, oldName)
	}
	if mgr.Current().Hash() != oldHash {
		t.Error("config hash should be unchanged after a failed reload")
	}
}

func TestRegisterReloadWiresLifecycleHook(t *testing.T) {
	p := writeConfig(t, validConfig)
	mgr, err := NewManager(p, discardLogger())
	if err != nil {
		t.Fatal(err)
	}
	lc := lifecycle.New(lifecycle.Config{Logger: discardLogger()})
	mgr.RegisterReload(lc)

	// Change config, then drive a reload through the lifecycle manager.
	newBody := strings.Replace(validConfig, "prod-cluster-1", "reloaded-via-hook", 1)
	if err := os.WriteFile(p, []byte(newBody), 0o600); err != nil {
		t.Fatal(err)
	}
	lc.Reload()

	if mgr.Current().Cluster.Name != "reloaded-via-hook" {
		t.Errorf("lifecycle reload did not swap config: got %q", mgr.Current().Cluster.Name)
	}
}

func TestNewManagerRejectsInvalidConfig(t *testing.T) {
	p := writeConfig(t, `cluster: {name: 123}`)
	if _, err := NewManager(p, discardLogger()); err == nil {
		t.Fatal("NewManager should fail on invalid config (startup must reject)")
	}
}

func TestLoadFileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "does-not-exist.cue"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// schemaSource returns the embedded schema bytes for tests that call
// loadFromBytes directly.
func schemaSource(t *testing.T) []byte {
	t.Helper()
	return schemaBytes()
}
