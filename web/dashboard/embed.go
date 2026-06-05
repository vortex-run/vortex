// Package dashboardui embeds the built VORTEX management dashboard (the Vite
// production build in dist/) so it ships inside the Go binary. The HTTP handler
// that serves it lives in internal/dashboard, which imports DistFS here.
//
// The embed path is relative to this file, so this Go file must sit alongside
// dist/ (go:embed cannot traverse "..").
package dashboardui

import "embed"

// DistFS is the embedded production build of the dashboard.
//
//go:embed all:dist
var DistFS embed.FS
