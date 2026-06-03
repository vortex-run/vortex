//go:build linux

package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// IsJournald reports whether the process is running under systemd with its
// stdout/stderr connected to the journal. systemd sets JOURNAL_STREAM (and,
// since v232, INVOCATION_ID) for services it launches; the presence of the
// private control socket is a further signal that systemd is PID 1.
func IsJournald() bool {
	if os.Getenv("JOURNAL_STREAM") != "" {
		return true
	}
	if os.Getenv("INVOCATION_ID") != "" {
		return true
	}
	if _, err := os.Stat("/run/systemd/private"); err == nil {
		return true
	}
	return false
}

// journalPriority maps an slog level to the syslog priority that journald
// understands as a "<N>" line prefix.
func journalPriority(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return 3 // err
	case level >= slog.LevelWarn:
		return 4 // warning
	case level >= slog.LevelInfo:
		return 6 // info
	default:
		return 7 // debug
	}
}

// journalHandler is an slog.Handler that emits one line per record in the form
//
//	<PRIORITY>msg key=value key=value ...
//
// to stderr, which journald captures for a systemd service. The correlation_id
// attribute, when present, is rendered first after the message so it is easy to
// scan in `journalctl` output.
type journalHandler struct {
	level slog.Level
	attrs []slog.Attr
	group string
}

// NewJournalHandler returns a journal-native slog.Handler at the given minimum
// level, writing to os.Stderr.
func NewJournalHandler(level slog.Level) slog.Handler {
	return &journalHandler{level: level}
}

func (h *journalHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.level
}

func (h *journalHandler) Handle(ctx context.Context, r slog.Record) error {
	var b strings.Builder
	fmt.Fprintf(&b, "<%d>%s", journalPriority(r.Level), r.Message)

	// correlation_id first: prefer the context value, then any matching attr.
	if id := CorrelationID(ctx); id != "" {
		writeKV(&b, correlationField, id)
	}

	// Pre-bound attrs (from WithAttrs), then per-record attrs.
	for _, a := range h.attrs {
		h.appendAttr(&b, a)
	}
	r.Attrs(func(a slog.Attr) bool {
		// Avoid duplicating correlation_id if it was already emitted from ctx.
		if a.Key == correlationField && CorrelationID(ctx) != "" {
			return true
		}
		h.appendAttr(&b, a)
		return true
	})

	b.WriteByte('\n')
	_, err := os.Stderr.WriteString(b.String())
	return err
}

// appendAttr writes a single attribute, honouring any active group prefix.
func (h *journalHandler) appendAttr(b *strings.Builder, a slog.Attr) {
	key := a.Key
	if h.group != "" {
		key = h.group + "." + key
	}
	writeKV(b, key, a.Value.String())
}

// writeKV appends " key=value", quoting the value if it contains whitespace.
func writeKV(b *strings.Builder, key, val string) {
	b.WriteByte(' ')
	b.WriteString(key)
	b.WriteByte('=')
	if strings.ContainsAny(val, " \t\"") {
		b.WriteString(strconv.Quote(val))
	} else {
		b.WriteString(val)
	}
}

func (h *journalHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	merged := make([]slog.Attr, 0, len(h.attrs)+len(attrs))
	merged = append(merged, h.attrs...)
	merged = append(merged, attrs...)
	return &journalHandler{level: h.level, attrs: merged, group: h.group}
}

func (h *journalHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	g := name
	if h.group != "" {
		g = h.group + "." + name
	}
	return &journalHandler{level: h.level, attrs: h.attrs, group: g}
}
