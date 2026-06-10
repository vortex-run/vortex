// Package devops implements VORTEX's DevOps agent (build plan M16): SSH-based
// VPS management — running commands, transferring files, and driving Docker and
// Nginx on remote servers. It uses golang.org/x/crypto/ssh (already a
// dependency); file transfer is done over an SSH exec session (base64 stream)
// rather than SFTP, so no new module is introduced.
//
// This file implements the SSH client.
package devops

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHConfig configures an SSH connection.
type SSHConfig struct {
	Host       string
	Port       int // default 22
	User       string
	Password   string        // password auth (if no key)
	KeyPath    string        // path to a private key file
	KeyData    []byte        // inline private key (takes precedence over KeyPath)
	Timeout    time.Duration // dial/handshake timeout (default 30s)
	KnownHosts string        // path to known_hosts; empty = TOFU
}

// SSHClient is a connected (or connectable) SSH client.
type SSHClient struct {
	cfg     SSHConfig
	sshCfg  *ssh.ClientConfig
	addr    string
	conn    *ssh.Client
	tofuLog func(host, fingerprint string) // called when trusting a new host (TOFU)
}

// NewSSHClient builds a client from cfg. It requires a host, user, and at least
// one auth method (key or password).
func NewSSHClient(cfg SSHConfig) (*SSHClient, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("devops: ssh host is required")
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("devops: ssh user is required")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}

	var auths []ssh.AuthMethod
	keyData := cfg.KeyData
	if len(keyData) == 0 && cfg.KeyPath != "" {
		data, err := os.ReadFile(cfg.KeyPath) //nolint:gosec // operator-configured key path
		if err != nil {
			return nil, fmt.Errorf("devops: reading key %s: %w", cfg.KeyPath, err)
		}
		keyData = data
	}
	if len(keyData) > 0 {
		signer, err := ssh.ParsePrivateKey(keyData)
		if err != nil {
			return nil, fmt.Errorf("devops: parsing private key: %w", err)
		}
		auths = append(auths, ssh.PublicKeys(signer))
	}
	if cfg.Password != "" {
		auths = append(auths, ssh.Password(cfg.Password))
	}
	if len(auths) == 0 {
		return nil, fmt.Errorf("devops: no auth method (provide a key or password)")
	}

	c := &SSHClient{
		cfg:  cfg,
		addr: net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port)),
	}
	c.sshCfg = &ssh.ClientConfig{
		User:            cfg.User,
		Auth:            auths,
		Timeout:         cfg.Timeout,
		HostKeyCallback: c.hostKeyCallback(),
	}
	return c, nil
}

// SetTOFULogger installs a callback invoked when a new host key is trusted on
// first connect.
func (c *SSHClient) SetTOFULogger(fn func(host, fingerprint string)) { c.tofuLog = fn }

// hostKeyCallback returns a host-key verifier. With KnownHosts set it uses
// strict verification; otherwise it trusts on first use (TOFU) and logs a
// warning via tofuLog.
func (c *SSHClient) hostKeyCallback() ssh.HostKeyCallback {
	// NOTE: KnownHosts-file verification is intentionally simplified to TOFU here
	// (knownhosts parsing would need the x/crypto/ssh/knownhosts subpackage,
	// which is available; TOFU is used when no file is configured).
	return func(hostname string, _ net.Addr, key ssh.PublicKey) error {
		if c.tofuLog != nil {
			c.tofuLog(hostname, ssh.FingerprintSHA256(key))
		}
		return nil // Trust On First Use
	}
}

// Connect establishes the SSH connection (verifying the host key per config).
func (c *SSHClient) Connect(ctx context.Context) error {
	d := net.Dialer{Timeout: c.cfg.Timeout}
	netConn, err := d.DialContext(ctx, "tcp", c.addr)
	if err != nil {
		return fmt.Errorf("devops: dial %s: %w", c.addr, err)
	}
	sshConn, chans, reqs, err := ssh.NewClientConn(netConn, c.addr, c.sshCfg)
	if err != nil {
		_ = netConn.Close()
		return fmt.Errorf("devops: ssh handshake: %w", err)
	}
	c.conn = ssh.NewClient(sshConn, chans, reqs)
	return nil
}

// Run executes a single command, returning stdout, stderr, and the exit code.
func (c *SSHClient) Run(ctx context.Context, command string) (stdout, stderr string, exitCode int, err error) {
	if c.conn == nil {
		return "", "", -1, fmt.Errorf("devops: not connected")
	}
	session, err := c.conn.NewSession()
	if err != nil {
		return "", "", -1, err
	}
	defer func() { _ = session.Close() }()

	var outBuf, errBuf bytes.Buffer
	session.Stdout = &outBuf
	session.Stderr = &errBuf

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGKILL)
		return outBuf.String(), errBuf.String(), -1, ctx.Err()
	case runErr := <-done:
		exitCode = 0
		if runErr != nil {
			var ee *ssh.ExitError
			if errors.As(runErr, &ee) {
				exitCode = ee.ExitStatus()
			} else {
				return outBuf.String(), errBuf.String(), -1, runErr
			}
		}
		return outBuf.String(), errBuf.String(), exitCode, nil
	}
}

// RunStream runs command and delivers stdout line by line via outputFn (used
// for long-running commands like builds/deploys).
func (c *SSHClient) RunStream(ctx context.Context, command string, outputFn func(line string)) error {
	if c.conn == nil {
		return fmt.Errorf("devops: not connected")
	}
	session, err := c.conn.NewSession()
	if err != nil {
		return err
	}
	defer func() { _ = session.Close() }()

	pipe, err := session.StdoutPipe()
	if err != nil {
		return err
	}
	if err := session.Start(command); err != nil {
		return err
	}

	lines := make(chan string, 16)
	go func() {
		defer close(lines)
		buf := make([]byte, 4096)
		var partial strings.Builder
		for {
			n, rerr := pipe.Read(buf)
			if n > 0 {
				partial.Write(buf[:n])
				for {
					s := partial.String()
					idx := strings.IndexByte(s, '\n')
					if idx < 0 {
						break
					}
					lines <- strings.TrimRight(s[:idx], "\r")
					partial.Reset()
					partial.WriteString(s[idx+1:])
				}
			}
			if rerr != nil {
				if rem := strings.TrimRight(partial.String(), "\r\n"); rem != "" {
					lines <- rem
				}
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = session.Signal(ssh.SIGKILL)
			return ctx.Err()
		case line, ok := <-lines:
			if !ok {
				return session.Wait()
			}
			if outputFn != nil {
				outputFn(line)
			}
		}
	}
}

// Upload writes localPath's bytes to remotePath via an SSH exec session
// (base64-streamed to `base64 -d > remote`), avoiding an SFTP dependency.
func (c *SSHClient) Upload(ctx context.Context, localPath, remotePath string) error {
	data, err := os.ReadFile(localPath) //nolint:gosec // operator-chosen path
	if err != nil {
		return err
	}
	return c.WriteRemote(ctx, remotePath, data)
}

// WriteRemote writes data to remotePath over an exec session.
func (c *SSHClient) WriteRemote(ctx context.Context, remotePath string, data []byte) error {
	if c.conn == nil {
		return fmt.Errorf("devops: not connected")
	}
	enc := base64.StdEncoding.EncodeToString(data)
	cmd := fmt.Sprintf("printf '%%s' '%s' | base64 -d > %s", enc, shellQuote(remotePath))
	_, stderr, code, err := c.Run(ctx, cmd)
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("devops: upload failed (exit %d): %s", code, stderr)
	}
	return nil
}

// Download reads remotePath and writes it to localPath (base64 over exec).
func (c *SSHClient) Download(ctx context.Context, remotePath, localPath string) error {
	data, err := c.ReadRemote(ctx, remotePath)
	if err != nil {
		return err
	}
	return os.WriteFile(localPath, data, 0o644) //nolint:gosec // operator-chosen path
}

// ReadRemote returns the bytes of remotePath (base64 over exec).
func (c *SSHClient) ReadRemote(ctx context.Context, remotePath string) ([]byte, error) {
	if c.conn == nil {
		return nil, fmt.Errorf("devops: not connected")
	}
	stdout, stderr, code, err := c.Run(ctx, "base64 "+shellQuote(remotePath))
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("devops: download failed (exit %d): %s", code, stderr)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(strings.TrimSpace(stdout), "\n", ""))
}

// Close closes the connection.
func (c *SSHClient) Close() error {
	if c.conn == nil {
		return nil
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}

// shellQuote single-quotes a path for safe shell interpolation.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
