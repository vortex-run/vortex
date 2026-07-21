package messaging

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// This file adds true per-token streaming to the AI gateway (AGUI audit item
// C). CompleteStreamForModel mirrors CompleteForModel but invokes onDelta with
// each text fragment as the provider produces it. Providers with a line-based
// streaming wire protocol stream natively:
//
//	claude                              SSE (content_block_delta events)
//	openai / deepseek / groq /
//	azure-openai / openrouter           SSE chat-completions chunks
//	ollama                              NDJSON generate chunks
//	gemini                              SSE streamGenerateContent
//	bedrock                             AWS binary event-stream (ai_bedrock_stream.go)
//
// Unknown providers fall back to the buffered call and emit the full text as
// one delta, so callers need no per-provider special casing.

// maxStreamDuration caps one streaming completion end to end. Streams run on
// the caller's context — a disconnected consumer should abort the upstream
// call, the opposite trade-off from doJSON's decoupling — with this ceiling
// applied when the caller has no earlier deadline.
const maxStreamDuration = 10 * time.Minute

// CompleteStreamForModel routes a completion to the provider serving model,
// invoking onDelta with each text fragment as it arrives, and returns the
// full text plus the provider-reported token count (estimated at ~4 chars per
// token when the provider's stream carries no usage). Like CompleteForModel it
// does NOT fall back across providers — partial output may already have
// reached the caller, so failing over would duplicate it.
func (g *AIGateway) CompleteStreamForModel(ctx context.Context, model, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
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
	if onDelta == nil {
		onDelta = func(string) {}
	}
	text, tokens, err := g.streamProvider(ctx, p, prompt, systemPrompt, onDelta)
	if err != nil {
		return "", 0, err
	}
	if tokens <= 0 {
		tokens = estimateTokens(prompt, text)
	}
	g.recordCost(model, tokens)
	g.mu.Lock()
	g.rolloverLocked()
	g.requestsToday++
	g.mu.Unlock()
	return text, tokens, nil
}

// CompleteStream is Complete's streaming variant: it tries providers in
// priority order (or, when key rotation is enabled, health-scored slots),
// invoking onDelta with each text fragment the active provider produces.
// Failover happens only until the first delta — once output has reached the
// caller, a retry would duplicate it, so the stream fails instead.
func (g *AIGateway) CompleteStream(ctx context.Context, prompt, systemPrompt string, onDelta func(string)) (string, error) {
	if onDelta == nil {
		onDelta = func(string) {}
	}
	if g.keyRotationEnabled() {
		return g.completeStreamRotating(ctx, prompt, systemPrompt, detectRequestType(prompt), onDelta)
	}
	if g.budgetExceeded() {
		return "", ErrBudgetExceeded
	}
	var lastErr error
	for _, p := range g.cfg.Providers {
		emitted := false
		text, tokens, err := g.streamProvider(ctx, p, prompt, systemPrompt, func(d string) {
			emitted = true
			onDelta(d)
		})
		if err != nil {
			lastErr = err
			if emitted {
				return "", err
			}
			continue
		}
		if tokens <= 0 {
			tokens = estimateTokens(prompt, text)
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

// completeStreamRotating mirrors completeRotating for streams: select a slot,
// stream from its provider, record outcomes for health scoring, and fail over
// to the next healthy slot only while nothing has been emitted yet.
func (g *AIGateway) completeStreamRotating(ctx context.Context, prompt, systemPrompt, requestType string, onDelta func(string)) (string, error) {
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
		if tried[slot.ID] >= 3 {
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

		emitted := false
		start := g.now()
		text, tokens, cerr := g.streamProvider(ctx, p, prompt, systemPrompt, func(d string) {
			emitted = true
			onDelta(d)
		})
		latency := g.now().Sub(start).Milliseconds()
		if cerr != nil {
			lastErr = cerr
			router.RecordError(slot.ID, cerr, isRateLimit(cerr))
			if emitted {
				return "", cerr
			}
			continue
		}
		if tokens <= 0 {
			tokens = estimateTokens(prompt, text)
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

// estimateTokens approximates a token count from text length (~4 chars per
// token, the same heuristic as the API layer's splitUsage) for providers whose
// stream reports no usage.
func estimateTokens(prompt, completion string) int {
	n := (len(prompt) + len(completion)) / 4
	if n < 1 {
		n = 1
	}
	return n
}

// streamProvider dispatches to the provider-specific streaming call. Providers
// without a line-based streaming protocol fall back to the buffered call and
// emit the whole reply as a single delta.
func (g *AIGateway) streamProvider(ctx context.Context, p AIProvider, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
	switch p.Name {
	case ProviderClaude:
		return g.streamClaude(ctx, p, prompt, systemPrompt, onDelta)
	case ProviderOpenAI, ProviderDeepSeek, ProviderGroq, ProviderAzureOpenAI, ProviderOpenRouter:
		return g.streamOpenAICompat(ctx, p, prompt, systemPrompt, onDelta)
	case ProviderOllama:
		return g.streamOllama(ctx, p, prompt, systemPrompt, onDelta)
	case ProviderGemini:
		return g.streamGemini(ctx, p, prompt, systemPrompt, onDelta)
	case ProviderBedrock:
		return g.streamBedrock(ctx, p, prompt, systemPrompt, onDelta)
	default: // unknown providers: buffered call, whole reply as one delta
		text, tokens, err := g.callProvider(ctx, p, prompt, systemPrompt)
		if err == nil && text != "" {
			onDelta(text)
		}
		return text, tokens, err
	}
}

// streamClaude streams the Anthropic Messages API (stream: true): text arrives
// as content_block_delta events; usage arrives on message_start (input) and
// message_delta (output).
func (g *AIGateway) streamClaude(ctx context.Context, p AIProvider, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://api.anthropic.com"
	}
	payload := map[string]any{
		"model":      g.modelOf(p),
		"max_tokens": 1000,
		"system":     systemPrompt,
		"messages":   []map[string]any{{"role": "user", "content": prompt}},
		"stream":     true,
	}
	var (
		b      strings.Builder
		inTok  int
		outTok int
	)
	err := g.doStreamLines(ctx, base+"/v1/messages",
		map[string]string{"x-api-key": p.APIKey, "anthropic-version": "2023-06-01"},
		payload, func(line []byte) error {
			data, ok := sseData(line)
			if !ok {
				return nil
			}
			var ev struct {
				Type  string `json:"type"`
				Delta struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"delta"`
				Message struct {
					Usage struct {
						InputTokens int `json:"input_tokens"`
					} `json:"usage"`
				} `json:"message"`
				Usage struct {
					OutputTokens int `json:"output_tokens"`
				} `json:"usage"`
			}
			if json.Unmarshal(data, &ev) != nil {
				return nil // tolerate unknown frames
			}
			switch ev.Type {
			case "message_start":
				inTok = ev.Message.Usage.InputTokens
			case "content_block_delta":
				if ev.Delta.Type == "text_delta" && ev.Delta.Text != "" {
					b.WriteString(ev.Delta.Text)
					onDelta(ev.Delta.Text)
				}
			case "message_delta":
				outTok = ev.Usage.OutputTokens
			}
			return nil
		})
	if err != nil {
		return "", 0, fmt.Errorf("claude: %w", err)
	}
	return b.String(), inTok + outTok, nil
}

// streamOpenAICompat streams any of the OpenAI-compatible providers — they
// share the chat-completions chunk shape and differ only in endpoint, auth,
// and default model. Providers documented to support it get
// stream_options.include_usage so streamed cost tracking is exact; the rest
// are estimated by the caller (Azure's pinned api-version and some compat
// gateways reject the field, so it is opt-in per provider).
func (g *AIGateway) streamOpenAICompat(ctx context.Context, p AIProvider, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
	model := g.modelOf(p)
	if model == "" {
		switch p.Name {
		case ProviderGroq:
			model = "llama-3.1-70b-versatile"
		case ProviderOpenRouter:
			model = "openai/gpt-4o"
		}
	}
	url, headers, errPrefix, includeModel, terr := openaiStreamTarget(p, model)
	if terr != nil {
		return "", 0, terr
	}
	payload := map[string]any{
		"stream": true,
		"messages": []map[string]any{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": prompt},
		},
	}
	if includeModel {
		payload["model"] = model
	}
	switch p.Name {
	case ProviderOpenAI, ProviderDeepSeek, ProviderOpenRouter:
		payload["stream_options"] = map[string]any{"include_usage": true}
	}
	var (
		b      strings.Builder
		tokens int
	)
	err := g.doStreamLines(ctx, url, headers, payload, func(line []byte) error {
		data, ok := sseData(line)
		if !ok || string(data) == "[DONE]" {
			return nil
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			return nil
		}
		if chunk.Usage != nil && chunk.Usage.TotalTokens > 0 {
			tokens = chunk.Usage.TotalTokens
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			b.WriteString(chunk.Choices[0].Delta.Content)
			onDelta(chunk.Choices[0].Delta.Content)
		}
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", errPrefix, err)
	}
	return b.String(), tokens, nil
}

// openaiStreamTarget resolves the URL, headers, and error prefix for one of
// the OpenAI-compatible providers, mirroring the defaults of the buffered
// call* methods. includeModel is false for Azure, whose deployment is encoded
// in the URL rather than the body.
func openaiStreamTarget(p AIProvider, model string) (url string, headers map[string]string, errPrefix string, includeModel bool, err error) {
	bearer := map[string]string{"Authorization": "Bearer " + p.APIKey}
	switch p.Name {
	case ProviderOpenAI:
		base := p.Endpoint
		if base == "" {
			base = "https://api.openai.com"
		}
		return base + "/v1/chat/completions", bearer, "openai", true, nil
	case ProviderDeepSeek:
		base := p.Endpoint
		if base == "" {
			base = "https://api.deepseek.com"
		}
		return base + "/v1/chat/completions", bearer, "deepseek", true, nil
	case ProviderGroq:
		base := p.Endpoint
		if base == "" {
			base = "https://api.groq.com/openai"
		}
		return base + "/v1/chat/completions", bearer, "groq", true, nil
	case ProviderOpenRouter:
		base := p.Endpoint
		if base == "" {
			base = "https://openrouter.ai/api"
		}
		return base + "/v1/chat/completions", map[string]string{
			"Authorization": "Bearer " + p.APIKey,
			"HTTP-Referer":  "https://github.com/vortex-run/vortex",
			"X-Title":       "VORTEX",
		}, "openrouter", true, nil
	case ProviderAzureOpenAI:
		if p.Endpoint == "" {
			return "", nil, "", false, fmt.Errorf("azure-openai: endpoint (resource URL) required")
		}
		if model == "" {
			return "", nil, "", false, fmt.Errorf("azure-openai: deployment name required")
		}
		u := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=2024-02-01",
			strings.TrimRight(p.Endpoint, "/"), model)
		return u, map[string]string{"api-key": p.APIKey}, "azure-openai", false, nil
	}
	return "", nil, "", false, fmt.Errorf("messaging: %q is not an OpenAI-compatible provider", p.Name)
}

// streamOllama streams a local Ollama generate call, which returns NDJSON
// chunks ({"response": "...", "done": false}) rather than SSE.
func (g *AIGateway) streamOllama(ctx context.Context, p AIProvider, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "http://localhost:11434"
	}
	payload := map[string]any{
		"model":  g.modelOf(p),
		"prompt": prompt,
		"system": systemPrompt,
		"stream": true,
	}
	var (
		b      strings.Builder
		tokens int
	)
	err := g.doStreamLines(ctx, base+"/api/generate", nil, payload, func(line []byte) error {
		var chunk struct {
			Response  string `json:"response"`
			Done      bool   `json:"done"`
			EvalCount int    `json:"eval_count"`
		}
		if json.Unmarshal(line, &chunk) != nil {
			return nil
		}
		if chunk.Response != "" {
			b.WriteString(chunk.Response)
			onDelta(chunk.Response)
		}
		if chunk.Done && chunk.EvalCount > 0 {
			tokens = chunk.EvalCount
		}
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("ollama: %w", err)
	}
	return b.String(), tokens, nil
}

// streamGemini streams Gemini's streamGenerateContent endpoint (alt=sse). Each
// SSE chunk carries the same candidates shape as generateContent; the final
// chunk carries usageMetadata.
func (g *AIGateway) streamGemini(ctx context.Context, p AIProvider, prompt, systemPrompt string, onDelta func(string)) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://generativelanguage.googleapis.com"
	}
	model := g.modelOf(p)
	if model == "" {
		model = "gemini-1.5-flash"
	}
	url := fmt.Sprintf("%s/v1beta/models/%s:streamGenerateContent?alt=sse&key=%s", base, model, p.APIKey)
	payload := map[string]any{
		"contents": []map[string]any{
			{"parts": []map[string]any{{"text": prompt}}},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]any{{"text": systemPrompt}},
		},
	}
	var (
		b      strings.Builder
		tokens int
	)
	err := g.doStreamLines(ctx, url, nil, payload, func(line []byte) error {
		data, ok := sseData(line)
		if !ok {
			return nil
		}
		var chunk struct {
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
		if json.Unmarshal(data, &chunk) != nil {
			return nil
		}
		if chunk.UsageMetadata.TotalTokenCount > 0 {
			tokens = chunk.UsageMetadata.TotalTokenCount
		}
		if len(chunk.Candidates) > 0 {
			for _, part := range chunk.Candidates[0].Content.Parts {
				if part.Text != "" {
					b.WriteString(part.Text)
					onDelta(part.Text)
				}
			}
		}
		return nil
	})
	if err != nil {
		return "", 0, fmt.Errorf("gemini: %w", err)
	}
	return b.String(), tokens, nil
}

// doStreamLines POSTs payload to url and invokes onLine for every non-empty
// line of the response body as it arrives. Unlike doJSON it keeps the caller's
// context — a stream's consumer receives output live, so if it disconnects the
// upstream call should abort rather than run to completion — bounded by
// maxStreamDuration when the caller has no earlier deadline. It uses
// streamClient, which has no client-level timeout (that would cut long
// generations mid-stream).
func (g *AIGateway) doStreamLines(ctx context.Context, url string, headers map[string]string, payload any, onLine func(line []byte) error) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, maxStreamDuration)
	defer cancel()
	req, err := http.NewRequestWithContext(callCtx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := g.streamClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("status %d: %s", resp.StatusCode, data)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if err := onLine(line); err != nil {
			return err
		}
	}
	return sc.Err()
}

// sseData returns the payload of an SSE "data:" line. ok is false for event
// names, comments, and other non-data lines.
func sseData(line []byte) (data []byte, ok bool) {
	rest, found := bytes.CutPrefix(line, []byte("data:"))
	if !found {
		return nil, false
	}
	return bytes.TrimSpace(rest), true
}
