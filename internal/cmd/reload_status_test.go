package cmd

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestLastReloadStatus: the reload command reads the outcome the server
// recorded (the only way to learn it after a SIGHUP) and ignores responses
// it can't use.
func TestLastReloadStatus(t *testing.T) {
	body := ""
	code := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	port := srv.Listener.Addr().(*net.TCPAddr).Port

	body = `{"cluster_name":"x","last_reload":{"at":"2026-11-03T13:42:57Z","ok":false,"error":"config error at vortex.cue:6:13"}}`
	st, ok := lastReloadStatus(port)
	if !ok || st.OK || st.Error != "config error at vortex.cue:6:13" || st.At.IsZero() {
		t.Errorf("failed reload: got %+v ok=%v", st, ok)
	}

	body = `{"cluster_name":"x"}` // no reload yet, or an older server
	if _, ok := lastReloadStatus(port); ok {
		t.Error("no last_reload: ok = true, want false")
	}

	code, body = http.StatusUnauthorized, `{"error":"authentication required"}` // VORTEX_TRUST_LOOPBACK=false
	if _, ok := lastReloadStatus(port); ok {
		t.Error("401: ok = true, want false")
	}
}
