package research

import (
	"context"
	"fmt"
	"sync"
	"testing"
)

// --- stage stubs ------------------------------------------------------------

type stubSearcher struct {
	results []SearchResult
	err     error
}

func (s stubSearcher) Search(context.Context, string, int) ([]SearchResult, error) {
	return s.results, s.err
}

type stubFetcher struct{ results []*FetchResult }

func (f stubFetcher) FetchMultiple(_ context.Context, urls []string) ([]*FetchResult, error) {
	if f.results != nil {
		return f.results, nil
	}
	out := make([]*FetchResult, len(urls))
	for i, u := range urls {
		out[i] = &FetchResult{URL: u, Content: "content of " + u}
	}
	return out, nil
}

type stubSummarizer struct {
	summary *Summary
	err     error
}

func (s stubSummarizer) SummarizeMultiple(_ context.Context, _ []*FetchResult, query string) (*Summary, error) {
	if s.err != nil {
		return nil, s.err
	}
	if s.summary != nil {
		return s.summary, nil
	}
	return &Summary{Title: "T", Points: []string{"point one"}, Text: "summary", Query: query}, nil
}

// recReporter records the Generate call and returns a fixed report.
type recReporter struct{ generated bool }

func (r *recReporter) Generate(_ context.Context, query string, summary *Summary, _ []SearchResult) (*Report, error) {
	r.generated = true
	return &Report{Title: summary.Title, Query: query, Summary: summary}, nil
}

// recNotifier records notifications.
type recNotifier struct {
	mu        sync.Mutex
	notified  bool
	fileSent  bool
	lastTitle string
}

func (n *recNotifier) Notify(_ context.Context, title, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = true
	n.lastTitle = title
	return nil
}
func (n *recNotifier) NotifyFile(context.Context, string, []byte, string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fileSent = true
	return nil
}

func newResearchAgentWith(s searcherIface, f fetcherIface, sm summarizerIface, r reporterIface, n Notifier) *ResearchAgent {
	return &ResearchAgent{searcher: s, fetcher: f, summarizer: sm, reporter: r, notifier: n}
}

func sampleResults() []SearchResult {
	return []SearchResult{
		{Title: "A", URL: "https://a.com"},
		{Title: "B", URL: "https://b.com"},
	}
}

// --- tests ------------------------------------------------------------------

func TestResearch_PipelineExecutesAllSteps(t *testing.T) {
	rep := &recReporter{}
	notif := &recNotifier{}
	agent := newResearchAgentWith(
		stubSearcher{results: sampleResults()}, stubFetcher{}, stubSummarizer{}, rep, notif)

	var steps []string
	report, err := agent.Research(context.Background(), "go frameworks", 1, func(s string) {
		steps = append(steps, s)
	})
	if err != nil {
		t.Fatalf("Research: %v", err)
	}
	if report == nil || report.Query != "go frameworks" {
		t.Errorf("report = %+v", report)
	}
	if !rep.generated {
		t.Error("report should have been generated")
	}
	// Progress steps in order.
	joined := fmt.Sprintf("%v", steps)
	for _, want := range []string{"Searching", "Reading", "Analyzing", "Writing report", "complete"} {
		if !contains(joined, want) {
			t.Errorf("progress missing %q: %s", want, joined)
		}
	}
}

func TestResearch_NotifiesOnCompletion(t *testing.T) {
	notif := &recNotifier{}
	agent := newResearchAgentWith(
		stubSearcher{results: sampleResults()}, stubFetcher{}, stubSummarizer{}, &recReporter{}, notif)
	if _, err := agent.Research(context.Background(), "topic", 1, nil); err != nil {
		t.Fatal(err)
	}
	notif.mu.Lock()
	defer notif.mu.Unlock()
	if !notif.notified || notif.lastTitle != "VORTEX Research" {
		t.Errorf("expected a research notification, got %+v", notif)
	}
}

func TestResearch_EmptyQueryErrors(t *testing.T) {
	agent := newResearchAgentWith(stubSearcher{}, stubFetcher{}, stubSummarizer{}, &recReporter{}, nil)
	if _, err := agent.Research(context.Background(), "  ", 1, nil); err == nil {
		t.Error("empty query should error")
	}
}

func TestResearch_SearchFailureErrors(t *testing.T) {
	agent := newResearchAgentWith(
		stubSearcher{err: fmt.Errorf("ddg down")}, stubFetcher{}, stubSummarizer{}, &recReporter{}, nil)
	if _, err := agent.Research(context.Background(), "q", 1, nil); err == nil {
		t.Error("search failure should propagate")
	}
}

func TestResearch_NoResultsErrors(t *testing.T) {
	agent := newResearchAgentWith(
		stubSearcher{results: nil}, stubFetcher{}, stubSummarizer{}, &recReporter{}, nil)
	if _, err := agent.Research(context.Background(), "q", 1, nil); err == nil {
		t.Error("no results should error")
	}
}

func TestResearch_SummarizeFailureErrors(t *testing.T) {
	agent := newResearchAgentWith(
		stubSearcher{results: sampleResults()}, stubFetcher{},
		stubSummarizer{err: fmt.Errorf("ai down")}, &recReporter{}, nil)
	if _, err := agent.Research(context.Background(), "q", 1, nil); err == nil {
		t.Error("summarize failure should propagate")
	}
}

func TestNewResearchAgent_WiresConcreteStages(t *testing.T) {
	// The exported constructor accepts the concrete types and they satisfy the
	// stage interfaces.
	a := NewResearchAgent(NewSearcher(), NewFetcher(), NewSummarizer(nil), NewReporter(t.TempDir()), nil)
	if a == nil {
		t.Fatal("NewResearchAgent returned nil")
	}
}
