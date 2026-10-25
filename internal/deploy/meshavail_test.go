package deploy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/store"
)

func TestDesiredMeshFailoverAndOffline(t *testing.T) {
	const (
		L, A, B = "node_local", "nod_aaaaaaaaaaaaa", "nod_bbbbbbbbbbbbb"
		env1    = "env_one0000000001"
	)
	nodes := []MeshNode{
		{ID: L, Addr: "10.0.0.1:7443", Serials: []string{"aa"}},
		{ID: A, Addr: "100.64.0.2:7443", Serials: []string{"bb"}},
		{ID: B, Addr: "10.0.0.3:7443", Serials: []string{"cc"}},
	}
	// api는 A(주 노드)와 B에 replica가 있다. L의 web이 api를 쓴다.
	ps := []MeshPlacement{
		{EnvID: env1, EnvName: "production", ServiceID: "svc_web0000000001", Service: "web", Node: L, Ports: []int{8080}},
		{EnvID: env1, EnvName: "production", ServiceID: "svc_api0000000001", Service: "api", Node: A, Ports: []int{3000}},
		{EnvID: env1, EnvName: "production", ServiceID: "svc_api0000000001", Service: "api", Node: B, Ports: []int{3000}},
	}
	got := DesiredMesh(nodes, ps)
	for id, c := range got {
		if err := mesh.Validate(c, id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	l := got[L]
	if len(l.Imports) != 1 || l.Imports[0].Node != A || strings.Join(l.Imports[0].Nodes, ",") != A+","+B {
		t.Fatalf("local import: %+v", l.Imports)
	}
	// A와 B는 서로의 api replica를 가져오지 않는다(자기 replica가 있다). web만 가져온다(후보 하나라 nodes 없음).
	if a := got[A]; len(a.Imports) != 1 || a.Imports[0].Service != "web" || a.Imports[0].Nodes != nil {
		t.Fatalf("A imports: %+v", a.Imports)
	}
	// A가 꺼지면 peer와 import 후보에서 빠지고 B가 주 노드가 된다.
	nodes[1].Offline = true
	got = DesiredMesh(nodes, ps)
	if _, ok := got[A]; ok {
		t.Fatal("offline node got a config")
	}
	l = got[L]
	if len(l.Peers) != 1 || l.Peers[0].NodeID != B || len(l.Imports) != 1 || l.Imports[0].Node != B || l.Imports[0].Nodes != nil {
		t.Fatalf("local after A offline: %+v", l)
	}
	if err := mesh.Validate(l, L); err != nil {
		t.Fatal(err)
	}
	// 옛 agent용: failover 후보를 뺀 설정
	nodes[1].Offline = false
	stripped := WithoutFailover(DesiredMesh(nodes, ps)[L])
	if stripped.Imports[0].Nodes != nil || stripped.Imports[0].Node != A {
		t.Fatalf("stripped: %+v", stripped.Imports)
	}
}

// availHub는 노드별 연결 상태를 바꿀 수 있고, old면 nodes 필드를 거부하는(옛 agent) mesh hub다.
type availHub struct {
	mu     sync.Mutex
	online map[string]bool
	old    map[string]bool
	reqs   map[string]mesh.Config
	calls  map[string]int
}

func (h *availHub) Online(n string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.online[n]
}
func (h *availHub) Reply(context.Context, string, agentprotocol.Message) error { return nil }
func (h *availHub) CallStream(_ context.Context, n, op string, req, res any, _ func(json.RawMessage)) error {
	if op != agentprotocol.OpMeshEnsure {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	cfg := req.(mesh.Config)
	h.calls[n]++
	if h.old[n] {
		b, _ := json.Marshal(cfg)
		if strings.Contains(string(b), `"nodes"`) {
			return &agentprotocol.RemoteError{Msg: `invalid payload: json: unknown field "nodes"`}
		}
	}
	h.reqs[n] = cfg
	return setResult(res, mesh.Status{Enabled: true, Peers: []mesh.PeerStatus{}})
}

func TestSyncMeshOfflineNodesFailoverAndEvents(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := &availHub{online: map[string]bool{}, old: map[string]bool{}, reqs: map[string]mesh.Config{}, calls: map[string]int{}}
	m := &Manager{Store: st, Agents: hub, Events: &events.Bus{Store: st}}
	mkNode := func(name, private string) *store.Node {
		st.CreateJoinToken(ctx, "h-"+name, name, time.Now().Add(time.Hour))
		n, err := st.ConsumeJoinToken(ctx, "h-"+name, "")
		if err != nil {
			t.Fatal(err)
		}
		st.RecordNodeCert(ctx, "ab"+name[:1], n.ID, time.Now().Add(24*time.Hour))
		p := private
		if err := st.SetNodeInfo(ctx, n.ID, store.NodeAddress{Private: &p}, store.NodeInfo{MeshPort: 7443}); err != nil {
			t.Fatal(err)
		}
		hub.online[n.ID] = true
		return n
	}
	na, nb, nc := mkNode("a", "100.64.0.2"), mkNode("b", "10.1.0.3"), mkNode("c", "10.1.0.4")
	hub.old[na.ID] = true // A의 agent는 nodes 필드를 모른다
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	place := func(name string, primary *store.Node, extra *store.Node) *store.Instance {
		svc := &store.Service{ProjectID: p.ID, Name: name, SourceType: "image"}
		if err := st.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, NodeID: primary.ID}
		in.PrivatePorts = []int{3000}
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		d := &store.Deployment{ServiceID: svc.ID, EnvironmentID: env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec,
			NodeID: primary.ID, Status: store.DepLive, Containers: []string{"keel-" + name + "-1"}}
		if extra != nil {
			d.Extra = []store.NodeContainers{{NodeID: extra.ID, Containers: []string{"keel-" + name + "-2"}}}
		}
		if err := st.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID)
		in, _ = st.GetInstance(ctx, in.ID)
		return in
	}
	place("web", na, nil)
	api := place("api", nb, nc) // B(주) + C

	if err := m.SyncMesh(ctx); err != nil {
		t.Fatal(err)
	}
	// A는 옛 agent: nodes 없이 다시 받는다(주 노드 B만).
	if a := hub.reqs[na.ID]; len(a.Imports) != 1 || a.Imports[0].Node != nb.ID || a.Imports[0].Nodes != nil || hub.calls[na.ID] != 2 {
		t.Fatalf("A (old agent): %+v calls=%d", a.Imports, hub.calls[na.ID])
	}
	if got := m.ServingNodes(ctx, api); len(got) != 2 {
		t.Fatalf("serving nodes: %v", got)
	}
	// B가 끊긴다: B는 설정을 받지 않고, A의 import는 C로 넘어가며, api에 node.unavailable 이벤트가 하나 난다.
	hub.mu.Lock()
	hub.online[nb.ID] = false
	hub.old[na.ID] = false
	hub.reqs = map[string]mesh.Config{}
	hub.mu.Unlock()
	if err := m.SyncMesh(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok := hub.reqs[nb.ID]; ok {
		t.Fatal("offline node got mesh.ensure")
	}
	a := hub.reqs[na.ID]
	if len(a.Imports) != 1 || a.Imports[0].Node != nc.ID || len(a.Peers) != 1 || a.Peers[0].NodeID != nc.ID {
		t.Fatalf("A after B offline: %+v", a)
	}
	if got := m.ServingNodes(ctx, api); len(got) != 1 || got[0] != nc.ID {
		t.Fatalf("serving nodes without B: %v", got)
	}
	count := func() int {
		evs, _ := st.ListEvents(ctx, p.ID, "", 100)
		n := 0
		for _, e := range evs {
			if e.Kind == "node.unavailable" {
				n++
				if e.ServiceID == nil || *e.ServiceID != api.ServiceID {
					t.Fatalf("event for wrong service: %+v", e)
				}
			}
		}
		return n
	}
	if n := count(); n != 1 {
		t.Fatalf("node.unavailable events: %d", n)
	}
	m.SyncMesh(ctx) // 이미 알렸다: 다시 내지 않는다
	if n := count(); n != 1 {
		t.Fatalf("repeated node.unavailable: %d", n)
	}
	// 모든 serving 노드가 끊기면 DNS 레코드가 비지 않도록 그대로 둔다.
	hub.mu.Lock()
	hub.online[nc.ID] = false
	hub.mu.Unlock()
	if got := m.ServingNodes(ctx, api); len(got) != 2 {
		t.Fatalf("all offline: %v", got)
	}
}
