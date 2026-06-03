package service

import (
	"strings"
	"text/template"
)

// OpenRCConfig holds the values rendered into an OpenRC init script (for
// Alpine, Gentoo, and other OpenRC-based distributions).
type OpenRCConfig struct {
	ExecPath   string // absolute path to the vortex binary
	ConfigPath string // absolute path to vortex.cue
	User       string // user the service runs as
	PidFile    string // pidfile path
	LogFile    string // combined stdout/stderr log path
}

// DefaultOpenRCConfig returns an OpenRCConfig with production defaults.
func DefaultOpenRCConfig(execPath, configPath string) OpenRCConfig {
	return OpenRCConfig{
		ExecPath:   execPath,
		ConfigPath: configPath,
		User:       "vortex",
		PidFile:    "/run/vortex/vortex.pid",
		LogFile:    "/var/log/vortex/vortex.log",
	}
}

// openrcTemplate is a supervised OpenRC service: start-stop-daemon backgrounds
// the process, captures logs, and the depend() block orders it after the
// network.
const openrcTemplate = `#!/sbin/openrc-run

description="VORTEX — autonomous infra platform"

command={{.ExecPath}}
command_args="start --config {{.ConfigPath}}"
command_user={{.User}}
command_background=true
pidfile={{.PidFile}}
output_log={{.LogFile}}
error_log={{.LogFile}}

depend() {
	need net
	after firewall
}

start_pre() {
	checkpath --directory --owner {{.User}} --mode 0755 "$(dirname {{.PidFile}})"
	checkpath --directory --owner {{.User}} --mode 0755 "$(dirname {{.LogFile}})"
}
`

// GenerateOpenRC renders a complete OpenRC init script for cfg.
func GenerateOpenRC(cfg OpenRCConfig) string {
	tmpl := template.Must(template.New("openrc").Parse(openrcTemplate))
	var b strings.Builder
	// Inputs are operator-supplied; the template has no failing actions.
	_ = tmpl.Execute(&b, cfg)
	return b.String()
}
