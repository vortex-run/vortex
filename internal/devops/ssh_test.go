package devops

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// commandHandler simulates a remote command, returning stdout, stderr, exit.
type commandHandler func(cmd string) (stdout, stderr string, exit int)

// testSSHServer is an in-process SSH server for tests.
type testSSHServer struct {
	addr     string
	hostKey  ssh.Signer
	password string
	handle   commandHandler
	ln       net.Listener
	wg       sync.WaitGroup
}

// newTestSSHServer starts an SSH server accepting password "secret" and runs
// handle for each command. The host key is freshly generated.
func newTestSSHServer(t *testing.T, handle commandHandler) *testSSHServer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &testSSHServer{
		addr: ln.Addr().String(), hostKey: signer,
		password: "secret", handle: handle, ln: ln,
	}
	t.Cleanup(s.close)
	s.wg.Add(1)
	go s.serve()
	return s
}

func (s *testSSHServer) close() {
	_ = s.ln.Close()
	s.wg.Wait()
}

func (s *testSSHServer) serve() {
	defer s.wg.Done()
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) == s.password {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("denied")
		},
	}
	cfg.AddHostKey(s.hostKey)
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			return
		}
		go s.handleConn(conn, cfg)
	}
}

func (s *testSSHServer) handleConn(conn net.Conn, cfg *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer func() { _ = sconn.Close() }()
	go ssh.DiscardRequests(reqs)
	for ch := range chans {
		if ch.ChannelType() != "session" {
			_ = ch.Reject(ssh.UnknownChannelType, "only session")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(channel, requests)
	}
}

func (s *testSSHServer) handleSession(channel ssh.Channel, requests <-chan *ssh.Request) {
	for req := range requests {
		if req.Type != "exec" {
			_ = req.Reply(false, nil)
			continue
		}
		// Payload: 4-byte length + command string.
		cmd := string(req.Payload[4:])
		_ = req.Reply(true, nil)
		stdout, stderr, exit := s.handle(cmd)
		_, _ = io.WriteString(channel, stdout)
		_, _ = io.WriteString(channel.Stderr(), stderr)
		// Send exit-status then close.
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{uint32(exit)}))
		_ = channel.Close()
		return
	}
}

// echoHandler simulates common commands.
func echoHandler(cmd string) (string, string, int) {
	switch {
	case cmd == "echo hello":
		return "hello\n", "", 0
	case cmd == "fail":
		return "", "boom\n", 3
	case strings.HasPrefix(cmd, "base64 "):
		// ReadRemote: return base64 of fixed content.
		return "ZG93bmxvYWRlZA==", "", 0 // "downloaded"
	case strings.Contains(cmd, "base64 -d >"):
		return "", "", 0 // Upload sink succeeds.
	case cmd == "stream":
		return "line1\nline2\nline3\n", "", 0
	default:
		return "ran: " + cmd + "\n", "", 0
	}
}

func connectedClient(t *testing.T, srv *testSSHServer) *SSHClient {
	t.Helper()
	host, port, _ := net.SplitHostPort(srv.addr)
	c, err := NewSSHClient(SSHConfig{Host: host, Port: atoi(port), User: "u", Password: "secret", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func atoi(s string) int {
	n := 0
	for _, r := range s {
		n = n*10 + int(r-'0')
	}
	return n
}

func TestNewSSHClient_RequiresAuth(t *testing.T) {
	if _, err := NewSSHClient(SSHConfig{Host: "h", User: "u"}); err == nil {
		t.Error("no key/password should error")
	}
	if _, err := NewSSHClient(SSHConfig{User: "u", Password: "p"}); err == nil {
		t.Error("missing host should error")
	}
}

func TestSSH_ConnectAndRun(t *testing.T) {
	srv := newTestSSHServer(t, echoHandler)
	c := connectedClient(t, srv)
	stdout, stderr, code, err := c.Run(context.Background(), "echo hello")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(stdout) != "hello" || stderr != "" || code != 0 {
		t.Errorf("run result: out=%q err=%q code=%d", stdout, stderr, code)
	}
}

func TestSSH_RunCapturesStderrAndExit(t *testing.T) {
	srv := newTestSSHServer(t, echoHandler)
	c := connectedClient(t, srv)
	_, stderr, code, err := c.Run(context.Background(), "fail")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.TrimSpace(stderr) != "boom" || code != 3 {
		t.Errorf("stderr=%q code=%d, want boom/3", stderr, code)
	}
}

func TestSSH_RunNotConnected(t *testing.T) {
	c, _ := NewSSHClient(SSHConfig{Host: "h", User: "u", Password: "p"})
	if _, _, _, err := c.Run(context.Background(), "x"); err == nil {
		t.Error("Run before Connect should error")
	}
}

func TestSSH_RunStream(t *testing.T) {
	srv := newTestSSHServer(t, echoHandler)
	c := connectedClient(t, srv)
	var lines []string
	if err := c.RunStream(context.Background(), "stream", func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if len(lines) != 3 || lines[0] != "line1" || lines[2] != "line3" {
		t.Errorf("streamed lines = %v", lines)
	}
}

func TestSSH_UploadWritesRemote(t *testing.T) {
	var uploaded string
	srv := newTestSSHServer(t, func(cmd string) (string, string, int) {
		if strings.Contains(cmd, "base64 -d >") {
			uploaded = cmd
			return "", "", 0
		}
		return "", "", 0
	})
	c := connectedClient(t, srv)
	dir := t.TempDir()
	local := filepath.Join(dir, "f.txt")
	_ = os.WriteFile(local, []byte("payload"), 0o600)
	if err := c.Upload(context.Background(), local, "/remote/f.txt"); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if !strings.Contains(uploaded, "/remote/f.txt") || !strings.Contains(uploaded, "base64 -d") {
		t.Errorf("upload command = %q", uploaded)
	}
}

func TestSSH_DownloadReadsRemote(t *testing.T) {
	srv := newTestSSHServer(t, echoHandler) // base64 → "downloaded"
	c := connectedClient(t, srv)
	dir := t.TempDir()
	local := filepath.Join(dir, "out.txt")
	if err := c.Download(context.Background(), "/remote/x", local); err != nil {
		t.Fatalf("Download: %v", err)
	}
	data, _ := os.ReadFile(local)
	if string(data) != "downloaded" {
		t.Errorf("downloaded content = %q, want 'downloaded'", data)
	}
}

func TestSSH_CloseCleansUp(t *testing.T) {
	srv := newTestSSHServer(t, echoHandler)
	c := connectedClient(t, srv)
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	// A second close is a no-op.
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}

func TestSSH_TOFULogsNewHost(t *testing.T) {
	srv := newTestSSHServer(t, echoHandler)
	host, port, _ := net.SplitHostPort(srv.addr)
	c, _ := NewSSHClient(SSHConfig{Host: host, Port: atoi(port), User: "u", Password: "secret"})
	var trusted string
	c.SetTOFULogger(func(h, fp string) { trusted = h + " " + fp })
	if err := c.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if !strings.Contains(trusted, "SHA256:") {
		t.Errorf("TOFU logger should record the fingerprint, got %q", trusted)
	}
}
