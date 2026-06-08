package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

// Provider names.
const (
	ProviderClaude   = "claude"
	ProviderOpenAI   = "openai"
	ProviderOllama   = "ollama"
	ProviderDeepSeek = "deepseek"
	ProviderGemini   = "gemini"
)

// AIProvider describes one upstream model provider.
type AIProvider struct {
	Name     string   // "claude" | "openai" | "ollama"
	APIKey   string   // from env (empty for ollama)
	Endpoint string   // override base URL; for ollama defaults to localhost:11434
	Models   []string // available models; Models[0] is used by default
	Priority int      // lower = preferred
}

// AIGatewayConfig configures the gateway.
type AIGatewayConfig struct {
	Providers    []AIProvider
	DefaultModel string
	CostPerToken map[string]float64 // model → USD per token (rough)
	DailyBudget  float64            // USD; 0 = unlimited
	Client       *http.Client
	now          func() time.Time // injectable clock (tests)
}

// AIGateway routes completion requests across providers in priority order,
// implementing agents.AIGateway. It tracks token cost and enforces a daily
// budget.
type AIGateway struct {
	cfg    AIGatewayConfig
	client *http.Client
	now    func() time.Time

	mu        sync.Mutex
	costToday float64
	dayStart  time.Time
}

// NewAIGateway builds the gateway. It requires at least one provider and sorts
// them by ascending priority.
func NewAIGateway(cfg AIGatewayConfig) (*AIGateway, error) {
	if len(cfg.Providers) == 0 {
		return nil, fmt.Errorf("messaging: ai gateway requires at least one provider")
	}
	sorted := make([]AIProvider, len(cfg.Providers))
	copy(sorted, cfg.Providers)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Priority < sorted[j].Priority })
	cfg.Providers = sorted

	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	now := cfg.now
	if now == nil {
		now = time.Now
	}
	return &AIGateway{cfg: cfg, client: client, now: now, dayStart: now()}, nil
}

// ErrBudgetExceeded is returned when the daily cost budget is reached.
var ErrBudgetExceeded = fmt.Errorf("messaging: daily AI budget exceeded")

// Complete tries each provider in priority order until one succeeds, returning
// the response text. It enforces the daily budget before calling out.
func (g *AIGateway) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	if g.budgetExceeded() {
		return "", ErrBudgetExceeded
	}

	var lastErr error
	for _, p := range g.cfg.Providers {
		text, tokens, err := g.callProvider(ctx, p, prompt, systemPrompt)
		if err != nil {
			lastErr = err
			continue
		}
		g.recordCost(g.modelOf(p), tokens)
		return text, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("messaging: no providers available")
	}
	return "", lastErr
}

// modelOf returns the default model for a provider.
func (g *AIGateway) modelOf(p AIProvider) string {
	if len(p.Models) > 0 {
		return p.Models[0]
	}
	return g.cfg.DefaultModel
}

// callProvider dispatches to the provider-specific request shape and returns
// (text, approxTokens, error).
func (g *AIGateway) callProvider(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	switch p.Name {
	case ProviderClaude:
		return g.callClaude(ctx, p, prompt, systemPrompt)
	case ProviderOpenAI:
		return g.callOpenAI(ctx, p, prompt, systemPrompt)
	case ProviderOllama:
		return g.callOllama(ctx, p, prompt, systemPrompt)
	case ProviderDeepSeek:
		return g.callDeepSeek(ctx, p, prompt, systemPrompt)
	case ProviderGemini:
		return g.callGemini(ctx, p, prompt, systemPrompt)
	default:
		return "", 0, fmt.Errorf("messaging: unknown provider %q", p.Name)
	}
}

// callDeepSeek calls DeepSeek's OpenAI-compatible chat completions API. It
// reuses the OpenAI request/response shape against DeepSeek's endpoint.
func (g *AIGateway) callDeepSeek(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	if p.Endpoint == "" {
		p.Endpoint = "https://api.deepseek.com"
	}
	text, tokens, err := g.callOpenAI(ctx, p, prompt, systemPrompt)
	if err != nil {
		return "", 0, fmt.Errorf("deepseek: %w", err)
	}
	return text, tokens, nil
}

// callGemini calls Google's Gemini generateContent REST API. The API key is a
// query parameter; the system prompt goes in systemInstruction.
func (g *AIGateway) callGemini(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	model := g.modelOf(p)
	if model == "" {
		model = "gemini-1.5-flash"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:generateContent?key=%s", base, model, p.APIKey)

	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		UsageMetadata struct {
			TotalTokenCount int `json:"totalTokenCount"`
		} `json:"usageMetadata"`
	}
	_, err := g.doJSON(ctx, url, nil, map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": prompt}}},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": systemPrompt}},
		},
	}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("gemini: %w", err)
	}
	text := ""
	if len(out.Candidates) > 0 && len(out.Candidates[0].Content.Parts) > 0 {
		text = out.Candidates[0].Content.Parts[0].Text
	}
	return text, out.UsageMetadata.TotalTokenCount, nil
}

// minAICallTimeout is the floor for any provider HTTP call. Providers like
// DeepSeek can take tens of seconds to respond; the caller's context (e.g. a
// TUI request) may be cancelled when the user navigates away, so we decouple
// the HTTP call onto a fresh background context with at least this deadline.
const minAICallTimeout = 60 * time.Second

// doJSON posts a JSON body to url with the given headers and decodes the
// response into out, returning the raw response bytes for token estimation.
func (g *AIGateway) doJSON(ctx context.Context, url string, headers map[string]string, payload, out any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	// Use a fresh background context with a 60s floor so a short-lived caller
	// context (cancelled by the TUI on navigation) cannot abort a slow provider
	// mid-request. If the caller's deadline is already longer, honour it.
	callCtx, cancel := context.WithTimeout(context.Background(), minAICallTimeout)
	defer cancel()
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) > minAICallTimeout {
		callCtx, cancel = context.WithDeadline(context.Background(), dl)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return data, err
		}
	}
	return data, nil
}

// callClaude calls the Anthropic Messages API.
func (g *AIGateway) callClaude(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://api.anthropic.com"
	}
	var out struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
		} `json:"usage"`
	}
	_, err := g.doJSON(ctx, base+"/v1/messages",
		map[string]string{"x-api-key": p.APIKey, "anthropic-version": "2023-06-01"},
		map[string]any{
			"model":      g.modelOf(p),
			"max_tokens": 1000,
			"system":     systemPrompt,
			"messages":   []map[string]any{{"role": "user", "content": prompt}},
		}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("claude: %w", err)
	}
	text := ""
	if len(out.Content) > 0 {
		text = out.Content[0].Text
	}
	return text, out.Usage.InputTokens + out.Usage.OutputTokens, nil
}

// callOpenAI calls the OpenAI Chat Completions API.
func (g *AIGateway) callOpenAI(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://api.openai.com"
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
		Usage struct {
			TotalTokens int `json:"total_tokens"`
		} `json:"usage"`
	}
	_, err := g.doJSON(ctx, base+"/v1/chat/completions",
		map[string]string{"Authorization": "Bearer " + p.APIKey},
		map[string]any{
			"model": g.modelOf(p),
			"messages": []map[string]any{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": prompt},
			},
		}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("openai: %w", err)
	}
	text := ""
	if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	return text, out.Usage.TotalTokens, nil
}

// callOllama calls a local Ollama generate endpoint.
func (g *AIGateway) callOllama(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "http://localhost:11434"
	}
	var out struct {
		Response  string `json:"response"`
		EvalCount int    `json:"eval_count"`
	}
	_, err := g.doJSON(ctx, base+"/api/generate", nil,
		map[string]any{
			"model":  g.modelOf(p),
			"prompt": prompt,
			"system": systemPrompt,
			"stream": false,
		}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("ollama: %w", err)
	}
	return out.Response, out.EvalCount, nil
}

// recordCost adds the estimated cost of tokens for model to today's total.
func (g *AIGateway) recordCost(model string, tokens int) {
	per := g.cfg.CostPerToken[model]
	if per == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rolloverLocked()
	g.costToday += per * float64(tokens)
}

// budgetExceeded reports whether today's spend has reached the daily budget.
func (g *AIGateway) budgetExceeded() bool {
	if g.cfg.DailyBudget <= 0 {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rolloverLocked()
	return g.costToday >= g.cfg.DailyBudget
}

// rolloverLocked resets the daily cost if the day has changed. Caller holds mu.
func (g *AIGateway) rolloverLocked() {
	if g.now().Sub(g.dayStart) >= 24*time.Hour {
		g.costToday = 0
		g.dayStart = g.now()
	}
}

// Cost returns the total USD spent today, rolling over (to 0) first if the day
// boundary has passed since the last activity.
func (g *AIGateway) Cost() float64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rolloverLocked()
	return g.costToday
}

// ResetDailyCost zeroes today's spend (called at midnight by a supervisor).
func (g *AIGateway) ResetDailyCost() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.costToday = 0
	g.dayStart = g.now()
}

// ProviderNames returns the configured provider names in priority order, for
// startup logging.
func (g *AIGateway) ProviderNames() []string {
	out := make([]string, 0, len(g.cfg.Providers))
	for _, p := range g.cfg.Providers {
		out = append(out, p.Name)
	}
	return out
}
