package forge

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeFakeAPK writes a >1MB zip with an AndroidManifest.xml entry to dir/app.apk
// and returns the artifact directory.
func makeFakeAPK(t *testing.T, withManifest bool, big bool) string {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if withManifest {
		w, _ := zw.Create("AndroidManifest.xml")
		_, _ = w.Write([]byte("<manifest/>"))
	}
	if big {
		// Pad with a large STORED (uncompressed) entry of random-ish bytes so
		// the apk actually exceeds 1MB on disk.
		fh := &zip.FileHeader{Name: "classes.dex", Method: zip.Store}
		w, _ := zw.CreateHeader(fh)
		pad := make([]byte, 1<<21) // 2MB
		for i := range pad {
			pad[i] = byte(i * 31)
		}
		_, _ = w.Write(pad)
	}
	_ = zw.Close()
	if err := os.WriteFile(filepath.Join(dir, "app.apk"), buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestQA_PassesValidBinary(t *testing.T) {
	sandbox := t.TempDir()
	writeFile(t, sandbox, "main.go", "package main\nfunc main(){}")
	binDir := filepath.Join(sandbox, "bin")
	writeFile(t, binDir, "app", "ELF...")

	q := NewQAAgent(QAConfig{SandboxDir: sandbox})
	res, err := q.Run(context.Background(), BuildOutput{ArtifactType: "binary", ArtifactPath: binDir})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Passed {
		t.Errorf("valid binary should pass QA: %+v", res.Checks)
	}
}

func TestQA_PassesValidAPK(t *testing.T) {
	apkDir := makeFakeAPK(t, true, true)
	q := NewQAAgent(QAConfig{SandboxDir: t.TempDir()})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "apk", ArtifactPath: apkDir})
	if !res.Passed {
		t.Errorf("valid apk should pass QA: %+v", res.Checks)
	}
}

func TestQA_FailsEmptyAPK(t *testing.T) {
	// Small apk without manifest.
	apkDir := makeFakeAPK(t, false, false)
	q := NewQAAgent(QAConfig{SandboxDir: t.TempDir()})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "apk", ArtifactPath: apkDir})
	if res.Passed {
		t.Error("undersized apk without manifest should fail QA")
	}
}

func TestQA_FailsMissingArtifact(t *testing.T) {
	q := NewQAAgent(QAConfig{SandboxDir: t.TempDir()})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "apk", ArtifactPath: t.TempDir()})
	if res.Passed {
		t.Error("missing apk file should fail QA")
	}
}

func TestQA_PassesWebDist(t *testing.T) {
	sandbox := t.TempDir()
	dist := filepath.Join(sandbox, "dist")
	writeFile(t, dist, "index.html", "<html></html>")
	writeFile(t, dist, "app.js", "console.log(1)")

	q := NewQAAgent(QAConfig{SandboxDir: sandbox})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "web-dist", ArtifactPath: dist})
	if !res.Passed {
		t.Errorf("valid web dist should pass QA: %+v", res.Checks)
	}
}

func TestQA_FailsWebDistMissingIndex(t *testing.T) {
	sandbox := t.TempDir()
	dist := filepath.Join(sandbox, "dist")
	writeFile(t, dist, "app.js", "console.log(1)")

	q := NewQAAgent(QAConfig{SandboxDir: sandbox})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "web-dist", ArtifactPath: dist})
	if res.Passed {
		t.Error("web dist without index.html should fail QA")
	}
}

func TestQA_WarnsOnHardcodedSecret(t *testing.T) {
	sandbox := t.TempDir()
	writeFile(t, sandbox, "config.py", `API_KEY = "sk-1234567890abcdef"`)
	binDir := filepath.Join(sandbox, "bin")
	writeFile(t, binDir, "app", "binary")

	q := NewQAAgent(QAConfig{SandboxDir: sandbox})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "binary", ArtifactPath: binDir})
	if len(res.Warnings) == 0 {
		t.Error("expected a warning for the hardcoded API key")
	}
	// A warning must NOT fail the gate.
	if !res.Passed {
		t.Error("a hardcoded-secret warning should not fail QA (binary is otherwise valid)")
	}
}

func TestQA_AllChecksRun(t *testing.T) {
	// Even when the artifact check fails, the security scan still runs.
	sandbox := t.TempDir()
	writeFile(t, sandbox, "main.go", `var token = "supersecretvalue123"`)

	q := NewQAAgent(QAConfig{SandboxDir: sandbox})
	res, _ := q.Run(context.Background(), BuildOutput{ArtifactType: "apk", ArtifactPath: t.TempDir()})
	if res.Passed {
		t.Error("missing apk should fail")
	}
	if len(res.Warnings) == 0 {
		t.Error("security scan should still run and warn even though apk check failed")
	}
	if !strings.Contains(strings.Join(res.Warnings, " "), "main.go") {
		t.Errorf("warning should reference the offending file: %v", res.Warnings)
	}
}
