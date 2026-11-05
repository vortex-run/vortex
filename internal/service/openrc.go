package service

import (
	"strings"
	"text/template"
)

// OpenRCConfig holds the values rendered into an OpenRC init script (for
// Alpine, Gentoo, and other OpenRC-based distributions).
type OpenRCConfig struct {
	ExecPath   string // absolute path to the vortex binary
	ConfigPath string // absolute path to gubern.cue
	User       string // user the service runs as
	PidFile    string // pidfile path
	LogFile    string // combined stdout/stderr log path
	Home       string // working directory (the service user's home)
}

// DefaultOpenRCConfig returns an OpenRCConfig with production defaults.
func DefaultOpenRCConfig(execPath, configPath string) OpenRCConfig {
	return OpenRCConfig{
		ExecPath:   execPath,
		ConfigPath: configPath,
		User:       ServiceUser,
		PidFile:    "/run/gubern/gubern.pid",
		LogFile:    "/var/log/vortex/vortex.log",
		Home:       ServiceHome,
	}
}

// openrcTemplate is a supervised OpenRC service: start-stop-daemon backgrounds
// the process, captures logs, and the depend() block orders it after the
// network.
const openrcTemplate = `#!/sbin/openrc-run

description="Gubern — autonomous infra platform"

command={{.ExecPath}}
command_args="start --config {{.ConfigPath}}"
command_user={{.User}}
command_background=true
directory={{.Home}}
# Gubern keeps its keys under $HOME/.config and $HOME/.cache, and refuses to
# start without a HOME; don't depend on start-stop-daemon setting it.
export HOME={{.Home}}
# Bind :80/:443 as the unprivileged user (OpenRC 0.45+; older ignore it).
capabilities="^cap_net_bind_service"
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
