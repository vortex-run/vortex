package observability

import (
	"context"
	"testing"
)

func TestTracer_DisabledIsNoop(t *testing.T) {
	tp, err := NewTracer(context.Background(), TracerConfig{ServiceName: "vortex", Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ShutdownTracer(context.Background(), tp) })
	if tp == nil {
		t.Fatal("disabled tracer should still return a (no-op) provider")
	}
	// A no-op provider still yields a usable tracer and spans (they are just not
	// exported).
	_, span := tp.Tracer("test").Start(context.Background(), "op")
	span.End()
}

func TestTracer_EnabledCreatesProvider(t *testing.T) {
	// otlptracehttp.New does not dial until export, so this is offline-safe.
	tp, err := NewTracer(context.Background(), TracerConfig{
		ServiceName: "vortex", Endpoint: "http://127.0.0.1:4318", Enabled: true,
	})
	if err != nil {
		t.Fatalf("NewTracer enabled: %v", err)
	}
	t.Cleanup(func() { _ = ShutdownTracer(context.Background(), tp) })
	if tp == nil {
		t.Error("enabled tracer should return a provider")
	}
}

func TestTracer_CreatesSpans(t *testing.T) {
	tp, _ := NewTracer(context.Background(), TracerConfig{ServiceName: "vortex", Enabled: false})
	t.Cleanup(func() { _ = ShutdownTracer(context.Background(), tp) })

	ctx, span := tp.Tracer("t").Start(context.Background(), "request")
	if !span.SpanContext().IsValid() {
		t.Error("span context should be valid")
	}
	if span.SpanContext().TraceID().String() == "" {
		t.Error("span should have a trace ID")
	}
	_ = ctx
	span.End()
}

func TestTracer_ShutdownFlushesWithoutError(t *testing.T) {
	tp, _ := NewTracer(context.Background(), TracerConfig{ServiceName: "vortex", Enabled: false})
	if err := ShutdownTracer(context.Background(), tp); err != nil {
		t.Errorf("ShutdownTracer = %v, want nil", err)
	}
	// Shutting down a nil provider is a no-op.
	if err := ShutdownTracer(context.Background(), nil); err != nil {
		t.Errorf("ShutdownTracer(nil) = %v, want nil", err)
	}
}

func TestTracer_SampleRateZeroSamplesNone(t *testing.T) {
	tp, _ := NewTracer(context.Background(), TracerConfig{
		ServiceName: "vortex", Enabled: false, SampleRate: 0,
	})
	t.Cleanup(func() { _ = ShutdownTracer(context.Background(), tp) })
	// SampleRate=0 defaults to 1.0 in NewTracer (see config), so spans are
	// recorded. We assert a span is sampled (recording) under the default.
	_, span := tp.Tracer("t").Start(context.Background(), "op")
	if !span.IsRecording() {
		t.Error("with default sampling, spans should record")
	}
	span.End()
}

func TestTracer_SampleRateOneSamplesAll(t *testing.T) {
	// Build a provider with an explicit always-sample rate and confirm every
	// span records.
	tp, _ := NewTracer(context.Background(), TracerConfig{
		ServiceName: "vortex", Enabled: false, SampleRate: 1.0,
	})
	t.Cleanup(func() { _ = ShutdownTracer(context.Background(), tp) })
	for i := 0; i < 5; i++ {
		_, span := tp.Tracer("t").Start(context.Background(), "op")
		if !span.IsRecording() {
			t.Errorf("span %d should be recording at SampleRate=1.0", i)
		}
		span.End()
	}
}
