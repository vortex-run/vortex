package security

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// defaultTorListURL is the official Tor Project bulk exit-node list.
const defaultTorListURL = "https://check.torproject.org/torbulkexitlist"

// torRefreshInterval is how often the Tor exit-node list is refreshed.
const torRefreshInterval = 24 * time.Hour

// AutoBanConfig configures rolling-window automatic banning.
type AutoBanConfig struct {
	Threshold   int           // requests within Window before a ban triggers
	Window      time.Duration // rolling window for counting requests
	BanDuration time.Duration // how long a ban lasts
}

// BlocklistConfig configures a Blocklist.
type BlocklistConfig struct {
	IPAllowlist []string // CIDRs or IPs; when non-empty, only these are allowed
	IPBlocklist []string // CIDRs or IPs to always block
	BlockTor    bool     // fetch and block Tor exit nodes
	TorListURL  string   // override the Tor list URL (default official)
	AutoBan     AutoBanConfig
}

// BlocklistStats is a snapshot of blocklist activity.
type BlocklistStats struct {
	ManualBlocks  int
	AutoBans      int
	TorBlocks     int
	AllowlistSize int
	TotalChecked  int64
}

// banRecord tracks an active auto-ban.
type banRecord struct {
	until time.Time
}

// requestWindow tracks request timestamps for auto-ban accounting.
type requestWindow struct {
	times []time.Time
}

// Blocklist enforces IP allow/block decisions with optional Tor and auto-ban.
// It is safe for concurrent use.
type Blocklist struct {
	cfg AutoBanConfig

	allowNets []*net.IPNet
	blockNets []*net.IPNet
	blockTor  bool

	mu           sync.Mutex
	manualBans   map[string]string // ip → reason
	autoBans     map[string]banRecord
	windows      map[string]*requestWindow
	torExits     map[string]struct{}
	totalChecked int64

	now func() time.Time
}

// NewBlocklist parses the allow/block lists and, when BlockTor is set, fetches
// the Tor exit-node list (gracefully degrading to no Tor blocking on failure).
func NewBlocklist(cfg BlocklistConfig) (*Blocklist, error) {
	allow, err := parseCIDRs(cfg.IPAllowlist)
	if err != nil {
		return nil, fmt.Errorf("security: parsing allowlist: %w", err)
	}
	block, err := parseCIDRs(cfg.IPBlocklist)
	if err != nil {
		return nil, fmt.Errorf("security: parsing blocklist: %w", err)
	}

	b := &Blocklist{
		cfg:        cfg.AutoBan,
		allowNets:  allow,
		blockNets:  block,
		blockTor:   cfg.BlockTor,
		manualBans: make(map[string]string),
		autoBans:   make(map[string]banRecord),
		windows:    make(map[string]*requestWindow),
		torExits:   make(map[string]struct{}),
		now:        time.Now,
	}

	if cfg.BlockTor {
		url := cfg.TorListURL
		if url == "" {
			url = defaultTorListURL
		}
		if err := b.refreshTorList(url); err != nil {
			// Graceful degradation: log and continue without Tor blocking.
			slog.Default().Warn("fetching Tor exit list failed, continuing without Tor blocking", "url", url, "err", err)
		}
	}
	return b, nil
}

// refreshTorList fetches and parses the Tor exit-node list into torExits.
func (b *Blocklist) refreshTorList(url string) error {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tor list returned %s", resp.Status)
	}

	exits := make(map[string]struct{})
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if net.ParseIP(line) != nil {
			exits[line] = struct{}{}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}

	b.mu.Lock()
	b.torExits = exits
	b.mu.Unlock()
	return nil
}

// StartTorRefresh refreshes the Tor exit list every 24h until ctx is cancelled.
// It is a no-op when Tor blocking is disabled.
func (b *Blocklist) StartTorRefresh(ctx context.Context, url string) {
	if !b.blockTor {
		return
	}
	if url == "" {
		url = defaultTorListURL
	}
	ticker := time.NewTicker(torRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := b.refreshTorList(url); err != nil {
				slog.Default().Warn("Tor exit list refresh failed", "err", err)
			}
		}
	}
}

// IsAllowed reports whether ip may proceed and a human-readable reason. The
// checks run in order: allowlist (if set), manual block, Tor exit, auto-ban.
func (b *Blocklist) IsAllowed(ip string) (bool, string) {
	parsed := net.ParseIP(ip)

	b.mu.Lock()
	defer b.mu.Unlock()
	b.totalChecked++

	// 1. Allowlist: when present, an IP not in it is blocked.
	if len(b.allowNets) > 0 {
		if parsed == nil || !ipInNets(parsed, b.allowNets) {
			return false, "not in allowlist"
		}
	}
	// 2. Manual blocklist (config CIDRs or runtime ManualBlock).
	if parsed != nil && ipInNets(parsed, b.blockNets) {
		return false, "manual block"
	}
	if _, ok := b.manualBans[ip]; ok {
		return false, "manual block"
	}
	// 3. Tor exit node.
	if b.blockTor {
		if _, ok := b.torExits[ip]; ok {
			return false, "tor exit node"
		}
	}
	// 4. Auto-ban.
	if rec, ok := b.autoBans[ip]; ok {
		if b.now().Before(rec.until) {
			return false, "auto-banned"
		}
		delete(b.autoBans, ip) // expired
	}
	return true, "allowed"
}

// RecordRequest tracks a request from ip for auto-ban accounting; if the count
// within the configured window exceeds the threshold, ip is auto-banned.
func (b *Blocklist) RecordRequest(ip string) {
	if b.cfg.Threshold <= 0 || b.cfg.Window <= 0 {
		return
	}
	now := b.now()
	cutoff := now.Add(-b.cfg.Window)

	b.mu.Lock()
	defer b.mu.Unlock()

	w, ok := b.windows[ip]
	if !ok {
		w = &requestWindow{}
		b.windows[ip] = w
	}
	// Drop timestamps older than the window, then record this one.
	kept := w.times[:0]
	for _, ts := range w.times {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	kept = append(kept, now)
	w.times = kept

	if len(w.times) > b.cfg.Threshold {
		b.autoBans[ip] = banRecord{until: now.Add(b.cfg.BanDuration)}
		w.times = nil // reset so the count starts fresh after the ban
	}
}

// ManualBlock blocks ip at runtime with the given reason.
func (b *Blocklist) ManualBlock(ip, reason string) {
	b.mu.Lock()
	b.manualBans[ip] = reason
	b.mu.Unlock()
}

// ManualUnblock removes a runtime manual block for ip.
func (b *Blocklist) ManualUnblock(ip string) {
	b.mu.Lock()
	delete(b.manualBans, ip)
	b.mu.Unlock()
}

// Stats returns a snapshot of current blocklist activity.
func (b *Blocklist) Stats() BlocklistStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	active := 0
	now := b.now()
	for _, rec := range b.autoBans {
		if now.Before(rec.until) {
			active++
		}
	}
	return BlocklistStats{
		ManualBlocks:  len(b.manualBans),
		AutoBans:      active,
		TorBlocks:     len(b.torExits),
		AllowlistSize: len(b.allowNets),
		TotalChecked:  b.totalChecked,
	}
}

// parseCIDRs parses a mix of bare IPs and CIDRs into *net.IPNet. A bare IP is
// treated as a /32 (IPv4) or /128 (IPv6).
func parseCIDRs(entries []string) ([]*net.IPNet, error) {
	var nets []*net.IPNet
	for _, e := range entries {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if !strings.Contains(e, "/") {
			ip := net.ParseIP(e)
			if ip == nil {
				return nil, fmt.Errorf("invalid IP %q", e)
			}
			bits := 32
			if ip.To4() == nil {
				bits = 128
			}
			e = fmt.Sprintf("%s/%d", e, bits)
		}
		_, n, err := net.ParseCIDR(e)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", e, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
}

// ipInNets reports whether ip is contained in any of nets.
func ipInNets(ip net.IP, nets []*net.IPNet) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
