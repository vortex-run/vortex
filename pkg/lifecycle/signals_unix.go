//go:build !windows

package lifecycle

import (
	"os"
	"os/signal"
	"syscall"
)

// notifyShutdown wires SIGTERM and SIGINT (Ctrl+C) to ch.
func notifyShutdown(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
}

// notifyReload wires SIGHUP to ch for configuration hot-reload.
func notifyReload(ch chan<- os.Signal) {
	signal.Notify(ch, syscall.SIGHUP)
}

// isReload reports whether sig is the hot-reload signal.
func isReload(sig os.Signal) bool {
	return sig == syscall.SIGHUP
}
