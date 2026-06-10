package pipeline

import (
	"fmt"
	"sort"
	"strings"
)

// Processor transforms datasets (filter/sort/aggregate/group-by). It is
// stateless; methods return new datasets and never mutate the input.
type Processor struct{}

// NewProcessor constructs a processor.
func NewProcessor() *Processor { return &Processor{} }

// Filter returns rows where column op value holds. op ∈ {==,!=,>,<,>=,<=,
// contains}. Numeric comparisons use float64 when both sides are numeric.
func (p *Processor) Filter(ds *Dataset, column, op string, value any) (*Dataset, error) {
	if !hasColumn(ds, column) {
		return nil, fmt.Errorf("pipeline: unknown column %q", column)
	}
	out := &Dataset{Columns: ds.Columns, Source: ds.Source, Rows: []map[string]any{}}
	for _, row := range ds.Rows {
		ok, err := compare(row[column], op, value)
		if err != nil {
			return nil, err
		}
		if ok {
			out.Rows = append(out.Rows, row)
		}
	}
	return out, nil
}

// Sort returns rows sorted by column. ascending controls direction. Numeric
// columns sort numerically; others lexicographically.
func (p *Processor) Sort(ds *Dataset, column string, ascending bool) (*Dataset, error) {
	if !hasColumn(ds, column) {
		return nil, fmt.Errorf("pipeline: unknown column %q", column)
	}
	rows := make([]map[string]any, len(ds.Rows))
	copy(rows, ds.Rows)
	sort.SliceStable(rows, func(i, j int) bool {
		less := lessValue(rows[i][column], rows[j][column])
		if ascending {
			return less
		}
		return !less && !equalValue(rows[i][column], rows[j][column])
	})
	return &Dataset{Columns: ds.Columns, Source: ds.Source, Rows: rows}, nil
}

// Aggregate computes a single aggregate over a numeric column. fn ∈ {sum,avg,
// min,max,count}.
func (p *Processor) Aggregate(ds *Dataset, column, fn string) (float64, error) {
	if fn != "count" && !hasColumn(ds, column) {
		return 0, fmt.Errorf("pipeline: unknown column %q", column)
	}
	if fn == "count" {
		return float64(len(ds.Rows)), nil
	}
	var nums []float64
	for _, row := range ds.Rows {
		if f, ok := toFloat(row[column]); ok {
			nums = append(nums, f)
		}
	}
	return reduce(nums, fn)
}

// GroupBy groups rows by groupCol and aggregates aggCol with fn, returning a
// dataset of {groupCol, fn(aggCol)} rows.
func (p *Processor) GroupBy(ds *Dataset, groupCol, aggCol, fn string) (*Dataset, error) {
	if !hasColumn(ds, groupCol) {
		return nil, fmt.Errorf("pipeline: unknown group column %q", groupCol)
	}
	if fn != "count" && !hasColumn(ds, aggCol) {
		return nil, fmt.Errorf("pipeline: unknown aggregate column %q", aggCol)
	}
	groups := map[string][]float64{}
	var order []string
	for _, row := range ds.Rows {
		key := fmt.Sprintf("%v", row[groupCol])
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		if fn == "count" {
			groups[key] = append(groups[key], 1)
		} else if f, ok := toFloat(row[aggCol]); ok {
			groups[key] = append(groups[key], f)
		}
	}
	resultCol := fn + "(" + aggCol + ")"
	if fn == "count" {
		resultCol = "count"
	}
	out := &Dataset{Columns: []string{groupCol, resultCol}, Source: ds.Source, Rows: []map[string]any{}}
	for _, key := range order {
		val, err := reduce(groups[key], fn)
		if err != nil {
			return nil, err
		}
		if fn == "count" {
			val = float64(len(groups[key]))
		}
		out.Rows = append(out.Rows, map[string]any{groupCol: key, resultCol: val})
	}
	return out, nil
}

// Limit returns the first n rows.
func (p *Processor) Limit(ds *Dataset, n int) *Dataset {
	if n < 0 {
		n = 0
	}
	if n > len(ds.Rows) {
		n = len(ds.Rows)
	}
	rows := make([]map[string]any, n)
	copy(rows, ds.Rows[:n])
	return &Dataset{Columns: ds.Columns, Source: ds.Source, Rows: rows}
}

// --- helpers ----------------------------------------------------------------

func hasColumn(ds *Dataset, col string) bool {
	for _, c := range ds.Columns {
		if c == col {
			return true
		}
	}
	return false
}

// reduce applies an aggregate function to a slice of numbers.
func reduce(nums []float64, fn string) (float64, error) {
	if len(nums) == 0 {
		if fn == "count" || fn == "sum" {
			return 0, nil
		}
		return 0, nil
	}
	switch fn {
	case "sum", "count":
		var s float64
		for _, n := range nums {
			s += n
		}
		return s, nil
	case "avg", "mean":
		var s float64
		for _, n := range nums {
			s += n
		}
		return s / float64(len(nums)), nil
	case "min":
		m := nums[0]
		for _, n := range nums[1:] {
			if n < m {
				m = n
			}
		}
		return m, nil
	case "max":
		m := nums[0]
		for _, n := range nums[1:] {
			if n > m {
				m = n
			}
		}
		return m, nil
	default:
		return 0, fmt.Errorf("pipeline: unknown aggregate %q", fn)
	}
}

// toFloat converts a cell to float64 if numeric.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	default:
		return 0, false
	}
}

// compare evaluates "cell op value".
func compare(cell any, op string, value any) (bool, error) {
	cf, cIsNum := toFloat(cell)
	vf, vIsNum := toFloat(value)
	bothNum := cIsNum && vIsNum

	switch op {
	case "==":
		if bothNum {
			return cf == vf, nil
		}
		return fmt.Sprintf("%v", cell) == fmt.Sprintf("%v", value), nil
	case "!=":
		if bothNum {
			return cf != vf, nil
		}
		return fmt.Sprintf("%v", cell) != fmt.Sprintf("%v", value), nil
	case ">":
		return cf > vf, nil
	case "<":
		return cf < vf, nil
	case ">=":
		return cf >= vf, nil
	case "<=":
		return cf <= vf, nil
	case "contains":
		return strings.Contains(fmt.Sprintf("%v", cell), fmt.Sprintf("%v", value)), nil
	default:
		return false, fmt.Errorf("pipeline: unknown operator %q", op)
	}
}

// lessValue compares two cells (numeric if both numeric, else string).
func lessValue(a, b any) bool {
	af, aOK := toFloat(a)
	bf, bOK := toFloat(b)
	if aOK && bOK {
		return af < bf
	}
	return fmt.Sprintf("%v", a) < fmt.Sprintf("%v", b)
}

func equalValue(a, b any) bool {
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}
