package dashboard

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	dashboardui "github.com/vortex-run/vortex/web/dashboard"
)

func get(t *testing.T, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// TestHandler_ServesEveryBuiltAsset: the index and every chunk the build
// produced are served as real files (pages load as separate chunks).
func TestHandler_ServesEveryBuiltAsset(t *testing.T) {
	sub, err := fs.Sub(dashboardui.DistFS, "dist")
	if err != nil {
		t.Fatal(err)
	}
	assets, err := fs.ReadDir(sub, "assets")
	if err != nil || len(assets) == 0 {
		t.Fatalf("no built assets: %v", err)
	}
	for _, a := range assets {
		rec := get(t, "/dashboard/assets/"+a.Name())
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "<div id=\"root\">") {
			t.Errorf("%s: code %d", a.Name(), rec.Code)
		}
	}
	if rec := get(t, "/dashboard/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=\"root\"") {
		t.Errorf("index: %d", rec.Code)
	}
}

func TestHandler_DeepLinkGetsIndex(t *testing.T) {
	rec := get(t, "/dashboard/agents")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "id=\"root\"") {
		t.Errorf("deep link: %d", rec.Code)
	}
}

// TestHandler_MissingAssetIs404: a chunk name from an older build must not be
// answered with index.html (the browser would try to run HTML as a script).
func TestHandler_MissingAssetIs404(t *testing.T) {
	if rec := get(t, "/dashboard/assets/Overview-OLDHASH.js"); rec.Code != http.StatusNotFound {
		t.Errorf("missing asset: %d, want 404", rec.Code)
	}
}
