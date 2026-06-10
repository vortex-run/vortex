package research

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// stubGateway returns a fixed reply (or error) for Complete.
type stubGateway struct {
	reply  string
	err    error
	prompt string // captured user prompt
}

func (g *stubGateway) Complete(_ context.Context, prompt, _ string) (string, error) {
	g.prompt = prompt
	if g.err != nil {
		return "", g.err
	}
	return g.reply, nil
}

func TestSummarize_ReturnsStructuredSummary(t *testing.T) {
	gw := &stubGateway{reply: `{"title":"Go Frameworks","points":["Gin is fast","Echo is minimal"],"text":"A short overview."}`}
	s := NewSummarizer(gw)
	sum, err := s.Summarize(context.Background(), SummaryRequest{Content: "lots of text", Query: "go web frameworks"})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if sum.Title != "Go Frameworks" || len(sum.Points) != 2 || sum.Text != "A short overview." {
		t.Errorf("summary = %+v", sum)
	}
	if sum.Query != "go web frameworks" {
		t.Errorf("query = %q", sum.Query)
	}
}

func TestSummarize_FallsBackToRawText(t *testing.T) {
	gw := &stubGateway{reply: "no json here, just prose"}
	sum, err := NewSummarizer(gw).Summarize(context.Background(), SummaryRequest{Content: "x", Query: "q"})
	if err != nil {
		t.Fatal(err)
	}
	if sum.Text != "no json here, just prose" {
		t.Errorf("fallback text = %q", sum.Text)
	}
	if sum.Title != "q" {
		t.Errorf("title should default to the query, got %q", sum.Title)
	}
}

func TestSummarize_EmptyContentErrors(t *testing.T) {
	if _, err := NewSummarizer(&stubGateway{}).Summarize(context.Background(), SummaryRequest{Content: "  "}); err == nil {
		t.Error("empty content should error")
	}
}

func TestSummarize_NilGatewayErrors(t *testing.T) {
	if _, err := NewSummarizer(nil).Summarize(context.Background(), SummaryRequest{Content: "x"}); err == nil {
		t.Error("nil gateway should error")
	}
}

func TestSummarize_AIErrorPropagates(t *testing.T) {
	gw := &stubGateway{err: fmt.Errorf("provider down")}
	if _, err := NewSummarizer(gw).Summarize(context.Background(), SummaryRequest{Content: "x", Query: "q"}); err == nil {
		t.Error("AI error should propagate")
	}
}

func TestSummarizeMultiple_CombinesSources(t *testing.T) {
	gw := &stubGateway{reply: `{"title":"T","points":["p1"],"text":"combined"}`}
	results := []*FetchResult{
		{URL: "https://a.com", Title: "A", Content: "alpha content"},
		{URL: "https://b.com", Title: "B", Content: "beta content"},
	}
	sum, err := NewSummarizer(gw).SummarizeMultiple(context.Background(), results, "topic")
	if err != nil {
		t.Fatalf("SummarizeMultiple: %v", err)
	}
	if len(sum.Sources) != 2 || sum.Sources[0] != "https://a.com" || sum.Sources[1] != "https://b.com" {
		t.Errorf("sources = %v", sum.Sources)
	}
	// Both sources' content should have been sent to the model.
	for _, want := range []string{"alpha content", "beta content", "https://a.com"} {
		if !contains(gw.prompt, want) {
			t.Errorf("prompt missing %q", want)
		}
	}
}

func TestSummarizeMultiple_EmptyErrors(t *testing.T) {
	if _, err := NewSummarizer(&stubGateway{}).SummarizeMultiple(context.Background(), nil, "q"); err == nil {
		t.Error("no results should error")
	}
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }
