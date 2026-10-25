package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseTTL(t *testing.T) {
	for in, want := range map[string]int{"30m": 1800, "1h": 3600, "2h": 7200, "4h": 14400, "12h": 43200, "1d": 86400, "14400": 14400} {
		if got, err := parseTTL(in); err != nil || got != want {
			t.Errorf("%s: %d %v", in, got, err)
		}
	}
	for _, bad := range []string{"5m", "100", "", "3h"} {
		if _, err := parseTTL(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseRateLimit(t *testing.T) {
	rl, err := parseRateLimit("100/1m")
	if err != nil || rl["requests"] != 100 || rl["window_seconds"] != 60 {
		t.Fatalf("%v %v", rl, err)
	}
	if rl, err := parseRateLimit("5/30"); err != nil || rl["window_seconds"] != 30 {
		t.Fatalf("%v %v", rl, err)
	}
	if rl, err := parseRateLimit("off"); err != nil || rl != nil {
		t.Fatalf("off: %v %v", rl, err)
	}
	for _, bad := range []string{"100", "x/60s", "10/2h", "10/500ms"} {
		if _, err := parseRateLimit(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestEdgeFlagsReorder(t *testing.T) {
	got := reorderFlags([]string{"update", "--html-caching", "force", "--default-ttl", "4h", "-s", "web"})
	if strings.Join(got, " ") != "--html-caching force --default-ttl 4h -s web update" {
		t.Fatalf("%v", got)
	}
}

func TestFormatEdge(t *testing.T) {
	var v edgeView
	until := time.Date(2026, 1, 1, 13, 0, 0, 0, time.UTC)
	json.Unmarshal([]byte(`{"cdn":{"enabled":true,"html_caching":"force","default_ttl_seconds":14400,"honor_swr":false,"purge_on_deploy":"all"},
	  "waf":{"allow_ips":["203.0.113.7"],"deny_ips":[],"rate_limit":{"requests":100,"window_seconds":60},"managed_rules":true,
	  "under_attack":{"since":"2026-01-01T12:00:00Z","until":"2026-01-01T13:00:00Z"}},"available":true,"under_attack_active":true}`), &v)
	cdn := formatCDN("web", "production", &v)
	for _, want := range []string{"CDN caching enabled", "html caching: force", "default ttl: 4h", "honor stale-while-revalidate: false", "purge on deploy: all", "last purged: html never"} {
		if !strings.Contains(cdn, want) {
			t.Errorf("cdn output missing %q:\n%s", want, cdn)
		}
	}
	waf := formatWAF("web", "production", &v, until.Add(-30*time.Minute))
	for _, want := range []string{"Under Attack Mode ON, 30m0s left", "allow ips: 203.0.113.7", "deny ips: -", "rate limit: 100 requests / 60s", "managed rules: true"} {
		if !strings.Contains(waf, want) {
			t.Errorf("waf output missing %q:\n%s", want, waf)
		}
	}
	v.UnderAttackActive = false
	if got := formatUnderAttack("web", "production", &v, until); !strings.Contains(got, "off") {
		t.Errorf("off: %s", got)
	}
}
