package messaging

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGroq_UsesOpenAIFormat(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"fast reply"}}],"usage":{"total_tokens":12}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderGroq, APIKey: "gsk_x", Endpoint: srv.URL, Models: []string{"llama-3.1-70b-versatile"}}},
		Client:    srv.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out != "fast reply" {
		t.Errorf("out = %q", out)
	}
	if gotAuth != "Bearer gsk_x" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotPath != "/v1/chat/completions" {
		t.Errorf("path = %q, want OpenAI-compatible chat path", gotPath)
	}
}

func TestOpenRouter_SendsAttributionHeaders(t *testing.T) {
	var referer, title, auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		referer = r.Header.Get("HTTP-Referer")
		title = r.Header.Get("X-Title")
		auth = r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"routed"}}],"usage":{"total_tokens":9}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{Name: ProviderOpenRouter, APIKey: "sk-or-1", Endpoint: srv.URL, Models: []string{"openai/gpt-4o"}}},
		Client:    srv.Client(),
	})
	if _, err := g.Complete(context.Background(), "hi", "sys"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(referer, "vortex") {
		t.Errorf("HTTP-Referer = %q, want a vortex URL", referer)
	}
	if title != "VORTEX" {
		t.Errorf("X-Title = %q, want VORTEX", title)
	}
	if auth != "Bearer sk-or-1" {
		t.Errorf("Authorization = %q", auth)
	}
}

func TestAzureOpenAI_UsesAPIKeyHeaderAndDeploymentURL(t *testing.T) {
	var apiKey, path, query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiKey = r.Header.Get("api-key")
		path = r.URL.Path
		query = r.URL.RawQuery
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"azure reply"}}],"usage":{"total_tokens":15}}`)
	}))
	defer srv.Close()

	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{
			Name: ProviderAzureOpenAI, APIKey: "az-key", Endpoint: srv.URL, Models: []string{"my-deployment"},
		}},
		Client: srv.Client(),
	})
	out, err := g.Complete(context.Background(), "hi", "sys")
	if err != nil {
		t.Fatal(err)
	}
	if out != "azure reply" {
		t.Errorf("out = %q", out)
	}
	if apiKey != "az-key" {
		t.Errorf("api-key header = %q", apiKey)
	}
	if !strings.Contains(path, "/deployments/my-deployment/chat/completions") {
		t.Errorf("path = %q, want deployment chat path", path)
	}
	if !strings.Contains(query, "api-version=") {
		t.Errorf("query = %q, want api-version", query)
	}
}

func TestBedrock_SignsWithSigV4(t *testing.T) {
	var authHeader, amzDate string
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		amzDate = r.Header.Get("X-Amz-Date")
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = io.WriteString(w, `{"content":[{"text":"bedrock reply"}],"usage":{"input_tokens":5,"output_tokens":7}}`)
	}))
	defer srv.Close()

	// Point the gateway at the test server by overriding the model path host is
	// not possible without hitting AWS; instead test the signer + body shape
	// directly by using a provider whose Endpoint (region) is set and a custom
	// client that redirects. Here we exercise bedrockSignV4 + body via a direct
	// call against the test server using Endpoint as a full URL override is not
	// supported, so we validate the signer separately and the body via a small
	// transport swap.
	g, _ := NewAIGateway(AIGatewayConfig{
		Providers: []AIProvider{{
			Name: ProviderBedrock, APIKey: "AKIA:secret", Endpoint: "us-east-1",
			Models: []string{"anthropic.claude-3-5-sonnet-20240620-v1:0"},
		}},
		Client: srv.Client(),
	})
	// Use a redirecting transport: rewrite the bedrock URL to the test server.
	g.client = &http.Client{Transport: rewriteTransport{to: srv.URL, base: srv.Client().Transport}}

	out, err := g.Complete(context.Background(), "hi", "you are helpful")
	if err != nil {
		t.Fatal(err)
	}
	if out != "bedrock reply" {
		t.Errorf("out = %q", out)
	}
	if !strings.HasPrefix(authHeader, "AWS4-HMAC-SHA256 ") {
		t.Errorf("Authorization = %q, want SigV4", authHeader)
	}
	if amzDate == "" {
		t.Error("X-Amz-Date header missing")
	}
	if body["anthropic_version"] != "bedrock-2023-05-31" {
		t.Errorf("body anthropic_version = %v", body["anthropic_version"])
	}
}

func TestSplitBedrockKey(t *testing.T) {
	a, s, ok := splitBedrockKey("AKIA:secret")
	if !ok || a != "AKIA" || s != "secret" {
		t.Errorf("splitBedrockKey = %q,%q,%v", a, s, ok)
	}
	if _, _, ok := splitBedrockKey("nocolon"); ok {
		t.Error("missing colon should fail")
	}
	if _, _, ok := splitBedrockKey(":secret"); ok {
		t.Error("empty access key should fail")
	}
}

// rewriteTransport redirects any request to a fixed test-server base URL,
// preserving the path, so a Bedrock URL can be served by httptest.
type rewriteTransport struct {
	to   string
	base http.RoundTripper
}

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, _ := req.URL.Parse(rt.to)
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return rt.base.RoundTrip(req)
}
