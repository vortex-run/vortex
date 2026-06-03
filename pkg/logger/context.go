package logger

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"
)

// contextKey is an unexported type for context keys defined in this package so
// values cannot collide with keys from other packages.
type contextKey string

// correlationIDKey is the context key under which the correlation ID is stored.
const correlationIDKey contextKey = "correlation_id"

// WithCorrelationID returns a copy of ctx carrying the given correlation ID.
// Records logged with that context (via Logger.InfoContext etc.) include the ID
// automatically.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationIDKey, id)
}

// CorrelationIDFromContext returns the correlation ID stored in ctx and whether
// one was present.
func CorrelationIDFromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(correlationIDKey).(string)
	if !ok || id == "" {
		return "", false
	}
	return id, true
}

// CorrelationID returns the correlation ID stored in ctx, or "" if none is set.
// It is the string-only form used by the log handlers.
func CorrelationID(ctx context.Context) string {
	id, _ := CorrelationIDFromContext(ctx)
	return id
}

// NewCorrelationID returns a fresh 32-character lowercase hex correlation ID
// (16 random bytes). If the system RNG fails — effectively never — it falls
// back to a timestamp-derived hex value so an ID is always produced.
func NewCorrelationID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		h := hex.EncodeToString([]byte(time.Now().UTC().Format("20060102150405.000000000")))
		for len(h) < 32 {
			h += "0"
		}
		return h[:32]
	}
	return hex.EncodeToString(b[:])
}

// EnsureCorrelationID returns ctx and the existing correlation ID if one is
// present; otherwise it generates a new ID, stores it in a derived context, and
// returns both.
func EnsureCorrelationID(ctx context.Context) (context.Context, string) {
	if id, ok := CorrelationIDFromContext(ctx); ok {
		return ctx, id
	}
	id := NewCorrelationID()
	return WithCorrelationID(ctx, id), id
}
