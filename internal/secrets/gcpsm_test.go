package secrets

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

// writeFakeSA generates an RSA key and writes a minimal service-account JSON
// pointing its token_uri at tokURL. Returns the file path.
func writeFakeSA(t *testing.T, tokURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der := x509.MarshalPKCS1PrivateKey(key)
	pemKey := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: der})
	sa := map[string]string{
		"type":         "service_account",
		"client_email": "vortex@test.iam.gserviceaccount.com",
		"private_key":  string(pemKey),
		"token_uri":    tokURL,
	}
	b, _ := json.Marshal(sa)
	path := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// fakeGCP serves both the OAuth2 token endpoint and the Secret Manager REST API.
type fakeGCP struct {
	srv        *httptest.Server
	getPayload string // raw secret value; base64-encoded into the access response
	getStatus  int    // override for :access; 0 = 200
	listBody   string // JSON for ListSecrets
	delStatus  int    // status for DELETE; 0 = 200
	tokenHits  int32  // number of times the token endpoint was hit
}

func newFakeGCP(t *testing.T) *fakeGCP {
	t.Helper()
	fg := &fakeGCP{}
	fg.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/token"):
			atomic.AddInt32(&fg.tokenHits, 1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "ya29.faketoken", "expires_in": 3600, "token_type": "Bearer",
			})
		case strings.HasSuffix(r.URL.Path, ":access"):
			if fg.getStatus != 0 {
				w.WriteHeader(fg.getStatus)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"payload": map[string]string{
					"data": base64.StdEncoding.EncodeToString([]byte(fg.getPayload)),
				},
			})
		case strings.Contains(r.URL.RawQuery, "secretId="): // create secret
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, ":addVersion"):
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		case r.Method == http.MethodDelete:
			if fg.delStatus != 0 {
				w.WriteHeader(fg.delStatus)
				return
			}
			_, _ = w.Write([]byte(`{}`))
		case strings.HasSuffix(r.URL.Path, "/secrets"): // list
			_, _ = w.Write([]byte(fg.listBody))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(fg.srv.Close)
	return fg
}

// newGCP builds a GCP adapter wired to the fake server for both API and token.
func newGCP(t *testing.T, fg *fakeGCP) *GCPSMAdapter {
	t.Helper()
	saPath := writeFakeSA(t, fg.srv.URL+"/token")
	a, err := NewGCPSMAdapter(GCPSMConfig{ProjectID: "test-proj", CredFile: saPath})
	if err != nil {
		t.Fatal(err)
	}
	adp := a.(*GCPSMAdapter)
	adp.apiBase = fg.srv.URL
	adp.tokURL = fg.srv.URL + "/token"
	return adp
}

func TestGCP_RequiresProjectID(t *testing.T) {
	if _, err := NewGCPSMAdapter(GCPSMConfig{}); err == nil {
		t.Error("expected error when ProjectID is empty")
	}
}

func TestGCP_Get(t *testing.T) {
	fg := newFakeGCP(t)
	fg.getPayload = "hunter2"
	a := newGCP(t, fg)
	got, err := a.Get(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "hunter2" {
		t.Errorf("Get = %q, want hunter2 (base64-decoded)", got)
	}
}

func TestGCP_GetNotFound(t *testing.T) {
	fg := newFakeGCP(t)
	fg.getStatus = http.StatusNotFound
	a := newGCP(t, fg)
	if _, err := a.Get(context.Background(), "missing"); !os.IsNotExist(err) {
		t.Errorf("Get err = %v, want os.ErrNotExist", err)
	}
}

func TestGCP_Set(t *testing.T) {
	fg := newFakeGCP(t)
	a := newGCP(t, fg)
	if err := a.Set(context.Background(), "api", "v"); err != nil {
		t.Fatalf("Set = %v", err)
	}
}

func TestGCP_List(t *testing.T) {
	fg := newFakeGCP(t)
	fg.listBody = `{"secrets":[` +
		`{"name":"projects/test-proj/secrets/vortex-a"},` +
		`{"name":"projects/test-proj/secrets/vortex-b"},` +
		`{"name":"projects/test-proj/secrets/other-c"}]}`
	a := newGCP(t, fg)
	keys, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("List = %v, want [a b] (prefix-filtered & stripped)", keys)
	}
}

func TestGCP_Delete(t *testing.T) {
	fg := newFakeGCP(t)
	a := newGCP(t, fg)
	if err := a.Delete(context.Background(), "k"); err != nil {
		t.Fatalf("Delete = %v", err)
	}
}

func TestGCP_DeleteIdempotent(t *testing.T) {
	fg := newFakeGCP(t)
	fg.delStatus = http.StatusNotFound
	a := newGCP(t, fg)
	if err := a.Delete(context.Background(), "missing"); err != nil {
		t.Errorf("Delete on 404 should be nil, got %v", err)
	}
}

func TestGCP_Ping(t *testing.T) {
	fg := newFakeGCP(t)
	fg.listBody = `{"secrets":[]}`
	a := newGCP(t, fg)
	if err := a.Ping(context.Background()); err != nil {
		t.Errorf("Ping = %v, want nil", err)
	}
}

func TestGCP_TokenCached(t *testing.T) {
	fg := newFakeGCP(t)
	fg.getPayload = "x"
	a := newGCP(t, fg)
	for i := 0; i < 3; i++ {
		if _, err := a.Get(context.Background(), "k"); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt32(&fg.tokenHits); got != 1 {
		t.Errorf("token endpoint hit %d times, want 1 (cached)", got)
	}
}
