package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

// stubForge is a fake ForgeRuntime for API tests.
type stubForge struct {
	job ForgeJob
}

func (s *stubForge) Submit(_ context.Context, message, _ string, _ int64) string {
	s.job = ForgeJob{ID: "job-1", Message: message, State: "running"}
	return "job-1"
}
func (s *stubForge) Get(id string) (ForgeJob, bool) {
	if id == s.job.ID {
		return s.job, true
	}
	return ForgeJob{}, false
}
func (s *stubForge) List() []ForgeJob { return []ForgeJob{s.job} }

func newForgeServer(t *testing.T, rt ForgeRuntime) (*Server, string) {
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
	s.SetForgeRuntime(rt)
	return s, secret
}

func TestForge_BuildReturnsJobID(t *testing.T) {
	s, secret := newForgeServer(t, &stubForge{})
	req := httptest.NewRequest(http.MethodPost, "/api/forge/build",
		strings.NewReader(`{"message":"build a todo app","chat_id":5}`))
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var body struct {
		JobID  string `json:"job_id"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.JobID == "" || body.Status != "started" {
		t.Errorf("response = %+v, want job_id + started", body)
	}
}

func TestForge_BuildRequiresAuth(t *testing.T) {
	s, _ := newForgeServer(t, &stubForge{})
	req := httptest.NewRequest(http.MethodPost, "/api/forge/build",
		strings.NewReader(`{"message":"x"}`))
	req.RemoteAddr = "127.0.0.1:5555" // loopback must NOT bypass
	rec := serve(s, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("build without key = %d, want 401", rec.Code)
	}
}

func TestForge_BuildRejectsEmptyMessage(t *testing.T) {
	s, secret := newForgeServer(t, &stubForge{})
	req := httptest.NewRequest(http.MethodPost, "/api/forge/build", strings.NewReader(`{"message":""}`))
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("empty message = %d, want 400", rec.Code)
	}
}

func TestForge_StatusEndpoint(t *testing.T) {
	sf := &stubForge{}
	s, secret := newForgeServer(t, sf)
	// First submit so the job exists.
	sf.Submit(context.Background(), "x", "", 0)

	req := httptest.NewRequest(http.MethodGet, "/api/forge/status/job-1", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var job ForgeJob
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	if job.ID != "job-1" {
		t.Errorf("job id = %q, want job-1", job.ID)
	}
}

func TestForge_StatusNotFound(t *testing.T) {
	s, secret := newForgeServer(t, &stubForge{})
	req := httptest.NewRequest(http.MethodGet, "/api/forge/status/ghost", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown job = %d, want 404", rec.Code)
	}
}

func TestForge_JobsList(t *testing.T) {
	sf := &stubForge{}
	sf.Submit(context.Background(), "x", "", 0)
	s, secret := newForgeServer(t, sf)
	req := httptest.NewRequest(http.MethodGet, "/api/forge/jobs", nil)
	req.Header.Set("X-API-Key", secret)
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body struct {
		Jobs []ForgeJob `json:"jobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Jobs) != 1 {
		t.Errorf("jobs = %d, want 1", len(body.Jobs))
	}
}

func TestForge_503WhenUnconfigured(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	// No SetForgeRuntime, no auth → localhost reaches handler, gets 503.
	req := httptest.NewRequest(http.MethodGet, "/api/forge/jobs", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	rec := serve(s, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured forge = %d, want 503", rec.Code)
	}
}
