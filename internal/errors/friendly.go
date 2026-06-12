// Package vortexerrors turns raw, low-level errors (dial timeouts, missing
// secrets, TLS failures) into friendly, actionable messages (brand redesign
// part 6): every error states what happened, why, and exactly how to fix it.
// The package is named vortexerrors to avoid shadowing the standard errors
// package at call sites.
package vortexerrors

import (
	"regexp"
	"strings"

	"github.com/vortex-run/vortex/internal/tui/brand"
)

// FriendlyError is a structured, human-facing error explanation.
type FriendlyError struct {
	Title   string // short description
	Detail  string // what happened
	Why     string // why it happened
	Fix     string // how to fix it
	DocsURL string // link to docs
	Code    string // short error code for support
}

// Pattern-extraction regexes.
var (
	reDialAddr = regexp.MustCompile(`dial tcp ([\d.:\[\]a-fA-F]+)`)
	reSecret   = regexp.MustCompile(`secret\s+['"]?([A-Za-z0-9_.-]+)['"]?`)
	reProvider = regexp.MustCompile(`(?i)(deepseek|claude|anthropic|openai|gemini|groq|ollama|bedrock|azure|openrouter)`)
)

// NewFriendly matches err against known failure patterns and returns a
// FriendlyError. An unrecognised error yields the default "unexpected error"
// explanation that still surfaces the raw message.
func NewFriendly(err error) *FriendlyError {
	if err == nil {
		return nil
	}
	msg := err.Error()
	low := strings.ToLower(msg)

	switch {
	case strings.Contains(low, "i/o timeout") && strings.Contains(low, "dial tcp"):
		addr := "the target host"
		if m := reDialAddr.FindStringSubmatch(msg); m != nil {
			addr = m[1]
		}
		return &FriendlyError{
			Title:   "Network connection failed",
			Detail:  "Could not connect to " + addr,
			Why:     "The target host is not reachable or the port is not open",
			Fix:     "Check the address in vortex.cue.\nIf running single node, remove unreachable peers: cluster: peers: []",
			DocsURL: "https://vortex.run/docs/cluster",
			Code:    "NET_TIMEOUT",
		}

	case (strings.Contains(low, "rate limit") || strings.Contains(low, "429")) && !strings.Contains(low, "connection"):
		provider := providerOr(msg, "the AI provider")
		return &FriendlyError{
			Title:   "AI provider rate limited",
			Detail:  "Too many requests to " + provider,
			Why:     "Your API key has hit its rate limit",
			Fix:     "Wait a minute, or add backup keys:\nvortex keys add --provider <name> --key <key>",
			DocsURL: "https://vortex.run/docs/api-keys",
			Code:    "AI_RATE_LIMIT",
		}

	case strings.Contains(low, "invalid api key") || strings.Contains(low, "401") || strings.Contains(low, "unauthorized"):
		provider := providerOr(msg, "the AI provider")
		return &FriendlyError{
			Title:   "Invalid API key",
			Detail:  "Authentication failed with " + provider,
			Why:     "The API key is incorrect or expired",
			Fix:     "Run vortex setup to configure a new key",
			DocsURL: "https://vortex.run/docs/api-keys",
			Code:    "AI_AUTH",
		}

	case strings.Contains(low, "secret") &&
		(strings.Contains(low, "not found") || strings.Contains(low, "missing") || strings.Contains(low, "not set")):
		name := "<name>"
		if m := reSecret.FindStringSubmatch(low); m != nil && m[1] != "" {
			name = m[1]
		}
		return &FriendlyError{
			Title:   "Secret not configured",
			Detail:  "Secret '" + name + "' is required but not set",
			Why:     "This secret was declared in vortex.cue but has not been given a value",
			Fix:     "Set it with: vortex secret set " + name + " <value>",
			DocsURL: "https://vortex.run/docs/secrets",
			Code:    "SECRET_MISSING",
		}

	case strings.Contains(low, "certificate") || strings.Contains(low, "tls") || strings.Contains(low, "x509"):
		return &FriendlyError{
			Title:   "TLS certificate error",
			Detail:  msg,
			Why:     "Certificate is invalid, expired, or self-signed",
			Fix:     "On first start, self-signed certs are normal. If using Let's Encrypt,\nensure your domain points to this server",
			DocsURL: "https://vortex.run/docs/tls",
			Code:    "TLS_CERT",
		}

	case strings.Contains(low, "connection refused"):
		addr := "the target address"
		if m := reDialAddr.FindStringSubmatch(msg); m != nil {
			addr = m[1]
		}
		return &FriendlyError{
			Title:  "Service not running",
			Detail: "Could not connect to " + addr,
			Why:    "The service at this address is not accepting connections",
			Fix:    "Start the target service, or check the address in vortex.cue",
			Code:   "CONN_REFUSED",
		}

	case strings.Contains(low, "permission denied") || strings.Contains(low, "operation not permitted"):
		return &FriendlyError{
			Title:  "Permission denied",
			Detail: msg,
			Why:    "VORTEX does not have permission to access this file or directory",
			Fix:    "Check file permissions, or run with appropriate user privileges",
			Code:   "PERM_DENIED",
		}

	default:
		return &FriendlyError{
			Title:  "Unexpected error",
			Detail: msg,
			Why:    "An unexpected error occurred",
			Fix:    "Check the logs with: vortex start --verbose\nReport at: https://github.com/vortex-run/vortex/issues",
			Code:   "UNKNOWN",
		}
	}
}

// providerOr extracts an AI provider name from msg, or returns def.
func providerOr(msg, def string) string {
	if m := reProvider.FindStringSubmatch(msg); m != nil {
		return m[1]
	}
	return def
}

// String renders the full multi-section explanation.
func (e *FriendlyError) String() string {
	var b strings.Builder
	b.WriteString(brand.IconError + "  " + e.Title + "\n\n")
	if e.Detail != "" {
		b.WriteString("   " + indent(e.Detail) + "\n\n")
	}
	if e.Why != "" {
		b.WriteString("   Why:  " + indent(e.Why) + "\n\n")
	}
	if e.Fix != "" {
		b.WriteString("   Fix:  " + indent(e.Fix) + "\n")
	}
	if e.DocsURL != "" {
		b.WriteString("\n   Docs: " + e.DocsURL + "\n")
	}
	return b.String()
}

// indent aligns continuation lines under the first line's text column.
func indent(s string) string {
	return strings.ReplaceAll(s, "\n", "\n         ")
}

// Short renders a single-line "✗ Title — Fix" summary (first fix line only).
func (e *FriendlyError) Short() string {
	fix := e.Fix
	if i := strings.IndexByte(fix, '\n'); i >= 0 {
		fix = fix[:i]
	}
	return brand.IconError + " " + e.Title + " — " + fix
}

// Format is a convenience for NewFriendly(err).String(). A nil error returns "".
func Format(err error) string {
	if err == nil {
		return ""
	}
	return NewFriendly(err).String()
}
