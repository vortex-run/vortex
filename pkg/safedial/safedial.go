// Package safedial builds HTTP clients that are robust against SSRF via DNS
// rebinding (production audit H2). The standard validate-then-fetch pattern is
// a TOCTOU hole: a guard resolves a hostname, sees a public IP, and allows the
// request; the http.Client then resolves independently at dial time and a
// rebinding DNS record can return 169.254.169.254 / 127.0.0.1 / RFC1918.
//
// safedial closes the hole by resolving once in a custom DialContext,
// validating every candidate IP, and dialing the validated IP directly — DNS
// resolution and the connection are controlled in one step. Redirects are
// re-validated per hop via CheckRedirect. Stdlib only.
package safedial

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"time"
)

// ErrBlocked is returned when a host resolves to a disallowed address.
var ErrBlocked = fmt.Errorf("safedial: blocked internal address")

// metadataIPs are cloud instance-metadata addresses that must never be reached.
// 100.100.100.200 is Alibaba Cloud ECS metadata (RAM-role STS credentials);
// it is also inside the blocked CGNAT range, listed for clarity.
var metadataIPs = map[string]bool{
	"169.254.169.254": true,
	"fd00:ec2::254":   true,
	"100.100.100.200": true,
}

// blockedPrefixes are non-global ranges that net.IP's predicates do not cover
// (audit F-11): RFC 6598 shared/CGNAT space (Tailscale and carrier overlays,
// Alibaba metadata), "this network", IETF protocol assignments, benchmarking,
// multicast, reserved/broadcast, the local-use NAT64 prefix, Teredo, and
// IPv6 multicast and discard-only space.
var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "198.18.0.0/15",
	"224.0.0.0/4", "240.0.0.0/4",
	"64:ff9b:1::/48", "2001::/32", "100::/64", "ff00::/8",
)

// Prefixes whose addresses embed an IPv4 address that the network translates
// to: NAT64 well-known (RFC 6052, low 32 bits) and 6to4 (RFC 3056, bits
// 16-47). 64:ff9b::a9fe:a9fe reaches 169.254.169.254 through a NAT64 gateway.
var (
	nat64WKP  = netip.MustParsePrefix("64:ff9b::/96")
	sixToFour = netip.MustParsePrefix("2002::/16")
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, len(cidrs))
	for i, c := range cidrs {
		out[i] = netip.MustParsePrefix(c)
	}
	return out
}

// IsBlockedIP reports whether ip is loopback, link-local, private, metadata,
// shared/CGNAT, multicast, reserved, or an IPv6 address that embeds a blocked
// IPv4 address — anything unsafe to reach from a server-side fetcher. This is
// the one blocklist: the agents http_get pre-flight and the research fetcher
// call it too, so the checks cannot drift apart.
func IsBlockedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}
	a, ok := netip.AddrFromSlice(ip)
	if !ok {
		return true
	}
	return blockedAddr(a.Unmap(), true)
}

func blockedAddr(a netip.Addr, recurse bool) bool {
	ip := net.IP(a.AsSlice())
	if metadataIPs[a.String()] {
		return true
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	for _, p := range blockedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	if recurse && a.Is6() {
		b := a.As16()
		switch {
		case nat64WKP.Contains(a):
			return blockedAddr(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}), false)
		case sixToFour.Contains(a):
			return blockedAddr(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}), false)
		}
	}
	return false
}

// Config tunes a safe client.
type Config struct {
	// Timeout is the overall request timeout (default 30s).
	Timeout time.Duration
	// DialTimeout is the per-connection dial timeout (default 10s).
	DialTimeout time.Duration
	// AllowLoopback permits loopback targets (tests reaching httptest servers).
	AllowLoopback bool
}

// allows reports whether ip is permitted under cfg.
func (cfg Config) allows(ip net.IP) bool {
	if cfg.AllowLoopback && ip != nil && ip.IsLoopback() {
		return true
	}
	return !IsBlockedIP(ip)
}

// Client returns an *http.Client whose dialer resolves and validates the
// target host once and dials a validated, pinned IP, and whose redirect
// handler re-validates every hop's host. The resulting client is safe to
// reuse concurrently.
func Client(cfg Config) *http.Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.DialTimeout <= 0 {
		cfg.DialTimeout = 10 * time.Second
	}
	d := &net.Dialer{Timeout: cfg.DialTimeout}

	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		// Resolve ONCE here; dial only the IPs we validate. The http.Transport
		// passes us the original hostname, so no second independent resolution
		// happens at dial time.
		var ips []net.IP
		if lit := net.ParseIP(host); lit != nil {
			ips = []net.IP{lit}
		} else {
			resolved, rerr := net.DefaultResolver.LookupIP(ctx, "ip", host)
			if rerr != nil {
				return nil, fmt.Errorf("safedial: resolve %q: %w", host, rerr)
			}
			ips = resolved
		}
		for _, ip := range ips {
			if !cfg.allows(ip) {
				return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, ip)
			}
		}
		// Dial the first validated IP directly (pinned), not the hostname.
		var lastErr error
		for _, ip := range ips {
			conn, derr := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if derr == nil {
				return conn, nil
			}
			lastErr = derr
		}
		return nil, lastErr
	}

	return &http.Client{
		Timeout:   cfg.Timeout,
		Transport: &http.Transport{DialContext: dial},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("safedial: stopped after 10 redirects")
			}
			// Re-validate each redirect target's host up front (the dialer also
			// guards, but failing here gives a clearer error and avoids the
			// connection attempt).
			return ValidateURL(req.URL, cfg.AllowLoopback)
		},
	}
}

// ValidateURL rejects non-http(s) schemes and hosts that resolve to a blocked
// address. It is the pre-flight check; the dialer enforces the same policy at
// connection time so a rebind between the two cannot slip through.
func ValidateURL(u *url.URL, allowLoopback bool) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: only http/https allowed", ErrBlocked)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrBlocked)
	}
	cfg := Config{AllowLoopback: allowLoopback}
	if lit := net.ParseIP(host); lit != nil {
		if !cfg.allows(lit) {
			return fmt.Errorf("%w: %s", ErrBlocked, host)
		}
		return nil
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: resolve %q: %v", ErrBlocked, host, err)
	}
	for _, ip := range ips {
		if !cfg.allows(ip) {
			return fmt.Errorf("%w: %s resolves to %s", ErrBlocked, host, ip)
		}
	}
	return nil
}
