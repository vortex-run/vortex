package devops

import (
	"context"
	"strings"
	"testing"
)

func newTestDocker(t *testing.T, r *stubRunner) *DockerManager {
	t.Helper()
	return NewDockerManager(newTestServer(t, r))
}

func TestDocker_ListContainers(t *testing.T) {
	r := &stubRunner{responses: map[string]string{
		`docker ps --format '{{json .}}'`: `{"ID":"abc123","Names":"web","Image":"nginx","Status":"Up 2 hours","Ports":"0.0.0.0:80->80/tcp"}
{"ID":"def456","Names":"db","Image":"postgres","Status":"Up 1 day","Ports":"5432/tcp"}`,
	}}
	d := newTestDocker(t, r)
	cs, err := d.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 2 {
		t.Fatalf("got %d containers, want 2", len(cs))
	}
	if cs[0].Name != "web" || cs[0].Image != "nginx" || cs[0].ID != "abc123" {
		t.Errorf("container[0] = %+v", cs[0])
	}
	if cs[1].Name != "db" {
		t.Errorf("container[1] = %+v", cs[1])
	}
}

func TestDocker_ListContainersEmpty(t *testing.T) {
	d := newTestDocker(t, &stubRunner{responses: map[string]string{`docker ps --format '{{json .}}'`: ""}})
	cs, err := d.ListContainers(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cs) != 0 {
		t.Errorf("no containers should yield empty list, got %d", len(cs))
	}
}

func TestDocker_Logs(t *testing.T) {
	r := &stubRunner{responses: map[string]string{"docker logs": "log line 1\nlog line 2\n"}}
	d := newTestDocker(t, r)
	out, err := d.Logs(context.Background(), "web", 50)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "log line 1") {
		t.Errorf("logs = %q", out)
	}
	if !strings.Contains(r.lastCmd, "--tail=50") || !strings.Contains(r.lastCmd, "web") {
		t.Errorf("logs command = %q", r.lastCmd)
	}
}

func TestDocker_Stats(t *testing.T) {
	r := &stubRunner{responses: map[string]string{
		`docker stats --no-stream --format '{{json .}}'`: `{"Name":"web","CPUPerc":"0.50%","MemUsage":"25MiB / 1GiB"}`,
	}}
	d := newTestDocker(t, r)
	stats, err := d.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Name != "web" || stats[0].CPU != "0.50%" {
		t.Errorf("stats = %+v", stats)
	}
}

func TestDocker_StartRequiresApproval(t *testing.T) {
	d := newTestDocker(t, &stubRunner{}) // no approver
	if err := d.StartContainer(context.Background(), "web"); err == nil {
		t.Error("unapproved start should error")
	}
}

func TestDocker_StopRunPullRequireApproval(t *testing.T) {
	d := newTestDocker(t, &stubRunner{})
	if err := d.StopContainer(context.Background(), "web"); err == nil {
		t.Error("unapproved stop should error")
	}
	if err := d.Pull(context.Background(), "nginx:latest"); err == nil {
		t.Error("unapproved pull should error")
	}
	if err := d.RunContainer(context.Background(), "nginx", "web", nil, nil); err == nil {
		t.Error("unapproved run should error")
	}
}

func TestDocker_RunContainerBuildsCommand(t *testing.T) {
	r := &stubRunner{}
	d := newTestDocker(t, r)
	d.server.SetApprover(approveAll)
	err := d.RunContainer(context.Background(), "nginx:1.25", "web",
		map[string]string{"8080": "80"}, map[string]string{"ENV": "prod"})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"docker run -d", "--name 'web'", "-p '8080:80'", "-e 'ENV=prod'", "'nginx:1.25'"} {
		if !strings.Contains(r.lastCmd, want) {
			t.Errorf("run command missing %q: %s", want, r.lastCmd)
		}
	}
}

func TestDocker_StartApprovedSucceeds(t *testing.T) {
	r := &stubRunner{}
	d := newTestDocker(t, r)
	d.server.SetApprover(approveAll)
	if err := d.StartContainer(context.Background(), "web"); err != nil {
		t.Fatalf("approved start should succeed: %v", err)
	}
	if !strings.Contains(r.lastCmd, "docker start 'web'") {
		t.Errorf("start command = %q", r.lastCmd)
	}
}
