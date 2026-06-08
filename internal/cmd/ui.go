package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/tui"
	tuiapp "github.com/vortex-run/vortex/internal/tui/app"
)

// errUINotRunning signals that `vortex ui` found no running server (and was not
// asked to start one); the message was already printed, so Execute exits 1
// without an extra generic log line.
var errUINotRunning = errors.New("ui: vortex is not running")

// newUICommand builds `vortex ui`.
func newUICommand() *cobra.Command {
	var addr, key string
	var start bool
	c := &cobra.Command{
		Use:   "ui",
		Short: "Open the VORTEX terminal dashboard",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runUI(cmd.Context(), addr, key, start)
		},
	}
	c.Flags().StringVar(&addr, "addr", "http://localhost:9090", "VORTEX API address")
	c.Flags().StringVar(&key, "key", "", "API key (reads VORTEX_API_KEY if empty)")
	c.Flags().BoolVar(&start, "start", false, "start an embedded server if VORTEX is not running")
	return c
}

// runUI connects (optionally starting an embedded server) and runs the TUI.
func runUI(ctx context.Context, addr, key string, start bool) error {
	client := tui.NewClient(tui.ClientConfig{BaseURL: addr, APIKey: key})
	if key == "" {
		client = tui.NewClient(tui.ClientConfig{BaseURL: addr, APIKey: client.LoadAPIKey()})
	}

	if !client.IsConnected() {
		if !start {
			fmt.Println("VORTEX is not running.")
			fmt.Println("Start it with: vortex start")
			fmt.Println("Or run:        vortex ui --start")
			return errUINotRunning
		}
		if err := startEmbeddedServer(ctx, client); err != nil {
			return err
		}
	}

	app := tuiapp.NewApp(client)
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// startEmbeddedServer launches the server in the background and waits (up to 5s)
// for it to accept connections.
func startEmbeddedServer(ctx context.Context, client *tui.Client) error {
	go func() {
		// runStart blocks until shutdown; run it detached. Errors surface via the
		// readiness poll below.
		_ = runStart(ctx, "vortex.pid")
	}()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if client.IsConnected() {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("embedded server did not become ready within 5s")
}
