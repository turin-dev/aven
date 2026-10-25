package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// recIP는 ip 명령 대역이다(진짜 ip는 실행하지 않는다). eth0에 공인 주소 하나가 있고, 쓰기는 기록만 한다.
type recIP struct{ writes []string }

func (r *recIP) Run(_ context.Context, args ...string) (string, error) {
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "-j -6 addr show"):
		return `[{"ifname":"eth0","addr_info":[{"family":"inet6","local":"2001:db8:2::","prefixlen":64,"scope":"global","preferred_life_time":4294967295}]}]`, nil
	case line == "-j -6 route show default":
		return `[{"dst":"default","dev":"eth0"}]`, nil
	case strings.HasPrefix(line, "-6 addr "):
		r.writes = append(r.writes, line)
		return "", nil
	}
	return "", fmt.Errorf("unexpected ip %s", line)
}

func useIP(t *testing.T, r node.IPRunner) {
	old := ipRunner
	ipRunner = func() node.IPRunner { return r }
	t.Cleanup(func() { ipRunner = old })
}

func TestIPv6EnsureOp(t *testing.T) {
	ip := &recIP{}
	useIP(t, ip)
	h := &Handler{IPv6: "2001:db8:2::"}
	ctx := context.Background()
	out, err := h.Handle(ctx, agentprotocol.OpIPv6Ensure, []byte(`{"pool":"2001:db8:2:1::/112","addresses":["2001:db8:2:1::2"]}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*node.IPv6EnsureResult)
	if !res.Supported || res.Iface != "eth0" || res.Added != 1 {
		t.Fatalf("%+v", res)
	}
	if len(ip.writes) != 1 || ip.writes[0] != "-6 addr replace 2001:db8:2:1::2/128 dev eth0 preferred_lft 0 valid_lft forever nodad" {
		t.Fatalf("writes: %v", ip.writes)
	}

	// agent 설정(KEEL_IPV6_IFACE)이 서버가 보낸 인터페이스보다 우선한다.
	ip.writes = nil
	h.IPv6Iface = "ens5"
	out, err = h.Handle(ctx, agentprotocol.OpIPv6Ensure, []byte(`{"iface":"eth0","pool":"2001:db8:2:1::/112","addresses":["2001:db8:2:1::3"]}`), nil)
	if err != nil || out.(*node.IPv6EnsureResult).Iface != "ens5" {
		t.Fatalf("iface override: %+v %v", out, err)
	}

	// 다시 검사: 풀 밖 주소, 틀린 풀, 노드 공인 주소를 담는 풀, 모르는 필드, 틀린 인터페이스.
	ip.writes = nil
	h.IPv6Iface = ""
	for _, bad := range []string{
		`{"pool":"2001:db8:2:1::/112","addresses":["2001:db8:2::9"]}`,
		`{"pool":"2001:db8:2:1::/48","addresses":[]}`,
		`{"pool":"fd00::/80","addresses":[]}`,
		`{"pool":"2001:db8:2::/64","addresses":[]}`,
		`{"pool":"2001:db8:2:1::/112","addresses":[],"cmd":"rm -rf /"}`,
		`{"iface":"eth0; reboot","pool":"2001:db8:2:1::/112","addresses":[]}`,
	} {
		if _, err := h.Handle(ctx, agentprotocol.OpIPv6Ensure, []byte(bad), nil); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	if len(ip.writes) != 0 {
		t.Fatalf("rejected requests wrote: %v", ip.writes)
	}

	// ipv6.addrs: 읽기 전용.
	out, err = h.Handle(ctx, agentprotocol.OpIPv6Addrs, []byte(`{}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"iface":"eth0"`) || len(ip.writes) != 0 {
		t.Fatalf("addrs: %s", b)
	}
}

func TestIPv6OpsWithoutIP(t *testing.T) {
	useIP(t, nil)
	h := &Handler{}
	ctx := context.Background()
	// 붙일 것이 없으면 unsupported 결과, 있으면 오류.
	out, err := h.Handle(ctx, agentprotocol.OpIPv6Ensure, []byte(`{"pool":"2001:db8:2:1::/112","addresses":[]}`), nil)
	if err != nil || out.(*node.IPv6EnsureResult).Supported {
		t.Fatalf("%+v %v", out, err)
	}
	if _, err := h.Handle(ctx, agentprotocol.OpIPv6Ensure, []byte(`{"pool":"2001:db8:2:1::/112","addresses":["2001:db8:2:1::2"]}`), nil); err == nil {
		t.Fatal("want unsupported error")
	}
	if _, err := h.Handle(ctx, agentprotocol.OpIPv6Addrs, []byte(`{}`), nil); err == nil {
		t.Fatal("want unsupported error")
	}
}
