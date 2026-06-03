package config

import (
	"context"
	"log/slog"

	"github.com/vortex-run/vortex/pkg/lifecycle"
)

// Manager owns the live config Holder and the metadata needed to reload it. It
// bridges the config engine to pkg/lifecycle: on SIGHUP (or an explicit
// lifecycle reload) it re-reads and re-validates the config file, and on
// success atomically swaps the active config. On failure it logs and keeps the
// previous config — a bad reload never takes the process down
// (this is the M1.2 "never crash on reload" requirement).
type Manager struct {
	path   string
	holder *Holder
	log    *slog.Logger
}

// NewManager loads the config at path once (failing if it is invalid — startup
// must reject bad config per Rule #3) and returns a Manager wrapping it.
func NewManager(path string, log *slog.Logger) (*Manager, error) {
	cfg, err := Load(path)
	if err != nil {
		return nil, err
	}
	return &Manager{
		path:   pathOrDefault(path),
		holder: NewHolder(cfg),
		log:    log,
	}, nil
}

func pathOrDefault(path string) string {
	if path == "" {
		return DefaultPath
	}
	return path
}

// Holder returns the thread-safe holder for the active config, suitable for
// passing to subsystems that need to read config concurrently.
func (m *Manager) Holder() *Holder { return m.holder }

// Current returns the currently active config.
func (m *Manager) Current() *Config { return m.holder.Get() }

// Reload re-reads and re-validates the config file. On success it swaps in the
// new config and returns nil; on failure it returns the error and leaves the
// active config untouched. It never panics.
func (m *Manager) Reload() error {
	newCfg, err := Load(m.path)
	if err != nil {
		m.log.Error("config reload failed, keeping previous", "path", m.path, "err", err)
		return err
	}
	old := m.holder.Get()
	m.holder.Store(newCfg)
	m.log.Info("config reloaded successfully",
		"path", m.path,
		"old_hash", hashOf(old),
		"new_hash", newCfg.Hash(),
	)
	return nil
}

func hashOf(c *Config) string {
	if c == nil {
		return ""
	}
	return c.Hash()
}

// RegisterReload wires this Manager's Reload into the lifecycle Manager so that
// SIGHUP (Unix) or a programmatic lifecycle reload triggers it. The hook
// swallows the error after logging so a bad reload is non-fatal.
func (m *Manager) RegisterReload(lc *lifecycle.Manager) {
	lc.OnReload("config", func(context.Context) error {
		// Reload already logs success/failure; return nil so a failed reload
		// does not propagate as a lifecycle error (we keep running on old config).
		_ = m.Reload()
		return nil
	})
}
