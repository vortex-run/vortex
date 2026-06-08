package forge

import (
	"context"
	"testing"
)

func TestRuleParser_WebApp(t *testing.T) {
	got, err := RuleIntentParser{}.Parse(context.Background(), "build me a web app for todos")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppType != AppTypeWeb {
		t.Errorf("app_type = %q, want web", got.AppType)
	}
	if !hasTarget(got, "web") {
		t.Errorf("web request should deliver 'web', got %v", got.DeliveryTargets)
	}
}

func TestRuleParser_MobileApk(t *testing.T) {
	got, err := RuleIntentParser{}.Parse(context.Background(), "build android apk for cricket scores")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppType != AppTypeMobile {
		t.Errorf("app_type = %q, want mobile", got.AppType)
	}
	if !hasTarget(got, "apk") {
		t.Errorf("mobile request should deliver 'apk', got %v", got.DeliveryTargets)
	}
	if got.Stack.Frontend != "flutter" {
		t.Errorf("mobile stack frontend = %q, want flutter", got.Stack.Frontend)
	}
}

func TestRuleParser_RestAPI(t *testing.T) {
	got, err := RuleIntentParser{}.Parse(context.Background(), "create a REST API for users")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppType != AppTypeAPI {
		t.Errorf("app_type = %q, want api", got.AppType)
	}
	if !hasTarget(got, "api") {
		t.Errorf("api request should deliver 'api', got %v", got.DeliveryTargets)
	}
}

func TestRuleParser_Script(t *testing.T) {
	got, err := RuleIntentParser{}.Parse(context.Background(), "write a python script to rename files")
	if err != nil {
		t.Fatal(err)
	}
	if got.AppType != AppTypeScript {
		t.Errorf("app_type = %q, want script", got.AppType)
	}
}

func TestRuleParser_NeedsLiveData(t *testing.T) {
	got, _ := RuleIntentParser{}.Parse(context.Background(), "build a web app that shows live data from an api")
	if !got.NeedsLiveData {
		t.Error("expected needs_live_data=true for a live-data request")
	}
}

func TestRuleParser_AmbiguousAsksClarify(t *testing.T) {
	got, _ := RuleIntentParser{}.Parse(context.Background(), "make me something cool")
	if len(got.ClarifyingQs) == 0 {
		t.Error("ambiguous request should produce a clarifying question")
	}
}

func TestRuleParser_EmptyErrors(t *testing.T) {
	p := RuleIntentParser{}
	if _, err := p.Parse(context.Background(), "   "); err == nil {
		t.Error("empty message should error")
	}
}

func hasTarget(intent BuildIntent, target string) bool {
	for _, d := range intent.DeliveryTargets {
		if d == target {
			return true
		}
	}
	return false
}
