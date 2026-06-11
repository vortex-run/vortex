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

// openAuditLog opens the audit log keyed by the master-key-derived audit
// subkey. The key must match the one start.go uses so verification succeeds
// across processes (both derive "audit" from the shared master key).
func openAuditLog() (*audit.Log, error) {
	auditKey, err := deriveKey("audit")
	if err != nil {
		return nil, err
	}
	return audit.NewLog(auditLogPath(), auditKey)
}

// auditCLI records a CLI-initiated audit event (actor "cli"), never logging
// secret values. It is best-effort: an audit-log failure must not break the
// user's command, so errors are silently ignored. The resource is the object
// acted on (e.g. a secret name).
func auditCLI(cmd *cobra.Command, action, resource string) {
	log, err := openAuditLog()
	if err != nil {
		return
	}
	_ = log.Append(cmd.Context(), "cli", action, resource, nil)
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
	c.AddCommand(newAuditReportCommand())
	c.AddCommand(newAuditArchiveCommand())
	return c
}

// newAuditReportCommand builds `vortex audit report` (M19): a compliance
// summary of the audit log over a period, in markdown, JSON, or CSV.
func newAuditReportCommand() *cobra.Command {
	var (
		since  string
		until  string
		format string
		output string
	)
	c := &cobra.Command{
		Use:   "report",
		Short: "Generate a compliance report (markdown|json|csv)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			errOut := cmd.OutOrStderr()
			var sinceT, untilT time.Time
			var err error
			if since != "" {
				if sinceT, err = parseAuditTime(since); err != nil {
					fmt.Fprintf(errOut, "error: invalid --since: %v\n", err)
					return errAudit
				}
			}
			if until != "" {
				if untilT, err = parseAuditTime(until); err != nil {
					fmt.Fprintf(errOut, "error: invalid --until: %v\n", err)
					return errAudit
				}
			}

			log, err := openAuditLog()
			if err != nil {
				fmt.Fprintf(errOut, "error: %v\n", err)
				return errAudit
			}
			report, err := audit.GenerateComplianceReport(log, sinceT, untilT)
			if err != nil {
				fmt.Fprintf(errOut, "error: %v\n", err)
				return errAudit
			}

			w := cmd.OutOrStdout()
			if output != "" {
				f, ferr := os.Create(output) //nolint:gosec // operator-supplied output path
				if ferr != nil {
					fmt.Fprintf(errOut, "error: %v\n", ferr)
					return errAudit
				}
				defer func() { _ = f.Close() }()
				w = f
			}

			switch format {
			case "markdown":
				err = report.WriteMarkdown(w)
			case "json":
				err = report.WriteJSON(w)
			case "csv":
				err = report.WriteCSV(w)
			default:
				fmt.Fprintf(errOut, "error: unknown format %q (want markdown|json|csv)\n", format)
				return errAudit
			}
			if err != nil {
				fmt.Fprintf(errOut, "error: %v\n", err)
				return errAudit
			}
			if output != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "Compliance report written to %s\n", output)
			}
			return nil
		},
	}
	c.Flags().StringVar(&since, "since", "", "start of the report period (2006-01-02 or RFC3339)")
	c.Flags().StringVar(&until, "until", "", "end of the report period (2006-01-02 or RFC3339)")
	c.Flags().StringVar(&format, "format", "markdown", "report format: markdown|json|csv")
	c.Flags().StringVar(&output, "output", "", "output file path (default: stdout)")
	return c
}

// newAuditArchiveCommand builds `vortex audit archive` (M19): manually rotate
// the live audit log into a gzipped monthly archive.
func newAuditArchiveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "archive",
		Short: "Archive the live audit log to audit-<year>-<month>.log.gz",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			log, err := openAuditLog()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errAudit
			}
			dest, err := log.Archive()
			if err != nil {
				fmt.Fprintf(cmd.OutOrStderr(), "error: %v\n", err)
				return errAudit
			}
			out := cmd.OutOrStdout()
			if dest == "" {
				fmt.Fprintln(out, "Audit log is empty — nothing to archive.")
				return nil
			}
			fmt.Fprintf(out, "Audit log archived to %s\n", dest)
			fmt.Fprintln(out, "A fresh audit log has been started.")
			return nil
		},
	}
}

// parseAuditTime parses a report period bound: a bare date (2006-01-02) or a
// full RFC3339 timestamp.
func parseAuditTime(s string) (time.Time, error) {
	if ts, err := time.Parse("2006-01-02", s); err == nil {
		return ts, nil
	}
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a date (2006-01-02) or RFC3339 timestamp", s)
	}
	return ts, nil
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
