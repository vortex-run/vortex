package observability

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/vortex-run/vortex/pkg/logger"
)

// routeHeader carries the route name into the request so the middleware can
// label metrics and spans by route.
const routeHeader = "X-Vortex-Route"

// NewMiddleware returns an HTTP middleware that records Prometheus metrics and
// an OpenTelemetry span for every request. metrics must be non-nil; tracer may
// be nil, in which case spans are taken from the global no-op tracer.
func NewMiddleware(metrics *Metrics, tracer trace.Tracer) func(http.Handler) http.Handler {
	if tracer == nil {
		tracer = noop.NewTracerProvider().Tracer("vortex")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route := routeOf(r)
			start := time.Now()

			// Continue any inbound trace, then start the request span.
			ctx := TraceContext(r.Context(), r)
			ctx, span := tracer.Start(ctx, "http.request",
				trace.WithAttributes(
					attribute.String("http.method", r.Method),
					attribute.String("http.route", route),
					attribute.String("http.target", r.URL.Path),
				),
			)
			defer span.End()

			// Attach the M1.5 correlation ID so logs and traces correlate.
			if cid := logger.CorrelationID(ctx); cid != "" {
				span.SetAttributes(attribute.String("vortex.correlation_id", cid))
			}

			sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(sw, r.WithContext(ctx))

			dur := time.Since(start)
			metrics.RecordRequest(route, r.Method, sw.status, dur)
			span.SetAttributes(attribute.Int("http.status_code", sw.status))
			if sw.status >= 500 {
				span.SetStatus(codes.Error, http.StatusText(sw.status))
				metrics.RecordRouteError(route, "5xx")
			} else {
				span.SetStatus(codes.Ok, "")
			}
		})
	}
}

// TraceContext extracts a W3C trace context (traceparent/tracestate) from the
// request headers and returns a context carrying it, so a span started next
// continues the inbound trace.
func TraceContext(ctx context.Context, req *http.Request) context.Context {
	prop := propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	)
	return prop.Extract(ctx, propagation.HeaderCarrier(req.Header))
}

// routeOf returns the route name for a request, from the X-Vortex-Route header
// when set, else "unknown".
func routeOf(r *http.Request) string {
	if name := r.Header.Get(routeHeader); name != "" {
		return name
	}
	return "unknown"
}

// statusRecorder captures the response status code for metrics/span labelling
// while forwarding hijack/flush so streaming and WebSocket upgrades still work.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (w *statusRecorder) WriteHeader(code int) {
	if !w.wroteHeader {
		w.status = code
		w.wroteHeader = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusRecorder) Write(b []byte) (int, error) {
	if !w.wroteHeader {
		w.wroteHeader = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("observability: ResponseWriter does not support Hijack")
	}
	return hj.Hijack()
}

func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
