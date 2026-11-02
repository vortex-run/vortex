package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/orchestration"
)

type mockOrchStore struct {
	runs    []OrchRunSummary
	tasks   map[string][]OrchTaskInfo
	listErr error
	loadErr error
}

func (m *mockOrchStore) ListRecent(limit int) ([]OrchRunSummary, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	if limit > 0 && len(m.runs) > limit {
		return m.runs[:limit], nil
	}
	return m.runs, nil
}

func (m *mockOrchStore) LoadRunTasks(runID string) ([]OrchTaskInfo, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	if m.tasks == nil {
		return nil, nil
	}
	return m.tasks[runID], nil
}

func orchStatusServer(t *testing.T, store OrchStoreReader) (*Server, string) {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("u", "default", []auth.Role{auth.RoleOperator}, "tok", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	if store != nil {
		s.SetOrchStoreProvider(store)
	}
	return s, secret
}

func TestAPI_OrchRunsEmptyWithoutProvider(t *testing.T) {
	s, secret := orchStatusServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Runs []OrchRunSummary `json:"runs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Runs) != 0 {
		t.Errorf("runs = %d, want 0", len(body.Runs))
	}
}

func TestAPI_OrchRunsReturnsProviderData(t *testing.T) {
	store := &mockOrchStore{
		runs: []OrchRunSummary{
			{ID: "r1", Goal: "ship", SessionID: "s1", Status: "running"},
			{ID: "r0", Goal: "old", SessionID: "s0", Status: "done"},
		},
	}
	s, secret := orchStatusServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Runs []OrchRunSummary `json:"runs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Runs) != 2 || body.Runs[0].ID != "r1" {
		t.Errorf("runs = %+v", body.Runs)
	}
}

func TestAPI_OrchRunsIncludesTerminalStatuses(t *testing.T) {
	store := &mockOrchStore{
		runs: []OrchRunSummary{
			{ID: "done-1", Goal: "ok", Status: orchestration.RunDone},
			{ID: "fail-1", Goal: "no", Status: orchestration.RunFailed},
		},
	}
	s, secret := orchStatusServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	var body struct {
		Runs []OrchRunSummary `json:"runs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Runs) != 2 {
		t.Fatalf("runs = %+v", body.Runs)
	}
	if body.Runs[0].Status != orchestration.RunDone || body.Runs[1].Status != orchestration.RunFailed {
		t.Errorf("statuses = %q, %q", body.Runs[0].Status, body.Runs[1].Status)
	}
}

func TestAPI_OrchRunsBoundedLimit(t *testing.T) {
	runs := make([]OrchRunSummary, 0, 10)
	for i := 0; i < 10; i++ {
		runs = append(runs, OrchRunSummary{ID: "r", Goal: "g", Status: "running"})
	}
	s, secret := orchStatusServer(t, &mockOrchStore{runs: runs})
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs?limit=3", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	var body struct {
		Runs []OrchRunSummary `json:"runs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Runs) != 3 {
		t.Errorf("limit=3 → %d runs", len(body.Runs))
	}
}

func TestAPI_OrchRunTasks(t *testing.T) {
	store := &mockOrchStore{
		tasks: map[string][]OrchTaskInfo{
			"run-a": {
				{ID: "t1", Name: "plan", State: "complete", AgentType: "planner"},
			},
		},
	}
	s, secret := orchStatusServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs/run-a/tasks", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		RunID string         `json:"run_id"`
		Tasks []OrchTaskInfo `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.RunID != "run-a" || len(body.Tasks) != 1 || body.Tasks[0].ID != "t1" {
		t.Errorf("body = %+v", body)
	}
}

func TestAPI_OrchRunsRequiresAuth(t *testing.T) {
	s, _ := orchStatusServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without key = %d, want 401", rec.Code)
	}
}

func TestAPI_OrchRunTasksRequiresAuth(t *testing.T) {
	s, _ := orchStatusServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs/run-a/tasks", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("without key = %d, want 401", rec.Code)
	}
}

// TestAPI_OrchEndpointsFollowDataPlaneContract pins the exposure contract
// documented on OrchTaskInfo: the orchestration read endpoints behave exactly
// like GET /api/agents/history/{id} — any valid key (any role) reads the full
// task payload, an invalid key is 401, and loopback gets no bypass. If this
// test starts failing because a role gate was added, update docs/security.md
// and the history endpoints together so the data plane stays consistent.
func TestAPI_OrchEndpointsFollowDataPlaneContract(t *testing.T) {
	store := &mockOrchStore{
		runs: []OrchRunSummary{{ID: "run-a", Goal: "g", SessionID: "s", Status: "done"}},
		tasks: map[string][]OrchTaskInfo{
			"run-a": {{ID: "t1", Name: "step", State: "complete", Input: "in", Result: "out",
				Meta: map[string]any{"permission_profile": "read_only"}}},
		},
	}
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	s.SetOrchStoreProvider(store)
	s.SetAgentRuntime(stubRuntime{})

	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleOperator, auth.RoleViewer, auth.RoleReadonly} {
		_, secret, err := keys.Issue("u-"+string(role), "default", []auth.Role{role}, "tok", 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{
			"/api/orchestration/runs",
			"/api/orchestration/runs/run-a/tasks",
			"/api/agents/history/sess-1", // the comparable existing endpoint
		} {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.RemoteAddr = "203.0.113.9:4444" // non-loopback
			req.Header.Set("X-API-Key", secret)
			rec := serve(s, req)
			if rec.Code != http.StatusOK {
				t.Errorf("role %s GET %s = %d, want 200 (data-plane: any valid key)", role, path, rec.Code)
			}
		}
		// Full payload (Input/Result/Meta) is returned to every role.
		req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs/run-a/tasks", nil)
		req.Header.Set("X-API-Key", secret)
		var body struct {
			Tasks []OrchTaskInfo `json:"tasks"`
		}
		_ = json.Unmarshal(serve(s, req).Body.Bytes(), &body)
		if len(body.Tasks) != 1 || body.Tasks[0].Input != "in" || body.Tasks[0].Result != "out" ||
			body.Tasks[0].Meta["permission_profile"] != "read_only" {
			t.Errorf("role %s: task payload = %+v", role, body.Tasks)
		}
	}

	// Invalid key → 401 (same JSON error shape the dashboard maps to ApiAuthError).
	for _, path := range []string{"/api/orchestration/runs", "/api/orchestration/runs/run-a/tasks"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-API-Key", "0123456789abcdef.deadbeef")
		if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
			t.Errorf("invalid key GET %s = %d, want 401", path, rec.Code)
		}
	}
}

func TestAPI_OrchRunTasksEmptyWithoutProvider(t *testing.T) {
	s, secret := orchStatusServer(t, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs/run-x/tasks", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		RunID string         `json:"run_id"`
		Tasks []OrchTaskInfo `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.RunID != "run-x" || len(body.Tasks) != 0 {
		t.Errorf("body = %+v", body)
	}
}

func TestAPI_OrchRunTasksBoundedLimit(t *testing.T) {
	tasks := make([]OrchTaskInfo, 0, 10)
	for i := 0; i < 10; i++ {
		tasks = append(tasks, OrchTaskInfo{ID: "t", Name: "step", State: "pending"})
	}
	store := &mockOrchStore{tasks: map[string][]OrchTaskInfo{"run-a": tasks}}
	s, secret := orchStatusServer(t, store)
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs/run-a/tasks?limit=3", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	var body struct {
		Tasks []OrchTaskInfo `json:"tasks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Tasks) != 3 {
		t.Errorf("limit=3 → %d tasks", len(body.Tasks))
	}
}

func TestAPI_OrchRunsInvalidLimitUsesDefault(t *testing.T) {
	runs := make([]OrchRunSummary, 0, 60)
	for i := 0; i < 60; i++ {
		runs = append(runs, OrchRunSummary{ID: "r", Goal: "g", Status: "running"})
	}
	s, secret := orchStatusServer(t, &mockOrchStore{runs: runs})
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs?limit=not-a-number", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	var body struct {
		Runs []OrchRunSummary `json:"runs"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if len(body.Runs) != 50 {
		t.Errorf("invalid limit → %d runs, want default 50", len(body.Runs))
	}
}

func TestAPI_OrchRunsListStoreError(t *testing.T) {
	s, secret := orchStatusServer(t, &mockOrchStore{listErr: errors.New("list failed")})
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestAPI_OrchRunTasksLoadError(t *testing.T) {
	s, secret := orchStatusServer(t, &mockOrchStore{loadErr: errors.New("load failed")})
	req := httptest.NewRequest(http.MethodGet, "/api/orchestration/runs/run-a/tasks", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
}

func TestOrchRunStoreAdapterRoundTrip(t *testing.T) {
	rs, err := orchestration.NewRunStore(filepath.Join(t.TempDir(), "orch.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rs.Close() })

	id, err := rs.CreateRun("goal", "sess", []*orchestration.Task{
		{ID: "t1", Name: "step", AgentType: "coder", State: orchestration.StatePending},
	})
	if err != nil {
		t.Fatal(err)
	}
	adapter := &OrchRunStoreAdapter{Store: rs}
	runs, err := adapter.ListRecent(10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRecent = %v, %v", runs, err)
	}
	if runs[0].Status != orchestration.RunRunning {
		t.Errorf("status = %q", runs[0].Status)
	}
	if err := rs.FinishRun(id, orchestration.RunDone, "ok"); err != nil {
		t.Fatal(err)
	}
	runs, err = adapter.ListRecent(10)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRecent after finish = %v, %v", runs, err)
	}
	if runs[0].Status != orchestration.RunDone {
		t.Errorf("after finish status = %q", runs[0].Status)
	}
	tasks, err := adapter.LoadRunTasks(id)
	if err != nil || len(tasks) != 1 || tasks[0].ID != "t1" {
		t.Fatalf("LoadRunTasks = %v, %v", tasks, err)
	}
}
