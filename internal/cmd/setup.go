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
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// AIProviderConfig is the persisted first-run AI provider selection. The API
// key is stored encrypted (see encryptKey/decryptKey).
type AIProviderConfig struct {
	Provider     string `json:"provider"`
	APIKeyEnc    string `json:"api_key"` // base64(nonce || chacha20(key))
	Model        string `json:"model"`
	Endpoint     string `json:"endpoint"`
	ConfiguredAt string `json:"configured_at"`
	// Backups are fallback API keys (brand redesign part 3): when the primary
	// hits a rate limit or fails, the gateway tries these in order.
	Backups []BackupKey `json:"backups,omitempty"`
	// Optional messaging (Telegram) set up during the wizard.
	TelegramToken  string `json:"telegram_token,omitempty"`
	TelegramChatID string `json:"telegram_chat_id,omitempty"`
	// EditorMode selects the TUI editor: "standard" (default) or "vim" (M20).
	EditorMode string `json:"editor_mode,omitempty"`
}

// BackupKey is one fallback AI key slot.
type BackupKey struct {
	Provider  string `json:"provider"`
	APIKeyEnc string `json:"api_key"`
	Model     string `json:"model,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
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

// setupSteps is the wizard length shown in each step header.
const setupSteps = 5

// providerInfo is one row of the provider comparison table plus everything the
// key-entry flow needs.
type providerInfo struct {
	Name    string // catalog/display name
	ID      string // gateway provider id
	Speed   string
	Cost    string
	BestFor string
	KeyURL  string
	Prefix  string
	Model   string
	Blurb   string
	Signup  []string // numbered signup steps shown in a box
	Verify  func(key string) string
}

// providerCatalog is the comparison table, in display order. Entry 1 is the
// recommended default.
var providerCatalog = []providerInfo{
	{
		Name: "DeepSeek", ID: "deepseek", Speed: "Fast", Cost: "~$0.001", BestFor: "Coding ★ Recommended",
		KeyURL: "https://platform.deepseek.com", Prefix: "sk-", Model: "deepseek-chat",
		Blurb: "DeepSeek is great for coding tasks and is very\naffordable. Most developers use ~$1-2 per month.",
		Signup: []string{
			"1. Visit: https://platform.deepseek.com",
			"2. Sign up (takes 30 seconds, no credit card)",
			"3. Go to: API Keys -> Create new key",
			"4. Copy the key and paste it below",
		},
		Verify: verifyOpenAICompat("https://api.deepseek.com/v1/chat/completions", "deepseek-chat"),
	},
	{
		Name: "Claude", ID: "claude", Speed: "Med", Cost: "~$0.015", BestFor: "Reasoning + writing",
		KeyURL: "https://console.anthropic.com", Prefix: "sk-ant-", Model: "claude-sonnet-4-20250514",
		Blurb: "Claude has the strongest reasoning and writing.\nIdeal when answer quality matters most.",
		Signup: []string{
			"1. Visit: https://console.anthropic.com",
			"2. Sign up and add billing",
			"3. Go to: API Keys -> Create Key",
			"4. Copy the key and paste it below",
		},
		Verify: verifyClaude,
	},
	{
		Name: "OpenAI GPT", ID: "openai", Speed: "Med", Cost: "~$0.005", BestFor: "General purpose",
		KeyURL: "https://platform.openai.com", Prefix: "sk-", Model: "gpt-4o-mini",
		Blurb: "GPT-4o and GPT-4o-mini: a solid general-purpose\nchoice with wide ecosystem support.",
		Signup: []string{
			"1. Visit: https://platform.openai.com",
			"2. Sign up and add billing",
			"3. Go to: API keys -> Create new secret key",
			"4. Copy the key and paste it below",
		},
		Verify: verifyOpenAICompat("https://api.openai.com/v1/chat/completions", "gpt-4o-mini"),
	},
	{
		Name: "Groq", ID: "groq", Speed: "Fast", Cost: "Free*", BestFor: "Quick answers",
		KeyURL: "https://console.groq.com/keys", Prefix: "gsk_", Model: "llama-3.1-70b-versatile",
		Blurb: "Groq serves open models at very low latency.\n* Free tier: 10,000 tokens/day.",
		Signup: []string{
			"1. Visit: https://console.groq.com/keys",
			"2. Sign up (free)",
			"3. Create API Key",
			"4. Copy the key and paste it below",
		},
		Verify: verifyOpenAICompat("https://api.groq.com/openai/v1/chat/completions", "llama-3.1-70b-versatile"),
	},
	{
		Name: "Gemini", ID: "gemini", Speed: "Med", Cost: "~$0.003", BestFor: "Multimodal tasks",
		KeyURL: "https://aistudio.google.com", Prefix: "", Model: "gemini-1.5-flash",
		Blurb: "Gemini 1.5 handles text and images with a\ngenerous free tier via AI Studio.",
		Signup: []string{
			"1. Visit: https://aistudio.google.com",
			"2. Sign in with a Google account",
			"3. Get API key -> Create API key",
			"4. Copy the key and paste it below",
		},
		Verify: verifyGemini,
	},
	{
		Name: "AWS Bedrock", ID: "bedrock", Speed: "Med", Cost: "~$0.010", BestFor: "Enterprise / AWS",
	},
	{
		Name: "Azure OpenAI", ID: "azure-openai", Speed: "Med", Cost: "~$0.008", BestFor: "Enterprise / Azure",
	},
	{
		Name: "OpenRouter", ID: "openrouter", Speed: "Var", Cost: "Varies", BestFor: "75+ models",
		KeyURL: "https://openrouter.ai/keys", Prefix: "sk-or-", Model: "openai/gpt-4o",
		Blurb: "One key, 75+ models. Routes to whichever\nprovider serves the model you ask for.",
		Signup: []string{
			"1. Visit: https://openrouter.ai/keys",
			"2. Sign up and add credits",
			"3. Create Key",
			"4. Copy the key and paste it below",
		},
		Verify: verifyOpenRouter,
	},
	{
		Name: "Ollama", ID: "ollama", Speed: "Local", Cost: "Free", BestFor: "Privacy / offline",
	},
}

// runSetup drives the interactive wizard. out/in are injectable for testing.
func runSetup(out io.Writer, in io.Reader) error {
	r := bufio.NewReader(in)
	cfg := AIProviderConfig{ConfiguredAt: time.Now().UTC().Format(time.RFC3339)}

	setupWelcome(out, r)            // step 0 — logo + welcome
	setupProviderStep(out, r, &cfg) // step 1 — provider table + key
	if cfg.Provider != "" && cfg.Provider != "none" {
		setupBackupKeys(out, r, &cfg) // step 2 — multi-key rotation
	}
	setupTelegramStep(out, r, &cfg) // step 3 — telegram
	setupEditorMode(out, r, &cfg)   // editor preference (kept from M20)
	printAPIKeyStep(out, r)         // step 4 — VORTEX API key
	setupComplete(out, &cfg)        // step 5 — summary + next steps
	return nil
}

// setupWelcome animates the logo and waits for Enter (step 0).
func setupWelcome(out io.Writer, r *bufio.Reader) {
	// Animate only on a real terminal; piped/test runs print instantly.
	delay := time.Duration(0)
	if isInteractive() {
		delay = 30 * time.Millisecond
	}
	for _, line := range strings.Split(brand.Logo, "\n") {
		fmt.Fprintln(out, line)
		time.Sleep(delay)
	}
	fmt.Fprintln(out)
	fmt.Fprintln(out, "╔═══════════════════════════════════════════════════╗")
	fmt.Fprintf(out, "║   %s  %s%s║\n", brand.LogoSmall, brand.Version, strings.Repeat(" ", 29))
	fmt.Fprintf(out, "║   %s%s║\n", brand.Tagline, strings.Repeat(" ", 7))
	fmt.Fprintln(out, "╚═══════════════════════════════════════════════════╝")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Welcome! Let's get you set up in about 2 minutes.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "VORTEX is an AI agent that lives on your server.")
	fmt.Fprintln(out, "It builds apps, manages infrastructure, researches")
	fmt.Fprintln(out, "topics, and responds on Telegram — autonomously.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, strings.Repeat("─", 52))
	fmt.Fprint(out, "Press Enter to start setup, or Ctrl+C to exit ")
	_ = readLine(r)
}

// stepHeader prints the "Step N of M — title" header.
func stepHeader(out io.Writer, n int, title string) {
	fmt.Fprintf(out, "\nStep %d of %d — %s\n", n, setupSteps, title)
	fmt.Fprintln(out, strings.Repeat("─", 52))
}

// printBox renders lines inside a light box sized to the longest line.
func printBox(out io.Writer, lines ...string) {
	width := 0
	for _, l := range lines {
		if n := len([]rune(l)); n > width {
			width = n
		}
	}
	fmt.Fprintln(out, "┌─"+strings.Repeat("─", width)+"─┐")
	for _, l := range lines {
		fmt.Fprintf(out, "│ %s%s │\n", l, strings.Repeat(" ", width-len([]rune(l))))
	}
	fmt.Fprintln(out, "└─"+strings.Repeat("─", width)+"─┘")
}

// setupProviderStep renders the comparison table and dispatches the chosen
// provider's flow (step 1).
func setupProviderStep(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	stepHeader(out, 1, "Choose your AI brain")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %-2s %-13s %-8s %-11s %s\n", "", "PROVIDER", "SPEED", "COST/TASK", "BEST FOR")
	fmt.Fprintln(out, "  "+strings.Repeat("─", 50))
	for i, p := range providerCatalog {
		fmt.Fprintf(out, "  %-2d %-13s %-8s %-11s %s\n", i+1, p.Name, p.Speed, p.Cost, p.BestFor)
	}
	fmt.Fprintf(out, "  %-2d %-13s %-8s %-11s %s\n", 10, "Skip for now", "", "", "Configure later: vortex setup")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  * Groq free tier: 10,000 tokens/day")
	fmt.Fprintln(out)
	fmt.Fprint(out, "Type a number and press Enter: ")
	choice := readLine(r)

	switch choice {
	case "1", "2", "3", "4", "5", "8":
		idx := map[string]int{"1": 0, "2": 1, "3": 2, "4": 3, "5": 4, "8": 7}[choice]
		setupKeyProvider(out, r, cfg, providerCatalog[idx])
	case "6":
		setupBedrock(out, r, cfg)
	case "7":
		setupAzureOpenAI(out, r, cfg)
	case "9":
		setupOllama(out, r, cfg)
	case "10", "":
		fmt.Fprintln(out, "\nSkipping AI setup. You can configure later:")
		fmt.Fprintln(out, "  vortex setup")
		fmt.Fprintln(out, "  or set VORTEX_ANTHROPIC_KEY=... env var")
	default:
		fmt.Fprintln(out, "Unrecognised choice; skipping AI setup.")
	}

	if cfg.Provider != "" {
		if err := saveProviderConfig(*cfg); err != nil {
			fmt.Fprintf(out, "%s Could not save config: %v\n", brand.IconWarn, err)
		} else {
			fmt.Fprintf(out, "%s Saved configuration to %s\n", brand.IconSuccess, aiProviderConfigPath())
		}
	}
}

// setupKeyProvider handles the key-input + verify flow for catalog providers.
func setupKeyProvider(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig, p providerInfo) {
	fmt.Fprintf(out, "\n%s Selected: %s\n\n", brand.IconSuccess, p.Name)
	fmt.Fprintln(out, p.Blurb)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "Get your %s API key:\n", p.Name)
	printBox(out, p.Signup...)
	fmt.Fprintln(out)
	fmt.Fprint(out, "Paste your API key: ")
	key := readLine(r)
	if key == "" {
		fmt.Fprintln(out, "No key entered; skipping.")
		return
	}
	fmt.Fprintf(out, "%s Key: %s\n", brand.IconSuccess, brand.MaskSecret(key))
	if p.Prefix != "" && !strings.HasPrefix(key, p.Prefix) {
		fmt.Fprintf(out, "That doesn't look like a valid %s key (should start with %s).\n", p.Name, p.Prefix)
	}
	fmt.Fprintf(out, "%s Verifying key...\n", brand.IconSpinner)
	fmt.Fprintln(out, p.Verify(key))
	fmt.Fprintf(out, "%s Model: %s available\n", brand.IconSuccess, p.Model)
	fmt.Fprintf(out, "%s Estimated cost: %s per typical task\n", brand.IconSuccess, strings.TrimPrefix(p.Cost, "~"))

	enc, err := encryptKey(key)
	if err != nil {
		fmt.Fprintf(out, "%s Could not encrypt key: %v\n", brand.IconWarn, err)
		return
	}
	cfg.Provider = p.ID
	cfg.APIKeyEnc = enc
	cfg.Model = p.Model
}

// setupBackupKeys adds fallback key slots (step 2). The gateway tries slots in
// order when the primary fails or is rate-limited.
func setupBackupKeys(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	stepHeader(out, 2, "Add backup API keys (optional)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "VORTEX can automatically switch between multiple")
	fmt.Fprintln(out, "API keys when one hits rate limits or runs out")
	fmt.Fprintln(out, "of budget. This keeps your tasks running 24/7.")
	fmt.Fprintln(out)
	primary := catalogByID(cfg.Provider)
	fmt.Fprintf(out, "You already have: %s (primary) %s\n", primary.Name, brand.IconSuccess)
	fmt.Fprintln(out)

	for len(cfg.Backups) < 2 {
		fmt.Fprintln(out, "Add more keys? (recommended for production use)")
		fmt.Fprintln(out)
		fmt.Fprintf(out, "  1  Add another %s key as backup\n", primary.Name)
		fmt.Fprintln(out, "  2  Add a different provider as fallback")
		fmt.Fprintln(out, "  3  Add Ollama as free fallback (local)")
		fmt.Fprintln(out, "  4  Skip — use single key for now")
		fmt.Fprintln(out)
		fmt.Fprint(out, "Type a number: ")
		switch readLine(r) {
		case "1":
			addBackupKey(out, r, cfg, primary)
		case "2":
			fmt.Fprint(out, "Provider number (1-8 from the table above): ")
			n := readLine(r)
			idx := map[string]int{"1": 0, "2": 1, "3": 2, "4": 3, "5": 4, "8": 7}[n]
			if (n == "6") || (n == "7") || (n == "9") || providerCatalog[idx].KeyURL == "" {
				fmt.Fprintln(out, "That provider can't be a key-slot backup; choose 1-5 or 8.")
				continue
			}
			addBackupKey(out, r, cfg, providerCatalog[idx])
		case "3":
			cfg.Backups = append(cfg.Backups, BackupKey{
				Provider: "ollama", Endpoint: "http://localhost:11434", Model: "llama3.2",
			})
			fmt.Fprintf(out, "%s Ollama added as offline fallback\n\n", brand.IconSuccess)
		default:
			goto done
		}
	}
done:
	if len(cfg.Backups) > 0 {
		_ = saveProviderConfig(*cfg)
		fmt.Fprintf(out, "\n%s Key rotation configured:\n", brand.IconSuccess)
		fmt.Fprintf(out, "  Slot 1: %s (primary)  ★\n", primary.Name)
		for i, b := range cfg.Backups {
			role := "rate limit backup"
			if b.Provider == "ollama" {
				role = "offline fallback"
			}
			fmt.Fprintf(out, "  Slot %d: %s (%s)\n", i+2, catalogByID(b.Provider).Name, role)
		}
		fmt.Fprintln(out)
		fmt.Fprintln(out, "VORTEX will automatically switch if a key fails.")
	}
}

// addBackupKey runs a compact key-entry flow for one backup slot.
func addBackupKey(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig, p providerInfo) {
	fmt.Fprintf(out, "Paste your %s API key: ", p.Name)
	key := readLine(r)
	if key == "" {
		fmt.Fprintln(out, "No key entered; skipping.")
		return
	}
	fmt.Fprintf(out, "%s Key: %s\n", brand.IconSuccess, brand.MaskSecret(key))
	enc, err := encryptKey(key)
	if err != nil {
		fmt.Fprintf(out, "%s Could not encrypt key: %v\n", brand.IconWarn, err)
		return
	}
	cfg.Backups = append(cfg.Backups, BackupKey{Provider: p.ID, APIKeyEnc: enc, Model: p.Model})
	fmt.Fprintf(out, "%s %s added as backup slot %d\n\n", brand.IconSuccess, p.Name, len(cfg.Backups)+1)
}

// catalogByID finds a catalog entry by provider id (zero entry if unknown).
func catalogByID(id string) providerInfo {
	for _, p := range providerCatalog {
		if p.ID == id {
			return p
		}
	}
	return providerInfo{Name: id, ID: id}
}

// setupTelegramStep optionally configures Telegram (step 3).
func setupTelegramStep(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	stepHeader(out, 3, "Connect Telegram (optional)")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "With Telegram you can:")
	fmt.Fprintln(out, "• Control VORTEX from your phone")
	fmt.Fprintln(out, "• Get task updates and alerts")
	fmt.Fprintln(out, "• Approve agent actions remotely")
	fmt.Fprintln(out, "• Send voice messages as commands")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Connect Telegram now?")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  1  Yes — set it up now (recommended)")
	fmt.Fprintln(out, "  2  Skip — set up later with: vortex setup")
	fmt.Fprintln(out)
	fmt.Fprint(out, "Type a number: ")
	if readLine(r) != "1" {
		return
	}

	fmt.Fprintln(out)
	printBox(out,
		"1. Open Telegram and search @BotFather",
		"2. Send: /newbot",
		"3. Follow the prompts to create your bot",
		"4. Copy the token BotFather gives you",
	)
	fmt.Fprintln(out)
	fmt.Fprint(out, "Paste your bot token: ")
	token := readLine(r)
	if token == "" {
		fmt.Fprintln(out, "No token entered; skipping Telegram.")
		return
	}
	cfg.TelegramToken = token
	fmt.Fprintf(out, "%s Connecting to Telegram...\n", brand.IconSpinner)
	fmt.Fprintln(out, verifyTelegramToken(token))

	fmt.Fprintln(out)
	fmt.Fprintln(out, "Now get your chat ID:")
	printBox(out,
		"1. Send any message to your bot on Telegram",
		"2. Visit this URL in your browser:",
		"   https://api.telegram.org/bot<TOKEN>/getUpdates",
		"3. Find \"id\" under \"chat\" in the response",
	)
	fmt.Fprintln(out)
	fmt.Fprint(out, "Paste your chat ID: ")
	cfg.TelegramChatID = readLine(r)
	fmt.Fprintf(out, "%s Telegram fully configured\n", brand.IconSuccess)

	if cfg.Provider == "" {
		// Persist even if only messaging was configured.
		cfg.Provider = "none"
		_ = saveProviderConfig(*cfg)
		cfg.Provider = ""
	} else {
		_ = saveProviderConfig(*cfg)
	}
}

// verifyTelegramToken calls getMe and reports the connected bot's username.
// Overridable in tests (no network).
var verifyTelegramToken = func(token string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"https://api.telegram.org/bot"+token+"/getMe", nil)
	if err != nil {
		return brand.IconWarn + " Could not verify token"
	}
	resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
	if err != nil {
		return brand.IconWarn + " Could not reach Telegram (check network)"
	}
	defer func() { _ = resp.Body.Close() }()
	var body struct {
		OK     bool `json:"ok"`
		Result struct {
			Username string `json:"username"`
		} `json:"result"`
	}
	if json.NewDecoder(resp.Body).Decode(&body) != nil || !body.OK {
		return brand.IconError + " Invalid bot token"
	}
	return brand.IconSuccess + " Bot connected: @" + body.Result.Username
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
	fmt.Fprintf(out, "%s Editor mode: %s\n", brand.IconSuccess, cfg.EditorMode)
}

// printAPIKeyStep creates and prints a VORTEX API key (step 4), waiting for
// the user to confirm they saved it.
func printAPIKeyStep(out io.Writer, r *bufio.Reader) {
	stepHeader(out, 4, "Your VORTEX API key")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "%s Creating your API key...\n\n", brand.IconSpinner)

	store, path := openAPIKeyStore(log)
	_, secret, err := store.Issue("admin", "default-org", []auth.Role{auth.RoleAdmin}, "setup key", 0)
	if err != nil {
		fmt.Fprintf(out, "%s Could not create API key: %v\n", brand.IconWarn, err)
		return
	}
	if serr := store.Save(path); serr != nil {
		fmt.Fprintf(out, "%s Could not persist API key store: %v\n", brand.IconWarn, serr)
	}
	// Persist the plaintext secret where the TUI can read it back (the store
	// only keeps a bcrypt hash). 0600, user-config dir.
	keyPath := tui.APIKeyFilePath()
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err == nil {
		if werr := os.WriteFile(keyPath, []byte(secret), 0o600); werr != nil {
			fmt.Fprintf(out, "%s Could not save TUI key file: %v\n", brand.IconWarn, werr)
		}
	}

	printBox(out,
		"",
		"Your VORTEX API Key:",
		"",
		secret,
		"",
		brand.IconWarn+"  Save this now — it will not be shown again",
		"",
		"Use it for:",
		"• Dashboard login",
		"• API access",
		"• vortex ui authentication",
		"",
	)
	fmt.Fprintln(out)
	fmt.Fprint(out, "Press Enter when saved... ")
	_ = readLine(r)
}

// setupComplete prints the step-5 summary and next steps.
func setupComplete(out io.Writer, cfg *AIProviderConfig) {
	stepHeader(out, 5, "You're all set!")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "╔═══════════════════════════════════════════════════╗")
	fmt.Fprintln(out, "║                                                   ║")
	fmt.Fprintf(out, "║   %s VORTEX is configured and ready                ║\n", brand.IconSuccess)
	fmt.Fprintln(out, "║                                                   ║")
	fmt.Fprintln(out, "╚═══════════════════════════════════════════════════╝")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "What you set up:")
	if cfg.Provider != "" && cfg.Provider != "none" {
		ai := catalogByID(cfg.Provider).Name + " (primary)"
		for _, b := range cfg.Backups {
			ai += " + " + catalogByID(b.Provider).Name + " (backup)"
		}
		fmt.Fprintf(out, "  %s AI: %s\n", brand.IconSuccess, ai)
	} else {
		fmt.Fprintf(out, "  %s AI: skipped — configure later with: vortex setup\n", brand.IconIdle)
	}
	if cfg.TelegramToken != "" {
		fmt.Fprintf(out, "  %s Telegram: connected\n", brand.IconSuccess)
	} else {
		fmt.Fprintf(out, "  %s Telegram: skipped\n", brand.IconIdle)
	}
	fmt.Fprintf(out, "  %s API key: saved to config\n", brand.IconSuccess)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Next steps:")
	printBox(out,
		"",
		"Start VORTEX:",
		"  vortex start",
		"",
		"Open the dashboard:",
		"  http://localhost:9090/dashboard",
		"",
		"Open the terminal UI:",
		"  vortex ui",
		"",
		"Try your first task:",
		"  vortex ui -> Agents ->",
		"  \"create a hello world Python script\"",
		"",
		"Use VORTEX as an OpenAI-compatible backend:",
		"  export OPENAI_BASE_URL=http://localhost:9090/v1",
		"  export OPENAI_API_KEY=<your-vortex-key>",
		"",
		"Docs: https://vortex.run/docs",
		"",
	)
	if cfg.TelegramToken != "" {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Send /status to your Telegram bot to test it!")
	}
	fmt.Fprintln(out)
}

// setupOllama handles the local-Ollama flow.
func setupOllama(out io.Writer, r *bufio.Reader, cfg *AIProviderConfig) {
	fmt.Fprintf(out, "\n%s Selected: Ollama\n\n", brand.IconSuccess)
	fmt.Fprintln(out, "Ollama runs AI models locally on your machine.")
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
		fmt.Fprintf(out, "%s Could not reach Ollama (is it running?)\n", brand.IconWarn)
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
	fmt.Fprintf(out, "\n%s Selected: AWS Bedrock\n\n", brand.IconSuccess)
	fmt.Fprintln(out, "AWS Bedrock uses your AWS credentials (SigV4-signed requests).")
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
	fmt.Fprintf(out, "\n%s Selected: Azure OpenAI\n\n", brand.IconSuccess)
	fmt.Fprintln(out, "Azure OpenAI uses your Azure resource endpoint and a deployment name.")
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
	fmt.Fprintf(out, "%s Key: %s\n", brand.IconSuccess, brand.MaskSecret(key))
	enc, err := encryptKey(key)
	if err != nil {
		fmt.Fprintf(out, "%s Could not encrypt key: %v\n", brand.IconWarn, err)
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
