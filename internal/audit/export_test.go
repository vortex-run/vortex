package audit

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"strings"
	"testing"
)

// seedLog appends n entries with the given actor and returns the log.
func seedLog(t *testing.T, entries ...[3]string) *Log {
	t.Helper()
	l, _ := newTestLog(t)
	for _, e := range entries {
		if err := l.Append(context.Background(), e[0], e[1], e[2], map[string]any{"x": 1}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func TestExport_JSONValidArray(t *testing.T) {
	l := seedLog(t, [3]string{"a", "act", "r"}, [3]string{"b", "act", "r"})
	var buf bytes.Buffer
	if err := ExportJSON(l, QueryFilter{}, &buf); err != nil {
		t.Fatal(err)
	}
	var arr []Entry
	if err := json.Unmarshal(buf.Bytes(), &arr); err != nil {
		t.Fatalf("output is not a valid JSON array: %v\n%s", err, buf.String())
	}
	if len(arr) != 2 {
		t.Errorf("array len = %d, want 2", len(arr))
	}
}

func TestExport_JSONRespectsFilter(t *testing.T) {
	l := seedLog(t, [3]string{"alice", "act", "r"}, [3]string{"bob", "act", "r"}, [3]string{"alice", "act", "r"})
	var buf bytes.Buffer
	if err := ExportJSON(l, QueryFilter{Actor: "alice"}, &buf); err != nil {
		t.Fatal(err)
	}
	var arr []Entry
	_ = json.Unmarshal(buf.Bytes(), &arr)
	if len(arr) != 2 {
		t.Errorf("filtered array len = %d, want 2 (alice only)", len(arr))
	}
	for _, e := range arr {
		if e.Actor != "alice" {
			t.Errorf("filter leaked actor %q", e.Actor)
		}
	}
}

func TestExport_SplunkEachLineValidJSON(t *testing.T) {
	l := seedLog(t, [3]string{"a", "act", "r"}, [3]string{"b", "act", "r"})
	var buf bytes.Buffer
	if err := ExportSplunk(l, QueryFilter{}, &buf); err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(buf.String())
	if len(lines) != 2 {
		t.Fatalf("Splunk lines = %d, want 2", len(lines))
	}
	for _, ln := range lines {
		var hec map[string]any
		if err := json.Unmarshal([]byte(ln), &hec); err != nil {
			t.Fatalf("Splunk line not valid JSON: %v\n%s", err, ln)
		}
		if _, ok := hec["time"]; !ok {
			t.Errorf("Splunk line missing 'time' field: %s", ln)
		}
		if _, ok := hec["event"]; !ok {
			t.Errorf("Splunk line missing 'event' field: %s", ln)
		}
	}
}

func TestExport_SyslogValidHeader(t *testing.T) {
	l := seedLog(t, [3]string{"a", "config.reload", "server"})
	var buf bytes.Buffer
	if err := ExportSyslog(l, QueryFilter{}, &buf); err != nil {
		t.Fatal(err)
	}
	lines := nonEmptyLines(buf.String())
	if len(lines) != 1 {
		t.Fatalf("syslog lines = %d, want 1", len(lines))
	}
	// RFC 5424: starts with "<PRI>VERSION " — here "<110>1 ".
	if !strings.HasPrefix(lines[0], "<110>1 ") {
		t.Errorf("syslog line missing RFC 5424 header: %s", lines[0])
	}
	if !strings.Contains(lines[0], "config.reload") {
		t.Errorf("syslog line missing action as MSGID: %s", lines[0])
	}
}

func TestExport_CSVHeaderRow(t *testing.T) {
	l := seedLog(t, [3]string{"a", "act", "r"})
	var buf bytes.Buffer
	if err := ExportCSV(l, QueryFilter{}, &buf); err != nil {
		t.Fatal(err)
	}
	rows, err := csv.NewReader(&buf).ReadAll()
	if err != nil {
		t.Fatalf("CSV not parseable: %v", err)
	}
	want := []string{"seq", "timestamp", "actor", "action", "resource", "detail", "hash"}
	if len(rows) == 0 || strings.Join(rows[0], ",") != strings.Join(want, ",") {
		t.Errorf("first row = %v, want header %v", rows[0], want)
	}
}

func TestExport_CSVRowCountMatchesFilter(t *testing.T) {
	l := seedLog(t,
		[3]string{"alice", "act", "r"},
		[3]string{"bob", "act", "r"},
		[3]string{"alice", "act", "r"},
		[3]string{"alice", "act", "r"},
	)
	var buf bytes.Buffer
	if err := ExportCSV(l, QueryFilter{Actor: "alice"}, &buf); err != nil {
		t.Fatal(err)
	}
	rows, _ := csv.NewReader(&buf).ReadAll()
	// header + 3 alice rows
	if len(rows) != 4 {
		t.Errorf("CSV rows = %d, want 4 (header + 3 alice)", len(rows))
	}
}

func nonEmptyLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if strings.TrimSpace(ln) != "" {
			out = append(out, ln)
		}
	}
	return out
}
