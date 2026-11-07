package studio

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePasswordCodeServer mimics code-server's --auth password: POST /login
// with the right password sets a session cookie; everything else needs it.
func fakePasswordCodeServer(t *testing.T, password string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" && r.Method == http.MethodPost {
			_ = r.ParseForm()
			if r.Form.Get("password") != password {
				http.Error(w, "bad password", http.StatusUnauthorized)
				return
			}
			http.SetCookie(w, &http.Cookie{Name: "code-server-session", Value: "sess-ok", Path: "/"})
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		if ck, err := r.Cookie("code-server-session"); err != nil || ck.Value != "sess-ok" {
			http.Error(w, "login required", http.StatusUnauthorized)
			return
		}
		_, _ = io.WriteString(w, "editor;cookies="+r.Header.Get("Cookie"))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Audit NV-02: in TCP mode code-server requires a password, so another local
// process hitting the raw port gets nothing; Gubern logs in itself and the
// Studio proxy carries the session.
func TestCodeServer_PasswordModeLoginAndProxy(t *testing.T) {
	const pw = "per-launch-secret"
	srv := fakePasswordCodeServer(t, pw)
	host := strings.TrimPrefix(srv.URL, "http://")

	// Direct access to the port, as another local process would try.
	if resp, err := http.Get(srv.URL + "/"); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw port without session: %v %v, want 401", resp, err)
	}

	cs := &CodeServer{log: discardLogger(), addr: host, password: pw}
	cookies, err := cs.login(context.Background())
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	cs.session.Store(&cookies)

	front := httptest.NewServer(cs.ProxyHandler())
	defer front.Close()
	req, _ := http.NewRequest(http.MethodGet, front.URL+"/", nil)
	// A client trying to choose the session, plus an unrelated cookie.
	req.Header.Set("Cookie", "code-server-session=attacker; theme=dark")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("proxied request: %d %s", resp.StatusCode, body)
	}
	if strings.Contains(string(body), "attacker") || !strings.Contains(string(body), "theme=dark") {
		t.Errorf("cookie handling wrong: %s", body)
	}

	bad := &CodeServer{log: discardLogger(), addr: host, password: "wrong"}
	if _, err := bad.login(context.Background()); err == nil {
		t.Error("login with the wrong password must fail (Start then refuses to run)")
	}
}

func TestWithSession_ReplacesOnlyOurCookie(t *testing.T) {
	h := http.Header{}
	h.Add("Cookie", "a=1; code-server-session=evil")
	withSession(h, []*http.Cookie{{Name: "code-server-session", Value: "good"}})
	got := h.Get("Cookie")
	if got != "a=1; code-server-session=good" {
		t.Errorf("Cookie = %q", got)
	}

}
