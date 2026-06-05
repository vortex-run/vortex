// Package policy provides VORTEX's authorization policy engine (build plan
// M3.4): it embeds the Open Policy Agent (OPA) Rego evaluator so operators can
// express request-authorization rules as .rego policies, hot-reloaded without a
// restart. When no policy is supplied the engine falls back to a built-in
// allow-all policy, so policy enforcement is opt-in and never blocks a fresh
// install.
package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/open-policy-agent/opa/v1/rego"
	"github.com/open-policy-agent/opa/v1/storage"
	"github.com/open-policy-agent/opa/v1/storage/inmem"
)

// defaultQueryPath is the Rego query evaluated for an authorization decision
// when EngineConfig.QueryPath is empty.
const defaultQueryPath = "data.vortex.allow"

// defaultPolicy is the built-in allow-all policy used when no .rego files are
// provided. It is intentionally permissive so policy enforcement is opt-in.
const defaultPolicy = `package vortex

default allow = true
`

// EngineConfig configures an Engine.
type EngineConfig struct {
	PolicyDir string // directory of .rego files; empty → built-in allow-all
	DataDir   string // optional directory of JSON data files
	QueryPath string // query to evaluate; default "data.vortex.allow"
}

// Engine evaluates Rego authorization policies. It is safe for concurrent use:
// Eval takes a read lock and Reload swaps the compiled query under a write lock,
// so evaluations never observe a half-built policy.
type Engine struct {
	cfg EngineConfig

	mu       sync.RWMutex
	prepared rego.PreparedEvalQuery
	usingDef bool // true when the built-in default policy is in effect
}

// NewEngine builds an Engine, loading every .rego file under cfg.PolicyDir (and
// any JSON data under cfg.DataDir). If no policy files are found it compiles the
// built-in allow-all policy. A policy that fails to compile is a fatal error.
func NewEngine(cfg EngineConfig) (*Engine, error) {
	if cfg.QueryPath == "" {
		cfg.QueryPath = defaultQueryPath
	}
	e := &Engine{cfg: cfg}
	prepared, usingDef, err := e.compile(context.Background())
	if err != nil {
		return nil, err
	}
	e.prepared = prepared
	e.usingDef = usingDef
	return e, nil
}

// compile reads the policy (and data) files and prepares a query. It returns the
// prepared query and whether the built-in default policy was used. It does not
// mutate the Engine, so Reload can build a new query without disturbing the
// live one until it succeeds.
func (e *Engine) compile(ctx context.Context) (rego.PreparedEvalQuery, bool, error) {
	var empty rego.PreparedEvalQuery

	regoFiles, err := listRegoFiles(e.cfg.PolicyDir)
	if err != nil {
		return empty, false, err
	}

	// We read .rego files ourselves and register them as named modules rather
	// than using rego.Load with file paths: OPA's loader mishandles Windows
	// volume-qualified paths (it drops the drive letter), and reading the bytes
	// directly is portable and keeps compilation independent of the loader.
	opts := []func(*rego.Rego){rego.Query(e.cfg.QueryPath)}
	usingDefault := false
	if len(regoFiles) == 0 {
		// No operator policy: fall back to the built-in allow-all module.
		opts = append(opts, rego.Module("vortex_default.rego", defaultPolicy))
		usingDefault = true
	} else {
		for _, path := range regoFiles {
			src, rerr := os.ReadFile(path) //nolint:gosec // path comes from listing PolicyDir
			if rerr != nil {
				return empty, false, fmt.Errorf("policy: reading %s: %w", path, rerr)
			}
			opts = append(opts, rego.Module(filepath.Base(path), string(src)))
		}
		store, serr := e.loadDataStore()
		if serr != nil {
			return empty, false, serr
		}
		if store != nil {
			opts = append(opts, rego.Store(store))
		}
	}

	prepared, err := rego.New(opts...).PrepareForEval(ctx)
	if err != nil {
		return empty, false, fmt.Errorf("policy: compiling policy: %w", err)
	}
	return prepared, usingDefault, nil
}

// Eval evaluates the policy query against input and reports whether the request
// is allowed. The decision is truthy: an explicit boolean true (or any non-false
// defined result) allows; false or an undefined/empty result denies. It returns
// an error only when evaluation itself fails, and never panics.
func (e *Engine) Eval(ctx context.Context, input map[string]any) (allowed bool, err error) {
	defer func() {
		if r := recover(); r != nil {
			allowed = false
			err = fmt.Errorf("policy: evaluation panicked: %v", r)
		}
	}()

	e.mu.RLock()
	prepared := e.prepared
	e.mu.RUnlock()

	rs, err := prepared.Eval(ctx, rego.EvalInput(input))
	if err != nil {
		return false, fmt.Errorf("policy: evaluating query: %w", err)
	}
	return resultIsAllow(rs), nil
}

// resultIsAllow reports whether a result set represents an allow decision. An
// undefined result (no bindings) denies; otherwise the first expression value
// must be truthy (true, or any value that is not the boolean false).
func resultIsAllow(rs rego.ResultSet) bool {
	if len(rs) == 0 || len(rs[0].Expressions) == 0 {
		return false
	}
	switch v := rs[0].Expressions[0].Value.(type) {
	case bool:
		return v
	case nil:
		return false
	default:
		return true
	}
}

// Reload re-reads the policy directory and recompiles. On success it atomically
// swaps in the new query; on failure it returns the error and leaves the
// previously-compiled policy in place, so a bad edit cannot take down
// enforcement.
func (e *Engine) Reload(ctx context.Context) error {
	prepared, usingDef, err := e.compile(ctx)
	if err != nil {
		return err
	}
	e.mu.Lock()
	e.prepared = prepared
	e.usingDef = usingDef
	e.mu.Unlock()
	return nil
}

// UsingDefault reports whether the built-in allow-all policy is currently in
// effect (i.e. no operator .rego files were loaded).
func (e *Engine) UsingDefault() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.usingDef
}

// DefaultPolicy returns the built-in allow-all policy text, useful for
// scaffolding a starter policy file.
func (e *Engine) DefaultPolicy() string { return defaultPolicy }

// listRegoFiles returns the .rego files in dir (non-recursive). An empty dir
// path yields no files (built-in default applies).
func listRegoFiles(dir string) ([]string, error) {
	return listByExt(dir, ".rego")
}

// loadDataStore builds an in-memory store from the JSON files under
// cfg.DataDir, merging each file's object into the store's root document. It
// returns nil (no store) when DataDir is unset or contains no JSON files.
func (e *Engine) loadDataStore() (storage.Store, error) {
	files, err := listByExt(e.cfg.DataDir, ".json")
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, nil
	}
	merged := map[string]any{}
	for _, path := range files {
		raw, rerr := os.ReadFile(path) //nolint:gosec // path comes from listing DataDir
		if rerr != nil {
			return nil, fmt.Errorf("policy: reading data %s: %w", path, rerr)
		}
		var doc map[string]any
		if jerr := json.Unmarshal(raw, &doc); jerr != nil {
			return nil, fmt.Errorf("policy: parsing data %s: %w", path, jerr)
		}
		for k, v := range doc {
			merged[k] = v
		}
	}
	return inmem.NewFromObject(merged), nil
}

// listByExt returns files in dir with the given extension. A missing directory
// is treated as empty, not an error, so an unconfigured PolicyDir/DataDir is
// fine.
func listByExt(dir, ext string) ([]string, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("policy: reading %s: %w", dir, err)
	}
	var out []string
	for _, ent := range entries {
		if ent.IsDir() || filepath.Ext(ent.Name()) != ext {
			continue
		}
		out = append(out, filepath.Join(dir, ent.Name()))
	}
	return out, nil
}
