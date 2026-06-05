package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/vortex-run/vortex/pkg/logger"
)

// tracingSetup returns a metrics object, a tracer, and the in-memory span
// exporter capturing ended spans.
func tracingSetup(t *testing.T) (*Metrics, *tracetest.InMemoryExporter, func(http.Handler) http.Handler) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	m := NewMetrics("vortex")
	mw := NewMiddleware(m, tp.Tracer("test"))
	return m, exp, mw
}

// doReq runs a request through mw to a handler returning the given status.
func doReq(mw func(http.Handler) http.Handler, route string, status int, header map[string]string) *httptest.ResponseRecorder {
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	req := httptest.NewRequest(http.MethodGet, "/path", nil)
	if route != "" {
		req.Header.Set(routeHeader, route)
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestObsMiddleware_RecordsMetrics(t *testing.T) {
	m, _, mw := tracingSetup(t)
	doReq(mw, "web", http.StatusOK, nil)

	out := scrape(t, m)
	if !strings.Contains(out, `vortex_requests_total{method="GET",route="web",status="2xx"} 1`) {
		t.Errorf("middleware did not record request metric:\n%s", out)
	}
}

func TestObsMiddleware_CreatesSpan(t *testing.T) {
	_, exp, mw := tracingSetup(t)
	doReq(mw, "web", http.StatusOK, nil)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Name != "http.request" {
		t.Errorf("span name = %q, want http.request", spans[0].Name)
	}
}

func TestObsMiddleware_SpanHasRouteAttr(t *testing.T) {
	_, exp, mw := tracingSetup(t)
	doReq(mw, "admin", http.StatusOK, nil)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	found := false
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "http.route" && attr.Value.AsString() == "admin" {
			found = true
		}
	}
	if !found {
		t.Errorf("span missing http.route=admin attribute: %+v", spans[0].Attributes)
	}
}

func TestObsMiddleware_5xxSetsSpanError(t *testing.T) {
	m, exp, mw := tracingSetup(t)
	doReq(mw, "web", http.StatusInternalServerError, nil)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if spans[0].Status.Code.String() != "Error" {
		t.Errorf("5xx span status = %s, want Error", spans[0].Status.Code)
	}
	out := scrape(t, m)
	if !strings.Contains(out, `vortex_route_errors_total{error_type="5xx",route="web"} 1`) {
		t.Errorf("5xx should increment route_errors_total:\n%s", out)
	}
}

func TestObsMiddleware_TraceContextExtracted(t *testing.T) {
	_, exp, mw := tracingSetup(t)
	// A valid W3C traceparent — the request span must become a child of it.
	tp := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	doReq(mw, "web", http.StatusOK, map[string]string{"traceparent": tp})

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	if got := spans[0].SpanContext.TraceID().String(); got != "0af7651916cd43dd8448eb211c80319c" {
		t.Errorf("span trace ID = %s, want it to continue the inbound traceparent", got)
	}
}

func TestObsMiddleware_CorrelationIDInSpan(t *testing.T) {
	_, exp, mw := tracingSetup(t)
	// Inject a correlation ID into the request context (as M1.5's API middleware
	// would), then run the request.
	h := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/p", nil)
	req.Header.Set(routeHeader, "web")
	req = req.WithContext(logger.WithCorrelationID(req.Context(), "corr-123"))
	h.ServeHTTP(httptest.NewRecorder(), req)

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	found := false
	for _, attr := range spans[0].Attributes {
		if string(attr.Key) == "vortex.correlation_id" && attr.Value.AsString() == "corr-123" {
			found = true
		}
	}
	if !found {
		t.Errorf("span missing vortex.correlation_id attribute: %+v", spans[0].Attributes)
	}
}
