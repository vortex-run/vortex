// Package logger provides VORTEX's structured logging built on the Go standard
// library's log/slog (Non-Negotiable Rule #10: standard library first).
//
// It supports two output formats — machine-readable JSON for production and a
// human-friendly text format for local development — and a correlation ID that
// flows through a request or agent task via context.Context so every log line
// emitted while handling one unit of work can be tied together.
package logger

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// Format selects the encoding used for log records.
type Format int

const (
	// FormatJSON emits one JSON object per line. Use in production.
	FormatJSON Format = iota
	// FormatText emits human-readable key=value lines. Use for local dev.
	FormatText
)

// Sink selects where log records are written.
type Sink string

const (
	// SinkAuto writes to the systemd journal when running as a service,
	// otherwise to stdout.
	SinkAuto Sink = "auto"
	// SinkStdout writes to standard output.
	SinkStdout Sink = "stdout"
	// SinkStderr writes to standard error.
	SinkStderr Sink = "stderr"
	// SinkFile writes to the file named by Config.Path.
	SinkFile Sink = "file"
)

// correlationKeyType is an unexported context key type so no other package can
// collide with our context value.
type correlationKeyType struct{}

var correlationKey correlationKeyType

// correlationField is the structured-log attribute key under which the
// correlation ID is recorded.
const correlationField = "correlation_id"

// Config configures a Logger.
type Config struct {
	// Level is the minimum level that will be emitted. Defaults to Info.
	Level slog.Level
	// Format selects JSON or text output. Defaults to FormatJSON.
	Format Format
	// Output is where records are written. When nil, the Sink selects the
	// destination. An explicit Output overrides Sink (used by tests).
	Output io.Writer
	// AddSource includes source file:line in each record when true.
	AddSource bool
	// Sink selects the output destination when Output is nil. Defaults to
	// SinkAuto, which uses the systemd journal under a service or stdout
	// otherwise.
	Sink Sink
	// Path is the log file path when Sink is SinkFile.
	Path string
}

// New builds a *slog.Logger from cfg. It installs a handler that automatically
// promotes a correlation ID stored in the record's context to a top-level
// attribute, so callers never have to thread it through manually.
//
// When Output is nil and Sink is SinkAuto on a systemd host, a journal-native
// handler is used (priority-prefixed lines on stderr for journalctl). Otherwise
// the configured Format (text/JSON) is written to the resolved sink.
func New(cfg Config) *slog.Logger {
	// Journal path: only when no explicit Output is given, Sink is auto, and we
	// detect journald. The journal handler emits correlation_id itself.
	if cfg.Output == nil && (cfg.Sink == "" || cfg.Sink == SinkAuto) && IsJournald() {
		if jh := NewJournalHandler(cfg.Level); jh != nil {
			return slog.New(jh)
		}
	}

	out := resolveSink(cfg)

	opts := &slog.HandlerOptions{
		Level:     cfg.Level,
		AddSource: cfg.AddSource,
	}

	var base slog.Handler
	switch cfg.Format {
	case FormatText:
		base = slog.NewTextHandler(out, opts)
	default:
		base = slog.NewJSONHandler(out, opts)
	}

	return slog.New(&correlationHandler{inner: base})
}

// resolveSink returns the io.Writer for cfg: an explicit Output wins, otherwise
// the Sink selects stdout/stderr/file. A file sink that cannot be opened logs
// the failure to stderr and falls back to stderr so logging never aborts boot.
func resolveSink(cfg Config) io.Writer {
	if cfg.Output != nil {
		return cfg.Output
	}
	switch cfg.Sink {
	case SinkStderr:
		return os.Stderr
	case SinkFile:
		f, err := os.OpenFile(cfg.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "logger: cannot open log file %q: %v; falling back to stderr\n", cfg.Path, err)
			return os.Stderr
		}
		return f
	case SinkStdout, SinkAuto, "":
		return os.Stdout
	default:
		return os.Stdout
	}
}

// ParseLevel converts a config string ("debug", "info", "warn", "error") into a
// slog.Level. Unknown values fall back to Info. The mapping mirrors the
// observability.log_level field in vortex.cue.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// WithCorrelationID returns a copy of ctx carrying the given correlation ID.
// Any record logged with that context (via Logger.InfoContext etc.) will
// include the ID automatically.
func WithCorrelationID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, correlationKey, id)
}

// CorrelationID extracts the correlation ID from ctx, or "" if none is set.
func CorrelationID(ctx context.Context) string {
	if id, ok := ctx.Value(correlationKey).(string); ok {
		return id
	}
	return ""
}

// correlationHandler wraps another slog.Handler and injects the context's
// correlation ID (if present) as a top-level attribute on every record.
type correlationHandler struct {
	inner slog.Handler
}

func (h *correlationHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

func (h *correlationHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := CorrelationID(ctx); id != "" {
		r.AddAttrs(slog.String(correlationField, id))
	}
	return h.inner.Handle(ctx, r)
}

func (h *correlationHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &correlationHandler{inner: h.inner.WithAttrs(attrs)}
}

func (h *correlationHandler) WithGroup(name string) slog.Handler {
	return &correlationHandler{inner: h.inner.WithGroup(name)}
}
