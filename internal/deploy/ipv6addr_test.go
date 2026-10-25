package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// ipv6Hub는 ipv6.addrs/ipv6.ensure만 흉내 내는 hub다. old에 든 노드는 옛 agent처럼 작업을 모른다고 답한다.
type ipv6Hub struct {
	mu      sync.Mutex
	old     map[string]bool
	offline map[string]bool
	ensures map[string][]agentprotocol.IPv6EnsureRequest
	addrs   []node.HostIPv6Addr
}

func (h *ipv6Hub) Online(n string) bool { return !h.offline[n] }
func (h *ipv6Hub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *ipv6Hub) CallStream(_ context.Context, nodeID string, op string, req, res any, _ func(json.RawMessage)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.old[nodeID] || (op != agentprotocol.OpIPv6Ensure && op != agentprotocol.OpIPv6Addrs) {
		return &agentprotocol.RemoteError{Msg: fmt.Sprintf("operation %q is not allowed", op)}
	}
	var out any
	switch op {
	case agentprotocol.OpIPv6Addrs:
		out = agentprotocol.IPv6AddrsResult{Addrs: h.addrs}
	case agentprotocol.OpIPv6Ensure:
		r := req.(agentprotocol.IPv6EnsureRequest)
		if h.ensures == nil {
			h.ensures = map[string][]agentprotocol.IPv6EnsureRequest{}
		}
		h.ensures[nodeID] = append(h.ensures[nodeID], r)
		out = node.IPv6EnsureResult{Supported: true, Iface: "eth0", Pool: r.Pool, Desired: len(r.Addresses), Present: len(r.Addresses), Added: len(r.Addresses)}
	}
	b, _ := json.Marshal(out)
	return json.Unmarshal(b, res)
}

// localIP는 로컬 노드 ip 대역이다(eth0에 기본 주소, 쓰기는 상태에 반영하고 기록한다).
type localIP struct {
	mu     sync.Mutex
	extra  []string // 붙은 /128 deprecated 주소
	writes []string
}

func (l *localIP) Run(_ context.Context, args ...string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	line := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(line, "-j -6 addr show"):
		ai := []string{`{"family":"inet6","local":"2001:db8:6b:1943::2","prefixlen":64,"scope":"global","preferred_life_time":4294967295}`}
		for _, a := range l.extra {
			ai = append(ai, fmt.Sprintf(`{"family":"inet6","local":%q,"prefixlen":128,"scope":"global","deprecated":true,"preferred_life_time":0}`, a))
		}
		return `[{"ifname":"eth0","addr_info":[` + strings.Join(ai, ",") + `]}]`, nil
	case line == "-j -6 route show default":
		return `[{"dst":"default","dev":"eth0"}]`, nil
	case strings.HasPrefix(line, "-6 addr replace "):
		l.writes = append(l.writes, line)
		l.extra = append(l.extra, strings.TrimSuffix(args[3], "/128"))
	case strings.HasPrefix(line, "-6 addr del "):
		l.writes = append(l.writes, line)
		a := strings.TrimSuffix(args[3], "/128")
		var keep []string
		for _, e := range l.extra {
			if e != a {
				keep = append(keep, e)
			}
		}
		l.extra = keep
	default:
		return "", fmt.Errorf("unexpected ip %s", line)
	}
	return "", nil
}

func TestIPv6OldAgent(t *testing.T) {
	ctx := context.Background()
	h := &ipv6Hub{old: map[string]bool{"node_remote": true}}
	d := &remoteDriver{hub: h, node: "node_remote"}
	if _, err := d.IPv6Ensure(ctx, agentprotocol.IPv6EnsureRequest{Pool: "2a01:db8::/120"}); !errors.Is(err, ErrIPv6AgentUnsupported) {
		t.Fatalf("ensure: %v", err)
	}
	if _, err := d.IPv6Addrs(ctx); !errors.Is(err, ErrIPv6AgentUnsupported) {
		t.Fatalf("addrs: %v", err)
	}
	// ensureNodeIPv6: 옛 agent는 오류가 아니라 supported=false로 기록하고 건너뛴다.
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Manager{Store: st, Agents: h}
	pool, _ := node.ParseIPv6Pool("2a01:db8::/120")
	if err := m.ensureNodeIPv6(ctx, "node_remote", pool, nil); err != nil {
		t.Fatalf("old agent should not fail the sync: %v", err)
	}
	s := m.ipv6StatusRaw("node_remote")
	if s == nil || s.Supported == nil || *s.Supported || !strings.Contains(s.Reason, "업그레이드") {
		t.Fatalf("status: %+v", s)
	}
	// 풀 검증은 노드에 물어볼 수 없으면 실패한다.
	if err := m.CheckIPv6Pool(ctx, "node_remote", pool); err == nil {
		t.Fatal("pool check on an old agent should fail")
	}
}

func TestSyncDomainIPv6(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mkNode := func(name, v6 string) *store.Node {
		st.CreateJoinToken(ctx, "h-"+name, name, time.Now().Add(time.Hour))
		n, err := st.ConsumeJoinToken(ctx, "h-"+name, "")
		if err != nil {
			t.Fatal(err)
		}
		st.SetNodeInfo(ctx, n.ID, store.NodeAddress{IPv6: &v6}, store.NodeInfo{})
		return n
	}
	us, remote := mkNode("us", "2001:db8:2::"), mkNode("remote", "")
	if err := st.SetNodeIPv6Pool(ctx, us.ID, "2001:db8:2:1::/112"); err != nil {
		t.Fatal(err)
	}
	h := &ipv6Hub{}
	lip := &localIP{}
	m := &Manager{Store: st, Agents: h, IPRunner: lip,
		Cfg: Config{PublicIPv6: "2001:db8:6b:1943::2", IPv6Pool: "2001:db8:6b:1943:d::/80"}}

	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	place := func(name, nodeID string, hosts ...string) *store.Instance {
		svc := &store.Service{ProjectID: p.ID, Name: name, SourceType: "image"}
		if err := st.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, NodeID: nodeID}
		for _, h := range hosts {
			in.Domains = append(in.Domains, store.Domain{Host: h, Kind: "custom"})
		}
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond) // created_at 순서
		return in
	}
	place("web", "", "a.example.com", "b.example.com")
	api := place("api", "", "api.example.com")
	place("sample", remote.ID, "app.example.com") // 풀 없는 노드: 주소 없음

	if err := m.SyncDomainIPv6(ctx); err != nil {
		t.Fatal(err)
	}
	for h, want := range map[string]string{"a.example.com": "2001:db8:6b:1943:d::2", "b.example.com": "2001:db8:6b:1943:d::3",
		"api.example.com": "2001:db8:6b:1943:d::4", "app.example.com": ""} {
		if got := st.DomainIPv6(ctx, h); got != want {
			t.Fatalf("%s = %q, want %q", h, got, want)
		}
	}
	if len(lip.writes) != 3 || !strings.Contains(lip.writes[0], "replace 2001:db8:6b:1943:d::2/128 dev eth0 preferred_lft 0") {
		t.Fatalf("local writes: %v", lip.writes)
	}
	ls := m.IPv6Status(ctx, store.LocalNodeID)
	if ls == nil || ls.Supported == nil || !*ls.Supported || ls.Present != 3 || ls.Iface != "eth0" {
		t.Fatalf("local status: %+v", ls)
	}
	// 다시 맞춰도 쓰기는 없다.
	lip.writes = nil
	if err := m.SyncDomainIPv6(ctx); err != nil || len(lip.writes) != 0 {
		t.Fatalf("not idempotent: %v %v", err, lip.writes)
	}

	// api가 us 노드로 옮겨 가면 us 풀에서 다시 받고, 로컬에서는 주소를 거둔다.
	if err := st.SetInstanceNode(ctx, api.ID, us.ID); err != nil {
		t.Fatal(err)
	}
	if err := m.SyncDomainIPv6(ctx); err != nil {
		t.Fatal(err)
	}
	if got := st.DomainIPv6(ctx, "api.example.com"); got != "2001:db8:2:1::2" {
		t.Fatalf("moved = %q", got)
	}
	if len(lip.writes) != 1 || lip.writes[0] != "-6 addr del 2001:db8:6b:1943:d::4/128 dev eth0" {
		t.Fatalf("local writes after move: %v", lip.writes)
	}
	reqs := h.ensures[us.ID]
	if len(reqs) == 0 || reqs[len(reqs)-1].Pool != "2001:db8:2:1::/112" || strings.Join(reqs[len(reqs)-1].Addresses, ",") != "2001:db8:2:1::2" {
		t.Fatalf("us ensures: %+v", reqs)
	}
	if len(h.ensures[remote.ID]) != 0 {
		t.Fatal("node without a pool got ipv6.ensure")
	}

	// 풀 검증: 다른 노드 풀이나 기본 주소, 이미 쓰는 주소.
	main, _ := node.ParseIPv6Pool("2001:db8:6b:1943::/120")
	if err := m.CheckIPv6Pool(ctx, store.LocalNodeID, main); err == nil {
		t.Fatal("pool containing ::2 accepted")
	}
	cur, _ := node.ParseIPv6Pool("2001:db8:6b:1943:d::/80")
	if err := m.CheckIPv6Pool(ctx, store.LocalNodeID, cur); err != nil {
		t.Fatalf("pool with only aven addresses rejected: %v", err)
	}
	h.addrs = []node.HostIPv6Addr{{Iface: "eth0", Addr: "2001:db8:2:2::5", Bits: 128}}
	other, _ := node.ParseIPv6Pool("2001:db8:2:2::/112")
	if err := m.CheckIPv6Pool(ctx, us.ID, other); err == nil || !strings.Contains(err.Error(), "이미 쓰는 주소") {
		t.Fatalf("foreign address on remote: %v", err)
	}

	// 예전 풀 거두기: 로컬 풀을 지운 뒤 남은 aven 주소를 모두 지운다(풀 밖 주소는 그대로).
	m.Cfg.IPv6Pool = ""
	lip.writes = nil
	if err := m.SyncDomainIPv6(ctx); err != nil {
		t.Fatal(err)
	}
	if st.DomainIPv6(ctx, "a.example.com") != "" {
		t.Fatal("address kept after pool removal")
	}
	if err := m.ReleaseIPv6Pool(ctx, store.LocalNodeID, cur); err != nil {
		t.Fatal(err)
	}
	if len(lip.extra) != 0 || len(lip.writes) != 2 {
		t.Fatalf("release: extra=%v writes=%v", lip.extra, lip.writes)
	}
	if m.IPv6Status(ctx, store.LocalNodeID) != nil {
		t.Fatal("status without a pool should be nil")
	}
}
