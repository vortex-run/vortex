package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStatus_LastReload: /api/status carries the latest reload outcome, which
// `gubern reload` reads after a SIGHUP.
func TestStatus_LastReload(t *testing.T) {
	s, secret := newAuthedServer(t)
	get := func() *ReloadStatus {
		req := httptest.NewRequest(http.MethodGet, "/api/status", nil)
		req.Header.Set("X-API-Key", secret)
		rec := serve(s, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/status = %d", rec.Code)
		}
		var body StatusInfo
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.LastReload
	}

	if st := get(); st != nil {
		t.Errorf("last_reload = %+v before any reload, want absent", st)
	}
	s.RecordReload(errors.New(`route "x": bind: address already in use`))
	if st := get(); st == nil || st.OK || st.Error == "" || st.At.IsZero() {
		t.Errorf("after a failed reload, last_reload = %+v, want ok=false with the error and a time", st)
	}
	s.RecordReload(nil)
	if st := get(); st == nil || !st.OK || st.Error != "" {
		t.Errorf("after a good reload, last_reload = %+v, want ok=true", st)
	}
}
