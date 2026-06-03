package logger

import (
	"context"
	"regexp"
	"testing"
)

var hex32 = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestWithCorrelationIDStoresAndRetrieves(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "abc-123")
	got, ok := CorrelationIDFromContext(ctx)
	if !ok {
		t.Fatal("expected correlation ID to be present")
	}
	if got != "abc-123" {
		t.Errorf("got %q, want abc-123", got)
	}
}

func TestCorrelationIDFromContextFalseWhenUnset(t *testing.T) {
	if _, ok := CorrelationIDFromContext(context.Background()); ok {
		t.Error("expected ok=false on empty context")
	}
}

func TestNewCorrelationIDIs32CharLowerHex(t *testing.T) {
	id := NewCorrelationID()
	if !hex32.MatchString(id) {
		t.Errorf("ID %q is not 32-char lowercase hex", id)
	}
}

func TestNewCorrelationIDUnique(t *testing.T) {
	seen := make(map[string]struct{})
	for i := 0; i < 1000; i++ {
		id := NewCorrelationID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		seen[id] = struct{}{}
	}
}

func TestEnsureCorrelationIDGeneratesWhenMissing(t *testing.T) {
	ctx, id := EnsureCorrelationID(context.Background())
	if id == "" {
		t.Fatal("EnsureCorrelationID should generate an ID")
	}
	got, ok := CorrelationIDFromContext(ctx)
	if !ok || got != id {
		t.Errorf("returned ctx should carry the generated ID; got %q ok=%v", got, ok)
	}
}

func TestEnsureCorrelationIDPreservesExisting(t *testing.T) {
	ctx := WithCorrelationID(context.Background(), "keep-me")
	gotCtx, id := EnsureCorrelationID(ctx)
	if id != "keep-me" {
		t.Errorf("existing ID should be preserved, got %q", id)
	}
	if got, _ := CorrelationIDFromContext(gotCtx); got != "keep-me" {
		t.Errorf("context should still carry keep-me, got %q", got)
	}
}

func TestCorrelationRoundTrip(t *testing.T) {
	id := NewCorrelationID()
	ctx := WithCorrelationID(context.Background(), id)
	got, ok := CorrelationIDFromContext(ctx)
	if !ok || got != id {
		t.Errorf("round-trip failed: got %q ok=%v, want %q", got, ok, id)
	}
}
