package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	agentskills "github.com/vortex-run/vortex/internal/skills"
)

// errSkills signals that some skills failed to load; the detail was printed.
var errSkills = errors.New("some skills could not be loaded")

// newSkillsCommand builds `gubern skills`, for inspecting the Agent Skills
// (SKILL.md folders) that /agent offers to the model.
func newSkillsCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "skills",
		Short: "Inspect the Agent Skills /agent can use",
	}
	c.AddCommand(newSkillsListCommand())
	return c
}

func newSkillsListCommand() *cobra.Command {
	var dir string
	c := &cobra.Command{
		Use:   "list",
		Short: "List workspace skills and any that fail to load",
		Long: "Lists the skills /agent would offer for a workspace (default: the current\n" +
			"directory), read from .agents/skills, .claude/skills and .opencode/skills.\n" +
			"Skills that fail validation are shown with the reason and skipped by\n" +
			"/agent. Exit status is non-zero if any fail.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dir == "" {
				wd, err := os.Getwd()
				if err != nil {
					return err
				}
				dir = wd
			}
			return runSkillsList(cmd.OutOrStdout(), dir)
		},
	}
	c.Flags().StringVar(&dir, "dir", "", "workspace directory (default: current directory)")
	return c
}

func runSkillsList(out io.Writer, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	// Same roots /agent uses (workspace only; see Coordinator.skillCatalog).
	found, problems := agentskills.Discover(agentskills.DefaultRoots(abs, ""))
	if len(found) == 0 && len(problems) == 0 {
		fmt.Fprintf(out, "No skills in %s (looked in .agents/skills, .claude/skills, .opencode/skills).\n", abs)
		return nil
	}
	for _, s := range found {
		rel, rerr := filepath.Rel(abs, filepath.Join(s.Dir, "SKILL.md"))
		if rerr != nil {
			rel = filepath.Join(s.Dir, "SKILL.md")
		}
		fmt.Fprintf(out, "%-28s %s\n", s.Name, oneLine(s.Description, 90))
		fmt.Fprintf(out, "%-28s %s  sha256:%s\n", "", filepath.ToSlash(rel), s.Hash[:12])
		if len(s.AllowedTools) > 0 {
			fmt.Fprintf(out, "%-28s allowed-tools requested (not granted): %s\n", "", strings.Join(s.AllowedTools, " "))
		}
	}
	for _, p := range problems {
		fmt.Fprintf(out, "SKIPPED %s: %v\n", filepath.ToSlash(p.Path), p.Err)
	}
	if len(problems) > 0 {
		return errSkills
	}
	return nil
}

func oneLine(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > limit {
		return s[:limit] + "…"
	}
	return s
}
