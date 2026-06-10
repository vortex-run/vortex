package research

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/vortex-run/vortex/internal/agents"
)

// summarizerSystemPrompt instructs the model to extract query-relevant facts.
const summarizerSystemPrompt = `You are a research summarizer. Extract the key facts relevant to the query. Be concise and accurate. Respond with ONLY a JSON object of the form:
{"title":"short title","points":["fact 1","fact 2"],"text":"a short paragraph summary"}
Return only the JSON, no prose.`

// maxSummaryInputWords caps the content sent to the model.
const maxSummaryInputWords = 6000

// SummaryRequest configures a summarization.
type SummaryRequest struct {
	Content  string // text to summarize
	Query    string // original search query
	MaxWords int    // default 300
	Format   string // "bullets"|"paragraph"|"report"
}

// Summary is the structured summarization result.
type Summary struct {
	Title   string   `json:"title"`
	Points  []string `json:"points"`
	Text    string   `json:"text"`
	Sources []string `json:"sources"`
	Query   string   `json:"query"`
}

// Summarizer summarizes content using the AI gateway.
type Summarizer struct {
	gateway agents.AIGateway
}

// NewSummarizer constructs a summarizer over an AI gateway.
func NewSummarizer(gateway agents.AIGateway) *Summarizer {
	return &Summarizer{gateway: gateway}
}

// Summarize produces a Summary for the request content + query.
func (s *Summarizer) Summarize(ctx context.Context, req SummaryRequest) (*Summary, error) {
	if strings.TrimSpace(req.Content) == "" {
		return nil, fmt.Errorf("research: empty content to summarize")
	}
	if s.gateway == nil {
		return nil, fmt.Errorf("research: no AI gateway configured")
	}

	content := req.Content
	if words := strings.Fields(content); len(words) > maxSummaryInputWords {
		content = strings.Join(words[:maxSummaryInputWords], " ")
	}
	prompt := fmt.Sprintf("Query: %s\n\nContent:\n%s", req.Query, content)

	reply, err := s.gateway.Complete(ctx, prompt, summarizerSystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("research: summarize: %w", err)
	}

	summary := parseSummary(reply)
	summary.Query = req.Query
	if summary.Title == "" {
		summary.Title = req.Query
	}
	return summary, nil
}

// SummarizeMultiple combines several fetch results into one summary, carrying
// all source URLs.
func (s *Summarizer) SummarizeMultiple(ctx context.Context, results []*FetchResult, query string) (*Summary, error) {
	if len(results) == 0 {
		return nil, fmt.Errorf("research: no content to summarize")
	}
	var b strings.Builder
	sources := make([]string, 0, len(results))
	for _, r := range results {
		if r == nil {
			continue
		}
		sources = append(sources, r.URL)
		fmt.Fprintf(&b, "Source: %s\nTitle: %s\n%s\n\n", r.URL, r.Title, r.Content)
	}

	summary, err := s.Summarize(ctx, SummaryRequest{Content: b.String(), Query: query})
	if err != nil {
		return nil, err
	}
	summary.Sources = sources
	return summary, nil
}

// parseSummary extracts a Summary from the model reply, tolerating prose around
// the JSON; falls back to using the whole reply as the text.
func parseSummary(reply string) *Summary {
	var out Summary
	jsonStr := extractJSONObject(reply)
	if jsonStr != "" {
		if err := json.Unmarshal([]byte(jsonStr), &out); err == nil {
			return &out
		}
	}
	// No parseable JSON — use the raw reply as the paragraph text.
	out.Text = strings.TrimSpace(reply)
	return &out
}

// extractJSONObject returns the first {...} object in s (or "").
func extractJSONObject(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return ""
}
