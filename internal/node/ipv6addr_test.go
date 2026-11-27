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
	addrs   map[string][]HostIPv6Addr // iface → 주소
	route   string                    // 기본 경로 인터페이스
	writes  []string
	failOn  string
	noProto bool // proto를 모르는 ip
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
				if a.Aven {
					m["protocol"] = AddrProto
				}
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
		if f.noProto && strings.Contains(line, " proto ") {
			return "", fmt.Errorf(`Error: either "local" is duplicate, or "proto" is a garbage.`)
		}
		a := strings.TrimSuffix(args[3], "/128")
		ifn := args[5]
		var keep []HostIPv6Addr
		for _, h := range f.addrs[ifn] {
			if !(h.Addr == a && h.Bits == 128) {
				keep = append(keep, h)
			}
		}
		f.addrs[ifn] = append(keep, HostIPv6Addr{Iface: ifn, Addr: a, Bits: 128, Scope: "global", Deprecated: true, Aven: strings.HasSuffix(line, " proto "+AddrProto)})
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
		"2001:db8:6b:1943::/48", "2001:db8:6b:1943::/121", "2001:db8:6b:1943::2"} {
		if _, err := ParseIPv6Pool(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	// 호스트 비트는 잘라 네트워크 주소로 돌려준다.
	for in, want := range map[string]string{"2001:db8:6b:1943:d::/64": "2001:db8:6b:1943::/64", "2001:db8:6b:1943:d::5/80": "2001:db8:6b:1943:d::/80"} {
		if p, err := ParseIPv6Pool(in); err != nil || p.String() != want {
			t.Errorf("%s: %v %v", in, p, err)
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
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::9", Bits: 128, Scope: "global", Deprecated: true, Aven: true},
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
		"-6 addr replace 2001:db8:6b:1943:d::2/128 dev eth0 preferred_lft 0 valid_lft forever nodad proto 0xaa",
		"-6 addr replace 2001:db8:6b:1943:d::3/128 dev eth0 preferred_lft 0 valid_lft forever nodad proto 0xaa",
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
	// 네트워크 주소가 아닌 풀은 거부한다.
	if _, err := IPv6Ensure(ctx, f, "eth0", netip.MustParsePrefix("2001:db8:6b:1943:d::/64"), nil, nil); err == nil || len(f.writes) != 0 {
		t.Fatalf("pool with host bits: %v %v", err, f.writes)
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
	// 기본 IPv6나 이미 쓰는 주소·서브넷을 담아도 된다(배정에서 건너뛴다).
	for _, ok := range []string{"2001:db8:6b:1943:d::/80", "2001:db8:6b:1943::/64", "2001:db8:6b:1943::/120", "2001:db8:6b:1943:66::/96"} {
		if err := CheckIPv6PoolOnHost(netip.MustParsePrefix(ok), host); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	// 풀 전체가 작은 서브넷(wg0 /112) 안이면 거부한다.
	for _, bad := range []string{"2001:db8:6b:1943:66::100/120", "2001:db8:6b:1943:66::/112"} {
		if err := CheckIPv6PoolOnHost(netip.MustParsePrefix(bad), host); err == nil || !strings.Contains(err.Error(), "서브넷") {
			t.Errorf("%s inside wg0: %v", bad, err)
		}
	}
}

func TestIPv6Reservation(t *testing.T) {
	f := newFakeIP()
	f.addrs["eth0"] = append(f.addrs["eth0"],
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::2", Bits: 128, Scope: "global", Deprecated: true},             // 배정된 aven 주소(표시 없음)
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::3", Bits: 128, Scope: "global", Deprecated: true, Aven: true}, // 풀린 aven 주소
	)
	host, _ := ListHostIPv6(context.Background(), f, "")
	wide := netip.MustParsePrefix("2001:db8:6b:1943::/64")
	managed := map[netip.Addr]bool{netip.MustParseAddr("2001:db8:6b:1943:d::2"): true}
	got, prefixes := IPv6Reservation(wide, host, managed, addrs("2001:db8:6b:1943::2"))
	var keys []string
	for a := range got {
		keys = append(keys, a.String())
	}
	sort.Strings(keys)
	if strings.Join(keys, " ") != "2001:db8:6b:1943:66::1 2001:db8:6b:1943::2 2001:db8:6b:1943::25" {
		t.Fatalf("reserved: %v", keys)
	}
	if len(prefixes) != 1 || prefixes[0].String() != "2001:db8:6b:1943:66::/112" {
		t.Fatalf("prefixes: %v", prefixes)
	}
	// 전용 풀에는 예약이 없다.
	if got, prefixes := IPv6Reservation(mainPool, host, managed, addrs("2001:db8:6b:1943::2")); len(got) != 0 || len(prefixes) != 0 {
		t.Fatalf("main pool: %v %v", got, prefixes)
	}
}

// 공유 풀(호스팅이 준 /64 전체): 기본 IPv6, Mailu ::25(표시 없는 /128 deprecated), wg0 서브넷은 절대 건드리지 않는다.
func TestIPv6EnsureSharedPool(t *testing.T) {
	ctx := context.Background()
	f := newFakeIP()
	wide := netip.MustParsePrefix("2001:db8:6b:1943::/64")
	protect := addrs("2001:db8:6b:1943::2")
	// 옛 풀(d::/80)에서 붙인 표시 없는 주소 두 개: d::2는 계속 원하고 d::5는 더 이상 원하지 않는다.
	f.addrs["eth0"] = append(f.addrs["eth0"],
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::2", Bits: 128, Scope: "global", Deprecated: true},
		HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::5", Bits: 128, Scope: "global", Deprecated: true},
	)
	res, err := IPv6Ensure(ctx, f, "eth0", wide, addrs("2001:db8:6b:1943::3", "2001:db8:6b:1943:d::2"), protect)
	if err != nil {
		t.Fatal(err)
	}
	wantWrites := []string{
		"-6 addr replace 2001:db8:6b:1943::3/128 dev eth0 preferred_lft 0 valid_lft forever nodad proto 0xaa",
		"-6 addr replace 2001:db8:6b:1943:d::2/128 dev eth0 preferred_lft 0 valid_lft forever nodad proto 0xaa", // 표시만 단다
	}
	if strings.Join(f.writes, "\n") != strings.Join(wantWrites, "\n") {
		t.Fatalf("writes:\n%s", strings.Join(f.writes, "\n"))
	}
	if res.Added != 1 || res.Removed != 0 || res.Present != 2 {
		t.Fatalf("result: %+v", res)
	}
	// 모두 풀어도 aven 표시가 있는 주소만 지운다.
	f.writes = nil
	res, err = IPv6Ensure(ctx, f, "eth0", wide, nil, protect)
	if err != nil || res.Removed != 2 {
		t.Fatalf("clear: %+v %v %v", res, err, f.writes)
	}
	for _, keep := range []string{"2001:db8:6b:1943::2", "2001:db8:6b:1943::25", "2001:db8:6b:1943:d::5"} {
		found := false
		for _, h := range f.addrs["eth0"] {
			found = found || h.Addr == keep
		}
		if !found {
			t.Fatalf("%s was removed", keep)
		}
	}
	if len(f.addrs["wg0"]) != 1 {
		t.Fatal("wg0 touched")
	}
	// 기본 IPv6, 다른 주소, 서브넷 안의 주소는 원해도 붙이지 않는다.
	f.writes = nil
	_, err = IPv6Ensure(ctx, f, "eth0", wide, addrs("2001:db8:6b:1943::2", "2001:db8:6b:1943:66::5"), protect)
	if err == nil || len(f.writes) != 0 {
		t.Fatalf("blocked addresses: %v %v", err, f.writes)
	}
}

// 전용 풀의 표시 없는 옛 aven 주소는 예전처럼 지운다. proto를 모르는 ip면 표시 없이 붙인다.
func TestIPv6EnsureLegacyAndNoProto(t *testing.T) {
	ctx := context.Background()
	f := newFakeIP()
	f.noProto = true
	f.addrs["eth0"] = append(f.addrs["eth0"], HostIPv6Addr{Iface: "eth0", Addr: "2001:db8:6b:1943:d::9", Bits: 128, Scope: "global", Deprecated: true})
	res, err := IPv6Ensure(ctx, f, "eth0", mainPool, addrs("2001:db8:6b:1943:d::2"), addrs("2001:db8:6b:1943::2"))
	if err != nil || res.Added != 1 || res.Removed != 1 || res.Present != 1 {
		t.Fatalf("%+v %v %v", res, err, f.writes)
	}
	if last := f.writes[len(f.writes)-1]; strings.Contains(last, "proto") {
		t.Fatalf("no fallback: %v", f.writes)
	}
}

func TestSystemIPRunnerNeedsRoot(t *testing.T) {
	if r := SystemIPRunner(); r != nil && !isRoot() {
		t.Fatal("non-root got a real ip runner")
	}
}
