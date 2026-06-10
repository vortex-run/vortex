package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// stubGateway returns a fixed plan reply.
type stubGateway struct {
	reply string
	err   error
}

func (g stubGateway) Complete(context.Context, string, string) (string, error) {
	return g.reply, g.err
}

// recNotifier records pipeline notifications.
type recNotifier struct {
	mu       sync.Mutex
	notified bool
	fileSent bool
}

func (n *recNotifier) Notify(context.Context, string, string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notified = true
	return nil
}
func (n *recNotifier) NotifyFile(context.Context, string, []byte, string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.fileSent = true
	return nil
}

const salesCSV = "region,sales\nnorth,100\nsouth,200\neast,150\nwest,50\n"

func TestAnalyze_EndToEnd(t *testing.T) {
	dir := t.TempDir()
	// Plan: sort by sales desc, then bar chart.
	gw := stubGateway{reply: `{"steps":[{"op":"sort","column":"sales","ascending":false}],"chart":{"type":"bar","title":"Sales","label":"region","value":"sales"}}`}
	notif := &recNotifier{}
	agent := NewDataPipelineAgent(gw, notif, dir)

	res, err := agent.Analyze(context.Background(), "data.csv", []byte(salesCSV), "top sales by region", nil)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if res.Rows != 4 {
		t.Errorf("rows = %d, want 4", res.Rows)
	}
	// Data + chart files written.
	if _, err := os.Stat(res.DataPath); err != nil {
		t.Errorf("data file not written: %v", err)
	}
	if res.ChartPath == "" {
		t.Error("chart should have been rendered")
	} else if data, _ := os.ReadFile(res.ChartPath); !strings.Contains(string(data), "<svg") {
		t.Error("chart file is not SVG")
	}
	// Notified + chart file sent.
	notif.mu.Lock()
	defer notif.mu.Unlock()
	if !notif.notified || !notif.fileSent {
		t.Errorf("expected notify + file, got %+v", notif)
	}
}

func TestAnalyze_AppliesFilterPlan(t *testing.T) {
	dir := t.TempDir()
	gw := stubGateway{reply: `{"steps":[{"op":"filter","column":"sales","operator":">","value":100}],"chart":{"label":"region","value":"sales"}}`}
	agent := NewDataPipelineAgent(gw, nil, dir)
	res, err := agent.Analyze(context.Background(), "d.csv", []byte(salesCSV), "big regions", nil)
	if err != nil {
		t.Fatal(err)
	}
	// sales>100 → south(200), east(150).
	if res.Rows != 2 {
		t.Errorf("filtered rows = %d, want 2", res.Rows)
	}
}

func TestAnalyze_GroupByPlan(t *testing.T) {
	dir := t.TempDir()
	csv := "region,product,sales\nnorth,a,100\nnorth,b,50\nsouth,a,200\n"
	gw := stubGateway{reply: `{"steps":[{"op":"groupby","group":"region","agg":"sales","fn":"sum"}],"chart":{"label":"region","value":"sum(sales)"}}`}
	agent := NewDataPipelineAgent(gw, nil, dir)
	res, err := agent.Analyze(context.Background(), "d.csv", []byte(csv), "sales by region", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 2 { // north, south
		t.Errorf("grouped rows = %d, want 2", res.Rows)
	}
}

func TestAnalyze_DegradesWithoutAI(t *testing.T) {
	dir := t.TempDir()
	agent := NewDataPipelineAgent(nil, nil, dir) // no gateway
	res, err := agent.Analyze(context.Background(), "d.csv", []byte(salesCSV), "show me sales", nil)
	if err != nil {
		t.Fatalf("Analyze without AI should still work: %v", err)
	}
	if res.Rows != 4 {
		t.Errorf("rows = %d, want 4 (no transform)", res.Rows)
	}
	// A default chart should still be produced.
	if res.ChartPath == "" {
		t.Error("default chart should be rendered without AI")
	}
}

func TestAnalyze_EmptyDataErrors(t *testing.T) {
	agent := NewDataPipelineAgent(nil, nil, t.TempDir())
	if _, err := agent.Analyze(context.Background(), "d.csv", []byte("col\n"), "x", nil); err == nil {
		t.Error("empty dataset should error")
	}
}

func TestAnalyze_NoSourceErrors(t *testing.T) {
	agent := NewDataPipelineAgent(nil, nil, t.TempDir())
	if _, err := agent.Analyze(context.Background(), "", nil, "x", nil); err == nil {
		t.Error("no source + no data should error")
	}
}

func TestAnalyze_ProgressEmitted(t *testing.T) {
	agent := NewDataPipelineAgent(nil, nil, t.TempDir())
	var steps []string
	_, err := agent.Analyze(context.Background(), "d.csv", []byte(salesCSV), "x", func(s string) {
		steps = append(steps, s)
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(steps, " ")
	for _, want := range []string{"Loading", "Planning", "Transforming", "Rendering", "complete"} {
		if !strings.Contains(joined, want) {
			t.Errorf("progress missing %q: %s", want, joined)
		}
	}
}

func TestParsePlan_TolerantOfProse(t *testing.T) {
	plan := parsePlan("Here is the plan: {\"steps\":[{\"op\":\"limit\",\"n\":5}],\"chart\":{\"label\":\"a\",\"value\":\"b\"}} done")
	if plan == nil || len(plan.Steps) != 1 || plan.Steps[0].N != 5 {
		t.Errorf("parsePlan = %+v", plan)
	}
}

func TestAnalyze_DataFileUnderPipelineDir(t *testing.T) {
	dir := t.TempDir()
	agent := NewDataPipelineAgent(nil, nil, dir)
	res, _ := agent.Analyze(context.Background(), "d.csv", []byte(salesCSV), "x", nil)
	if filepath.Dir(res.DataPath) != filepath.Join(dir, "pipeline") {
		t.Errorf("data path = %q, want under pipeline/", res.DataPath)
	}
}
