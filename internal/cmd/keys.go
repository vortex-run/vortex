package cmd

import (
	"bufio"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/gateway"
	"github.com/vortex-run/vortex/internal/tui/brand"
)

// errKeys is the sentinel signalling a handled keys-command failure (so the
// root prints nothing extra).
var errKeys = fmt.Errorf("keys command failed")

// keystorePath resolves the on-disk key-slot database.
func keystorePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "keyslots.db")
}

// openKeyStore opens the key-slot store under the master-derived "keystore"
// key. The caller is responsible for closing it.
func openKeyStore() (*gateway.KeyStore, error) {
	encKey, err := deriveKey("keystore")
	if err != nil {
		return nil, err
	}
	return gateway.NewKeyStore(keystorePath(), encKey)
}

// nextSlotID returns the lowest free "slot-N" id (1-4), or "" when full.
func nextSlotID(store *gateway.KeyStore) string {
	slots, _ := store.List()
	used := map[string]bool{}
	for _, s := range slots {
		used[s.ID] = true
	}
	for i := 1; i <= 4; i++ {
		id := fmt.Sprintf("slot-%d", i)
		if !used[id] {
			return id
		}
	}
	return ""
}

// newKeysCommand builds `vortex keys` and its subcommands.
func newKeysCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "keys",
		Short: "Manage AI provider API-key slots (rotation, health, budgets)",
		Long: "Manage VORTEX's API-key slots for autonomous rotation.\n\n" +
			"Up to 4 slots (same or different providers) are health-scored and\n" +
			"switched automatically on rate limits or budget exhaustion, with\n" +
			"conversation context preserved across provider switches.",
	}
	c.AddCommand(
		newKeysAddCommand(),
		newKeysListCommand(),
		newKeysRemoveCommand(),
		newKeysStatusCommand(),
		newKeysModeCommand(),
		newKeysTestCommand(),
	)
	return c
}

func newKeysAddCommand() *cobra.Command {
	var provider, key, model, label string
	var priority int
	var budget float64
	c := &cobra.Command{
		Use:   "add",
		Short: "Add an API-key slot",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			if provider == "" {
				fmt.Fprintf(out, "%s --provider is required\n", brand.IconError)
				return errKeys
			}
			store, err := openKeyStore()
			if err != nil {
				fmt.Fprintf(out, "%s could not open key store: %v\n", brand.IconError, err)
				return errKeys
			}
			defer func() { _ = store.Close() }()

			if key == "" {
				fmt.Fprintf(out, "Enter API key for %s: ", provider)
				key = readLine(bufio.NewReader(cmd.InOrStdin()))
			}
			if key == "" {
				fmt.Fprintf(out, "%s no key entered\n", brand.IconError)
				return errKeys
			}

			id := nextSlotID(store)
			if id == "" {
				fmt.Fprintf(out, "%s all 4 slots are in use — remove one first (vortex keys remove <slot-id>)\n", brand.IconError)
				return errKeys
			}
			if priority <= 0 {
				priority = slotNumber(id) // default priority tracks the slot number
			}
			slot := gateway.KeySlot{
				ID: id, Provider: provider, APIKey: key, Model: model,
				Priority: priority, DailyBudget: budget, Enabled: true, Label: label,
				AddedAt: time.Now(),
			}
			if err := store.Add(slot); err != nil {
				fmt.Fprintf(out, "%s could not save slot: %v\n", brand.IconError, err)
				return errKeys
			}
			// Verify the key with a minimal call (best effort).
			fmt.Fprintf(out, "%s Verifying key...\n", brand.IconSpinner)
			if verifyKeySlot(slot) {
				fmt.Fprintf(out, "%s Verified — %s responding\n", brand.IconSuccess, provider)
			} else {
				fmt.Fprintf(out, "%s Could not verify key — saved anyway\n", brand.IconWarn)
			}
			fmt.Fprintf(out, "%s Key saved as %s (%s)\n", brand.IconSuccess, id, provider)
			return nil
		},
	}
	c.Flags().StringVar(&provider, "provider", "", "provider name (deepseek|claude|openai|gemini|groq|ollama) [required]")
	c.Flags().StringVar(&key, "key", "", "API key (prompted if omitted)")
	c.Flags().StringVar(&model, "model", "", "specific model override")
	c.Flags().IntVar(&priority, "priority", 0, "priority 1-4 (default: next available)")
	c.Flags().Float64Var(&budget, "budget", 0, "daily USD limit (default: unlimited)")
	c.Flags().StringVar(&label, "label", "", "friendly name")
	return c
}

// slotNumber extracts N from "slot-N", defaulting to 4.
func slotNumber(id string) int {
	var n int
	if _, err := fmt.Sscanf(id, "slot-%d", &n); err != nil || n <= 0 {
		return 4
	}
	return n
}

func newKeysListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List API-key slots with scores and spend",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			store, err := openKeyStore()
			if err != nil {
				fmt.Fprintf(out, "%s could not open key store: %v\n", brand.IconError, err)
				return errKeys
			}
			defer func() { _ = store.Close() }()
			slots, _ := store.List()
			if len(slots) == 0 {
				fmt.Fprintln(out, "No key slots configured. Add one: vortex keys add --provider deepseek --key <key>")
				return nil
			}
			fmt.Fprintf(out, "Mode: %s\n\n", loadKeysMode())
			fmt.Fprintf(out, "%-5s %-11s %-14s %-6s %-10s %s\n", "SLOT", "PROVIDER", "LABEL", "SCORE", "TODAY", "STATUS")
			fmt.Fprintln(out, strings.Repeat("─", 60))
			total := 0.0
			var active string
			best, _ := store.BestSlot()
			if best != nil {
				active = best.ID
			}
			for _, s := range slots {
				h, _ := store.GetHealth(s.ID)
				total += h.SpentTodayUSD
				status := brand.IconIdle + " Standby"
				if !s.Enabled {
					status = brand.IconError + " Disabled"
				} else if s.ID == active {
					status = brand.IconSuccess + " Active"
				}
				fmt.Fprintf(out, "%-5s %-11s %-14s %-6d %-10s %s\n",
					slotShort(s.ID), titleProvider(s.Provider), trunc(s.Label, 14),
					h.Score, brand.FormatCost(h.SpentTodayUSD), status)
			}
			fmt.Fprintln(out)
			if best != nil {
				fmt.Fprintf(out, "Current: %s (%s %s)\n", slotShort(best.ID), titleProvider(best.Provider), best.Label)
			}
			fmt.Fprintf(out, "Daily total: %s\n", brand.FormatCost(total))
			return nil
		},
	}
}

func newKeysRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <slot-id>",
		Short: "Remove an API-key slot",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openKeyStore()
			if err != nil {
				fmt.Fprintf(out, "%s could not open key store: %v\n", brand.IconError, err)
				return errKeys
			}
			defer func() { _ = store.Close() }()
			id := normalizeSlotID(args[0])
			slot, err := store.Get(id)
			if err != nil {
				fmt.Fprintf(out, "%s no such slot: %s\n", brand.IconError, id)
				return errKeys
			}
			fmt.Fprintf(out, "Remove %s (%s %s)? [y/N]: ", slotShort(id), titleProvider(slot.Provider), slot.Label)
			if !strings.EqualFold(readLine(bufio.NewReader(cmd.InOrStdin())), "y") {
				fmt.Fprintln(out, "Cancelled.")
				return nil
			}
			if err := store.Remove(id); err != nil {
				fmt.Fprintf(out, "%s could not remove: %v\n", brand.IconError, err)
				return errKeys
			}
			fmt.Fprintf(out, "%s Removed %s\n", brand.IconSuccess, slotShort(id))
			return nil
		},
	}
}

func newKeysStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show detailed health for all slots",
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			store, err := openKeyStore()
			if err != nil {
				fmt.Fprintf(out, "%s could not open key store: %v\n", brand.IconError, err)
				return errKeys
			}
			defer func() { _ = store.Close() }()
			slots, _ := store.List()
			if len(slots) == 0 {
				fmt.Fprintln(out, "No key slots configured.")
				return nil
			}
			best, _ := store.BestSlot()
			active := ""
			if best != nil {
				active = best.ID
			}
			for _, s := range slots {
				h, _ := store.GetHealth(s.ID)
				marker := brand.IconIdle + " Standby"
				if s.ID == active {
					marker = brand.IconSuccess + " Active"
				}
				fmt.Fprintf(out, "\n%s — %s (%s) %s\n", slotShort(s.ID), titleProvider(s.Provider), s.Label, marker)
				fmt.Fprintln(out, strings.Repeat("─", 45))
				fmt.Fprintf(out, "Score:       %d/100\n", h.Score)
				fmt.Fprintf(out, "Requests:    %d today\n", h.RequestsToday)
				fmt.Fprintf(out, "Errors:      %d of last 10\n", h.ErrorsLast10)
				fmt.Fprintf(out, "Avg latency: %s\n", latencyStr(h.AvgLatencyMs))
				budget := "unlimited"
				if s.DailyBudget > 0 {
					budget = brand.FormatCost(s.DailyBudget)
				}
				fmt.Fprintf(out, "Spent today: %s of %s\n", brand.FormatCost(h.SpentTodayUSD), budget)
				fmt.Fprintf(out, "Last used:   %s\n", agoStr(h.LastUsed))
			}
			return nil
		},
	}
}

func newKeysModeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "mode <auto|cheap|fast|quality|balanced>",
		Short: "Set the routing/budget mode",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			mode := strings.ToLower(args[0])
			valid := map[string]bool{"auto": true, "cheap": true, "fast": true, "quality": true, "balanced": true}
			if !valid[mode] {
				fmt.Fprintf(out, "%s invalid mode %q (want auto|cheap|fast|quality|balanced)\n", brand.IconError, mode)
				return errKeys
			}
			if err := saveKeysMode(mode); err != nil {
				fmt.Fprintf(out, "%s could not save mode: %v\n", brand.IconError, err)
				return errKeys
			}
			fmt.Fprintf(out, "%s Mode set to: %s\n", brand.IconSuccess, mode)
			return nil
		},
	}
}

func newKeysTestCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "test [slot-id]",
		Short: "Test one slot or all slots",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			store, err := openKeyStore()
			if err != nil {
				fmt.Fprintf(out, "%s could not open key store: %v\n", brand.IconError, err)
				return errKeys
			}
			defer func() { _ = store.Close() }()
			var slots []gateway.KeySlot
			if len(args) == 1 {
				s, gerr := store.GetDecrypted(normalizeSlotID(args[0]))
				if gerr != nil {
					fmt.Fprintf(out, "%s no such slot: %s\n", brand.IconError, args[0])
					return errKeys
				}
				slots = []gateway.KeySlot{*s}
			} else {
				all, _ := store.List()
				for _, s := range all {
					if d, derr := store.GetDecrypted(s.ID); derr == nil {
						slots = append(slots, *d)
					}
				}
			}
			if len(slots) == 0 {
				fmt.Fprintln(out, "No slots to test.")
				return nil
			}
			sort.Slice(slots, func(i, j int) bool { return slots[i].ID < slots[j].ID })
			for _, s := range slots {
				fmt.Fprintf(out, "Testing %s (%s)... ", slotShort(s.ID), titleProvider(s.Provider))
				start := time.Now()
				if verifyKeySlot(s) {
					fmt.Fprintf(out, "%s %.1fs\n", brand.IconSuccess, time.Since(start).Seconds())
				} else {
					fmt.Fprintf(out, "%s not responding\n", brand.IconError)
				}
			}
			return nil
		},
	}
}

// --- helpers ----------------------------------------------------------------

// keysModePath stores the selected routing mode.
func keysModePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "keys-mode")
}

// saveKeysMode persists the routing mode for start.go to read.
func saveKeysMode(mode string) error {
	path := keysModePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(mode), 0o600)
}

// loadKeysMode reads the saved routing mode, defaulting to "auto".
func loadKeysMode() string {
	data, err := os.ReadFile(keysModePath())
	if err != nil {
		return "auto"
	}
	m := strings.TrimSpace(string(data))
	if m == "" {
		return "auto"
	}
	return m
}

// verifyKeySlot makes a minimal request to confirm the slot's key works.
// Returns false on any error (including unconfigured providers).
func verifyKeySlot(slot gateway.KeySlot) bool {
	switch slot.Provider {
	case "deepseek", "openai", "groq", "openrouter":
		url := openAICompatVerifyURL(slot.Provider, slot.Endpoint)
		return httpVerifyOK(http.MethodPost, url,
			map[string]string{"Authorization": "Bearer " + slot.APIKey},
			map[string]any{"model": slot.Model, "max_tokens": 1,
				"messages": []map[string]any{{"role": "user", "content": "hi"}}})
	case "claude":
		return httpVerifyOK(http.MethodPost, "https://api.anthropic.com/v1/messages",
			map[string]string{"x-api-key": slot.APIKey, "anthropic-version": "2023-06-01"},
			map[string]any{"model": slot.Model, "max_tokens": 1,
				"messages": []map[string]any{{"role": "user", "content": "hi"}}})
	case "ollama":
		base := slot.Endpoint
		if base == "" {
			base = "http://localhost:11434"
		}
		return httpVerifyOK(http.MethodGet, base+"/api/tags", nil, nil)
	default:
		return false
	}
}

// openAICompatVerifyURL returns the chat-completions URL for an OpenAI-compatible
// provider, honouring an endpoint override.
func openAICompatVerifyURL(provider, endpoint string) string {
	if endpoint != "" {
		return strings.TrimRight(endpoint, "/") + "/v1/chat/completions"
	}
	switch provider {
	case "deepseek":
		return "https://api.deepseek.com/v1/chat/completions"
	case "groq":
		return "https://api.groq.com/openai/v1/chat/completions"
	case "openrouter":
		return "https://openrouter.ai/api/v1/chat/completions"
	default:
		return "https://api.openai.com/v1/chat/completions"
	}
}

// httpVerifyOK reports whether a minimal request returns a non-auth-error
// status (2xx or any non-401/403 — a 400 still proves the key is accepted).
func httpVerifyOK(method, url string, headers map[string]string, payload any) bool {
	res := httpVerify(method, url, headers, payload)
	return strings.HasPrefix(res, brand.IconSuccess) || strings.Contains(res, "verified")
}

// slotShort renders "slot-1" as "1".
func slotShort(id string) string { return fmt.Sprintf("%d", slotNumber(id)) }

// normalizeSlotID accepts "1" or "slot-1" and returns "slot-1".
func normalizeSlotID(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "slot-") {
		return s
	}
	return "slot-" + s
}

// titleProvider returns a display-cased provider name.
func titleProvider(p string) string {
	switch p {
	case "deepseek":
		return "DeepSeek"
	case "openai":
		return "OpenAI"
	case "claude":
		return "Claude"
	case "gemini":
		return "Gemini"
	case "groq":
		return "Groq"
	case "ollama":
		return "Ollama"
	case "openrouter":
		return "OpenRouter"
	default:
		if p == "" {
			return "—"
		}
		return strings.ToUpper(p[:1]) + p[1:]
	}
}

// trunc shortens s to n runes with an ellipsis.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// latencyStr formats a millisecond latency as seconds or "local".
func latencyStr(ms int64) string {
	if ms == 0 {
		return "—"
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// agoStr renders a "time ago" string for a last-used timestamp.
func agoStr(t time.Time) string {
	if t.IsZero() || t.UnixMilli() == 0 {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
}
