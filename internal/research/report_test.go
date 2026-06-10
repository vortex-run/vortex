package research

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sampleSummary() *Summary {
	return &Summary{
		Title:  "Go Web Frameworks",
		Points: []string{"Gin is fast", "Echo is minimal"},
		Text:   "An overview of Go web frameworks.",
	}
}

func sampleSources() []SearchResult {
	return []SearchResult{
		{Title: "Gin", URL: "https://gin-gonic.com", Snippet: "Fast framework"},
		{Title: "Echo", URL: "https://echo.labstack.com", Snippet: "Minimal framework"},
	}
}

func TestGenerate_CreatesMarkdownFile(t *testing.T) {
	dir := t.TempDir()
	r := NewReporter(dir)
	report, err := r.Generate(context.Background(), "go web frameworks", sampleSummary(), sampleSources())
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// File saved under workDir/research with a .md slug name.
	if filepath.Dir(report.FilePath) != filepath.Join(dir, "research") {
		t.Errorf("file path = %q, want under research/", report.FilePath)
	}
	if !strings.HasSuffix(report.FilePath, ".md") || !strings.Contains(filepath.Base(report.FilePath), "go-web-frameworks") {
		t.Errorf("file name = %q", report.FilePath)
	}
	if _, err := os.Stat(report.FilePath); err != nil {
		t.Fatalf("report file not written: %v", err)
	}
}

func TestGenerate_ContainsQuerySummarySources(t *testing.T) {
	dir := t.TempDir()
	report, err := NewReporter(dir).Generate(context.Background(), "go web frameworks", sampleSummary(), sampleSources())
	if err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(report.FilePath)
	md := string(data)
	for _, want := range []string{
		"# Research Report: go web frameworks",
		"## Summary",
		"An overview of Go web frameworks.",
		"## Key Findings",
		"- Gin is fast",
		"## Sources",
		"[Gin](https://gin-gonic.com)",
		"Minimal framework",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report missing %q:\n%s", want, md)
		}
	}
}

func TestGenerate_EmptyQueryErrors(t *testing.T) {
	if _, err := NewReporter(t.TempDir()).Generate(context.Background(), "  ", nil, nil); err == nil {
		t.Error("empty query should error")
	}
}

func TestList_ReturnsSavedReports(t *testing.T) {
	dir := t.TempDir()
	r := NewReporter(dir)
	if _, err := r.Generate(context.Background(), "alpha topic", sampleSummary(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Generate(context.Background(), "beta topic", sampleSummary(), nil); err != nil {
		t.Fatal(err)
	}
	reports, err := r.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(reports) != 2 {
		t.Fatalf("List returned %d reports, want 2", len(reports))
	}
}

func TestList_EmptyDirNoError(t *testing.T) {
	reports, err := NewReporter(t.TempDir()).List()
	if err != nil {
		t.Errorf("List on a fresh dir should not error: %v", err)
	}
	if len(reports) != 0 {
		t.Errorf("expected no reports, got %d", len(reports))
	}
}

func TestGet_ReadsReport(t *testing.T) {
	dir := t.TempDir()
	r := NewReporter(dir)
	report, _ := r.Generate(context.Background(), "topic", sampleSummary(), nil)
	got, err := r.Get(filepath.Base(report.FilePath))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !strings.Contains(got.Summary.Text, "# Research Report") {
		t.Errorf("Get content = %q", got.Summary.Text)
	}
}

func TestGet_RejectsPathTraversal(t *testing.T) {
	if _, err := NewReporter(t.TempDir()).Get("../../etc/passwd"); err == nil {
		t.Error("path traversal should be rejected")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"Go Web Frameworks!": "go-web-frameworks",
		"  spaces  ":         "spaces",
		"@@@":                "report",
		"a/b\\c":             "a-b-c",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("slugify(%q) = %q, want %q", in, got, want)
		}
	}
}
