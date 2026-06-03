package logger

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	cases := map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"DEBUG":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"":        slog.LevelInfo,
		"unknown": slog.LevelInfo,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		" error ": slog.LevelError,
	}
	for in, want := range cases {
		if got := ParseLevel(in); got != want {
			t.Errorf("ParseLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestCorrelationIDRoundTrip(t *testing.T) {
	ctx := context.Background()
	if got := CorrelationID(ctx); got != "" {
		t.Fatalf("empty context should have no correlation ID, got %q", got)
	}
	ctx = WithCorrelationID(ctx, "abc-123")
	if got := CorrelationID(ctx); got != "abc-123" {
		t.Fatalf("CorrelationID = %q, want abc-123", got)
	}
}

func TestJSONLoggerEmitsCorrelationID(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Format: FormatJSON, Output: &buf, Level: slog.LevelInfo})

	ctx := WithCorrelationID(context.Background(), "req-42")
	log.InfoContext(ctx, "hello", "key", "value")

	var rec map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &rec); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if rec[correlationField] != "req-42" {
		t.Errorf("correlation_id = %v, want req-42", rec[correlationField])
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", rec["msg"])
	}
	if rec["key"] != "value" {
		t.Errorf("key = %v, want value", rec["key"])
	}
}

func TestLoggerOmitsCorrelationIDWhenAbsent(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Format: FormatJSON, Output: &buf})
	log.Info("no correlation here")

	if strings.Contains(buf.String(), correlationField) {
		t.Errorf("expected no correlation_id field, got: %s", buf.String())
	}
}

func TestTextFormat(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Format: FormatText, Output: &buf})
	ctx := WithCorrelationID(context.Background(), "trace-9")
	log.InfoContext(ctx, "text mode")

	out := buf.String()
	if !strings.Contains(out, "text mode") {
		t.Errorf("text output missing message: %s", out)
	}
	if !strings.Contains(out, "trace-9") {
		t.Errorf("text output missing correlation id: %s", out)
	}
}

func TestLevelFiltering(t *testing.T) {
	var buf bytes.Buffer
	log := New(Config{Format: FormatJSON, Output: &buf, Level: slog.LevelWarn})
	log.Info("should be dropped")
	if buf.Len() != 0 {
		t.Errorf("info record should be filtered at warn level, got: %s", buf.String())
	}
	log.Warn("should appear")
	if buf.Len() == 0 {
		t.Error("warn record should not be filtered")
	}
}
