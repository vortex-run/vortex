package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetup_CommandRegisters(t *testing.T) {
	c := newSetupCommand()
	if c.Use != "setup" {
		t.Errorf("Use = %q, want setup", c.Use)
	}
	// It should appear under the root command.
	root := NewRootCommand()
	var found bool
	for _, sub := range root.Commands() {
		if sub.Name() == "setup" {
			found = true
		}
	}
	if !found {
		t.Error("setup should be registered on the root command")
	}
}

func TestSetup_EncryptDecryptRoundTrip(t *testing.T) {
	key := "sk-ant-abc123def456ghi789"
	enc, err := encryptKey(key)
	if err != nil {
		t.Fatalf("encryptKey: %v", err)
	}
	if enc == key {
		t.Error("encrypted value must differ from plaintext")
	}
	if strings.Contains(enc, key) {
		t.Error("ciphertext must not contain the plaintext key")
	}
	got, err := decryptKey(enc)
	if err != nil {
		t.Fatalf("decryptKey: %v", err)
	}
	if got != key {
		t.Errorf("decrypt = %q, want %q", got, key)
	}
}

func TestSetup_EmptyKeyEncryptsToEmpty(t *testing.T) {
	enc, _ := encryptKey("")
	if enc != "" {
		t.Errorf("empty key should encrypt to empty, got %q", enc)
	}
	dec, _ := decryptKey("")
	if dec != "" {
		t.Errorf("empty enc should decrypt to empty, got %q", dec)
	}
}

func TestSetup_ConfigSaveLoad(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // redirect UserConfigDir on Linux
	// On Windows UserConfigDir uses %AppData%; redirect that too.
	t.Setenv("AppData", t.TempDir())

	enc, _ := encryptKey("sk-ant-secret")
	cfg := AIProviderConfig{
		Provider: "claude", APIKeyEnc: enc, Model: "claude-sonnet-4-20250514",
		ConfiguredAt: "2026-01-01T00:00:00Z",
	}
	if err := saveProviderConfig(cfg); err != nil {
		t.Fatalf("saveProviderConfig: %v", err)
	}

	loaded, ok := loadProviderConfig()
	if !ok {
		t.Fatal("loadProviderConfig should find the saved config")
	}
	if loaded.Provider != "claude" || loaded.Model != "claude-sonnet-4-20250514" {
		t.Errorf("loaded config wrong: %+v", loaded)
	}
	dec, _ := decryptKey(loaded.APIKeyEnc)
	if dec != "sk-ant-secret" {
		t.Errorf("decrypted key = %q, want sk-ant-secret", dec)
	}
}

func TestSetup_KeyEncryptedAtRest(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())

	enc, _ := encryptKey("sk-ant-PLAINTEXTSECRET")
	if err := saveProviderConfig(AIProviderConfig{Provider: "claude", APIKeyEnc: enc}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(aiProviderConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("sk-ant-PLAINTEXTSECRET")) {
		t.Error("the API key must NOT appear in plaintext on disk")
	}
}

func TestSetup_SkipWritesNoProvider(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	// Ensure no provider env var triggers a "configured" state.
	for _, e := range []string{"VORTEX_ANTHROPIC_KEY", "VORTEX_OPENAI_KEY", "VORTEX_DEEPSEEK_KEY", "VORTEX_GEMINI_KEY", "VORTEX_OLLAMA_URL"} {
		t.Setenv(e, "")
	}

	// Isolate the API-key store so the test doesn't touch the real one.
	t.Setenv("VORTEX_APIKEY_STORE", filepath.Join(t.TempDir(), "apikeys.json"))

	var out bytes.Buffer
	// Enter (welcome), 10 (skip AI), 2 (skip Telegram), 1 (editor), Enter (key).
	if err := runSetup(&out, strings.NewReader("\n10\n2\n1\n\n")); err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	// Skipping AI must not record an AI provider (the editor-mode step may
	// persist a config with provider "none"/empty, but never a real provider).
	if cfg, ok := loadProviderConfig(); ok && cfg.Provider != "" && cfg.Provider != "none" {
		t.Errorf("skip option should not record an AI provider, got %q", cfg.Provider)
	}
	if !strings.Contains(out.String(), "Skipping AI setup") {
		t.Errorf("skip output missing message:\n%s", out.String())
	}
	// The wizard always prints the API key box + the complete screen.
	if !strings.Contains(out.String(), "Your VORTEX API Key") {
		t.Error("wizard should print the API key box")
	}
	if !strings.Contains(out.String(), "VORTEX is configured and ready") {
		t.Error("wizard should print the completion banner")
	}
}

// setupTestEnv isolates config + key-store paths for a full wizard run.
func setupTestEnv(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	t.Setenv("VORTEX_APIKEY_STORE", filepath.Join(t.TempDir(), "apikeys.json"))
	for _, e := range []string{"VORTEX_ANTHROPIC_KEY", "VORTEX_OPENAI_KEY", "VORTEX_DEEPSEEK_KEY", "VORTEX_GEMINI_KEY", "VORTEX_OLLAMA_URL"} {
		t.Setenv(e, "")
	}
}

// TestSetup_FullFlowBranded drives the whole 5-step wizard: DeepSeek primary,
// Ollama backup slot, Telegram with a stubbed verifier, editor, API key.
func TestSetup_FullFlowBranded(t *testing.T) {
	setupTestEnv(t)

	// Stub out the network verifiers (catalog entry + telegram getMe).
	verifyCalled := false
	origVerify := providerCatalog[0].Verify
	providerCatalog[0].Verify = func(string) string { verifyCalled = true; return "✓ API key verified" }
	origTG := verifyTelegramToken
	verifyTelegramToken = func(string) string { return "✓ Bot connected: @TestBot" }
	t.Cleanup(func() {
		providerCatalog[0].Verify = origVerify
		verifyTelegramToken = origTG
	})

	input := "\n" + // welcome: Enter
		"1\n" + // step 1: DeepSeek
		"sk-test-1234567890\n" + // API key
		"3\n" + // step 2: add Ollama fallback
		"4\n" + // step 2: done
		"1\n" + // step 3: telegram yes
		"123456:token\n" + // bot token
		"987654\n" + // chat id
		"1\n" + // editor: standard
		"\n" // step 4: Enter when saved
	var buf bytes.Buffer
	if err := runSetup(&buf, strings.NewReader(input)); err != nil {
		t.Fatalf("runSetup: %v", err)
	}
	out := buf.String()

	// Welcome banner names the product.
	for _, want := range []string{"VORTEX", "One binary. Any server. Fully autonomous.", "Press Enter to start setup"} {
		if !strings.Contains(out, want) {
			t.Errorf("welcome missing %q", want)
		}
	}
	// Provider table: all 9 providers + the cost column.
	for _, want := range []string{
		"PROVIDER", "COST/TASK", "BEST FOR",
		"DeepSeek", "Claude", "OpenAI GPT", "Groq", "Gemini",
		"AWS Bedrock", "Azure OpenAI", "OpenRouter", "Ollama",
		"~$0.001", "~$0.015", "Recommended",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("provider table missing %q", want)
		}
	}
	// Key entry masked after first 4 chars; plaintext never echoed.
	if !strings.Contains(out, "sk-t****") {
		t.Error("key echo not masked")
	}
	if strings.Contains(out, "sk-test-1234567890") {
		t.Error("plaintext key echoed to output")
	}
	if !verifyCalled {
		t.Error("verification step did not call the provider API")
	}
	// Multi-key step shows slot numbers.
	for _, want := range []string{"Slot 1: DeepSeek (primary)", "Slot 2: Ollama (offline fallback)"} {
		if !strings.Contains(out, want) {
			t.Errorf("backup step missing %q", want)
		}
	}
	// Telegram step shows the BotFather + getUpdates instructions and verifies.
	for _, want := range []string{"@BotFather", "getUpdates", "Bot connected: @TestBot"} {
		if !strings.Contains(out, want) {
			t.Errorf("telegram step missing %q", want)
		}
	}
	// API key box.
	for _, want := range []string{"Your VORTEX API Key", "it will not be shown again"} {
		if !strings.Contains(out, want) {
			t.Errorf("API key step missing %q", want)
		}
	}
	// Complete screen lists everything configured.
	for _, want := range []string{
		"VORTEX is configured and ready",
		"AI: DeepSeek (primary) + Ollama (backup)",
		"Telegram: connected",
		"vortex start",
		"OPENAI_BASE_URL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("complete screen missing %q", want)
		}
	}

	// Persisted config: primary + one backup slot + telegram.
	cfg, ok := loadProviderConfig()
	if !ok || cfg.Provider != "deepseek" {
		t.Fatalf("saved provider = %+v", cfg)
	}
	if len(cfg.Backups) != 1 || cfg.Backups[0].Provider != "ollama" {
		t.Errorf("backups = %+v, want one ollama slot", cfg.Backups)
	}
	if cfg.TelegramToken == "" || cfg.TelegramChatID != "987654" {
		t.Errorf("telegram not persisted: %+v", cfg)
	}
	if key, _ := decryptKey(cfg.APIKeyEnc); key != "sk-test-1234567890" {
		t.Errorf("primary key round-trip = %q", key)
	}
}

func TestSetup_ProviderConfiguredEnvOverridesFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())

	// No file, env set → configured.
	t.Setenv("VORTEX_ANTHROPIC_KEY", "sk-ant-x")
	if !providerConfigured() {
		t.Error("env var should mark provider as configured")
	}
}

func TestSetup_ProviderConfiguredFromFile(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("AppData", t.TempDir())
	for _, e := range []string{"VORTEX_ANTHROPIC_KEY", "VORTEX_OPENAI_KEY", "VORTEX_DEEPSEEK_KEY", "VORTEX_GEMINI_KEY", "VORTEX_OLLAMA_URL"} {
		t.Setenv(e, "")
	}
	if providerConfigured() {
		t.Fatal("should not be configured with no env and no file")
	}
	enc, _ := encryptKey("sk-ant-fromfile")
	_ = saveProviderConfig(AIProviderConfig{Provider: "claude", APIKeyEnc: enc, Model: "m"})
	if !providerConfigured() {
		t.Error("saved file config should mark provider as configured")
	}
}

func TestSetup_ConfigPathUnderVortexDir(t *testing.T) {
	p := aiProviderConfigPath()
	if filepath.Base(p) != "ai-provider.json" {
		t.Errorf("config path base = %q, want ai-provider.json", filepath.Base(p))
	}
	if !strings.Contains(filepath.ToSlash(p), "vortex/") {
		t.Errorf("config path should be under a vortex dir: %q", p)
	}
}
