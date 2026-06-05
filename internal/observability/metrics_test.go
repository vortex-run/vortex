package observability

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// scrape returns the /metrics text output of m.
func scrape(t *testing.T, m *Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	return string(body)
}

func TestMetrics_NewCreatesRegistry(t *testing.T) {
	m := NewMetrics("vortex")
	if m == nil || m.registry == nil {
		t.Fatal("NewMetrics should create a registry")
	}
}

func TestMetrics_RecordRequestIncrementsTotal(t *testing.T) {
	m := NewMetrics("vortex")
	m.RecordRequest("web", "GET", 200, 5*time.Millisecond)
	m.RecordRequest("web", "GET", 200, 7*time.Millisecond)

	out := scrape(t, m)
	if !strings.Contains(out, `vortex_requests_total{method="GET",route="web",status="2xx"} 2`) {
		t.Errorf("requests_total not incremented to 2:\n%s", out)
	}
}

func TestMetrics_RecordRequestObservesDuration(t *testing.T) {
	m := NewMetrics("vortex")
	m.RecordRequest("api", "POST", 201, 30*time.Millisecond)

	out := scrape(t, m)
	if !strings.Contains(out, "vortex_request_duration_seconds_bucket") {
		t.Errorf("duration histogram not present:\n%s", out)
	}
	if !strings.Contains(out, `vortex_request_duration_seconds_count{route="api"} 1`) {
		t.Errorf("duration count not recorded:\n%s", out)
	}
}

func TestMetrics_SetActiveConns(t *testing.T) {
	m := NewMetrics("vortex")
	m.SetActiveConns("web", "http", 42)
	out := scrape(t, m)
	if !strings.Contains(out, `vortex_active_connections{protocol="http",route="web"} 42`) {
		t.Errorf("active_connections gauge not set to 42:\n%s", out)
	}
}

func TestMetrics_RecordBytes(t *testing.T) {
	m := NewMetrics("vortex")
	m.RecordBytes("web", 100, 250)
	out := scrape(t, m)
	if !strings.Contains(out, `vortex_bytes_in_total{route="web"} 100`) {
		t.Errorf("bytes_in_total not 100:\n%s", out)
	}
	if !strings.Contains(out, `vortex_bytes_out_total{route="web"} 250`) {
		t.Errorf("bytes_out_total not 250:\n%s", out)
	}
}

func TestMetrics_HandlerContentType(t *testing.T) {
	m := NewMetrics("vortex")
	rec := httptest.NewRecorder()
	m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/plain") {
		t.Errorf("Content-Type = %q, want text/plain (Prometheus exposition)", ct)
	}
}

func TestMetrics_OutputContainsRegisteredNames(t *testing.T) {
	m := NewMetrics("vortex")
	// Touch each collector so it appears in the output.
	m.RecordRequest("r", "GET", 200, time.Millisecond)
	m.SetActiveConns("r", "http", 1)
	m.RecordBytes("r", 1, 1)
	m.RecordRouteError("r", "timeout")
	m.SetClusterMembers(3)
	m.RecordPolicyEval("allow")
	m.RecordSecretOp("get")

	out := scrape(t, m)
	for _, name := range []string{
		"vortex_requests_total",
		"vortex_request_duration_seconds",
		"vortex_active_connections",
		"vortex_bytes_in_total",
		"vortex_bytes_out_total",
		"vortex_route_errors_total",
		"vortex_cluster_members",
		"vortex_policy_evaluations_total",
		"vortex_secret_operations_total",
	} {
		if !strings.Contains(out, name) {
			t.Errorf("metrics output missing %q", name)
		}
	}
}
