package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/config"
)

// recordingCompleter captures what the /v1 handlers pass to the gateway and
// returns a fixed reply.
type recordingCompleter struct {
	model, prompt, system string
	reply                 string
	tokens                int
	err                   error
}

func (c *recordingCompleter) complete(_ context.Context, model, prompt, system string) (string, int, error) {
	c.model, c.prompt, c.system = model, prompt, system
	return c.reply, c.tokens, c.err
}

// newOpenAITestServer builds an in-memory server with the OpenAI surface wired
// and no auth (handlers run directly).
func newOpenAITestServer(t *testing.T, models []string, c *recordingCompleter) *Server {
	t.Helper()
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	s.SetOpenAIGateway(func() []string { return models }, c.complete, nil)
	return s
}

func TestOpenAIModels_ListsProviders(t *testing.T) {
	s := newOpenAITestServer(t, []string{"deepseek-chat", "claude-sonnet-4"}, &recordingCompleter{})
	rec := serve(s, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var got struct {
		Object string `json:"object"`
		Data   []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Object != "list" || len(got.Data) != 2 {
		t.Fatalf("got %+v, want list of 2", got)
	}
	if got.Data[0].ID != "deepseek-chat" || got.Data[0].Object != "model" ||
		got.Data[0].OwnedBy != "vortex" || got.Data[0].Created == 0 {
		t.Errorf("model entry = %+v", got.Data[0])
	}
}

func TestChatCompletions_RoutesAndMatchesSpec(t *testing.T) {
	c := &recordingCompleter{reply: "Hello from VORTEX", tokens: 100}
	s := newOpenAITestServer(t, nil, c)

	body := `{"model":"deepseek-chat","messages":[
	  {"role":"system","content":"be terse"},
	  {"role":"user","content":"this prompt is exactly forty characters!"}]}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	rec := serve(s, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	if c.model != "deepseek-chat" {
		t.Errorf("routed model = %q", c.model)
	}
	if c.system != "be terse" {
		t.Errorf("system prompt = %q", c.system)
	}
	if c.prompt != "this prompt is exactly forty characters!" {
		t.Errorf("prompt = %q (single user turn should pass through bare)", c.prompt)
	}

	var got struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index        int               `json:"index"`
			Message      openaiChatMessage `json:"message"`
			FinishReason string            `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(got.ID, "chatcmpl-") || got.Object != "chat.completion" ||
		got.Created == 0 || got.Model != "deepseek-chat" {
		t.Errorf("envelope = %+v", got)
	}
	if len(got.Choices) != 1 || got.Choices[0].Message.Role != "assistant" ||
		got.Choices[0].Message.Content != "Hello from VORTEX" ||
		got.Choices[0].FinishReason != "stop" {
		t.Errorf("choices = %+v", got.Choices)
	}
	// Provider reported 100 total tokens; the 41-char prompt estimates to 10
	// prompt tokens, leaving 90 for the completion.
	if got.Usage.TotalTokens != 100 || got.Usage.PromptTokens != 10 || got.Usage.CompletionTokens != 90 {
		t.Errorf("usage = %+v, want 10/90/100", got.Usage)
	}
}

func TestChatCompletions_MultiTurnTranscript(t *testing.T) {
	c := &recordingCompleter{reply: "ok"}
	s := newOpenAITestServer(t, nil, c)
	body := `{"model":"m","messages":[
	  {"role":"user","content":"first"},
	  {"role":"assistant","content":"reply"},
	  {"role":"user","content":"second"}]}`
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	want := "user: first\nassistant: reply\nuser: second"
	if c.prompt != want {
		t.Errorf("prompt = %q, want transcript %q", c.prompt, want)
	}
}

func TestChatCompletions_StreamingSSE(t *testing.T) {
	c := &recordingCompleter{reply: strings.Repeat("x", 300), tokens: 50}
	s := newOpenAITestServer(t, nil, c)
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"go"}]}`
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	lines := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
	if lines[len(lines)-1] != "data: [DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", lines[len(lines)-1])
	}
	// Every frame before [DONE] must be valid JSON; the deltas concatenate to
	// the full reply (300 chars splits across two 256-char chunks).
	var content string
	var sawRole, sawStop bool
	for _, ln := range lines[:len(lines)-1] {
		payload := strings.TrimPrefix(ln, "data: ")
		var chunk struct {
			Object  string `json:"object"`
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON %q: %v", payload, err)
		}
		if len(chunk.Choices) == 0 {
			continue // usage frame
		}
		if chunk.Object != "chat.completion.chunk" {
			t.Errorf("chunk object = %q", chunk.Object)
		}
		if chunk.Choices[0].Delta.Role == "assistant" {
			sawRole = true
		}
		content += chunk.Choices[0].Delta.Content
		if fr := chunk.Choices[0].FinishReason; fr != nil && *fr == "stop" {
			sawStop = true
		}
	}
	if !sawRole || !sawStop {
		t.Errorf("sawRole=%v sawStop=%v, want both", sawRole, sawStop)
	}
	if content != c.reply {
		t.Errorf("streamed content = %d chars, want %d", len(content), len(c.reply))
	}
}

// recordingStreamer is an OpenAIStreamFunc test double: it emits scripted
// deltas and records what the handler passed to it.
type recordingStreamer struct {
	model, prompt, system string
	deltas                []string
	tokens                int
	err                   error
}

func (r *recordingStreamer) stream(_ context.Context, model, prompt, system string, onDelta func(string)) (string, int, error) {
	r.model, r.prompt, r.system = model, prompt, system
	for _, d := range r.deltas {
		onDelta(d)
	}
	if r.err != nil {
		return "", 0, r.err
	}
	return strings.Join(r.deltas, ""), r.tokens, nil
}

func TestChatCompletions_StreamLiveForwardsDeltas(t *testing.T) {
	c := &recordingCompleter{reply: "buffered — must not be used"}
	st := &recordingStreamer{deltas: []string{"He", "llo", " world"}, tokens: 40}
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	s.SetOpenAIGateway(func() []string { return nil }, c.complete, st.stream)

	body := `{"model":"m","stream":true,"messages":[{"role":"system","content":"sys"},{"role":"user","content":"go"}]}`
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type = %q", ct)
	}
	if c.model != "" {
		t.Errorf("buffered complete was called (model=%q); live path must bypass it", c.model)
	}
	if st.model != "m" || st.prompt != "go" || st.system != "sys" {
		t.Errorf("stream func got (%q, %q, %q)", st.model, st.prompt, st.system)
	}

	frames := strings.Split(strings.TrimSpace(rec.Body.String()), "\n\n")
	if frames[len(frames)-1] != "data: [DONE]" {
		t.Fatalf("last frame = %q, want [DONE]", frames[len(frames)-1])
	}
	var contents []string
	var sawRole, sawStop, sawUsage bool
	for _, ln := range frames[:len(frames)-1] {
		payload := strings.TrimPrefix(ln, "data: ")
		var chunk struct {
			Choices []struct {
				Delta struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason"`
			} `json:"choices"`
			Usage *struct {
				TotalTokens int `json:"total_tokens"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			t.Fatalf("invalid SSE JSON %q: %v", payload, err)
		}
		if len(chunk.Choices) == 0 {
			if chunk.Usage != nil && chunk.Usage.TotalTokens == 40 {
				sawUsage = true
			}
			continue
		}
		if chunk.Choices[0].Delta.Role == "assistant" {
			sawRole = true
		}
		if chunk.Choices[0].Delta.Content != "" {
			contents = append(contents, chunk.Choices[0].Delta.Content)
		}
		if fr := chunk.Choices[0].FinishReason; fr != nil && *fr == "stop" {
			sawStop = true
		}
	}
	if !sawRole || !sawStop || !sawUsage {
		t.Errorf("sawRole=%v sawStop=%v sawUsage=%v, want all", sawRole, sawStop, sawUsage)
	}
	// Deltas must arrive individually and in order — not concatenated by the
	// handler into one frame (that would be buffering, the bug this fixes).
	if len(contents) != 3 || contents[0] != "He" || contents[1] != "llo" || contents[2] != " world" {
		t.Errorf("delta frames = %q, want [He llo \" world\"]", contents)
	}
}

func TestChatCompletions_StreamLiveErrorMidStream(t *testing.T) {
	st := &recordingStreamer{err: context.DeadlineExceeded}
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	s.SetOpenAIGateway(func() []string { return nil }, (&recordingCompleter{}).complete, st.stream)

	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"go"}]}`
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	// The 200 is committed once streaming starts; the error travels in-band.
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"error"`) || !strings.Contains(out, "deadline") {
		t.Errorf("stream error not reported in-band: %q", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "data: [DONE]") {
		t.Errorf("stream not terminated with [DONE]: %q", out)
	}
}

func TestChatCompletions_StreamFallsBackWhenNoStreamFunc(t *testing.T) {
	// stream:true with a nil stream func must still work (compute-then-chunk).
	c := &recordingCompleter{reply: "fallback reply", tokens: 5}
	s := newOpenAITestServer(t, nil, c)
	body := `{"model":"m","stream":true,"messages":[{"role":"user","content":"go"}]}`
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if out := rec.Body.String(); !strings.Contains(out, "fallback reply") || !strings.Contains(out, "data: [DONE]") {
		t.Errorf("buffered fallback stream malformed: %q", out)
	}
}

func TestChatCompletions_Validation(t *testing.T) {
	s := newOpenAITestServer(t, nil, &recordingCompleter{reply: "x"})
	cases := map[string]string{
		"missing model":    `{"messages":[{"role":"user","content":"hi"}]}`,
		"missing messages": `{"model":"m"}`,
		"invalid JSON":     `{not json`,
	}
	for name, body := range cases {
		rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
		var got struct {
			Error struct {
				Message string `json:"message"`
				Type    string `json:"type"`
			} `json:"error"`
		}
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil || got.Error.Type != "invalid_request_error" {
			t.Errorf("%s: error envelope = %+v (%v)", name, got, err)
		}
	}
}

func TestChatCompletions_GatewayErrorIs502(t *testing.T) {
	c := &recordingCompleter{err: context.DeadlineExceeded}
	s := newOpenAITestServer(t, nil, c)
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)))
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", rec.Code)
	}
}

func TestOpenAIEndpoints_503WhenUnwired(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/models", nil),
		httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{}`)),
		httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{}`)),
	} {
		if rec := serve(s, req); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d, want 503", req.Method, req.URL.Path, rec.Code)
		}
	}
}

func TestOpenAIEndpoints_AuthBearerAndMissing(t *testing.T) {
	holder := config.NewHolder(&config.Config{})
	s := New("127.0.0.1:0", holder, "test", discardLogger())
	keys := auth.NewAPIKeyStore()
	rbac := auth.NewRBAC()
	_, secret, err := keys.Issue("compat-user", "default", []auth.Role{auth.RoleOperator}, "token", 0)
	if err != nil {
		t.Fatal(err)
	}
	s.SetAuth(auth.NewAuthMiddleware(keys, nil, rbac), keys, rbac)
	s.SetOpenAIGateway(func() []string { return []string{"m"} },
		(&recordingCompleter{reply: "ok"}).complete, nil)

	// No credential — 401 even from localhost (data plane).
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	if rec := serve(s, req); rec.Code != http.StatusUnauthorized {
		t.Errorf("no key = %d, want 401", rec.Code)
	}

	// Authorization: Bearer with the SAME key as X-API-Key (unified auth).
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	req = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+secret)
	if rec := serve(s, req); rec.Code != http.StatusOK {
		t.Errorf("bearer key = %d, want 200 (body=%s)", rec.Code, rec.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("X-API-Key", secret)
	if rec := serve(s, req); rec.Code != http.StatusOK {
		t.Errorf("x-api-key = %d, want 200", rec.Code)
	}
}

func TestResponsesAPI_StringAndArrayInput(t *testing.T) {
	c := &recordingCompleter{reply: "answered", tokens: 8}
	s := newOpenAITestServer(t, nil, c)

	rec := serve(s, httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":"plain question","instructions":"be brief"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d (body=%s)", rec.Code, rec.Body)
	}
	if c.prompt != "plain question" || c.system != "be brief" {
		t.Errorf("gateway got prompt=%q system=%q", c.prompt, c.system)
	}
	var got struct {
		Object string `json:"object"`
		Status string `json:"status"`
		Output []struct {
			Type    string `json:"type"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Object != "response" || got.Status != "completed" ||
		len(got.Output) != 1 || got.Output[0].Content[0].Text != "answered" {
		t.Errorf("response = %+v", got)
	}

	// Array-form input with content parts.
	rec = serve(s, httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":"part form"}]}]}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("array input status = %d", rec.Code)
	}
	if c.prompt != "part form" {
		t.Errorf("array input prompt = %q", c.prompt)
	}

	// Missing input → 400.
	rec = serve(s, httptest.NewRequest(http.MethodPost, "/v1/responses",
		strings.NewReader(`{"model":"m"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("missing input = %d, want 400", rec.Code)
	}
}
