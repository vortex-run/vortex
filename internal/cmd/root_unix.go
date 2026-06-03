//go:build !windows

package cmd

import "os"

// isRoot reports whether the current process is running as the superuser.
func isRoot() bool {
	return os.Getuid() == 0
}
