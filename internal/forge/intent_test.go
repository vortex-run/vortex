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

func TestAIIntentParser_CapsClarifyingQuestions(t *testing.T) {
	gw := &scriptedGateway{replies: []string{`{"app_type":"mobile","clarifying_questions":[
		{"question":"q1","key":"k1","options":["a","b","c","d","e"]},
		{"question":"q2","key":"k2"},
		{"question":"q3","key":"k3"},
		{"question":"q4","key":"k4"}]}`}}
	p := NewAIIntentParser(gw)
	intent, err := p.Parse(context.Background(), "build something")
	if err != nil {
		t.Fatal(err)
	}
	if len(intent.ClarifyingQs) != 2 {
		t.Errorf("clarifying questions = %d, want capped at 2: %v", len(intent.ClarifyingQs), intent.ClarifyingQs)
	}
	if len(intent.ClarifyingQs[0].Options) != 3 {
		t.Errorf("options = %d, want capped at 3", len(intent.ClarifyingQs[0].Options))
	}
}

func TestClarifyingQuestion_StructuredParse(t *testing.T) {
	gw := &scriptedGateway{replies: []string{`{"app_type":"mobile","clarifying_questions":[
		{"question":"What type of calculator?","key":"calc_type","options":["Basic","Scientific"]}]}`}}
	intent, err := NewAIIntentParser(gw).Parse(context.Background(), "flutter calculator")
	if err != nil {
		t.Fatal(err)
	}
	q := intent.ClarifyingQs[0]
	if q.Question != "What type of calculator?" || q.Key != "calc_type" || len(q.Options) != 2 {
		t.Errorf("structured question = %+v", q)
	}
	// ClarifyingTexts returns the question texts.
	if texts := intent.ClarifyingTexts(); len(texts) != 1 || texts[0] != "What type of calculator?" {
		t.Errorf("ClarifyingTexts = %v", texts)
	}
}

func TestRuleIntentParser_StructuredOptions(t *testing.T) {
	intent, _ := NewRuleIntentParser().Parse(context.Background(), "make me something cool")
	if len(intent.ClarifyingQs) == 0 {
		t.Fatal("ambiguous request should produce a clarifying question")
	}
	q := intent.ClarifyingQs[0]
	if len(q.Options) == 0 || q.Key == "" {
		t.Errorf("rule clarifying question should have options + key: %+v", q)
	}
}

func TestForge_StatusCarriesQuestions(t *testing.T) {
	intent := BuildIntent{ClarifyingQs: []ClarifyingQuestion{
		{Question: "web or mobile?", Key: "platform", Options: []string{"Web", "Mobile"}},
	}}
	b, q, c, d := &stubBuilder{}, &stubQA{}, &stubCodegen{}, &stubDeliver{}
	f := newStubForge(t, intent, b, q, c, d)
	_ = f.Build(context.Background(), "make something", 1, nil)
	st := f.Status()
	if len(st.Questions) != 1 || st.Questions[0].Key != "platform" || len(st.Questions[0].Options) != 2 {
		t.Errorf("Status should carry structured questions: %+v", st.Questions)
	}
}
