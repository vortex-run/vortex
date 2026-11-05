package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/vortex-run/vortex/internal/messaging"
)

// fakeOllamaChat answers /api/chat like a model: a capable one calls the
// tool it's offered and then reports the tool result; a text-only one never
// calls tools.
func fakeOllamaChat(t *testing.T, capable bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		msg := map[string]any{"role": "assistant", "content": "I would call a tool."}
		if capable {
			var toolResult string
			for _, m := range req.Messages {
				if m.Role == "tool" {
					toolResult = m.Content
				}
			}
			switch {
			case toolResult != "":
				msg = map[string]any{"role": "assistant", "content": "It returned " + strings.SplitN(toolResult, "\n", 2)[0]}
			case len(req.Tools) > 0 && req.Tools[0].Function.Name == "add":
				msg["tool_calls"] = []any{map[string]any{"function": map[string]any{"name": "add", "arguments": map[string]any{"a": 2, "b": 3}}}}
			default:
				msg["tool_calls"] = []any{map[string]any{"function": map[string]any{"name": "echo", "arguments": map[string]any{"text": "ping"}}}}
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"message": msg, "done": true, "eval_count": 3})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func probeGateway(t *testing.T, srvs map[string]*httptest.Server) *messaging.AIGateway {
	t.Helper()
	var ps []messaging.AIProvider
	prio := 0
	for _, url := range []string{srvs["a"].URL, srvs["b"].URL} {
		ps = append(ps, messaging.AIProvider{Name: messaging.ProviderOllama, Endpoint: url, Models: []string{"m"}, Priority: prio})
		prio++
	}
	gw, err := messaging.NewAIGateway(messaging.AIGatewayConfig{Providers: ps})
	if err != nil {
		t.Fatal(err)
	}
	return gw
}

func TestAIProbe_ReportsEachProvider(t *testing.T) {
	gw := probeGateway(t, map[string]*httptest.Server{"a": fakeOllamaChat(t, true), "b": fakeOllamaChat(t, false)})
	var out bytes.Buffer
	err := runAIProbe(context.Background(), &out, gw.EachProvider(), "")
	if !errors.Is(err, errProbe) {
		t.Fatalf("err = %v, want errProbe (second provider can't call tools)", err)
	}
	text := out.String()
	blocks := strings.Split(text, "ollama (m)")
	if len(blocks) != 3 {
		t.Fatalf("both providers should be listed:\n%s", text)
	}
	if strings.Contains(blocks[1], "FAIL") {
		t.Errorf("capable provider failed a check:\n%s", text)
	}
	if !strings.Contains(blocks[2], "FAIL calls a tool") {
		t.Errorf("text-only provider not reported:\n%s", text)
	}
}

func TestAIProbe_AllPass(t *testing.T) {
	gw := probeGateway(t, map[string]*httptest.Server{"a": fakeOllamaChat(t, true), "b": fakeOllamaChat(t, true)})
	var out bytes.Buffer
	if err := runAIProbe(context.Background(), &out, gw.EachProvider(), ""); err != nil {
		t.Fatalf("err = %v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "FAIL") {
		t.Errorf("unexpected failure:\n%s", out.String())
	}
}

func TestAIProbe_UnknownProvider(t *testing.T) {
	gw := probeGateway(t, map[string]*httptest.Server{"a": fakeOllamaChat(t, true), "b": fakeOllamaChat(t, true)})
	if err := runAIProbe(context.Background(), &bytes.Buffer{}, gw.EachProvider(), "nope"); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("err = %v", err)
	}
}

// fakeNoToolsOllama is a model without tool support: /api/chat with tools is
// a 400, and /api/generate follows the text protocol.
func fakeNoToolsOllama(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Prompt string `json:"prompt"`
			System string `json:"system"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if r.URL.Path == "/api/chat" {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "m does not support tools"})
			return
		}
		reply := "```json\n{\"tool\": \"echo\", \"args\": {\"text\": \"ping\"}}\n```"
		switch {
		case strings.Contains(req.Prompt, "<tool_result"):
			start := strings.Index(req.Prompt, "<tool_result")
			body := req.Prompt[strings.Index(req.Prompt[start:], "\n")+start+1:]
			reply = "It returned " + strings.SplitN(body, "\n", 2)[0]
		case strings.Contains(req.System, "add:") && strings.Contains(req.Prompt, "add"):
			reply = "```json\n{\"tool\": \"add\", \"args\": {\"a\": 2, \"b\": 3}}\n```"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": reply, "done": true})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestAIProbe_NoNativeToolsTriesTextProtocol(t *testing.T) {
	srv := fakeNoToolsOllama(t)
	gw, err := messaging.NewAIGateway(messaging.AIGatewayConfig{Providers: []messaging.AIProvider{
		{Name: messaging.ProviderOllama, Endpoint: srv.URL, Models: []string{"m"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := runAIProbe(context.Background(), &out, gw.EachProvider(), ""); !errors.Is(err, errProbe) {
		t.Fatalf("err = %v (native tools are missing, so the probe still fails)", err)
	}
	text := out.String()
	if !strings.Contains(text, "trying the text protocol") || !strings.Contains(text, "usable through the text protocol") {
		t.Errorf("text-protocol result not reported:\n%s", text)
	}
}
