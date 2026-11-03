package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWithNotices_CollectsLicenseAndNotice(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "LICENSE"), "MIT text")
	writeFile(t, filepath.Join(dir, "NOTICE.txt"), "notice text")
	writeFile(t, filepath.Join(dir, "main.go"), "package x")
	d, err := withNotices(dep{Kind: "go", Name: "example.com/x", Version: "v1.0.0"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 2 {
		t.Fatalf("files = %v, want LICENSE and NOTICE.txt", d.Files)
	}
}

func TestWithNotices_MissingLicenseFails(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "no license here")
	if _, err := withNotices(dep{Kind: "go", Name: "example.com/unlisted", Version: "v1"}, dir); err == nil {
		t.Fatal("a dependency without a license file must fail the release")
	}
}

func TestWithNotices_KnownLicenselessUsesReadme(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "## License\n\nMIT")
	d, err := withNotices(dep{Kind: "go", Name: "github.com/mattn/go-localereader", Version: "v0.0.1"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Files) != 1 || !strings.HasSuffix(d.Files[0], "README.md") || d.Note == "" {
		t.Fatalf("got %+v, want README with a declared-license note", d)
	}
}

func TestParseGoList_Dedups(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "LICENSE"), "BSD")
	raw := "example.com/a\tv1.0.0\t" + dir + "\r\nexample.com/a\tv1.0.0\t" + dir + "\n\n"
	deps, err := parseGoList([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 {
		t.Fatalf("deps = %d, want 1 after dedup", len(deps))
	}
}

func TestNpmDependencies_SkipsDevPackages(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "package-lock.json"), `{"packages":{
		"": {"name":"dashboard"},
		"node_modules/react": {"version":"19.0.0","license":"MIT"},
		"node_modules/@scope/pkg": {"version":"1.2.3","license":"ISC"},
		"node_modules/vite": {"version":"8.0.0","license":"MIT","dev":true}
	}}`)
	writeFile(t, filepath.Join(dir, "node_modules", "react", "LICENSE"), "react MIT")
	writeFile(t, filepath.Join(dir, "node_modules", "@scope", "pkg", "LICENSE.md"), "ISC")
	deps, err := npmDependencies(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, d := range deps {
		names[d.Name] = true
	}
	if !names["react"] || !names["@scope/pkg"] || names["vite"] {
		t.Fatalf("npm deps = %v, want react and @scope/pkg but not dev-only vite", names)
	}
}

func TestRender_IsSortedAndIncludesText(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "LICENSE"), "Copyright (c) Someone\r\nMIT")
	deps := []dep{
		{Kind: "npm", Name: "zed", Version: "1", Files: []string{filepath.Join(dir, "LICENSE")}},
		{Kind: "go", Name: "b.example/b", Version: "v2", Files: []string{filepath.Join(dir, "LICENSE")}},
		{Kind: "go", Name: "a.example/a", Version: "v1", Files: []string{filepath.Join(dir, "LICENSE")}},
	}
	var buf bytes.Buffer
	if err := render(&buf, deps); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	a, b, z := strings.Index(out, "go a.example/a"), strings.Index(out, "go b.example/b"), strings.Index(out, "npm zed")
	if a < 0 || b < 0 || z < 0 || a > b || b > z {
		t.Fatalf("entries missing or unsorted:\n%s", out)
	}
	if !strings.Contains(out, "Copyright (c) Someone\nMIT") {
		t.Error("license text missing or CRLF not normalized")
	}
}

func TestWithNotices_KnownLicenselessIncludesVendoredLicenses(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "README.md"), "MIT")
	writeFile(t, filepath.Join(dir, "lib-vendor", "d3-array", "LICENSE"), "ISC d3-array")
	writeFile(t, filepath.Join(dir, "node_modules", "x", "LICENSE"), "must be skipped")
	d, err := withNotices(dep{Kind: "npm", Name: "victory-vendor", Version: "37"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	var vendored, nested bool
	for _, f := range d.Files {
		vendored = vendored || strings.HasSuffix(filepath.ToSlash(f), "d3-array/LICENSE")
		nested = nested || strings.Contains(filepath.ToSlash(f), "node_modules")
	}
	if !vendored || nested {
		t.Fatalf("files = %v, want the vendored d3-array LICENSE and nothing from node_modules", d.Files)
	}
}
