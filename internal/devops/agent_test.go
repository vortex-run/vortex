package devops

import (
	"context"
	"strings"
	"testing"
)

// newTestAgent builds a DevOpsAgent backed by a stub runner (no real SSH).
func newTestAgent(t *testing.T, r *writeRecorder) *DevOpsAgent {
	t.Helper()
	if r.responses == nil {
		r.responses = map[string]string{}
	}
	r.responses[`. /etc/os-release 2>/dev/null; echo "$ID"`] = "ubuntu\n"
	r.responses["uname -m"] = "x86_64\n"
	srv, err := newServerWithRunner(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetApprover(approveAll)
	a := NewDevOpsAgent(nil, nil, approveAll)
	a.server = srv
	a.docker = NewDockerManager(srv)
	a.nginx = NewNginxManager(srv)
	return a
}

func TestDevOps_NotConnectedErrors(t *testing.T) {
	a := NewDevOpsAgent(nil, nil, approveAll)
	if _, err := a.Handle(context.Background(), "server status", nil); err == nil {
		t.Error("Handle with no server should error")
	}
}

func TestDevOps_RoutesServerStatus(t *testing.T) {
	r := &writeRecorder{}
	r.stubRunner = stubRunner{responses: map[string]string{
		"hostname": "prod-vps\n", "nproc": "4\n",
		"free -m": "Mem: 8000 2000\n", "uptime -p": "up 1 hour\n",
	}}
	a := newTestAgent(t, r)
	out, err := a.Handle(context.Background(), "server status", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "prod-vps") || !strings.Contains(out, "ubuntu/amd64") {
		t.Errorf("server status output = %q", out)
	}
}

func TestDevOps_RoutesDockerPs(t *testing.T) {
	r := &writeRecorder{}
	r.stubRunner = stubRunner{responses: map[string]string{
		`docker ps --format '{{json .}}'`: `{"ID":"a1","Names":"web","Image":"nginx","Status":"Up","Ports":"80"}`,
	}}
	a := newTestAgent(t, r)
	out, err := a.Handle(context.Background(), "docker ps", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "web") || !strings.Contains(out, "nginx") {
		t.Errorf("docker ps output = %q", out)
	}
}

func TestDevOps_RoutesRestart(t *testing.T) {
	r := &writeRecorder{}
	a := newTestAgent(t, r)
	out, err := a.Handle(context.Background(), "restart nginx", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Restarted nginx") {
		t.Errorf("restart output = %q", out)
	}
	if !strings.Contains(r.lastCmd, "systemctl restart 'nginx'") {
		t.Errorf("restart command = %q", r.lastCmd)
	}
}

func TestDevOps_GeneralCommandGoesThroughApproval(t *testing.T) {
	r := &writeRecorder{}
	r.stubRunner = stubRunner{streamOut: []string{"output line"}}
	// Deny everything: a general command must require approval.
	srv, _ := newServerWithRunner(context.Background(), &writeRecorder{
		stubRunner: stubRunner{responses: map[string]string{
			`. /etc/os-release 2>/dev/null; echo "$ID"`: "ubuntu\n", "uname -m": "x86_64\n",
		}},
	})
	srv.SetApprover(func(string) bool { return false }) // deny
	a := NewDevOpsAgent(nil, nil, nil)
	a.server = srv
	a.docker = NewDockerManager(srv)
	a.nginx = NewNginxManager(srv)
	if _, err := a.Handle(context.Background(), "rm -rf /var/log/old", nil); err == nil {
		t.Error("a general command must require approval")
	}
}

func TestDevOps_RoutesAddNginxSite(t *testing.T) {
	r := &writeRecorder{}
	a := newTestAgent(t, r)
	out, err := a.Handle(context.Background(), "add nginx site api.example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Added nginx site api.example.com") {
		t.Errorf("add site output = %q", out)
	}
	if !strings.Contains(string(r.writtenData), "server_name api.example.com") {
		t.Errorf("nginx config not written: %q", r.writtenData)
	}
}

func TestDevOps_ServersReturnsHostname(t *testing.T) {
	r := &writeRecorder{}
	r.stubRunner = stubRunner{responses: map[string]string{"hostname": "myhost\n"}}
	a := newTestAgent(t, r)
	servers := a.Servers()
	if len(servers) != 1 || servers[0] != "myhost" {
		t.Errorf("Servers() = %v, want [myhost]", servers)
	}
}
