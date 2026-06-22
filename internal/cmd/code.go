package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/agents"
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
	var noTeam, team, initMD bool
	c := &cobra.Command{
		Use:   "code",
		Short: "Start VORTEX coding agent",
		Long: `Open the VORTEX coding interface.

Like Claude Code, but self-hosted and free.
Supports multiple AI providers and a specialist agent team.

Examples:
  vortex code
  vortex code --team
  vortex code --init
  vortex code --dir ~/myproject
  vortex code --model deepseek-chat`,
		Args: cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			if initMD {
				return runCodeInit(dir)
			}
			return runCode(addr, key, dir, model, team, noTeam)
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "project directory (default: current directory)")
	c.Flags().StringVar(&model, "model", "", "AI model override")
	c.Flags().BoolVar(&team, "team", false, "force the specialist agent team (Code/Test/Review)")
	c.Flags().BoolVar(&noTeam, "solo", false, "force single-agent mode (no specialist team)")
	c.Flags().BoolVar(&noTeam, "no-team", false, "alias for --solo")
	c.Flags().BoolVar(&initMD, "init", false, "generate AGENTS.md for the current project, then exit")
	c.Flags().StringVar(&addr, "addr", "http://localhost:9090", "VORTEX API address")
	c.Flags().StringVar(&key, "key", "", "API key (reads VORTEX_API_KEY if empty)")
	return c
}

// runCodeInit generates an AGENTS.md for the project at dir.
func runCodeInit(dir string) error {
	if dir == "" {
		dir, _ = os.Getwd()
	}
	fmt.Printf("%s Analyzing project structure...\n", brand.IconSpinner)

	gw := codeInitGateway()
	if gw == nil {
		fmt.Printf("%s No AI provider configured. Run: vortex setup\n", brand.IconError)
		return errCodeNotRunning
	}
	fmt.Printf("%s Generating AGENTS.md...\n", brand.IconSpinner)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	md, err := agents.Generate(ctx, gw, dir)
	if err != nil {
		fmt.Printf("%s Could not generate AGENTS.md: %v\n", brand.IconError, err)
		return errCodeNotRunning
	}
	fmt.Printf("%s AGENTS.md created\n\n", brand.IconSuccess)
	fmt.Println("Your project is now configured for VORTEX:")
	if md.Project != "" {
		fmt.Printf("  Project: %s\n", md.Project)
	}
	if len(md.Stack) > 0 {
		fmt.Printf("  Stack: %s\n", joinComma(md.Stack))
	}
	if md.TestCmd != "" {
		fmt.Printf("  Test command: %s\n", md.TestCmd)
	}
	fmt.Println("\nRun: vortex code")
	fmt.Println("To start coding with your agent team.")
	return nil
}

// codeInitGateway builds a one-shot AI gateway from the saved/env provider
// config for AGENTS.md generation (no running server required).
func codeInitGateway() agents.AIGateway {
	l := log
	if l == nil {
		l = slog.Default()
	}
	gw := buildMessaging(l).gateway
	if gw == nil {
		return nil
	}
	return gw
}

// runCode connects to the running server and opens the coding interface.
func runCode(addr, key, dir, model string, team, noTeam bool) error {
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
	// Mode resolution: --solo wins, then --team, else default to team.
	switch {
	case noTeam:
		opts = append(opts, views.WithoutTeam())
	case team:
		opts = append(opts, views.WithTeam())
	}

	// Load AGENTS.md (optional) for the PROJECT panel.
	if md, _ := agents.Load(dir); md != nil {
		opts = append(opts, views.WithProjectInfo(&views.ProjectInfo{
			Name: md.Project, Stack: md.Stack, TestCmd: md.TestCmd,
		}))
	}

	p := tea.NewProgram(views.NewCode(client, opts...),
		tea.WithAltScreen(), tea.WithMouseCellMotion())
	_, err := p.Run()
	return err
}

// joinComma joins a slice with ", ".
func joinComma(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}
