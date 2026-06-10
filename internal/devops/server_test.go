package devops

import (
	"context"
	"strings"
	"testing"
)

// stubRunner answers commands from a map; falls back to empty output.
type stubRunner struct {
	responses map[string]string // command (or prefix) → stdout
	exit      map[string]int    // command → exit code
	streamOut []string          // lines delivered by RunStream
	lastCmd   string
}

func (r *stubRunner) Run(_ context.Context, cmd string) (string, string, int, error) {
	r.lastCmd = cmd
	code := 0
	if r.exit != nil {
		if c, ok := r.exit[cmd]; ok {
			code = c
		}
	}
	// Exact match, then prefix match.
	if out, ok := r.responses[cmd]; ok {
		return out, "", code, nil
	}
	for k, v := range r.responses {
		if strings.HasPrefix(cmd, k) {
			return v, "", code, nil
		}
	}
	return "", "", code, nil
}

func (r *stubRunner) RunStream(_ context.Context, cmd string, fn func(string)) error {
	r.lastCmd = cmd
	for _, l := range r.streamOut {
		fn(l)
	}
	return nil
}

func newTestServer(t *testing.T, r *stubRunner) *Server {
	t.Helper()
	// Provide OS/arch detection responses.
	if r.responses == nil {
		r.responses = map[string]string{}
	}
	r.responses[`. /etc/os-release 2>/dev/null; echo "$ID"`] = "ubuntu\n"
	r.responses["uname -m"] = "x86_64\n"
	s, err := newServerWithRunner(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func approveAll(string) bool { return true }

func TestServer_DetectsOSAndArch(t *testing.T) {
	s := newTestServer(t, &stubRunner{})
	if s.OS != "ubuntu" {
		t.Errorf("OS = %q, want ubuntu", s.OS)
	}
	if s.Arch != "amd64" {
		t.Errorf("Arch = %q, want amd64 (from x86_64)", s.Arch)
	}
}

func TestServer_SystemInfo(t *testing.T) {
	r := &stubRunner{responses: map[string]string{
		"hostname":          "prod-vps\n",
		"nproc":             "4\n",
		"free -m":           "              total        used\nMem:           8000        2000\nSwap:             0           0\n",
		"df -BG":            "20G\n",
		"uptime -p":         "up 2 hours\n",
		"cat /proc/loadavg": "0.10 0.20 0.30 1/100 1234\n",
	}}
	s := newTestServer(t, r)
	info, err := s.SystemInfo()
	if err != nil {
		t.Fatal(err)
	}
	if info.Hostname != "prod-vps" || info.CPUs != 4 || info.MemoryMB != 8000 || info.DiskGB != 20 {
		t.Errorf("system info = %+v", info)
	}
	if info.Uptime != "up 2 hours" || !strings.HasPrefix(info.LoadAvg, "0.10") {
		t.Errorf("uptime/load = %q / %q", info.Uptime, info.LoadAvg)
	}
}

func TestServer_RunCommandStreams(t *testing.T) {
	r := &stubRunner{streamOut: []string{"building...", "done"}}
	s := newTestServer(t, r)
	s.SetApprover(approveAll)
	var lines []string
	out, err := s.RunCommand(context.Background(), "make build", func(l string) { lines = append(lines, l) })
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[1] != "done" {
		t.Errorf("streamed = %v", lines)
	}
	if !strings.Contains(out, "building...") {
		t.Errorf("aggregated output = %q", out)
	}
}

func TestServer_RunCommandRequiresApproval(t *testing.T) {
	s := newTestServer(t, &stubRunner{}) // no approver → deny
	if _, err := s.RunCommand(context.Background(), "rm -rf /tmp/x", nil); err == nil {
		t.Error("unapproved command should error")
	}
}

func TestServer_InstallPackageRequiresApproval(t *testing.T) {
	s := newTestServer(t, &stubRunner{})
	if err := s.InstallPackage(context.Background(), "nginx"); err == nil {
		t.Error("unapproved install should error")
	}
}

func TestServer_InstallPackageUsesAptOnUbuntu(t *testing.T) {
	r := &stubRunner{}
	s := newTestServer(t, r)
	s.SetApprover(approveAll)
	if err := s.InstallPackage(context.Background(), "nginx"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.lastCmd, "apt-get install -y nginx") {
		t.Errorf("install command = %q, want apt-get", r.lastCmd)
	}
}

func TestServer_InstallPackageUsesYumOnCentos(t *testing.T) {
	r := &stubRunner{}
	s := newTestServer(t, r)
	s.OS = "centos"
	s.SetApprover(approveAll)
	_ = s.InstallPackage(context.Background(), "httpd")
	if !strings.Contains(r.lastCmd, "yum install -y httpd") {
		t.Errorf("install command = %q, want yum", r.lastCmd)
	}
}

func TestServer_ServiceStatus(t *testing.T) {
	r := &stubRunner{responses: map[string]string{
		"systemctl is-active": "active\n",
	}}
	s := newTestServer(t, r)
	out, err := s.ServiceStatus(context.Background(), "nginx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "active") {
		t.Errorf("status = %q", out)
	}
}

func TestServer_ServiceRestartRequiresApproval(t *testing.T) {
	s := newTestServer(t, &stubRunner{})
	if err := s.ServiceRestart(context.Background(), "nginx"); err == nil {
		t.Error("unapproved restart should error")
	}
	s.SetApprover(approveAll)
	if err := s.ServiceRestart(context.Background(), "nginx"); err != nil {
		t.Errorf("approved restart should succeed: %v", err)
	}
}
