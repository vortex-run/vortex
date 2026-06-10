package pipeline

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestReadCSV_ParsesHeaderAndRows(t *testing.T) {
	csv := "name,age,city\nAlice,30,NYC\nBob,25,LA\n"
	ds, err := NewReader().ReadCSV([]byte(csv))
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if len(ds.Columns) != 3 || ds.Columns[0] != "name" {
		t.Errorf("columns = %v", ds.Columns)
	}
	if ds.RowCount() != 2 {
		t.Fatalf("rows = %d, want 2", ds.RowCount())
	}
	if ds.Rows[0]["name"] != "Alice" {
		t.Errorf("row0 name = %v", ds.Rows[0]["name"])
	}
	// Numeric coercion: age is a float64.
	if age, ok := ds.Rows[0]["age"].(float64); !ok || age != 30 {
		t.Errorf("row0 age = %v (%T), want 30 float64", ds.Rows[0]["age"], ds.Rows[0]["age"])
	}
}

func TestReadCSV_RaggedRows(t *testing.T) {
	csv := "a,b,c\n1,2\n3,4,5\n"
	ds, err := NewReader().ReadCSV([]byte(csv))
	if err != nil {
		t.Fatalf("ReadCSV: %v", err)
	}
	if ds.RowCount() != 2 {
		t.Fatalf("rows = %d", ds.RowCount())
	}
	// Missing cell → nil.
	if ds.Rows[0]["c"] != nil {
		t.Errorf("row0 c = %v, want nil", ds.Rows[0]["c"])
	}
}

func TestReadCSV_Empty(t *testing.T) {
	ds, err := NewReader().ReadCSV([]byte(""))
	if err != nil {
		t.Fatal(err)
	}
	if ds.RowCount() != 0 || len(ds.Columns) != 0 {
		t.Errorf("empty CSV should yield no rows/cols, got %+v", ds)
	}
}

func TestReadJSON_ArrayOfObjects(t *testing.T) {
	js := `[{"name":"Alice","age":30},{"name":"Bob","age":25}]`
	ds, err := NewReader().ReadJSON([]byte(js))
	if err != nil {
		t.Fatalf("ReadJSON: %v", err)
	}
	if ds.RowCount() != 2 {
		t.Fatalf("rows = %d", ds.RowCount())
	}
	if len(ds.Columns) != 2 {
		t.Errorf("columns = %v", ds.Columns)
	}
	if ds.Rows[1]["name"] != "Bob" {
		t.Errorf("row1 name = %v", ds.Rows[1]["name"])
	}
}

func TestReadJSON_Envelope(t *testing.T) {
	js := `{"status":"ok","data":[{"x":1},{"x":2}]}`
	ds, err := NewReader().ReadJSON([]byte(js))
	if err != nil {
		t.Fatalf("ReadJSON envelope: %v", err)
	}
	if ds.RowCount() != 2 {
		t.Errorf("envelope rows = %d, want 2", ds.RowCount())
	}
}

func TestReadJSON_Invalid(t *testing.T) {
	if _, err := NewReader().ReadJSON([]byte(`"just a string"`)); err == nil {
		t.Error("non-array/non-envelope JSON should error")
	}
}

func TestReadURL_CSV(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/csv")
		_, _ = io.WriteString(w, "x,y\n1,2\n3,4\n")
	}))
	t.Cleanup(srv.Close)
	ds, err := NewReader().ReadURL(context.Background(), srv.URL)
	if err != nil {
		t.Fatalf("ReadURL: %v", err)
	}
	if ds.RowCount() != 2 || ds.Source != srv.URL {
		t.Errorf("url csv = %+v", ds)
	}
}

func TestReadURL_JSONByContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `[{"a":1},{"a":2}]`)
	}))
	t.Cleanup(srv.Close)
	ds, err := NewReader().ReadURL(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ds.RowCount() != 2 {
		t.Errorf("url json rows = %d", ds.RowCount())
	}
}

func TestReadURL_SniffsJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// No content type, leading '[' → JSON.
		_, _ = io.WriteString(w, `[{"a":1}]`)
	}))
	t.Cleanup(srv.Close)
	ds, err := NewReader().ReadURL(context.Background(), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if ds.RowCount() != 1 {
		t.Errorf("sniffed json rows = %d", ds.RowCount())
	}
}

func TestReadURL_Non200Errors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	if _, err := NewReader().ReadURL(context.Background(), srv.URL); err == nil {
		t.Error("a 500 should error")
	}
}

func TestCoerce(t *testing.T) {
	if v := coerce("42"); v != float64(42) {
		t.Errorf("coerce(42) = %v", v)
	}
	if v := coerce("3.14"); v != 3.14 {
		t.Errorf("coerce(3.14) = %v", v)
	}
	if v := coerce("hello"); v != "hello" {
		t.Errorf("coerce(hello) = %v", v)
	}
	if v := coerce(""); v != "" {
		t.Errorf("coerce empty = %v", v)
	}
}
