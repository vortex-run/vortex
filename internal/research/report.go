package research

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Report is a generated research report.
type Report struct {
	Title    string         `json:"title"`
	Query    string         `json:"query"`
	Summary  *Summary       `json:"summary"`
	Sources  []SearchResult `json:"sources"`
	SavedAt  time.Time      `json:"saved_at"`
	FilePath string         `json:"file_path"`
}

// Reporter generates and persists markdown research reports.
type Reporter struct {
	dir string // workDir/research
}

// NewReporter constructs a reporter that saves under workDir/research.
func NewReporter(workDir string) *Reporter {
	return &Reporter{dir: filepath.Join(workDir, "research")}
}

// Generate writes a markdown report and returns it.
func (r *Reporter) Generate(_ context.Context, query string, summary *Summary, sources []SearchResult) (*Report, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("research: empty query")
	}
	now := time.Now()
	title := query
	if summary != nil && summary.Title != "" {
		title = summary.Title
	}

	report := &Report{Title: title, Query: query, Summary: summary, Sources: sources, SavedAt: now}
	md := renderMarkdown(report)

	if err := os.MkdirAll(r.dir, 0o755); err != nil { //nolint:gosec // user work dir
		return nil, err
	}
	name := fmt.Sprintf("%s-%s.md", slugify(query), now.Format("20060102-150405"))
	path := filepath.Join(r.dir, name)
	if err := os.WriteFile(path, []byte(md), 0o644); err != nil { //nolint:gosec // user report
		return nil, err
	}
	report.FilePath = path
	return report, nil
}

// List returns saved reports (filename + path + mod time as SavedAt).
func (r *Reporter) List() ([]Report, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Report
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		info, ierr := e.Info()
		saved := time.Time{}
		if ierr == nil {
			saved = info.ModTime()
		}
		out = append(out, Report{
			Title:    strings.TrimSuffix(e.Name(), ".md"),
			FilePath: filepath.Join(r.dir, e.Name()),
			SavedAt:  saved,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SavedAt.After(out[j].SavedAt) })
	return out, nil
}

// Get reads a saved report's markdown by filename.
func (r *Reporter) Get(filename string) (*Report, error) {
	// Reject path traversal — only a bare filename under the research dir.
	if filename != filepath.Base(filename) {
		return nil, fmt.Errorf("research: invalid report name")
	}
	path := filepath.Join(r.dir, filename)
	data, err := os.ReadFile(path) //nolint:gosec // confined to research dir
	if err != nil {
		return nil, err
	}
	return &Report{
		Title:    strings.TrimSuffix(filename, ".md"),
		FilePath: path,
		Summary:  &Summary{Text: string(data)},
	}, nil
}

// renderMarkdown renders the report as markdown.
func renderMarkdown(r *Report) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Research Report: %s\n\n", r.Query)
	fmt.Fprintf(&b, "Generated: %s\n\n", r.SavedAt.Format(time.RFC3339))

	b.WriteString("## Summary\n\n")
	if r.Summary != nil && r.Summary.Text != "" {
		b.WriteString(r.Summary.Text + "\n\n")
	}

	if r.Summary != nil && len(r.Summary.Points) > 0 {
		b.WriteString("## Key Findings\n\n")
		for _, p := range r.Summary.Points {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}

	if len(r.Sources) > 0 {
		b.WriteString("## Sources\n\n")
		for i, s := range r.Sources {
			fmt.Fprintf(&b, "%d. [%s](%s)", i+1, s.Title, s.URL)
			if s.Snippet != "" {
				fmt.Fprintf(&b, " — %s", s.Snippet)
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	return b.String()
}

// slugRe matches non-slug characters.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify converts a query into a filename-safe slug.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 60 {
		s = s[:60]
	}
	if s == "" {
		s = "report"
	}
	return s
}
