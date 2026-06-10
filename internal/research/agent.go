package research

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Notifier delivers research results. Satisfied by *messaging.Router via an
// adapter (keeps research decoupled from messaging). All methods may be nil-safe
// no-ops when no notifier is wired.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
	NotifyFile(ctx context.Context, filename string, data []byte, caption string) error
}

// searcherIface / fetcherIface / summarizerIface / reporterIface abstract the
// pipeline stages so the agent can be unit-tested with stubs.
type searcherIface interface {
	Search(ctx context.Context, query string, limit int) ([]SearchResult, error)
}
type fetcherIface interface {
	FetchMultiple(ctx context.Context, urls []string) ([]*FetchResult, error)
}
type summarizerIface interface {
	SummarizeMultiple(ctx context.Context, results []*FetchResult, query string) (*Summary, error)
}
type reporterIface interface {
	Generate(ctx context.Context, query string, summary *Summary, sources []SearchResult) (*Report, error)
}

// ResearchAgent orchestrates search → fetch → summarize → report → notify.
//
//nolint:revive // ResearchAgent name is mandated by the M15 spec
type ResearchAgent struct {
	searcher   searcherIface
	fetcher    fetcherIface
	summarizer summarizerIface
	reporter   reporterIface
	notifier   Notifier
}

// NewResearchAgent constructs the agent. notifier may be nil.
func NewResearchAgent(s *Searcher, f *Fetcher, sm *Summarizer, r *Reporter, notifier Notifier) *ResearchAgent {
	return &ResearchAgent{searcher: s, fetcher: f, summarizer: sm, reporter: r, notifier: notifier}
}

// maxFetchPerResearch caps how many top results are fetched.
const maxFetchPerResearch = 8

// Research runs the full pipeline, reporting progress via progressFn.
func (a *ResearchAgent) Research(ctx context.Context, query string, depth int, progressFn func(string)) (*Report, error) {
	if strings.TrimSpace(query) == "" {
		return nil, fmt.Errorf("research: empty query")
	}
	if depth < 1 {
		depth = 1
	}
	emit := func(s string) {
		if progressFn != nil {
			progressFn(s)
		}
	}

	// Step 1 — search.
	emit("🔍 Searching for: " + query)
	results, err := a.searcher.Search(ctx, query, 5*depth)
	if err != nil {
		return nil, fmt.Errorf("research: search: %w", err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("research: no results for %q", query)
	}

	// Step 2 — fetch the top results.
	urls := make([]string, 0, len(results))
	for _, r := range results {
		if len(urls) >= maxFetchPerResearch {
			break
		}
		if r.URL != "" {
			urls = append(urls, r.URL)
		}
	}
	emit(fmt.Sprintf("📄 Reading %d sources...", len(urls)))
	fetched, _ := a.fetcher.FetchMultiple(ctx, urls)

	// Step 3 — summarize.
	emit("🧠 Analyzing and summarizing...")
	summary, err := a.summarizer.SummarizeMultiple(ctx, fetched, query)
	if err != nil {
		return nil, fmt.Errorf("research: summarize: %w", err)
	}

	// Step 4 — generate the report.
	emit("📝 Writing report...")
	report, err := a.reporter.Generate(ctx, query, summary, results)
	if err != nil {
		return nil, fmt.Errorf("research: report: %w", err)
	}

	// Step 5 — notify.
	a.notify(ctx, query, summary, report)
	emit("✅ Research complete")
	return report, nil
}

// notify sends the summary (and the report file) to the notifier if configured.
func (a *ResearchAgent) notify(ctx context.Context, query string, summary *Summary, report *Report) {
	if a.notifier == nil {
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "📊 Research complete: %s\n", query)
	for _, p := range summary.Points {
		fmt.Fprintf(&b, "• %s\n", p)
	}
	if report.FilePath != "" {
		fmt.Fprintf(&b, "Full report saved to: %s", report.FilePath)
	}
	_ = a.notifier.Notify(ctx, "VORTEX Research", b.String())

	if report.FilePath != "" {
		if data, err := os.ReadFile(report.FilePath); err == nil { //nolint:gosec // own report
			_ = a.notifier.NotifyFile(ctx, baseName(report.FilePath), data, "Research report: "+query)
		}
	}
}

// baseName returns the file name portion of a path.
func baseName(p string) string {
	if i := strings.LastIndexAny(p, `/\`); i >= 0 {
		return p[i+1:]
	}
	return p
}
