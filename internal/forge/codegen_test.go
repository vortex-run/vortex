package forge

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// scriptedGateway returns a pre-set sequence of replies (one per Complete call),
// repeating the last reply once the script is exhausted. Used to test retries.
type scriptedGateway struct {
	mu      sync.Mutex
	replies []string
	calls   int
	err     error
}

func (g *scriptedGateway) Complete(_ context.Context, _, _ string) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.err != nil {
		return "", g.err
	}
	i := g.calls
	g.calls++
	if i >= len(g.replies) {
		i = len(g.replies) - 1
	}
	return g.replies[i], nil
}

func (g *scriptedGateway) callCount() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.calls
}

func newCodegen(t *testing.T, gw *scriptedGateway) (*CodegenAgent, string) {
	t.Helper()
	dir := t.TempDir()
	return NewCodegenAgent(CodegenConfig{SandboxDir: dir, AIGateway: gw}), dir
}

func TestCodegen_GenerateWritesFiles(t *testing.T) {
	gw := &scriptedGateway{replies: []string{
		`{"files":[{"path":"main.go","content":"package main\nfunc main(){}"},{"path":"go.mod","content":"module x"}]}`,
	}}
	c, dir := newCodegen(t, gw)

	files, err := c.Generate(context.Background(), BuildIntent{Stack: StackChoice{Backend: "go"}}, "hello world")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want 2", len(files))
	}
	// Files must actually exist on disk with the right content + size.
	data, err := os.ReadFile(filepath.Join(dir, "main.go"))
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if string(data) != "package main\nfunc main(){}" {
		t.Errorf("main.go content = %q", data)
	}
	if files[0].Size != len(files[0].Content) {
		t.Errorf("size not filled: %d vs %d", files[0].Size, len(files[0].Content))
	}
}

func TestCodegen_GenerateToleratesFencedJSON(t *testing.T) {
	gw := &scriptedGateway{replies: []string{
		"Here is your code:\n```json\n{\"files\":[{\"path\":\"a.txt\",\"content\":\"hi\"}]}\n```\n",
	}}
	c, _ := newCodegen(t, gw)
	files, err := c.Generate(context.Background(), BuildIntent{}, "")
	if err != nil {
		t.Fatalf("Generate with fenced JSON: %v", err)
	}
	if len(files) != 1 || files[0].Path != "a.txt" {
		t.Errorf("unexpected files: %+v", files)
	}
}

func TestCodegen_Fix(t *testing.T) {
	gw := &scriptedGateway{replies: []string{
		`{"files":[{"path":"main.go","content":"package main\nfunc main(){ fixed }"}]}`,
	}}
	c, dir := newCodegen(t, gw)

	orig := []GeneratedFile{{Path: "main.go", Content: "package main\nfunc main(){ broken }"}}
	fixed, err := c.Fix(context.Background(), orig, "syntax error: broken")
	if err != nil {
		t.Fatalf("Fix: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if string(data) != "package main\nfunc main(){ fixed }" {
		t.Errorf("Fix did not overwrite: %q", data)
	}
	_ = fixed
}

func TestCodegen_InvalidJSONRetriesThenFails(t *testing.T) {
	gw := &scriptedGateway{replies: []string{"not json", "still not json", "nope"}}
	c, _ := newCodegen(t, gw)

	_, err := c.Generate(context.Background(), BuildIntent{}, "")
	if err == nil {
		t.Fatal("expected error after exhausting retries on invalid JSON")
	}
	if gw.callCount() != 3 {
		t.Errorf("call count = %d, want 3 (MaxRetries)", gw.callCount())
	}
}

func TestCodegen_RetriesThenSucceeds(t *testing.T) {
	gw := &scriptedGateway{replies: []string{
		"garbage",
		`{"files":[{"path":"ok.txt","content":"ok"}]}`,
	}}
	c, _ := newCodegen(t, gw)
	files, err := c.Generate(context.Background(), BuildIntent{}, "")
	if err != nil {
		t.Fatalf("Generate should succeed on the 2nd attempt: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("got %d files, want 1", len(files))
	}
	if gw.callCount() != 2 {
		t.Errorf("call count = %d, want 2", gw.callCount())
	}
}

func TestCodegen_RejectsPathEscape(t *testing.T) {
	gw := &scriptedGateway{replies: []string{
		`{"files":[{"path":"../escape.txt","content":"evil"}]}`,
	}}
	c, _ := newCodegen(t, gw)
	if _, err := c.Generate(context.Background(), BuildIntent{}, ""); err == nil {
		t.Error("a path escaping the sandbox must be rejected")
	}
}
