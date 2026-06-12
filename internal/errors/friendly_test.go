package vortexerrors

import (
	"errors"
	"strings"
	"testing"
)

func TestNewFriendly_NetworkTimeout(t *testing.T) {
	fe := NewFriendly(errors.New("dial tcp 10.0.0.5:7946: i/o timeout"))
	if fe.Title != "Network connection failed" || fe.Code != "NET_TIMEOUT" {
		t.Fatalf("got %+v", fe)
	}
	if !strings.Contains(fe.Detail, "10.0.0.5:7946") {
		t.Errorf("detail should name the address: %q", fe.Detail)
	}
	if !strings.Contains(fe.Fix, "cluster: peers: []") {
		t.Errorf("fix should mention the peers remedy: %q", fe.Fix)
	}
}

func TestNewFriendly_RateLimit(t *testing.T) {
	for _, in := range []string{
		"deepseek: status 429: rate limit exceeded",
		"too many requests (rate limit) to claude",
	} {
		fe := NewFriendly(errors.New(in))
		if fe.Code != "AI_RATE_LIMIT" {
			t.Errorf("%q → code %q, want AI_RATE_LIMIT", in, fe.Code)
		}
		if !strings.Contains(fe.Fix, "vortex keys add") {
			t.Errorf("%q fix should suggest backup keys: %q", in, fe.Fix)
		}
	}
	// The provider name is extracted into the detail.
	fe := NewFriendly(errors.New("deepseek: 429 rate limit"))
	if !strings.Contains(strings.ToLower(fe.Detail), "deepseek") {
		t.Errorf("detail should name the provider: %q", fe.Detail)
	}
}

func TestNewFriendly_InvalidKey(t *testing.T) {
	for _, in := range []string{
		"openai: 401 unauthorized",
		"invalid api key",
	} {
		fe := NewFriendly(errors.New(in))
		if fe.Code != "AI_AUTH" {
			t.Errorf("%q → code %q, want AI_AUTH", in, fe.Code)
		}
		if !strings.Contains(fe.Fix, "vortex setup") {
			t.Errorf("%q fix should suggest setup: %q", in, fe.Fix)
		}
	}
}

func TestNewFriendly_MissingSecret(t *testing.T) {
	fe := NewFriendly(errors.New(`secret "db_password" not found in store`))
	if fe.Code != "SECRET_MISSING" {
		t.Fatalf("code = %q, want SECRET_MISSING", fe.Code)
	}
	if !strings.Contains(fe.Fix, "vortex secret set db_password") {
		t.Errorf("fix should name the exact command: %q", fe.Fix)
	}
}

func TestNewFriendly_TLS(t *testing.T) {
	fe := NewFriendly(errors.New("x509: certificate signed by unknown authority"))
	if fe.Code != "TLS_CERT" {
		t.Fatalf("code = %q, want TLS_CERT", fe.Code)
	}
	if !strings.Contains(fe.Fix, "self-signed") {
		t.Errorf("fix = %q", fe.Fix)
	}
}

func TestNewFriendly_ConnectionRefused(t *testing.T) {
	fe := NewFriendly(errors.New("dial tcp 127.0.0.1:5432: connect: connection refused"))
	if fe.Code != "CONN_REFUSED" {
		t.Fatalf("code = %q, want CONN_REFUSED", fe.Code)
	}
	if !strings.Contains(fe.Detail, "127.0.0.1:5432") {
		t.Errorf("detail should name the address: %q", fe.Detail)
	}
}

func TestNewFriendly_PermissionDenied(t *testing.T) {
	fe := NewFriendly(errors.New("open /etc/vortex/vortex.cue: permission denied"))
	if fe.Code != "PERM_DENIED" {
		t.Errorf("code = %q, want PERM_DENIED", fe.Code)
	}
}

func TestNewFriendly_UnknownDefault(t *testing.T) {
	fe := NewFriendly(errors.New("the flux capacitor desynchronised"))
	if fe.Title != "Unexpected error" || fe.Code != "UNKNOWN" {
		t.Fatalf("got %+v", fe)
	}
	if !strings.Contains(fe.Detail, "flux capacitor") {
		t.Errorf("default should surface the raw message: %q", fe.Detail)
	}
	if !strings.Contains(fe.Fix, "--verbose") || !strings.Contains(fe.Fix, "github.com/vortex-run") {
		t.Errorf("default fix should point at logs + issues: %q", fe.Fix)
	}
}

func TestNewFriendly_Nil(t *testing.T) {
	if NewFriendly(nil) != nil {
		t.Error("NewFriendly(nil) should be nil")
	}
	if Format(nil) != "" {
		t.Error("Format(nil) should be empty")
	}
}

func TestString_IncludesAllSections(t *testing.T) {
	fe := NewFriendly(errors.New("dial tcp 10.0.0.5:7946: i/o timeout"))
	s := fe.String()
	for _, want := range []string{fe.Title, "Why:", "Fix:", "Docs:", fe.DocsURL} {
		if !strings.Contains(s, want) {
			t.Errorf("String() missing %q:\n%s", want, s)
		}
	}
}

func TestShort_SingleLine(t *testing.T) {
	fe := NewFriendly(errors.New("invalid api key"))
	short := fe.Short()
	if strings.Contains(short, "\n") {
		t.Errorf("Short() must be one line: %q", short)
	}
	if !strings.Contains(short, fe.Title) {
		t.Errorf("Short() should contain the title: %q", short)
	}
}

// TestAllPatternsCovered asserts every documented failure family resolves to
// its own non-default code (a guard against a future regex regression silently
// dropping a case into the UNKNOWN bucket).
func TestAllPatternsCovered(t *testing.T) {
	cases := map[string]string{
		"dial tcp 1.2.3.4:9: i/o timeout":        "NET_TIMEOUT",
		"provider 429 rate limit":                "AI_RATE_LIMIT",
		"401 unauthorized":                       "AI_AUTH",
		`secret "X" not set`:                     "SECRET_MISSING",
		"x509: certificate invalid":              "TLS_CERT",
		"dial tcp 1.2.3.4:9: connection refused": "CONN_REFUSED",
		"open file: permission denied":           "PERM_DENIED",
	}
	for in, want := range cases {
		if got := NewFriendly(errors.New(in)).Code; got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
