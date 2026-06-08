package forge

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSender records messages and files sent.
type fakeSender struct {
	messages []string
	files    []string
	chatID   int64
}

func (f *fakeSender) SendMessage(_ context.Context, chatID int64, text string) error {
	f.chatID = chatID
	f.messages = append(f.messages, text)
	return nil
}

func (f *fakeSender) SendFile(_ context.Context, chatID int64, filename string, _ []byte, _ string) error {
	f.chatID = chatID
	f.files = append(f.files, filename)
	return nil
}

// fakeDeployer records web deploys and returns a fixed URL.
type fakeDeployer struct {
	deployed string
}

func (f *fakeDeployer) DeployWebApp(_ context.Context, distDir, name string) (string, error) {
	f.deployed = distDir
	return "https://" + name + ".vortex.app", nil
}

func TestDelivery_APKSendsFile(t *testing.T) {
	apkDir := t.TempDir()
	writeFile(t, apkDir, "app.apk", "PK\x03\x04fakeapk")

	sender := &fakeSender{}
	d := NewDeliveryAgent(DeliveryConfig{Sender: sender})
	intent := BuildIntent{DeliveryTargets: []string{"apk"}, Description: "cricket app"}
	out := BuildOutput{ArtifactType: "apk", ArtifactPath: apkDir, DurationMs: 90000}

	if err := d.Deliver(context.Background(), out, intent, 42, 0.05); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if len(sender.files) != 1 || sender.files[0] != "app.apk" {
		t.Errorf("expected app.apk sent, got %v", sender.files)
	}
	if sender.chatID != 42 {
		t.Errorf("chatID = %d, want 42", sender.chatID)
	}
}

func TestDelivery_MissingAPKErrors(t *testing.T) {
	sender := &fakeSender{}
	d := NewDeliveryAgent(DeliveryConfig{Sender: sender})
	intent := BuildIntent{DeliveryTargets: []string{"apk"}}
	out := BuildOutput{ArtifactType: "apk", ArtifactPath: t.TempDir()} // no apk file

	if err := d.Deliver(context.Background(), out, intent, 1, 0); err == nil {
		t.Error("missing apk should error")
	}
}

func TestDelivery_WebDeploysAndReturnsURL(t *testing.T) {
	dist := t.TempDir()
	writeFile(t, dist, "index.html", "<html></html>")

	sender := &fakeSender{}
	deployer := &fakeDeployer{}
	d := NewDeliveryAgent(DeliveryConfig{Sender: sender, Deployer: deployer})
	intent := BuildIntent{DeliveryTargets: []string{"web"}, Description: "Todo App"}
	out := BuildOutput{ArtifactType: "web-dist", ArtifactPath: dist, DurationMs: 5000}

	if err := d.Deliver(context.Background(), out, intent, 7, 0.01); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	if deployer.deployed != dist {
		t.Errorf("deployer got %q, want %q", deployer.deployed, dist)
	}
	// The summary should contain the deployed URL.
	joined := strings.Join(sender.messages, "\n")
	if !strings.Contains(joined, "vortex.app") {
		t.Errorf("summary should contain the web URL: %s", joined)
	}
}

func TestDelivery_SummaryHasRequiredFields(t *testing.T) {
	sender := &fakeSender{}
	d := NewDeliveryAgent(DeliveryConfig{Sender: sender})
	intent := BuildIntent{DeliveryTargets: []string{"script"}}
	out := BuildOutput{ArtifactType: "script", DurationMs: 12000, Stdout: "hello"}

	if err := d.Deliver(context.Background(), out, intent, 1, 0.0234); err != nil {
		t.Fatalf("Deliver: %v", err)
	}
	summary := sender.messages[len(sender.messages)-1]
	for _, want := range []string{"Build complete", "Time:", "AI cost: $"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestDelivery_DeployWebAppRegistersRoute(t *testing.T) {
	deployer := &fakeDeployer{}
	d := NewDeliveryAgent(DeliveryConfig{Deployer: deployer})
	url, err := d.DeployWebApp(context.Background(), filepath.Join(t.TempDir(), "dist"), "myapp")
	if err != nil {
		t.Fatalf("DeployWebApp: %v", err)
	}
	if !strings.Contains(url, "myapp") {
		t.Errorf("url = %q, want it to contain the app name", url)
	}
}

func TestDelivery_DeployWebAppNoDeployerErrors(t *testing.T) {
	d := NewDeliveryAgent(DeliveryConfig{})
	if _, err := d.DeployWebApp(context.Background(), "/dist", "x"); err == nil {
		t.Error("DeployWebApp with no deployer should error")
	}
}

func TestSlug(t *testing.T) {
	cases := map[string]string{
		"Todo App":          "todo-app",
		"  Cricket Scores!": "cricket-scores",
		"":                  "app",
	}
	for in, want := range cases {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}
