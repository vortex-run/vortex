package specialists

import (
	"context"
	"fmt"
	"strings"

	"github.com/vortex-run/vortex/internal/a2a"
)

// ReviewAgent reviews code quality and returns a 0-10 score with an approval
// decision. A score >= 7 is approved.
type ReviewAgent struct {
	*BaseAgent
}

// reviewApprovalThreshold is the minimum score that approves the work.
const reviewApprovalThreshold = 7

// reviewSystemPrompt is the Review Agent's focused role definition.
const reviewSystemPrompt = `You are a senior code reviewer in VORTEX.
Your ONLY job is to review code quality.

Review every file for:
1. Correctness — does it do what it should?
2. Security — SQL injection, path traversal, etc
3. Performance — obvious bottlenecks
4. Maintainability — readable, documented
5. Test coverage — edge cases covered

Return ONLY valid JSON, nothing else:
{
  "score": 8,
  "approved": true,
  "issues": [{"severity": "low", "file": "main.py", "line": 45, "message": "missing docstring"}],
  "security_issues": [],
  "summary": "Clean implementation, well tested."
}

Score >= 7 means approved. Score < 7 means must fix before shipping.`

// NewReviewAgent constructs a ReviewAgent over a BaseAgent.
func NewReviewAgent(base *BaseAgent) *ReviewAgent {
	base.card.Role = "reviewer"
	if base.card.Capabilities == nil {
		base.card.Capabilities = []string{"code_review", "security_check", "quality_score", "approve"}
	}
	base.SetSystemPrompt(reviewSystemPrompt)
	return &ReviewAgent{BaseAgent: base}
}

// ReviewIssue is one finding from a review.
type ReviewIssue struct {
	Severity string `json:"severity"` // low|medium|high|critical
	File     string `json:"file"`
	Line     int    `json:"line"`
	Message  string `json:"message"`
}

// ReviewResult is the parsed review.
type ReviewResult struct {
	Score          int           `json:"score"`
	Approved       bool          `json:"approved"`
	Issues         []ReviewIssue `json:"issues"`
	SecurityIssues []ReviewIssue `json:"security_issues"`
	Summary        string        `json:"summary"`
}

// HandleTask implements the a2a.Agent contract for review work.
func (a *ReviewAgent) HandleTask(ctx context.Context, task a2a.Task, progressFn func(a2a.Progress)) a2a.TaskResult {
	result := a2a.NewResult(task.ID, a.card.ID, false)

	// Step 1 — read files for review.
	a.Progress(progressFn, task.ID, "Reading files for review...", 1, 3)
	contents := map[string]string{}
	for _, p := range task.Files {
		res, err := a.RunTool(ctx, "read_file", map[string]any{"path": p})
		if err != nil {
			continue
		}
		contents[p] = toolString(res)
	}

	// Step 2 — AI review.
	a.Progress(progressFn, task.ID, "Reviewing code quality...", 2, 3)
	reply, err := a.Complete(ctx, reviewSystemPrompt, buildReviewPrompt(contents, task.Context))
	if err != nil {
		result.Errors = []string{"review failed: " + err.Error()}
		return *result
	}
	var review ReviewResult
	if perr := jsonUnmarshalFences(reply, &review); perr != nil {
		// Malformed JSON: fail safe — do not approve, surface the raw reply.
		result.Errors = []string{"could not parse review: " + perr.Error()}
		result.Output = "Review parse error. Raw response:\n" + reply
		result.Score = 0
		result.Approved = false
		return *result
	}
	// The threshold is authoritative; honour the model's approved flag only
	// when it agrees with the score gate (prevents a high "approved" with a low
	// score slipping through).
	review.Approved = review.Score >= reviewApprovalThreshold

	// Step 3 — return result.
	a.Progress(progressFn, task.ID, "Review complete", 3, 3)
	result.Score = review.Score
	result.Approved = review.Approved
	result.Success = review.Approved
	result.Output = formatReview(review)
	if !review.Approved {
		result.Errors = issueMessages(review)
	}
	return *result
}

// buildReviewPrompt assembles the files + context for the reviewer.
func buildReviewPrompt(files map[string]string, taskContext string) string {
	var b strings.Builder
	if taskContext != "" {
		b.WriteString("Context:\n" + taskContext + "\n\n")
	}
	if len(files) == 0 {
		b.WriteString("No files were provided; review the context above.\n")
	}
	for path, content := range files {
		b.WriteString("--- " + path + " ---\n" + content + "\n\n")
	}
	b.WriteString("Review the code and return the JSON verdict.")
	return b.String()
}

// formatReview renders the human-readable review summary.
func formatReview(r ReviewResult) string {
	var b strings.Builder
	mark := "✗ Needs fixes"
	if r.Approved {
		mark = "✓ Approved"
	}
	fmt.Fprintf(&b, "Score: %d/10 %s\n", r.Score, mark)
	if r.Summary != "" {
		b.WriteString("Summary: " + r.Summary + "\n")
	}
	if len(r.SecurityIssues) > 0 {
		b.WriteString("Security issues:\n")
		for _, s := range r.SecurityIssues {
			fmt.Fprintf(&b, "  [%s] %s:%d %s\n", strings.ToUpper(s.Severity), s.File, s.Line, s.Message)
		}
	}
	if len(r.Issues) > 0 {
		fmt.Fprintf(&b, "Issues (%d):\n", len(r.Issues))
		for _, i := range r.Issues {
			if strings.EqualFold(i.Severity, "high") || strings.EqualFold(i.Severity, "critical") {
				fmt.Fprintf(&b, "  [%s] %s:%d %s\n", strings.ToUpper(i.Severity), i.File, i.Line, i.Message)
			} else {
				fmt.Fprintf(&b, "  - %s:%d %s\n", i.File, i.Line, i.Message)
			}
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// issueMessages flattens issues into short strings for TaskResult.Errors.
func issueMessages(r ReviewResult) []string {
	var out []string
	for _, s := range r.SecurityIssues {
		out = append(out, fmt.Sprintf("[%s] %s:%d %s", strings.ToUpper(s.Severity), s.File, s.Line, s.Message))
	}
	for _, i := range r.Issues {
		out = append(out, fmt.Sprintf("[%s] %s:%d %s", strings.ToUpper(i.Severity), i.File, i.Line, i.Message))
	}
	return out
}
