package perf

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func newAutoscaler(t *testing.T, cfg AutoscaleConfig) *Autoscaler {
	t.Helper()
	if cfg.Provider == "" {
		cfg.Provider = "custom"
	}
	a, err := NewAutoscaler(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestAutoscale_ScaleUpAboveThreshold(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{ScaleUpAt: 80, MaxNodes: 5})
	d := a.Evaluate(90, 2)
	if d.Action != "scale-up" || d.NodeCount != 3 {
		t.Errorf("decision = %+v, want scale-up to 3", d)
	}
}

func TestAutoscale_ScaleDownBelowThreshold(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{ScaleDownAt: 20, MinNodes: 1})
	d := a.Evaluate(10, 3)
	if d.Action != "scale-down" || d.NodeCount != 2 {
		t.Errorf("decision = %+v, want scale-down to 2", d)
	}
}

func TestAutoscale_NoneWithinThresholds(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{ScaleUpAt: 80, ScaleDownAt: 20})
	d := a.Evaluate(50, 3)
	if d.Action != "none" {
		t.Errorf("decision = %+v, want none", d)
	}
}

func TestAutoscale_RespectsCooldown(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{ScaleUpAt: 80, MaxNodes: 5, Cooldown: time.Hour})
	// First scale-up sets lastScale via Execute. The POST fails (no webhook URL)
	// but Execute sets lastScale before posting, so the cooldown still applies.
	d1 := a.Evaluate(90, 2)
	_ = a.Execute(context.Background(), d1)
	// Within cooldown, the next evaluate returns none.
	d2 := a.Evaluate(95, 3)
	if d2.Action != "none" {
		t.Errorf("within cooldown decision = %+v, want none", d2)
	}
}

func TestAutoscale_RespectsMaxNodes(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{ScaleUpAt: 80, MaxNodes: 3})
	// At the max, no further scale-up.
	d := a.Evaluate(99, 3)
	if d.Action != "none" {
		t.Errorf("at MaxNodes decision = %+v, want none", d)
	}
}

func TestAutoscale_RespectsMinNodes(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{ScaleDownAt: 20, MinNodes: 2})
	// At the min, no further scale-down.
	d := a.Evaluate(5, 2)
	if d.Action != "none" {
		t.Errorf("at MinNodes decision = %+v, want none", d)
	}
}

func TestAutoscale_ExecuteCustomWebhook(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	a := newAutoscaler(t, AutoscaleConfig{Provider: "custom", WebhookURL: srv.URL, MaxNodes: 5})
	err := a.Execute(context.Background(), AutoscaleDecision{Action: "scale-up", NodeCount: 3, Reason: "CPU at 85%"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if got["action"] != "scale-up" {
		t.Errorf("webhook action = %v, want scale-up", got["action"])
	}
	if got["desired_nodes"] != float64(3) {
		t.Errorf("webhook desired_nodes = %v, want 3", got["desired_nodes"])
	}
}

func TestAutoscale_UnknownProviderErrors(t *testing.T) {
	if _, err := NewAutoscaler(AutoscaleConfig{Provider: "aws-magic"}); err == nil {
		t.Error("unknown provider should error")
	}
}

func TestAutoscale_MaxLessThanMinErrors(t *testing.T) {
	if _, err := NewAutoscaler(AutoscaleConfig{Provider: "custom", MinNodes: 5, MaxNodes: 2}); err == nil {
		t.Error("MaxNodes < MinNodes should error")
	}
}

func TestAutoscale_StartRequiresProviders(t *testing.T) {
	a := newAutoscaler(t, AutoscaleConfig{})
	if err := a.Start(context.Background(), nil, nil); err == nil {
		t.Error("Start with nil providers should error")
	}
}
