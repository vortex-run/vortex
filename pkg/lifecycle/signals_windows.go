//go:build windows

package lifecycle

import (
	"os"
	"os/signal"
)

// notifyShutdown wires os.Interrupt (Ctrl+C) to ch. Windows has no SIGTERM;
// production VORTEX targets Linux, but the binary must build and run on Windows
// for local development.
func notifyShutdown(ch chan<- os.Signal) {
	signal.Notify(ch, os.Interrupt)
}

// notifyReload is a no-op on Windows: there is no SIGHUP. Hot-reload can still
// be driven programmatically via Manager.Reload (e.g. from `vortex reload`).
func notifyReload(_ chan<- os.Signal) {}

// isReload always reports false on Windows since no reload signal is delivered.
func isReload(_ os.Signal) bool {
	return false
}
