package pipeline

import "testing"

func sampleDataset() *Dataset {
	return &Dataset{
		Columns: []string{"name", "dept", "salary"},
		Rows: []map[string]any{
			{"name": "Alice", "dept": "eng", "salary": float64(100)},
			{"name": "Bob", "dept": "eng", "salary": float64(80)},
			{"name": "Carol", "dept": "sales", "salary": float64(90)},
			{"name": "Dave", "dept": "sales", "salary": float64(70)},
		},
	}
}

func TestFilter_Numeric(t *testing.T) {
	ds := sampleDataset()
	out, err := NewProcessor().Filter(ds, "salary", ">", float64(85))
	if err != nil {
		t.Fatal(err)
	}
	if out.RowCount() != 2 {
		t.Errorf("salary>85 → %d rows, want 2", out.RowCount())
	}
	// Input is not mutated.
	if ds.RowCount() != 4 {
		t.Error("Filter mutated the input dataset")
	}
}

func TestFilter_StringEquals(t *testing.T) {
	out, err := NewProcessor().Filter(sampleDataset(), "dept", "==", "eng")
	if err != nil {
		t.Fatal(err)
	}
	if out.RowCount() != 2 {
		t.Errorf("dept==eng → %d rows, want 2", out.RowCount())
	}
}

func TestFilter_Contains(t *testing.T) {
	out, err := NewProcessor().Filter(sampleDataset(), "name", "contains", "a")
	if err != nil {
		t.Fatal(err)
	}
	// Carol, Dave contain lowercase 'a'.
	if out.RowCount() != 2 {
		t.Errorf("name contains a → %d rows, want 2", out.RowCount())
	}
}

func TestFilter_UnknownColumn(t *testing.T) {
	if _, err := NewProcessor().Filter(sampleDataset(), "nope", "==", "x"); err == nil {
		t.Error("unknown column should error")
	}
}

func TestFilter_UnknownOperator(t *testing.T) {
	if _, err := NewProcessor().Filter(sampleDataset(), "salary", "~=", 1); err == nil {
		t.Error("unknown operator should error")
	}
}

func TestSort_NumericDescending(t *testing.T) {
	out, err := NewProcessor().Sort(sampleDataset(), "salary", false)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rows[0]["name"] != "Alice" || out.Rows[3]["name"] != "Dave" {
		t.Errorf("desc sort order wrong: %v … %v", out.Rows[0]["name"], out.Rows[3]["name"])
	}
}

func TestSort_StringAscending(t *testing.T) {
	out, err := NewProcessor().Sort(sampleDataset(), "name", true)
	if err != nil {
		t.Fatal(err)
	}
	if out.Rows[0]["name"] != "Alice" || out.Rows[1]["name"] != "Bob" {
		t.Errorf("asc string sort wrong: %v, %v", out.Rows[0]["name"], out.Rows[1]["name"])
	}
}

func TestAggregate(t *testing.T) {
	p := NewProcessor()
	ds := sampleDataset()
	cases := map[string]float64{"sum": 340, "avg": 85, "min": 70, "max": 100, "count": 4}
	for fn, want := range cases {
		got, err := p.Aggregate(ds, "salary", fn)
		if err != nil {
			t.Fatalf("%s: %v", fn, err)
		}
		if got != want {
			t.Errorf("Aggregate(%s) = %v, want %v", fn, got, want)
		}
	}
}

func TestAggregate_UnknownFn(t *testing.T) {
	if _, err := NewProcessor().Aggregate(sampleDataset(), "salary", "median"); err == nil {
		t.Error("unknown aggregate should error")
	}
}

func TestGroupBy_SumBySalary(t *testing.T) {
	out, err := NewProcessor().GroupBy(sampleDataset(), "dept", "salary", "sum")
	if err != nil {
		t.Fatal(err)
	}
	if out.RowCount() != 2 {
		t.Fatalf("groups = %d, want 2", out.RowCount())
	}
	// First-seen order: eng then sales.
	if out.Rows[0]["dept"] != "eng" || out.Rows[0]["sum(salary)"] != float64(180) {
		t.Errorf("eng group = %+v", out.Rows[0])
	}
	if out.Rows[1]["sum(salary)"] != float64(160) {
		t.Errorf("sales sum = %v, want 160", out.Rows[1]["sum(salary)"])
	}
}

func TestGroupBy_Count(t *testing.T) {
	out, err := NewProcessor().GroupBy(sampleDataset(), "dept", "", "count")
	if err != nil {
		t.Fatal(err)
	}
	if out.Rows[0]["count"] != float64(2) {
		t.Errorf("eng count = %v, want 2", out.Rows[0]["count"])
	}
}

func TestLimit(t *testing.T) {
	out := NewProcessor().Limit(sampleDataset(), 2)
	if out.RowCount() != 2 {
		t.Errorf("Limit(2) → %d rows", out.RowCount())
	}
	// Over-limit is clamped.
	if NewProcessor().Limit(sampleDataset(), 99).RowCount() != 4 {
		t.Error("Limit beyond length should clamp")
	}
}

func TestChaining(t *testing.T) {
	p := NewProcessor()
	ds := sampleDataset()
	eng, _ := p.Filter(ds, "dept", "==", "eng")
	sorted, _ := p.Sort(eng, "salary", false)
	top := p.Limit(sorted, 1)
	if top.RowCount() != 1 || top.Rows[0]["name"] != "Alice" {
		t.Errorf("chained filter→sort→limit = %+v", top.Rows)
	}
}
