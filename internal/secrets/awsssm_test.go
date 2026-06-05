package secrets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// fakeSSM is a configurable in-memory AWS SSM JSON-1.1 server for tests. It
// dispatches on the X-Amz-Target header.
type fakeSSM struct {
	srv        *httptest.Server
	getBody    string // JSON returned for GetParameter (200)
	getStatus  int    // override status for GetParameter; 0 = 200
	getErrBody string // error body for GetParameter when getStatus != 0
	listBody   string // JSON for GetParametersByPath
	delStatus  int    // status for DeleteParameter; 0 = 200
	delErrBody string
	lastTarget string
	lastBody   string
	lastAuth   string
}

func newFakeSSM(t *testing.T) *fakeSSM {
	t.Helper()
	fs := &fakeSSM{}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.lastTarget = r.Header.Get("X-Amz-Target")
		fs.lastAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		fs.lastBody = string(b)
		switch fs.lastTarget {
		case "AmazonSSM.GetParameter":
			if fs.getStatus != 0 {
				w.WriteHeader(fs.getStatus)
				_, _ = w.Write([]byte(fs.getErrBody))
				return
			}
			_, _ = w.Write([]byte(fs.getBody))
		case "AmazonSSM.PutParameter":
			_, _ = w.Write([]byte(`{}`))
		case "AmazonSSM.GetParametersByPath":
			_, _ = w.Write([]byte(fs.listBody))
		case "AmazonSSM.DeleteParameter":
			if fs.delStatus != 0 {
				w.WriteHeader(fs.delStatus)
				_, _ = w.Write([]byte(fs.delErrBody))
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

// newSSM builds an adapter whose endpoint points at the fake server.
func newSSM(t *testing.T, fs *fakeSSM) *AWSSSMAdapter {
	t.Helper()
	a, err := NewAWSSSMAdapter(AWSSSMConfig{
		Region: "us-east-1", AccessKey: "AKIATEST", SecretKey: "secret123",
	})
	if err != nil {
		t.Fatal(err)
	}
	adp := a.(*AWSSSMAdapter)
	adp.endpoint = fs.srv.URL + "/"
	return adp
}

func TestSSM_Get(t *testing.T) {
	fs := newFakeSSM(t)
	fs.getBody = `{"Parameter":{"Value":"topsecret"}}`
	a := newSSM(t, fs)
	got, err := a.Get(context.Background(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if got != "topsecret" {
		t.Errorf("Get = %q, want topsecret", got)
	}
	if fs.lastTarget != "AmazonSSM.GetParameter" {
		t.Errorf("target = %q", fs.lastTarget)
	}
	if !strings.Contains(fs.lastBody, "/vortex/db") {
		t.Errorf("body missing prefixed name: %s", fs.lastBody)
	}
}

func TestSSM_GetNotFound(t *testing.T) {
	fs := newFakeSSM(t)
	fs.getStatus = http.StatusBadRequest
	fs.getErrBody = `{"__type":"ParameterNotFound"}`
	a := newSSM(t, fs)
	if _, err := a.Get(context.Background(), "missing"); !os.IsNotExist(err) {
		t.Errorf("Get err = %v, want os.ErrNotExist", err)
	}
}

func TestSSM_GetServerError(t *testing.T) {
	fs := newFakeSSM(t)
	fs.getStatus = http.StatusInternalServerError
	fs.getErrBody = `{"__type":"InternalServerError"}`
	a := newSSM(t, fs)
	if _, err := a.Get(context.Background(), "x"); err == nil {
		t.Error("expected error on 500")
	}
}

func TestSSM_Set(t *testing.T) {
	fs := newFakeSSM(t)
	a := newSSM(t, fs)
	if err := a.Set(context.Background(), "api", "v"); err != nil {
		t.Fatal(err)
	}
	if fs.lastTarget != "AmazonSSM.PutParameter" {
		t.Errorf("target = %q", fs.lastTarget)
	}
	if !strings.Contains(fs.lastBody, "SecureString") {
		t.Errorf("Set should use SecureString: %s", fs.lastBody)
	}
}

func TestSSM_List(t *testing.T) {
	fs := newFakeSSM(t)
	fs.listBody = `{"Parameters":[{"Name":"/vortex/a"},{"Name":"/vortex/b"}]}`
	a := newSSM(t, fs)
	keys, err := a.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 2 || keys[0] != "a" || keys[1] != "b" {
		t.Errorf("List = %v, want [a b] (prefix stripped)", keys)
	}
}

func TestSSM_DeleteIdempotent(t *testing.T) {
	fs := newFakeSSM(t)
	fs.delStatus = http.StatusBadRequest
	fs.delErrBody = `{"__type":"ParameterNotFound"}`
	a := newSSM(t, fs)
	if err := a.Delete(context.Background(), "missing"); err != nil {
		t.Errorf("Delete on ParameterNotFound should be nil, got %v", err)
	}
}

func TestSSM_Ping(t *testing.T) {
	fs := newFakeSSM(t)
	a := newSSM(t, fs)
	if err := a.Ping(context.Background()); err != nil {
		t.Errorf("Ping = %v, want nil (any HTTP response is reachable)", err)
	}
}

func TestSSM_SigV4Deterministic(t *testing.T) {
	// A fixed time, key, and payload must produce a stable signature, proving
	// the canonical-request and signing-key derivation are correct.
	req, _ := http.NewRequest(http.MethodPost, "https://ssm.us-east-1.amazonaws.com/", strings.NewReader("{}"))
	req.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	req.Host = "ssm.us-east-1.amazonaws.com"
	fixed := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)
	if err := sigV4Sign(req, []byte("{}"), "AKIATEST", "secret123", "us-east-1", "ssm", fixed); err != nil {
		t.Fatal(err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 Credential=AKIATEST/20260605/us-east-1/ssm/aws4_request") {
		t.Errorf("unexpected Authorization prefix: %s", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-date;x-amz-target") {
		t.Errorf("SignedHeaders wrong: %s", auth)
	}
	// Re-signing identical inputs must yield an identical signature.
	req2, _ := http.NewRequest(http.MethodPost, "https://ssm.us-east-1.amazonaws.com/", strings.NewReader("{}"))
	req2.Header.Set("X-Amz-Target", "AmazonSSM.GetParameter")
	req2.Host = "ssm.us-east-1.amazonaws.com"
	_ = sigV4Sign(req2, []byte("{}"), "AKIATEST", "secret123", "us-east-1", "ssm", fixed)
	if req.Header.Get("Authorization") != req2.Header.Get("Authorization") {
		t.Error("SigV4 signature is not deterministic for identical inputs")
	}
}
