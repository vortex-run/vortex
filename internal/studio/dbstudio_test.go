package studio

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeExecutor is a stub QueryExecutor that echoes the query.
type fakeExecutor struct {
	lastQuery string
}

func (f *fakeExecutor) Query(_ context.Context, _ DBRoute, query string) (QueryResult, error) {
	f.lastQuery = query
	return QueryResult{Columns: []string{"result"}, Rows: [][]any{{"ok"}}}, nil
}

func (f *fakeExecutor) Schema(_ context.Context, _ DBRoute) (QueryResult, error) {
	return QueryResult{Columns: []string{"table"}, Rows: [][]any{{"users"}}}, nil
}

func newDBStudio(t *testing.T, readOnly bool, exec QueryExecutor, audit DBAuditLogger) *DBStudio {
	t.Helper()
	d, err := NewDBStudio(DBStudioConfig{
		Routes: []DBRoute{
			{Name: "pg", Kind: "postgres", ListenAddr: "127.0.0.1:5432"},
			{Name: "cache", Kind: "redis", ListenAddr: "127.0.0.1:6379"},
		},
		ReadOnly: readOnly,
		Executor: exec,
		AuditLog: audit,
		Logger:   discardLogger(),
	})
	if err != nil {
		t.Fatalf("NewDBStudio: %v", err)
	}
	return d
}

func doReq(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var r *http.Request
	if body != "" {
		r = httptest.NewRequest(method, target, strings.NewReader(body))
	} else {
		r = httptest.NewRequest(method, target, nil)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestDBStudio_Connections(t *testing.T) {
	d := newDBStudio(t, false, &fakeExecutor{}, nil)
	rec := doReq(t, d.Handler(), http.MethodGet, "/studio/db/connections", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Connections []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"connections"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Connections) != 2 {
		t.Errorf("connections = %d, want 2", len(resp.Connections))
	}
}

func TestDBStudio_ReadOnlyRejectsInsert(t *testing.T) {
	d := newDBStudio(t, true, &fakeExecutor{}, nil)
	rec := doReq(t, d.Handler(), http.MethodPost, "/studio/db/query",
		`{"connection":"pg","query":"INSERT INTO users VALUES (1)"}`)
	if rec.Code != http.StatusForbidden {
		t.Errorf("INSERT in read-only = %d, want 403", rec.Code)
	}
}

func TestDBStudio_ReadOnlyAllowsSelect(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDBStudio(t, true, exec, nil)
	rec := doReq(t, d.Handler(), http.MethodPost, "/studio/db/query",
		`{"connection":"pg","query":"SELECT * FROM users"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("SELECT in read-only = %d, want 200", rec.Code)
	}
	if exec.lastQuery != "SELECT * FROM users" {
		t.Errorf("executor got %q", exec.lastQuery)
	}
}

func TestDBStudio_ReadWriteAllowsInsert(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDBStudio(t, false, exec, nil) // ReadOnly=false
	rec := doReq(t, d.Handler(), http.MethodPost, "/studio/db/query",
		`{"connection":"pg","query":"INSERT INTO users VALUES (1)"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("INSERT in read-write = %d, want 200", rec.Code)
	}
}

func TestDBStudio_UnknownConnection404(t *testing.T) {
	d := newDBStudio(t, false, &fakeExecutor{}, nil)
	rec := doReq(t, d.Handler(), http.MethodPost, "/studio/db/query",
		`{"connection":"ghost","query":"SELECT 1"}`)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown connection = %d, want 404", rec.Code)
	}
}

func TestDBStudio_QueriesAudited(t *testing.T) {
	rec := &recordingAudit{}
	d := newDBStudio(t, false, &fakeExecutor{}, rec)
	_ = doReq(t, d.Handler(), http.MethodPost, "/studio/db/query",
		`{"connection":"pg","query":"SELECT 1"}`)
	if !rec.has("studio.db.query") {
		t.Error("query should be recorded to audit log")
	}
}

func TestDBStudio_SchemaEndpoint(t *testing.T) {
	d := newDBStudio(t, false, &fakeExecutor{}, nil)
	rec := doReq(t, d.Handler(), http.MethodGet, "/studio/db/schema?connection=pg", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("schema status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "users") {
		t.Errorf("schema body = %s, want table info", rec.Body.String())
	}
}

func TestDBStudio_NoExecutorReturns503(t *testing.T) {
	d := newDBStudio(t, false, nil, nil) // no executor
	rec := doReq(t, d.Handler(), http.MethodPost, "/studio/db/query",
		`{"connection":"pg","query":"SELECT 1"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("no executor = %d, want 503", rec.Code)
	}
}

func TestIsReadOnlyQuery(t *testing.T) {
	cases := []struct {
		q  string
		ro bool
	}{
		{"SELECT * FROM t", true},
		{"  select 1", true},
		{"SHOW TABLES", true},
		{"EXPLAIN SELECT 1", true},
		{"WITH x AS (SELECT 1) SELECT * FROM x", true},
		{"INSERT INTO t VALUES (1)", false},
		{"UPDATE t SET x=1", false},
		{"DELETE FROM t", false},
		{"DROP TABLE t", false},
		{"-- comment\nSELECT 1", true},
	}
	for _, c := range cases {
		if got := isReadOnlyQuery(c.q); got != c.ro {
			t.Errorf("isReadOnlyQuery(%q) = %v, want %v", c.q, got, c.ro)
		}
	}
}

func TestKindForPort(t *testing.T) {
	if KindForPort(5432) != "postgres" || KindForPort(6379) != "redis" {
		t.Error("KindForPort wrong for known ports")
	}
}
