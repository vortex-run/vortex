package studio

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
)

// DBAuditLogger records DB Studio query activity. Satisfied by *audit.Log.
type DBAuditLogger interface {
	Append(ctx context.Context, actor, action, resource string, detail map[string]any) error
}

// DBRoute describes a database exposed via a VORTEX mTLS TCP route.
type DBRoute struct {
	Name       string // route name from vortex.cue
	Kind       string // "postgres" | "mysql" | "redis" | "mongodb"
	ListenAddr string // address of the mTLS TCP listener to connect through
}

// QueryResult is the outcome of a query execution.
type QueryResult struct {
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Error   string   `json:"error,omitempty"`
}

// QueryExecutor runs a query against a database route and returns its result.
// It is an interface so the actual driver (added when a DB driver dependency is
// approved) is decoupled from the HTTP/validation layer; tests inject a fake.
type QueryExecutor interface {
	Query(ctx context.Context, route DBRoute, query string) (QueryResult, error)
	Schema(ctx context.Context, route DBRoute) (QueryResult, error)
}

// DBStudioConfig configures the browser DB GUI.
type DBStudioConfig struct {
	Routes   []DBRoute
	ReadOnly bool // when true, only read queries (SELECT/SHOW/...) are allowed
	AuditLog DBAuditLogger
	Executor QueryExecutor // nil → queries return a "not configured" result
	Logger   *slog.Logger
}

// DBStudio serves the browser database GUI under /studio/db/.
type DBStudio struct {
	cfg    DBStudioConfig
	log    *slog.Logger
	routes map[string]DBRoute
}

// NewDBStudio constructs the DB studio.
func NewDBStudio(cfg DBStudioConfig) (*DBStudio, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	routes := make(map[string]DBRoute, len(cfg.Routes))
	for _, r := range cfg.Routes {
		routes[r.Name] = r
	}
	return &DBStudio{cfg: cfg, log: cfg.Logger, routes: routes}, nil
}

// Handler returns the DB studio HTTP handler mounted at /studio/db/.
func (d *DBStudio) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /studio/db/connections", d.handleConnections)
	mux.HandleFunc("POST /studio/db/query", d.handleQuery)
	mux.HandleFunc("GET /studio/db/schema", d.handleSchema)
	return mux
}

// handleConnections lists configured database connections.
func (d *DBStudio) handleConnections(w http.ResponseWriter, _ *http.Request) {
	type conn struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	out := make([]conn, 0, len(d.cfg.Routes))
	for _, r := range d.cfg.Routes {
		out = append(out, conn{Name: r.Name, Kind: r.Kind})
	}
	writeJSON(w, http.StatusOK, map[string]any{"connections": out})
}

// queryRequest is the POST /studio/db/query body.
type queryRequest struct {
	Connection string `json:"connection"`
	Query      string `json:"query"`
}

// handleQuery executes a query against a named connection, enforcing read-only
// mode and recording the query to the audit log.
func (d *DBStudio) handleQuery(w http.ResponseWriter, r *http.Request) {
	var req queryRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON"})
		return
	}
	route, ok := d.routes[req.Connection]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown connection"})
		return
	}
	if d.cfg.ReadOnly && !isReadOnlyQuery(req.Query) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error": "read-only mode: only SELECT/SHOW queries are allowed",
		})
		return
	}
	d.audit(r.Context(), "studio.db.query", req.Connection, req.Query)

	if d.cfg.Executor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "no database executor configured",
		})
		return
	}
	res, err := d.cfg.Executor.Query(r.Context(), route, req.Query)
	if err != nil {
		writeJSON(w, http.StatusOK, QueryResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSchema returns the table/column info for a connection.
func (d *DBStudio) handleSchema(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("connection")
	route, ok := d.routes[name]
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "unknown connection"})
		return
	}
	if d.cfg.Executor == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{
			"error": "no database executor configured",
		})
		return
	}
	res, err := d.cfg.Executor.Schema(r.Context(), route)
	if err != nil {
		writeJSON(w, http.StatusOK, QueryResult{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// audit records a query when an audit log is configured (the query text is the
// detail; it is not a secret value).
func (d *DBStudio) audit(ctx context.Context, action, conn, query string) {
	if d.cfg.AuditLog == nil {
		return
	}
	_ = d.cfg.AuditLog.Append(ctx, "studio", action, conn, map[string]any{"query": query})
}

// readOnlyPrefixes are the SQL/command verbs considered non-mutating.
var readOnlyPrefixes = []string{"select", "show", "explain", "describe", "desc", "with"}

// isReadOnlyQuery reports whether query is a read-only statement. It is a
// conservative allow-list: anything not starting with a known read verb (after
// trimming comments/whitespace) is treated as a write.
func isReadOnlyQuery(query string) bool {
	q := strings.ToLower(strings.TrimSpace(query))
	// Strip a leading line comment if present.
	if strings.HasPrefix(q, "--") {
		if i := strings.IndexByte(q, '\n'); i >= 0 {
			q = strings.TrimSpace(q[i+1:])
		} else {
			return true // comment-only
		}
	}
	for _, p := range readOnlyPrefixes {
		if strings.HasPrefix(q, p+" ") || q == p {
			return true
		}
	}
	return false
}

// writeJSON writes v as a JSON response with the given status.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// KindForPort guesses a database kind from a well-known port (best effort).
func KindForPort(port int) string {
	switch port {
	case 5432:
		return "postgres"
	case 3306:
		return "mysql"
	case 6379:
		return "redis"
	case 27017:
		return "mongodb"
	default:
		return fmt.Sprintf("tcp:%d", port)
	}
}
