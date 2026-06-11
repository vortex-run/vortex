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
	// Option 10 (skip AI), decline Telegram, default editor mode.
	if err := runSetup(&out, strings.NewReader("10\nN\n1\n")); err != nil {
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
	// The wizard always prints the completion footer + an API key box.
	if !strings.Contains(out.String(), "Setup Complete") {
		t.Error("wizard should print the completion banner")
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
