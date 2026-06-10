package devops

import (
	"context"
	"strings"
	"testing"
)

// writeRecorder is a stubRunner that also captures remote file writes.
type writeRecorder struct {
	stubRunner
	writtenPath string
	writtenData []byte
}

func (w *writeRecorder) WriteRemote(_ context.Context, path string, data []byte) error {
	w.writtenPath = path
	w.writtenData = data
	return nil
}

func newTestNginx(t *testing.T, r *writeRecorder) *NginxManager {
	t.Helper()
	if r.responses == nil {
		r.responses = map[string]string{}
	}
	r.responses[`. /etc/os-release 2>/dev/null; echo "$ID"`] = "ubuntu\n"
	r.responses["uname -m"] = "x86_64\n"
	s, err := newServerWithRunner(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	// NewNginxManager only sees the runner via server.ssh; ensure the writer is
	// detected (writeRecorder implements WriteRemote).
	return NewNginxManager(s)
}

func TestNginx_Status(t *testing.T) {
	r := &writeRecorder{}
	r.stubRunner = stubRunner{responses: map[string]string{"systemctl is-active": "active\n"}}
	n := newTestNginx(t, r)
	out, err := n.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("status = %q", out)
	}
}

func TestNginx_AddSiteCreatesConfig(t *testing.T) {
	r := &writeRecorder{}
	n := newTestNginx(t, r)
	n.server.SetApprover(approveAll)
	if err := n.AddSite(context.Background(), "api.example.com", "http://localhost:3000", false); err != nil {
		t.Fatalf("AddSite: %v", err)
	}
	// Config written to sites-available with the right server_name + proxy_pass.
	if r.writtenPath != "/etc/nginx/sites-available/api.example.com" {
		t.Errorf("written path = %q", r.writtenPath)
	}
	conf := string(r.writtenData)
	for _, want := range []string{"server_name api.example.com", "proxy_pass http://localhost:3000", "proxy_set_header Host $host"} {
		if !strings.Contains(conf, want) {
			t.Errorf("config missing %q:\n%s", want, conf)
		}
	}
	// Symlink + test + reload command run.
	if !strings.Contains(r.lastCmd, "ln -sf") || !strings.Contains(r.lastCmd, "nginx -t") {
		t.Errorf("enable command = %q", r.lastCmd)
	}
}

func TestNginx_AddSiteRequiresApproval(t *testing.T) {
	n := newTestNginx(t, &writeRecorder{})
	if err := n.AddSite(context.Background(), "x.com", "http://localhost:80", false); err == nil {
		t.Error("unapproved AddSite should error")
	}
}

func TestNginx_EnableSSLRunsCertbot(t *testing.T) {
	r := &writeRecorder{}
	n := newTestNginx(t, r)
	n.server.SetApprover(approveAll)
	if err := n.EnableSSL(context.Background(), "api.example.com", "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"certbot --nginx", "-d 'api.example.com'", "admin@example.com", "--non-interactive", "--agree-tos"} {
		if !strings.Contains(r.lastCmd, want) {
			t.Errorf("certbot command missing %q: %s", want, r.lastCmd)
		}
	}
}

func TestNginx_ListSites(t *testing.T) {
	r := &writeRecorder{}
	r.stubRunner = stubRunner{responses: map[string]string{"ls /etc/nginx/sites-enabled/": "api.example.com\nfrontend.example.com\n"}}
	n := newTestNginx(t, r)
	sites, err := n.ListSites(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 2 || sites[0] != "api.example.com" {
		t.Errorf("sites = %v", sites)
	}
}

func TestNginx_ReloadRequiresApproval(t *testing.T) {
	n := newTestNginx(t, &writeRecorder{})
	if err := n.Reload(context.Background()); err == nil {
		t.Error("unapproved reload should error")
	}
	n.server.SetApprover(approveAll)
	if err := n.Reload(context.Background()); err != nil {
		t.Errorf("approved reload should succeed: %v", err)
	}
}

func TestNginx_RemoveSiteRequiresApproval(t *testing.T) {
	n := newTestNginx(t, &writeRecorder{})
	if err := n.RemoveSite(context.Background(), "x.com"); err == nil {
		t.Error("unapproved remove should error")
	}
}
