package observability

import (
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds VORTEX's Prometheus collectors and their registry.
type Metrics struct {
	registry *prometheus.Registry

	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
	activeConns     *prometheus.GaugeVec
	bytesIn         *prometheus.CounterVec
	bytesOut        *prometheus.CounterVec
	routeErrors     *prometheus.CounterVec
	clusterMembers  prometheus.Gauge
	policyEvals     *prometheus.CounterVec
	secretOps       *prometheus.CounterVec

	// Agent/orchestration plane (production audit M8): the most failure-prone
	// subsystem was previously unobservable.
	orchTasksTotal   *prometheus.CounterVec // by agent_type + outcome (complete/failed)
	orchTaskDuration *prometheus.HistogramVec
	orchRunsTotal    prometheus.Counter
	orchTasksActive  prometheus.Gauge // in-flight orchestration tasks (queue depth)
}

// NewMetrics creates a Metrics with a private registry under the given namespace
// and registers all VORTEX collectors.
func NewMetrics(namespace string) *Metrics {
	if namespace == "" {
		namespace = "vortex"
	}
	reg := prometheus.NewRegistry()
	m := &Metrics{registry: reg}

	m.requestsTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "requests_total",
		Help: "Total HTTP requests handled, labelled by route, method, and status.",
	}, []string{"route", "method", "status"})

	m.requestDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "request_duration_seconds",
		Help:    "HTTP request duration in seconds, labelled by route.",
		Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
	}, []string{"route"})

	m.activeConns = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "active_connections",
		Help: "Currently active connections, labelled by route and protocol.",
	}, []string{"route", "protocol"})

	m.bytesIn = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "bytes_in_total",
		Help: "Total bytes received, labelled by route.",
	}, []string{"route"})

	m.bytesOut = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "bytes_out_total",
		Help: "Total bytes sent, labelled by route.",
	}, []string{"route"})

	m.routeErrors = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "route_errors_total",
		Help: "Total route errors, labelled by route and error type.",
	}, []string{"route", "error_type"})

	m.clusterMembers = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "cluster_members",
		Help: "Number of members currently in the cluster.",
	})

	m.policyEvals = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "policy_evaluations_total",
		Help: "Total policy evaluations, labelled by result (allow/deny/error).",
	}, []string{"result"})

	m.secretOps = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "secret_operations_total",
		Help: "Total secret operations, labelled by operation.",
	}, []string{"operation"})

	m.orchTasksTotal = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: namespace, Name: "orchestration_tasks_total",
		Help: "Total orchestration tasks that reached a terminal state, labelled by agent type and outcome (complete/failed).",
	}, []string{"agent_type", "outcome"})

	m.orchTaskDuration = prometheus.NewHistogramVec(prometheus.HistogramOpts{
		Namespace: namespace, Name: "orchestration_task_duration_seconds",
		Help:    "Orchestration task execution duration in seconds, labelled by agent type.",
		Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120, 300},
	}, []string{"agent_type"})

	m.orchRunsTotal = prometheus.NewCounter(prometheus.CounterOpts{
		Namespace: namespace, Name: "orchestration_runs_total",
		Help: "Total orchestration runs started.",
	})

	m.orchTasksActive = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: namespace, Name: "orchestration_tasks_active",
		Help: "Orchestration tasks currently executing (in-flight queue depth).",
	})

	reg.MustRegister(
		m.requestsTotal, m.requestDuration, m.activeConns,
		m.bytesIn, m.bytesOut, m.routeErrors,
		m.clusterMembers, m.policyEvals, m.secretOps,
		m.orchTasksTotal, m.orchTaskDuration, m.orchRunsTotal, m.orchTasksActive,
	)
	return m
}

// Handler returns an http.Handler serving the registry in Prometheus text
// format, suitable for the /metrics endpoint.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// RecordRequest records one completed request: it increments requests_total
// (labelled by route/method/status) and observes the duration histogram.
func (m *Metrics) RecordRequest(route, method string, status int, duration time.Duration) {
	m.requestsTotal.WithLabelValues(route, method, statusLabel(status)).Inc()
	m.requestDuration.WithLabelValues(route).Observe(duration.Seconds())
}

// SetActiveConns sets the active-connection gauge for a route/protocol.
func (m *Metrics) SetActiveConns(route, protocol string, n int64) {
	m.activeConns.WithLabelValues(route, protocol).Set(float64(n))
}

// RecordBytes adds to the in/out byte counters for a route.
func (m *Metrics) RecordBytes(route string, in, out int64) {
	if in > 0 {
		m.bytesIn.WithLabelValues(route).Add(float64(in))
	}
	if out > 0 {
		m.bytesOut.WithLabelValues(route).Add(float64(out))
	}
}

// RecordRouteError increments the route error counter.
func (m *Metrics) RecordRouteError(route, errorType string) {
	m.routeErrors.WithLabelValues(route, errorType).Inc()
}

// SetClusterMembers sets the cluster member-count gauge.
func (m *Metrics) SetClusterMembers(n int) {
	m.clusterMembers.Set(float64(n))
}

// RecordPolicyEval increments the policy-evaluation counter by result.
func (m *Metrics) RecordPolicyEval(result string) {
	m.policyEvals.WithLabelValues(result).Inc()
}

// RecordSecretOp increments the secret-operation counter by operation.
func (m *Metrics) RecordSecretOp(operation string) {
	m.secretOps.WithLabelValues(operation).Inc()
}

// --- Agent/orchestration plane (production audit M8) ------------------------

// RecordOrchestrationRun increments the run counter (one per Orchestrator.Run).
func (m *Metrics) RecordOrchestrationRun() {
	m.orchRunsTotal.Inc()
}

// TaskStarted marks one orchestration task as in-flight (raises the active
// gauge). Pair with TaskFinished.
func (m *Metrics) TaskStarted() {
	m.orchTasksActive.Inc()
}

// TaskFinished records a terminal task: it lowers the active gauge, increments
// orchestration_tasks_total by agent type and outcome ("complete"/"failed"),
// and observes the duration histogram. agentType is normalised to "unknown"
// when empty to keep the metric label populated.
func (m *Metrics) TaskFinished(agentType, outcome string, duration time.Duration) {
	if agentType == "" {
		agentType = "unknown"
	}
	m.orchTasksActive.Dec()
	m.orchTasksTotal.WithLabelValues(agentType, outcome).Inc()
	m.orchTaskDuration.WithLabelValues(agentType).Observe(duration.Seconds())
}

// statusLabel reduces an HTTP status code to a class label (2xx, 3xx, …) to keep
// label cardinality bounded.
func statusLabel(status int) string {
	switch {
	case status >= 500:
		return "5xx"
	case status >= 400:
		return "4xx"
	case status >= 300:
		return "3xx"
	case status >= 200:
		return "2xx"
	default:
		return "1xx"
	}
}
