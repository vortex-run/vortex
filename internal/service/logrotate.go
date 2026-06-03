package service

import (
	"strings"
	"text/template"
)

// LogrotateConfig holds the values rendered into a logrotate(8) config, used on
// distributions where VORTEX logs to a file (OpenRC) rather than journald.
type LogrotateConfig struct {
	LogPath    string // path of the log file to rotate
	MaxSize    string // rotate when the file reaches this size (e.g. "100M")
	Rotate     int    // number of rotated files to keep
	Compress   bool   // gzip rotated files
	DateExt    bool   // append a date extension to rotated files
	PostRotate string // command run after rotation (e.g. reload to reopen logs)
}

// DefaultLogrotateConfig returns production defaults for the logrotate config.
func DefaultLogrotateConfig() LogrotateConfig {
	return LogrotateConfig{
		LogPath:    "/var/log/vortex/vortex.log",
		MaxSize:    "100M",
		Rotate:     7,
		Compress:   true,
		DateExt:    true,
		PostRotate: "rc-service vortex reload",
	}
}

const logrotateTemplate = `{{.LogPath}} {
    daily
    size {{.MaxSize}}
    rotate {{.Rotate}}
{{- if .Compress}}
    compress
    delaycompress
{{- end}}
{{- if .DateExt}}
    dateext
{{- end}}
    missingok
    notifempty
    create 0644 vortex vortex
    postrotate
        {{.PostRotate}}
    endscript
}
`

// GenerateLogrotate renders a complete /etc/logrotate.d/vortex file for cfg.
func GenerateLogrotate(cfg LogrotateConfig) string {
	tmpl := template.Must(template.New("logrotate").Parse(logrotateTemplate))
	var b strings.Builder
	// Inputs are operator-supplied; the template has no failing actions.
	_ = tmpl.Execute(&b, cfg)
	return b.String()
}
