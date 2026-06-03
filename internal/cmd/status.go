package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/pkg/pidfile"
)

// statusHealth mirrors the JSON returned by GET /health.
type statusHealth struct {
	Status      string `json:"status"`
	Version     string `json:"version"`
	ConfigHash  string `json:"config_hash"`
	ClusterName string `json:"cluster_name"`
	Uptime      string `json:"uptime"`
}

// errNotRunning signals that the server is not running; status exits 1 without
// an extra generic log line.
var errNotRunning = errors.New("vortex is not running")

// newStatusCommand builds `vortex status`, which reports whether the server is
// running and, if so, fetches /health and prints a formatted table (or raw
// JSON with --json).
func newStatusCommand() *cobra.Command {
	var (
		pidfilePath string
		apiPort     int
		jsonOut     bool
	)
	c := &cobra.Command{
		Use:   "status",
		Short: "Show status of the running VORTEX server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			alive, pid, err := pidfile.IsRunning(pidfilePath)
			if errors.Is(err, os.ErrNotExist) || (err == nil && !alive) {
				fmt.Fprintln(out, "VORTEX is not running")
				return errNotRunning
			}
			if err != nil {
				return fmt.Errorf("checking pidfile: %w", err)
			}

			body, err := fetchHealth(apiPort)
			if err != nil {
				return fmt.Errorf("fetching health from API: %w", err)
			}

			if jsonOut {
				fmt.Fprintln(out, string(body))
				return nil
			}

			var h statusHealth
			if err := json.Unmarshal(body, &h); err != nil {
				return fmt.Errorf("parsing health response: %w", err)
			}
			printStatusTable(out, pid, apiPort, h)
			return nil
		},
	}
	c.Flags().StringVar(&pidfilePath, "pidfile", "vortex.pid", "path to the PID file")
	c.Flags().IntVar(&apiPort, "api-port", 9090, "management API port")
	c.Flags().BoolVar(&jsonOut, "json", false, "output raw JSON from /health instead of a table")
	return c
}

func fetchHealth(apiPort int) ([]byte, error) {
	url := fmt.Sprintf("http://127.0.0.1:%d/health", apiPort)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("/health returned %s", resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func printStatusTable(out io.Writer, pid, apiPort int, h statusHealth) {
	fmt.Fprintf(out, "Status:   running\n")
	fmt.Fprintf(out, "PID:      %d\n", pid)
	fmt.Fprintf(out, "Version:  %s\n", h.Version)
	fmt.Fprintf(out, "Uptime:   %s\n", h.Uptime)
	fmt.Fprintf(out, "Config:   %s (hash: %s)\n", flags.configPath, shortHash(h.ConfigHash))
	fmt.Fprintf(out, "Cluster:  %s\n", h.ClusterName)
	fmt.Fprintf(out, "API:      http://127.0.0.1:%d\n", apiPort)
}
