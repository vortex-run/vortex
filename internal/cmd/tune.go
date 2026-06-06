package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/perf"
)

// errTune signals a tune-command failure whose detail was already printed.
var errTune = errors.New("tune command failed")

// benchBaselinePath returns the benchmark baseline path
// (<user-cache>/vortex/bench-baseline.json).
func benchBaselinePath() string {
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "bench-baseline.json")
}

// newTuneCommand builds `vortex tune` with show/apply/bench subcommands.
func newTuneCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "tune",
		Short: "Inspect, apply OS tuning and run benchmarks",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newTuneShowCommand())
	c.AddCommand(newTuneApplyCommand())
	c.AddCommand(newTuneBenchCommand())
	return c
}

func newTuneShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show recommended OS tuning settings",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			osName := perf.DetectOS()
			settings := perf.RecommendedSysctl()
			fmt.Fprintf(out, "Recommended OS tuning for %s:\n", osName)
			if len(settings) == 0 {
				fmt.Fprintln(out, "  (no sysctl tuning applies on this OS)")
			} else {
				keys := make([]string, 0, len(settings))
				for k := range settings {
					keys = append(keys, k)
				}
				sort.Strings(keys)
				for _, k := range keys {
					fmt.Fprintf(out, "  %s = %s\n", k, settings[k])
				}
			}
			fmt.Fprintf(out, "GOMAXPROCS: %d\n", perf.MaxGOMAXPROCS())
			fmt.Fprintf(out, "Buffer size: %d bytes\n", perf.RecommendedBufferSize())
			return nil
		},
	}
}

func newTuneApplyCommand() *cobra.Command {
	var dryRun bool
	c := &cobra.Command{
		Use:   "apply",
		Short: "Apply recommended OS tuning (requires root on Linux)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			res := perf.Apply(dryRun)
			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintf(out, "Dry run — would apply %d settings:\n", len(res.Skipped))
				for _, s := range res.Skipped {
					fmt.Fprintf(out, "  %s\n", s)
				}
				return nil
			}
			fmt.Fprintf(out, "Applied %d settings, skipped %d (insufficient perms)\n",
				len(res.Applied), len(res.Skipped))
			for _, e := range res.Errors {
				fmt.Fprintf(cmd.OutOrStderr(), "  error: %s\n", e)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be applied without changing anything")
	return c
}

func newTuneBenchCommand() *cobra.Command {
	var save bool
	c := &cobra.Command{
		Use:   "bench",
		Short: "Run the built-in benchmark suite",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			suite := perf.NewBenchmarkSuite("vortex")
			results := suite.QuickBench()

			fmt.Fprintln(out, "Benchmark results:")
			for _, r := range results {
				switch {
				case r.ThroughputMBs > 0 && r.ReqPerSec > 0:
					fmt.Fprintf(out, "  %s: %.1f MB/s, %.0f pps\n", r.Name, r.ThroughputMBs, r.ReqPerSec)
				case r.ThroughputMBs > 0:
					fmt.Fprintf(out, "  %s: %.1f MB/s\n", r.Name, r.ThroughputMBs)
				default:
					fmt.Fprintf(out, "  %s: %.0f req/s\n", r.Name, r.ReqPerSec)
				}
			}

			// Compare against an existing baseline, if any.
			path := benchBaselinePath()
			if base, err := suite.LoadBaseline(path); err == nil && len(results) > 0 {
				rep := suite.Compare(base, results[0])
				if rep.Regressed {
					fmt.Fprintf(out, "REGRESSION: %s\n", rep.Message)
				} else {
					fmt.Fprintln(out, "No regression vs baseline.")
				}
			}

			if save && len(results) > 0 {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
					return errTune
				}
				if err := suite.SaveBaseline(results[0], path); err != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
					return errTune
				}
				fmt.Fprintf(out, "Baseline saved to %s\n", path)
			}
			return nil
		},
	}
	c.Flags().BoolVar(&save, "save", false, "save results as the new baseline")
	return c
}
