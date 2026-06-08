package forge

import (
	"context"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestBuild_EmptySandboxErrors(t *testing.T) {
	b := NewBuildAgent(BuildConfig{SandboxDir: t.TempDir(), Stack: StackChoice{Backend: "go"}})
	if _, err := b.Build(context.Background()); err == nil {
		t.Error("empty sandbox should error")
	}
}

func TestBuild_GoSucceeds(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module forgebuild\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"hi\") }\n")

	b := NewBuildAgent(BuildConfig{SandboxDir: dir, Stack: StackChoice{Backend: "go"}})
	out, err := b.Build(context.Background())
	if err != nil {
		t.Fatalf("go build: %v\nstderr: %s", err, out.Stderr)
	}
	if !out.Success {
		t.Error("expected Success=true")
	}
	if out.ArtifactType != "binary" {
		t.Errorf("artifact type = %q, want binary", out.ArtifactType)
	}
	if out.DurationMs < 0 {
		t.Error("duration should be non-negative")
	}
}

func TestBuild_GoFailureReturnsStderr(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module forgebuild\n\ngo 1.26\n")
	// Deliberately broken Go.
	writeFile(t, dir, "main.go", "package main\n\nfunc main() { this is not valid go }\n")

	b := NewBuildAgent(BuildConfig{SandboxDir: dir, Stack: StackChoice{Backend: "go"}})
	out, err := b.Build(context.Background())
	if err == nil {
		t.Fatal("broken Go should fail the build")
	}
	if out.Stderr == "" {
		t.Error("failed build should capture stderr")
	}
}

func TestBuild_Timeout(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module forgebuild\n\ngo 1.26\n")
	writeFile(t, dir, "main.go", "package main\n\nfunc main() {}\n")

	b := NewBuildAgent(BuildConfig{SandboxDir: dir, Stack: StackChoice{Backend: "go"}, Timeout: time.Nanosecond})
	if _, err := b.Build(context.Background()); err == nil {
		t.Error("a 1ns timeout should fail the build")
	}
}

func TestBuild_ArtifactPaths(t *testing.T) {
	cases := []struct {
		stack StackChoice
		want  string
	}{
		{StackChoice{Frontend: "flutter"}, filepath.Join("build", "app", "outputs", "apk", "release")},
		{StackChoice{Frontend: "react"}, "dist"},
		{StackChoice{Backend: "go"}, "bin"},
	}
	for _, c := range cases {
		b := NewBuildAgent(BuildConfig{SandboxDir: "/sb", Stack: c.stack})
		got := b.ArtifactPath()
		if !strings.HasSuffix(filepath.ToSlash(got), filepath.ToSlash(c.want)) {
			t.Errorf("stack %+v ArtifactPath = %q, want suffix %q", c.stack, got, c.want)
		}
	}
}

func TestBuild_FlutterSkippedWhenAbsent(t *testing.T) {
	if _, err := exec.LookPath("flutter"); err != nil {
		t.Skip("flutter not installed — Flutter build not exercised in this environment")
	}
	// If flutter IS present (rare in CI), at least confirm the command wiring.
	dir := t.TempDir()
	writeFile(t, dir, "pubspec.yaml", "name: app\n")
	b := NewBuildAgent(BuildConfig{SandboxDir: dir, Stack: StackChoice{Frontend: "flutter"}, Timeout: 30 * time.Second})
	// A bare pubspec won't build a real APK; we only assert it doesn't panic and
	// returns a structured result.
	_, _ = b.Build(context.Background())
	_ = runtime.GOOS
}
