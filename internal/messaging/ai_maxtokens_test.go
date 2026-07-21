package messaging

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// claudeMaxTokensServer captures the max_tokens field of a Messages API call.
func claudeMaxTokensServer(t *testing.T, got *float64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if v, ok := payload["max_tokens"].(float64); ok {
			*got = v
		}
		_, _ = io.WriteString(w, `{"content":[{"text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
}

func TestMaxTokens_DefaultIsNotTruncatingLowValue(t *testing.T) {
	var got float64
	srv := claudeMaxTokensServer(t, &got)
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderClaude, APIKey: "k", Endpoint: srv.URL, Models: []string{"claude-x"}}},
		Client:    srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if int(got) != defaultMaxTokens {
		t.Errorf("max_tokens = %v, want the %d default (regression: was hardcoded 1000)", got, defaultMaxTokens)
	}
}

func TestMaxTokens_ConfigDefaultApplies(t *testing.T) {
	var got float64
	srv := claudeMaxTokensServer(t, &got)
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderClaude, APIKey: "k", Endpoint: srv.URL, Models: []string{"claude-x"}}},
		Client:    srv.Client(),
		MaxTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if int(got) != 2048 {
		t.Errorf("max_tokens = %v, want the configured 2048", got)
	}
}

func TestMaxTokens_PerRequestOverrideWins(t *testing.T) {
	var got float64
	srv := claudeMaxTokensServer(t, &got)
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderClaude, APIKey: "k", Endpoint: srv.URL, Models: []string{"claude-x"}}},
		Client:    srv.Client(),
		MaxTokens: 2048,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithMaxTokens(context.Background(), 300)
	if _, err := g.Complete(ctx, "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if int(got) != 300 {
		t.Errorf("max_tokens = %v, want the per-request 300", got)
	}

	// A non-positive override is ignored (config default stays in force).
	got = 0
	if _, err := g.Complete(WithMaxTokens(context.Background(), 0), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if int(got) != 2048 {
		t.Errorf("max_tokens = %v, want 2048 when the override is non-positive", got)
	}
}

func TestMaxTokens_BedrockHonoursOverride(t *testing.T) {
	var got float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if v, ok := payload["max_tokens"].(float64); ok {
			got = v
		}
		_, _ = io.WriteString(w, `{"content":[{"text":"ok"}],"usage":{"input_tokens":1,"output_tokens":1}}`)
	}))
	defer srv.Close()

	g, err := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{
			Name: ProviderBedrock, APIKey: "AKIA:secret", Endpoint: "us-east-1",
			Models: []string{"anthropic.claude-3-5-sonnet-20240620-v1:0"},
		}},
		Client: srv.Client(),
	})
	if err != nil {
		t.Fatal(err)
	}
	g.client = &http.Client{Transport: rewriteTransport{to: srv.URL, base: srv.Client().Transport}}

	if _, err := g.Complete(WithMaxTokens(context.Background(), 4096), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if int(got) != 4096 {
		t.Errorf("bedrock max_tokens = %v, want 4096", got)
	}
}
