package cmd

import "os"

// defaultPidfile is the pidfile name; legacyPidfile is the pre-rename one.
const (
	defaultPidfile = "gubern.pid"
	legacyPidfile  = "vortex.pid"
)

// resolvePidfile is the pidfile a client command (stop, status, reload,
// watchdog) should read: the given path, except that the default name falls
// back to a pre-rename vortex.pid when only that exists, so a server started
// by an older version can still be stopped.
func resolvePidfile(path string) string {
	if path != defaultPidfile {
		return path
	}
	if _, err := os.Stat(defaultPidfile); err != nil {
		if _, lerr := os.Stat(legacyPidfile); lerr == nil {
			return legacyPidfile
		}
	}
	return path
}
