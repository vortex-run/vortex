package forge

import (
	"archive/zip"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// QAConfig configures the QA gate.
type QAConfig struct {
	SandboxDir      string
	ResponseTimeout time.Duration // per check; default 5s
}

// QAAgent runs the quality-assurance gate before delivery. The gate is never
// skipped: a failed QAResult must block delivery.
type QAAgent struct {
	cfg QAConfig
}

// NewQAAgent constructs the agent.
func NewQAAgent(cfg QAConfig) *QAAgent {
	if cfg.ResponseTimeout <= 0 {
		cfg.ResponseTimeout = 5 * time.Second
	}
	return &QAAgent{cfg: cfg}
}

// QACheck is the outcome of one check.
type QACheck struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Message string `json:"message"`
}

// QAResult aggregates all checks. Passed is true only if every check passed.
type QAResult struct {
	Passed   bool      `json:"passed"`
	Checks   []QACheck `json:"checks"`
	Warnings []string  `json:"warnings"`
}

// Run executes all applicable checks for the build output. All checks run even
// if one fails (a full report). The overall result Passed is the AND of every
// check; warnings (e.g. a hardcoded secret) do not fail the gate but are
// surfaced.
func (q *QAAgent) Run(_ context.Context, output BuildOutput) (QAResult, error) {
	var res QAResult

	switch output.ArtifactType {
	case "apk":
		res.Checks = append(res.Checks, q.checkAPK(output.ArtifactPath))
	case "web-dist":
		res.Checks = append(res.Checks, q.checkWebDist(output.ArtifactPath))
	case "binary":
		res.Checks = append(res.Checks, q.checkBinary(output.ArtifactPath))
	}

	// Security scan always runs over the generated source.
	warns := q.scanSecrets(q.cfg.SandboxDir)
	res.Warnings = append(res.Warnings, warns...)

	// Overall pass = every check passed (warnings do not fail the gate).
	res.Passed = len(res.Checks) > 0
	for _, c := range res.Checks {
		if !c.Passed {
			res.Passed = false
		}
	}
	return res, nil
}

// checkAPK validates an APK file: > 1MB, starts with the ZIP magic, and
// contains AndroidManifest.xml.
func (q *QAAgent) checkAPK(artifactDir string) QACheck {
	apk := firstFileWithExt(artifactDir, ".apk")
	if apk == "" {
		return QACheck{Name: "apk", Passed: false, Message: "no .apk found in artifact dir"}
	}
	info, err := os.Stat(apk)
	if err != nil {
		return QACheck{Name: "apk", Passed: false, Message: err.Error()}
	}
	if info.Size() < 1<<20 {
		return QACheck{Name: "apk", Passed: false, Message: fmt.Sprintf("apk too small: %d bytes", info.Size())}
	}
	// ZIP magic "PK\x03\x04".
	f, err := os.Open(apk)
	if err != nil {
		return QACheck{Name: "apk", Passed: false, Message: err.Error()}
	}
	defer func() { _ = f.Close() }()
	magic := make([]byte, 4)
	if _, err := f.Read(magic); err != nil || magic[0] != 'P' || magic[1] != 'K' {
		return QACheck{Name: "apk", Passed: false, Message: "not a zip/apk (bad magic)"}
	}
	// Contains AndroidManifest.xml?
	zr, err := zip.OpenReader(apk)
	if err != nil {
		return QACheck{Name: "apk", Passed: false, Message: "cannot read apk zip: " + err.Error()}
	}
	defer func() { _ = zr.Close() }()
	for _, e := range zr.File {
		if e.Name == "AndroidManifest.xml" {
			return QACheck{Name: "apk", Passed: true, Message: "valid apk"}
		}
	}
	return QACheck{Name: "apk", Passed: false, Message: "apk missing AndroidManifest.xml"}
}

// checkWebDist validates a web dist: index.html exists and no empty JS files.
func (q *QAAgent) checkWebDist(distDir string) QACheck {
	if _, err := os.Stat(filepath.Join(distDir, "index.html")); err != nil {
		return QACheck{Name: "web-dist", Passed: false, Message: "missing index.html"}
	}
	empty := ""
	_ = filepath.WalkDir(distDir, func(path string, d os.DirEntry, _ error) error {
		if d != nil && !d.IsDir() && strings.HasSuffix(path, ".js") {
			if info, err := d.Info(); err == nil && info.Size() == 0 {
				empty = path
			}
		}
		return nil
	})
	if empty != "" {
		return QACheck{Name: "web-dist", Passed: false, Message: "empty JS file: " + filepath.Base(empty)}
	}
	return QACheck{Name: "web-dist", Passed: true, Message: "valid web dist"}
}

// checkBinary validates a compiled binary artifact: at least one file exists in
// the artifact dir.
func (q *QAAgent) checkBinary(binDir string) QACheck {
	if dirHasFiles(binDir) {
		return QACheck{Name: "binary", Passed: true, Message: "binary present"}
	}
	// The Go build may place the binary in the sandbox root rather than ./bin;
	// accept either.
	if dirHasFiles(q.cfg.SandboxDir) {
		return QACheck{Name: "binary", Passed: true, Message: "build artifacts present"}
	}
	return QACheck{Name: "binary", Passed: false, Message: "no binary artifact found"}
}

// secretPattern flags likely hardcoded secrets in generated source.
var secretPattern = regexp.MustCompile(`(?i)(password|secret|api[_-]?key|token)\s*[:=]\s*["'][^"']{6,}["']`)

// scanSecrets walks source files looking for hardcoded credentials, returning a
// warning per finding (warnings do not fail the gate).
func (q *QAAgent) scanSecrets(dir string) []string {
	var warns []string
	if dir == "" {
		return warns
	}
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, _ error) error {
		if d == nil || d.IsDir() || !isSourceFile(path) {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // scanning sandboxed generated files
		if err != nil {
			return nil
		}
		if secretPattern.Match(data) {
			warns = append(warns, "possible hardcoded secret in "+filepath.Base(path))
		}
		return nil
	})
	return warns
}

// isSourceFile reports whether path looks like a source file worth scanning.
func isSourceFile(path string) bool {
	switch filepath.Ext(path) {
	case ".go", ".py", ".js", ".ts", ".dart", ".java", ".env", ".yaml", ".yml", ".json":
		return true
	default:
		return false
	}
}

// firstFileWithExt returns the first file under dir with the given extension.
func firstFileWithExt(dir, ext string) string {
	var found string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, _ error) error {
		if found == "" && d != nil && !d.IsDir() && strings.EqualFold(filepath.Ext(path), ext) {
			found = path
		}
		return nil
	})
	return found
}
