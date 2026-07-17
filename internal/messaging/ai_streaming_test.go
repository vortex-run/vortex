package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// streamGateway builds a gateway with one provider pointed at srv.
func streamGateway(t *testing.T, srv *httptest.Server, p AIProvider, cfg AIGatewayConfig) *AIGateway {
	t.Helper()
	p.Endpoint = srv.URL
	cfg.Providers = []AIProvider{p}
	cfg.Client = srv.Client()
	g, err := NewAIGateway(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestStreamClaude_DeltasAndUsage(t *testing.T) {
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &gotBody)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\n")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"usage":{"input_tokens":3}}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"Hel"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"lo"}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_delta","usage":{"output_tokens":5}}`+"\n\n")
		fmt.Fprint(w, `data: {"type":"message_stop"}`+"\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderClaude, APIKey: "k", Models: []string{"claude-3"}}, AIGatewayConfig{})

	var deltas []string
	text, tokens, err := g.CompleteStreamForModel(context.Background(), "claude-3", "hi", "sys",
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "Hello" {
		t.Errorf("text = %q, want Hello", text)
	}
	if tokens != 8 {
		t.Errorf("tokens = %d, want 8 (3 in + 5 out)", tokens)
	}
	if len(deltas) != 2 || deltas[0] != "Hel" || deltas[1] != "lo" {
		t.Errorf("deltas = %q, want [Hel lo]", deltas)
	}
	if gotBody["stream"] != true {
		t.Errorf("request stream = %v, want true", gotBody["stream"])
	}
	if gotBody["model"] != "claude-3" {
		t.Errorf("request model = %v", gotBody["model"])
	}
}

func TestStreamOpenAI_DeltasAndUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer key1" {
			t.Errorf("auth = %q", auth)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"role":"assistant"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"foo"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"bar"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"total_tokens":42}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderOpenAI, APIKey: "key1", Models: []string{"gpt-x"}}, AIGatewayConfig{})

	var deltas []string
	text, tokens, err := g.CompleteStreamForModel(context.Background(), "gpt-x", "hi", "sys",
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "foobar" {
		t.Errorf("text = %q", text)
	}
	if tokens != 42 {
		t.Errorf("tokens = %d, want 42 (from usage chunk)", tokens)
	}
	if len(deltas) != 2 {
		t.Errorf("deltas = %q, want 2 fragments", deltas)
	}
}

func TestStreamOpenAI_NoUsageEstimatesTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"`+strings.Repeat("x", 40)+`"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderOpenAI, APIKey: "k", Models: []string{"gpt-x"}}, AIGatewayConfig{})

	_, tokens, err := g.CompleteStreamForModel(context.Background(), "gpt-x", "hi", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if tokens < 10 { // 42 chars / 4 ≈ 10
		t.Errorf("tokens = %d, want a length-based estimate", tokens)
	}
}

func TestStreamOpenAICompat_IncludeUsageOptIn(t *testing.T) {
	// openai/deepseek/openrouter get stream_options.include_usage (exact
	// streamed cost); azure's pinned api-version rejects it, so it must not.
	for _, tc := range []struct {
		provider string
		want     bool
	}{
		{ProviderOpenAI, true},
		{ProviderDeepSeek, true},
		{ProviderOpenRouter, true},
		{ProviderAzureOpenAI, false},
		{ProviderGroq, false},
	} {
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(body, &gotBody)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: [DONE]\n\n")
		}))
		g := streamGateway(t, srv, AIProvider{Name: tc.provider, APIKey: "k", Models: []string{"m1"}}, AIGatewayConfig{})
		_, _, err := g.CompleteStreamForModel(context.Background(), "m1", "hi", "", nil)
		srv.Close()
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		_, has := gotBody["stream_options"]
		if has != tc.want {
			t.Errorf("%s: stream_options present = %v, want %v", tc.provider, has, tc.want)
		}
	}
}

func TestStreamOllama_NDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/generate" {
			t.Errorf("path = %q", r.URL.Path)
		}
		fmt.Fprintln(w, `{"response":"a","done":false}`)
		fmt.Fprintln(w, `{"response":"b","done":false}`)
		fmt.Fprintln(w, `{"done":true,"eval_count":7}`)
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderOllama, Models: []string{"llama3"}}, AIGatewayConfig{})

	var deltas []string
	text, tokens, err := g.CompleteStreamForModel(context.Background(), "llama3", "hi", "sys",
		func(d string) { deltas = append(deltas, d) })
	if err != nil {
		t.Fatal(err)
	}
	if text != "ab" || tokens != 7 {
		t.Errorf("text = %q tokens = %d, want ab / 7", text, tokens)
	}
	if len(deltas) != 2 {
		t.Errorf("deltas = %q", deltas)
	}
}

func TestStreamGemini_SSE(t *testing.T) {
	var gotURL string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"one "}]}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"two"}]}}],"usageMetadata":{"totalTokenCount":9}}`+"\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderGemini, APIKey: "k", Models: []string{"gemini-pro"}}, AIGatewayConfig{})

	text, tokens, err := g.CompleteStreamForModel(context.Background(), "gemini-pro", "hi", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "one two" || tokens != 9 {
		t.Errorf("text = %q tokens = %d, want %q / 9", text, tokens, "one two")
	}
	if !strings.Contains(gotURL, ":streamGenerateContent") || !strings.Contains(gotURL, "alt=sse") {
		t.Errorf("url = %q, want streamGenerateContent with alt=sse", gotURL)
	}
}

func TestStreamAzure_URLAndHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/openai/deployments/dep1/chat/completions") {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("api-version") == "" {
			t.Error("api-version query missing")
		}
		if r.Header.Get("api-key") != "azkey" {
			t.Errorf("api-key header = %q", r.Header.Get("api-key"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"ok"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderAzureOpenAI, APIKey: "azkey", Models: []string{"dep1"}}, AIGatewayConfig{})

	text, _, err := g.CompleteStreamForModel(context.Background(), "dep1", "hi", "sys", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "ok" {
		t.Errorf("text = %q", text)
	}
}

func TestStreamHTTPErrorSurfaces(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderOpenAI, APIKey: "k", Models: []string{"gpt-x"}}, AIGatewayConfig{})

	_, _, err := g.CompleteStreamForModel(context.Background(), "gpt-x", "hi", "", nil)
	if err == nil || !strings.Contains(err.Error(), "status 500") {
		t.Errorf("err = %v, want status 500", err)
	}
}

func TestCompleteStreamForModel_Budget(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"hi"}}]}`+"\n\n")
		fmt.Fprint(w, `data: {"choices":[],"usage":{"total_tokens":100}}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderOpenAI, APIKey: "k", Models: []string{"gpt-x"}}, AIGatewayConfig{
		CostPerToken: map[string]float64{"gpt-x": 0.001},
		DailyBudget:  0.05,
	})

	// First call: 100 tokens × 0.001 = 0.10 → exceeds the 0.05 budget after.
	if _, _, err := g.CompleteStreamForModel(context.Background(), "gpt-x", "hi", "", nil); err != nil {
		t.Fatal(err)
	}
	snap := g.CostToday()
	if snap.RequestsToday != 1 || snap.TotalUSD == 0 {
		t.Errorf("cost snapshot = %+v, want 1 request with recorded cost", snap)
	}
	if _, _, err := g.CompleteStreamForModel(context.Background(), "gpt-x", "hi", "", nil); err != ErrBudgetExceeded {
		t.Errorf("second call err = %v, want ErrBudgetExceeded", err)
	}
}

func TestCompleteStream_FailoverBeforeFirstDelta(t *testing.T) {
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer failing.Close()
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"recovered"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer ok.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{
			{Name: ProviderOpenAI, APIKey: "k", Endpoint: failing.URL, Models: []string{"a"}, Priority: 0},
			{Name: ProviderDeepSeek, APIKey: "k", Endpoint: ok.URL, Models: []string{"b"}, Priority: 1},
		},
		Client: ok.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	text, serr := g.CompleteStream(context.Background(), "hi", "sys", func(d string) { deltas = append(deltas, d) })
	if serr != nil {
		t.Fatal(serr)
	}
	if text != "recovered" || len(deltas) != 1 || deltas[0] != "recovered" {
		t.Errorf("text=%q deltas=%q, want failover to second provider", text, deltas)
	}
}

func TestCompleteStream_NoFailoverAfterFirstDelta(t *testing.T) {
	// First provider emits a delta and then dies mid-stream; failing over
	// would duplicate the emitted output, so the stream must error instead.
	var secondCalled bool
	dying := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"partial"}}]}`+"\n\n")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		panic(http.ErrAbortHandler) // slam the connection mid-stream
	}))
	defer dying.Close()
	second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		secondCalled = true
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer second.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{
			{Name: ProviderOpenAI, APIKey: "k", Endpoint: dying.URL, Models: []string{"a"}, Priority: 0},
			{Name: ProviderDeepSeek, APIKey: "k", Endpoint: second.URL, Models: []string{"b"}, Priority: 1},
		},
		Client: dying.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	var deltas []string
	_, serr := g.CompleteStream(context.Background(), "hi", "sys", func(d string) { deltas = append(deltas, d) })
	if serr == nil {
		t.Fatal("want error after mid-stream death, got nil")
	}
	if secondCalled {
		t.Error("second provider was tried after output had been emitted")
	}
	if len(deltas) != 1 || deltas[0] != "partial" {
		t.Errorf("deltas = %q, want the partial output only", deltas)
	}
}

func TestStreamUnknownModelFallsBackToPrimary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"content":"primary"}}]}`+"\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()
	g := streamGateway(t, srv, AIProvider{Name: ProviderOpenAI, APIKey: "k", Models: []string{"gpt-x"}}, AIGatewayConfig{})

	// A model no provider serves routes to the primary provider (same
	// behavior as CompleteForModel via providerForModel).
	text, _, err := g.CompleteStreamForModel(context.Background(), "some-unknown", "hi", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if text != "primary" {
		t.Errorf("text = %q", text)
	}
}
