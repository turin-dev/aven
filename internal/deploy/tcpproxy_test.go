package deploy

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

// portHub는 port.free를 받는 agent다. busy 포트는 쓰는 중이라고 답하고, old면 작업을 모른다고 거부한다.
type portHub struct {
	old    bool
	busy   map[int]bool
	probed []int
}

func (h *portHub) Online(string) bool { return true }
func (h *portHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *portHub) CallStream(_ context.Context, _, op string, req, res any, _ func(json.RawMessage)) error {
	if op != agentprotocol.OpPortFree || h.old {
		return &agentErr{`operation "` + op + `" is not allowed`}
	}
	p := req.(agentprotocol.PortFreeRequest).Port
	h.probed = append(h.probed, p)
	return setResult(res, agentprotocol.PortFreeResult{Free: !h.busy[p]})
}

func tcpFixture(t *testing.T) (*Manager, *store.Store, func(name string, addr store.NodeAddress) *store.Node) {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	m := &Manager{Store: st, Cfg: Config{TCPPortMin: 20000, TCPPortMax: 20010, BaseDomain: "aven.example.com"}}
	mkNode := func(name string, addr store.NodeAddress) *store.Node {
		st.CreateJoinToken(ctx, "h-"+name, name, time.Now().Add(time.Hour))
		n, err := st.ConsumeJoinToken(ctx, "h-"+name, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := st.SetNodeInfo(ctx, n.ID, addr, store.NodeInfo{}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	return m, st, mkNode
}

func TestAllocateTCPPortRemoteProbe(t *testing.T) {
	ctx := context.Background()
	m, st, mkNode := tcpFixture(t)
	v6 := "2001:db8::1"
	n := mkNode("us", store.NodeAddress{IPv6: &v6})
	// 20000은 다른 인스턴스가 이미 가졌다(DB), 20001·20002는 노드에서 쓰는 중이다(port.free).
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	sv := &store.Service{ProjectID: p.ID, Name: "db", SourceType: "image"}
	if err := st.CreateService(ctx, sv); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateInstance(ctx, &store.Instance{ServiceID: sv.ID, EnvironmentID: env.ID, Spec: store.Spec{TCPProxy: &store.TCPProxy{ContainerPort: 5432, PublicPort: 20000}}}); err != nil {
		t.Fatal(err)
	}
	hub := &portHub{busy: map[int]bool{20001: true, 20002: true}}
	m.Agents = hub
	got, err := m.AllocateTCPPortFor(ctx, n.ID)
	if err != nil || got != 20003 {
		t.Fatalf("allocate: %d %v (probed %v)", got, err, hub.probed)
	}
	if len(hub.probed) != 3 || hub.probed[0] != 20001 {
		t.Fatalf("probed: %v", hub.probed)
	}
	// 옛 agent: 확인 없이 첫 빈 후보
	m.Agents = &portHub{old: true}
	if got, err := m.AllocateTCPPortFor(ctx, n.ID); err != nil || got != 20001 {
		t.Fatalf("old agent: %d %v", got, err)
	}
	// 범위 전체가 쓰는 중
	all := map[int]bool{}
	for p := 20000; p <= 20010; p++ {
		all[p] = true
	}
	m.Agents = &portHub{busy: all}
	if _, err := m.AllocateTCPPortFor(ctx, n.ID); err == nil {
		t.Fatal("allocated a busy port")
	}
	// 로컬 노드는 직접 bind해 본다
	ln, err := net.Listen("tcp", "127.0.0.1:20001")
	if err == nil {
		defer ln.Close()
		m.Cfg.ProxyBind = "127.0.0.1"
		if got, err := m.AllocateTCPPortFor(ctx, store.LocalNodeID); err != nil || got == 20000 || got == 20001 {
			t.Fatalf("local: %d %v", got, err)
		}
	}
}

func TestTCPProxyEndpoint(t *testing.T) {
	ctx := context.Background()
	m, st, mkNode := tcpFixture(t)
	t.Setenv("KEEL_INGRESS_MODE", "")
	v6, v4 := "2001:db8::1", "203.0.113.9"
	us := mkNode("us", store.NodeAddress{IPv6: &v6})
	dual := mkNode("dual", store.NodeAddress{IPv6: &v6, IPv4: &v4})
	kr := mkNode("remote", store.NodeAddress{})
	tun := mkNode("tun", store.NodeAddress{IPv4: &v4})
	if err := st.SetNodeIngress(ctx, tun.ID, store.NodeIngress{IngressMode: store.IngressCloudflareTunnel}); err != nil {
		t.Fatal(err)
	}
	on := func(id string) *store.Instance { return &store.Instance{NodeID: id} }
	check := func(name string, in *store.Instance, wantHost string, wantOK bool) {
		t.Helper()
		h, ok := m.TCPProxyEndpoint(ctx, in)
		if h != wantHost || ok != wantOK {
			t.Errorf("%s: %q %v, want %q %v", name, h, ok, wantHost, wantOK)
		}
		if m.TCPProxyHostFor(ctx, in) != wantHost {
			t.Errorf("%s: TCPProxyHostFor differs", name)
		}
	}
	// 로컬: 지금 운영과 같은 설정(KEEL_TCP_PROXY_DOMAIN 없음) → KEEL_BASE_DOMAIN 그대로
	check("local default", on(store.LocalNodeID), "aven.example.com", true)
	check("nil instance", nil, "aven.example.com", true)
	check("us (IPv6 only)", on(us.ID), v6, true)
	check("dual (IPv6 first)", on(dual.ID), v6, true)
	check("remote (no address)", on(kr.ID), "", false)
	check("tunnel", on(tun.ID), "", false)

	// 노드 tcp_hostname이 있으면 그것이 먼저다(원격). 로컬은 KEEL_TCP_PROXY_DOMAIN이 우선.
	if err := st.SetNodeIngress(ctx, us.ID, store.NodeIngress{TCPHostname: "tcp-us.example.com"}); err != nil {
		t.Fatal(err)
	}
	check("us tcp_hostname", on(us.ID), "tcp-us.example.com", true)
	if err := st.SetNodeIngress(ctx, kr.ID, store.NodeIngress{TCPHostname: "tcp-kr.example.com"}); err != nil {
		t.Fatal(err)
	}
	check("remote tcp_hostname without public IP", on(kr.ID), "", false)
	if err := st.SetNodeIngress(ctx, store.LocalNodeID, store.NodeIngress{TCPHostname: "tcp-main.example.com"}); err != nil {
		t.Fatal(err)
	}
	check("local tcp_hostname", on(store.LocalNodeID), "tcp-main.example.com", true)
	m.Cfg.TCPProxyDomain = "env.example.com"
	check("local env wins", on(store.LocalNodeID), "env.example.com", true)
	t.Setenv("KEEL_INGRESS_MODE", store.IngressCloudflareTunnel)
	check("local tunnel (env)", on(store.LocalNodeID), "", false)
}
