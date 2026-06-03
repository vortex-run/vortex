// Package service generates init-system integration (systemd units, OpenRC
// scripts) and installs VORTEX as a managed system service (build plan M1.4).
// All generation uses text/template from the standard library — no external
// dependencies (Non-Negotiable Rule #10).
package service

import (
	"strings"
	"text/template"
)

// SystemdConfig holds the values rendered into a systemd unit file.
type SystemdConfig struct {
	ExecPath    string // absolute path to the vortex binary
	ConfigPath  string // absolute path to vortex.cue
	User        string // system user the service runs as
	Group       string // system group the service runs as
	Description string // unit description
	After       string // ordering dependency (e.g. network-online.target)
}

// DefaultSystemdConfig returns a SystemdConfig with production defaults filled
// in, given the binary and config paths.
func DefaultSystemdConfig(execPath, configPath string) SystemdConfig {
	return SystemdConfig{
		ExecPath:    execPath,
		ConfigPath:  configPath,
		User:        "vortex",
		Group:       "vortex",
		Description: "VORTEX — autonomous infra platform",
		After:       "network-online.target",
	}
}

// systemdTemplate is a production-grade unit. ExecReload sends SIGHUP for
// VORTEX's config hot-reload; Restart=on-failure with RestartSec gives crash
// resilience; the LimitNOFILE/NPROC raise the file-descriptor and process
// ceilings a network proxy needs.
const systemdTemplate = `[Unit]
Description={{.Description}}
After={{.After}}
Wants={{.After}}

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
ExecStart={{.ExecPath}} start --config {{.ConfigPath}}
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=vortex
LimitNOFILE=65536
LimitNPROC=65536

[Install]
WantedBy=multi-user.target
`

// GenerateSystemd renders a complete systemd unit file for cfg.
func GenerateSystemd(cfg SystemdConfig) string {
	tmpl := template.Must(template.New("systemd").Parse(systemdTemplate))
	var b strings.Builder
	// Inputs are operator-supplied paths/identifiers, not untrusted data, and
	// the template has no failing actions, so Execute cannot error here.
	_ = tmpl.Execute(&b, cfg)
	return b.String()
}
