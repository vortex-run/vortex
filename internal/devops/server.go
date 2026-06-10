package devops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// runner is the subset of *SSHClient that Server needs (so tests can stub it).
type runner interface {
	Run(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error)
	RunStream(ctx context.Context, command string, outputFn func(line string)) error
}

// Server is a managed VPS reached over SSH.
type Server struct {
	ssh      runner
	OS       string // "ubuntu"|"debian"|"centos"|"alpine"|…
	Arch     string // "amd64"|"arm64"|…
	approver func(action string) bool
}

// SystemInfo summarises a server's resources.
type SystemInfo struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	CPUs     int    `json:"cpus"`
	MemoryMB int    `json:"memory_mb"`
	DiskGB   int    `json:"disk_gb"`
	Uptime   string `json:"uptime"`
	LoadAvg  string `json:"load_avg"`
}

// NewServer connects to ssh and detects the OS + architecture.
func NewServer(ssh *SSHClient) (*Server, error) {
	return newServerWithRunner(context.Background(), ssh)
}

// newServerWithRunner builds a Server from any runner (used by tests).
func newServerWithRunner(ctx context.Context, r runner) (*Server, error) {
	s := &Server{ssh: r}
	osID, _, _, err := r.Run(ctx, `. /etc/os-release 2>/dev/null; echo "$ID"`)
	if err != nil {
		return nil, fmt.Errorf("devops: detect OS: %w", err)
	}
	s.OS = strings.TrimSpace(osID)
	arch, _, _, err := r.Run(ctx, "uname -m")
	if err != nil {
		return nil, fmt.Errorf("devops: detect arch: %w", err)
	}
	s.Arch = normalizeArch(strings.TrimSpace(arch))
	return s, nil
}

// SetApprover installs the human-approval callback for mutating operations.
func (s *Server) SetApprover(fn func(action string) bool) { s.approver = fn }

// approve returns true when the action is approved (or no approver is set).
func (s *Server) approve(action string) bool {
	if s.approver == nil {
		return false // fail-safe: no approver → deny mutating ops
	}
	return s.approver(action)
}

// SystemInfo gathers hostname/CPU/memory/disk/uptime/load via SSH.
func (s *Server) SystemInfo() (*SystemInfo, error) {
	ctx := context.Background()
	info := &SystemInfo{OS: s.OS, Arch: s.Arch}

	if out, _, _, err := s.ssh.Run(ctx, "hostname"); err == nil {
		info.Hostname = strings.TrimSpace(out)
	}
	if out, _, _, err := s.ssh.Run(ctx, "nproc"); err == nil {
		info.CPUs = atoiSafe(strings.TrimSpace(out))
	}
	// free -m: total memory is the 2nd field of the "Mem:" line.
	if out, _, _, err := s.ssh.Run(ctx, "free -m"); err == nil {
		info.MemoryMB = parseFreeMem(out)
	}
	// df -h /: used/total; report total GB (strip the trailing G).
	if out, _, _, err := s.ssh.Run(ctx, "df -BG --output=size / | tail -1"); err == nil {
		info.DiskGB = atoiSafe(strings.TrimRight(strings.TrimSpace(out), "G"))
	}
	if out, _, _, err := s.ssh.Run(ctx, "uptime -p"); err == nil {
		info.Uptime = strings.TrimSpace(out)
	}
	if out, _, _, err := s.ssh.Run(ctx, "cat /proc/loadavg"); err == nil {
		info.LoadAvg = strings.TrimSpace(out)
	}
	return info, nil
}

// RunCommand runs an arbitrary command (approval-gated), streaming output.
func (s *Server) RunCommand(ctx context.Context, cmd string, stream func(string)) (string, error) {
	if !s.approve("run: " + cmd) {
		return "", fmt.Errorf("devops: command not approved: %s", cmd)
	}
	var b strings.Builder
	err := s.ssh.RunStream(ctx, cmd, func(line string) {
		b.WriteString(line + "\n")
		if stream != nil {
			stream(line)
		}
	})
	return b.String(), err
}

// InstallPackage installs pkg using the detected package manager (approval).
func (s *Server) InstallPackage(ctx context.Context, pkg string) error {
	if !s.approve("install package: " + pkg) {
		return fmt.Errorf("devops: install not approved: %s", pkg)
	}
	cmd := s.installCommand(pkg)
	_, stderr, code, err := s.ssh.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: install %s failed (exit %d): %s", pkg, code, strings.TrimSpace(stderr))
	}
	return nil
}

// installCommand returns the install command for the detected package manager.
func (s *Server) installCommand(pkg string) string {
	switch s.OS {
	case "centos", "rhel", "fedora", "rocky", "almalinux":
		return "yum install -y " + pkg
	case "alpine":
		return "apk add " + pkg
	default: // ubuntu/debian
		return "DEBIAN_FRONTEND=noninteractive apt-get install -y " + pkg
	}
}

// ServiceStatus returns the systemctl status of a service.
func (s *Server) ServiceStatus(ctx context.Context, service string) (string, error) {
	out, stderr, _, err := s.ssh.Run(ctx, "systemctl is-active "+shellQuote(service)+" 2>&1; systemctl status "+shellQuote(service)+" --no-pager 2>&1 | head -5")
	if err != nil {
		return "", err
	}
	if out == "" {
		out = stderr
	}
	return strings.TrimSpace(out), nil
}

// ServiceRestart restarts a service (approval-gated).
func (s *Server) ServiceRestart(ctx context.Context, service string) error {
	if !s.approve("restart service: " + service) {
		return fmt.Errorf("devops: restart not approved: %s", service)
	}
	_, stderr, code, err := s.ssh.Run(ctx, "systemctl restart "+shellQuote(service))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: restart %s failed (exit %d): %s", service, code, strings.TrimSpace(stderr))
	}
	return nil
}

// --- helpers ----------------------------------------------------------------

// normalizeArch maps uname -m output to Go-style arch names.
func normalizeArch(m string) string {
	switch m {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "armv7l", "armv6l":
		return "arm"
	default:
		return m
	}
}

// parseFreeMem extracts total memory (MB) from `free -m` output.
func parseFreeMem(out string) int {
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Mem:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				return atoiSafe(fields[1])
			}
		}
	}
	return 0
}

// atoiSafe parses an int, returning 0 on error.
func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}
