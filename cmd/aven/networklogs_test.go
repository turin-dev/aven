package main

import (
	"strings"
	"testing"
)

func TestNetLogKind(t *testing.T) {
	for args, want := range map[string]string{
		"--dns": "dns", "-network --port 5432": "network", "--dns=true": "dns", "--dns=false": "", "-n 10": "", "-- --dns": "",
		"--filter dns": "",
	} {
		if got := netLogKind(strings.Fields(args)); got != want {
			t.Errorf("%q: %q want %q", args, got, want)
		}
	}
}

func TestNetLogQuery(t *testing.T) {
	got := netLogQuery("dns", map[string]string{"qtype": "aaaa", "rcode": "nxdomain", "domain": "example.com", "port": "1"}, "-foo")
	if got != "@domain:example.com @qtype:AAAA @rcode:NXDOMAIN -foo" {
		t.Fatalf("dns: %q", got)
	}
	got = netLogQuery("network", map[string]string{"peer": "my svc", "port": ">=5000", "direction": "egress"}, "")
	if got != `@direction:egress "@peer:my svc" @port:>=5000` {
		t.Fatalf("network: %q", got)
	}
}

func TestFormatNetLog(t *testing.T) {
	line, ok := formatNetLog("dns", `{"t":"2026-10-18T12:00:00Z","zone":"external","qtype":"A","rcode":"NOERROR","qname":"api.example.com","answers":["203.0.113.10"]}`)
	if !ok || !strings.Contains(line, "external") || !strings.Contains(line, "api.example.com") || !strings.HasSuffix(line, "203.0.113.10") {
		t.Fatalf("dns: %q", line)
	}
	line, ok = formatNetLog("network", `{"t":"2026-10-18T12:00:00Z","direction":"egress","protocol":"tcp","src_addr":"fd00::2","src_port":40000,
		"dst_addr":"10.0.0.3","dst_port":5432,"peer":"postgres","bytes_sent":2048,"bytes_received":10,"latency_ms":1.25,"status":"ok","flow_state":"active"}`)
	for _, want := range []string{"egress", "[fd00::2]:40000", "10.0.0.3:5432", "postgres", "↑2.0K ↓10B", "1.2ms", "ok (active)"} {
		if !ok || !strings.Contains(line, want) {
			t.Fatalf("network: %q missing %q", line, want)
		}
	}
	if _, ok := formatNetLog("dns", "not json"); ok {
		t.Fatal("bad json formatted")
	}
}
