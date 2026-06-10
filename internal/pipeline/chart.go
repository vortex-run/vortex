package pipeline

import (
	"fmt"
	"math"
	"strings"
)

// ChartType enumerates supported chart kinds.
const (
	ChartBar  = "bar"
	ChartLine = "line"
	ChartPie  = "pie"
)

// ChartSpec configures a chart rendering.
type ChartSpec struct {
	Type     string // "bar"|"line"|"pie"
	Title    string
	LabelCol string // column for category labels / x-axis
	ValueCol string // numeric column for values
	Width    int    // default 640
	Height   int    // default 400
}

// chartColors is the categorical palette.
var chartColors = []string{
	"#4f46e5", "#06b6d4", "#10b981", "#f59e0b", "#ef4444",
	"#8b5cf6", "#ec4899", "#14b8a6", "#f97316", "#84cc16",
}

// ChartRenderer renders datasets to SVG (stdlib string building, no deps).
type ChartRenderer struct{}

// NewChartRenderer constructs a renderer.
func NewChartRenderer() *ChartRenderer { return &ChartRenderer{} }

// Render produces an SVG document string for ds per spec.
func (c *ChartRenderer) Render(ds *Dataset, spec ChartSpec) (string, error) {
	if spec.Width == 0 {
		spec.Width = 640
	}
	if spec.Height == 0 {
		spec.Height = 400
	}
	if !hasColumn(ds, spec.LabelCol) {
		return "", fmt.Errorf("pipeline: unknown label column %q", spec.LabelCol)
	}
	if !hasColumn(ds, spec.ValueCol) {
		return "", fmt.Errorf("pipeline: unknown value column %q", spec.ValueCol)
	}
	labels, values := extractSeries(ds, spec.LabelCol, spec.ValueCol)
	if len(values) == 0 {
		return "", fmt.Errorf("pipeline: no numeric data to chart")
	}

	switch spec.Type {
	case ChartBar, "":
		return renderBar(labels, values, spec), nil
	case ChartLine:
		return renderLine(labels, values, spec), nil
	case ChartPie:
		return renderPie(labels, values, spec), nil
	default:
		return "", fmt.Errorf("pipeline: unknown chart type %q", spec.Type)
	}
}

// extractSeries pulls (label, value) pairs from the dataset.
func extractSeries(ds *Dataset, labelCol, valueCol string) (labels []string, values []float64) {
	for _, row := range ds.Rows {
		v, ok := toFloat(row[valueCol])
		if !ok {
			continue
		}
		labels = append(labels, fmt.Sprintf("%v", row[labelCol]))
		values = append(values, v)
	}
	return labels, values
}

// --- SVG renderers ----------------------------------------------------------

const (
	padL = 50
	padR = 20
	padT = 40
	padB = 50
)

func svgHeader(spec ChartSpec) string {
	return fmt.Sprintf(
		`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" font-family="sans-serif">`+
			`<rect width="%d" height="%d" fill="#ffffff"/>`+
			`<text x="%d" y="24" text-anchor="middle" font-size="16" font-weight="bold" fill="#111">%s</text>`,
		spec.Width, spec.Height, spec.Width, spec.Height, spec.Width, spec.Height, spec.Width/2, escapeXML(spec.Title))
}

func renderBar(labels []string, values []float64, spec ChartSpec) string {
	var b strings.Builder
	b.WriteString(svgHeader(spec))
	plotW := spec.Width - padL - padR
	plotH := spec.Height - padT - padB
	maxV := maxFloat(values)
	if maxV <= 0 {
		maxV = 1
	}
	n := len(values)
	gap := 0.2
	bw := float64(plotW) / (float64(n) * (1 + gap))
	// Axis.
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#999"/>`, padL, padT+plotH, padL+plotW, padT+plotH)
	for i, v := range values {
		h := (v / maxV) * float64(plotH)
		x := float64(padL) + float64(i)*(bw*(1+gap)) + bw*gap/2
		y := float64(padT+plotH) - h
		color := chartColors[i%len(chartColors)]
		fmt.Fprintf(&b, `<rect x="%.1f" y="%.1f" width="%.1f" height="%.1f" fill="%s"/>`, x, y, bw, h, color)
		fmt.Fprintf(&b, `<text x="%.1f" y="%d" text-anchor="middle" font-size="10" fill="#444">%s</text>`,
			x+bw/2, padT+plotH+16, escapeXML(truncLabel(labels[i])))
	}
	b.WriteString("</svg>")
	return b.String()
}

func renderLine(labels []string, values []float64, spec ChartSpec) string {
	var b strings.Builder
	b.WriteString(svgHeader(spec))
	plotW := spec.Width - padL - padR
	plotH := spec.Height - padT - padB
	maxV := maxFloat(values)
	if maxV <= 0 {
		maxV = 1
	}
	n := len(values)
	fmt.Fprintf(&b, `<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#999"/>`, padL, padT+plotH, padL+plotW, padT+plotH)
	step := float64(plotW)
	if n > 1 {
		step = float64(plotW) / float64(n-1)
	}
	var pts strings.Builder
	for i, v := range values {
		x := float64(padL) + float64(i)*step
		y := float64(padT+plotH) - (v/maxV)*float64(plotH)
		fmt.Fprintf(&pts, "%.1f,%.1f ", x, y)
		fmt.Fprintf(&b, `<circle cx="%.1f" cy="%.1f" r="3" fill="%s"/>`, x, y, chartColors[0])
		if i%maxInt(1, n/10) == 0 {
			fmt.Fprintf(&b, `<text x="%.1f" y="%d" text-anchor="middle" font-size="10" fill="#444">%s</text>`,
				x, padT+plotH+16, escapeXML(truncLabel(labels[i])))
		}
	}
	fmt.Fprintf(&b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2"/>`, strings.TrimSpace(pts.String()), chartColors[0])
	b.WriteString("</svg>")
	return b.String()
}

func renderPie(labels []string, values []float64, spec ChartSpec) string {
	var b strings.Builder
	b.WriteString(svgHeader(spec))
	total := 0.0
	for _, v := range values {
		total += v
	}
	if total <= 0 {
		total = 1
	}
	cx := float64(spec.Width) / 2
	cy := float64(spec.Height)/2 + 10
	r := math.Min(float64(spec.Width), float64(spec.Height))/2 - padB
	angle := -math.Pi / 2 // start at top
	for i, v := range values {
		frac := v / total
		next := angle + frac*2*math.Pi
		x1 := cx + r*math.Cos(angle)
		y1 := cy + r*math.Sin(angle)
		x2 := cx + r*math.Cos(next)
		y2 := cy + r*math.Sin(next)
		large := 0
		if frac > 0.5 {
			large = 1
		}
		color := chartColors[i%len(chartColors)]
		fmt.Fprintf(&b, `<path d="M%.1f,%.1f L%.1f,%.1f A%.1f,%.1f 0 %d,1 %.1f,%.1f Z" fill="%s"/>`,
			cx, cy, x1, y1, r, r, large, x2, y2, color)
		// Legend swatch.
		ly := padT + i*16
		fmt.Fprintf(&b, `<rect x="%d" y="%d" width="10" height="10" fill="%s"/>`, spec.Width-130, ly, color)
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-size="10" fill="#444">%s (%.0f%%)</text>`,
			spec.Width-115, ly+9, escapeXML(truncLabel(labels[i])), frac*100)
		angle = next
	}
	b.WriteString("</svg>")
	return b.String()
}

// --- helpers ----------------------------------------------------------------

func maxFloat(v []float64) float64 {
	m := math.Inf(-1)
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	if math.IsInf(m, -1) {
		return 0
	}
	return m
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// truncLabel shortens long labels for axis display.
func truncLabel(s string) string {
	if len(s) > 12 {
		return s[:11] + "…"
	}
	return s
}

// escapeXML escapes the few characters unsafe in SVG text.
func escapeXML(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}
