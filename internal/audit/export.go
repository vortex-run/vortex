package audit

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"time"
)

// ExportJSON writes the filtered entries as a single JSON array to w.
func ExportJSON(log *Log, filter QueryFilter, w io.Writer) error {
	entries, err := log.Query(filter)
	if err != nil {
		return err
	}
	if entries == nil {
		entries = []Entry{}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(entries); err != nil {
		return fmt.Errorf("audit: exporting JSON: %w", err)
	}
	return nil
}

// ExportSplunk writes the filtered entries in Splunk HEC JSON format, one event
// per line: {"time":<epoch>,"event":{...entry fields...}}.
func ExportSplunk(log *Log, filter QueryFilter, w io.Writer) error {
	entries, err := log.Query(filter)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w) // newline-delimited, one object per line
	for _, e := range entries {
		hec := map[string]any{
			"time":  e.Timestamp.Unix(),
			"event": e,
		}
		if err := enc.Encode(hec); err != nil {
			return fmt.Errorf("audit: exporting Splunk: %w", err)
		}
	}
	return nil
}

// ExportSyslog writes the filtered entries in RFC 5424 syslog format, one line
// per entry. VORTEX audit events use facility 13 (log audit) and severity 6
// (informational), giving PRI = 13*8 + 6 = 110.
func ExportSyslog(log *Log, filter QueryFilter, w io.Writer) error {
	entries, err := log.Query(filter)
	if err != nil {
		return err
	}
	const (
		pri      = 110 // facility 13 (audit) * 8 + severity 6 (info)
		version  = 1
		hostname = "vortex"
		appName  = "vortex-audit"
	)
	for _, e := range entries {
		// RFC 5424: <PRI>VERSION TIMESTAMP HOSTNAME APP-NAME PROCID MSGID SD MSG
		// PROCID = sequence number; MSGID = action; SD = "-"; MSG = a compact
		// key/value description.
		ts := e.Timestamp.UTC().Format(time.RFC3339)
		msg := fmt.Sprintf("actor=%s resource=%s", e.Actor, e.Resource)
		line := fmt.Sprintf("<%d>%d %s %s %s %d %s - %s\n",
			pri, version, ts, hostname, appName, e.Seq, e.Action, msg)
		if _, err := io.WriteString(w, line); err != nil {
			return fmt.Errorf("audit: exporting syslog: %w", err)
		}
	}
	return nil
}

// ExportCSV writes the filtered entries as CSV with a header row. The detail map
// is JSON-encoded into a single column.
func ExportCSV(log *Log, filter QueryFilter, w io.Writer) error {
	entries, err := log.Query(filter)
	if err != nil {
		return err
	}
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"seq", "timestamp", "actor", "action", "resource", "detail", "hash"}); err != nil {
		return fmt.Errorf("audit: exporting CSV header: %w", err)
	}
	for _, e := range entries {
		detail := ""
		if e.Detail != nil {
			b, merr := json.Marshal(e.Detail)
			if merr != nil {
				return fmt.Errorf("audit: encoding detail for CSV: %w", merr)
			}
			detail = string(b)
		}
		row := []string{
			strconv.FormatUint(e.Seq, 10),
			e.Timestamp.UTC().Format(time.RFC3339),
			e.Actor,
			e.Action,
			e.Resource,
			detail,
			e.Hash,
		}
		if err := cw.Write(row); err != nil {
			return fmt.Errorf("audit: exporting CSV row: %w", err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("audit: flushing CSV: %w", err)
	}
	return nil
}
