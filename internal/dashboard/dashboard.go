// Package dashboard serves the embedded VORTEX management dashboard (build plan
// M7): a React single-page app, built by Vite and embedded into the binary, so
// the management UI ships with VORTEX and needs no separate server. It is mounted
// at /dashboard/ by the management API.
package dashboard

import (
	"io/fs"
	"net/http"
	"strings"

	dashboardui "github.com/vortex-run/vortex/web/dashboard"
)

// indexPath is the SPA entry document served for any non-asset route.
const indexPath = "index.html"

// Handler returns an http.Handler serving the embedded dashboard under
// /dashboard/. Requests for existing files are served directly with long cache
// lifetimes; any other path falls back to index.html (with no-cache) so client-
// side routing (React Router) works on deep links and refreshes.
func Handler() http.Handler {
	// The dist/ subtree is the web root; strip the "dist" prefix so URLs map to
	// dist/<path>.
	sub, err := fs.Sub(dashboardui.DistFS, "dist")
	if err != nil {
		// A build error here means the embed is broken; serve a clear 500.
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "dashboard assets unavailable", http.StatusInternalServerError)
		})
	}

	fileServer := http.FileServer(http.FS(sub))
	return http.StripPrefix("/dashboard/", spaHandler(sub, fileServer))
}

// spaHandler serves a file when it exists, otherwise falls back to index.html.
func spaHandler(root fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqPath := strings.TrimPrefix(r.URL.Path, "/")
		if reqPath == "" {
			serveIndex(w, r, root)
			return
		}
		if _, err := fs.Stat(root, reqPath); err != nil {
			// Not a real file: SPA route, serve index.html.
			serveIndex(w, r, root)
			return
		}
		// Real asset: cache aggressively (content-hashed filenames from Vite).
		w.Header().Set("Cache-Control", "public, max-age=3600")
		fileServer.ServeHTTP(w, r)
	})
}

// serveIndex writes index.html with a no-cache header so a redeploy is picked up
// immediately while hashed assets stay cached.
func serveIndex(w http.ResponseWriter, _ *http.Request, root fs.FS) {
	data, err := fs.ReadFile(root, indexPath)
	if err != nil {
		http.Error(w, "dashboard index unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}
