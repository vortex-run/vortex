package startup

import (
	"bytes"
	"strings"
	"testing"
)

func newTestDisplay(verbose bool) (*Display, *bytes.Buffer) {
	d := NewStartupDisplay(verbose)
	var buf bytes.Buffer
	d.SetOutput(&buf)
	return d, &buf
}

func TestBannerPrintsVortex(t *testing.T) {
	d, buf := newTestDisplay(false)
	d.Banner()
	out := buf.String()
	if !strings.Contains(out, "VORTEX") {
		t.Errorf("banner missing VORTEX: %q", out)
	}
	if !strings.Contains(out, "One binary. Any server. Fully autonomous.") {
		t.Errorf("banner missing tagline: %q", out)
	}
	if !strings.Contains(out, "╔") || !strings.Contains(out, "╚") {
		t.Errorf("banner missing box frame: %q", out)
	}
}

func TestStepShowsSpinnerThenCheckmark(t *testing.T) {
	d, buf := newTestDisplay(false)
	d.Step("Audit log", "enabled")
	out := buf.String()
	if !strings.Contains(out, "⠸ Audit log...") {
		t.Errorf("step missing spinner phase: %q", out)
	}
	if !strings.Contains(out, "\r✓ Audit log") {
		t.Errorf("step missing in-place checkmark update: %q", out)
	}
	if !strings.Contains(out, "→ enabled") {
		t.Errorf("step missing detail: %q", out)
	}
}

func TestStepFailShowsExplanation(t *testing.T) {
	d, buf := newTestDisplay(false)
	d.StepFail("Cluster", "dial tcp 10.0.0.5:7946: i/o timeout")
	out := buf.String()
	if !strings.Contains(out, "✗ Cluster") {
		t.Errorf("fail line missing: %q", out)
	}
	if !strings.Contains(out, "Could not reach cluster peer at 10.0.0.5:7946") {
		t.Errorf("fail missing mapped explanation: %q", out)
	}
	if !strings.Contains(out, "cluster: peers: []") {
		t.Errorf("fail missing concrete fix: %q", out)
	}
}

func TestReadyPrintsDashboardURL(t *testing.T) {
	d, buf := newTestDisplay(false)
	d.Ready(ReadyConfig{
		Version: "v1.0.0", Cluster: "prod", APIAddr: ":9090",
		Routes: []string{"api", "frontend"}, AIProvider: "deepseek", Telegram: true,
	})
	out := buf.String()
	for _, want := range []string{
		"VORTEX is running",
		"http://localhost:9090/dashboard",
		"vortex ui",
		"vortex code",
		"https://vortex.run/docs",
		"cluster prod",
		"AI deepseek",
		"2 routes",
		"Press Ctrl+C to stop",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ready output missing %q:\n%s", want, out)
		}
	}
}

func TestVerboseModeWritesNothing(t *testing.T) {
	d, buf := newTestDisplay(true)
	d.Banner()
	d.Step("Audit log", "enabled")
	d.StepFail("Cluster", "boom")
	d.Ready(ReadyConfig{APIAddr: ":9090"})
	if buf.Len() != 0 {
		t.Errorf("verbose display wrote %q, want pass-through (nothing)", buf.String())
	}
	if d.Active() {
		t.Error("verbose display must report inactive")
	}
}

func TestExplainTimeout(t *testing.T) {
	got := Explain("dial tcp 192.168.1.20:7946: i/o timeout")
	if !strings.Contains(got, "Could not reach cluster peer at 192.168.1.20:7946") {
		t.Errorf("Explain(timeout) = %q", got)
	}
	if !strings.Contains(got, "cluster: peers: []") {
		t.Errorf("Explain(timeout) missing fix: %q", got)
	}
}

func TestExplainMissingSecret(t *testing.T) {
	got := Explain(`secret "db_password" not found in store`)
	if !strings.Contains(got, "Secret 'db_password' is not set") {
		t.Errorf("Explain(secret) = %q", got)
	}
	if !strings.Contains(got, "vortex secret set db_password") {
		t.Errorf("Explain(secret) missing fix command: %q", got)
	}
}

func TestExplainCertificate(t *testing.T) {
	got := Explain("x509: certificate signed by unknown authority")
	if !strings.Contains(got, "self-signed") {
		t.Errorf("Explain(cert) = %q", got)
	}
}

func TestExplainUnknownPassesThrough(t *testing.T) {
	if got := Explain("something completely novel"); got != "something completely novel" {
		t.Errorf("Explain(unknown) = %q, want pass-through", got)
	}
}

func TestNormalizeAddr(t *testing.T) {
	cases := map[string]string{
		":9090":         ":9090",
		"0.0.0.0:9090":  ":9090",
		"127.0.0.1:880": ":880",
		"":              ":9090",
		"8080":          ":8080",
	}
	for in, want := range cases {
		if got := normalizeAddr(in); got != want {
			t.Errorf("normalizeAddr(%q) = %q, want %q", in, got, want)
		}
	}
}
