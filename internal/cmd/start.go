package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/a2a"
	"github.com/vortex-run/vortex/internal/agents"
	"github.com/vortex-run/vortex/internal/api"
	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/auth"
	"github.com/vortex-run/vortex/internal/cluster"
	"github.com/vortex-run/vortex/internal/config"
	"github.com/vortex-run/vortex/internal/devops"
	"github.com/vortex-run/vortex/internal/forge"
	"github.com/vortex-run/vortex/internal/gateway"
	"github.com/vortex-run/vortex/internal/healing"
	"github.com/vortex-run/vortex/internal/messaging"
	"github.com/vortex-run/vortex/internal/observability"
	"github.com/vortex-run/vortex/internal/orchestration"
	"github.com/vortex-run/vortex/internal/perf"
	"github.com/vortex-run/vortex/internal/pipeline"
	"github.com/vortex-run/vortex/internal/plugins"
	"github.com/vortex-run/vortex/internal/policy"
	"github.com/vortex-run/vortex/internal/proxy"
	"github.com/vortex-run/vortex/internal/proxy/tcp"
	"github.com/vortex-run/vortex/internal/research"
	"github.com/vortex-run/vortex/internal/secrets"
	"github.com/vortex-run/vortex/internal/security"
	"github.com/vortex-run/vortex/internal/startup"
	"github.com/vortex-run/vortex/internal/studio"
	"github.com/vortex-run/vortex/internal/tenancy"
	vtls "github.com/vortex-run/vortex/internal/tls"
	"github.com/vortex-run/vortex/internal/tui/brand"
	"github.com/vortex-run/vortex/pkg/lifecycle"
	"github.com/vortex-run/vortex/pkg/logger"
	"go.opentelemetry.io/otel/trace"
)

// newStartCommand builds `vortex start`, which loads and validates config,
// writes a PID file, starts the management API, wires SIGHUP hot-reload, then
// blocks until a shutdown signal — removing the PID file on the way out.
func newStartCommand() *cobra.Command {
	var pidfile string
	var setup bool
	var withUI bool
	c := &cobra.Command{
		Use:   "start",
		Short: "Start the VORTEX server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// Run the first-run wizard when --setup is given, or automatically on
			// first start when no AI provider is configured AND stdin is an
			// interactive terminal (never block a non-interactive/scripted start,
			// e.g. integration tests or systemd).
			if setup || (!providerConfigured() && isInteractive()) {
				if err := runSetup(cmd.OutOrStdout(), cmd.InOrStdin()); err != nil {
					return err
				}
			}
			if withUI {
				// Start the server in the background, then open the TUI; the TUI
				// owns the foreground until the user quits.
				return runUI(cmd.Context(), "http://localhost:9090", "", true)
			}
			return runStart(cmd.Context(), pidfile)
		},
	}
	c.Flags().StringVar(&pidfile, "pidfile", "vortex.pid", "path to the PID file")
	c.Flags().BoolVar(&setup, "setup", false, "run the interactive setup wizard before starting")
	c.Flags().BoolVar(&withUI, "ui", false, "start the server and open the terminal dashboard")
	c.Flags().BoolVar(&startVerbose, "verbose", false, "show raw structured logs instead of the clean startup sequence")
	c.Flags().BoolVar(&teamMode, "team", false, "enable the specialist agent team (Code/Test/Review agents)")
	return c
}

// startVerbose selects raw structured logs over the clean startup display
// (--verbose). Non-interactive runs (systemd, CI, pipes) always use raw logs
// regardless, so journals never lose records to the cosmetic display.
var startVerbose bool

// teamMode enables the specialist agent team (--team or VORTEX_TEAM_MODE=true).
var teamMode bool

// telegramFromFile reads the Telegram token + chat id from the setup config
// (ai-provider.json), returning ok=false when absent.
func telegramFromFile() (token string, chatID int64, ok bool) {
	cfg, found := loadProviderConfig()
	if !found || cfg.TelegramToken == "" {
		return "", 0, false
	}
	chatID = atoi64Default(cfg.TelegramChatID, 0)
	return cfg.TelegramToken, chatID, true
}

// resolveWorkingDir returns the base directory the agents use for relative
// paths (where the Code Agent writes files). It honours VORTEX_WORK_DIR so the
// server can be pointed at a project directory (e.g. the one the user opens with
// `vortex code`), falling back to the process working directory. It is shown in
// the TUI top bar; the agent's FS/terminal tools have no path confinement (the
// approval gate is the security control), so this never restricts where files
// may be written.
func resolveWorkingDir() string {
	if dir := strings.TrimSpace(os.Getenv("VORTEX_WORK_DIR")); dir != "" {
		if abs, err := filepath.Abs(dir); err == nil {
			return abs
		}
		return dir
	}
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

// readinessQueueDepthLimit is the agent submit-queue depth above which /ready
// reports not-ready (production audit I3).
const readinessQueueDepthLimit = 1000

// stdoutIsTerminal reports whether stdout is a character device (a console).
// Piped/captured stdout (CI harnesses, journald, shell redirects) must keep
// the raw structured logs.
func stdoutIsTerminal() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// routerNotifierAdapter adapts a messaging.Router to gateway.Notifier so the
// key-rotation router can alert via Telegram without gateway importing
// messaging.
type routerNotifierAdapter struct{ router *messaging.Router }

func (a routerNotifierAdapter) Notify(title, body string) {
	if a.router != nil {
		_ = a.router.Send(context.Background(), messaging.SeverityWarn, title, body)
	}
}

// wireKeyRotation opens the key-slot store and, when slots are configured,
// enables autonomous rotation on the gateway: a health-scored router with
// failover, a context bridge for provider switches, a background health
// monitor, a midnight daily-stats reset, and the GET /api/keys/status feed.
// With no slots it logs single-provider mode (backward compatible).
func wireKeyRotation(ctx context.Context, apiSrv *api.Server, gw *messaging.AIGateway, notifier *messaging.Router, display *startup.Display, log *slog.Logger) {
	encKey, err := deriveKey("keystore")
	if err != nil {
		log.Warn("key rotation unavailable: could not derive keystore key", "err", err)
		return
	}
	store, err := gateway.NewKeyStore(keystorePath(), encKey)
	if err != nil {
		log.Warn("key rotation unavailable: could not open key store", "err", err)
		return
	}

	slots, _ := store.List()
	if len(slots) == 0 {
		log.Info("AI gateway: single provider mode")
		display.Step("Key rotation", "single provider mode")
		_ = store.Close()
		return
	}

	mode := gateway.BudgetMode(loadKeysMode())
	router := gateway.NewRouter(gateway.RouterConfig{
		Store:    store,
		Mode:     mode,
		Notifier: routerNotifierAdapter{router: notifier},
	})
	bridge := gateway.NewContextBridge(gw)
	gw.SetKeyRotation(router, bridge, store)

	// Feed GET /api/keys/status (TUI Keys view + top-bar indicator).
	apiSrv.SetKeyStatusProvider(func() api.KeyStatusInfo {
		return keyStatusSnapshot(store, router, string(mode))
	})

	// Prime the active slot so the status feed has one before the first call.
	_, _ = router.SelectSlot(ctx, "simple")

	safeGo(ctx, log, "key-health-monitor", func() { router.StartHealthMonitor(ctx) })
	safeGo(ctx, log, "key-midnight-reset", func() { midnightReset(ctx, store, log) })

	log.Info("AI gateway: key rotation enabled", "slots", len(slots), "mode", string(mode))
	display.Step("Key rotation", fmt.Sprintf("enabled, %d slots (%s)", len(slots), mode))
}

// keyStatusSnapshot builds the API status response from the store + router.
func keyStatusSnapshot(store *gateway.KeyStore, router *gateway.Router, mode string) api.KeyStatusInfo {
	active := router.ActiveSlotID()
	slots, _ := store.List()
	out := api.KeyStatusInfo{Mode: mode, Slots: []api.KeySlotInfo{}}
	for _, s := range slots {
		h, _ := store.GetHealth(s.ID)
		out.TotalUSD += h.SpentTodayUSD
		// Mask the real key (decrypt only to take a 4-char prefix; never expose
		// the full value through the API).
		masked := ""
		if dec, derr := store.GetDecrypted(s.ID); derr == nil {
			masked = brand.MaskSecret(dec.APIKey)
		}
		out.Slots = append(out.Slots, api.KeySlotInfo{
			ID: s.ID, Provider: s.Provider, Label: s.Label, Model: s.Model,
			MaskedKey: masked, Priority: s.Priority, Enabled: s.Enabled,
			Score: h.Score, RequestsToday: h.RequestsToday, ErrorsLast10: h.ErrorsLast10,
			AvgLatencyMs: h.AvgLatencyMs, SpentTodayUSD: h.SpentTodayUSD, DailyBudget: s.DailyBudget,
			RateLimited: h.RateLimited, Active: s.ID == active,
		})
	}
	return out
}

// midnightReset zeroes per-day key stats at each local midnight until ctx ends.
func midnightReset(ctx context.Context, store *gateway.KeyStore, log *slog.Logger) {
	for {
		now := time.Now()
		next := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).Add(24 * time.Hour)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			if err := store.ResetDailyStats(); err != nil {
				log.Warn("midnight key-stats reset failed", "err", err)
			} else {
				log.Info("key rotation: daily stats reset")
			}
		}
	}
}

// safeGo runs fn in a goroutine with panic recovery (a panicking background
// monitor must never take down VORTEX).
func safeGo(_ context.Context, log *slog.Logger, name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Error("background goroutine panic recovered", "goroutine", name, "panic", r)
			}
		}()
		fn()
	}()
}

// portSuffix turns a listen address (":9090", "0.0.0.0:9090") into ":port".
func portSuffix(addr string) string {
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		return addr[i:]
	}
	if addr == "" {
		return ":9090"
	}
	return ":" + addr
}

// envTrue reports whether an env var is set to a truthy value.
func envTrue(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// wireAgentTeam builds the A2A server, registers the specialist agents, mounts
// the A2A tree on the API server, enables team mode on the coordinator, and
// exposes the /api/team/agents view (agent teams). It also wires the AG-UI
// collaboration layer: a MessageBus for inter-agent visibility, a
// CheckpointManager for human review between steps, the collaboration API
// endpoints, and (when configured) the Telegram team bridge.
func wireAgentTeam(ctx context.Context, apiSrv *api.Server, rt *agents.Runtime, gateway agents.AIGateway, notifier *messaging.Router, telegram *messaging.TelegramBot, workDir string, display *startup.Display, log *slog.Logger) {
	a2aServer := a2a.NewAgentServer()
	baseURL := "http://localhost" + portSuffix(apiSrv.Addr())
	a2aClient := a2a.NewAgentClient(baseURL, "coordinator")

	// AG-UI collaboration layer: the bus carries all inter-agent traffic for the
	// UI; the checkpoint manager pauses between steps for human review. The bus
	// must be set on the server before Register so each agent's DirectChat
	// publishes to it.
	bus := a2a.NewMessageBus()
	a2aServer.SetBus(bus)
	checkpoints := a2a.NewCheckpointManager(bus, 0) // 0 = always wait for a human

	// Trusted tool registry for the specialists (file ops without approval;
	// terminal/commit still gated).
	tools, terr := agents.NewTrustedToolRegistry(agents.LocalFSConfig{Root: workDir})
	if terr != nil {
		log.Warn("agent team disabled: tool registry failed", "err", terr)
		return
	}

	team := agents.NewAgentTeam(agents.TeamConfig{
		Client:      a2aClient,
		Server:      a2aServer,
		Notifier:    routerNotifierAdapter{router: notifier},
		WorkDir:     workDir,
		BaseURL:     baseURL,
		Bus:         bus,
		Checkpoints: checkpoints,
	}, gateway, tools)

	rt.Coordinator().SetTeam(team)
	apiSrv.SetA2AHandler(a2aServer.Handler())
	apiSrv.SetTeamAgentsProvider(func() []api.TeamAgentInfo {
		out := []api.TeamAgentInfo{}
		// Coordinator first, then the specialists from the A2A server.
		out = append(out, api.TeamAgentInfo{
			ID: "coordinator", Name: "VORTEX Coordinator", Role: "coordinator", Status: "idle",
		})
		for _, c := range a2aServer.List() {
			out = append(out, api.TeamAgentInfo{
				ID: c.ID, Name: c.Name, Role: c.Role, Status: c.Status, Capabilities: c.Capabilities,
			})
		}
		return out
	})

	// Collaboration API: comms feed + SSE, direct chat, checkpoint review.
	collab := &teamCollab{server: a2aServer, bus: bus, checkpoints: checkpoints}
	apiSrv.SetCommsProvider(collab)
	apiSrv.SetChatProvider(collab)
	apiSrv.SetCheckpointProvider(collab)

	// Telegram team bridge: live progress, checkpoint buttons, "@agent" chat.
	if telegram != nil {
		if chatID, ok := primaryTelegramChatID(); ok {
			bridge := messaging.NewTeamBridge(telegram, chatID, "", checkpoints, collab)
			telegram.SetTeamCallbackResolver(bridge)
			telegram.SetMentionHandler(func(ctx context.Context, _ int64, text string) bool {
				return bridge.HandleMention(ctx, text)
			})
			go bridge.Run(ctx, bus)
			log.Info("telegram team bridge enabled", "chat_id", chatID)
		}
	}

	log.Info("agent team enabled", "agents", []string{"coordinator", "code-agent", "test-agent", "review-agent"})
	display.Step("Agent team", "coordinator, code-agent, test-agent, review-agent")
}

// teamCollab adapts the a2a bus / server / checkpoint manager to the api and
// messaging provider interfaces (CommsProvider, ChatProvider,
// CheckpointProvider, and the bridge's directChatter).
type teamCollab struct {
	server      *a2a.AgentServer
	bus         *a2a.MessageBus
	checkpoints *a2a.CheckpointManager
}

// History returns the recent comms feed as api.CommsRecord values.
func (t *teamCollab) History(limit int) []api.CommsRecord {
	msgs := t.bus.History("", limit)
	out := make([]api.CommsRecord, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, busToRecord(m))
	}
	return out
}

// Subscribe bridges the bus subscription to api.CommsRecord values.
func (t *teamCollab) Subscribe() (<-chan api.CommsRecord, func()) {
	src, unsub := t.bus.Subscribe()
	out := make(chan api.CommsRecord, 64)
	go func() {
		defer close(out)
		for m := range src {
			select {
			case out <- busToRecord(m):
			default: // drop to a slow consumer rather than stall the bus
			}
		}
	}()
	return out, unsub
}

// Chat routes a direct-chat message to a registered specialist.
func (t *teamCollab) Chat(ctx context.Context, agentID, sessionID, message string) (string, error) {
	dc := t.server.DirectChatFor(agentID)
	if dc == nil {
		return "", fmt.Errorf("unknown agent: %s", agentID)
	}
	return dc.Send(ctx, sessionID, message)
}

// List returns the pending checkpoints across all sessions.
func (t *teamCollab) List() []api.CheckpointRecord {
	pending := t.checkpoints.Pending("")
	out := make([]api.CheckpointRecord, 0, len(pending))
	for _, cp := range pending {
		files := make([]api.CheckpointFileRecord, 0, len(cp.Files))
		for _, f := range cp.Files {
			files = append(files, api.CheckpointFileRecord{Path: f.Path, Lines: f.Lines, IsNew: f.IsNew})
		}
		out = append(out, api.CheckpointRecord{
			ID: cp.ID, SessionID: cp.SessionID, FromAgent: cp.FromAgent, ToAgent: cp.ToAgent,
			Title: cp.Title, Description: cp.Description, Status: cp.Status, Files: files,
			CreatedAt: cp.CreatedAt,
		})
	}
	return out
}

// Approve resolves a pending checkpoint, unblocking the pipeline.
func (t *teamCollab) Approve(id string) error { return t.checkpoints.Approve(id) }

// Reject stops the pipeline at a checkpoint.
func (t *teamCollab) Reject(id, reason string) error { return t.checkpoints.Reject(id, reason) }

// Get returns a checkpoint by ID (for the Telegram bridge's file preview).
func (t *teamCollab) Get(id string) (*a2a.Checkpoint, error) { return t.checkpoints.Get(id) }

// busToRecord converts an a2a bus message to the api comms record shape.
func busToRecord(m a2a.BusMessage) api.CommsRecord {
	return api.CommsRecord{
		ID: m.ID, From: m.From, To: m.To, Type: m.Type, Content: m.Content,
		SessionID: m.SessionID, Timestamp: m.Timestamp,
	}
}

// primaryTelegramChatID returns the first allowed Telegram chat ID (the user's
// own chat), used as the default target for the team bridge's proactive
// messages. It reads VORTEX_TELEGRAM_ALLOWED_IDS (comma-separated).
func primaryTelegramChatID() (int64, bool) {
	raw := strings.TrimSpace(os.Getenv("VORTEX_TELEGRAM_ALLOWED_IDS"))
	if raw == "" {
		return 0, false
	}
	first, _, _ := strings.Cut(raw, ",")
	id, err := strconv.ParseInt(strings.TrimSpace(first), 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// routeNames lists the configured route names for the startup display.
func routeNames(cfg *config.Config) []string {
	out := make([]string, 0, len(cfg.Routes))
	for _, r := range cfg.Routes {
		out = append(out, r.Name)
	}
	return out
}

// runStart performs the start sequence. ctx controls shutdown: cancelling it
// (or a SIGTERM/SIGINT) triggers a graceful stop. It is separated from the
// cobra command so tests can drive it with a cancellable context instead of
// blocking on real signals.
func runStart(ctx context.Context, pidfile string) error {
	// Clean human startup display (brand redesign part 2): active only for an
	// interactive terminal without --verbose. While active, console logging is
	// silenced (the ring buffer for the TUI log viewer still receives every
	// record) and each subsystem reports through display.Step instead.
	// BOTH stdin and stdout must be terminals: stdin alone is not enough
	// because /dev/null is a character device on Linux, which made CI's
	// integration harness (piped stdout, /dev/null stdin) lose the raw logs
	// its assertions read.
	display := startup.NewStartupDisplay(startVerbose || !isInteractive() || !stdoutIsTerminal())
	if display.Active() {
		log = logger.New(logger.Config{
			Level:  logger.ParseLevel(flags.logLevel),
			Format: logger.FormatText,
			Output: io.Discard,
		})
	}

	cfgMgr, err := config.NewManager(flags.configPath, log)
	if err != nil {
		display.StepFail("Configuration", err.Error())
		return fmt.Errorf("config invalid, refusing to start: %w", err)
	}
	cfg := cfgMgr.Current()
	display.Banner()

	if err := writePIDFile(pidfile); err != nil {
		return err
	}

	mgr := lifecycle.New(lifecycle.Config{Logger: log})
	cfgMgr.RegisterReload(mgr)

	// One-time migration (production audit C1): re-key any legacy
	// cluster-name-keyed secret store / audit log onto the master key. Runs
	// before those subsystems open so they see the migrated data.
	migrateLegacyKeys(cfg, log)

	// Audit log: tamper-proof record of security-relevant events. Keyed by a
	// master-key-derived subkey so the `vortex audit` CLI verifies the same
	// chain (both derive "audit" from the shared master key).
	auditLog, err := openRuntimeAuditLog(log)
	if err != nil {
		display.StepFail("Audit log", err.Error())
		return fmt.Errorf("initialising audit log: %w", err)
	}
	if auditLog != nil {
		display.Step("Audit log", "enabled, tamper-proof chain")
	} else {
		display.Step("Audit log", "disabled")
	}
	if auditLog != nil {
		_ = auditLog.Append(ctx, "system", "vortex.start", "server", map[string]any{
			"version": version, "cluster": cfg.Cluster.Name,
		})
		mgr.OnShutdown("audit", func(context.Context) error {
			_ = auditLog.Append(context.Background(), "system", "vortex.stop", "server", nil)
			return nil
		})
	}

	apiSrv := api.New(api.DefaultAddr, cfgMgr.Holder(), version, log)
	apiSrv.SetAuditLog(auditLog)

	// Authentication: load (or start) the API-key store and seed the RBAC roles,
	// then protect the management API. /internal/* stays reachable from localhost
	// without a key (control plane); /api/keys requires an admin key.
	keyStore, keyStorePath := openAPIKeyStore(log)
	// Persist on every Issue/Revoke (atomic write) so an unclean exit does not
	// lose keys issued since boot (production audit M3); the shutdown hook
	// below is now a final flush rather than the only save.
	keyStore.SetPath(keyStorePath)
	rbac := auth.NewRBAC()
	apiSrv.SetAuth(auth.NewAuthMiddleware(keyStore, nil, rbac), keyStore, rbac)
	log.Info("auth middleware enabled", "key_store", keyStorePath, "roles", len(rbac.Roles()))
	// Make sure at least one key exists so the local TUI/dashboard can
	// authenticate: import the setup-written tui-key, else auto-create one and
	// persist its raw secret to tui-key for the TUI to read back.
	ensureAPIKey(keyStore, log)
	display.Step("Authentication", fmt.Sprintf("enabled, %d key(s)", keyStore.Count()))
	mgr.OnShutdown("apikeys", func(context.Context) error {
		if err := keyStore.Save(keyStorePath); err != nil {
			log.Warn("saving API key store failed", "err", err)
		}
		return nil
	})

	// Windows-safe control plane: POST /internal/reload and /internal/shutdown
	// stand in for SIGHUP/SIGTERM, which Windows lacks. Reload goes through the
	// lifecycle so ALL reload hooks fire (config swap + proxy rebuild), not just
	// the config swap.
	apiSrv.SetReloadFunc(func() error { mgr.Reload(); return nil })
	apiSrv.SetShutdownFunc(mgr.Shutdown)
	apiSrv.Start()
	// Background sweeps that bound the rate-limiter maps (production audit H5).
	apiSrv.StartCleanup(ctx)
	mgr.OnShutdown("api", func(ctx context.Context) error {
		return apiSrv.Shutdown(ctx)
	})
	mgr.OnShutdown("pidfile", func(context.Context) error {
		return os.Remove(pidfile)
	})

	// In-memory log ring buffer feeding GET /api/logs (the TUI log viewer).
	logBuffer := api.NewLogBuffer(1000)
	apiSrv.SetLogBuffer(logBuffer)

	// Re-derive the logger from the loaded config now that observability
	// settings (level, sink, file, sampling) are known. Tee records into the
	// ring buffer too.
	format := logger.FormatText
	if flags.jsonLog {
		format = logger.FormatJSON
	}
	// While the clean display is active the console output is discarded — the
	// ring buffer (TUI/API log viewer) still receives every record.
	var consoleOut io.Writer
	if display.Active() && cfg.Observability.LogFile == "" {
		consoleOut = io.Discard
	}
	log = logger.New(logger.Config{
		Level:    logger.ParseLevel(cfg.Observability.LogLevel),
		Format:   format,
		Output:   consoleOut,
		Sink:     logger.Sink(cfg.Observability.LogSink),
		Path:     cfg.Observability.LogFile,
		Sampling: cfg.Observability.LogSampling,
		Buffer:   &logBufferAdapter{buf: logBuffer},
	})

	// --- secrets: validate declared keys, open store, load injectable env ---
	secretEnv, err := loadSecrets(ctx, cfg, log)
	if err != nil {
		display.StepFail("Secrets", err.Error())
		return fmt.Errorf("initialising secrets: %w", err)
	}
	display.Step("Secrets", fmt.Sprintf("%d/%d loaded", len(secretEnv), len(cfg.Secrets.Keys)))

	// --- policy: OPA authorization engine (opt-in via VORTEX_POLICY_DIR) -----
	policyEngine, err := buildPolicyEngine(log)
	if err != nil {
		display.StepFail("Policy engine", err.Error())
		return fmt.Errorf("initialising policy engine: %w", err)
	}
	if os.Getenv("VORTEX_POLICY_DIR") != "" {
		display.Step("Policy engine", "custom policies loaded")
	} else {
		display.Step("Policy engine", "default allow")
	}
	// Hot-reload policy on SIGHUP / POST /internal/reload alongside the config.
	mgr.OnReload("policy", func(context.Context) error {
		if rerr := policyEngine.Reload(ctx); rerr != nil {
			log.Error("policy reload failed, keeping previous policy", "err", rerr)
		}
		return nil
	})

	// --- security edge: IP blocking + rate limiting at the L7 edge ----------
	edge, err := buildSecurityEdge(ctx, cfg, log)
	if err != nil {
		display.StepFail("Security edge", err.Error())
		return fmt.Errorf("initialising security edge: %w", err)
	}
	display.Step("Security edge", "active")

	// --- observability: metrics, tracing, profiling ------------------------
	metrics := observability.NewMetrics("vortex")
	apiSrv.SetMetricsHandler(metrics.Handler())

	tracerProvider, err := observability.NewTracer(ctx, observability.TracerConfig{
		ServiceName: cfg.Cluster.Name,
		Endpoint:    cfg.Observability.TraceEndpoint,
		Enabled:     cfg.Observability.Tracing && cfg.Observability.TraceEndpoint != "",
	})
	if err != nil {
		return fmt.Errorf("initialising tracing: %w", err)
	}
	mgr.OnShutdown("tracer", func(c context.Context) error {
		return observability.ShutdownTracer(c, tracerProvider)
	})
	tracer := tracerProvider.Tracer("vortex/proxy")

	profiler := observability.NewProfiler(observability.ProfilerConfig{
		Enabled: os.Getenv("VORTEX_PPROF") == "true",
	})
	go func() {
		if perr := profiler.Start(ctx); perr != nil {
			log.Warn("profiler stopped with error", "err", perr)
		}
	}()

	log.Info("observability started",
		"tracing", cfg.Observability.Tracing && cfg.Observability.TraceEndpoint != "",
		"profiling", os.Getenv("VORTEX_PPROF") == "true",
		"metrics_path", "/metrics",
	)
	if cfg.Observability.Tracing && cfg.Observability.TraceEndpoint != "" {
		display.Step("Observability", "tracing + metrics")
	} else {
		display.Step("Observability", "metrics")
	}

	// --- plugins: sandboxed WASM runtime + registry -------------------------
	pluginRuntime, pluginRegistry, err := buildPlugins(ctx, log)
	if err != nil {
		return fmt.Errorf("initialising plugins: %w", err)
	}
	if pluginRuntime != nil {
		mgr.OnShutdown("plugins", func(c context.Context) error { return pluginRuntime.Close(c) })
	}

	// --- performance: OS tuning + optional autoscaler -----------------------
	applyTuning(log)
	startAutoscaler(ctx, log)

	// --- tenancy: namespace registry + quota enforcer -----------------------
	tenantRegistry, tenantEnforcer := buildTenancy(log)

	// --- cluster: gossip + raft when multi-node, else single-node mode ------
	clusterMgr, err := buildCluster(ctx, cfg, log)
	if err != nil {
		display.StepFail("Cluster", err.Error())
		return fmt.Errorf("initialising cluster: %w", err)
	}
	if len(cfg.Cluster.Nodes) > 1 {
		display.Step("Cluster", fmt.Sprintf("%s, %d members", cfg.Cluster.Name, len(cfg.Cluster.Nodes)))
	} else {
		display.Step("Cluster", cfg.Cluster.Name+", single node")
	}
	if clusterMgr != nil {
		mgr.OnShutdown("cluster", func(context.Context) error { return clusterMgr.Shutdown() })
	}

	// --- data plane: TLS manager, connection pool, proxy manager -----------

	tlsMgr, err := buildTLSManager(cfg, log)
	if err != nil {
		return fmt.Errorf("initialising TLS: %w", err)
	}
	if tlsMgr != nil {
		// ACME renewal + 24h session ticket key rotation (M19).
		tlsMgr.StartBackground(ctx)
	}

	// mTLS identity mesh: when any route has mtls:true, set up the cluster CA +
	// node cert rotation and the mTLS config used to wrap those routes.
	mtlsCfg, err := buildMTLS(ctx, cfg, log)
	if err != nil {
		return fmt.Errorf("initialising mTLS: %w", err)
	}

	pool := tcp.NewPool(tcp.PoolConfig{
		MaxIdle:     100,
		MaxOpen:     1000,
		IdleTimeout: 90 * time.Second,
		DialTimeout: 10 * time.Second,
	})
	mgr.OnShutdown("tcp-pool", func(context.Context) error { return pool.Close() })

	// The data plane is held behind a swappable holder so config reload can
	// rebuild all listeners against the new route set.
	dp := &dataPlane{
		ctx: ctx, pool: pool, tls: tlsMgr, mtls: mtlsCfg, policy: policyEngine,
		edge: edge, metrics: metrics, tracer: tracer,
		pluginRT: pluginRuntime, pluginReg: pluginRegistry,
		tenantReg: tenantRegistry, tenantEnf: tenantEnforcer, log: log,
	}
	if err := dp.rebuild(cfgMgr.Holder()); err != nil {
		display.StepFail("Routes", err.Error())
		return fmt.Errorf("initialising proxy manager: %w", err)
	}
	if names := routeNames(cfg); len(names) > 0 {
		display.Step("Routes", strings.Join(names, ", "))
	} else {
		display.Step("Routes", "none configured")
	}
	mgr.OnShutdown("proxy", func(c context.Context) error { return dp.stop(c) })

	// On config reload, rebuild the data plane from the new config. This hook
	// runs after the config-swap hook registered by RegisterReload above.
	mgr.OnReload("proxy", func(context.Context) error {
		if rerr := dp.rebuild(cfgMgr.Holder()); rerr != nil {
			log.Error("proxy rebuild after reload failed, keeping previous routes", "err", rerr)
		}
		return nil
	})

	// Surface live route stats on /health from whichever manager is current.
	apiSrv.SetRouteStats(func() []api.RouteHealth {
		stats := dp.stats()
		out := make([]api.RouteHealth, len(stats))
		for i, s := range stats {
			out[i] = api.RouteHealth{Name: s.Name, Protocol: s.Protocol, Listen: s.Listen, Active: s.Active}
		}
		return out
	})

	// Dashboard data providers: extended status, secret set/unset state (never
	// values), and installed plugins.
	wireDashboardProviders(apiSrv, cfgMgr, auditLog, pluginRegistry, policyEngine)
	wireNamespaceHooks(apiSrv, tenantRegistry, tenantEnforcer)

	// --- messaging (M11): AI gateway + notification router + approval -------
	msg := buildMessaging(log)
	if msg.gateway != nil {
		display.Step("AI gateway", "provider="+strings.Join(msg.gateway.ProviderNames(), ","))
	} else {
		display.Step("AI gateway", "not configured — run: vortex setup")
	}
	if msg.telegram != nil {
		display.Step("Messaging", "telegram")
	} else {
		display.Step("Messaging", "disabled")
	}

	// Burst-protection bans (M19) alert through the notification router
	// (Telegram et al) when messaging is configured.
	if msg.router != nil {
		router := msg.router
		apiSrv.SetBurstNotifier(func(title, body string) {
			_ = router.Send(context.Background(), messaging.SeverityWarn, title, body)
		})
		// POST /api/notify (the TUI code view's [T] Telegram forward).
		apiSrv.SetNotifier(func(title, body string) error {
			return router.Send(context.Background(), messaging.SeverityInfo, title, body)
		})
	}

	// Secret expiry / rotation startup check (M19): warn + alert for any
	// local-store secret that is expired or due for rotation.
	go checkSecretRotation(cfg, msg.router, log)

	// AI cost endpoint: report today's spend/budget from the gateway.
	if msg.gateway != nil {
		gw := msg.gateway
		apiSrv.SetAICostProvider(func() api.AICostInfo {
			c := gw.CostToday()
			return api.AICostInfo{
				Provider: c.Provider, TotalUSD: c.TotalUSD, RequestsToday: c.RequestsToday,
				DailyBudget: c.DailyBudget, RemainingBudget: c.RemainingBudget, Free: c.Free,
			}
		})
		// OpenAI-compatible /v1/* surface (upgrade 3): any OpenAI-speaking tool
		// can use VORTEX as its AI backend with provider routing + cost tracking.
		apiSrv.SetOpenAIGateway(gw.ModelIDs, gw.CompleteForModel)
		log.Info("OpenAI-compatible server enabled", "endpoint", "/v1/chat/completions", "models", gw.ModelIDs())

		// Autonomous API key rotation: when key slots are configured, route
		// through health-scored slots with failover + context preservation.
		wireKeyRotation(ctx, apiSrv, gw, msg.router, display, log)
	}

	// --- agent runtime (M10/M11) --------------------------------------------
	// Use the real AI gateway when configured; otherwise the stub. Wire the
	// human-in-the-loop approval function when an approver is configured.
	var gateway agents.AIGateway = agents.StubAIGateway{}
	if msg.gateway != nil {
		gateway = msg.gateway
	}
	// --- VORTEX Forge (M13): autonomous app builder ------------------------
	// Built before the agent runtime so its BUILD_APP handler can be wired into
	// the coordinator. forge imports agents, so the coordinator reaches forge
	// only through a callback (no import cycle).
	forgeJobs := buildForge(gateway, msg, log)
	if forgeJobs != nil {
		apiSrv.SetForgeRuntime(&forgeRuntimeAdapter{jm: forgeJobs})
		display.Step("Forge", "ready")
	}

	var clarifying, pending func(string) bool
	if forgeJobs != nil {
		clarifying = forgeJobs.SessionClarifying
		pending = forgeJobs.SessionPending
	}
	// --- research agent (M15) -----------------------------------------------
	researchFn := buildResearch(gateway, msg, resolveWorkingDir(), apiSrv, log)
	// --- devops agent (M16) -------------------------------------------------
	devopsFn := buildDevOps(ctx, cfg, gateway, msg, apiSrv, log)
	// --- data pipeline agent (M17) ------------------------------------------
	pipelineFn := buildPipeline(gateway, msg, resolveWorkingDir(), apiSrv, log)
	// --- multi-agent orchestration (M18) ------------------------------------
	orchestrateFn := buildOrchestration(gateway, msg, apiSrv, log, researchFn, devopsFn, pipelineFn)
	// Durable workflow store (upgrade 4 — crash recovery): opened here so the
	// startup resume below can notify via the messaging router.
	var workflowStore *agents.WorkflowStore
	if cacheDir, cerr := os.UserCacheDir(); cerr == nil {
		wfDB := filepath.Join(cacheDir, "vortex", "memory", "workflows.db")
		if ws, werr := agents.NewWorkflowStore(wfDB); werr != nil {
			log.Warn("workflow store unavailable, crash recovery disabled", "err", werr)
		} else {
			workflowStore = ws
		}
	}
	agentRuntime := buildAgentRuntime(ctx, log, apiSrv.Addr(), auditLog, gateway, msg.approvalFn, forgeBuildApp(forgeJobs), resolveWorkingDir(), clarifying, pending, researchFn, devopsFn, pipelineFn, orchestrateFn, workflowStore)
	if agentRuntime != nil {
		adapter := &agentRuntimeAdapter{rt: agentRuntime}
		apiSrv.SetAgentRuntime(adapter)
		mgr.OnShutdown("agents", func(c context.Context) error { return agentRuntime.Stop(c) })
		display.Step("Agent runtime", "8 slots ready")
		resumeInterruptedWorkflows(ctx, workflowStore, agentRuntime, msg.router, log)

		// Aggregate agent-plane health into /ready (production audit I3): report
		// not-ready when the submit queue is deeply backed up, so an orchestration
		// stall or executor hang fails readiness instead of silently degrading.
		apiSrv.SetReadinessFunc(func() error {
			if d := adapter.Stats().QueueDepth; d > readinessQueueDepthLimit {
				return fmt.Errorf("agent queue saturated (%d pending)", d)
			}
			return nil
		})

		// Register messaging webhooks (each with its own per-IP rate limit) now
		// that the runtime exists to receive their messages.
		registerMessagingWebhooks(apiSrv, msg, agentRuntime, log)

		// Specialist agent team (agent teams): coder → tester → reviewer over
		// A2A. Enabled by --team or VORTEX_TEAM_MODE=true.
		if teamMode || envTrue("VORTEX_TEAM_MODE") {
			wireAgentTeam(ctx, apiSrv, agentRuntime, gateway, msg.router, msg.telegram, resolveWorkingDir(), display, log)
		}
	}

	// --- VORTEX Studio (M12): browser IDE/terminal/db/git -------------------
	if h := buildStudio(ctx, cfg, auditLog, mgr, log); h != nil {
		apiSrv.SetStudioHandler(h)
	}

	// ACME HTTP-01 challenge handler must be reachable on :80 for cert issuance.
	if h := tlsChallengeHandler(tlsMgr, cfg); h != nil {
		startChallengeServer(mgr, h, log)
	}

	// --- self-healing (M14): health monitor + recovery + SLO tracking -------
	buildHealing(ctx, cfg, apiSrv, mgr, msg, auditLog, metrics, log)
	display.Step("Self-healing", "enabled")

	log.Info("VORTEX started",
		"version", version,
		"cluster", cfg.Cluster.Name,
		"api_addr", apiSrv.Addr(),
		"routes", len(cfg.Routes),
	)
	provider := ""
	if msg.gateway != nil {
		if names := msg.gateway.ProviderNames(); len(names) > 0 {
			provider = names[0]
		}
	}
	display.Ready(startup.ReadyConfig{
		Version:    version,
		Cluster:    cfg.Cluster.Name,
		APIAddr:    apiSrv.Addr(),
		Routes:     routeNames(cfg),
		AIProvider: provider,
		Telegram:   msg.telegram != nil,
	})

	mgr.Run(ctx)
	log.Info("VORTEX stopped cleanly")
	return nil
}

// dataPlane holds the currently-running proxy.Manager and rebuilds it on config
// reload. It is safe for concurrent stats reads and reload-driven rebuilds.
type dataPlane struct {
	ctx       context.Context
	pool      *tcp.Pool
	tls       *vtls.Manager
	mtls      *vtls.MTLSConfig
	policy    *policy.Engine
	edge      *security.Edge
	metrics   *observability.Metrics
	tracer    trace.Tracer
	pluginRT  *plugins.Runtime
	pluginReg *plugins.Registry
	tenantReg *tenancy.Registry
	tenantEnf *tenancy.Enforcer
	log       *slog.Logger

	mu      sync.Mutex
	current *proxy.Manager
	cancel  context.CancelFunc
}

// rebuild constructs a proxy.Manager from the current config and starts it,
// stopping any previously-running manager first. On a build error the previous
// manager is left running and the error is returned.
//
// The previous manager is stopped BEFORE the new one starts: routes bind fixed
// ports, so an overlapping start would race the old listener and fail with
// "address already in use", leaving the route with no listener. Stopping first
// frees the ports for a clean rebind. This trades a brief (sub-second) bind gap
// for correctness; in-flight requests on the old listener still drain via the
// listener's own graceful shutdown.
func (d *dataPlane) rebuild(holder *config.Holder) error {
	cfg := holder.Get()
	mgr, err := proxy.NewManager(proxy.ManagerConfig{
		Config: cfg, TLS: d.tls, TCPPool: d.pool, MTLSConfig: d.mtls,
		PolicyEngine: d.policy, Edge: d.edge,
		Metrics: d.metrics, Tracer: d.tracer,
		Runtime: d.pluginRT, PluginRegistry: d.pluginReg,
		Registry: d.tenantReg, Enforcer: d.tenantEnf, Logger: d.log,
	})
	if err != nil {
		return err
	}

	// Swap in the new manager and capture the previous one to stop.
	d.mu.Lock()
	prev, prevCancel := d.current, d.cancel
	runCtx, cancel := context.WithCancel(d.ctx)
	d.current, d.cancel = mgr, cancel
	d.mu.Unlock()

	// Stop the previous manager and wait for its listeners to release their
	// ports before starting the new one on the same ports.
	if prev != nil {
		prevCancel()
		_ = prev.Stop(context.Background())
	}

	go func() {
		if perr := mgr.Start(runCtx); perr != nil {
			d.log.Error("proxy manager stopped with error", "err", perr)
		}
	}()
	return nil
}

// stop cancels and stops the current manager.
func (d *dataPlane) stop(ctx context.Context) error {
	d.mu.Lock()
	mgr, cancel := d.current, d.cancel
	d.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if mgr != nil {
		return mgr.Stop(ctx)
	}
	return nil
}

// stats returns the current manager's route stats.
func (d *dataPlane) stats() []proxy.RouteStats {
	d.mu.Lock()
	mgr := d.current
	d.mu.Unlock()
	if mgr == nil {
		return nil
	}
	return mgr.Stats()
}

// buildTLSManager creates a vtls.Manager when any route needs TLS (https/h3),
// or returns nil when none do.
func buildTLSManager(cfg *config.Config, log *slog.Logger) (*vtls.Manager, error) {
	if !needsTLS(cfg) {
		return nil, nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	storePath := filepath.Join(cacheDir, "vortex", "certs")
	storeKey, err := deriveKey("tls-store")
	if err != nil {
		return nil, err
	}

	log.Info("initialising TLS manager", "provider", cfg.TLS.Provider, "store", storePath)
	return vtls.NewManager(vtls.ManagerConfig{
		Provider:   cfg.TLS.Provider,
		StorePath:  storePath,
		StoreKey:   storeKey,
		MinVersion: cfg.TLS.MinVersion,
		ACME: vtls.ACMEConfig{
			Email:   cfg.TLS.ACMEEmail,
			Staging: false,
		},
	})
}

// needsTLS reports whether any route uses a TLS-bearing protocol.
func needsTLS(cfg *config.Config) bool {
	for _, r := range cfg.Routes {
		if r.Protocol == "https" || r.Protocol == "h3" {
			return true
		}
	}
	return false
}

// buildMTLS sets up the cluster CA + node cert rotation and the mTLS config when
// any route has mtls:true, starting the rotation loop. Returns nil when no route
// uses mTLS.
func buildMTLS(ctx context.Context, cfg *config.Config, log *slog.Logger) (*vtls.MTLSConfig, error) {
	if !needsMTLS(cfg) {
		return nil, nil
	}
	// The mTLS store path defaults to the user cache dir but can be overridden
	// via VORTEX_MTLS_STORE so peers (and tests) can share the same cluster CA.
	storePath := os.Getenv("VORTEX_MTLS_STORE")
	if storePath == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		storePath = filepath.Join(cacheDir, "vortex", "mtls")
	}
	mtlsKey, err := deriveKey("mtls-store")
	if err != nil {
		return nil, err
	}
	store, err := vtls.NewStore(storePath, mtlsKey)
	if err != nil {
		return nil, fmt.Errorf("creating mTLS store: %w", err)
	}

	rm, err := vtls.NewRotationManager(vtls.RotationConfig{
		ClusterName: cfg.Cluster.Name,
		Store:       store,
		Logger:      log,
	})
	if err != nil {
		return nil, fmt.Errorf("creating rotation manager: %w", err)
	}
	rm.StartRotation(ctx)

	mc, err := vtls.NewMTLSConfig(vtls.MTLSConfig{
		RotationMgr: rm,
		TrustDomain: rm.Identity().TrustDomain,
		Logger:      log,
	})
	if err != nil {
		return nil, err
	}
	log.Info("mTLS identity mesh enabled",
		"node_id", rm.Identity().NodeID,
		"trust_domain", rm.Identity().TrustDomain,
		"store", storePath,
	)
	return mc, nil
}

// openAPIKeyStore opens the API-key store, loading any persisted keys. The path
// honours VORTEX_APIKEY_STORE (used by tests/operators) and otherwise defaults
// to <user-cache>/vortex/apikeys.json. A load failure is logged but not fatal —
// the server starts with an empty store rather than refusing to boot.
func openAPIKeyStore(log *slog.Logger) (*auth.APIKeyStore, string) {
	path := os.Getenv("VORTEX_APIKEY_STORE")
	if path == "" {
		cacheDir, err := os.UserCacheDir()
		if err != nil {
			cacheDir = os.TempDir()
		}
		path = filepath.Join(cacheDir, "vortex", "apikeys.json")
	}
	// Ensure the parent directory exists so Save on shutdown succeeds.
	if derr := os.MkdirAll(filepath.Dir(path), 0o700); derr != nil {
		log.Warn("creating API key store dir failed", "path", filepath.Dir(path), "err", derr)
	}
	store := auth.NewAPIKeyStore()
	if err := store.Load(path); err != nil {
		log.Warn("loading API key store failed, starting empty", "path", path, "err", err)
	}
	return store, path
}

// tuiKeyPath returns where the plaintext TUI/dashboard key lives:
// <user-config>/vortex/tui-key. This matches tui.APIKeyFilePath() but is
// computed locally so the server path does not import the TUI package.
func tuiKeyPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "vortex", "tui-key")
}

// ensureAPIKey guarantees the store holds at least one usable key so the local
// TUI/dashboard can authenticate. It first imports the setup-written tui-key
// (re-admitting it when the persisted hash store is empty), and if the store is
// still empty, auto-creates an admin key and writes its raw secret to tui-key.
func ensureAPIKey(keyStore *auth.APIKeyStore, log *slog.Logger) {
	keyPath := tuiKeyPath()

	// Step 1: import an existing tui-key (from `vortex setup`) so a raw key that
	// outlived an empty/rotated hash store still authenticates.
	if data, err := os.ReadFile(keyPath); err == nil {
		if raw := strings.TrimSpace(string(data)); raw != "" {
			if _, verr := keyStore.Verify(raw); verr != nil {
				// Not currently valid (store empty or out of sync) — re-admit it.
				if ierr := keyStore.ImportRaw(raw, "admin", "default", []auth.Role{auth.RoleAdmin}, "tui-setup-key"); ierr != nil {
					log.Warn("importing tui-key failed", "err", ierr)
				} else {
					log.Info("loaded TUI key from setup config")
				}
			}
		}
	}

	// Step 2: if still empty, auto-create an admin key and save it for the TUI.
	if keyStore.Count() > 0 {
		return
	}
	_, raw, err := keyStore.Issue("admin", "default", []auth.Role{auth.RoleAdmin}, "auto-created", 0)
	if err != nil {
		log.Warn("auto-creating admin API key failed", "err", err)
		return
	}
	if derr := os.MkdirAll(filepath.Dir(keyPath), 0o700); derr != nil {
		log.Warn("creating tui-key dir failed", "err", derr)
	}
	if werr := os.WriteFile(keyPath, []byte(raw), 0o600); werr != nil {
		log.Warn("saving auto-created key to tui-key failed", "err", werr)
	}
	masked := raw
	if len(masked) > 8 {
		masked = masked[:8] + "****"
	}
	log.Info("auto-created admin API key — saved to tui-key", "key", masked)
}

// openRuntimeAuditLog opens the audit log used by the running server. The path
// (VORTEX_AUDIT_LOG or <cache>/vortex/audit.log) and the master-key-derived
// HMAC key match the `vortex audit` CLI so the same chain is verifiable. A
// failure to open is fatal — an unwritable audit log is a security
// regression, not something to silently skip.
func openRuntimeAuditLog(log *slog.Logger) (*audit.Log, error) {
	path := auditLogPath()
	if derr := os.MkdirAll(filepath.Dir(path), 0o700); derr != nil {
		log.Warn("creating audit log dir failed", "path", filepath.Dir(path), "err", derr)
	}
	auditKey, err := deriveKey("audit")
	if err != nil {
		return nil, err
	}
	al, err := audit.NewLog(path, auditKey)
	if err != nil {
		return nil, err
	}
	log.Info("audit log enabled", "path", path)
	return al, nil
}

// buildCluster starts the gossip+raft cluster manager when the deployment is
// multi-node (cfg.Cluster.Nodes has more than one entry) or VORTEX_BOOTSTRAP is
// set. Single-node deployments skip all clustering overhead and just log the
// mode. The returned manager is nil in single-node mode.
func buildCluster(ctx context.Context, cfg *config.Config, log *slog.Logger) (*cluster.Manager, error) {
	bootstrap := os.Getenv("VORTEX_BOOTSTRAP") == "true"
	if len(cfg.Cluster.Nodes) <= 1 && !bootstrap {
		log.Info("running in single-node mode")
		return nil, nil
	}

	bindAddr := os.Getenv("VORTEX_CLUSTER_BIND")
	if bindAddr == "" {
		bindAddr = "127.0.0.1"
	}
	node, err := cluster.NewNodeConfig(cfg.Cluster.Name, bindAddr, cfg.Cluster.GossipPort)
	if err != nil {
		return nil, err
	}

	mgr, err := cluster.NewManager(cluster.Config{
		Node:       node,
		RaftPort:   cfg.Cluster.RaftPort,
		GossipPort: cfg.Cluster.GossipPort,
		Bootstrap:  bootstrap,
		Peers:      peersExcludingSelf(cfg.Cluster.Nodes, bindAddr),
		Logger:     log,
	})
	if err != nil {
		return nil, err
	}
	if err := mgr.Start(ctx); err != nil {
		_ = mgr.Shutdown()
		return nil, err
	}
	log.Info("cluster started",
		"node_id", node.NodeID,
		"peers", len(cfg.Cluster.Nodes),
		"leader", mgr.IsLeader(),
	)
	return mgr, nil
}

// peersExcludingSelf returns the configured node addresses minus this node's
// bind address (a node should not try to gossip-join itself).
func peersExcludingSelf(nodes []string, self string) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if n != self {
			out = append(out, n)
		}
	}
	return out
}

// secretsStatusTTL bounds how long the secret set/unset snapshot is cached
// before the backend is re-queried.
const secretsStatusTTL = 10 * time.Second

// newSecretsStatusProvider returns a provider for GET /api/secrets/status that
// reuses one secret adapter and caches the set/unset snapshot for
// secretsStatusTTL, instead of rebuilding the adapter and re-querying the
// backend on every request (production audit L4). The adapter is rebuilt only
// when the backend kind changes across a config reload.
func newSecretsStatusProvider(cfgMgr *config.Manager) func() []api.SecretStatus {
	var (
		mu          sync.Mutex
		adapter     secrets.Adapter
		adapterKind string
		cached      []api.SecretStatus
		cachedAt    time.Time
	)
	return func() []api.SecretStatus {
		cfg := cfgMgr.Current()
		ac, err := buildAdapterConfig(cfg)
		if err != nil {
			return nil
		}

		mu.Lock()
		defer mu.Unlock()

		// Rebuild the adapter only when missing or the backend kind changed.
		if adapter == nil || adapterKind != ac.Kind {
			a, aerr := secrets.NewAdapter(ac)
			if aerr != nil {
				return nil
			}
			adapter = a
			adapterKind = ac.Kind
			cached = nil // invalidate snapshot for the new backend
		}

		if cached != nil && time.Since(cachedAt) < secretsStatusTTL {
			return cached
		}

		out := make([]api.SecretStatus, 0, len(cfg.Secrets.Keys))
		for _, key := range cfg.Secrets.Keys {
			_, gerr := adapter.Get(context.Background(), key)
			out = append(out, api.SecretStatus{Name: key, Set: gerr == nil})
		}
		cached = out
		cachedAt = time.Now()
		return out
	}
}

// wireDashboardProviders attaches the dashboard data providers to the API
// server: extended status, declared-secret set/unset state (never values), and
// installed plugins. Each provider reads live state when called.
func wireDashboardProviders(
	apiSrv *api.Server,
	cfgMgr *config.Manager,
	auditLog *audit.Log,
	pluginRegistry *plugins.Registry,
	policyEngine *policy.Engine,
) {
	apiSrv.SetStatusProvider(func() api.StatusInfo {
		cfg := cfgMgr.Current()
		info := api.StatusInfo{
			ClusterName:   cfg.Cluster.Name,
			TLSProvider:   cfg.TLS.Provider,
			SecretBackend: secretBackendKind(cfg),
			PolicyDefault: policyEngine != nil && policyEngine.UsingDefault(),
			WorkingDir:    resolveWorkingDir(),
			Routes:        len(cfg.Routes),
		}
		for _, r := range cfg.Routes {
			info.RouteNames = append(info.RouteNames, r.Name)
		}
		if id, err := vtls.NewNodeIdentity(cfg.Cluster.Name); err == nil {
			info.NodeID = id.NodeID
			info.TrustDomain = id.TrustDomain
		}
		if pluginRegistry != nil {
			info.PluginCount = len(pluginRegistry.List())
		}
		if auditLog != nil {
			if entries, err := auditLog.Query(audit.QueryFilter{}); err == nil {
				info.AuditEntryCount = len(entries)
			}
		}
		return info
	})

	// The secret adapter (and its backend clients) is built once and reused
	// rather than reconstructed per request, with a short cache of the
	// set/unset states; the cache is keyed by the backend kind so a config
	// reload that switches backends rebuilds it (production audit L4).
	apiSrv.SetSecretsProvider(newSecretsStatusProvider(cfgMgr))

	apiSrv.SetPluginsProvider(func() []api.PluginInfo {
		if pluginRegistry == nil {
			return nil
		}
		manifests := pluginRegistry.List()
		out := make([]api.PluginInfo, 0, len(manifests))
		for _, m := range manifests {
			hooks := make([]string, 0, len(m.HookTypes))
			for _, h := range m.HookTypes {
				hooks = append(hooks, string(h))
			}
			out = append(out, api.PluginInfo{
				Name: m.Name, Version: m.Version,
				Description: m.Description, HookTypes: hooks,
			})
		}
		return out
	})
}

// wireNamespaceHooks attaches the namespace management endpoints to the API
// server, backed by the tenant registry and enforcer.
func wireNamespaceHooks(apiSrv *api.Server, reg *tenancy.Registry, enf *tenancy.Enforcer) {
	apiSrv.SetNamespaceHooks(
		func() []api.NamespaceInfo {
			out := []api.NamespaceInfo{}
			for _, ns := range reg.List("") {
				out = append(out, nsToAPI(ns.Config()))
			}
			return out
		},
		func(ni api.NamespaceInfo) error {
			_, err := reg.Create(apiToNS(ni))
			if err != nil {
				return err
			}
			return reg.Save(namespaceStorePath())
		},
		func(id string) error {
			if err := reg.Delete(id); err != nil {
				return err
			}
			return reg.Save(namespaceStorePath())
		},
		func(id string) (api.NamespaceStats, bool) {
			if _, err := reg.Get(id); err != nil {
				return api.NamespaceStats{}, false
			}
			s := enf.Stats(id)
			return api.NamespaceStats{
				ActiveConns: s.ActiveConns, BandwidthUsed: s.BandwidthUsed, RouteCount: s.RouteCount,
			}, true
		},
	)
}

// nsToAPI converts a tenancy config to the API shape.
func nsToAPI(c tenancy.NamespaceConfig) api.NamespaceInfo {
	var ni api.NamespaceInfo
	ni.ID, ni.Name, ni.OrgID = c.ID, c.Name, c.OrgID
	ni.Quotas.MaxRoutes = c.Quotas.MaxRoutes
	ni.Quotas.MaxSecrets = c.Quotas.MaxSecrets
	ni.Quotas.MaxConnections = c.Quotas.MaxConnections
	ni.Quotas.BandwidthMbps = c.Quotas.BandwidthMbps
	return ni
}

// apiToNS converts the API shape to a tenancy config.
func apiToNS(ni api.NamespaceInfo) tenancy.NamespaceConfig {
	return tenancy.NamespaceConfig{
		ID: ni.ID, Name: ni.Name, OrgID: ni.OrgID,
		Quotas: tenancy.QuotaConfig{
			MaxRoutes: ni.Quotas.MaxRoutes, MaxSecrets: ni.Quotas.MaxSecrets,
			MaxConnections: ni.Quotas.MaxConnections, BandwidthMbps: ni.Quotas.BandwidthMbps,
		},
	}
}

// secretBackendKind returns the configured secret backend, defaulting to local.
func secretBackendKind(cfg *config.Config) string {
	if cfg.Secrets.Store == "" {
		return "local"
	}
	return cfg.Secrets.Store
}

// applyTuning logs the recommended OS tuning at startup, and applies it when
// VORTEX_APPLY_TUNING=true (Linux + root only; otherwise settings are skipped).
func applyTuning(log *slog.Logger) {
	apply := os.Getenv("VORTEX_APPLY_TUNING") == "true"
	res := perf.Apply(!apply)
	for _, s := range res.Skipped {
		log.Debug("os tuning skipped", "setting", s)
	}
	if apply {
		log.Info("os tuning applied", "applied", len(res.Applied), "skipped", len(res.Skipped))
		for _, e := range res.Errors {
			log.Warn("os tuning error", "detail", e)
		}
	}
}

// startAutoscaler creates and starts the horizontal autoscaler when
// VORTEX_AUTOSCALE_PROVIDER is set; otherwise it logs that autoscaling is off.
func startAutoscaler(ctx context.Context, log *slog.Logger) {
	provider := os.Getenv("VORTEX_AUTOSCALE_PROVIDER")
	if provider == "" {
		log.Info("autoscaler disabled")
		return
	}
	cfg := perf.AutoscaleConfig{
		Provider:   provider,
		APIKey:     os.Getenv("VORTEX_AUTOSCALE_API_KEY"),
		WebhookURL: os.Getenv("VORTEX_AUTOSCALE_WEBHOOK"),
		MinNodes:   atoiDefault(os.Getenv("VORTEX_AUTOSCALE_MIN_NODES"), 1),
		MaxNodes:   atoiDefault(os.Getenv("VORTEX_AUTOSCALE_MAX_NODES"), 10),
	}
	as, err := perf.NewAutoscaler(cfg)
	if err != nil {
		log.Warn("autoscaler configuration invalid, disabling", "err", err)
		return
	}
	log.Info("autoscaler enabled", "provider", cfg.Provider, "min", cfg.MinNodes, "max", cfg.MaxNodes)
	// CPU and node-count providers are placeholders until cluster metrics are
	// wired; the loop runs but evaluates conservatively (0% CPU, 1 node).
	go func() {
		_ = as.Start(ctx, func() float64 { return 0 }, func() int { return 1 })
	}()
}

// atoiDefault parses s as an int, returning def on empty or parse failure.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// resumeInterruptedWorkflows finds workflows left incomplete by a crash and
// resubmits their goals to the agent runtime in the background (upgrade 4),
// notifying via the messaging router when one is configured. Each interrupted
// workflow is marked terminal first so the resubmission (which records a fresh
// workflow) cannot be re-resumed forever on subsequent restarts.
func resumeInterruptedWorkflows(ctx context.Context, store *agents.WorkflowStore, rt *agents.Runtime, router *messaging.Router, log *slog.Logger) {
	if store == nil || rt == nil {
		return
	}
	incomplete, err := store.ListIncomplete()
	if err != nil {
		log.Warn("workflow recovery check failed", "err", err)
		return
	}
	log.Info("workflow recovery checked", "interrupted", len(incomplete))
	if len(incomplete) == 0 {
		return
	}
	log.Info("resuming interrupted workflows", "count", len(incomplete))
	for _, wf := range incomplete {
		wf := wf
		_ = store.Fail(wf.ID, "interrupted by shutdown; resubmitted on startup")
		if router != nil {
			_ = router.Send(ctx, messaging.SeverityInfo, "VORTEX",
				fmt.Sprintf("🔄 Resuming interrupted task:\n%s (step %d of %d)",
					wf.Goal, wf.CurrentStep+1, len(wf.Steps)+1))
		}
		go func() {
			defer func() {
				if r := recover(); r != nil {
					log.Error("workflow resume panic recovered", "panic", r)
				}
			}()
			ch, serr := rt.Submit(ctx, "/orchestrate "+wf.Goal, wf.SessionID)
			if serr != nil {
				log.Warn("workflow resume submit failed", "goal", wf.Goal, "err", serr)
				return
			}
			<-ch // drain the (buffered) response so the runtime can finish it
		}()
	}
}

// buildAgentRuntime constructs and starts the agent runtime: a message bus, a
// sandboxed tool registry wired to the audit log, a coordinator (using the
// given AI gateway and approval function), and the supervising runtime. It
// returns nil if construction fails (the server still runs without agents).
func buildAgentRuntime(ctx context.Context, log *slog.Logger, apiAddr string, auditLog *audit.Log, gateway agents.AIGateway, approval agents.ApprovalFunc, buildApp agents.BuildAppFunc, workingDir string, sessionClarifying, sessionPending func(string) bool, research agents.ResearchFunc, devops agents.DevOpsFunc, pipeline agents.PipelineFunc, orchestrate agents.OrchestrateFunc, workflows *agents.WorkflowStore) *agents.Runtime {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	sandboxBase := filepath.Join(cacheDir, "vortex", "agents")

	bus := agents.NewBus()
	registry := agents.NewToolRegistry()
	apiBaseURL := "http://" + apiAddr
	if err := agents.RegisterBuiltins(registry, sandboxBase, agents.DefaultAllowedCommands, bus, apiBaseURL, "coordinator"); err != nil {
		log.Warn("agent runtime disabled: tool registration failed", "err", err)
		return nil
	}
	sandboxed := agents.NewSandboxedRegistry(registry, sandboxBase, agents.DefaultAllowedCommands, bus)
	if auditLog != nil {
		sandboxed = sandboxed.WithAudit(auditLog, "coordinator")
	}

	// Local filesystem + terminal tools (real machine access, approval-gated),
	// confined to the working directory so writes/cwd stay inside the user's
	// project tree. Registered in their own registry to avoid read_file/
	// write_file name collisions with the sandbox builtins.
	localRegistry := agents.NewToolRegistry()
	if err := agents.RegisterLocalTools(localRegistry, agents.LocalFSConfig{Root: workingDir}); err != nil {
		log.Warn("local agent tools disabled: registration failed", "err", err)
		localRegistry = nil
	}

	coord, err := agents.NewCoordinator(agents.CoordinatorConfig{
		Bus:               bus,
		Tools:             sandboxed,
		LocalTools:        localRegistry,
		AIGateway:         gateway,
		MaxAgents:         8,
		Approval:          approval,
		BuildApp:          buildApp,
		SessionClarifying: sessionClarifying,
		SessionPending:    sessionPending,
		Research:          research,
		DevOps:            devops,
		Pipeline:          pipeline,
		Orchestrate:       orchestrate,
		MemoryStore:       filepath.Join(cacheDir, "vortex", "memory"),
		WorkingDir:        workingDir,
	})
	if err != nil {
		log.Warn("agent runtime disabled: coordinator init failed", "err", err)
		return nil
	}

	// SQLite conversation store (M20): supersedes the JSON memory directory for
	// persistence, listing, history, and full-text search. On first run, legacy
	// per-session JSON files are migrated into the database (idempotent).
	memoryDir := filepath.Join(cacheDir, "vortex", "memory")
	if store, serr := agents.NewMemoryStore(filepath.Join(memoryDir, "conversations.db")); serr != nil {
		log.Warn("SQLite conversation store unavailable, using JSON memory", "err", serr)
	} else {
		if n, merr := store.MigrateJSONDir(memoryDir); merr != nil {
			log.Warn("conversation JSON→SQLite migration failed", "err", merr)
		} else if n > 0 {
			log.Info("migrated sessions from JSON to SQLite", "count", n)
		}
		coord.SetMemoryStore(store)
		log.Info("SQLite conversation store enabled", "db", filepath.Join(memoryDir, "conversations.db"))
	}

	// Learned-skill store (upgrade 1 — self-improving agent): proven procedures
	// are recalled into prompts and new ones distilled from completed tasks.
	skillsDB := filepath.Join(cacheDir, "vortex", "memory", "skills.db")
	if skills, serr := agents.NewSkillStore(skillsDB); serr != nil {
		log.Warn("skill store unavailable, agent will not learn skills", "err", serr)
	} else {
		coord.SetSkillStore(skills)
		log.Info("skills store loaded", "db", skillsDB, "skills", skills.Stats().Total)
	}

	// Episodic memory (upgrade 2 — tier-2 cross-session memory): durable facts
	// recalled into prompts; new facts mined from each exchange in background.
	episodesDB := filepath.Join(cacheDir, "vortex", "memory", "episodes.db")
	if episodes, eerr := agents.NewEpisodicStore(episodesDB); eerr != nil {
		log.Warn("episodic memory unavailable", "err", eerr)
	} else {
		coord.SetEpisodicStore(episodes)
		log.Info("episodic memory loaded", "db", episodesDB)
	}

	// Durable workflows (upgrade 4): orchestration progress persists step by
	// step so interrupted goals are resumed on the next startup.
	if workflows != nil {
		coord.SetWorkflowStore(workflows)
	}
	rt, err := agents.NewRuntime(agents.RuntimeConfig{
		Bus: bus, Coordinator: coord, MaxAgents: 8,
		SandboxBase: sandboxBase, Logger: log,
	})
	if err != nil {
		log.Warn("agent runtime disabled: runtime init failed", "err", err)
		return nil
	}
	if err := rt.Start(ctx); err != nil {
		log.Warn("agent runtime disabled: start failed", "err", err)
		return nil
	}
	return rt
}

// logBufferAdapter bridges *api.LogBuffer to logger.BufferSink so the logger
// can tee records into the ring buffer without pkg/logger importing api.
type logBufferAdapter struct{ buf *api.LogBuffer }

func (a *logBufferAdapter) Record(t, level, msg string, fields map[string]string) {
	a.buf.Write(api.LogEntry{Time: t, Level: level, Msg: msg, Fields: fields})
}

// agentRuntimeAdapter adapts *agents.Runtime to the api.AgentRuntime interface,
// translating agents.RuntimeStats into api.AgentRuntimeStats so the api package
// stays decoupled from the agents package.
type agentRuntimeAdapter struct{ rt *agents.Runtime }

func (a *agentRuntimeAdapter) Submit(ctx context.Context, userMsg, sessionID string) (<-chan string, error) {
	ch, err := a.rt.Submit(ctx, userMsg, sessionID)
	if errors.Is(err, agents.ErrTooManyRequests) {
		// Translate to the api-layer sentinel so the handler maps it to 503
		// without the api package importing the agents package.
		return nil, api.ErrAgentBusy
	}
	return ch, err
}

func (a *agentRuntimeAdapter) Stats() api.AgentRuntimeStats {
	s := a.rt.Stats()
	return api.AgentRuntimeStats{
		ActiveAgents:  s.ActiveAgents,
		TotalMessages: s.TotalMessages,
		QueueDepth:    s.QueueDepth,
		Skills:        s.Skills,
		Episodes:      s.Episodes,
		Sessions:      s.Sessions,
	}
}

func (a *agentRuntimeAdapter) Approve(sessionID string, approved bool) (string, bool) {
	return a.rt.Approve(sessionID, approved)
}

func (a *agentRuntimeAdapter) ListSessions() []api.SessionSummary {
	src := a.rt.ListSessions()
	out := make([]api.SessionSummary, 0, len(src))
	for _, s := range src {
		out = append(out, api.SessionSummary{SessionID: s.SessionID, Summary: s.Summary, UpdatedAt: s.UpdatedAt})
	}
	return out
}

func (a *agentRuntimeAdapter) SessionHistory(sessionID string) []api.SessionMessage {
	src := a.rt.SessionHistory(sessionID)
	out := make([]api.SessionMessage, 0, len(src))
	for _, m := range src {
		out = append(out, api.SessionMessage{Role: m.Role, Content: m.Content, Timestamp: m.Timestamp})
	}
	return out
}

// buildForge constructs the Forge orchestrator + job manager. The AI gateway is
// required for real code generation; without it (stub gateway) Forge still
// builds but the codegen agent has no provider, so it is only enabled when a
// real gateway is configured. Delivery routes through the notification router
// when messaging is configured.
func buildForge(gateway agents.AIGateway, msg messagingComponents, log *slog.Logger) *forge.JobManager {
	if _, isStub := gateway.(agents.StubAIGateway); isStub || gateway == nil {
		log.Info("forge disabled (no AI gateway configured)")
		return nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	sandboxBase := filepath.Join(cacheDir, "vortex", "agents", "forge")

	delivery := forge.DeliveryConfig{ServeBase: filepath.Join(cacheDir, "vortex", "forge-serve")}
	if msg.router != nil {
		delivery.Sender = &forgeNotifyAdapter{router: msg.router}
	}

	f, err := forge.NewForge(forge.ForgeConfig{
		AIGateway:   gateway,
		SandboxBase: sandboxBase,
		Delivery:    delivery,
	})
	if err != nil {
		log.Warn("forge disabled: init failed", "err", err)
		return nil
	}
	log.Info("forge enabled", "sandbox_base", sandboxBase)
	return forge.NewJobManager(f)
}

// forgeBuildApp returns an agents.BuildAppFunc that submits a BUILD_APP request
// to the forge job manager, or nil when forge is not configured (the
// coordinator then falls back to its stub).
func forgeBuildApp(jm *forge.JobManager) agents.BuildAppFunc {
	if jm == nil {
		return nil
	}
	return func(ctx context.Context, userMsg, sessionID string) (string, error) {
		id := jm.Submit(ctx, userMsg, sessionID, 0)
		return "🛠 Build started. Job ID: " + id, nil
	}
}

// forgeRuntimeAdapter adapts *forge.JobManager to api.ForgeRuntime.
type forgeRuntimeAdapter struct{ jm *forge.JobManager }

func (a *forgeRuntimeAdapter) Submit(ctx context.Context, message, sessionID string, chatID int64) string {
	return a.jm.Submit(ctx, message, sessionID, chatID)
}

func (a *forgeRuntimeAdapter) Get(id string) (api.ForgeJob, bool) {
	j, ok := a.jm.Get(id)
	if !ok {
		return api.ForgeJob{}, false
	}
	return api.ForgeJob{
		ID: j.ID, Message: j.Message, State: string(j.State), Progress: j.Progress,
		ProgressHistory: j.ProgressHistory, Questions: forgeQuestions(j.Questions),
		Result: j.Result, DurationMs: j.DurationMs, Error: j.Error,
	}, true
}

// forgeQuestions converts forge clarifying questions into the API type.
func forgeQuestions(qs []forge.ClarifyingQuestion) []api.ForgeQuestion {
	if len(qs) == 0 {
		return nil
	}
	out := make([]api.ForgeQuestion, 0, len(qs))
	for _, q := range qs {
		out = append(out, api.ForgeQuestion{Question: q.Question, Options: q.Options, Key: q.Key})
	}
	return out
}

func (a *forgeRuntimeAdapter) List() []api.ForgeJob {
	jobs := a.jm.List()
	out := make([]api.ForgeJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, api.ForgeJob{ID: j.ID, Message: j.Message, State: string(j.State), Progress: j.Progress, Error: j.Error})
	}
	return out
}

// forgeNotifyAdapter adapts messaging.Router to forge.NotificationSender.
type forgeNotifyAdapter struct{ router *messaging.Router }

func (a *forgeNotifyAdapter) SendMessage(ctx context.Context, _ int64, text string) error {
	return a.router.Send(ctx, messaging.SeverityInfo, "VORTEX Forge", text)
}

func (a *forgeNotifyAdapter) SendFile(ctx context.Context, _ int64, filename string, data []byte, caption string) error {
	return a.router.SendFile(ctx, filename, data, caption)
}

// researchNotifyAdapter bridges *messaging.Router to research.Notifier.
type researchNotifyAdapter struct{ router *messaging.Router }

func (a *researchNotifyAdapter) Notify(ctx context.Context, title, body string) error {
	return a.router.Send(ctx, messaging.SeverityInfo, title, body)
}

func (a *researchNotifyAdapter) NotifyFile(ctx context.Context, filename string, data []byte, caption string) error {
	return a.router.SendFile(ctx, filename, data, caption)
}

// buildResearch constructs the M15 research pipeline and returns a ResearchFunc
// for the coordinator, plus registers the report API endpoints. Returns nil
// when no AI gateway is configured (summarization needs it).
func buildResearch(gateway agents.AIGateway, msg messagingComponents, workingDir string, apiSrv *api.Server, log *slog.Logger) agents.ResearchFunc {
	if gateway == nil {
		return nil
	}
	reporter := research.NewReporter(workingDir)
	var notifier research.Notifier
	if msg.router != nil {
		notifier = &researchNotifyAdapter{router: msg.router}
	}
	agent := research.NewResearchAgent(
		research.NewSearcher(), research.NewFetcher(),
		research.NewSummarizer(gateway), reporter, notifier)

	// Report API: list + fetch saved reports.
	apiSrv.SetResearchProvider(func() []api.ResearchReport {
		reports, _ := reporter.List()
		out := make([]api.ResearchReport, 0, len(reports))
		for _, r := range reports {
			out = append(out, api.ResearchReport{Title: r.Title, FilePath: r.FilePath, SavedAt: r.SavedAt})
		}
		return out
	}, func(name string) (string, bool) {
		rep, err := reporter.Get(name)
		if err != nil || rep.Summary == nil {
			return "", false
		}
		return rep.Summary.Text, true
	})

	log.Info("research agent enabled")
	return func(ctx context.Context, query string, progressFn func(string)) (string, error) {
		report, err := agent.Research(ctx, query, 1, progressFn)
		if err != nil {
			return "", err
		}
		reply := "📊 Research complete: " + query
		if report.Summary != nil {
			for _, p := range report.Summary.Points {
				reply += "\n• " + p
			}
		}
		if report.FilePath != "" {
			reply += "\nReport saved to: " + report.FilePath
		}
		return reply, nil
	}
}

// orchestrateNotifyAdapter bridges *messaging.Router to orchestration.Notifier.
type orchestrateNotifyAdapter struct{ router *messaging.Router }

func (a *orchestrateNotifyAdapter) Notify(ctx context.Context, title, body string) error {
	return a.router.Send(ctx, messaging.SeverityInfo, title, body)
}

// buildOrchestration constructs the M18 multi-agent orchestration agent: a
// planner-driven runner whose AgentRouter dispatches each task to the right
// specialized agent handler (research/devops/data_pipeline) or the gateway
// (general). Registers the API and returns an OrchestrateFunc. Returns nil when
// no AI gateway is configured (planning needs it).
func buildOrchestration(gateway agents.AIGateway, msg messagingComponents, apiSrv *api.Server, log *slog.Logger,
	research agents.ResearchFunc, devops agents.DevOpsFunc, pipeline agents.PipelineFunc) agents.OrchestrateFunc {
	if gateway == nil {
		apiSrv.SetOrchestrateProvider(nil)
		return nil
	}
	// Router: dispatch a task to its agent type. Each handler is the same *Func
	// the coordinator uses; "general" falls back to a direct gateway completion.
	router := orchestration.AgentRouterFunc(func(ctx context.Context, agentType, input string) (string, error) {
		switch agentType {
		case "research":
			if research != nil {
				return research(ctx, input, nil)
			}
		case "devops":
			if devops != nil {
				return devops(ctx, input, nil)
			}
		case "data_pipeline":
			if pipeline != nil {
				return pipeline(ctx, input, nil)
			}
		}
		// general / build_app / unwired → a direct model completion.
		return gateway.Complete(ctx, input, "You are a VORTEX agent. Complete the task concisely.")
	})

	var notifier orchestration.Notifier
	if msg.router != nil {
		notifier = &orchestrateNotifyAdapter{router: msg.router}
	}
	agent := orchestration.NewOrchestrationAgent(gateway, router, notifier)

	apiSrv.SetOrchestrateProvider(func(ctx context.Context, goal string) (string, error) {
		return agent.Run(ctx, goal, nil)
	})

	log.Info("multi-agent orchestration enabled")
	return func(ctx context.Context, goal string, progressFn func(string)) (string, error) {
		return agent.Run(ctx, goal, progressFn)
	}
}

// pipelineNotifyAdapter bridges *messaging.Router to pipeline.Notifier.
type pipelineNotifyAdapter struct{ router *messaging.Router }

func (a *pipelineNotifyAdapter) Notify(ctx context.Context, title, body string) error {
	return a.router.Send(ctx, messaging.SeverityInfo, title, body)
}

func (a *pipelineNotifyAdapter) NotifyFile(ctx context.Context, filename string, data []byte, caption string) error {
	return a.router.SendFile(ctx, filename, data, caption)
}

// buildPipeline constructs the M17 data pipeline agent, registers the analyze
// API, and returns a PipelineFunc for the coordinator. Returns nil when no AI
// gateway is configured (planning needs it, though analysis degrades).
func buildPipeline(gateway agents.AIGateway, msg messagingComponents, workingDir string, apiSrv *api.Server, log *slog.Logger) agents.PipelineFunc {
	if gateway == nil {
		apiSrv.SetPipelineProvider(nil)
		return nil
	}
	var notifier pipeline.Notifier
	if msg.router != nil {
		notifier = &pipelineNotifyAdapter{router: msg.router}
	}
	agent := pipeline.NewDataPipelineAgent(gateway, notifier, workingDir)

	apiSrv.SetPipelineProvider(func(ctx context.Context, source, data, request string) (api.PipelineResultInfo, error) {
		res, err := agent.Analyze(ctx, source, []byte(data), request, nil)
		if err != nil {
			return api.PipelineResultInfo{}, err
		}
		return api.PipelineResultInfo{
			Summary: res.Summary, DataPath: res.DataPath, ChartPath: res.ChartPath,
			Rows: res.Rows, Columns: res.Columns,
		}, nil
	})

	log.Info("data pipeline agent enabled")
	return func(ctx context.Context, m string, progressFn func(string)) (string, error) {
		// Chat requests carry the analysis instruction; the data source must be a
		// URL embedded in the message (inline data comes via the API).
		res, err := agent.Analyze(ctx, extractURL(m), nil, m, progressFn)
		if err != nil {
			return "", err
		}
		reply := "📊 " + res.Summary
		if res.ChartPath != "" {
			reply += "\nChart: " + res.ChartPath
		}
		if res.DataPath != "" {
			reply += "\nData: " + res.DataPath
		}
		return reply, nil
	}
}

// extractURL returns the first http(s) URL in s, or "".
func extractURL(s string) string {
	for _, tok := range strings.Fields(s) {
		if strings.HasPrefix(tok, "http://") || strings.HasPrefix(tok, "https://") {
			return tok
		}
	}
	return ""
}

// devopsNotifyAdapter bridges *messaging.Router to devops.Notifier.
type devopsNotifyAdapter struct{ router *messaging.Router }

func (a *devopsNotifyAdapter) Notify(ctx context.Context, title, body string) error {
	return a.router.Send(ctx, messaging.SeverityInfo, title, body)
}

// buildDevOps constructs the M16 DevOps agent from configured servers, connects
// to each, registers the API, and returns a DevOpsFunc for the coordinator.
// Returns nil when no servers are configured.
func buildDevOps(ctx context.Context, cfg *config.Config, gateway agents.AIGateway, msg messagingComponents, apiSrv *api.Server, log *slog.Logger) agents.DevOpsFunc {
	if len(cfg.Servers) == 0 {
		// No servers configured — still register the (empty) servers endpoint.
		apiSrv.SetDevOpsProvider(func() []api.DevOpsServer { return nil }, nil)
		return nil
	}
	var notifier devops.Notifier
	if msg.router != nil {
		notifier = &devopsNotifyAdapter{router: msg.router}
	}
	// Approval for DevOps mutating ops routes through the messaging approval gate
	// when configured; otherwise denied (fail-safe).
	approver := func(string) bool { return false }
	if msg.approvalFn != nil {
		approver = func(action string) bool {
			return msg.approvalFn(ctx, agents.ApprovalRequest{Tool: "devops", Description: action})
		}
	}

	agent := devops.NewDevOpsAgent(gateway, notifier, approver)
	connected := 0
	for _, srv := range cfg.Servers {
		if err := agent.Connect(ctx, srv.Host, srv.User, srv.KeyPath); err != nil {
			log.Warn("devops: failed to connect to server", "name", srv.Name, "host", srv.Host, "err", err)
			continue
		}
		connected++
	}

	apiSrv.SetDevOpsProvider(func() []api.DevOpsServer {
		out := make([]api.DevOpsServer, 0)
		for _, h := range agent.Servers() {
			out = append(out, api.DevOpsServer{Name: h})
		}
		return out
	}, func(ctx context.Context, _, command string) (string, error) {
		return agent.Handle(ctx, command, nil)
	})

	log.Info("devops agent enabled", "servers", connected)
	return func(ctx context.Context, m string, progressFn func(string)) (string, error) {
		return agent.Handle(ctx, m, progressFn)
	}
}

// healingNotifyAdapter bridges *messaging.Router to healing.Notifier (string
// severity → messaging.Severity).
type healingNotifyAdapter struct{ router *messaging.Router }

func (a *healingNotifyAdapter) Send(ctx context.Context, severity, title, body string) error {
	sev := messaging.SeverityInfo
	switch severity {
	case "critical":
		sev = messaging.SeverityCritical
	case "warn":
		sev = messaging.SeverityWarn
	}
	return a.router.Send(ctx, sev, title, body)
}

// buildHealing wires the M14 self-healing subsystem: a health monitor with a
// check per route, a recovery manager with default rules, an SLO tracker, and
// the event loop that routes health events to recovery. It registers shutdown
// and exposes status via the API healing provider.
func buildHealing(ctx context.Context, cfg *config.Config, apiSrv *api.Server,
	mgr *lifecycle.Manager, msg messagingComponents, auditLog *audit.Log,
	_ *observability.Metrics, log *slog.Logger) {

	// One check per route: HTTP routes probe the listen addr as an endpoint,
	// others TCP-dial it. Listen 0 means an ephemeral port — skip (untestable).
	var checks []healing.HealthCheck
	for _, r := range cfg.Routes {
		if r.Listen <= 0 {
			continue
		}
		addr := fmt.Sprintf("127.0.0.1:%d", r.Listen)
		checks = append(checks, healing.HealthCheck{
			Name: r.Name, Kind: healing.KindRoute, Target: addr,
			Interval: 30 * time.Second, Timeout: 5 * time.Second, Threshold: 3,
		})
	}

	monitor := healing.NewMonitor(checks)

	// Default recovery rules: every route failure → notify (alert only). A
	// config-check failure would reload; a sustained route failure restarts.
	var rules []healing.RecoveryRule
	for _, r := range cfg.Routes {
		if r.Listen <= 0 {
			continue
		}
		rules = append(rules, healing.RecoveryRule{
			CheckName: r.Name, Action: healing.ActionNotify, NotifyOnly: true,
		})
	}

	var notifier healing.Notifier
	if msg.router != nil {
		notifier = &healingNotifyAdapter{router: msg.router}
	}
	var auditAdapter healing.AuditLogger
	if auditLog != nil {
		auditAdapter = auditLog // *audit.Log satisfies AuditLogger
	}

	recovery := healing.NewRecoveryManager(rules, notifier, auditAdapter, healing.RecoveryDeps{
		ReloadConfig: func(context.Context) error { mgr.Reload(); return nil },
	})

	// SLO tracker: 99% target per route. Compliance reads are best-effort (the
	// metrics layer is write-only today) — returns no-data until a read API
	// lands, so it registers routes without false alerts.
	slo := healing.NewSLOTracker(func(string) (float64, bool) { return 0, false }, notifier)
	sloRoutes := 0
	for _, r := range cfg.Routes {
		if r.Listen <= 0 {
			continue
		}
		slo.AddSLO(r.Name, 0.99)
		sloRoutes++
	}

	monitor.Start(ctx)
	slo.Start(ctx)

	// Event loop: route health events to the recovery manager.
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case ev := <-monitor.Events():
				_ = recovery.Handle(ctx, ev)
			}
		}
	}()

	apiSrv.SetHealingProvider(func() api.HealingStatus {
		return healingStatus(monitor, recovery, slo)
	})

	mgr.OnShutdown("healing", func(context.Context) error { return nil })

	log.Info("self-healing enabled", "checks", len(checks), "rules", len(rules), "slo_routes", sloRoutes)
}

// healingStatus snapshots the healing subsystem for the API.
func healingStatus(monitor *healing.Monitor, recovery *healing.RecoveryManager, slo *healing.SLOTracker) api.HealingStatus {
	status := monitor.Status()
	checks := make([]api.HealingCheck, 0, len(status))
	for _, r := range status {
		checks = append(checks, api.HealingCheck{
			Name: r.Name, Healthy: r.Healthy, LatencyMs: r.LatencyMs,
			LastCheck: r.Timestamp, ConsecutiveFailures: r.Attempts,
		})
	}
	alerts := make([]api.HealingSLOAlert, 0)
	for _, a := range slo.Status() {
		alerts = append(alerts, api.HealingSLOAlert{
			RouteName: a.RouteName, Target: a.Target, Current: a.Current,
			BurnRate: a.BurnRate, AlertLevel: a.AlertLevel,
		})
	}
	st := recovery.Stats()
	return api.HealingStatus{
		Healthy: monitor.Healthy(), Checks: checks, SLOAlerts: alerts,
		RecoveryStats: api.HealingRecoveryStats{
			TotalEvents: st.TotalEvents, ActionsExecuted: st.ActionsExecuted,
		},
	}
}

// messagingComponents holds the messaging subsystem built from environment
// configuration. Any field may be nil when not configured.
type messagingComponents struct {
	gateway    *messaging.AIGateway
	router     *messaging.Router
	telegram   *messaging.TelegramBot
	whatsapp   *messaging.WhatsAppBot
	slack      *messaging.SlackBot
	approval   *messaging.ApprovalManager
	approvalFn agents.ApprovalFunc
	tgChat     int64 // resolved Telegram default chat (env or setup config)
}

// providerFromFile builds an AIProvider from the saved `vortex setup` config
// (decrypting its API key), or nil when no usable config exists. Env vars take
// precedence; this is only consulted when none are set.
func providerFromFile(log *slog.Logger) *messaging.AIProvider {
	cfg, ok := loadProviderConfig()
	if !ok || cfg.Provider == "" || cfg.Provider == "none" {
		return nil
	}
	// Bedrock stores no key in the setup file — its credentials come from the
	// AWS environment variables at runtime (production: never persist AWS
	// secrets). Assemble the access:secret key here.
	if cfg.Provider == messaging.ProviderBedrock {
		ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY")
		if ak == "" || sk == "" {
			log.Warn("bedrock configured but AWS credentials are not set; skipping", "provider", cfg.Provider)
			return nil
		}
		models := []string(nil)
		if cfg.Model != "" {
			models = []string{cfg.Model}
		}
		log.Info("AI provider loaded from setup config", "provider", cfg.Provider)
		return &messaging.AIProvider{Name: cfg.Provider, APIKey: ak + ":" + sk, Endpoint: cfg.Endpoint, Models: models}
	}

	key, err := decryptKey(cfg.APIKeyEnc)
	if err != nil {
		log.Warn("could not decrypt saved AI key, ignoring setup config", "err", err)
		return nil
	}
	if cfg.Provider != messaging.ProviderOllama && key == "" {
		return nil
	}
	models := []string(nil)
	if cfg.Model != "" {
		models = []string{cfg.Model}
	}
	log.Info("AI provider loaded from setup config", "provider", cfg.Provider)
	return &messaging.AIProvider{
		Name:     cfg.Provider,
		APIKey:   key,
		Endpoint: cfg.Endpoint,
		Models:   models,
	}
}

// backupProvidersFromFile builds the fallback providers recorded by the setup
// wizard's backup-key step, in slot order, with priorities after the primary.
func backupProvidersFromFile(log *slog.Logger) []messaging.AIProvider {
	cfg, ok := loadProviderConfig()
	if !ok {
		return nil
	}
	var out []messaging.AIProvider
	for i, b := range cfg.Backups {
		key, err := decryptKey(b.APIKeyEnc)
		if err != nil {
			log.Warn("could not decrypt backup AI key, skipping slot", "slot", i+2, "err", err)
			continue
		}
		if b.Provider != messaging.ProviderOllama && key == "" {
			continue
		}
		models := []string(nil)
		if b.Model != "" {
			models = []string{b.Model}
		}
		out = append(out, messaging.AIProvider{
			Name: b.Provider, APIKey: key, Endpoint: b.Endpoint,
			Models: models, Priority: i + 1,
		})
		log.Info("backup AI provider loaded from setup config", "provider", b.Provider, "slot", i+2)
	}
	return out
}

// buildMessaging constructs the AI gateway, notification router, and platform
// bots from environment variables. All credentials come from the environment,
// never from the config file. Returns a struct whose fields are nil when the
// corresponding integration is not configured.
func buildMessaging(log *slog.Logger) messagingComponents {
	var m messagingComponents

	// --- AI gateway: assemble providers from env keys -----------------------
	var providers []messaging.AIProvider
	if k := os.Getenv("VORTEX_ANTHROPIC_KEY"); k != "" {
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderClaude, APIKey: k,
			Models: []string{"claude-opus-4-8"}, Priority: 0,
		})
	}
	if k := os.Getenv("VORTEX_OPENAI_KEY"); k != "" {
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderOpenAI, APIKey: k,
			Models: []string{"gpt-4o"}, Priority: 1,
		})
	}
	if u := os.Getenv("VORTEX_OLLAMA_URL"); u != "" {
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderOllama, Endpoint: u,
			Models: []string{"llama3"}, Priority: 2,
		})
	}
	if k := os.Getenv("VORTEX_DEEPSEEK_KEY"); k != "" {
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderDeepSeek, APIKey: k,
			Models: []string{"deepseek-chat"}, Priority: 2,
		})
	}
	if k := os.Getenv("VORTEX_GEMINI_KEY"); k != "" {
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderGemini, APIKey: k,
			Models: []string{"gemini-1.5-flash"}, Priority: 3,
		})
	}
	// --- M20 providers ------------------------------------------------------
	if k := os.Getenv("VORTEX_GROQ_KEY"); k != "" {
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderGroq, APIKey: k,
			Models: []string{"llama-3.1-70b-versatile"}, Priority: 2, // fast + cheap
		})
	}
	if region := os.Getenv("VORTEX_BEDROCK_REGION"); region != "" {
		if ak, sk := os.Getenv("AWS_ACCESS_KEY_ID"), os.Getenv("AWS_SECRET_ACCESS_KEY"); ak != "" && sk != "" {
			providers = append(providers, messaging.AIProvider{
				Name:     messaging.ProviderBedrock,
				APIKey:   ak + ":" + sk,
				Endpoint: region, // Bedrock carries the region in Endpoint
				Models:   []string{"anthropic.claude-3-5-sonnet-20240620-v1:0"},
				Priority: 1,
			})
		}
	}
	if k := os.Getenv("VORTEX_AZURE_OPENAI_KEY"); k != "" {
		ep := os.Getenv("VORTEX_AZURE_OPENAI_ENDPOINT")
		dep := os.Getenv("VORTEX_AZURE_OPENAI_DEPLOYMENT")
		if ep != "" && dep != "" {
			providers = append(providers, messaging.AIProvider{
				Name: messaging.ProviderAzureOpenAI, APIKey: k,
				Endpoint: ep, Models: []string{dep}, Priority: 1,
			})
		}
	}
	if k := os.Getenv("VORTEX_OPENROUTER_KEY"); k != "" {
		model := os.Getenv("VORTEX_OPENROUTER_MODEL")
		if model == "" {
			model = "openai/gpt-4o"
		}
		providers = append(providers, messaging.AIProvider{
			Name: messaging.ProviderOpenRouter, APIKey: k,
			Models: []string{model}, Priority: 4,
		})
	}
	// Fall back to the saved `vortex setup` config only when NO provider env var
	// was set — env vars always override the file.
	if len(providers) == 0 {
		if p := providerFromFile(log); p != nil {
			providers = append(providers, *p)
			// Backup key slots from setup (part 3): lower-priority fallbacks the
			// gateway tries in order when the primary fails or is rate-limited.
			providers = append(providers, backupProvidersFromFile(log)...)
		}
	}
	if len(providers) > 0 {
		gw, err := messaging.NewAIGateway(messaging.AIGatewayConfig{Providers: providers})
		if err != nil {
			log.Warn("AI gateway disabled", "err", err)
		} else {
			m.gateway = gw
			log.Info("AI gateway configured", "providers", gw.ProviderNames())
		}
	}

	// --- platform bots ------------------------------------------------------
	// Telegram token+chat: env var wins; otherwise fall back to the setup config
	// (ai-provider.json telegram_token/telegram_chat_id).
	tgToken := os.Getenv("VORTEX_TELEGRAM_TOKEN")
	tgChat := atoi64Default(os.Getenv("VORTEX_TELEGRAM_DEFAULT_CHAT"), 0)
	if tgToken == "" {
		if fileTok, fileChat, ok := telegramFromFile(); ok {
			tgToken = fileTok
			if tgChat == 0 {
				tgChat = fileChat
			}
			log.Info("Telegram loaded from setup config")
		}
	}
	if tgToken != "" {
		allowed := parseInt64List(os.Getenv("VORTEX_TELEGRAM_ALLOWED_IDS"))
		if len(allowed) == 0 && tgChat != 0 {
			allowed = []int64{tgChat} // the configured chat is implicitly allowed
		}
		bot, err := messaging.NewTelegramBot(messaging.TelegramConfig{
			Token:       tgToken,
			AllowedIDs:  allowed,
			SecretToken: os.Getenv("VORTEX_TELEGRAM_SECRET"),
		})
		if err != nil {
			log.Warn("telegram disabled", "err", err)
		} else {
			m.telegram = bot
		}
	}
	if pid, tok := os.Getenv("VORTEX_WA_PHONE_ID"), os.Getenv("VORTEX_WA_TOKEN"); pid != "" && tok != "" {
		bot, err := messaging.NewWhatsAppBot(messaging.WhatsAppConfig{
			PhoneNumberID: pid, AccessToken: tok,
			VerifyToken: os.Getenv("VORTEX_WA_VERIFY_TOKEN"),
			AppSecret:   os.Getenv("VORTEX_WA_APP_SECRET"),
		})
		if err != nil {
			log.Warn("whatsapp disabled", "err", err)
		} else {
			m.whatsapp = bot
		}
	}
	if wh := os.Getenv("VORTEX_SLACK_WEBHOOK"); wh != "" {
		bot, _ := messaging.NewSlackBot(messaging.SlackConfig{
			WebhookURL:    wh,
			SigningSecret: os.Getenv("VORTEX_SLACK_SIGNING_SECRET"),
		})
		m.slack = bot
	}

	// --- notification router ------------------------------------------------
	m.router = messaging.NewRouter(messaging.NotificationConfig{
		Telegram:      m.telegram,
		WhatsApp:      m.whatsapp,
		Slack:         m.slack,
		DefaultChatID: tgChat,
		DefaultPhone:  os.Getenv("VORTEX_WA_DEFAULT_PHONE"),
	})
	m.tgChat = tgChat
	if chans := m.router.ConfiguredChannels(); len(chans) > 0 {
		log.Info("messaging configured", "channels", chans)
	} else {
		log.Info("messaging disabled")
	}

	// --- human-in-the-loop approval (M10.7) ---------------------------------
	// Approval routes to Telegram when a default chat is set; the resolver
	// consumes approve/reject button callbacks before they reach the runtime.
	if m.telegram != nil {
		chat := tgChat
		if chat != 0 {
			m.approval = messaging.NewApprovalManager(m.telegram, chat, 0)
			m.telegram.SetCallbackResolver(m.approval)
			m.approvalFn = m.approval.ApprovalFunc()
			log.Info("agent approval gate enabled", "channel", "telegram")
		}
	}

	return m
}

// registerMessagingWebhooks mounts the configured platform webhooks with their
// own per-IP rate limits (Telegram 30/min, WhatsApp 30/min, Slack 60/min).
func registerMessagingWebhooks(apiSrv *api.Server, m messagingComponents, rt *agents.Runtime, log *slog.Logger) {
	var specs []api.WebhookSpec
	if m.telegram != nil {
		specs = append(specs, api.WebhookSpec{
			Path: "/webhook/telegram", Handler: m.telegram.HandleWebhook(rt), RPM: 30,
		})
	}
	if m.whatsapp != nil {
		specs = append(specs, api.WebhookSpec{
			Path: "/webhook/whatsapp", Handler: m.whatsapp.HandleWebhook(rt), RPM: 30,
		})
	}
	if m.slack != nil {
		specs = append(specs, api.WebhookSpec{
			Path: "/webhook/slack", Handler: m.slack.HandleSlashCommand(rt), RPM: 60,
		})
	}
	if len(specs) > 0 {
		apiSrv.SetWebhooks(specs)
		log.Info("messaging webhooks registered", "count", len(specs))
	}

	// Telegram: wire built-in command hooks, register the command menu, and pick
	// a delivery mode (polling for local testing, webhook for production). The
	// network calls (setMyCommands, setWebhook) run in the BACKGROUND so a slow
	// or unreachable Telegram API never delays startup or shutdown.
	if m.telegram != nil {
		wireTelegramCommands(m.telegram, apiSrv, rt)
		bot := m.telegram
		go func() {
			// 5s cap: a revoked/unreachable token must not keep this goroutine
			// (or its API connection) alive any longer than necessary.
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if serr := bot.SetCommands(ctx); serr != nil {
				log.Warn("telegram setMyCommands failed", "err", serr)
			}
		}()
		startTelegramDelivery(m.telegram, rt, log)
	}
}

// wireTelegramCommands wires the built-in /status, /routes, /cost, /ls,
// /approve, /reject command data sources.
func wireTelegramCommands(bot *messaging.TelegramBot, apiSrv *api.Server, rt *agents.Runtime) {
	bot.SetCommandHooks(messaging.CommandHooks{
		Status: func() string {
			cfg := apiSrv.StatusSnapshot()
			return fmt.Sprintf("VORTEX %s ✅\nCluster: %s\nRoutes: %d active",
				version, cfg.ClusterName, cfg.Routes)
		},
		Routes: func() string {
			cfg := apiSrv.StatusSnapshot()
			if len(cfg.RouteNames) == 0 {
				return "No routes configured."
			}
			return "Routes:\n• " + strings.Join(cfg.RouteNames, "\n• ")
		},
		Cost: func() string {
			info := apiSrv.AICostSnapshot()
			if info.Free {
				return "AI cost today: free (local model)"
			}
			return fmt.Sprintf("AI cost today: $%.2f (%d requests)", info.TotalUSD, info.RequestsToday)
		},
		List: func(path string) string {
			res, err := rt.Coordinator().ExecuteLocalToolSync("list_directory", map[string]any{"path": path})
			if err != nil {
				return "⚠ " + err.Error()
			}
			return res
		},
		Approve: func(sessionID string, ok bool) (string, bool) {
			return rt.Approve(sessionID, ok)
		},
		ClarifySubmit: func(sessionID, answer string) {
			// Submit the combined clarifying answer to the runtime (same session
			// → continues the build). Drain the response channel in the background.
			if ch, err := rt.Submit(context.Background(), answer, sessionID); err == nil {
				go func() {
					for range ch { //nolint:revive // draining
					}
				}()
			}
		},
	})
}

// startTelegramDelivery starts polling mode (VORTEX_TELEGRAM_POLLING=true) or
// registers a webhook (VORTEX_PUBLIC_URL); otherwise logs how to enable one.
func startTelegramDelivery(bot *messaging.TelegramBot, rt *agents.Runtime, log *slog.Logger) {
	if os.Getenv("VORTEX_TELEGRAM_POLLING") == "true" {
		go bot.Poll(context.Background(), rt, 2*time.Second)
		log.Info("Telegram polling mode enabled")
		return
	}
	if pub := os.Getenv("VORTEX_PUBLIC_URL"); pub != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			if err := bot.SetWebhook(ctx, strings.TrimRight(pub, "/")+"/webhook/telegram"); err != nil {
				log.Warn("Telegram webhook registration failed", "err", err)
			} else {
				log.Info("Telegram webhook registered", "url", pub)
			}
		}()
		return
	}
	log.Info("Telegram webhook not registered — set VORTEX_PUBLIC_URL or VORTEX_TELEGRAM_POLLING=true")
}

// buildStudio constructs the VORTEX Studio handler tree (code-server proxy,
// terminal, DB studio, git panel) when VORTEX_STUDIO_WORKSPACE is set. code-
// server degrades gracefully when its binary is absent. Returns nil when Studio
// is not configured.
func buildStudio(ctx context.Context, cfg *config.Config, auditLog *audit.Log, mgr *lifecycle.Manager, log *slog.Logger) http.Handler {
	workspace := os.Getenv("VORTEX_STUDIO_WORKSPACE")
	if workspace == "" {
		log.Info("studio disabled")
		return nil
	}

	mux := http.NewServeMux()

	// code-server (optional — graceful degradation when not installed).
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	cs, cerr := studio.NewCodeServer(studio.CodeServerConfig{
		WorkspaceDir: workspace,
		DataDir:      filepath.Join(cacheDir, "vortex", "code-server"),
		Logger:       log,
	})
	codeServerOK := false
	if cerr != nil {
		if errors.Is(cerr, studio.ErrCodeServerNotInstalled) {
			log.Info("studio: code-server not installed, IDE panel disabled")
		} else {
			log.Warn("studio: code-server unavailable", "err", cerr)
		}
	} else if serr := cs.Start(ctx); serr != nil {
		log.Warn("studio: code-server failed to start", "err", serr)
	} else {
		codeServerOK = true
		mux.Handle("/studio/", cs.ProxyHandler())
		mgr.OnShutdown("code-server", func(context.Context) error { return cs.Stop() })
	}

	// Terminal.
	term := studio.NewTerminalManager(studio.TerminalConfig{
		WorkDir:  workspace,
		AuditLog: auditLog,
		Logger:   log,
	})
	mux.Handle("/studio/terminal", term.Handler())
	mgr.OnShutdown("studio-terminal", func(context.Context) error { return term.CloseSessions() })

	// DB studio from mTLS-enabled TCP routes.
	var dbRoutes []studio.DBRoute
	for _, rc := range cfg.Routes {
		if rc.Protocol == "tcp" && rc.MTLS {
			dbRoutes = append(dbRoutes, studio.DBRoute{
				Name:       rc.Name,
				Kind:       studio.KindForPort(rc.Listen),
				ListenAddr: fmt.Sprintf("127.0.0.1:%d", rc.Listen),
			})
		}
	}
	db, _ := studio.NewDBStudio(studio.DBStudioConfig{
		Routes:   dbRoutes,
		ReadOnly: os.Getenv("VORTEX_STUDIO_DB_READONLY") != "false",
		AuditLog: auditLog,
		Logger:   log,
	})
	mux.Handle("/studio/db/", db.Handler())

	// Git panel (when the workspace is a git repo).
	if gp, gerr := studio.NewGitPanel(studio.GitPanelConfig{
		RepoPath: workspace, AuditLog: auditLog, Logger: log,
	}); gerr == nil {
		mux.Handle("/studio/git/", gp.Handler())
	} else {
		log.Info("studio: git panel disabled", "reason", gerr.Error())
	}

	log.Info("studio started",
		"workspace", workspace, "code_server", codeServerOK,
		"db_connections", len(dbRoutes))
	return mux
}

// parseInt64List parses a comma-separated list of int64s, skipping invalid
// entries. Used for VORTEX_TELEGRAM_ALLOWED_IDS.
func parseInt64List(s string) []int64 {
	if s == "" {
		return nil
	}
	var out []int64
	for _, part := range strings.Split(s, ",") {
		if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

// atoi64Default parses s as int64, returning def on empty/parse failure.
func atoi64Default(s string, def int64) int64 {
	if s == "" {
		return def
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return def
	}
	return n
}

// buildTenancy opens the namespace registry and builds the quota enforcer.
// Both are always created so routes can declare a namespace_id; a route without
// one incurs no tenancy cost. The registry path honours VORTEX_NAMESPACE_STORE
// (shared with the `vortex namespace` CLI).
func buildTenancy(log *slog.Logger) (*tenancy.Registry, *tenancy.Enforcer) {
	reg := tenancy.NewRegistry()
	path := namespaceStorePath()
	if err := reg.Load(path); err != nil {
		log.Warn("loading namespace registry failed, starting empty", "path", path, "err", err)
	}
	enforcer := tenancy.NewEnforcer(reg)
	log.Info("tenancy enabled", "namespace_count", len(reg.List("")))
	return reg, enforcer
}

// buildPlugins creates the sandboxed WASM runtime and opens the plugin
// registry. Both are always created so routes can declare plugins; a route with
// no plugins incurs no per-request plugin cost. The registry path honours
// VORTEX_PLUGIN_DIR (shared with the `vortex plugin` CLI).
func buildPlugins(ctx context.Context, log *slog.Logger) (*plugins.Runtime, *plugins.Registry, error) {
	rt, err := plugins.NewRuntime(ctx, plugins.RuntimeConfig{})
	if err != nil {
		return nil, nil, err
	}
	reg, err := plugins.NewRegistry(pluginStorePath())
	if err != nil {
		_ = rt.Close(ctx)
		return nil, nil, err
	}
	log.Info("plugin runtime ready", "store", pluginStorePath(), "installed", len(reg.List()))
	return rt, reg, nil
}

// buildSecurityEdge constructs the L7 edge (IP blocking + optional global rate
// limiting) from cfg.Security and starts its background maintenance goroutines.
// It returns nil when no edge protection is configured. block_clouds is a stub:
// it is logged but not yet enforced.
func buildSecurityEdge(ctx context.Context, cfg *config.Config, log *slog.Logger) (*security.Edge, error) {
	sec := cfg.Security
	hasAllowlist := len(sec.IPAllowlist) > 0
	if !hasAllowlist && !sec.BlockTor {
		// Nothing to enforce at the edge; per-route rate limits still apply via
		// the proxy manager independently of the global Edge.
		log.Info("security edge inactive", "reason", "no ip_allowlist or exit-node filtering configured")
		return nil, nil
	}

	bl, err := security.NewBlocklist(security.BlocklistConfig{
		IPAllowlist: sec.IPAllowlist,
		BlockTor:    sec.BlockTor,
	})
	if err != nil {
		return nil, err
	}
	if sec.BlockClouds {
		log.Warn("block_clouds is configured but not yet implemented (no-op)")
	}

	edge := security.NewEdge(security.EdgeConfig{Blocklist: bl})

	// Keep the Tor exit list fresh in the background.
	if sec.BlockTor {
		go bl.StartTorRefresh(ctx, "")
	}

	log.Info("security edge enabled",
		"block_tor", sec.BlockTor,
		"allowlist_size", len(sec.IPAllowlist),
		"auto_ban", false,
	)
	return edge, nil
}

// buildPolicyEngine constructs the OPA policy engine. Policy enforcement is
// opt-in: when VORTEX_POLICY_DIR is unset the engine compiles the built-in
// allow-all policy, so a fresh install proxies all requests. A directory with
// .rego files enables real enforcement.
func buildPolicyEngine(log *slog.Logger) (*policy.Engine, error) {
	policyDir := os.Getenv("VORTEX_POLICY_DIR")
	engine, err := policy.NewEngine(policy.EngineConfig{
		PolicyDir: policyDir,
		QueryPath: "data.vortex.allow",
	})
	if err != nil {
		return nil, err
	}
	log.Info("policy engine loaded", "policy_dir", policyDir, "default", engine.UsingDefault())
	return engine, nil
}

// loadSecrets validates the declared secret keys, opens the secret store, warns
// (non-fatally) about any declared secret that is not yet set, and — when
// inject_env is enabled — resolves the secrets that ARE set into an env map for
// injection into managed processes. Invalid key names or a store that cannot be
// opened are fatal; missing secret values are not (they may be set later).
func loadSecrets(ctx context.Context, cfg *config.Config, log *slog.Logger) (map[string]string, error) {
	if err := secrets.ValidateKeys(cfg.Secrets.Keys); err != nil {
		return nil, err
	}

	ac, err := buildAdapterConfig(cfg)
	if err != nil {
		return nil, err
	}
	adapter, err := secrets.NewAdapter(ac)
	if err != nil {
		return nil, fmt.Errorf("opening secret backend: %w", err)
	}

	// Verify connectivity to the backend. An unreachable external backend is a
	// warning, not a fatal error: secrets may be resolved later, and a transient
	// outage should not prevent the proxy from starting.
	if perr := adapter.Ping(ctx); perr != nil {
		log.Warn("secret backend unreachable at startup", "kind", ac.Kind, "err", perr)
	} else {
		log.Info("secret backend connected", "kind", ac.Kind)
	}

	// Warn about declared-but-unset secrets and collect the ones that are set.
	var present, missing []string
	for _, key := range cfg.Secrets.Keys {
		_, gerr := adapter.Get(ctx, key)
		switch {
		case gerr == nil:
			present = append(present, key)
		case errors.Is(gerr, os.ErrNotExist):
			missing = append(missing, key)
			log.Warn("declared secret not set", "name", key,
				"hint", "run: vortex secret set "+key+" <value>")
		default:
			return nil, fmt.Errorf("checking secret %s: %w", key, gerr)
		}
	}

	if !cfg.Secrets.InjectEnv {
		return nil, nil
	}

	env, err := secrets.ResolveAdapter(ctx, adapter, present)
	if err != nil {
		return nil, err
	}
	log.Info("secrets loaded", "count", len(present), "missing", len(missing))
	return env, nil
}

// checkSecretRotation scans the local secret store for expired or
// rotation-due secrets (M19), logging a WARN and alerting through the
// notification router (Telegram et al) for each. External backends are
// skipped — they manage their own TTLs. router may be nil (no messaging).
func checkSecretRotation(cfg *config.Config, router *messaging.Router, log *slog.Logger) {
	ac, err := buildAdapterConfig(cfg)
	if err != nil || ac.Local == nil {
		return
	}
	alerts, err := ac.Local.CheckRotation()
	if err != nil {
		log.Warn("secret rotation check failed", "err", err)
		return
	}
	for _, a := range alerts {
		hint := "vortex secret set " + a.Name + " <value>"
		if a.Expired {
			days := int(time.Since(a.Deadline).Hours() / 24)
			log.Warn("secret EXPIRED", "name", a.Name,
				"expired", a.Deadline.Format("2006-01-02"), "days_ago", days, "hint", hint)
			if router != nil {
				_ = router.Send(context.Background(), messaging.SeverityWarn,
					"⚠️ Secret EXPIRED: "+a.Name,
					fmt.Sprintf("Expired: %s (%d days ago)\nUpdate with: %s",
						a.Deadline.Format("2006-01-02"), days, hint))
			}
			continue
		}
		days := int(time.Until(a.Deadline).Hours() / 24)
		log.Warn("secret rotation due", "name", a.Name,
			"rotate_by", a.Deadline.Format("2006-01-02"), "days_left", days, "hint", hint)
		if router != nil {
			_ = router.Send(context.Background(), messaging.SeverityWarn,
				"⚠️ Secret rotation due: "+a.Name,
				fmt.Sprintf("Rotate by: %s (%d days)\nUpdate with: %s",
					a.Deadline.Format("2006-01-02"), days, hint))
		}
	}
}

// needsMTLS reports whether any route has mtls:true.
func needsMTLS(cfg *config.Config) bool {
	for _, r := range cfg.Routes {
		if r.MTLS {
			return true
		}
	}
	return false
}

// tlsChallengeHandler returns the ACME HTTP-01 challenge handler for ACME
// providers, or nil for the internal CA / no TLS.
func tlsChallengeHandler(tlsMgr *vtls.Manager, cfg *config.Config) http.Handler {
	if tlsMgr == nil {
		return nil
	}
	if cfg.TLS.Provider != "letsencrypt" && cfg.TLS.Provider != "zerossl" {
		return nil
	}
	return tlsMgr.ChallengeHandler()
}

// startChallengeServer serves the ACME HTTP-01 challenge handler on :80 and
// registers its shutdown with the lifecycle manager.
func startChallengeServer(mgr *lifecycle.Manager, h http.Handler, log *slog.Logger) {
	srv := &http.Server{Addr: ":80", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Warn("ACME challenge server stopped", "err", err)
		}
	}()
	mgr.OnShutdown("acme-challenge", func(c context.Context) error { return srv.Shutdown(c) })
}

// writePIDFile writes the current process ID to path as plain text. The richer
// stale-lock-aware logic lives in pkg/pidfile (used by stop/status/reload).
func writePIDFile(path string) error {
	pid := strconv.Itoa(os.Getpid())
	if err := os.WriteFile(path, []byte(pid+"\n"), 0o644); err != nil {
		return fmt.Errorf("writing pidfile %s: %w", path, err)
	}
	return nil
}
