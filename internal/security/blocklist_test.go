package security

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBlocklist_EmptyAllowlistAllowsAll(t *testing.T) {
	b, err := NewBlocklist(BlocklistConfig{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ip := range []string{"1.2.3.4", "10.0.0.1", "203.0.113.7"} {
		if ok, reason := b.IsAllowed(ip); !ok {
			t.Errorf("%s blocked (%s); empty allowlist should allow all", ip, reason)
		}
	}
}

func TestBlocklist_AllowlistOnlyListed(t *testing.T) {
	b, err := NewBlocklist(BlocklistConfig{IPAllowlist: []string{"127.0.0.1", "10.0.0.0/8"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.IsAllowed("127.0.0.1"); !ok {
		t.Error("127.0.0.1 should be allowed")
	}
	if ok, _ := b.IsAllowed("10.5.5.5"); !ok {
		t.Error("10.5.5.5 (in 10.0.0.0/8) should be allowed")
	}
	if ok, reason := b.IsAllowed("8.8.8.8"); ok || reason != "not in allowlist" {
		t.Errorf("8.8.8.8 = (%v, %q), want blocked 'not in allowlist'", ok, reason)
	}
}

func TestBlocklist_ManualBlocklistConfig(t *testing.T) {
	b, err := NewBlocklist(BlocklistConfig{IPBlocklist: []string{"6.6.6.6", "192.168.0.0/16"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := b.IsAllowed("6.6.6.6"); ok || reason != "manual block" {
		t.Errorf("6.6.6.6 = (%v, %q), want blocked 'manual block'", ok, reason)
	}
	if ok, _ := b.IsAllowed("192.168.1.50"); ok {
		t.Error("192.168.1.50 (in 192.168.0.0/16) should be blocked")
	}
}

func TestBlocklist_TorBlocksExitNode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("# Tor exit list\n66.66.66.66\n77.77.77.77\n"))
	}))
	defer srv.Close()

	b, err := NewBlocklist(BlocklistConfig{BlockTor: true, TorListURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if ok, reason := b.IsAllowed("66.66.66.66"); ok || reason != "tor exit node" {
		t.Errorf("66.66.66.66 = (%v, %q), want blocked 'tor exit node'", ok, reason)
	}
	if ok, _ := b.IsAllowed("1.1.1.1"); !ok {
		t.Error("non-Tor IP should be allowed")
	}
}

func TestBlocklist_TorFetchFailureContinues(t *testing.T) {
	// Point at an unreachable URL — construction must succeed (graceful degrade).
	b, err := NewBlocklist(BlocklistConfig{BlockTor: true, TorListURL: "http://127.0.0.1:1/list"})
	if err != nil {
		t.Fatalf("NewBlocklist should not fail when Tor fetch fails: %v", err)
	}
	if ok, _ := b.IsAllowed("1.1.1.1"); !ok {
		t.Error("with no Tor list loaded, traffic should still be allowed")
	}
}

func TestBlocklist_AutoBanAfterThreshold(t *testing.T) {
	b, _ := NewBlocklist(BlocklistConfig{AutoBan: AutoBanConfig{
		Threshold: 3, Window: time.Minute, BanDuration: time.Hour,
	}})
	cur := time.Now()
	b.now = func() time.Time { return cur }

	// 4 requests (> threshold of 3) within the window trigger a ban.
	for i := 0; i < 4; i++ {
		b.RecordRequest("5.5.5.5")
	}
	if ok, reason := b.IsAllowed("5.5.5.5"); ok || reason != "auto-banned" {
		t.Errorf("5.5.5.5 = (%v, %q), want blocked 'auto-banned'", ok, reason)
	}
}

func TestBlocklist_AutoBanExpires(t *testing.T) {
	b, _ := NewBlocklist(BlocklistConfig{AutoBan: AutoBanConfig{
		Threshold: 2, Window: time.Minute, BanDuration: 10 * time.Minute,
	}})
	cur := time.Now()
	b.now = func() time.Time { return cur }

	for i := 0; i < 3; i++ {
		b.RecordRequest("4.4.4.4")
	}
	if ok, _ := b.IsAllowed("4.4.4.4"); ok {
		t.Fatal("should be banned right after threshold")
	}
	// Advance past the ban duration.
	cur = cur.Add(11 * time.Minute)
	if ok, _ := b.IsAllowed("4.4.4.4"); !ok {
		t.Error("ban should have expired after BanDuration")
	}
}

func TestBlocklist_ManualBlockUnblock(t *testing.T) {
	b, _ := NewBlocklist(BlocklistConfig{})
	b.ManualBlock("3.3.3.3", "abuse")
	if ok, reason := b.IsAllowed("3.3.3.3"); ok || reason != "manual block" {
		t.Errorf("after ManualBlock = (%v, %q), want blocked", ok, reason)
	}
	b.ManualUnblock("3.3.3.3")
	if ok, _ := b.IsAllowed("3.3.3.3"); !ok {
		t.Error("after ManualUnblock the IP should be allowed")
	}
}

func TestBlocklist_StatsAccurate(t *testing.T) {
	b, _ := NewBlocklist(BlocklistConfig{
		IPAllowlist: []string{"10.0.0.0/8", "127.0.0.1"},
		AutoBan:     AutoBanConfig{Threshold: 1, Window: time.Minute, BanDuration: time.Hour},
	})
	cur := time.Now()
	b.now = func() time.Time { return cur }

	b.ManualBlock("1.1.1.1", "x")
	for i := 0; i < 2; i++ {
		b.RecordRequest("10.0.0.9")
	}
	_, _ = b.IsAllowed("10.0.0.9")

	s := b.Stats()
	if s.AllowlistSize != 2 {
		t.Errorf("AllowlistSize = %d, want 2", s.AllowlistSize)
	}
	if s.ManualBlocks != 1 {
		t.Errorf("ManualBlocks = %d, want 1", s.ManualBlocks)
	}
	if s.AutoBans != 1 {
		t.Errorf("AutoBans = %d, want 1", s.AutoBans)
	}
	if s.TotalChecked < 1 {
		t.Errorf("TotalChecked = %d, want >= 1", s.TotalChecked)
	}
}

func TestBlocklist_CIDRInAllowlist(t *testing.T) {
	b, err := NewBlocklist(BlocklistConfig{IPAllowlist: []string{"172.16.0.0/12"}})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := b.IsAllowed("172.16.5.5"); !ok {
		t.Error("172.16.5.5 should match 172.16.0.0/12")
	}
	if ok, _ := b.IsAllowed("172.32.0.1"); ok {
		t.Error("172.32.0.1 is outside 172.16.0.0/12 and should be blocked")
	}
}

func TestBlocklist_InvalidCIDRErrors(t *testing.T) {
	if _, err := NewBlocklist(BlocklistConfig{IPAllowlist: []string{"not-an-ip"}}); err == nil {
		t.Error("invalid allowlist entry should error")
	}
}
