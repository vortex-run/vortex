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
	Home        string // service user's home and working directory
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
		Home:        ServiceHome,
	}
}

// systemdTemplate is a production-grade unit. ExecReload sends SIGHUP for
// VORTEX's config hot-reload; Restart=on-failure with RestartSec gives crash
// resilience; the LimitNOFILE/NPROC raise the file-descriptor and process
// ceilings a network proxy needs.
//
// The service runs as an unprivileged user, so it is granted exactly one
// capability, CAP_NET_BIND_SERVICE, to bind the edge's :80/:443. The
// hardening directives below only remove things VORTEX never needs (kernel
// tunables, modules, logs, cgroup writes, SUID/SGID, realtime, foreign
// syscall ABIs). Deliberately absent, because they would break the product:
// MemoryDenyWriteExecute (the WASM plugin runtime compiles to native code),
// ProtectSystem/ProtectHome (the agent operates on the host, with approval),
// RestrictNamespaces (rootless container builds), and ProtectClock/
// PrivateDevices (they restrict /dev, which the Studio terminal's ptys need).
// WorkingDirectory is the service home, so the agent's default workspace is
// /var/lib/vortex rather than /.
const systemdTemplate = `[Unit]
Description={{.Description}}
After={{.After}}
Wants={{.After}}

[Service]
Type=simple
User={{.User}}
Group={{.Group}}
WorkingDirectory={{.Home}}
ExecStart={{.ExecPath}} start --config {{.ConfigPath}}
ExecReload=/bin/kill -HUP $MAINPID
Restart=on-failure
RestartSec=5s
StandardOutput=journal
StandardError=journal
SyslogIdentifier=vortex
LimitNOFILE=65536
LimitNPROC=65536
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectKernelTunables=true
ProtectKernelModules=true
ProtectKernelLogs=true
ProtectControlGroups=true
ProtectHostname=true
RestrictSUIDSGID=true
RestrictRealtime=true
LockPersonality=true
SystemCallArchitectures=native

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
