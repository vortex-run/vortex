package messaging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vortex-run/vortex/internal/gateway"
)

// Provider names.
const (
	ProviderClaude      = "claude"
	ProviderOpenAI      = "openai"
	ProviderOllama      = "ollama"
	ProviderDeepSeek    = "deepseek"
	ProviderGemini      = "gemini"
	ProviderGroq        = "groq"         // OpenAI-compatible, very fast (M20)
	ProviderBedrock     = "bedrock"      // AWS Bedrock, SigV4-signed (M20)
	ProviderAzureOpenAI = "azure-openai" // Azure OpenAI deployment (M20)
	ProviderOpenRouter  = "openrouter"   // 75+ models via one API (M20)
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
	// MaxTokens caps generated tokens on providers that require an explicit
	// limit (the Anthropic Messages API and Anthropic-on-Bedrock). 0 selects
	// defaultMaxTokens. Callers can override per request via
	// CompleteWithOptions / the OpenAI-compatible max_tokens field.
	MaxTokens int
	now       func() time.Time // injectable clock (tests)
}

// defaultMaxTokens is the generation cap used when neither the request nor the
// gateway config specifies one. The Anthropic Messages API requires max_tokens
// to be set explicitly, so some value must always be sent; this default is
// generous enough for long answers (previously this was a hardcoded 1000,
// which silently truncated long generations).
const defaultMaxTokens = 8192

// maxTokensKey carries a per-request generation cap through the context. The
// cap is request-scoped data threaded through an already-ubiquitous ctx rather
// than added to the signature of every call*/stream* provider method.
type maxTokensKey struct{}

// WithMaxTokens returns a context requesting n generated tokens for this call
// (n <= 0 is ignored, leaving the gateway default in force). Honoured by the
// providers that take an explicit cap: claude and bedrock.
func WithMaxTokens(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxTokensKey{}, n)
}

// maxTokensFor returns the generation cap for a request: the per-request
// override from ctx when set, else the configured gateway default, else
// defaultMaxTokens.
func (g *AIGateway) maxTokensFor(ctx context.Context) int {
	if n, ok := ctx.Value(maxTokensKey{}).(int); ok && n > 0 {
		return n
	}
	if g.cfg.MaxTokens > 0 {
		return g.cfg.MaxTokens
	}
	return defaultMaxTokens
}

// AIGateway routes completion requests across providers in priority order,
// implementing agents.AIGateway. It tracks token cost and enforces a daily
// budget. When a key-rotation Router is wired (SetKeyRotation), Complete routes
// through health-scored key slots with automatic failover and context handoff
// instead of the static provider list.
type AIGateway struct {
	cfg    AIGatewayConfig
	client *http.Client
	now    func() time.Time

	mu            sync.Mutex
	costToday     float64
	requestsToday int
	dayStart      time.Time

	// Key rotation (autonomous API key rotation). When router != nil, Complete
	// selects slots from the key store, records outcomes, and fails over.
	router   *gateway.Router
	bridge   *gateway.ContextBridge
	keyStore *gateway.KeyStore
}

// SetKeyRotation enables autonomous key rotation: Complete selects from
// health-scored key slots with failover and context preservation instead of
// the static provider list. Passing a nil router disables it (single-provider
// mode).
func (g *AIGateway) SetKeyRotation(router *gateway.Router, bridge *gateway.ContextBridge, store *gateway.KeyStore) {
	g.mu.Lock()
	g.router = router
	g.bridge = bridge
	g.keyStore = store
	g.mu.Unlock()
}

// keyRotationEnabled reports whether a router is wired.
func (g *AIGateway) keyRotationEnabled() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.router != nil
}

// CostSnapshot summarises AI usage for the current day (for /api/ai/cost).
type CostSnapshot struct {
	Provider        string  `json:"provider"`
	TotalUSD        float64 `json:"total_usd"`
	RequestsToday   int     `json:"requests_today"`
	DailyBudget     float64 `json:"daily_budget"`
	RemainingBudget float64 `json:"remaining_budget"`
	Free            bool    `json:"free"`
}

// CostToday returns a snapshot of today's AI spend and budget.
func (g *AIGateway) CostToday() CostSnapshot {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.rolloverLocked()
	provider := ""
	if len(g.cfg.Providers) > 0 {
		provider = g.cfg.Providers[0].Name
	}
	remaining := 0.0
	if g.cfg.DailyBudget > 0 {
		remaining = g.cfg.DailyBudget - g.costToday
		if remaining < 0 {
			remaining = 0
		}
	}
	// Ollama (local) is free; treat a zero cost table as free too.
	free := provider == "ollama" || len(g.cfg.CostPerToken) == 0
	return CostSnapshot{
		Provider:        provider,
		TotalUSD:        g.costToday,
		RequestsToday:   g.requestsToday,
		DailyBudget:     g.cfg.DailyBudget,
		RemainingBudget: remaining,
		Free:            free,
	}
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
// the response text. It enforces the daily budget before calling out. When key
// rotation is enabled it routes through health-scored slots instead.
func (g *AIGateway) Complete(ctx context.Context, prompt, systemPrompt string) (string, error) {
	if g.keyRotationEnabled() {
		return g.completeRotating(ctx, prompt, systemPrompt, detectRequestType(prompt))
	}
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
		g.mu.Lock()
		g.rolloverLocked()
		g.requestsToday++
		g.mu.Unlock()
		return text, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("messaging: no providers available")
	}
	return "", lastErr
}

// maxSlotTries bounds how many distinct slots Complete will try before giving
// up (one full rotation across 4 slots, with a retry budget).
const maxSlotTries = 6

// completeRotating routes a completion through the key-rotation router: select
// a slot, call its provider, and on rate-limit/error fail over to the next
// healthy slot. Success/error/cost are recorded so health scores stay current.
func (g *AIGateway) completeRotating(ctx context.Context, prompt, systemPrompt, requestType string) (string, error) {
	g.mu.Lock()
	router, store := g.router, g.keyStore
	g.mu.Unlock()

	var lastErr error
	tried := map[string]int{}
	for i := 0; i < maxSlotTries; i++ {
		slot, err := router.SelectSlot(ctx, requestType)
		if err != nil {
			if lastErr != nil {
				return "", lastErr
			}
			return "", err
		}
		// Avoid hammering the same slot more than 3 times for non-rate-limit errors.
		if tried[slot.ID] >= 3 {
			// All remaining selections keep returning an exhausted slot; stop.
			if lastErr != nil {
				return "", lastErr
			}
			return "", fmt.Errorf("messaging: all key slots exhausted")
		}
		tried[slot.ID]++

		full, derr := store.GetDecrypted(slot.ID)
		if derr != nil {
			lastErr = derr
			router.RecordError(slot.ID, derr, false)
			continue
		}
		p := providerFromSlot(full)

		start := g.now()
		text, tokens, cerr := g.callProvider(ctx, p, prompt, systemPrompt)
		latency := g.now().Sub(start).Milliseconds()
		if cerr != nil {
			lastErr = cerr
			isRL := isRateLimit(cerr)
			router.RecordError(slot.ID, cerr, isRL)
			continue
		}
		cost := g.costOf(g.modelOf(p), tokens)
		router.RecordSuccess(slot.ID, latency, cost)
		router.RecordCost(slot.ID, 0) // budget check (cost already added by RecordSuccess)
		g.mu.Lock()
		g.rolloverLocked()
		g.requestsToday++
		g.costToday += cost
		g.mu.Unlock()
		return text, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("messaging: all key slots failed")
	}
	return "", lastErr
}

// CompleteWithSession is like Complete but preserves conversation context
// across slot switches: it stores the session's messages and, when the active
// slot changes provider mid-session, builds a handoff so the new provider
// receives the full context. The reply is prefixed with a switch notice when a
// provider change occurred.
func (g *AIGateway) CompleteWithSession(ctx context.Context, sessionID string, messages []gateway.ContextMessage, systemPrompt string) (string, error) {
	g.mu.Lock()
	router, bridge := g.router, g.bridge
	g.mu.Unlock()
	if router == nil || bridge == nil {
		// Single-provider mode: flatten and use Complete.
		return g.Complete(ctx, flattenMessages(messages), systemPrompt)
	}

	bridge.Store(sessionID, messages)
	prev := router.ActiveSlotID()

	slot, err := router.SelectSlot(ctx, detectRequestTypeMsgs(messages))
	if err != nil {
		return "", err
	}
	prompt := flattenMessages(messages)
	notice := ""
	// If the active slot's provider changed from the previous turn, hand off.
	if prev != "" && prev != slot.ID {
		if oldSlot, oerr := g.keyStore.Get(prev); oerr == nil && oldSlot.Provider != slot.Provider {
			handoff, herr := bridge.BuildHandoff(ctx, sessionID, slot.Provider)
			if herr == nil {
				prompt = flattenMessages(handoff)
				notice = bridge.NotifySwitch(oldSlot, slot, "previous key unavailable", len(handoff)) + "\n\n"
			}
		}
	}
	reply, err := g.completeRotating(ctx, prompt, systemPrompt, detectRequestTypeMsgs(messages))
	if err != nil {
		return "", err
	}
	return notice + reply, nil
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
	case ProviderGroq:
		return g.callGroq(ctx, p, prompt, systemPrompt)
	case ProviderBedrock:
		return g.callBedrock(ctx, p, prompt, systemPrompt)
	case ProviderAzureOpenAI:
		return g.callAzureOpenAI(ctx, p, prompt, systemPrompt)
	case ProviderOpenRouter:
		return g.callOpenRouter(ctx, p, prompt, systemPrompt)
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

// doSignedJSON is like doJSON but lets sign mutate the request headers based on
// the marshalled body (used for AWS SigV4, which signs the payload). The
// signer receives the header map to populate and the request body bytes.
func (g *AIGateway) doSignedJSON(ctx context.Context, url string, sign func(headers map[string]string, body []byte), payload, out any) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	headers := map[string]string{"Content-Type": "application/json"}
	sign(headers, body)

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
			"max_tokens": g.maxTokensFor(ctx),
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

// costOf returns the estimated USD cost of tokens for model (0 when no rate is
// configured). Used by the rotation path, which records cost per slot.
func (g *AIGateway) costOf(model string, tokens int) float64 {
	return g.cfg.CostPerToken[model] * float64(tokens)
}

// providerFromSlot builds an AIProvider from a decrypted key slot so the
// existing per-provider call path can serve a rotated request.
func providerFromSlot(slot *gateway.KeySlot) AIProvider {
	p := AIProvider{Name: slot.Provider, APIKey: slot.APIKey, Endpoint: slot.Endpoint}
	if slot.Model != "" {
		p.Models = []string{slot.Model}
	}
	return p
}

// isRateLimit reports whether err is a provider rate-limit (HTTP 429).
func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "429") ||
		strings.Contains(strings.ToLower(s), "rate limit") ||
		strings.Contains(strings.ToLower(s), "too many requests")
}

// codingKeywords flag a coding request for request-type detection.
var codingKeywords = []string{
	"code", "function", "bug", "refactor", "compile", "implement", "class",
	"def ", "func ", "import ", "api", "test", "debug", "python", "golang",
	"javascript", "typescript", "rust", "java",
}

// visionKeywords flag a vision request.
var visionKeywords = []string{"image", "screenshot", "photo", "picture", "diagram", "look at this"}

// detectRequestType classifies a prompt for routing: short → simple,
// vision refs → vision, coding keywords → coding, else complex.
func detectRequestType(prompt string) string {
	low := strings.ToLower(prompt)
	for _, k := range visionKeywords {
		if strings.Contains(low, k) {
			return "vision"
		}
	}
	if len(prompt) < 100 {
		return "simple"
	}
	for _, k := range codingKeywords {
		if strings.Contains(low, k) {
			return "coding"
		}
	}
	return "complex"
}

// detectRequestTypeMsgs classifies based on the last user message.
func detectRequestTypeMsgs(messages []gateway.ContextMessage) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "user" {
			return detectRequestType(messages[i].Content)
		}
	}
	if len(messages) > 0 {
		return detectRequestType(messages[len(messages)-1].Content)
	}
	return "complex"
}

// flattenMessages renders a message list into a single prompt string.
func flattenMessages(messages []gateway.ContextMessage) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(m.Role + ": " + m.Content + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
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

// rolloverLocked resets the daily counters if the day has changed. Caller holds
// mu.
func (g *AIGateway) rolloverLocked() {
	if g.now().Sub(g.dayStart) >= 24*time.Hour {
		g.costToday = 0
		g.requestsToday = 0
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

// ModelIDs returns every model this gateway can serve, in provider priority
// order (a provider with no configured models is listed under its own name).
// Backs GET /v1/models on the OpenAI-compatible server (upgrade 3).
func (g *AIGateway) ModelIDs() []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !seen[id] {
			out = append(out, id)
			seen[id] = true
		}
	}
	for _, p := range g.cfg.Providers {
		if len(p.Models) == 0 {
			add(p.Name)
			continue
		}
		for _, m := range p.Models {
			add(m)
		}
	}
	return out
}

// CompleteForModel routes a completion to the provider serving model (upgrade
// 3 — OpenAI-compatible server). Unlike Complete it does NOT fall back across
// providers: the caller asked for a specific model, so a failure surfaces
// rather than silently answering with a different one. Returns the text and
// the provider-reported token count.
func (g *AIGateway) CompleteForModel(ctx context.Context, model, prompt, systemPrompt string) (string, int, error) {
	if g.budgetExceeded() {
		return "", 0, ErrBudgetExceeded
	}
	p, ok := g.providerForModel(model)
	if !ok {
		return "", 0, fmt.Errorf("messaging: no provider configured for model %q", model)
	}
	// Send the requested model name upstream (not the provider's default).
	if model != "" {
		p.Models = []string{model}
	}
	text, tokens, err := g.callProvider(ctx, p, prompt, systemPrompt)
	if err != nil {
		return "", 0, err
	}
	g.recordCost(model, tokens)
	g.mu.Lock()
	g.rolloverLocked()
	g.requestsToday++
	g.mu.Unlock()
	return text, tokens, nil
}

// providerForModel picks the provider for a model name: an exact match against
// configured models wins, then family heuristics (claude-* → claude, gpt-* →
// openai, deepseek* → deepseek, gemini* → gemini, llama*/mistral* → ollama),
// falling back to the primary (highest-priority) provider.
func (g *AIGateway) providerForModel(model string) (AIProvider, bool) {
	if len(g.cfg.Providers) == 0 {
		return AIProvider{}, false
	}
	for _, p := range g.cfg.Providers {
		for _, m := range p.Models {
			if m == model {
				return p, true
			}
		}
	}
	low := strings.ToLower(model)
	want := ""
	switch {
	case strings.HasPrefix(low, "claude"):
		want = ProviderClaude
	case strings.HasPrefix(low, "gpt"), strings.HasPrefix(low, "o1"), strings.HasPrefix(low, "o3"):
		want = ProviderOpenAI
	case strings.HasPrefix(low, "deepseek"):
		want = ProviderDeepSeek
	case strings.HasPrefix(low, "gemini"):
		want = ProviderGemini
	case strings.HasPrefix(low, "llama"), strings.HasPrefix(low, "mistral"):
		want = ProviderOllama
	}
	if want != "" {
		for _, p := range g.cfg.Providers {
			if p.Name == want {
				return p, true
			}
		}
	}
	return g.cfg.Providers[0], true
}
