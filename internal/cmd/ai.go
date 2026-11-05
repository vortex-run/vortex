package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sandeepbollavaram/vortex/internal/agents"
	"github.com/sandeepbollavaram/vortex/internal/llm"
	"github.com/sandeepbollavaram/vortex/internal/messaging"
)

// errProbe signals that a probe check failed; the detail was already printed.
var errProbe = errors.New("one or more providers failed the tool-use probe")

// newAICommand builds `gubern ai`, for checking the configured AI providers.
func newAICommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "ai",
		Short: "Check the configured AI providers",
	}
	c.AddCommand(newAIProbeCommand())
	return c
}

// newAIProbeCommand builds `gubern ai probe`: run the tool-use checks /agent
// depends on against each configured provider, so a model that can't drive
// the agent loop is found before a task relies on it.
func newAIProbeCommand() *cobra.Command {
	var only string
	c := &cobra.Command{
		Use:   "probe",
		Short: "Check that each AI provider can drive /agent (tool calling)",
		Long: "Sends a few small requests to each configured provider (the same env vars\n" +
			"and setup file `gubern start` uses) and checks: it calls a tool, uses\n" +
			"schema-typed arguments, reads a tool result back, and ignores an\n" +
			"instruction planted in a tool result. Nothing is executed. Each provider\n" +
			"costs a few short model calls.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			providers := aiProviders(slog.New(slog.NewTextHandler(io.Discard, nil)))
			if len(providers) == 0 {
				return errors.New("no AI provider configured: run `vortex setup` or set a provider key (e.g. GUBERN_OLLAMA_URL)")
			}
			gw, err := messaging.NewAIGateway(messaging.AIGatewayConfig{Providers: providers})
			if err != nil {
				return err
			}
			return runAIProbe(cmd.Context(), cmd.OutOrStdout(), gw.EachProvider(), only)
		},
	}
	c.Flags().StringVar(&only, "provider", "", "probe only this provider (e.g. ollama, claude)")
	return c
}

// printChecks prints one block of probe results and reports whether all passed.
func printChecks(out io.Writer, checks []agents.ProbeCheck) bool {
	ok := true
	for _, c := range checks {
		mark := "ok  "
		if !c.Pass {
			mark, ok = "FAIL", false
		}
		fmt.Fprintf(out, "  %s %-30s %6s  %s\n", mark, c.Name, c.Took.Round(time.Millisecond*10), c.Detail)
	}
	return ok
}

// unsupportedNative reports a model that has no native tool calling (as
// opposed to one that is unreachable or answered wrongly).
func unsupportedNative(checks []agents.ProbeCheck) bool {
	return len(checks) > 0 && strings.Contains(checks[0].Detail, llm.ErrToolsUnsupported.Error())
}

func runAIProbe(ctx context.Context, out io.Writer, providers []messaging.ProviderTools, only string) error {
	failed, ran := false, 0
	for _, p := range providers {
		if only != "" && p.Name() != only {
			continue
		}
		ran++
		model := p.Model()
		if model == "" {
			model = "default model"
		}
		fmt.Fprintf(out, "%s (%s)\n", p.Name(), model)
		pctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		checks := agents.ProbeModel(pctx, p)
		cancel()
		if !printChecks(out, checks) {
			failed = true
			if unsupportedNative(checks) {
				// /agent falls back to the text protocol for such models;
				// report how well that works too.
				fmt.Fprintln(out, "  no native tool calling; trying the text protocol /agent falls back to:")
				pctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
				if printChecks(out, agents.ProbeModel(pctx, agents.PromptedTools{Model: p})) {
					fmt.Fprintln(out, "  usable through the text protocol (less reliable than native tools)")
				}
				cancel()
			}
		}
	}
	if ran == 0 {
		return fmt.Errorf("no configured provider named %q", only)
	}
	if failed {
		return errProbe
	}
	return nil
}
