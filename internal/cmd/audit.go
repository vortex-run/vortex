package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/vortex-run/vortex/internal/audit"
	"github.com/vortex-run/vortex/internal/config"
)

// errAudit signals an audit-command failure whose detail was already printed.
var errAudit = errors.New("audit command failed")

// auditLogPath returns the on-disk path of the audit log, honouring
// VORTEX_AUDIT_LOG and otherwise defaulting to <user-cache>/vortex/audit.log.
func auditLogPath() string {
	if override := os.Getenv("VORTEX_AUDIT_LOG"); override != "" {
		return override
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		cacheDir = os.TempDir()
	}
	return filepath.Join(cacheDir, "vortex", "audit.log")
}

// openAuditLog loads the config to derive the cluster-scoped HMAC key, then
// opens the audit log. The key must match the one start.go uses so verification
// succeeds across processes.
func openAuditLog() (*audit.Log, error) {
	cfg, err := config.Load(flags.configPath)
	if err != nil {
		return nil, err
	}
	return audit.NewLog(auditLogPath(), []byte(cfg.Cluster.Name+"-audit-key"))
}

// newAuditCommand builds `vortex audit` with verify and export subcommands.
func newAuditCommand() *cobra.Command {
	c := &cobra.Command{
		Use:   "audit",
		Short: "Verify and export the tamper-proof audit log",
		Args:  cobra.NoArgs,
	}
	c.AddCommand(newAuditVerifyCommand())
	c.AddCommand(newAuditExportCommand())
	return c
}

func newAuditVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify",
		Short: "Verify the integrity of the audit log hash chain",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := openAuditLog()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errAudit
			}
			if verr := log.Verify(); verr != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "AUDIT LOG INTEGRITY FAILURE: %v\n", verr)
				return errAudit
			}
			entries, qerr := log.Query(audit.QueryFilter{})
			if qerr != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", qerr)
				return errAudit
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Audit log integrity verified (%d entries)\n", len(entries))
			return nil
		},
	}
}

func newAuditExportCommand() *cobra.Command {
	var (
		format string
		since  string
		until  string
		actor  string
		action string
		output string
	)
	c := &cobra.Command{
		Use:   "export",
		Short: "Export audit log entries in json|splunk|syslog|csv format",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			filter, err := buildAuditFilter(since, until, actor, action)
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errAudit
			}

			log, err := openAuditLog()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errAudit
			}

			w := cmd.OutOrStdout()
			if output != "" {
				f, ferr := os.Create(output) //nolint:gosec // operator-supplied output path
				if ferr != nil {
					fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", ferr)
					return errAudit
				}
				defer func() { _ = f.Close() }()
				w = f
			}

			exporter, ok := auditExporters[format]
			if !ok {
				fmt.Fprintf(cmd.OutOrStderr(), "error: unknown format %q (want json|splunk|syslog|csv)\n", format)
				return errAudit
			}
			if eerr := exporter(log, filter, w); eerr != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", eerr)
				return errAudit
			}
			return nil
		},
	}
	c.Flags().StringVar(&format, "format", "json", "output format: json|splunk|syslog|csv")
	c.Flags().StringVar(&since, "since", "", "only entries at or after this RFC3339 timestamp")
	c.Flags().StringVar(&until, "until", "", "only entries at or before this RFC3339 timestamp")
	c.Flags().StringVar(&actor, "actor", "", "filter by actor")
	c.Flags().StringVar(&action, "action", "", "filter by action")
	c.Flags().StringVar(&output, "output", "", "output file path (default: stdout)")
	return c
}

// auditExporters maps a format name to its export function.
var auditExporters = map[string]func(*audit.Log, audit.QueryFilter, io.Writer) error{
	"json":   audit.ExportJSON,
	"splunk": audit.ExportSplunk,
	"syslog": audit.ExportSyslog,
	"csv":    audit.ExportCSV,
}

// buildAuditFilter parses the export filter flags into a QueryFilter.
func buildAuditFilter(since, until, actor, action string) (audit.QueryFilter, error) {
	f := audit.QueryFilter{Actor: actor, Action: action}
	if since != "" {
		ts, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return f, fmt.Errorf("invalid --since timestamp: %w", err)
		}
		f.Since = ts
	}
	if until != "" {
		ts, err := time.Parse(time.RFC3339, until)
		if err != nil {
			return f, fmt.Errorf("invalid --until timestamp: %w", err)
		}
		f.Until = ts
	}
	return f, nil
}
