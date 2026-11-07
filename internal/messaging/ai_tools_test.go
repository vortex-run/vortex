package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var readFileTool = ToolDef{
	Name: "read_file", Description: "Read a file.",
	Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}},
}

// fakeProvider serves one canned reply and captures the request body.
func fakeProvider(t *testing.T, reply string, got *map[string]any) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if got != nil {
			_ = json.Unmarshal(body, got)
			(*got)["_path"] = r.URL.Path
		}
		_, _ = io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func toolGateway(t *testing.T, providers ...AIProvider) *AIGateway {
	t.Helper()
	g, err := NewAIGateway(AIGatewayConfig{Providers: providers})
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// A follow-up turn: the model called read_file, the runtime answered.
var followUp = []ChatMessage{
	{Role: "user", Content: "what's in go.mod?"},
	{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_1", Name: "read_file", Arguments: map[string]any{"path": "go.mod"}}}},
	{Role: "tool", ToolCallID: "call_1", ToolName: "read_file", Content: "module x"},
}

func TestChatWithTools_OpenAICompat(t *testing.T) {
	var req map[string]any
	srv := fakeProvider(t, `{"choices":[{"message":{"content":"","tool_calls":[{"id":"call_9","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"main.go\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"total_tokens":42}}`, &req)
	g := toolGateway(t, AIProvider{Name: ProviderOpenAI, APIKey: "k", Endpoint: srv.URL, Models: []string{"gpt"}})

	res, err := g.ChatWithTools(context.Background(), "sys", followUp, []ToolDef{readFileTool})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "tool_calls" || len(res.ToolCalls) != 1 || res.ToolCalls[0].Name != "read_file" ||
		res.ToolCalls[0].Arguments["path"] != "main.go" || res.ToolCalls[0].ID != "call_9" {
		t.Fatalf("result = %+v", res)
	}
	if req["_path"] != "/v1/chat/completions" {
		t.Errorf("path = %v", req["_path"])
	}
	tools, _ := req["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools sent = %v", req["tools"])
	}
	msgs, _ := req["messages"].([]any)
	if len(msgs) != 4 { // system + 3
		t.Fatalf("messages = %v", msgs)
	}
	asst := msgs[2].(map[string]any)
	call := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if _, isString := call["arguments"].(string); !isString {
		t.Errorf("OpenAI wants arguments as a JSON string, got %T", call["arguments"])
	}
	if tm := msgs[3].(map[string]any); tm["role"] != "tool" || tm["tool_call_id"] != "call_1" {
		t.Errorf("tool result turn = %v", tm)
	}
}

func TestChatWithTools_Anthropic(t *testing.T) {
	var req map[string]any
	srv := fakeProvider(t, `{"content":[{"type":"text","text":"Reading it."},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"go.mod"}}],"stop_reason":"tool_use","usage":{"input_tokens":10,"output_tokens":5}}`, &req)
	g := toolGateway(t, AIProvider{Name: ProviderClaude, APIKey: "k", Endpoint: srv.URL, Models: []string{"claude-x"}})

	res, err := g.ChatWithTools(context.Background(), "sys", followUp, []ToolDef{readFileTool})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "tool_calls" || res.Text != "Reading it." || len(res.ToolCalls) != 1 ||
		res.ToolCalls[0].ID != "toolu_1" || res.ToolCalls[0].Arguments["path"] != "go.mod" || res.Tokens != 15 {
		t.Fatalf("result = %+v", res)
	}
	if req["system"] != "sys" {
		t.Errorf("system = %v", req["system"])
	}
	tool := req["tools"].([]any)[0].(map[string]any)
	if tool["input_schema"] == nil {
		t.Errorf("Anthropic tools need input_schema: %v", tool)
	}
	msgs := req["messages"].([]any)
	asst := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)
	if asst["type"] != "tool_use" || asst["id"] != "call_1" {
		t.Errorf("assistant tool_use block = %v", asst)
	}
	result := msgs[2].(map[string]any)
	block := result["content"].([]any)[0].(map[string]any)
	if result["role"] != "user" || block["type"] != "tool_result" || block["tool_use_id"] != "call_1" {
		t.Errorf("tool result must be a user turn with a tool_result block: %v", result)
	}
}

func TestChatWithTools_Ollama(t *testing.T) {
	var req map[string]any
	srv := fakeProvider(t, `{"message":{"role":"assistant","content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"x.go"}}}]},"done_reason":"stop","eval_count":3,"prompt_eval_count":4}`, &req)
	g := toolGateway(t, AIProvider{Name: ProviderOllama, Endpoint: srv.URL, Models: []string{"qwen"}})

	res, err := g.ChatWithTools(context.Background(), "", followUp, []ToolDef{readFileTool})
	if err != nil {
		t.Fatal(err)
	}
	if res.Stop != "tool_calls" || len(res.ToolCalls) != 1 || res.ToolCalls[0].Arguments["path"] != "x.go" || res.ToolCalls[0].ID == "" {
		t.Fatalf("result = %+v", res)
	}
	if req["_path"] != "/api/chat" || req["stream"] != false {
		t.Errorf("request = %v", req)
	}
	asst := req["messages"].([]any)[1].(map[string]any)
	fn := asst["tool_calls"].([]any)[0].(map[string]any)["function"].(map[string]any)
	if _, isObj := fn["arguments"].(map[string]any); !isObj {
		t.Errorf("Ollama wants arguments as an object, got %T", fn["arguments"])
	}
}

func TestChatWithTools_SkipsUnsupportedAndFallsBack(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	good := fakeProvider(t, `{"choices":[{"message":{"content":"done"},"finish_reason":"stop"}],"usage":{}}`, nil)
	g := toolGateway(t,
		AIProvider{Name: "custom", APIKey: "k", Priority: 1},                          // no native tools: skipped
		AIProvider{Name: ProviderOpenAI, APIKey: "k", Endpoint: bad.URL, Priority: 2}, // fails
		AIProvider{Name: ProviderGroq, APIKey: "k", Endpoint: good.URL, Priority: 3},  // answers
	)
	res, err := g.ChatWithTools(context.Background(), "", []ChatMessage{{Role: "user", Content: "hi"}}, nil)
	if err != nil || res.Text != "done" || res.Provider != ProviderGroq {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func TestChatWithTools_OnlyUnsupportedProviders(t *testing.T) {
	g := toolGateway(t, AIProvider{Name: "custom", APIKey: "k"})
	if _, err := g.ChatWithTools(context.Background(), "", []ChatMessage{{Role: "user", Content: "hi"}}, nil); !errors.Is(err, ErrToolsUnsupported) {
		t.Errorf("err = %v, want ErrToolsUnsupported", err)
	}
}

func TestChatWithTools_MalformedArgumentsKept(t *testing.T) {
	srv := fakeProvider(t, `{"choices":[{"message":{"tool_calls":[{"id":"c","function":{"name":"read_file","arguments":"{not json"}}]}}]}`, nil)
	g := toolGateway(t, AIProvider{Name: ProviderOpenAI, APIKey: "k", Endpoint: srv.URL})
	res, err := g.ChatWithTools(context.Background(), "", []ChatMessage{{Role: "user", Content: "hi"}}, []ToolDef{readFileTool})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.ToolCalls) != 1 || res.ToolCalls[0].Arguments != nil || res.ToolCalls[0].RawArgs != "{not json" {
		t.Errorf("malformed call should be kept with RawArgs for the validator: %+v", res.ToolCalls)
	}
}

func TestChatWithTools_Gemini(t *testing.T) {
	var req map[string]any
	var gotKey string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		req["_path"] = r.URL.Path
		gotKey = r.Header.Get("x-goog-api-key")
		_, _ = io.WriteString(w, `{"candidates":[{"content":{"role":"model","parts":[
			{"text":"thinking...","thought":true},
			{"text":"Let me look. "},
			{"functionCall":{"name":"read_file","args":{"path":"main.go"}},"thoughtSignature":"sig-1"}]},
			"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":33}}`)
	}))
	t.Cleanup(srv.Close)
	g := toolGateway(t, AIProvider{Name: ProviderGemini, APIKey: "gk", Endpoint: srv.URL, Models: []string{"gemini-x"}})

	history := append([]ChatMessage(nil), followUp...)
	history[1].ToolCalls = []ToolCall{{ID: "call_1", Name: "read_file", Arguments: map[string]any{"path": "go.mod"}, Opaque: "sig-0"}}
	tools := []ToolDef{{Name: "read_file", Description: "Read", Parameters: map[string]any{
		"type": "object", "additionalProperties": false, "$schema": "x",
		"properties": map[string]any{"path": map[string]any{"type": "string", "description": "p"}},
		"required":   []string{"path"},
	}}}
	res, err := g.ChatWithTools(context.Background(), "sys", history, tools)
	if err != nil {
		t.Fatal(err)
	}
	if gotKey != "gk" || req["_path"] != "/v1beta/models/gemini-x:generateContent" {
		t.Errorf("key=%q path=%v", gotKey, req["_path"])
	}
	if res.Stop != "tool_calls" || res.Text != "Let me look. " || res.Tokens != 33 || len(res.ToolCalls) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if c := res.ToolCalls[0]; c.Name != "read_file" || c.Arguments["path"] != "main.go" || c.Opaque != "sig-1" || c.ID == "" {
		t.Errorf("call = %+v", c)
	}

	// Request shape: system instruction, model turn echoing the signature,
	// tool result as a functionResponse, schema stripped to Gemini's subset.
	if req["systemInstruction"] == nil {
		t.Error("no systemInstruction")
	}
	contents := req["contents"].([]any)
	if len(contents) != 3 {
		t.Fatalf("contents = %v", contents)
	}
	model := contents[1].(map[string]any)
	part := model["parts"].([]any)[0].(map[string]any)
	if model["role"] != "model" || part["thoughtSignature"] != "sig-0" || part["functionCall"].(map[string]any)["name"] != "read_file" {
		t.Errorf("model turn = %v", model)
	}
	fr := contents[2].(map[string]any)["parts"].([]any)[0].(map[string]any)["functionResponse"].(map[string]any)
	if fr["name"] != "read_file" || fr["response"].(map[string]any)["content"] != "module x" {
		t.Errorf("function response = %v", fr)
	}
	decl := req["tools"].([]any)[0].(map[string]any)["functionDeclarations"].([]any)[0].(map[string]any)
	params := decl["parameters"].(map[string]any)
	if _, bad := params["additionalProperties"]; bad {
		t.Error("additionalProperties sent to Gemini")
	}
	if _, bad := params["$schema"]; bad {
		t.Error("$schema sent to Gemini")
	}
	if params["properties"].(map[string]any)["path"].(map[string]any)["type"] != "string" {
		t.Errorf("params = %v", params)
	}
}

func TestGeminiContents_GroupsParallelResults(t *testing.T) {
	got := geminiContents([]ChatMessage{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []ToolCall{{Name: "a"}, {Name: "b"}}},
		{Role: "tool", ToolName: "a", Content: "1"},
		{Role: "tool", ToolName: "b", Content: "2"},
		{Role: "user", Content: "next"},
	})
	if len(got) != 4 {
		t.Fatalf("contents = %v", got)
	}
	if parts := got[2]["parts"].([]map[string]any); len(parts) != 2 {
		t.Errorf("parallel results should share one turn: %v", got[2])
	}
	if args := got[1]["parts"].([]map[string]any)[0]["functionCall"].(map[string]any)["args"]; args == nil {
		t.Error("nil args must be sent as {}")
	}
}

func TestChatWithTools_BedrockConverse(t *testing.T) {
	var req map[string]any
	var uri, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		uri, auth = r.RequestURI, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"output":{"message":{"role":"assistant","content":[
			{"text":"Checking. "},
			{"toolUse":{"toolUseId":"tu_1","name":"read_file","input":{"path":"main.go"}}}]}},
			"stopReason":"tool_use","usage":{"inputTokens":10,"outputTokens":5,"totalTokens":15}}`)
	}))
	t.Cleanup(srv.Close)
	g := toolGateway(t, AIProvider{Name: ProviderBedrock, APIKey: "AKIA:secret", Endpoint: "us-east-1",
		Models: []string{"anthropic.claude-3-5-sonnet-20240620-v1:0"}})
	g.client = &http.Client{Transport: rewriteTransport{to: srv.URL, base: srv.Client().Transport}}

	res, err := g.ChatWithTools(context.Background(), "sys", followUp, []ToolDef{readFileTool})
	if err != nil {
		t.Fatal(err)
	}
	if uri != "/model/anthropic.claude-3-5-sonnet-20240620-v1%3A0/converse" || !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Errorf("uri=%q auth=%q", uri, auth)
	}
	if res.Stop != "tool_calls" || res.Text != "Checking. " || res.Tokens != 15 || len(res.ToolCalls) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if c := res.ToolCalls[0]; c.ID != "tu_1" || c.Name != "read_file" || c.Arguments["path"] != "main.go" {
		t.Errorf("call = %+v", c)
	}
	msgs := req["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("messages = %v", msgs)
	}
	use := msgs[1].(map[string]any)["content"].([]any)[0].(map[string]any)["toolUse"].(map[string]any)
	if use["toolUseId"] != "call_1" || use["name"] != "read_file" {
		t.Errorf("toolUse = %v", use)
	}
	tr := msgs[2].(map[string]any)["content"].([]any)[0].(map[string]any)["toolResult"].(map[string]any)
	if tr["toolUseId"] != "call_1" || tr["content"].([]any)[0].(map[string]any)["text"] != "module x" {
		t.Errorf("toolResult = %v", tr)
	}
	spec := req["toolConfig"].(map[string]any)["tools"].([]any)[0].(map[string]any)["toolSpec"].(map[string]any)
	if spec["name"] != "read_file" || spec["inputSchema"].(map[string]any)["json"] == nil {
		t.Errorf("toolSpec = %v", spec)
	}
	if req["system"].([]any)[0].(map[string]any)["text"] != "sys" {
		t.Errorf("system = %v", req["system"])
	}
}

// TestChatWithTools_OllamaModelWithoutTools: Ollama's 400 for a model with
// no tool support is reported as ErrToolsUnsupported, so /agent can fall
// back to the text protocol.
func TestChatWithTools_OllamaModelWithoutTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":"registry.ollama.ai/library/gemma:2b does not support tools"}`)
	}))
	t.Cleanup(srv.Close)
	g := toolGateway(t, AIProvider{Name: ProviderOllama, Endpoint: srv.URL, Models: []string{"gemma:2b"}})
	_, err := g.ChatWithTools(context.Background(), "", []ChatMessage{{Role: "user", Content: "hi"}}, []ToolDef{readFileTool})
	if !errors.Is(err, ErrToolsUnsupported) {
		t.Errorf("err = %v, want ErrToolsUnsupported", err)
	}
}
