package cmd

import (
	"errors"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/tui"
	"github.com/vortex-run/vortex/internal/tui/brand"
	"github.com/vortex-run/vortex/internal/tui/views"
)

// errCodeNotRunning signals that `vortex code` found no running server; the
// helpful message was already printed, so Execute exits 1 quietly.
var errCodeNotRunning = errors.New("code: vortex is not running")

// newCodeCommand builds `vortex code` — the dedicated coding interface.
func newCodeCommand() *cobra.Command {
	var dir, model, addr, key string
	var noTeam bool
	c := &cobra.Command{
		Use:   "code",
		Short: "Start VORTEX coding agent",
		Long: `Open the VORTEX coding interface.

Like Claude Code, but self-hosted and free.
Supports multiple AI providers.

Examples:
  vortex code
  vortex code --dir ~/myproject
  vortex code --model deepseek-chat`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			return runCode(addr, key, dir, model, noTeam)
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "project directory (default: current directory)")
	c.Flags().StringVar(&model, "model", "", "AI model override")
	c.Flags().BoolVar(&noTeam, "no-team", false, "single agent mode (no specialist team)")
	c.Flags().StringVar(&addr, "addr", "http://localhost:9090", "VORTEX API address")
	c.Flags().StringVar(&key, "key", "", "API key (reads VORTEX_API_KEY if empty)")
	return c
}

// runCode connects to the running server and opens the coding interface.
func runCode(addr, key, dir, model string, noTeam bool) error {
	fmt.Println(brand.StyleTitle.Render("▲ VORTEX CODE") + "  Starting...")

	client := tui.NewClient(tui.ClientConfig{BaseURL: addr, APIKey: key})
	if key == "" {
		client = tui.NewClient(tui.ClientConfig{BaseURL: addr, APIKey: client.LoadAPIKey()})
	}
	if !client.IsConnected() {
		fmt.Println("VORTEX is not running.")
		fmt.Println("Start it first: vortex start")
		fmt.Println("Or start with UI: vortex start --ui")
		return errCodeNotRunning
	}

	if dir == "" {
		dir, _ = os.Getwd()
	}
	opts := []views.CodeOption{views.Standalone(), views.WithProject(dir)}
	if model != "" {
		opts = append(opts, views.WithModel(model))
	}
	if noTeam {
		opts = append(opts, views.WithoutTeam())
	}

	p := tea.NewProgram(views.NewCode(client, opts...),
		tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}
