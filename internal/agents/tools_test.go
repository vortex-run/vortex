package agents

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestToolRegistry_RegisterGetRoundTrip(t *testing.T) {
	r := NewToolRegistry()
	if err := r.Register(HTTPGetTool{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	got, err := r.Get("http_get")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name() != "http_get" {
		t.Errorf("Name = %q, want http_get", got.Name())
	}
}

func TestToolRegistry_GetUnknownErrors(t *testing.T) {
	r := NewToolRegistry()
	if _, err := r.Get("nope"); !errors.Is(err, ErrToolNotFound) {
		t.Errorf("Get unknown err = %v, want ErrToolNotFound", err)
	}
}

func TestHTTPGetTool_FetchesURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "hello")
	}))
	defer srv.Close()

	// httptest binds loopback, which the SSRF guard blocks by default; the
	// operator opt-in (AllowedHosts) is the supported way to reach it.
	res, err := HTTPGetTool{Client: srv.Client(), AllowedHosts: []string{"127.0.0.1"}}.
		Execute(context.Background(), map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := res.(map[string]any)
	if m["status"].(int) != 200 || m["body"].(string) != "hello" {
		t.Errorf("got status=%v body=%v, want 200 hello", m["status"], m["body"])
	}
}

func TestHTTPGet_BlocksSSRFTargets(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"loopback-ip", "http://127.0.0.1/secret"},
		{"localhost", "http://localhost/secret"},
		{"metadata", "http://169.254.169.254/latest/meta-data/"},
		{"rfc1918-10", "http://10.0.0.1/"},
		{"rfc1918-192", "http://192.168.1.1/"},
		{"rfc1918-172", "http://172.16.0.1/"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := HTTPGetTool{}.Execute(context.Background(), map[string]any{"url": c.url})
			if !errors.Is(err, ErrSSRFBlocked) {
				t.Errorf("%s: err = %v, want ErrSSRFBlocked", c.url, err)
			}
		})
	}
}

func TestHTTPGet_BlocksNonHTTPSchemeViaSSRF(t *testing.T) {
	for _, u := range []string{"file:///etc/passwd", "ftp://host/x", "gopher://host"} {
		_, err := HTTPGetTool{}.Execute(context.Background(), map[string]any{"url": u})
		if !errors.Is(err, ErrSandboxViolation) {
			t.Errorf("%s: err = %v, want ErrSandboxViolation", u, err)
		}
	}
}

func TestHTTPGet_AllowedHostsRestriction(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()

	// Host not in the allow-list is blocked even though it would otherwise be
	// reachable.
	_, err := HTTPGetTool{Client: srv.Client(), AllowedHosts: []string{"example.com"}}.
		Execute(context.Background(), map[string]any{"url": srv.URL})
	if !errors.Is(err, ErrSSRFBlocked) {
		t.Errorf("host outside allow-list: err = %v, want ErrSSRFBlocked", err)
	}
}

func TestHTTPGet_AllowsPublicIP(t *testing.T) {
	// A literal public IP passes the SSRF check without any DNS lookup, so this
	// is hermetic (no network). 8.8.8.8 is public (not private/loopback/meta).
	if err := (HTTPGetTool{}).isSSRFTarget("https://8.8.8.8/path"); err != nil {
		t.Errorf("public IP should pass SSRF check, got: %v", err)
	}
}

func TestHTTPGetTool_RejectsNonHTTPScheme(t *testing.T) {
	_, err := HTTPGetTool{}.Execute(context.Background(), map[string]any{"url": "file:///etc/passwd"})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("err = %v, want ErrSandboxViolation", err)
	}
}

func TestWriteFileTool_WritesToSandbox(t *testing.T) {
	dir := t.TempDir()
	res, err := WriteFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "sub/out.txt", "content": "data"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.(map[string]any)["bytes_written"].(int) != 4 {
		t.Errorf("bytes_written = %v, want 4", res.(map[string]any)["bytes_written"])
	}
	got, _ := os.ReadFile(filepath.Join(dir, "sub", "out.txt"))
	if string(got) != "data" {
		t.Errorf("file content = %q, want data", got)
	}
}

func TestWriteFileTool_RejectsEscape(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "../escape.txt", "content": "x"})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("err = %v, want ErrSandboxViolation", err)
	}
}

func TestReadFileTool_ReadsInSandbox(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("abc"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := ReadFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "f.txt"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := res.(map[string]any)
	if m["content"].(string) != "abc" || m["size"].(int) != 3 {
		t.Errorf("got %v, want content=abc size=3", m)
	}
}

func TestReadFileTool_RejectsEscape(t *testing.T) {
	dir := t.TempDir()
	_, err := ReadFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "../../etc/hosts"})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("err = %v, want ErrSandboxViolation", err)
	}
}

func TestRunCommandTool_RunsAllowed(t *testing.T) {
	dir := t.TempDir()
	// "go version" is allowed; RequireApproval is false (zero value) so it runs.
	res, err := RunCommandTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"command": "go", "args": []string{"version"}})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := res.(map[string]any)
	if m["exit_code"].(int) != 0 {
		t.Errorf("exit_code = %v, want 0 (stderr=%v)", m["exit_code"], m["stderr"])
	}
}

func TestRunCommandTool_RejectsDisallowed(t *testing.T) {
	bad := "rm"
	if runtime.GOOS == "windows" {
		bad = "del"
	}
	_, err := RunCommandTool{SandboxDir: t.TempDir()}.Execute(context.Background(),
		map[string]any{"command": bad, "args": []string{}})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("err = %v, want ErrSandboxViolation", err)
	}
}

func TestRunCommandTool_InterpretersRemovedFromAllowlist(t *testing.T) {
	for _, banned := range []string{"python3", "npm", "pip3"} {
		if (RunCommandTool{}).allowed(banned) {
			t.Errorf("%q must not be in the default allowlist (RCE risk)", banned)
		}
	}
	for _, ok := range []string{"go", "git", "tar", "unzip", "flutter"} {
		if !(RunCommandTool{}).allowed(ok) {
			t.Errorf("%q should remain allowed", ok)
		}
	}
}

func TestRunCommandTool_DangerousArgsRejected(t *testing.T) {
	// Each case opts the command into the allowlist so the rejection is proven
	// to come from validateArgs, not the allowlist check.
	cases := []struct {
		name    string
		command string
		args    []string
	}{
		{"curl-file", "curl", []string{"-o", "x", "file:///etc/passwd"}},
		{"curl-loopback", "curl", []string{"http://127.0.0.1/secret"}},
		{"curl-metadata", "curl", []string{"http://169.254.169.254/latest/meta-data/"}},
		{"curl-rfc1918", "curl", []string{"http://10.0.0.5/"}},
		{"git-sshcommand", "git", []string{"-c", "core.sshCommand=touch pwned", "clone", "x"}},
		{"go-exec", "go", []string{"test", "-exec", "evil", "./..."}},
		{"tar-tocommand", "tar", []string{"--to-command=evil", "-xf", "a.tar"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tool := RunCommandTool{SandboxDir: t.TempDir(), AllowedCommands: []string{c.command}}
			_, err := tool.Execute(context.Background(),
				map[string]any{"command": c.command, "args": c.args})
			if !errors.Is(err, ErrSandboxViolation) {
				t.Errorf("%s: err = %v, want ErrSandboxViolation", c.name, err)
			}
		})
	}
}

func TestRunCommandTool_LegitimateArgsPass(t *testing.T) {
	// These pass validateArgs; with RequireApproval the tool stops at the gate
	// (an ApprovalError, not a sandbox violation) — proving validation passed.
	// curl is not in the default allowlist (SSRF/file-read risk), so an operator
	// must opt it in explicitly; when they do, validateArgs still applies.
	cases := []struct {
		command string
		allow   []string
		args    []string
	}{
		{"curl", []string{"curl"}, []string{"https://example.com"}},
		{"go", nil, []string{"build", "./..."}},
	}
	for _, c := range cases {
		_, err := NewRunCommandTool(t.TempDir(), c.allow).Execute(context.Background(),
			map[string]any{"command": c.command, "args": c.args})
		if errors.Is(err, ErrSandboxViolation) {
			t.Errorf("%s %v should pass validation, got sandbox violation: %v", c.command, c.args, err)
		}
		if !errors.Is(err, ErrApprovalRequired) {
			t.Errorf("%s %v should reach the approval gate, got: %v", c.command, c.args, err)
		}
	}
}

func TestRunCommandTool_ApprovalGate(t *testing.T) {
	_, err := NewRunCommandTool(t.TempDir(), nil).Execute(context.Background(),
		map[string]any{"command": "go", "args": []string{"version"}})
	if !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("err = %v, want ErrApprovalRequired", err)
	}
	var ae *ApprovalError
	if !errors.As(err, &ae) {
		t.Fatalf("err is not *ApprovalError: %v", err)
	}
	if ae.Request.Command != "go" || len(ae.Request.Args) != 1 || ae.Request.Args[0] != "version" {
		t.Errorf("approval request = %+v, want go [version]", ae.Request)
	}
}

func TestVortexAPITool_CallsManagementAPI(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	res, err := VortexAPITool{BaseURL: srv.URL, Client: srv.Client()}.Execute(context.Background(),
		map[string]any{"method": "GET", "path": "/api/status"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if gotPath != "/api/status" {
		t.Errorf("server got path %q, want /api/status", gotPath)
	}
	if res.(map[string]any)["status"].(int) != 200 {
		t.Errorf("status = %v, want 200", res.(map[string]any)["status"])
	}
}

func TestVortexAPITool_RejectsNonAPIPath(t *testing.T) {
	_, err := VortexAPITool{BaseURL: "http://x"}.Execute(context.Background(),
		map[string]any{"method": "GET", "path": "/admin"})
	if !errors.Is(err, ErrSandboxViolation) {
		t.Errorf("err = %v, want ErrSandboxViolation", err)
	}
}

func TestVortexAPITool_DeniesAgentAndControlPaths(t *testing.T) {
	for _, p := range []string{"/api/agents/submit", "/api/agents/status", "/internal/shutdown", "/internal/reload"} {
		_, err := VortexAPITool{BaseURL: "http://x"}.Execute(context.Background(),
			map[string]any{"method": "POST", "path": p})
		if !errors.Is(err, ErrSandboxViolation) {
			t.Errorf("path %q: err = %v, want ErrSandboxViolation", p, err)
		}
	}
}

func TestVortexAPITool_AllowsSafeAPIPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()
	_, err := VortexAPITool{BaseURL: srv.URL, Client: srv.Client()}.Execute(context.Background(),
		map[string]any{"method": "GET", "path": "/api/status"})
	if err != nil {
		t.Fatalf("/api/status should be allowed: %v", err)
	}
	if gotPath != "/api/status" {
		t.Errorf("server got %q, want /api/status", gotPath)
	}
}

func TestSendMessageTool_DeliversViaBus(t *testing.T) {
	bus := NewBus()
	target := NewBaseAgent(AgentConfig{Name: "worker", Type: TypeTask})
	_ = bus.Register(target)

	res, err := SendMessageTool{Bus: bus, From: "coordinator"}.Execute(context.Background(),
		map[string]any{"to": "worker", "type": MsgTaskBrief, "payload": "do it"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.(map[string]any)["sent"].(bool) != true {
		t.Error("sent should be true")
	}
	select {
	case msg := <-target.Receive():
		if msg.FromAgent != "coordinator" || msg.Payload.(string) != "do it" {
			t.Errorf("delivered msg = %+v", msg)
		}
	default:
		t.Error("worker should have received the message")
	}
}

// recordingAudit captures audit appends for assertions.
type recordingAudit struct{ calls []string }

func (r *recordingAudit) Append(_ context.Context, _, action, resource string, _ map[string]any) error {
	r.calls = append(r.calls, action+":"+resource)
	return nil
}

func TestSandboxedRegistry_AuditsExecution(t *testing.T) {
	reg := NewToolRegistry()
	_ = reg.Register(WriteFileTool{SandboxDir: t.TempDir()})
	rec := &recordingAudit{}
	s := NewSandboxedRegistry(reg, t.TempDir(), nil, nil).WithAudit(rec, "tester")

	tool, _ := s.Get("write_file")
	if tool == nil {
		t.Fatal("write_file not found via sandbox Get")
	}
	_, err := s.Execute(context.Background(), "write_file",
		map[string]any{"path": "ok.txt", "content": "hi"})
	if err != nil {
		// The sandbox dir on the tool differs from s.sandboxDir; that's fine —
		// the tool enforces its own bound dir. We only assert auditing here.
		t.Logf("execute err (expected possible): %v", err)
	}
	if len(rec.calls) != 1 || rec.calls[0] != "tool.execute:write_file" {
		t.Errorf("audit calls = %v, want [tool.execute:write_file]", rec.calls)
	}
}

// mkSymlink creates a symlink old→new, skipping on platforms/permissions where
// symlink creation is denied (typically Windows without privilege). On CI/Linux
// (where the C3 exploit applies) a failure to create is fatal, not skipped, so
// the escape test cannot silently no-op.
func mkSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if os.Getenv("CI") != "" && runtime.GOOS != "windows" {
			t.Fatalf("symlink creation must work on CI/Linux: %v", err)
		}
		t.Skipf("symlink not supported here: %v", err)
	}
}

func TestSandbox_SymlinkPointingInsideAllowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("inside"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkSymlink(t, target, filepath.Join(dir, "link")) // points INSIDE the sandbox

	res, err := ReadFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "link"})
	if err != nil {
		t.Fatalf("legitimate in-sandbox symlink should be readable: %v", err)
	}
	if res.(map[string]any)["content"].(string) != "inside" {
		t.Errorf("content = %v, want inside", res.(map[string]any)["content"])
	}
}

func TestSandbox_SymlinkEscape(t *testing.T) {
	dir := t.TempDir()
	// A secret OUTSIDE the sandbox.
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("TOP SECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	mkSymlink(t, secret, filepath.Join(dir, "link")) // inside sandbox, points OUT

	// Read via the escaping symlink must be blocked.
	_, err := ReadFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "link"})
	if !errors.Is(err, ErrSandboxEscape) {
		t.Errorf("read via escaping symlink: err = %v, want ErrSandboxEscape", err)
	}
}

func TestSandbox_WriteViaSymlinkedParentRejected(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	// "dir/out" is a symlink to a directory outside the sandbox; a write to
	// "out/file.txt" would land outside.
	mkSymlink(t, outside, filepath.Join(dir, "out"))

	_, err := WriteFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "out/file.txt", "content": "x"})
	if !errors.Is(err, ErrSandboxEscape) {
		t.Errorf("write via symlinked parent: err = %v, want ErrSandboxEscape", err)
	}
}

func TestSandbox_WriteNewFileAllowed(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "newfile.txt", "content": "ok"})
	if err != nil {
		t.Fatalf("writing a new file in the sandbox should succeed: %v", err)
	}
}

func TestSandbox_BrokenSymlinkHandledSafely(t *testing.T) {
	dir := t.TempDir()
	// Symlink to a non-existent target inside the sandbox.
	mkSymlink(t, filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "broken"))
	_, err := ReadFileTool{SandboxDir: dir}.Execute(context.Background(),
		map[string]any{"path": "broken"})
	// Must error (EvalSymlinks fails on a broken link) and must not panic.
	if err == nil {
		t.Error("reading a broken symlink should return an error")
	}
}
