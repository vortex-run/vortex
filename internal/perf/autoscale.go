package perf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// knownProviders are the cloud providers the autoscaler understands. "custom"
// posts to a webhook; the cloud names are stubs until their APIs are wired.
var knownProviders = map[string]bool{
	"hetzner": true, "digitalocean": true, "vultr": true,
	"linode": true, "custom": true,
}

// AutoscaleConfig configures horizontal autoscaling.
type AutoscaleConfig struct {
	Provider    string        // "hetzner"|"digitalocean"|"vultr"|"linode"|"custom"
	APIKey      string        // from env — never a config file
	WebhookURL  string        // for "custom" provider
	MinNodes    int           // default 1
	MaxNodes    int           // default 10
	ScaleUpAt   float64       // CPU% to scale up (default 80)
	ScaleDownAt float64       // CPU% to scale down (default 20)
	Cooldown    time.Duration // min time between scale events (default 5m)
}

// AutoscaleDecision is the outcome of an Evaluate call.
type AutoscaleDecision struct {
	Action    string `json:"action"` // "scale-up"|"scale-down"|"none"
	Reason    string `json:"reason"`
	NodeCount int    `json:"node_count"` // desired node count
}

// Autoscaler evaluates CPU pressure and triggers scale events.
type Autoscaler struct {
	cfg    AutoscaleConfig
	client *http.Client

	mu        sync.Mutex
	lastScale time.Time
}

// NewAutoscaler validates cfg and builds an Autoscaler.
func NewAutoscaler(cfg AutoscaleConfig) (*Autoscaler, error) {
	if !knownProviders[cfg.Provider] {
		return nil, fmt.Errorf("perf: unknown autoscale provider %q", cfg.Provider)
	}
	if cfg.MinNodes <= 0 {
		cfg.MinNodes = 1
	}
	if cfg.MaxNodes <= 0 {
		cfg.MaxNodes = 10
	}
	if cfg.MaxNodes < cfg.MinNodes {
		return nil, fmt.Errorf("perf: MaxNodes (%d) < MinNodes (%d)", cfg.MaxNodes, cfg.MinNodes)
	}
	if cfg.ScaleUpAt <= 0 {
		cfg.ScaleUpAt = 80
	}
	if cfg.ScaleDownAt <= 0 {
		cfg.ScaleDownAt = 20
	}
	if cfg.Cooldown <= 0 {
		cfg.Cooldown = 5 * time.Minute
	}
	return &Autoscaler{cfg: cfg, client: &http.Client{Timeout: 10 * time.Second}}, nil
}

// Evaluate decides whether to scale given current CPU usage and node count. It
// respects the cooldown and the min/max node bounds.
func (a *Autoscaler) Evaluate(cpuPercent float64, currentNodes int) AutoscaleDecision {
	a.mu.Lock()
	cooling := time.Since(a.lastScale) < a.cfg.Cooldown && !a.lastScale.IsZero()
	a.mu.Unlock()

	if cooling {
		return AutoscaleDecision{Action: "none", Reason: "cooldown active", NodeCount: currentNodes}
	}

	switch {
	case cpuPercent > a.cfg.ScaleUpAt && currentNodes < a.cfg.MaxNodes:
		return AutoscaleDecision{
			Action: "scale-up", NodeCount: currentNodes + 1,
			Reason: fmt.Sprintf("CPU at %.0f%% > %.0f%%", cpuPercent, a.cfg.ScaleUpAt),
		}
	case cpuPercent < a.cfg.ScaleDownAt && currentNodes > a.cfg.MinNodes:
		return AutoscaleDecision{
			Action: "scale-down", NodeCount: currentNodes - 1,
			Reason: fmt.Sprintf("CPU at %.0f%% < %.0f%%", cpuPercent, a.cfg.ScaleDownAt),
		}
	default:
		return AutoscaleDecision{Action: "none", Reason: "within thresholds", NodeCount: currentNodes}
	}
}

// Execute carries out a scale decision. For the "custom" provider it POSTs a
// JSON body to the webhook; cloud providers are stubbed (logged, non-fatal).
func (a *Autoscaler) Execute(ctx context.Context, decision AutoscaleDecision) error {
	if decision.Action == "none" {
		return nil
	}
	a.mu.Lock()
	a.lastScale = time.Now()
	a.mu.Unlock()

	if a.cfg.Provider == "custom" {
		body, _ := json.Marshal(map[string]any{
			"action":        decision.Action,
			"desired_nodes": decision.NodeCount,
			"reason":        decision.Reason,
		})
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.WebhookURL, bytes.NewReader(body))
		if err != nil {
			return fmt.Errorf("perf: building autoscale webhook request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := a.client.Do(req)
		if err != nil {
			return fmt.Errorf("perf: posting autoscale webhook: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("perf: autoscale webhook returned %s", resp.Status)
		}
		return nil
	}

	// Cloud provider APIs are not yet implemented; log and continue.
	slog.Default().Info("scaling via cloud provider not yet implemented",
		"provider", a.cfg.Provider, "action", decision.Action, "desired_nodes", decision.NodeCount)
	return nil
}

// CPUProvider returns the current cluster-wide CPU usage percentage.
type CPUProvider func() float64

// Start runs the autoscale loop every 30s until ctx is cancelled: it reads CPU
// via cpu, evaluates, and executes any non-none decision. nodes returns the
// current node count.
func (a *Autoscaler) Start(ctx context.Context, cpu CPUProvider, nodes func() int) error {
	if cpu == nil || nodes == nil {
		return errors.New("perf: autoscaler Start requires cpu and nodes providers")
	}
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			decision := a.Evaluate(cpu(), nodes())
			if decision.Action == "none" {
				continue
			}
			slog.Default().Info("autoscale decision",
				"action", decision.Action, "desired_nodes", decision.NodeCount, "reason", decision.Reason)
			if err := a.Execute(ctx, decision); err != nil {
				slog.Default().Warn("autoscale execute failed", "err", err)
			}
		}
	}
}
