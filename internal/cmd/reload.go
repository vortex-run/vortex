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

	"github.com/vortex-run/vortex/internal/api"
	"github.com/vortex-run/vortex/pkg/pidfile"
)

// newReloadCommand builds `gubern reload`, which asks a running server to
// re-read its config without restarting. On Unix it sends SIGHUP to the PID in
// the pidfile; on Windows it calls the localhost-only POST /internal/reload
// endpoint. It then checks the outcome the server recorded and reads /health
// to report the (possibly new) config hash.
func newReloadCommand() *cobra.Command {
	var (
		pidfilePath string
		apiPort     int
	)
	c := &cobra.Command{
		Use:   "reload",
		Short: "Reload configuration without restarting the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			alive, pid, err := pidfile.IsRunning(resolvePidfile(pidfilePath))
			if errors.Is(err, os.ErrNotExist) || (err == nil && !alive) {
				fmt.Fprintln(out, "Gubern is not running")
				return errNotRunning
			}
			if err != nil {
				return fmt.Errorf("checking pidfile: %w", err)
			}

			sent := time.Now()
			if err := requestReload(pid, apiPort); err != nil {
				return fmt.Errorf("requesting reload: %w", err)
			}

			// Give the server a moment to re-validate and swap config.
			time.Sleep(2 * time.Second)

			// SIGHUP has no reply, so read the outcome the server recorded.
			if st, ok := lastReloadStatus(apiPort); ok && !st.At.Before(sent.Add(-time.Second)) && !st.OK {
				return fmt.Errorf("reload failed: %s (the server keeps running its previous config)", st.Error)
			}

			body, err := fetchHealth(apiPort)
			if err != nil {
				return fmt.Errorf("confirming reload via health: %w", err)
			}
			var h statusHealth
			if err := json.Unmarshal(body, &h); err != nil {
				return fmt.Errorf("parsing health response: %w", err)
			}
			fmt.Fprintf(out, "Config reloaded successfully (hash: %s)\n", shortHash(h.ConfigHash))
			return nil
		},
	}
	c.Flags().StringVar(&pidfilePath, "pidfile", defaultPidfile, "path to the PID file")
	c.Flags().IntVar(&apiPort, "api-port", defaultAPIPort(), "management API port")
	return c
}

// lastReloadStatus reads last_reload from GET /api/status (loopback-trusted
// by default). ok is false when it can't: no reload recorded yet, an older
// server, or GUBERN_TRUST_LOOPBACK=false.
func lastReloadStatus(apiPort int) (api.ReloadStatus, bool) {
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + localAPIAddr(apiPort) + "/api/status")
	if err != nil {
		return api.ReloadStatus{}, false
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return api.ReloadStatus{}, false
	}
	var st struct {
		LastReload *api.ReloadStatus `json:"last_reload"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&st); err != nil || st.LastReload == nil {
		return api.ReloadStatus{}, false
	}
	return *st.LastReload, true
}
