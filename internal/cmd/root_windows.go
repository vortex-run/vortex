//go:build windows

package cmd

// isRoot reports whether the process has administrative privileges. On Windows
// there is no Getuid; service installation there is handled via NSSM rather
// than a privileged self-install, so the root gate does not apply and we
// report true to avoid blocking the (informational) Windows install path.
func isRoot() bool {
	return true
}
