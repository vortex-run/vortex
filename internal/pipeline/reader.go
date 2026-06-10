// Package pipeline implements VORTEX's data pipeline agent (build plan M17):
// reading tabular data (CSV/JSON), transforming it (filter/aggregate/sort),
// rendering charts (SVG), and running scheduled jobs. It is stdlib-only —
// encoding/csv + encoding/json for I/O and hand-rolled SVG for charts.
//
// This file implements the data reader.
package pipeline

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

// maxDataBytes caps how much a remote/inline source may provide (16 MiB).
const maxDataBytes = 16 << 20

// Dataset is tabular data: named columns + row records.
type Dataset struct {
	Columns []string         `json:"columns"`
	Rows    []map[string]any `json:"rows"`
	Source  string           `json:"source"`
}

// RowCount returns the number of rows.
func (d *Dataset) RowCount() int { return len(d.Rows) }

// Reader loads datasets from CSV/JSON bytes or URLs.
type Reader struct {
	client *http.Client
}

// NewReader constructs a reader with a 20s HTTP timeout.
func NewReader() *Reader {
	return &Reader{client: &http.Client{Timeout: 20 * time.Second}}
}

// ReadCSV parses CSV bytes into a Dataset. The first row is the header.
func (r *Reader) ReadCSV(data []byte) (*Dataset, error) {
	cr := csv.NewReader(bytes.NewReader(data))
	cr.FieldsPerRecord = -1 // tolerate ragged rows
	records, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("pipeline: parsing CSV: %w", err)
	}
	if len(records) == 0 {
		return &Dataset{Columns: []string{}, Rows: []map[string]any{}, Source: "csv"}, nil
	}
	header := records[0]
	ds := &Dataset{Columns: header, Source: "csv", Rows: make([]map[string]any, 0, len(records)-1)}
	for _, rec := range records[1:] {
		row := make(map[string]any, len(header))
		for i, col := range header {
			if i < len(rec) {
				row[col] = coerce(rec[i])
			} else {
				row[col] = nil
			}
		}
		ds.Rows = append(ds.Rows, row)
	}
	return ds, nil
}

// ReadJSON parses JSON bytes into a Dataset. Accepts an array of objects or an
// object with a "data"/"rows" array.
func (r *Reader) ReadJSON(data []byte) (*Dataset, error) {
	var arr []map[string]any
	if err := json.Unmarshal(data, &arr); err != nil {
		// Try an envelope object.
		var env map[string]json.RawMessage
		if err2 := json.Unmarshal(data, &env); err2 != nil {
			return nil, fmt.Errorf("pipeline: parsing JSON: %w", err)
		}
		for _, key := range []string{"data", "rows", "results", "items"} {
			if raw, ok := env[key]; ok {
				if err3 := json.Unmarshal(raw, &arr); err3 == nil {
					break
				}
			}
		}
		if arr == nil {
			return nil, fmt.Errorf("pipeline: JSON is not an array of objects")
		}
	}
	return datasetFromMaps(arr, "json"), nil
}

// ReadURL fetches a URL and parses it as CSV or JSON based on the content type
// or the URL suffix.
func (r *Reader) ReadURL(ctx context.Context, url string) (*Dataset, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("pipeline: fetch %s returned %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDataBytes))
	if err != nil {
		return nil, err
	}
	ct := resp.Header.Get("Content-Type")
	ds, err := r.parse(data, ct, url)
	if err != nil {
		return nil, err
	}
	ds.Source = url
	return ds, nil
}

// parse picks CSV or JSON based on content type / URL suffix / sniffing.
func (r *Reader) parse(data []byte, contentType, url string) (*Dataset, error) {
	switch {
	case strings.Contains(contentType, "json") || strings.HasSuffix(url, ".json"):
		return r.ReadJSON(data)
	case strings.Contains(contentType, "csv") || strings.HasSuffix(url, ".csv"):
		return r.ReadCSV(data)
	default:
		// Sniff: a leading '[' or '{' is JSON, else CSV.
		trimmed := bytes.TrimSpace(data)
		if len(trimmed) > 0 && (trimmed[0] == '[' || trimmed[0] == '{') {
			return r.ReadJSON(data)
		}
		return r.ReadCSV(data)
	}
}

// datasetFromMaps builds a Dataset from records, deriving an ordered column set
// from the first record (plus any new keys seen later, appended).
func datasetFromMaps(rows []map[string]any, source string) *Dataset {
	ds := &Dataset{Rows: rows, Source: source}
	seen := map[string]bool{}
	for _, row := range rows {
		// Stable-ish ordering: sort keys of each row, add unseen ones.
		keys := sortedKeys(row)
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				ds.Columns = append(ds.Columns, k)
			}
		}
	}
	if ds.Columns == nil {
		ds.Columns = []string{}
	}
	if ds.Rows == nil {
		ds.Rows = []map[string]any{}
	}
	return ds
}

// sortedKeys returns a map's keys in sorted order (deterministic columns).
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// coerce converts a CSV string cell to a number when it parses, else keeps the
// string. Empty stays empty string.
func coerce(s string) any {
	if s == "" {
		return ""
	}
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return float64(i) // unify numerics as float64 for downstream math
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f
	}
	return s
}
