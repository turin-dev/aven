package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"sort"
	"strings"
	"testing"
)

// FakeIP는 ip 명령 대역이다. 인터페이스별 주소 상태를 들고 있고 addr show/route show/replace/del만 흉내 낸다.
// 진짜 ip는 실행하지 않는다.
type fakeIP struct {
	addrs  map[string][]HostIPv6Addr // iface → 주소
	route  string                    // 기본 경로 인터페이스
	writes []string
	failOn string
}

func newFakeIP() *fakeIP {
	return &fakeIP{route: "eth0", addrs: map[string][]HostIPv6Addr{
		"eth0": {
			{Iface: "eth0", Addr: "2001:db8:6b:1943::25", Bits: 128, Scope: "global", Deprecated: true},
			{Iface: "eth0", Addr: "2001:db8:6b:1943::2", Bits: 64, Scope: "global"},
			{Iface: "eth0", Addr: "fe80::1", Bits: 64, Scope: "link"},
		},
		"wg0": {{Iface: "wg0", Addr: "2001:db8:6b:1943:66::1", Bits: 112, Scope: "global"}},
	}}
}

func (f *fakeIP) Run(_ context.Context, args ...string) (string, error) {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "-j -6 addr show"):
		var ifs []string
		if len(args) == 6 {
			if _, ok := f.addrs[args[5]]; !ok {
				return "", fmt.Errorf("Device %q does not exist", args[5])
			}
			ifs = []string{args[5]}
		} else {
			for k := range f.addrs {
				ifs = append(ifs, k)
			}
			sort.Strings(ifs)
		}
		var out []map[string]any
		for _, ifn := range ifs {
			var ai []map[string]any
			for _, a := range f.addrs[ifn] {
				m := map[string]any{"family": "inet6", "local": a.Addr, "prefixlen": a.Bits, "scope": a.Scope, "valid_life_time": 4294967295}
				if a.Deprecated {
					m["deprecated"] = true
					m["preferred_life_time"] = 0
				} else {
					m["preferred_life_time"] = 4294967295
				}
				ai = append(ai, m)
			}
			out = append(out, map[string]any{"ifname": ifn, "addr_info": ai})
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	case line == "-j -6 route show default":
		if f.route == "" {
			return "[]", nil
		}
		return fmt.Sprintf(`[{"dst":"default","gateway":"fe80::1","dev":%q}]`, f.route), nil
	case strings.HasPrefix(line, "-6 addr replace "):
		f.writes = append(f.writes, line)
		if f.failOn != "" && strings.Contains(line, f.failOn) {
			return "", fmt.Errorf("RTNETLINK answers: boom")
		}
		a := strings.TrimSuffix(args[3], "/128")
		ifn := args[5]
		f.addrs[ifn] = append(f.addrs[ifn], HostIPv6Addr{Iface: ifn, Addr: a, Bits: 128, Scope: "global", Deprecated: true})
		return "", nil
	case strings.HasPrefix(line, "-6 addr del "):
		f.writes = append(f.writes, line)
		a := strings.TrimSuffix(args[3], "/128")
		ifn := args[5]
		var keep []HostIPv6Addr
		for _, h := range f.addrs[ifn] {
			if !(h.Addr == a && h.Bits == 128) {
				keep = append(keep, h)
			}
		}
		f.addrs[ifn] = keep
		return "", nil
	}
	return "", fmt.Errorf("unexpected ip %s", line)
}

func addrs(s ...string) []netip.Addr {
	var out []netip.Addr
	for _, x := range s {
		out = append(out, netip.MustParseAddr(x))
	}
	return out
}

var mainPool = netip.MustParsePrefix("2001:db8:6b:1943:d::/80")

func TestParseIPv6Pool(t *testing.T) {
	for _, ok := range []string{"2001:db8:6b:1943:d::/80", "2001:db8:6b:1943::/64", "2001:db8:6b:1943:d::100/120"} {
		if _, err := ParseIPv6Pool(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "nope", "10.0.0.0/24", "::ffff:10.0.0.0/120", "fd00:1::/80", "fe80::/80", "ff02::/80",
		"2001:db8:6b:1943::/48", "2001:db8:6b:1943::/121", "2001:db8:6b:1943:d::5/80", "2001:db8:6b:1943::2"} {
		if _, err := ParseIPv6Pool(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, _, err := ParseIPv6PoolAddresses("2001:db8:6b:1943:d::/80", []string{"2001:db8:6b:1943::2"}); err == nil {
		t.Error("address outside the pool accepted")
	}
	if _, a, err := ParseIPv6PoolAddresses("2001:db8:6b:1943:d::/80", []string{"2001:db8:6b:1943:d::3", "2001:db8:6b:1943:d::2", "2001:db8:6b:1943:d::3"}); err != nil || len(a) != 2 || a[0].String() != "2001:db8:6b:1943:d::2" {
		t.Errorf("dedupe/sort: %v %v", a, err)
	}
}

func TestIPv6EnsureReconcile(t *testing.T) {
	ctx := context.Background()
	f := newFakeIP()
	// 풀 안의 오래된 aven 주소, 풀 안의 aven 모양이 아닌 주소(/64 아님, preferred 살아 있음), 풀 밖의 /128 deprecated.
	f.addrs["eth0"] = append(f.addrs["eth0"],
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::9", Bits: 128, Scope: "global", Deprecated: true},
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::77", Bits: 128, Scope: "global"},
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:e::9", Bits: 128, Scope: "global", Deprecated: true},
	)
	want := addrs("2001:db8:6b:1943:d::2", "2001:db8:6b:1943:d::3")
	protect := addrs("2001:db8:6b:1943::2")
	res, err := IPv6Ensure(ctx, f, "eth0", mainPool, want, protect)
	if err != nil {
		t.Fatal(err)
	}
	wantWrites := []string{
		"-6 addr del 2001:db8:6b:1943:d::9/128 dev eth0",
		"-6 addr replace 2001:db8:6b:1943:d::2/128 dev eth0 preferred_lft 0 valid_lft forever nodad",
		"-6 addr replace 2001:db8:6b:1943:d::3/128 dev eth0 preferred_lft 0 valid_lft forever nodad",
	}
	if strings.Join(f.writes, "\n") != strings.Join(wantWrites, "\n") {
		t.Fatalf("writes:\n%s", strings.Join(f.writes, "\n"))
	}
	if !res.Supported || res.Added != 2 || res.Removed != 1 || res.Present != 2 || res.Desired != 2 || len(res.Foreign) != 1 || res.Foreign[0] != "2001:db8:6b:1943:d::77/128" {
		t.Fatalf("result: %+v", res)
	}
	// 풀 밖의 주소(::2, ::25, e::9, wg0)는 그대로다.
	for _, keep := range []string{"2001:db8:6b:1943::2", "2001:db8:6b:1943::25", "2001:db8:6b:1943:e::9", "2001:db8:6b:1943:d::77"} {
		found := false
		for _, h := range f.addrs["eth0"] {
			found = found || h.Addr == keep
		}
		if !found {
			t.Fatalf("%s was touched", keep)
		}
	}
	if len(f.addrs["wg0"]) != 1 {
		t.Fatal("wg0 touched")
	}
	// 두 번째 실행: 바꿀 것이 없으면 쓰기 명령이 없다.
	f.writes = nil
	res, err = IPv6Ensure(ctx, f, "eth0", mainPool, want, protect)
	if err != nil || len(f.writes) != 0 || res.Added != 0 || res.Removed != 0 || res.Present != 2 {
		t.Fatalf("not idempotent: %+v %v %v", res, err, f.writes)
	}
	// 원하는 주소가 비면 풀 안의 aven 주소만 모두 지운다.
	res, err = IPv6Ensure(ctx, f, "eth0", mainPool, nil, protect)
	if err != nil || res.Removed != 2 {
		t.Fatalf("clear: %+v %v", res, err)
	}
}

func TestIPv6EnsureGuards(t *testing.T) {
	ctx := context.Background()
	f := newFakeIP()
	// 풀이 노드의 기본 주소를 담으면 아무것도 하지 않는다.
	wide := netip.MustParsePrefix("2001:db8:6b:1943::/64")
	if _, err := IPv6Ensure(ctx, f, "eth0", wide, nil, addrs("2001:db8:6b:1943::2")); err == nil || len(f.writes) != 0 {
		t.Fatalf("pool containing the main address: %v %v", err, f.writes)
	}
	// 풀 밖의 원하는 주소는 거부한다.
	if _, err := IPv6Ensure(ctx, f, "eth0", mainPool, addrs("2001:db8:6b:1943::99"), nil); err == nil {
		t.Fatal("address outside pool accepted")
	}
	// 인터페이스 이름 검사.
	if _, err := IPv6Ensure(ctx, f, "-x", mainPool, addrs("2001:db8:6b:1943:d::2"), nil); err == nil {
		t.Fatal("bad iface accepted")
	}
	// ip를 쓸 수 없는 노드: 붙일 것이 없으면 오류 없이 unsupported, 있으면 ErrIPv6Unsupported.
	res, err := IPv6Ensure(ctx, nil, "", mainPool, nil, nil)
	if err != nil || res.Supported {
		t.Fatalf("nil runner empty: %+v %v", res, err)
	}
	if _, err := IPv6Ensure(ctx, nil, "", mainPool, addrs("2001:db8:6b:1943:d::2"), nil); err == nil || !strings.Contains(err.Error(), ErrIPv6Unsupported.Error()) {
		t.Fatalf("nil runner: %v", err)
	}
	// 붙이기가 하나 실패해도 나머지는 계속한다.
	f.failOn = "d::2/128"
	res, err = IPv6Ensure(ctx, f, "eth0", mainPool, addrs("2001:db8:6b:1943:d::2", "2001:db8:6b:1943:d::3"), nil)
	if err == nil || res.Added != 1 || res.Present != 1 {
		t.Fatalf("partial: %+v %v", res, err)
	}
}

func TestDefaultIPv6Iface(t *testing.T) {
	ctx := context.Background()
	f := newFakeIP()
	f.route = "eth9"
	if got, err := DefaultIPv6Iface(ctx, f, "2001:db8:6b:1943::2"); err != nil || got != "eth0" {
		t.Fatalf("by address: %q %v", got, err)
	}
	if got, err := DefaultIPv6Iface(ctx, f, ""); err != nil || got != "eth9" {
		t.Fatalf("by route: %q %v", got, err)
	}
	f.route = ""
	if _, err := DefaultIPv6Iface(ctx, f, "2001:db8::1"); err == nil {
		t.Fatal("want error without a default route")
	}
}

func TestCheckIPv6PoolOnHost(t *testing.T) {
	f := newFakeIP()
	host, err := ListHostIPv6(context.Background(), f, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range host {
		if h.Scope == "link" {
			t.Fatal("link-local listed")
		}
	}
	protect := addrs("2001:db8:6b:1943::2")
	if err := CheckIPv6PoolOnHost(mainPool, host, nil, protect); err != nil {
		t.Fatalf("main pool: %v", err)
	}
	cases := map[string]string{
		"2001:db8:6b:1943::/64":        "기본 IPv6",
		"2001:db8:6b:1943::/120":       "기본 IPv6", // ::2 포함
		"2001:db8:6b:1943::20/123":     "이미 쓰는 주소",
		"2001:db8:6b:1943:66::/120":    "이미 쓰는 주소", // wg0 ::1
		"2001:db8:6b:1943:66::/96":     "이미 쓰는 주소",
		"2001:db8:6b:1943:66::100/120": "겹칩니다", // wg0 /112 서브넷
	}
	for pool, want := range cases {
		err := CheckIPv6PoolOnHost(netip.MustParsePrefix(pool), host, nil, protect)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v (want %q)", pool, err, want)
		}
	}
	// aven이 배정한 주소가 이미 붙어 있으면 허용한다.
	host = append(host, HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::2", Bits: 128, Deprecated: true})
	if err := CheckIPv6PoolOnHost(mainPool, host, map[netip.Addr]bool{netip.MustParseAddr("2001:db8:6b:1943:d::2"): true}, protect); err != nil {
		t.Fatal(err)
	}
	if err := CheckIPv6PoolOnHost(mainPool, host, nil, protect); err == nil {
		t.Fatal("unmanaged in-pool address accepted")
	}
}

func TestSystemIPRunnerNeedsRoot(t *testing.T) {
	if r := SystemIPRunner(); r != nil && !isRoot() {
		t.Fatal("non-root got a real ip runner")
	}
}
