package messaging

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// (context is used by the call* methods' ctx parameters.)

// This file adds the M20 AI providers: Groq, AWS Bedrock, Azure OpenAI, and
// OpenRouter. Groq/Azure/OpenRouter speak the OpenAI chat-completions shape and
// reuse callOpenAI with provider-specific endpoints/headers; Bedrock uses the
// Anthropic-on-Bedrock invoke API signed with AWS Signature V4.

// callGroq calls Groq's OpenAI-compatible chat completions API. Groq is very
// fast and offers a free tier (env VORTEX_GROQ_KEY).
func (g *AIGateway) callGroq(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	if p.Endpoint == "" {
		p.Endpoint = "https://api.groq.com/openai"
	}
	if len(p.Models) == 0 {
		p.Models = []string{"llama-3.1-70b-versatile"}
	}
	text, tokens, err := g.callOpenAI(ctx, p, prompt, systemPrompt)
	if err != nil {
		return "", 0, fmt.Errorf("groq: %w", err)
	}
	return text, tokens, nil
}

// callOpenRouter calls OpenRouter's OpenAI-compatible API, which fronts 75+
// models. It adds the HTTP-Referer and X-Title headers OpenRouter uses for
// app attribution (env VORTEX_OPENROUTER_KEY).
func (g *AIGateway) callOpenRouter(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	base := p.Endpoint
	if base == "" {
		base = "https://openrouter.ai/api"
	}
	model := g.modelOf(p)
	if model == "" {
		model = "openai/gpt-4o"
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
		map[string]string{
			"Authorization": "Bearer " + p.APIKey,
			"HTTP-Referer":  "https://github.com/vortex-run/vortex",
			"X-Title":       "VORTEX",
		},
		map[string]any{
			"model": model,
			"messages": []map[string]any{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": prompt},
			},
		}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("openrouter: %w", err)
	}
	text := ""
	if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	return text, out.Usage.TotalTokens, nil
}

// callAzureOpenAI calls an Azure OpenAI deployment. The request/response shape
// is OpenAI's, but the URL embeds the deployment and an api-version query
// parameter, and the API key is sent in the api-key header. The deployment is
// taken from p.Models[0] (env VORTEX_AZURE_OPENAI_DEPLOYMENT); p.Endpoint is
// the resource base (e.g. https://<resource>.openai.azure.com).
func (g *AIGateway) callAzureOpenAI(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	if p.Endpoint == "" {
		return "", 0, fmt.Errorf("azure-openai: endpoint (resource URL) required")
	}
	deployment := g.modelOf(p)
	if deployment == "" {
		return "", 0, fmt.Errorf("azure-openai: deployment name required")
	}
	url := fmt.Sprintf("%s/openai/deployments/%s/chat/completions?api-version=2024-02-01",
		strings.TrimRight(p.Endpoint, "/"), deployment)

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
	_, err := g.doJSON(ctx, url,
		map[string]string{"api-key": p.APIKey},
		map[string]any{
			"messages": []map[string]any{
				{"role": "system", "content": systemPrompt},
				{"role": "user", "content": prompt},
			},
		}, &out)
	if err != nil {
		return "", 0, fmt.Errorf("azure-openai: %w", err)
	}
	text := ""
	if len(out.Choices) > 0 {
		text = out.Choices[0].Message.Content
	}
	return text, out.Usage.TotalTokens, nil
}

// callBedrock invokes an Anthropic Claude model on AWS Bedrock, signing the
// request with AWS Signature V4. Credentials and region come from the
// environment via the AIProvider fields (Endpoint encodes the region; APIKey
// is "<accessKey>:<secretKey>"). The Anthropic-on-Bedrock body format is used.
func (g *AIGateway) callBedrock(ctx context.Context, p AIProvider, prompt, systemPrompt string) (string, int, error) {
	region := p.Endpoint // Endpoint carries the region for bedrock (e.g. "us-east-1")
	if region == "" {
		region = "us-east-1"
	}
	accessKey, secretKey, ok := splitBedrockKey(p.APIKey)
	if !ok {
		return "", 0, fmt.Errorf("bedrock: APIKey must be \"<accessKey>:<secretKey>\"")
	}
	model := g.modelOf(p)
	if model == "" {
		model = "anthropic.claude-3-5-sonnet-20240620-v1:0"
	}
	host := fmt.Sprintf("bedrock-runtime.%s.amazonaws.com", region)
	url := fmt.Sprintf("https://%s/model/%s/invoke", host, model)

	payload := map[string]any{
		"anthropic_version": "bedrock-2023-05-31",
		"max_tokens":        1000,
		"system":            systemPrompt,
		"messages":          []map[string]any{{"role": "user", "content": prompt}},
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
	signer := func(headers map[string]string, body []byte) {
		bedrockSignV4(headers, body, "POST", host, "/model/"+model+"/invoke",
			accessKey, secretKey, region, "bedrock", g.now())
	}
	_, err := g.doSignedJSON(ctx, url, signer, payload, &out)
	if err != nil {
		return "", 0, fmt.Errorf("bedrock: %w", err)
	}
	text := ""
	if len(out.Content) > 0 {
		text = out.Content[0].Text
	}
	return text, out.Usage.InputTokens + out.Usage.OutputTokens, nil
}

// splitBedrockKey parses "<accessKey>:<secretKey>".
func splitBedrockKey(key string) (access, secret string, ok bool) {
	i := strings.IndexByte(key, ':')
	if i <= 0 || i == len(key)-1 {
		return "", "", false
	}
	return key[:i], key[i+1:], true
}

// --- AWS Signature V4 (self-contained, stdlib crypto) -----------------------

// bedrockSignV4 adds the Authorization and X-Amz-Date headers for an AWS
// SigV4-signed request. It mirrors the signer in internal/secrets/awsssm.go;
// kept here to avoid exporting cross-package internals. headers is mutated in
// place; the caller has already set Host/Content-Type.
func bedrockSignV4(headers map[string]string, payload []byte, method, host, path, accessKey, secretKey, region, service string, now time.Time) {
	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")
	headers["X-Amz-Date"] = amzDate
	headers["Host"] = host

	payloadHash := hexSHA256Hex(payload)

	// Canonical headers must be sorted by lowercased name.
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-amz-date:%s\n",
		"application/json", host, amzDate)
	signedHeaders := "content-type;host;x-amz-date"

	canonicalRequest := strings.Join([]string{
		method, path, "", canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", amzDate, scope, hexSHA256Hex([]byte(canonicalRequest)),
	}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+secretKey), dateStamp)
	signingKey = hmacSHA256(signingKey, region)
	signingKey = hmacSHA256(signingKey, service)
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	headers["Authorization"] = fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		accessKey, scope, signedHeaders, signature)
}

func hmacSHA256(key []byte, data string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(data))
	return h.Sum(nil)
}

func hexSHA256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
