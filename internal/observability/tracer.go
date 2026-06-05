// Package observability implements VORTEX's observability stack (build plan M5):
// OpenTelemetry distributed tracing, a Prometheus metrics registry, an HTTP
// middleware that records both, a localhost pprof profiler, and SLO/error-budget
// tracking. Tracing and profiling are opt-in; metrics are always available.
package observability

import (
	"context"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// serviceVersion is reported as the OTEL service.version resource attribute. It
// is overridden from build info by the caller where available.
var serviceVersion = "dev"

// TracerConfig configures the OpenTelemetry trace provider.
type TracerConfig struct {
	ServiceName string
	Endpoint    string  // OTLP HTTP endpoint (host:port or URL); required when Enabled
	Enabled     bool    // when false, NewTracer returns a no-op provider
	SampleRate  float64 // 0.0–1.0; defaults to 1.0 when <= 0
}

// NewTracer builds a TracerProvider for cfg and registers it as the global
// provider. When cfg.Enabled is false it returns a no-op provider (created with
// no span processors), so callers can always obtain a tracer and create spans
// without branching on whether tracing is on.
func NewTracer(ctx context.Context, cfg TracerConfig) (*sdktrace.TracerProvider, error) {
	if cfg.ServiceName == "" {
		cfg.ServiceName = "vortex"
	}
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 1.0
	}

	res, err := resource.New(ctx, resource.WithAttributes(
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(serviceVersion),
	))
	if err != nil {
		return nil, fmt.Errorf("observability: building trace resource: %w", err)
	}

	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(sampleRate)),
	}

	if cfg.Enabled {
		exporter, eerr := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(cfg.Endpoint),
		)
		if eerr != nil {
			return nil, fmt.Errorf("observability: creating OTLP exporter: %w", eerr)
		}
		opts = append(opts, sdktrace.WithBatcher(exporter))
	}

	tp := sdktrace.NewTracerProvider(opts...)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{},
	))
	return tp, nil
}

// ShutdownTracer flushes pending spans and shuts the provider down.
func ShutdownTracer(ctx context.Context, tp *sdktrace.TracerProvider) error {
	if tp == nil {
		return nil
	}
	if err := tp.Shutdown(ctx); err != nil {
		return fmt.Errorf("observability: shutting down tracer: %w", err)
	}
	return nil
}
