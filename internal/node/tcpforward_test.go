package node

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeTCPAllowCIDRs(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want []string
		err  string
	}{
		{in: nil, want: nil},
		{in: []string{" ", ""}, want: nil},
		{in: []string{"203.0.113.7"}, want: []string{"203.0.113.7/32"}},
		{in: []string{"10.1.2.3/8", "10.0.0.0/8", "192.168.1.0/24"}, want: []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{in: []string{"192.168.1.0/24", "10.0.0.0/8"}, want: []string{"10.0.0.0/8", "192.168.1.0/24"}},
		{in: []string{"10.0.0.0/16", "10.0.0.0/8"}, want: []string{"10.0.0.0/8", "10.0.0.0/16"}},
		{in: []string{"::ffff:198.51.100.1"}, want: []string{"198.51.100.1/32"}},
		{in: []string{"10.0.0.0/8", "0.0.0.0/0"}, want: []string{TCPAllowAll}},
		{in: []string{"2001:db8::/32"}, err: "only IPv4"},
		{in: []string{"10.0.0.0/33"}, err: "not an IP"},
		{in: []string{"nope"}, err: "not an IP"},
		{in: []string{"1.2.3.4,5.6.7.8"}, err: "not an IP"},
	} {
		got, err := NormalizeTCPAllowCIDRs(tc.in)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%v: err %v, want %q", tc.in, err, tc.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%v: got %v, %v; want %v", tc.in, got, err, tc.want)
		}
	}
	many := make([]string, MaxTCPAllowCIDRs+1)
	for i := range many {
		many[i] = fmt.Sprintf("10.0.%d.0/24", i)
	}
	if _, err := NormalizeTCPAllowCIDRs(many[:MaxTCPAllowCIDRs]); err != nil {
		t.Errorf("a list of %d ranges was refused: %v", MaxTCPAllowCIDRs, err)
	}
	if _, err := NormalizeTCPAllowCIDRs(many); err == nil || !strings.Contains(err.Error(), "at most") {
		t.Errorf("an over-long list: %v", err)
	}
}

func TestTCPForwardWithoutAllowlistIsUnchanged(t *testing.T) {
	f := TCPForward{Port: 5432, Target: "db.production.keel.internal"}
	if got, want := f.Signature(20001), "20001:5432:db.production.keel.internal"; got != want {
		t.Errorf("signature %q, want the pre-allowlist value %q", got, want)
	}
	if got, want := f.Cmd(), []string{"TCP-LISTEN:5432,fork,reuseaddr", "TCP:db.production.keel.internal:5432"}; !reflect.DeepEqual(got, want) {
		t.Errorf("cmd %v, want %v", got, want)
	}
	if f.Env() != nil {
		t.Error("env set without an allowlist")
	}
	open := TCPForward{Port: 5432, Target: "db.production.keel.internal", Allow: []string{TCPAllowAll}}
	if open.Signature(20001) != f.Signature(20001) || !reflect.DeepEqual(open.Cmd(), f.Cmd()) {
		t.Error("an explicit 0.0.0.0/0 should be the plain forwarder")
	}
	if got := EffectiveTCPAllow(nil, []string{"10.0.0.0/8"}); !reflect.DeepEqual(got, []string{"10.0.0.0/8"}) {
		t.Errorf("default not applied: %v", got)
	}
	if got := EffectiveTCPAllow([]string{TCPAllowAll}, []string{"10.0.0.0/8"}); !reflect.DeepEqual(got, []string{TCPAllowAll}) {
		t.Errorf("own list should win over the default: %v", got)
	}
}

func TestTCPForwardWithAllowlist(t *testing.T) {
	f := TCPForward{Port: 5432, Target: "db.production.keel.internal", Allow: []string{"10.0.0.0/8", "203.0.113.7/32"}}
	if sig := f.Signature(20001); !strings.HasSuffix(sig, ":allow=10.0.0.0/8,203.0.113.7/32") {
		t.Errorf("signature %q does not include the allowlist", sig)
	}
	cmd := f.Cmd()
	if len(cmd) != 2 || strings.Contains(cmd[1], ",") {
		t.Errorf("cmd %v: the SYSTEM address must not contain commas", cmd)
	}
	env := f.Env()
	if env["AVEN_TCP_ALLOW"] != "10.0.0.0/8 203.0.113.7/32" || env["AVEN_TCP_TARGET"] != "db.production.keel.internal:5432" {
		t.Errorf("env %v", env)
	}
	if strings.Contains(env["AVEN_TCP_CHECK"], ",") {
		t.Error("the check script must not contain commas")
	}
}
