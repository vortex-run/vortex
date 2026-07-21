package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// OpenAI-compatible endpoints (upgrade 3). Any tool that speaks the OpenAI
// API — Claude Code, Aider, Cline, Cursor — can point OPENAI_BASE_URL at
// VORTEX and get its provider routing, fallback, budget, and cost tracking:
//
//	export OPENAI_BASE_URL=http://localhost:9090/v1
//	export OPENAI_API_KEY=<your-vortex-key>
//
// Auth is unified: the middleware accepts the same key via either
// Authorization: Bearer or X-API-Key.

// OpenAICompleteFunc routes one completion to the AI gateway for a specific
// model, returning the reply text and the provider-reported total token count.
// A client-requested generation cap travels on the context (WithMaxTokens) so
// this signature stays stable across the gateway's provider call chain.
type OpenAICompleteFunc func(ctx context.Context, model, prompt, systemPrompt string) (string, int, error)

// OpenAIStreamFunc is the streaming variant: it invokes onDelta with each text
// fragment as the provider produces it and returns the full text and token
// count (AGUI audit item C — true token streaming).
type OpenAIStreamFunc func(ctx context.Context, model, prompt, systemPrompt string, onDelta func(string)) (string, int, error)

// maxTokensKey carries a client-requested generation cap through the context
// to the AI gateway. The api package cannot import messaging (the gateway is
// wired in via callbacks to keep the packages decoupled), so the wiring in
// start.go translates this value onto the gateway's own context key.
type maxTokensKey struct{}

// WithMaxTokens returns a context carrying a client-requested generation cap.
// n <= 0 is ignored, leaving the gateway's default in force.
func WithMaxTokens(ctx context.Context, n int) context.Context {
	if n <= 0 {
		return ctx
	}
	return context.WithValue(ctx, maxTokensKey{}, n)
}

// MaxTokensFrom returns the client-requested generation cap carried by ctx.
// Used by the gateway wiring to forward the value into the messaging layer.
func MaxTokensFrom(ctx context.Context) int {
	n, _ := ctx.Value(maxTokensKey{}).(int)
	return n
}

// SetOpenAIGateway wires the /v1/* OpenAI-compatible endpoints to the AI
// gateway: models lists servable model IDs, complete routes a request to the
// provider serving the requested model, and stream is its token-streaming
// variant (nil degrades stream:true requests to buffered compute-then-chunk).
func (s *Server) SetOpenAIGateway(models func() []string, complete OpenAICompleteFunc, stream OpenAIStreamFunc) {
	s.openaiModels = models
	s.openaiComplete = complete
	s.openaiStream = stream
}

// openaiMaxBody bounds /v1/* request bodies (1 MB, matching the management
// API's input-validation budget).
const openaiMaxBody = 1 << 20

// openaiError writes an OpenAI-format error envelope.
func openaiError(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{"message": msg, "type": errType, "code": nil},
	})
}

// openaiChatMessage is one OpenAI chat message (request or response shape).
type openaiChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// openaiChatRequest is the standard chat-completions request body. Tool
// definitions are accepted for wire compatibility but not yet forwarded —
// the gateway's providers expose text completion only.
type openaiChatRequest struct {
	Model       string              `json:"model"`
	Messages    []openaiChatMessage `json:"messages"`
	Temperature *float64            `json:"temperature,omitempty"`
	MaxTokens   int                 `json:"max_tokens,omitempty"`
	Stream      bool                `json:"stream"`
	Tools       []json.RawMessage   `json:"tools,omitempty"`
}

// handleOpenAIModels serves GET /v1/models: one entry per servable model.
func (s *Server) handleOpenAIModels(w http.ResponseWriter, _ *http.Request) {
	if s.openaiModels == nil {
		openaiError(w, http.StatusServiceUnavailable, "server_error", "AI gateway not configured")
		return
	}
	created := s.startTime.Unix()
	data := []map[string]any{}
	for _, id := range s.openaiModels() {
		data = append(data, map[string]any{
			"id": id, "object": "model", "created": created, "owned_by": "vortex",
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

// handleChatCompletions serves POST /v1/chat/completions in both buffered and
// SSE-streaming forms.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if s.openaiComplete == nil {
		openaiError(w, http.StatusServiceUnavailable, "server_error", "AI gateway not configured")
		return
	}
	// Generations (buffered or streamed) can outlive the 60s WriteTimeout;
	// the gateway bounds the call instead.
	allowLongResponse(w)
	var req openaiChatRequest
	r.Body = http.MaxBytesReader(w, r.Body, openaiMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	if len(req.Messages) == 0 {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "messages is required")
		return
	}

	prompt, system := flattenChatMessages(req.Messages)
	id := "chatcmpl-" + newCorrelationID()
	created := time.Now().Unix()
	// Honour the client's max_tokens: it was previously parsed and dropped, so
	// every completion silently used the gateway's cap. Applied to the
	// streaming path too — a streamed generation truncates just as silently.
	ctx := WithMaxTokens(r.Context(), req.MaxTokens)

	// True token streaming (AGUI item C): forward provider deltas as they
	// arrive. Falls back to compute-then-chunk when no stream func is wired.
	if req.Stream && s.openaiStream != nil {
		s.streamChatCompletionLive(w, r.WithContext(ctx), id, req.Model, created, prompt, system)
		return
	}

	text, tokens, err := s.openaiComplete(ctx, req.Model, prompt, system)
	if err != nil {
		openaiError(w, http.StatusBadGateway, "server_error", err.Error())
		return
	}
	promptTokens, completionTokens := splitUsage(prompt, text, tokens)

	if req.Stream {
		s.streamChatCompletion(w, id, req.Model, created, text, promptTokens, completionTokens)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": id, "object": "chat.completion", "created": created, "model": req.Model,
		"choices": []map[string]any{{
			"index":         0,
			"message":       openaiChatMessage{Role: "assistant", Content: text},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	})
}

// sseChatHeaders sets the SSE response headers and returns a chunk writer that
// emits one chat.completion.chunk frame per call (flushing when supported).
func sseChatHeaders(w http.ResponseWriter, id, model string, created int64) func(delta map[string]any, finish any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)
	return func(delta map[string]any, finish any) {
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		b, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if flusher != nil {
			flusher.Flush()
		}
	}
}

// writeChatUsageAndDone writes the final usage frame
// (stream_options.include_usage shape) and the [DONE] terminator.
func writeChatUsageAndDone(w http.ResponseWriter, id, model string, created int64, promptTokens, completionTokens int) {
	usage := map[string]any{
		"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
		"choices": []map[string]any{},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	b, _ := json.Marshal(usage)
	fmt.Fprintf(w, "data: %s\n\n", b)
	fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// streamChatCompletionLive streams provider deltas to the client as OpenAI SSE
// chunks the moment they arrive — true token streaming (AGUI audit item C).
// An upstream error is reported as an SSE error object: the 200 status is
// already committed once streaming starts, matching OpenAI's own mid-stream
// error behavior.
func (s *Server) streamChatCompletionLive(w http.ResponseWriter, r *http.Request, id, model string, created int64, prompt, system string) {
	writeChunk := sseChatHeaders(w, id, model, created)
	writeChunk(map[string]any{"role": "assistant"}, nil)

	text, tokens, err := s.openaiStream(r.Context(), model, prompt, system, func(delta string) {
		writeChunk(map[string]any{"content": delta}, nil)
	})
	if err != nil {
		b, _ := json.Marshal(map[string]any{
			"error": map[string]any{"message": err.Error(), "type": "server_error", "code": nil},
		})
		fmt.Fprintf(w, "data: %s\n\n", b)
		fmt.Fprint(w, "data: [DONE]\n\n")
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	writeChunk(map[string]any{}, "stop")
	promptTokens, completionTokens := splitUsage(prompt, text, tokens)
	writeChatUsageAndDone(w, id, model, created, promptTokens, completionTokens)
}

// streamChatCompletion emits an already-computed reply as OpenAI SSE delta
// chunks — the fallback when no token-streaming gateway func is wired. Clients
// that require stream:true work unchanged; they just receive the reply in
// fixed-size chunks after it completes.
func (s *Server) streamChatCompletion(w http.ResponseWriter, id, model string, created int64, text string, promptTokens, completionTokens int) {
	writeChunk := sseChatHeaders(w, id, model, created)
	writeChunk(map[string]any{"role": "assistant"}, nil)
	// Chunk the reply so clients exercise their incremental render path.
	const chunkSize = 256
	for i := 0; i < len(text); i += chunkSize {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		writeChunk(map[string]any{"content": text[i:end]}, nil)
	}
	writeChunk(map[string]any{}, "stop")
	writeChatUsageAndDone(w, id, model, created, promptTokens, completionTokens)
}

// openaiResponsesRequest is the (minimal) OpenAI Responses API request shape:
// input is either a string or an array of {role, content} items, instructions
// plays the system-prompt role.
type openaiResponsesRequest struct {
	Model        string          `json:"model"`
	Input        json.RawMessage `json:"input"`
	Instructions string          `json:"instructions,omitempty"`
}

// handleResponses serves POST /v1/responses (the newer OpenAI Responses API),
// mapping onto the same gateway as chat completions.
func (s *Server) handleResponses(w http.ResponseWriter, r *http.Request) {
	if s.openaiComplete == nil {
		openaiError(w, http.StatusServiceUnavailable, "server_error", "AI gateway not configured")
		return
	}
	// Buffered AI generation; may exceed the server's 60s WriteTimeout.
	allowLongResponse(w)
	var req openaiResponsesRequest
	r.Body = http.MaxBytesReader(w, r.Body, openaiMaxBody)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "model is required")
		return
	}
	prompt, err := flattenResponsesInput(req.Input)
	if err != nil || strings.TrimSpace(prompt) == "" {
		openaiError(w, http.StatusBadRequest, "invalid_request_error", "input is required")
		return
	}

	text, tokens, err := s.openaiComplete(r.Context(), req.Model, prompt, req.Instructions)
	if err != nil {
		openaiError(w, http.StatusBadGateway, "server_error", err.Error())
		return
	}
	promptTokens, completionTokens := splitUsage(prompt, text, tokens)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id":         "resp_" + newCorrelationID(),
		"object":     "response",
		"created_at": time.Now().Unix(),
		"model":      req.Model,
		"status":     "completed",
		"output": []map[string]any{{
			"type": "message", "role": "assistant",
			"content": []map[string]any{{"type": "output_text", "text": text}},
		}},
		"usage": map[string]any{
			"input_tokens":  promptTokens,
			"output_tokens": completionTokens,
			"total_tokens":  promptTokens + completionTokens,
		},
	})
}

// flattenChatMessages converts an OpenAI message list to the gateway's
// (prompt, systemPrompt) pair: system messages join into the system prompt; a
// single user turn passes through verbatim; longer conversations become a
// role-tagged transcript.
func flattenChatMessages(msgs []openaiChatMessage) (prompt, system string) {
	var sys, turns []string
	for _, m := range msgs {
		if m.Role == "system" || m.Role == "developer" {
			sys = append(sys, m.Content)
			continue
		}
		turns = append(turns, m.Role+": "+m.Content)
	}
	system = strings.Join(sys, "\n")
	if len(turns) == 1 {
		// Single turn: strip the role tag so the provider sees the bare prompt.
		_, content, _ := strings.Cut(turns[0], ": ")
		return content, system
	}
	return strings.Join(turns, "\n"), system
}

// flattenResponsesInput accepts the Responses API input field: a JSON string,
// or an array of {role, content} items (content a string or an array of
// {type, text} parts).
func flattenResponsesInput(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", nil
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		return str, nil
	}
	var items []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(raw, &items); err != nil {
		return "", err
	}
	var turns []string
	for _, it := range items {
		var content string
		if err := json.Unmarshal(it.Content, &content); err != nil {
			var parts []struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(it.Content, &parts); err != nil {
				continue
			}
			var texts []string
			for _, p := range parts {
				texts = append(texts, p.Text)
			}
			content = strings.Join(texts, "\n")
		}
		turns = append(turns, it.Role+": "+content)
	}
	if len(turns) == 1 {
		_, content, _ := strings.Cut(turns[0], ": ")
		return content, nil
	}
	return strings.Join(turns, "\n"), nil
}

// splitUsage apportions a provider's total token count into prompt/completion
// parts. Providers report only a total, so the split is estimated from text
// length (~4 chars per token); when no total is reported both sides are
// estimated.
func splitUsage(prompt, completion string, total int) (promptTokens, completionTokens int) {
	promptEst := len(prompt) / 4
	completionEst := len(completion) / 4
	if total <= 0 {
		return promptEst, completionEst
	}
	if promptEst >= total {
		promptEst = total
	}
	return promptEst, total - promptEst
}
