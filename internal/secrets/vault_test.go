package secrets

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// fakeVault is a configurable in-memory Vault KV v2 server for tests.
type fakeVault struct {
	srv         *httptest.Server
	getStatus   int    // status to return for GET data; 0 = 200 with value
	getValue    string // value for GET data
	listStatus  int    // status for LIST; 0 = 200
	listKeys    []string
	healthCode  int               // status for sys/health; 0 = 200
	lastSetBody map[string]any    // captured POST data body
	lastToken   string            // captured X-Vault-Token
	lastMethod  map[string]string // path-suffix → method seen
}

func newFakeVault(t *testing.T) *fakeVault {
	t.Helper()
	fv := &fakeVault{lastMethod: map[string]string{}}
	fv.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fv.lastToken = r.Header.Get("X-Vault-Token")
		switch {
		case strings.Contains(r.URL.Path, "/sys/health"):
			code := fv.healthCode
			if code == 0 {
				code = http.StatusOK
			}
			w.WriteHeader(code)
		case strings.Contains(r.URL.Path, "/metadata/") && r.Method == "LIST":
			if fv.listStatus != 0 {
				w.WriteHeader(fv.listStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"keys": fv.listKeys},
			})
		case strings.Contains(r.URL.Path, "/metadata/") && r.Method == http.MethodDelete:
			fv.lastMethod["delete"] = r.Method
			w.WriteHeader(http.StatusNoContent)
		case strings.Contains(r.URL.Path, "/data/") && r.Method == http.MethodGet:
			if fv.getStatus != 0 {
				w.WriteHeader(fv.getStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": map[string]any{"data": map[string]string{"value": fv.getValue}},
			})
		case strings.Contains(r.URL.Path, "/data/") && r.Method == http.MethodPost:
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &fv.lastSetBody)
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fv.srv.Close)
	return fv
}

func newVault(t *testing.T, fv *fakeVault) *VaultAdapter {
	t.Helper()
	a, err := NewVaultAdapter(VaultConfig{Address: fv.srv.URL, Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	return a.(*VaultAdapter)
}

func TestVault_GetValue(t *testing.T) {
	fv := newFakeVault(t)
	fv.getValue = "s3cret"
	v := newVault(t, fv)
	got, err := v.Get(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "s3cret" {
		t.Errorf("Get = %q, want s3cret", got)
	}
}

func TestVault_GetNotFound(t *testing.T) {
	fv := newFakeVault(t)
	fv.getStatus = http.StatusNotFound
	v := newVault(t, fv)
	if _, err := v.Get(context.Background(), "missing"); !os.IsNotExist(err) {
		t.Errorf("Get err = %v, want os.ErrNotExist", err)
	}
}

func TestVault_GetServerError(t *testing.T) {
	fv := newFakeVault(t)
	fv.getStatus = http.StatusInternalServerError
	v := newVault(t, fv)
	if _, err := v.Get(context.Background(), "x"); err == nil {
		t.Error("expected error on 500")
	}
}

func TestVault_SetSendsBodyAndToken(t *testing.T) {
	fv := newFakeVault(t)
	v := newVault(t, fv)
	if err := v.Set(context.Background(), "jwt", "value123"); err != nil {
		t.Fatal(err)
	}
	if fv.lastToken != "test-token" {
		t.Errorf("X-Vault-Token = %q, want test-token", fv.lastToken)
	}
	data, _ := fv.lastSetBody["data"].(map[string]any)
	if data["value"] != "value123" {
		t.Errorf("set body value = %v, want value123", data["value"])
	}
}

func TestVault_SetError(t *testing.T) {
	fv := newFakeVault(t)
	// Override the handler to return 403 on POST.
	fv.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	v := newVault(t, fv)
	if err := v.Set(context.Background(), "k", "v"); err == nil {
		t.Error("expected error on non-2xx set")
	}
}

func TestVault_List(t *testing.T) {
	fv := newFakeVault(t)
	fv.listKeys = []string{"a", "b", "c"}
	v := newVault(t, fv)
	keys, err := v.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 3 {
		t.Errorf("List = %v, want 3 keys", keys)
	}
}

func TestVault_ListEmptyOn404(t *testing.T) {
	fv := newFakeVault(t)
	fv.listStatus = http.StatusNotFound
	v := newVault(t, fv)
	keys, err := v.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 0 {
		t.Errorf("List on 404 = %v, want empty", keys)
	}
}

func TestVault_Delete(t *testing.T) {
	fv := newFakeVault(t)
	v := newVault(t, fv)
	if err := v.Delete(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if fv.lastMethod["delete"] != http.MethodDelete {
		t.Error("Delete should issue an HTTP DELETE")
	}
}

func TestVault_DeleteIdempotent(t *testing.T) {
	fv := newFakeVault(t)
	fv.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	v := newVault(t, fv)
	if err := v.Delete(context.Background(), "missing"); err != nil {
		t.Errorf("Delete on 404 should be nil, got %v", err)
	}
}

func TestVault_PingOK(t *testing.T) {
	fv := newFakeVault(t)
	v := newVault(t, fv)
	if err := v.Ping(context.Background()); err != nil {
		t.Errorf("Ping = %v, want nil", err)
	}
}

func TestVault_PingStandby429(t *testing.T) {
	fv := newFakeVault(t)
	fv.healthCode = http.StatusTooManyRequests
	v := newVault(t, fv)
	if err := v.Ping(context.Background()); err != nil {
		t.Errorf("Ping on 429 (standby) = %v, want nil", err)
	}
}

func TestVault_PingError(t *testing.T) {
	fv := newFakeVault(t)
	fv.healthCode = http.StatusInternalServerError
	v := newVault(t, fv)
	if err := v.Ping(context.Background()); err == nil {
		t.Error("Ping on 500 should error")
	}
}
