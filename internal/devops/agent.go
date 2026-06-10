package devops

import (
	"context"
	"fmt"
	"strings"

	"github.com/vortex-run/vortex/internal/agents"
)

// Notifier delivers DevOps alerts. Satisfied by *messaging.Router via an
// adapter (keeps devops decoupled from messaging). Nil-safe at call sites.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
}

// DevOpsAgent orchestrates SSH/Docker/Nginx operations on connected servers.
//
//nolint:revive // DevOpsAgent name is mandated by the M16 spec
type DevOpsAgent struct {
	gateway  agents.AIGateway
	notifier Notifier
	approver func(action string) bool

	server *Server
	docker *DockerManager
	nginx  *NginxManager
}

// NewDevOpsAgent constructs a DevOps agent. approver gates mutating ops.
func NewDevOpsAgent(gateway agents.AIGateway, notifier Notifier, approver func(action string) bool) *DevOpsAgent {
	return &DevOpsAgent{gateway: gateway, notifier: notifier, approver: approver}
}

// Connect establishes an SSH connection to host and builds the sub-managers.
func (a *DevOpsAgent) Connect(ctx context.Context, host, user, keyPath string) error {
	ssh, err := NewSSHClient(SSHConfig{Host: host, User: user, KeyPath: keyPath})
	if err != nil {
		return err
	}
	if err := ssh.Connect(ctx); err != nil {
		return err
	}
	return a.attach(ctx, ssh)
}

// attach builds the Server + managers from a connected SSH client.
func (a *DevOpsAgent) attach(ctx context.Context, ssh *SSHClient) error {
	srv, err := newServerWithRunner(ctx, ssh)
	if err != nil {
		return err
	}
	srv.ssh = ssh // keep the concrete client so nginx can write files
	srv.SetApprover(a.approver)
	a.server = srv
	a.docker = NewDockerManager(srv)
	a.nginx = NewNginxManager(srv)
	return nil
}

// Servers returns the connected server hostnames (currently one).
func (a *DevOpsAgent) Servers() []string {
	if a.server == nil {
		return nil
	}
	info, err := a.server.SystemInfo()
	if err != nil || info.Hostname == "" {
		return []string{"connected"}
	}
	return []string{info.Hostname}
}

// Handle routes a natural-language DevOps request to the right operation,
// streaming progress via progressFn.
func (a *DevOpsAgent) Handle(ctx context.Context, msg string, progressFn func(string)) (string, error) {
	if a.server == nil {
		return "", fmt.Errorf("devops: no server connected")
	}
	emit := func(s string) {
		if progressFn != nil {
			progressFn(s)
		}
	}
	low := strings.ToLower(strings.TrimSpace(msg))

	switch {
	case strings.Contains(low, "server status"), strings.Contains(low, "system info"),
		strings.Contains(low, "disk space"), strings.Contains(low, "memory"):
		emit("📊 Gathering system info…")
		info, err := a.server.SystemInfo()
		if err != nil {
			return "", err
		}
		return formatSystemInfo(info), nil

	case strings.Contains(low, "docker ps"), strings.Contains(low, "list containers"):
		emit("🐳 Listing containers…")
		cs, err := a.docker.ListContainers(ctx)
		if err != nil {
			return "", err
		}
		return formatContainers(cs), nil

	case strings.HasPrefix(low, "docker logs "):
		name := strings.TrimSpace(msg[len("docker logs "):])
		return a.docker.Logs(ctx, name, 100)

	case strings.HasPrefix(low, "restart "):
		service := strings.TrimSpace(msg[len("restart "):])
		emit("🔄 Restarting " + service + "…")
		if err := a.server.ServiceRestart(ctx, service); err != nil {
			return "", err
		}
		return "✓ Restarted " + service, nil

	case strings.HasPrefix(low, "install "):
		pkg := strings.TrimSpace(msg[len("install "):])
		emit("📦 Installing " + pkg + "…")
		if err := a.server.InstallPackage(ctx, pkg); err != nil {
			return "", err
		}
		return "✓ Installed " + pkg, nil

	case strings.HasPrefix(low, "add nginx site "):
		domain := strings.TrimSpace(msg[len("add nginx site "):])
		emit("🌐 Adding nginx site " + domain + "…")
		// Default upstream localhost:3000 unless the message names a port.
		upstream := "http://localhost:3000"
		if err := a.nginx.AddSite(ctx, domain, upstream, false); err != nil {
			return "", err
		}
		return "✓ Added nginx site " + domain, nil

	case strings.HasPrefix(low, "enable ssl "):
		domain := strings.TrimSpace(msg[len("enable ssl "):])
		emit("🔒 Enabling SSL for " + domain + "…")
		if err := a.nginx.EnableSSL(ctx, domain, "admin@"+domain); err != nil {
			return "", err
		}
		return "✓ SSL enabled for " + domain, nil

	default:
		// General command — run via the approval gate, streaming output.
		emit("$ " + msg)
		return a.server.RunCommand(ctx, msg, progressFn)
	}
}

// --- formatting -------------------------------------------------------------

func formatSystemInfo(i *SystemInfo) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Host: %s (%s/%s)\n", i.Hostname, i.OS, i.Arch)
	fmt.Fprintf(&b, "CPUs: %d  Memory: %d MB  Disk: %d GB\n", i.CPUs, i.MemoryMB, i.DiskGB)
	fmt.Fprintf(&b, "Uptime: %s\nLoad: %s", i.Uptime, i.LoadAvg)
	return b.String()
}

func formatContainers(cs []Container) string {
	if len(cs) == 0 {
		return "No running containers."
	}
	var b strings.Builder
	for _, c := range cs {
		fmt.Fprintf(&b, "%s  %s  %s  %s\n", c.Name, c.Image, c.Status, c.Ports)
	}
	return strings.TrimRight(b.String(), "\n")
}
