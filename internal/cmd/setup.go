package cmd

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/crypto/chacha20"

	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/tui"
)

// AIProviderConfig is the persisted first-run AI provider selection. The API
// key is stored encrypted (see encryptKey/decryptKey).
type AIProviderConfig struct {
	Provider     string `json:"provider"`
	APIKeyEnc    string `json:"api_key"` // base64(nonce || chacha20(key))
	Model        string `json:"model"`
	Endpoint     string `json:"endpoint"`
	ConfiguredAt string `json:"configured_at"`
	// Optional messaging (Telegram) set up during the wizard.
	TelegramToken  string `json:"telegram_token,omitempty"`
	TelegramChatID string `json:"telegram_chat_id,omitempty"`
	// EditorMode selects the TUI editor: "standard" (default) or "vim" (M20).
	EditorMode string `json:"editor_mode,omitempty"`
}

// aiProviderConfigPath returns <user-config>/vortex/ai-provider.json.
func aiProviderConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "vortex", "ai-provider.json")
}

// machineKey derives a 32-byte key from the hostname so the API key is
// encrypted at rest (protection against casual disk reads, not a secrets vault).
func machineKey() []byte {
	host, _ := os.Hostname()
	sum := sha256.Sum256([]byte(host + "vortex-ai-config"))
	return sum[:]
}

// encryptKey encrypts plaintext with ChaCha20 under the machine key, returning
// base64(nonce || ciphertext).
func encryptKey(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	nonce := make([]byte, chacha20.NonceSize)
	sum := sha256.Sum256([]byte("vortex-nonce" + plaintext[:min(len(plaintext), 4)]))
	copy(nonce, sum[:chacha20.NonceSize])

	cipher, err := chacha20.NewUnauthenticatedCipher(machineKey(), nonce)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(plaintext))
	cipher.XORKeyStream(out, []byte(plaintext))
	return base64.StdEncoding.EncodeToString(append(nonce, out...)), nil
}

// decryptKey reverses encryptKey.
func decryptKey(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", err
	}
	if len(raw) < chacha20.NonceSize {
		return "", fmt.Errorf("setup: ciphertext too short")
	}
	nonce, ct := raw[:chacha20.NonceSize], raw[chacha20.NonceSize:]
	cipher, err := chacha20.NewUnauthenticatedCipher(machineKey(), nonce)
	if err != nil {
		return "", err
	}
	out := make([]byte, len(ct))
	cipher.XORKeyStream(out, ct)
	return string(out), nil
}

// saveProviderConfig writes cfg to the config path (key already encrypted).
func saveProviderConfig(cfg AIProviderConfig) error {
	path := aiProviderConfigPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// loadProviderConfig reads the persisted config (if any).
func loadProviderConfig() (AIProviderConfig, bool) {
	data, err := os.ReadFile(aiProviderConfigPath())
	if err != nil {
		return AIProviderConfig{}, false
	}
	var cfg AIProviderConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return AIProviderConfig{}, false
	}
	return cfg, cfg.Provider != ""
}

// providerConfigured reports whether an AI provider is configured via the saved
// file or any of the provider env vars.
func providerConfigured() bool {
	for _, env := range []string{
		"VORTEX_ANTHROPIC_KEY", "VORTEX_OPENAI_KEY", "VORTEX_DEEPSEEK_KEY",
		"VORTEX_GEMINI_KEY", "VORTEX_OLLAMA_URL",
	} {
		if os.Getenv(env) != "" {
			return true
		}
	}
	_, ok := loadProviderConfig()
	return ok
}

// newSetupCommand builds `vortex setup`.
func newSetupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "Interactive first-run setup (AI provider, messaging, API key)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runSetup(cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
}

// runSetup drives the interactive wizard. out/in are injectable for testing.
func runSetup(out io.Writer, in io.Reader) error {
	r := bufio.NewReader(in)

	fmt.Fprintln(out, welcomeBanner)
	fmt.Fprintln(out, providerMenu)
	fmt.Fprint(out, "Select [1-10]: ")
	choice := readLine(r)

	cfg := AIProviderConfig{ConfiguredAt: time.Now().UTC().Format(time.RFC3339)}
	switch choice {
	case "1":
		setupKeyProvider(out, r, &cfg, "claude", "https://console.anthropic.com",
			"sk-ant-", "claude-sonnet-4-20250514",
			verifyClaude)
	case "2":
		setupKeyProvider(out, r, &cfg, "deepseek", "https://platform.deepseek.com",
			"sk-", "deepseek-chat", verifyOpenAICompat("https://api.deepseek.com/v1/chat/completions", "deepseek-chat"))
	case "3":
		setupKeyProvider(out, r, &cfg, "openai", "https://platform.openai.com",
			"sk-", "gpt-4o-mini", verifyOpenAICompat("https://api.openai.com/v1/chat/completions", "gpt-4o-mini"))
	case "4":
		setupKeyProvider(out, r, &cfg, "gemini", "https://aistudio.google.com",
			"", "gemini-1.5-flash", verifyGemini)
	case "5":
		setupKeyProvider(out, r, &cfg, "groq", "https://console.groq.com/keys",
			"gsk_", "llama-3.1-70b-versatile",
			verifyOpenAICompat("https://api.groq.com/openai/v1/chat/completions", "llama-3.1-70b-versatile"))
	case "6":
		setupBedrock(out, r, &cfg)
	case "7":
		setupAzureOpenAI(out, r, &cfg)
	case "8":
		setupKeyProvider(out, r, &cfg, "openrouter", "https://openrouter.ai/keys",
			"sk-or-", "openai/gpt-4o",
			verifyOpenRouter)
	case "9":
		setupOllama(out, r, &cfg)
	case "10", "":
		fmt.Fprintln(out, "\nSkipping AI setup. You can configure later:")
		fmt.Fprintln(out, "  vortex setup")
		fmt.Fprintln(out, "  or set VORTEX_ANTHROPIC_KEY=... env var")
	default:
		fmt.Fprintln(out, "Unrecognised choice; skipping AI setup.")
	}

	// Persist provider config when one was selected.
	if cfg.Provider != "" {
		if err := saveProviderConfig(cfg); err != nil {
			fmt.Fprintf(out, "⚠ Could not save config: %v\n", err)
		} else {
			fmt.Fprintf(out, "✓ Saved configuration to %s\n", aiProviderConfigPath())
		}
	}

	// Step 5 — optional Telegram.
	setupTelegram(out, r, &cfg)

	// Step 6 — editor mode preference (M20).
	setupEditorMode(out, r, &cfg)

	// Step 7 — auto-create a VORTEX API key.
	printAPIKey(out)

	fmt.Fprintln(out, completionFooter)
	return nil
}

// setupKeyProvider handles the key-input + verify flow for providers 1–4.
func setupKeyProvider(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig,
	provider, keyURL, prefix, model string, verify func(key string) string) {
	fmt.Fprintf(out, "\nGet your API key at: %s\n", keyURL)
	fmt.Fprintf(out, "Enter your %s API key: ", provider)
	key := readLine(r)
	if key == "" {
		fmt.Fprintln(out, "No key entered; skipping.")
		return
	}
	if prefix != "" && !strings.HasPrefix(key, prefix) {
		fmt.Fprintf(out, "That doesn't look like a valid %s key (should start with %s).\n", provider, prefix)
	}
	fmt.Fprintln(out, verify(key))

	enc, err := encryptKey(key)
	if err != nil {
		fmt.Fprintf(out, "⚠ Could not encrypt key: %v\n", err)
		return
	}
	cfg.Provider = provider
	cfg.APIKeyEnc = enc
	cfg.Model = model
}

// setupOllama handles the local-Ollama flow.
func setupOllama(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	fmt.Fprintln(out, "\nOllama runs AI models locally on your machine.")
	fmt.Fprintln(out, "Install Ollama from: https://ollama.com")
	fmt.Fprint(out, "Ollama endpoint (default: http://localhost:11434): ")
	endpoint := readLine(r)
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	if models := listOllamaModels(endpoint); len(models) > 0 {
		fmt.Fprintln(out, "Available models:")
		for _, m := range models {
			fmt.Fprintf(out, "  • %s\n", m)
		}
	} else {
		fmt.Fprintln(out, "⚠ Could not reach Ollama (is it running?)")
	}
	fmt.Fprint(out, "Which model to use? (default: llama3.2): ")
	model := readLine(r)
	if model == "" {
		model = "llama3.2"
	}
	cfg.Provider = "ollama"
	cfg.Endpoint = endpoint
	cfg.Model = model
}

// setupTelegram optionally configures Telegram alerts.
func setupTelegram(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	fmt.Fprint(out, "\nWould you like to set up Telegram alerts? [y/N]: ")
	if !strings.EqualFold(readLine(r), "y") {
		return
	}
	fmt.Fprintln(out, "Create a bot at https://t.me/BotFather")
	fmt.Fprint(out, "Enter your Telegram bot token: ")
	cfg.TelegramToken = readLine(r)
	fmt.Fprintln(out, "Enter your Telegram chat ID:")
	fmt.Fprintln(out, "(Send any message to your bot, then visit:")
	fmt.Fprintln(out, " https://api.telegram.org/bot<TOKEN>/getUpdates)")
	fmt.Fprint(out, "Chat ID: ")
	cfg.TelegramChatID = readLine(r)
	if cfg.Provider == "" {
		// Persist even if only messaging was configured.
		cfg.Provider = "none"
		_ = saveProviderConfig(*cfg)
		cfg.Provider = ""
	} else {
		_ = saveProviderConfig(*cfg)
	}
}

// setupEditorMode asks which TUI editor to use and persists the choice (M20).
func setupEditorMode(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	fmt.Fprintln(out, "\nEditor mode for the terminal UI:")
	fmt.Fprintln(out, "  [1] Standard (default, simple input)")
	fmt.Fprintln(out, "  [2] Vim (normal/insert/visual modes)")
	fmt.Fprint(out, "Select [1-2] (default 1): ")
	switch strings.TrimSpace(readLine(r)) {
	case "2":
		cfg.EditorMode = "vim"
	default:
		cfg.EditorMode = "standard"
	}
	// Persist the choice; reuse the "none" provider marker so a messaging-only
	// or editor-only setup still saves.
	if cfg.Provider == "" {
		cfg.Provider = "none"
		_ = saveProviderConfig(*cfg)
		cfg.Provider = ""
	} else {
		_ = saveProviderConfig(*cfg)
	}
	fmt.Fprintf(out, "✓ Editor mode: %s\n", cfg.EditorMode)
}

// printAPIKey creates and prints a VORTEX API key for the dashboard/CLI.
func printAPIKey(out io.Writer) {
	store, path := openAPIKeyStore(log)
	_, secret, err := store.Issue("admin", "default-org", []auth.Role{auth.RoleAdmin}, "setup key", 0)
	if err != nil {
		fmt.Fprintf(out, "⚠ Could not create API key: %v\n", err)
		return
	}
	if serr := store.Save(path); serr != nil {
		fmt.Fprintf(out, "⚠ Could not persist API key store: %v\n", serr)
	}
	// Persist the plaintext secret where the TUI can read it back (the store
	// only keeps a bcrypt hash). 0600, user-config dir.
	keyPath := tui.APIKeyFilePath()
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err == nil {
		if werr := os.WriteFile(keyPath, []byte(secret), 0o600); werr != nil {
			fmt.Fprintf(out, "⚠ Could not save TUI key file: %v\n", werr)
		}
	}
	fmt.Fprintln(out, apiKeyBanner(secret))
}

// readLine reads one trimmed line from r.
func readLine(r *bufio.Reader) string {
	line, _ := r.ReadString('\n')
	return strings.TrimSpace(line)
}

// isInteractive reports whether stdin is a terminal (character device). When it
// is not (a pipe, file, or no TTY — e.g. systemd, CI, integration tests), the
// auto-setup wizard must NOT run, or a non-interactive start would block.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// --- live verification helpers ----------------------------------------------

// httpVerify posts payload and returns a status-based result string.
func httpVerify(method, url string, headers map[string]string, payload any) string {
	var body io.Reader
	if payload != nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return "⚠ Could not verify (request error)"
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return "⚠ Could not verify (check network)"
	}
	defer func() { _ = resp.Body.Close() }()
	switch {
	case resp.StatusCode == http.StatusOK:
		return "✓ API key verified"
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return "✗ Invalid API key"
	default:
		return fmt.Sprintf("⚠ Could not verify (status %d)", resp.StatusCode)
	}
}

func verifyClaude(key string) string {
	return httpVerify(http.MethodPost, "https://api.anthropic.com/v1/messages",
		map[string]string{"x-api-key": key, "anthropic-version": "2023-06-01"},
		map[string]any{
			"model": "claude-3-haiku-20240307", "max_tokens": 10,
			"messages": []map[string]any{{"role": "user", "content": "hi"}},
		})
}

func verifyOpenAICompat(url, model string) func(string) string {
	return func(key string) string {
		return httpVerify(http.MethodPost, url,
			map[string]string{"Authorization": "Bearer " + key},
			map[string]any{"model": model, "max_tokens": 5,
				"messages": []map[string]any{{"role": "user", "content": "hi"}}})
	}
}

func verifyGemini(key string) string {
	url := "https://generativelanguage.googleapis.com/v1beta/models/gemini-1.5-flash:generateContent?key=" + key
	return httpVerify(http.MethodPost, url, nil,
		map[string]any{"contents": []map[string]any{{"parts": []map[string]any{{"text": "hi"}}}}})
}

// verifyOpenRouter checks an OpenRouter key, including the attribution headers
// OpenRouter expects.
func verifyOpenRouter(key string) string {
	return httpVerify(http.MethodPost, "https://openrouter.ai/api/v1/chat/completions",
		map[string]string{
			"Authorization": "Bearer " + key,
			"HTTP-Referer":  "https://github.com/vortex-run/vortex",
			"X-Title":       "VORTEX",
		},
		map[string]any{"model": "openai/gpt-4o", "max_tokens": 5,
			"messages": []map[string]any{{"role": "user", "content": "hi"}}})
}

// setupBedrock handles the AWS Bedrock flow. Credentials come from the
// environment (AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY); the wizard records
// only the region and default model, never the secret key.
func setupBedrock(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	fmt.Fprintln(out, "\nAWS Bedrock uses your AWS credentials (SigV4-signed requests).")
	fmt.Fprintln(out, "Set these environment variables before starting VORTEX:")
	fmt.Fprintln(out, "  AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, VORTEX_BEDROCK_REGION")
	fmt.Fprint(out, "AWS region (default: us-east-1): ")
	region := readLine(r)
	if region == "" {
		region = "us-east-1"
	}
	cfg.Provider = "bedrock"
	cfg.Endpoint = region
	cfg.Model = "anthropic.claude-3-5-sonnet-20240620-v1:0"
	fmt.Fprintln(out, "✓ Bedrock selected. Credentials are read from the environment at startup.")
}

// setupAzureOpenAI handles the Azure OpenAI flow: resource endpoint +
// deployment name + key.
func setupAzureOpenAI(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	fmt.Fprintln(out, "\nAzure OpenAI uses your Azure resource endpoint and a deployment name.")
	fmt.Fprint(out, "Resource endpoint (https://<resource>.openai.azure.com): ")
	endpoint := readLine(r)
	fmt.Fprint(out, "Deployment name: ")
	deployment := readLine(r)
	fmt.Fprint(out, "API key: ")
	key := readLine(r)
	if endpoint == "" || deployment == "" || key == "" {
		fmt.Fprintln(out, "Endpoint, deployment, and key are all required; skipping.")
		return
	}
	enc, err := encryptKey(key)
	if err != nil {
		fmt.Fprintf(out, "⚠ Could not encrypt key: %v\n", err)
		return
	}
	cfg.Provider = "azure-openai"
	cfg.APIKeyEnc = enc
	cfg.Endpoint = endpoint
	cfg.Model = deployment
	fmt.Fprintln(out, "✓ Azure OpenAI configured.")
}

// listOllamaModels fetches the model list from <endpoint>/api/tags.
func listOllamaModels(endpoint string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/api/tags", nil)
	if err != nil {
		return nil
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	return names
}

// --- banners ----------------------------------------------------------------

const welcomeBanner = `╔══════════════════════════════════════╗
║     VORTEX — First Time Setup        ║
║  One binary. Any server. Fully       ║
║  autonomous.                         ║
╚══════════════════════════════════════╝`

const providerMenu = `
Select your AI provider:
1.  Anthropic Claude (Recommended)
    Best reasoning, most capable
2.  DeepSeek
    Fast, cost-effective, OpenAI-compatible
3.  OpenAI GPT
    GPT-4o and GPT-4o-mini
4.  Google Gemini
    Gemini 1.5 Pro and Flash
5.  Groq (Fast + Free tier)
    Llama 3.1 / Mixtral at very low latency
6.  AWS Bedrock
    Claude & Titan via your AWS account (SigV4)
7.  Azure OpenAI
    GPT models on your Azure resource
8.  OpenRouter (75+ models)
    One key, many models
9.  Ollama (Local, Free)
    Run AI models on your own machine
10. Skip for now
    Configure later with: vortex setup`

const completionFooter = `
Start VORTEX:  vortex start
Dashboard:     http://localhost:9090/dashboard/

Set AI provider in env (alternative to this setup):
  export VORTEX_ANTHROPIC_KEY=<your-key>`

// apiKeyBanner renders the one-time API-key box.
func apiKeyBanner(secret string) string {
	return "╔══════════════════════════════════════╗\n" +
		"║  Setup Complete!                     ║\n" +
		"║                                      ║\n" +
		"║  Your VORTEX API Key:                ║\n" +
		"║  " + secret + "\n" +
		"║                                      ║\n" +
		"║  Save this — it won't show again.    ║\n" +
		"║  Use it with the dashboard and CLI.  ║\n" +
		"╚══════════════════════════════════════╝"
}
