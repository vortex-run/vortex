package studio

import (
	"io"
	"log/slog"
)

// discardLogger returns a logger that drops all output, for use in tests.
func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }
