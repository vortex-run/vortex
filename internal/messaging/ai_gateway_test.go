package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestNewAIGateway_RequiresProvider(t *testing.T) {
	if _, err := NewAIGateway(AIGatewayConfig{}); err == nil {
		t.Error("expected error with no providers")
	}
}

func TestAIGateway_RoutesToFirstProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"content":[{"text":"claude says hi"}],"usage":{"input_tokens":3,"output_tokens":2}}`)
	}))
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{
			{Name: ProviderClaude, APIKey: "k", Endpoint: srv.URL, Models: []string{"claude-x"}, Priority: 0},
		},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := g.Complete(context.Background(), "hi", "be brief")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "claude says hi" {
		t.Errorf("out = %q, want 'claude says hi'", out)
	}
}

func TestAIGateway_FallsBackOnFailure(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"openai fallback"}}],"usage":{"total_tokens":5}}`)
	}))
	defer ok.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{
			{Name: ProviderClaude, APIKey: "k", Endpoint: failing.URL, Models: []string{"c"}, Priority: 0},
			{Name: ProviderOpenAI, APIKey: "k", Endpoint: ok.URL, Models: []string{"gpt"}, Priority: 1},
		},
		Client: ok.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if out != "openai fallback" {
		t.Errorf("out = %q, want fallback to openai", out)
	}
}

func TestAIGateway_ClaudeHeaders(t *testing.T) {
	var gotKey, gotVer string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVer = r.Header.Get("anthropic-version")
		_, _ = io.WriteString(w, `{"content":[{"text":"ok"}],"usage":{}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderClaude, APIKey: "secret-key", Endpoint: srv.URL, Models: []string{"c"}}},
		Client:    srv.Client(),
	})
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if gotKey != "secret-key" || gotVer != "2023-06-01" {
		t.Errorf("headers: x-api-key=%q anthropic-version=%q", gotKey, gotVer)
	}
}

func TestAIGateway_OpenAIAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}],"usage":{}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderOpenAI, APIKey: "sk-123", Endpoint: srv.URL, Models: []string{"gpt"}}},
		Client:    srv.Client(),
	})
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if gotAuth != "Bearer sk-123" {
		t.Errorf("Authorization = %q, want Bearer sk-123", gotAuth)
	}
}

func TestAIGateway_OllamaLocalEndpoint(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"response":"local model reply","eval_count":7}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderOllama, Endpoint: srv.URL, Models: []string{"llama"}}},
		Client:    srv.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out != "local model reply" || gotPath != "/api/generate" {
		t.Errorf("out=%q path=%q", out, gotPath)
	}
}

func TestAIGateway_CostTracking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"x"}}],"usage":{"total_tokens":100}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers:    []AIProvider{{Name: ProviderOpenAI, APIKey: "k", Endpoint: srv.URL, Models: []string{"gpt"}}},
		CostPerToken: map[string]float64{"gpt": 0.001},
		Client:       srv.Client(),
	})
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if c := g.Cost(); c < 0.099 || c > 0.101 { // 100 * 0.001 = 0.1
		t.Errorf("Cost = %f, want ~0.1", c)
	}
}

func TestAIGateway_BudgetExceeded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"x"}}],"usage":{"total_tokens":1000}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers:    []AIProvider{{Name: ProviderOpenAI, APIKey: "k", Endpoint: srv.URL, Models: []string{"gpt"}}},
		CostPerToken: map[string]float64{"gpt": 0.01},
		DailyBudget:  5.0,
		Client:       srv.Client(),
	})
	// First call spends 1000*0.01 = $10, exceeding the $5 budget.
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatalf("first call: %v", err)
	}
	// Second call is rejected before any provider is reached.
	if _, err := g.Complete(context.Background(), "again", "sys"); !errors.Is(err, ErrBudgetExceeded) {
		t.Errorf("second call err = %v, want ErrBudgetExceeded", err)
	}
}

func TestAIGateway_ResetDailyCost(t *testing.T) {
	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderOllama, Models: []string{"x"}}},
	})
	g.recordCost("x", 0) // no-op, but exercise path
	g.mu.Lock()
	g.costToday = 42
	g.mu.Unlock()
	g.ResetDailyCost()
	if g.Cost() != 0 {
		t.Errorf("Cost after reset = %f, want 0", g.Cost())
	}
}

func TestAIGateway_AllProvidersFail(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer bad.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{
			{Name: ProviderClaude, APIKey: "k", Endpoint: bad.URL, Models: []string{"c"}, Priority: 0},
			{Name: ProviderOpenAI, APIKey: "k", Endpoint: bad.URL, Models: []string{"g"}, Priority: 1},
		},
		Client: bad.Client(),
	})
	if _, err := g.Complete(context.Background(), "hi", "sys"); err == nil {
		t.Error("expected error when all providers fail")
	}
}

func TestAIGateway_DailyRollover(t *testing.T) {
	clock := &fakeClock{t: time.Now()}
	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderOllama, Models: []string{"x"}}},
		now:       clock.now,
	})
	g.mu.Lock()
	g.costToday = 99
	g.mu.Unlock()
	// Advance 25h; cost should roll over to 0 on the next budget/cost check.
	clock.advance(25 * time.Hour)
	if g.budgetExceeded() { // triggers rollover (budget 0 → never exceeded, but rolls)
		t.Error("budget 0 should never be exceeded")
	}
	if g.Cost() != 0 {
		t.Errorf("cost after 25h rollover = %f, want 0", g.Cost())
	}
}

// fakeClock is a controllable clock for rollover tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestAIGateway_DeepSeekOpenAICompatible(t *testing.T) {
	var gotPath, gotAuth string
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"deepseek reply"}}],"usage":{"total_tokens":8}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderDeepSeek, APIKey: "sk-ds", Endpoint: srv.URL, Models: []string{"deepseek-chat"}}},
		Client:    srv.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out != "deepseek reply" {
		t.Errorf("out = %q, want deepseek reply", out)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want OpenAI-compatible /v1/chat/completions", gotPath)
	}
	if gotAuth != "Bearer sk-ds" {
		t.Errorf("auth = %q, want Bearer sk-ds", gotAuth)
	}
	if gotModel != "deepseek-chat" {
		t.Errorf("model = %q, want deepseek-chat", gotModel)
	}
}

func TestAIGateway_GeminiEndpointAndParse(t *testing.T) {
	var gotKey, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"gemini reply"}]}}],"usageMetadata":{"totalTokenCount":12}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderGemini, APIKey: "g-key", Endpoint: srv.URL, Models: []string{"gemini-1.5-flash"}}},
		Client:    srv.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out != "gemini reply" {
		t.Errorf("out = %q, want gemini reply", out)
	}
	if gotKey != "g-key" {
		t.Errorf("query key = %q, want g-key", gotKey)
	}
	if !strings.Contains(gotPath, "gemini-1.5-flash:generateContent") {
		t.Errorf("path = %q, want .../gemini-1.5-flash:generateContent", gotPath)
	}
}

func TestAIGateway_DeepSeekFallback(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer failing.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"parts":[{"text":"gemini fallback"}]}}]}`)
	}))
	defer ok.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{
			{Name: ProviderDeepSeek, APIKey: "sk", Endpoint: failing.URL, Models: []string{"deepseek-chat"}, Priority: 0},
			{Name: ProviderGemini, APIKey: "g", Endpoint: ok.URL, Models: []string{"gemini-1.5-flash"}, Priority: 1},
		},
		Client: ok.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out != "gemini fallback" {
		t.Errorf("out = %q, want fallback to gemini after deepseek fails", out)
	}
}

func TestAIGateway_CostToday(t *testing.T) {
	gw, err := NewAIGateway(AIGatewayConfig{
		Providers:    []AIProvider{{Name: "openai", Models: []string{"gpt-4o-mini"}}},
		CostPerToken: map[string]float64{"gpt-4o-mini": 0.000002},
		DailyBudget:  1.0,
	})
	if err != nil {
		t.Fatal(err)
	}
	gw.recordCost("gpt-4o-mini", 1000) // $0.002
	gw.mu.Lock()
	gw.requestsToday = 1
	gw.mu.Unlock()

	c := gw.CostToday()
	if c.Provider != "openai" {
		t.Errorf("provider = %q, want openai", c.Provider)
	}
	if c.TotalUSD < 0.0019 || c.TotalUSD > 0.0021 {
		t.Errorf("total = %v, want ~0.002", c.TotalUSD)
	}
	if c.DailyBudget != 1.0 || c.RemainingBudget > 1.0 || c.RemainingBudget < 0.99 {
		t.Errorf("budget = %v remaining = %v", c.DailyBudget, c.RemainingBudget)
	}
	if c.Free {
		t.Error("openai with a cost table should not be free")
	}
}

func TestAIGateway_CostFreeForOllama(t *testing.T) {
	gw, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: "ollama", Models: []string{"llama3"}}},
	})
	if !gw.CostToday().Free {
		t.Error("ollama should report free")
	}
}

func TestModelIDs(t *testing.T) {
	gw, err := NewAIGateway(AIGatewayConfig{Providers: []AIProvider{
		{Name: ProviderDeepSeek, Models: []string{"deepseek-chat"}, Priority: 1},
		{Name: ProviderClaude, Models: []string{"claude-sonnet-4", "claude-haiku-4-5"}, Priority: 2},
		{Name: ProviderOllama, Priority: 3}, // no models → listed under provider name
	}})
	if err != nil {
		t.Fatal(err)
	}
	got := gw.ModelIDs()
	want := []string{"deepseek-chat", "claude-sonnet-4", "claude-haiku-4-5", "ollama"}
	if len(got) != len(want) {
		t.Fatalf("ModelIDs = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ModelIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestProviderForModelRouting(t *testing.T) {
	gw, err := NewAIGateway(AIGatewayConfig{Providers: []AIProvider{
		{Name: ProviderDeepSeek, Models: []string{"deepseek-chat"}, Priority: 1},
		{Name: ProviderClaude, Models: []string{"claude-sonnet-4"}, Priority: 2},
		{Name: ProviderOpenAI, Priority: 3},
		{Name: ProviderOllama, Priority: 4},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"deepseek-chat":   ProviderDeepSeek, // exact configured match
		"claude-opus-4-8": ProviderClaude,   // family heuristic
		"gpt-4o":          ProviderOpenAI,
		"llama3.2":        ProviderOllama,
		"mistral-small":   ProviderOllama,
		"totally-unknown": ProviderDeepSeek, // default → primary provider
	}
	for model, want := range cases {
		p, ok := gw.providerForModel(model)
		if !ok || p.Name != want {
			t.Errorf("providerForModel(%q) = %q (ok=%v), want %q", model, p.Name, ok, want)
		}
	}
}

func TestCompleteForModelSendsRequestedModel(t *testing.T) {
	var gotModel string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotModel = req.Model
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"routed"}}],"usage":{"total_tokens":12}}`)
	}))
	defer srv.Close()

	gw, err := NewAIGateway(AIGatewayConfig{Providers: []AIProvider{
		{Name: ProviderDeepSeek, APIKey: "k", Endpoint: srv.URL, Models: []string{"deepseek-chat"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	// Request a model that is not the provider default: it must be sent upstream.
	text, tokens, err := gw.CompleteForModel(context.Background(), "deepseek-reasoner", "hi", "sys")
	if err != nil {
		t.Fatalf("CompleteForModel: %v", err)
	}
	if text != "routed" || tokens != 12 {
		t.Errorf("got (%q, %d), want (routed, 12)", text, tokens)
	}
	if gotModel != "deepseek-reasoner" {
		t.Errorf("upstream model = %q, want deepseek-reasoner", gotModel)
	}
}
