//go:build !linux

package logger

import "log/slog"

// IsJournald is always false on non-Linux platforms (no systemd journal).
func IsJournald() bool { return false }

// NewJournalHandler returns nil on non-Linux platforms; callers fall back to the
// text or JSON handler.
func NewJournalHandler(_ slog.Level) slog.Handler { return nil }
