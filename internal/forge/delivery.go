package forge

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// NotificationSender delivers messages and files to the requesting chat. It is
// satisfied by an adapter over messaging.Router so forge stays decoupled from
// the messaging package.
type NotificationSender interface {
	// SendMessage sends a text summary to the requester.
	SendMessage(ctx context.Context, chatID int64, text string) error
	// SendFile delivers a file (e.g. an APK) to the requester.
	SendFile(ctx context.Context, chatID int64, filename string, data []byte, caption string) error
}

// WebDeployer deploys a built web app and returns its public URL. It is
// satisfied by an adapter over the proxy manager.
type WebDeployer interface {
	DeployWebApp(ctx context.Context, distDir, name string) (string, error)
}

// DeliveryConfig configures the delivery agent.
type DeliveryConfig struct {
	Sender    NotificationSender
	Deployer  WebDeployer
	ServeBase string // base directory web apps are copied into
}

// DeliveryAgent delivers build output to the requester's channels.
type DeliveryAgent struct {
	cfg DeliveryConfig
}

// NewDeliveryAgent constructs the agent.
func NewDeliveryAgent(cfg DeliveryConfig) *DeliveryAgent {
	return &DeliveryAgent{cfg: cfg}
}

// Deliver routes the build output to each delivery target in the intent, then
// sends a summary message. Cost is the AI spend for the build (USD).
func (d *DeliveryAgent) Deliver(ctx context.Context, result BuildOutput, intent BuildIntent, chatID int64, cost float64) error {
	var delivered []string

	for _, target := range intent.DeliveryTargets {
		switch target {
		case "apk":
			if err := d.deliverAPK(ctx, result, chatID); err != nil {
				return fmt.Errorf("forge: deliver apk: %w", err)
			}
			delivered = append(delivered, "📱 APK: attached")
		case "web":
			url, err := d.deliverWeb(ctx, result, intent)
			if err != nil {
				return fmt.Errorf("forge: deliver web: %w", err)
			}
			delivered = append(delivered, "🌐 Web: "+url)
		case "api":
			delivered = append(delivered, "🔌 API: deployed")
		case "script", "":
			if err := d.deliverScript(ctx, result, chatID); err != nil {
				return fmt.Errorf("forge: deliver script: %w", err)
			}
			delivered = append(delivered, "📄 Script: attached")
		}
	}

	summary := buildSummary(result, delivered, cost)
	if d.cfg.Sender != nil {
		if err := d.cfg.Sender.SendMessage(ctx, chatID, summary); err != nil {
			return fmt.Errorf("forge: send summary: %w", err)
		}
	}
	return nil
}

// deliverAPK sends the built APK file to the requester.
func (d *DeliveryAgent) deliverAPK(ctx context.Context, result BuildOutput, chatID int64) error {
	if d.cfg.Sender == nil {
		return nil
	}
	apk := firstFileWithExt(result.ArtifactPath, ".apk")
	if apk == "" {
		return fmt.Errorf("no apk artifact found in %s", result.ArtifactPath)
	}
	data, err := readFile(apk)
	if err != nil {
		return err
	}
	return d.cfg.Sender.SendFile(ctx, chatID, baseName(apk), data, "Your app is ready")
}

// deliverScript sends the build's output (e.g. a script + its stdout).
func (d *DeliveryAgent) deliverScript(ctx context.Context, result BuildOutput, chatID int64) error {
	if d.cfg.Sender == nil {
		return nil
	}
	return d.cfg.Sender.SendMessage(ctx, chatID, "Script build complete.\n\n"+truncate(result.Stdout, 2000))
}

// deliverWeb deploys the web dist and returns its URL.
func (d *DeliveryAgent) deliverWeb(ctx context.Context, result BuildOutput, intent BuildIntent) (string, error) {
	if d.cfg.Deployer == nil {
		return "", fmt.Errorf("no web deployer configured")
	}
	name := slug(intent.Description)
	return d.DeployWebApp(ctx, result.ArtifactPath, name)
}

// DeployWebApp deploys a built web dist via the configured deployer, returning
// the public URL.
func (d *DeliveryAgent) DeployWebApp(ctx context.Context, distDir, name string) (string, error) {
	if d.cfg.Deployer == nil {
		return "", fmt.Errorf("forge: no web deployer configured")
	}
	if distDir == "" {
		return "", fmt.Errorf("forge: empty dist dir")
	}
	return d.cfg.Deployer.DeployWebApp(ctx, distDir, name)
}

// buildSummary renders the delivery summary message.
func buildSummary(result BuildOutput, delivered []string, cost float64) string {
	dur := time.Duration(result.DurationMs) * time.Millisecond
	var b strings.Builder
	b.WriteString("✅ Build complete in " + dur.Round(time.Second).String() + "\n")
	for _, line := range delivered {
		b.WriteString(line + "\n")
	}
	b.WriteString(fmt.Sprintf("⏱ Time: %s\n", dur.Round(time.Second)))
	b.WriteString(fmt.Sprintf("💰 AI cost: $%.4f", cost))
	return b.String()
}
