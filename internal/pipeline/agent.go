package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/vortex-run/vortex/internal/agents"
)

// Notifier delivers pipeline results. Satisfied by *messaging.Router via an
// adapter (keeps pipeline decoupled from messaging). Nil-safe at call sites.
type Notifier interface {
	Notify(ctx context.Context, title, body string) error
	NotifyFile(ctx context.Context, filename string, data []byte, caption string) error
}

// planSystemPrompt asks the model to translate a request into a transform plan.
const planSystemPrompt = `You translate a data-analysis request into a JSON plan. Given the available columns, respond with ONLY this JSON:
{"steps":[{"op":"filter","column":"c","operator":">","value":10},{"op":"groupby","group":"c","agg":"v","fn":"sum"},{"op":"sort","column":"c","ascending":false},{"op":"limit","n":10}],"chart":{"type":"bar","title":"T","label":"c","value":"v"}}
Use only the listed columns. Omit steps you don't need. Return only JSON.`

// Step is one transform step in a plan.
type Step struct {
	Op        string `json:"op"`       // filter|sort|groupby|limit|aggregate
	Column    string `json:"column"`   // filter/sort column
	Operator  string `json:"operator"` // filter operator
	Value     any    `json:"value"`    // filter value
	Ascending bool   `json:"ascending"`
	Group     string `json:"group"` // groupby group column
	Agg       string `json:"agg"`   // groupby/aggregate value column
	Fn        string `json:"fn"`    // aggregate/groupby function
	N         int    `json:"n"`     // limit
}

// Plan is a parsed analysis plan.
type Plan struct {
	Steps []Step    `json:"steps"`
	Chart ChartSpec `json:"-"`
	chart planChart // raw chart for JSON unmarshal
}

// planChart is the JSON shape of the plan's chart.
type planChart struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	Label string `json:"label"`
	Value string `json:"value"`
}

// PipelineResult is the outcome of an analysis run.
//
//nolint:revive // PipelineResult name is descriptive
type PipelineResult struct {
	Summary   string   `json:"summary"`
	DataPath  string   `json:"data_path"`
	ChartPath string   `json:"chart_path"`
	Rows      int      `json:"rows"`
	Columns   []string `json:"columns"`
}

// DataPipelineAgent orchestrates read → process → chart → report.
//
//nolint:revive // DataPipelineAgent name is mandated by the M17 spec
type DataPipelineAgent struct {
	gateway   agents.AIGateway
	notifier  Notifier
	reader    *Reader
	processor *Processor
	charts    *ChartRenderer
	dir       string // workDir/pipeline
}

// NewDataPipelineAgent constructs the agent saving under workDir/pipeline.
func NewDataPipelineAgent(gateway agents.AIGateway, notifier Notifier, workDir string) *DataPipelineAgent {
	return &DataPipelineAgent{
		gateway: gateway, notifier: notifier,
		reader: NewReader(), processor: NewProcessor(), charts: NewChartRenderer(),
		dir: filepath.Join(workDir, "pipeline"),
	}
}

// Analyze loads a dataset from source (URL/CSV/JSON bytes), asks the model for a
// plan, applies it, renders a chart, saves outputs, and notifies.
func (a *DataPipelineAgent) Analyze(ctx context.Context, source string, data []byte, request string, progressFn func(string)) (*PipelineResult, error) {
	emit := func(s string) {
		if progressFn != nil {
			progressFn(s)
		}
	}

	emit("📥 Loading data…")
	ds, err := a.load(ctx, source, data)
	if err != nil {
		return nil, err
	}
	if ds.RowCount() == 0 {
		return nil, fmt.Errorf("pipeline: dataset is empty")
	}

	emit("🧠 Planning analysis…")
	plan, err := a.plan(ctx, ds, request)
	if err != nil {
		return nil, err
	}

	emit("⚙️ Transforming…")
	result, err := a.applyPlan(ds, plan)
	if err != nil {
		return nil, err
	}

	emit("📊 Rendering chart…")
	out, err := a.saveOutputs(ctx, result, plan, request)
	if err != nil {
		return nil, err
	}
	emit("✅ Analysis complete")
	return out, nil
}

// load reads from inline data or a URL.
func (a *DataPipelineAgent) load(ctx context.Context, source string, data []byte) (*Dataset, error) {
	if len(data) > 0 {
		if strings.HasSuffix(source, ".json") {
			return a.reader.ReadJSON(data)
		}
		return a.reader.ReadCSV(data)
	}
	if source == "" {
		return nil, fmt.Errorf("pipeline: no data source provided")
	}
	return a.reader.ReadURL(ctx, source)
}

// plan asks the model for a transform plan (falling back to a no-op plan with a
// best-effort chart when AI is unavailable or returns nothing usable).
func (a *DataPipelineAgent) plan(ctx context.Context, ds *Dataset, request string) (*Plan, error) {
	fallback := &Plan{Steps: nil, chart: planChart{Type: ChartBar}}
	a.defaultChart(ds, &fallback.chart)
	fallback.Chart = a.toChartSpec(fallback.chart, request)

	if a.gateway == nil {
		return fallback, nil
	}
	prompt := fmt.Sprintf("Columns: %s\nRequest: %s", strings.Join(ds.Columns, ", "), request)
	reply, err := a.gateway.Complete(ctx, prompt, planSystemPrompt)
	if err != nil {
		return fallback, nil // degrade gracefully
	}
	plan := parsePlan(reply)
	if plan == nil {
		return fallback, nil
	}
	if plan.chart.Label == "" || plan.chart.Value == "" {
		a.defaultChart(ds, &plan.chart)
	}
	plan.Chart = a.toChartSpec(plan.chart, request)
	return plan, nil
}

// applyPlan runs the plan's steps in order.
func (a *DataPipelineAgent) applyPlan(ds *Dataset, plan *Plan) (*Dataset, error) {
	cur := ds
	for _, step := range plan.Steps {
		var err error
		switch step.Op {
		case "filter":
			cur, err = a.processor.Filter(cur, step.Column, step.Operator, step.Value)
		case "sort":
			cur, err = a.processor.Sort(cur, step.Column, step.Ascending)
		case "groupby":
			cur, err = a.processor.GroupBy(cur, step.Group, step.Agg, step.Fn)
		case "limit":
			cur = a.processor.Limit(cur, step.N)
		case "aggregate":
			// Aggregate collapses to a single value; represent as a 1-row dataset.
			v, aerr := a.processor.Aggregate(cur, step.Agg, step.Fn)
			if aerr != nil {
				return nil, aerr
			}
			col := step.Fn + "(" + step.Agg + ")"
			cur = &Dataset{Columns: []string{col}, Source: cur.Source, Rows: []map[string]any{{col: v}}}
		default:
			err = fmt.Errorf("pipeline: unknown step op %q", step.Op)
		}
		if err != nil {
			return nil, err
		}
	}
	return cur, nil
}

// saveOutputs writes the processed data (JSON) + chart (SVG) and notifies.
func (a *DataPipelineAgent) saveOutputs(ctx context.Context, ds *Dataset, plan *Plan, request string) (*PipelineResult, error) {
	if err := os.MkdirAll(a.dir, 0o755); err != nil { //nolint:gosec // user work dir
		return nil, err
	}
	ts := time.Now().Format("20060102-150405")
	base := slugify(request) + "-" + ts

	dataPath := filepath.Join(a.dir, base+".json")
	dataBytes, _ := json.MarshalIndent(ds.Rows, "", "  ")
	if err := os.WriteFile(dataPath, dataBytes, 0o644); err != nil { //nolint:gosec // user output
		return nil, err
	}

	result := &PipelineResult{
		Summary:  fmt.Sprintf("Produced %d result rows across %d columns.", ds.RowCount(), len(ds.Columns)),
		DataPath: dataPath, Rows: ds.RowCount(), Columns: ds.Columns,
	}

	// Chart (best-effort: skip if columns aren't chartable).
	if svg, err := a.charts.Render(ds, plan.Chart); err == nil {
		chartPath := filepath.Join(a.dir, base+".svg")
		if werr := os.WriteFile(chartPath, []byte(svg), 0o644); werr == nil { //nolint:gosec // user output
			result.ChartPath = chartPath
		}
	}

	a.notify(ctx, request, result)
	return result, nil
}

// notify sends the result (and chart file) to the notifier if configured.
func (a *DataPipelineAgent) notify(ctx context.Context, request string, r *PipelineResult) {
	if a.notifier == nil {
		return
	}
	body := fmt.Sprintf("📊 Analysis: %s\nRows: %d\nSaved: %s", request, r.Rows, r.DataPath)
	_ = a.notifier.Notify(ctx, "VORTEX Data Pipeline", body)
	if r.ChartPath != "" {
		if data, err := os.ReadFile(r.ChartPath); err == nil { //nolint:gosec // own output
			_ = a.notifier.NotifyFile(ctx, filepath.Base(r.ChartPath), data, "Chart: "+request)
		}
	}
}

// Scheduler returns a new scheduler for recurring pipeline jobs.
func (a *DataPipelineAgent) Scheduler() *Scheduler { return NewScheduler() }

// --- helpers ----------------------------------------------------------------

// defaultChart picks a label (first non-numeric column) + value (first numeric).
func (a *DataPipelineAgent) defaultChart(ds *Dataset, c *planChart) {
	if c.Type == "" {
		c.Type = ChartBar
	}
	if len(ds.Rows) == 0 {
		return
	}
	for _, col := range ds.Columns {
		if _, isNum := toFloat(ds.Rows[0][col]); isNum {
			if c.Value == "" {
				c.Value = col
			}
		} else if c.Label == "" {
			c.Label = col
		}
	}
	// Fallbacks if all columns were numeric or all strings.
	if c.Label == "" && len(ds.Columns) > 0 {
		c.Label = ds.Columns[0]
	}
	if c.Value == "" && len(ds.Columns) > 1 {
		c.Value = ds.Columns[1]
	}
}

// toChartSpec converts a planChart into a ChartSpec.
func (a *DataPipelineAgent) toChartSpec(c planChart, request string) ChartSpec {
	title := c.Title
	if title == "" {
		title = request
	}
	return ChartSpec{Type: c.Type, Title: title, LabelCol: c.Label, ValueCol: c.Value}
}

// parsePlan extracts a Plan from a model reply (tolerant of surrounding prose).
func parsePlan(reply string) *Plan {
	js := extractJSON(reply)
	if js == "" {
		return nil
	}
	var raw struct {
		Steps []Step    `json:"steps"`
		Chart planChart `json:"chart"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil {
		return nil
	}
	return &Plan{Steps: raw.Steps, chart: raw.Chart}
}

// extractJSON returns the first {...} object in s.
func extractJSON(s string) string {
	start := strings.IndexByte(s, '{')
	end := strings.LastIndexByte(s, '}')
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return ""
}

// slugRe matches non-slug characters.
var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify makes a filename-safe slug from a request.
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 50 {
		s = s[:50]
	}
	if s == "" {
		s = "analysis"
	}
	return s
}
