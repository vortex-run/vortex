package observability

import (
	"io"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestCompat_MetricNames snapshots every metric name operators' dashboards
// and alerts may query. Renaming or dropping one silently breaks them; a
// rename should emit both names for a release (see the Gubern migration
// design) and update this snapshot in the same change.
func TestCompat_MetricNames(t *testing.T) {
	m := NewMetrics("vortex")
	// Vector metrics only appear once they have a series.
	m.RecordRequest("r", "GET", 200, time.Millisecond)
	m.SetActiveConns("r", "http", 1)
	m.RecordBytes("r", 1, 1)
	m.RecordRouteError("r", "x")
	m.SetClusterMembers(1)
	m.RecordPolicyEval("allow")
	m.RecordSecretOp("get")
	m.RecordOrchestrationRun()
	m.TaskStarted()
	m.TaskFinished("general", "ok", time.Millisecond)

	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	var got []string
	for _, line := range strings.Split(string(body), "\n") {
		if strings.HasPrefix(line, "# TYPE ") {
			got = append(got, strings.Fields(line)[2])
		}
	}
	sort.Strings(got)
	want := []string{
		"vortex_active_connections",
		"vortex_bytes_in_total",
		"vortex_bytes_out_total",
		"vortex_cluster_members",
		"vortex_orchestration_runs_total",
		"vortex_orchestration_task_duration_seconds",
		"vortex_orchestration_tasks_active",
		"vortex_orchestration_tasks_total",
		"vortex_policy_evaluations_total",
		"vortex_request_duration_seconds",
		"vortex_requests_total",
		"vortex_route_errors_total",
		"vortex_secret_operations_total",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("metric names changed:\n got: %v\nwant: %v", got, want)
	}
}

func TestCompat_DefaultNamespace(t *testing.T) {
	m := NewMetrics("")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	body, _ := io.ReadAll(rec.Body)
	if !strings.Contains(string(body), "# TYPE vortex_cluster_members ") {
		t.Errorf("default namespace is no longer vortex:\n%s", body)
	}
}
