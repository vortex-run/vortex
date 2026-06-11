package safedial

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"127.0.0.1", "::1", // loopback
		"169.254.169.254",                       // metadata
		"10.0.0.5", "192.168.1.1", "172.16.0.1", // RFC1918
		"169.254.1.1", // link-local
		"0.0.0.0",     // unspecified
		"fd00:ec2::254",
	}
	for _, s := range blocked {
		if !IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34"}
	for _, s := range allowed {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = true, want false", s)
		}
	}
	if !IsBlockedIP(nil) {
		t.Error("IsBlockedIP(nil) should be true")
	}
}

func TestValidateURL_RejectsScheme(t *testing.T) {
	u, _ := url.Parse("file:///etc/passwd")
	if err := ValidateURL(u, false); err == nil {
		t.Error("file:// scheme should be rejected")
	}
}

func TestValidateURL_RejectsInternalLiteralIP(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1/", "http://169.254.169.254/latest/meta-data/", "http://10.0.0.1/"} {
		u, _ := url.Parse(raw)
		if err := ValidateURL(u, false); err == nil {
			t.Errorf("%s should be rejected", raw)
		}
	}
}

func TestValidateURL_AllowsPublicLiteralIP(t *testing.T) {
	u, _ := url.Parse("https://8.8.8.8/")
	if err := ValidateURL(u, false); err != nil {
		t.Errorf("public IP should be allowed: %v", err)
	}
}

func TestClient_BlocksInternalAtDial(t *testing.T) {
	// A loopback httptest server must be unreachable through a default client.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("secret"))
	}))
	defer srv.Close()

	client := Client(Config{})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Error("default safedial client should refuse to dial loopback")
	}
}

func TestClient_AllowLoopbackReachesServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()

	client := Client(Config{AllowLoopback: true})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("AllowLoopback client should reach loopback server: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}

func TestClient_RedirectToInternalBlocked(t *testing.T) {
	// A public-looking server (loopback, but we allow loopback) that redirects
	// to an internal metadata address must have the redirect refused.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, nil, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer srv.Close()

	client := Client(Config{AllowLoopback: true})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	resp, err := client.Do(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Error("redirect to metadata IP should be refused")
	}
}
