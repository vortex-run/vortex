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

	res, err := HTTPGetTool{Client: srv.Client()}.Execute(context.Background(),
		map[string]any{"url": srv.URL})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	m := res.(map[string]any)
	if m["status"].(int) != 200 || m["body"].(string) != "hello" {
		t.Errorf("got status=%v body=%v, want 200 hello", m["status"], m["body"])
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
