package pipeline

import (
	"strings"
	"testing"
)

func chartData() *Dataset {
	return &Dataset{
		Columns: []string{"dept", "total"},
		Rows: []map[string]any{
			{"dept": "eng", "total": float64(180)},
			{"dept": "sales", "total": float64(160)},
			{"dept": "ops", "total": float64(90)},
		},
	}
}

func TestRender_BarChart(t *testing.T) {
	svg, err := NewChartRenderer().Render(chartData(), ChartSpec{
		Type: ChartBar, Title: "Spend by Dept", LabelCol: "dept", ValueCol: "total",
	})
	if err != nil {
		t.Fatalf("Render bar: %v", err)
	}
	if !strings.HasPrefix(svg, "<svg") || !strings.HasSuffix(svg, "</svg>") {
		t.Error("output is not a complete SVG document")
	}
	if !strings.Contains(svg, "<rect") {
		t.Error("bar chart should contain rects")
	}
	if !strings.Contains(svg, "Spend by Dept") {
		t.Error("title missing")
	}
	// One bar per row (3) plus the background rect.
	if n := strings.Count(svg, "<rect"); n < 4 {
		t.Errorf("expected >=4 rects (bg + 3 bars), got %d", n)
	}
}

func TestRender_LineChart(t *testing.T) {
	svg, err := NewChartRenderer().Render(chartData(), ChartSpec{
		Type: ChartLine, Title: "Trend", LabelCol: "dept", ValueCol: "total",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svg, "<polyline") {
		t.Error("line chart should contain a polyline")
	}
	if strings.Count(svg, "<circle") != 3 {
		t.Errorf("expected 3 points, got %d", strings.Count(svg, "<circle"))
	}
}

func TestRender_PieChart(t *testing.T) {
	svg, err := NewChartRenderer().Render(chartData(), ChartSpec{
		Type: ChartPie, Title: "Share", LabelCol: "dept", ValueCol: "total",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(svg, "<path") != 3 {
		t.Errorf("pie should have 3 slices, got %d", strings.Count(svg, "<path"))
	}
	// Legend shows a percentage.
	if !strings.Contains(svg, "%)") {
		t.Error("pie legend should show percentages")
	}
}

func TestRender_DefaultsToBar(t *testing.T) {
	svg, err := NewChartRenderer().Render(chartData(), ChartSpec{LabelCol: "dept", ValueCol: "total"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svg, "<rect") {
		t.Error("empty type should default to a bar chart")
	}
}

func TestRender_UnknownColumns(t *testing.T) {
	r := NewChartRenderer()
	if _, err := r.Render(chartData(), ChartSpec{LabelCol: "nope", ValueCol: "total"}); err == nil {
		t.Error("unknown label column should error")
	}
	if _, err := r.Render(chartData(), ChartSpec{LabelCol: "dept", ValueCol: "nope"}); err == nil {
		t.Error("unknown value column should error")
	}
}

func TestRender_UnknownType(t *testing.T) {
	if _, err := NewChartRenderer().Render(chartData(), ChartSpec{Type: "scatter", LabelCol: "dept", ValueCol: "total"}); err == nil {
		t.Error("unknown chart type should error")
	}
}

func TestRender_NoNumericData(t *testing.T) {
	ds := &Dataset{Columns: []string{"a", "b"}, Rows: []map[string]any{{"a": "x", "b": "y"}}}
	if _, err := NewChartRenderer().Render(ds, ChartSpec{LabelCol: "a", ValueCol: "b"}); err == nil {
		t.Error("no numeric data should error")
	}
}

func TestRender_EscapesTitle(t *testing.T) {
	svg, err := NewChartRenderer().Render(chartData(), ChartSpec{
		Title: "A & B <test>", LabelCol: "dept", ValueCol: "total",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(svg, "<test>") || !strings.Contains(svg, "&amp;") {
		t.Error("title should be XML-escaped")
	}
}
