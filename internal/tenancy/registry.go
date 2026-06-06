package tenancy

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// Registry errors.
var (
	ErrAlreadyExists = errors.New("tenancy: namespace already exists")
	ErrNotFound      = errors.New("tenancy: namespace not found")
)

// Registry holds all namespaces, keyed by ID. It is safe for concurrent use.
type Registry struct {
	mu  sync.RWMutex
	nss map[string]*Namespace
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{nss: make(map[string]*Namespace)}
}

// Create validates and stores a new namespace. It returns ErrAlreadyExists if
// the ID is taken.
func (r *Registry) Create(cfg NamespaceConfig) (*Namespace, error) {
	ns, err := NewNamespace(cfg)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nss[cfg.ID]; ok {
		return nil, fmt.Errorf("%w: %s", ErrAlreadyExists, cfg.ID)
	}
	r.nss[cfg.ID] = ns
	return ns, nil
}

// Get returns the namespace with id, or ErrNotFound.
func (r *Registry) Get(id string) (*Namespace, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ns, ok := r.nss[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return ns, nil
}

// List returns all namespaces for orgID. An empty orgID returns every
// namespace.
func (r *Registry) List(orgID string) []*Namespace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*Namespace
	for _, ns := range r.nss {
		if orgID == "" || ns.OrgID() == orgID {
			out = append(out, ns)
		}
	}
	return out
}

// Delete removes the namespace with id, or ErrNotFound.
func (r *Registry) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.nss[id]; !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	delete(r.nss, id)
	return nil
}

// Update changes a namespace's name and quotas. The ID and OrgID are immutable:
// cfg.ID must match id, and cfg.OrgID must match the existing OrgID.
func (r *Registry) Update(id string, cfg NamespaceConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	existing, ok := r.nss[id]
	if !ok {
		return fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	if cfg.OrgID != "" && cfg.OrgID != existing.OrgID() {
		return errors.New("tenancy: cannot change a namespace's OrgID")
	}
	updated := NamespaceConfig{
		ID:     id,
		Name:   cfg.Name,
		OrgID:  existing.OrgID(),
		Quotas: cfg.Quotas,
	}
	ns, err := NewNamespace(updated)
	if err != nil {
		return err
	}
	r.nss[id] = ns
	return nil
}

// Save persists the registry to path as a JSON array of namespace configs.
func (r *Registry) Save(path string) error {
	r.mu.RLock()
	configs := make([]NamespaceConfig, 0, len(r.nss))
	for _, ns := range r.nss {
		configs = append(configs, ns.Config())
	}
	r.mu.RUnlock()

	data, err := json.MarshalIndent(configs, "", "  ")
	if err != nil {
		return fmt.Errorf("tenancy: encoding registry: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("tenancy: writing registry %s: %w", path, err)
	}
	return nil
}

// Load replaces the registry contents with the namespaces in the JSON file at
// path. A missing file is treated as an empty registry (not an error).
func (r *Registry) Load(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("tenancy: reading registry %s: %w", path, err)
	}
	var configs []NamespaceConfig
	if err := json.Unmarshal(data, &configs); err != nil {
		return fmt.Errorf("tenancy: decoding registry: %w", err)
	}
	m := make(map[string]*Namespace, len(configs))
	for _, cfg := range configs {
		ns, err := NewNamespace(cfg)
		if err != nil {
			return fmt.Errorf("tenancy: invalid namespace in registry: %w", err)
		}
		m[cfg.ID] = ns
	}
	r.mu.Lock()
	r.nss = m
	r.mu.Unlock()
	return nil
}
