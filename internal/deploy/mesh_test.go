package deploy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/store"
)

func TestDesiredMesh(t *testing.T) {
	const (
		L, A, B = "node_local", "nod_aaaaaaaaaaaaa", "nod_bbbbbbbbbbbbb"
		env1    = "env_one0000000001"
		env2    = "env_two0000000002"
		env3    = "env_three00000003"
	)
	nodes := []MeshNode{
		{ID: L, Addr: "10.0.0.1:7443", Serials: []string{"aa"}},
		{ID: A, Addr: "100.64.0.2:7443", Serials: []string{"bb"}},
		{ID: B, Addr: "[fd7a:115c:a1e0::3]:7443", Serials: []string{"cc"}},
	}
	ps := []MeshPlacement{
		{EnvID: env1, EnvName: "production", ServiceID: "svc_web0000000001", Service: "web", Node: L, Ports: []int{8080}},
		{EnvID: env1, EnvName: "production", ServiceID: "svc_db00000000001", Service: "db", Node: A, Ports: []int{5432, 9187}},
		{EnvID: env2, EnvName: "staging", ServiceID: "svc_api0000000001", Service: "api", Node: B, Ports: []int{8080}},
		// mesh 노드가 아닌 C에 있는 서비스는 빠지고, env3은 mesh 노드 하나에만 있게 되어 아무것도 내보내지 않는다.
		{EnvID: env3, EnvName: "production", ServiceID: "svc_x000000000001", Service: "x", Node: L, Ports: []int{80}},
		{EnvID: env3, EnvName: "production", ServiceID: "svc_y000000000001", Service: "y", Node: "nod_ccccccccccccc", Ports: []int{80}},
	}
	got := DesiredMesh(nodes, ps)
	for id, c := range got {
		if err := mesh.Validate(c, id); err != nil {
			t.Fatalf("%s: invalid config: %v", id, err)
		}
		if len(c.Peers) != 2 {
			t.Errorf("%s: peers %+v", id, c.Peers)
		}
	}
	l, a, b := got[L], got[A], got[B]
	if len(l.Exports) != 1 || l.Exports[0].Service != "web" || len(l.Exports[0].Peers) != 1 || l.Exports[0].Peers[0] != A {
		t.Errorf("local exports: %+v", l.Exports)
	}
	if len(l.Imports) != 1 || l.Imports[0].Service != "db" || l.Imports[0].Node != A || len(l.Imports[0].Ports) != 2 {
		t.Errorf("local imports: %+v", l.Imports)
	}
	if len(a.Exports) != 1 || a.Exports[0].Service != "db" || a.Exports[0].Peers[0] != L || len(a.Imports) != 1 || a.Imports[0].Service != "web" {
		t.Errorf("A: %+v", a)
	}
	// B만 env2를 갖고 있다: 내보내거나 가져올 것이 없다(상태 확인용 peer만 있다).
	if len(b.Exports) != 0 || len(b.Imports) != 0 {
		t.Errorf("B: %+v", b)
	}
	// 환경 격리: env1의 서비스는 env1의 노드에게만 export된다(B는 env1의 노드가 아니다).
	for _, e := range append(l.Exports, a.Exports...) {
		for _, p := range e.Peers {
			if p == B {
				t.Errorf("env1 service %s exported to B", e.Service)
			}
		}
	}
}

// meshHub는 mesh.ensure 요청을 노드별로 기록한다.
type meshHub struct {
	mu   sync.Mutex
	reqs map[string]mesh.Config
}

func (h *meshHub) Online(string) bool { return true }
func (h *meshHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *meshHub) CallStream(_ context.Context, node, op string, req, res any, _ func(json.RawMessage)) error {
	if op != agentprotocol.OpMeshEnsure {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reqs[node] = req.(mesh.Config)
	return setResult(res, mesh.Status{Enabled: true, Peers: []mesh.PeerStatus{{NodeID: "x", Status: "ok"}}})
}

func TestSyncMeshAndCrossNodeRefs(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	hub := &meshHub{reqs: map[string]mesh.Config{}}
	m := &Manager{Store: st, Agents: hub}
	mkNode := func(name, private string, meshPort int) *store.Node {
		st.CreateJoinToken(ctx, "h-"+name, name, time.Now().Add(time.Hour))
		n, err := st.ConsumeJoinToken(ctx, "h-"+name, "")
		if err != nil {
			t.Fatal(err)
		}
		st.RecordNodeCert(ctx, "ab"+name[:1], n.ID, time.Now().Add(24*time.Hour))
		p := private
		if err := st.SetNodeInfo(ctx, n.ID, store.NodeAddress{Private: &p}, store.NodeInfo{MeshPort: meshPort}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	na := mkNode("a", "100.64.0.2", 7443)
	nb := mkNode("b", "10.1.0.3", 7443)
	nc := mkNode("c", "", 0) // 사설 주소 없음
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	place := func(name string, n *store.Node, vars map[string]string, ports []int) *store.Instance {
		svc := &store.Service{ProjectID: p.ID, Name: name, SourceType: "image"}
		if err := st.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, NodeID: n.ID, Variables: vars}
		in.PrivatePorts = ports
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		d := &store.Deployment{ServiceID: svc.ID, EnvironmentID: env.ID, InstanceID: in.ID, Trigger: "manual", Spec: in.Spec, ReqSpec: in.Spec,
			NodeID: n.ID, Status: store.DepLive, Env: map[string]string{"PORT": "3000"}}
		if err := st.CreateDeployment(ctx, d); err != nil {
			t.Fatal(err)
		}
		st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID)
		in, _ = st.GetInstance(ctx, in.ID)
		return in
	}
	web := place("web", na, map[string]string{"DATABASE_URL": "${{db.DATABASE_URL}}", "CACHE": "${{cache.REDIS_URL}}", "PLAIN": "x"}, nil)
	place("db", nb, nil, []int{5432})
	place("cache", nc, nil, nil)

	if err := m.SyncMesh(ctx); err != nil {
		t.Fatal(err)
	}
	ca, cb := hub.reqs[na.ID], hub.reqs[nb.ID]
	if _, ok := hub.reqs[nc.ID]; ok {
		t.Fatal("mesh config pushed to a node without a private address")
	}
	if err := mesh.Validate(ca, na.ID); err != nil {
		t.Fatal(err)
	}
	if len(ca.Imports) != 1 || ca.Imports[0].Service != "db" || ca.Imports[0].Ports[0] != 5432 || ca.Imports[0].EnvName != "production" {
		t.Fatalf("A imports: %+v", ca.Imports)
	}
	if len(ca.Exports) != 1 || ca.Exports[0].Service != "web" || ca.Exports[0].Ports[0] != 3000 { // private_ports가 없으면 live 배포의 PORT
		t.Fatalf("A exports: %+v", ca.Exports)
	}
	if len(cb.Exports) != 1 || cb.Exports[0].Service != "db" || len(cb.Imports) != 1 || len(cb.Peers) != 1 || cb.Peers[0].Serials[0] != "aba" {
		t.Fatalf("B: %+v", cb)
	}
	na, _ = st.GetNode(ctx, na.ID)
	nc, _ = st.GetNode(ctx, nc.ID)
	if st := m.MeshStatus(ctx, na); !st.Enabled || st.Listen == nil || *st.Listen != "100.64.0.2:7443" || len(st.Peers) != 1 {
		t.Fatalf("status A: %+v", st)
	}
	if st := m.MeshStatus(ctx, nc); st.Enabled || st.Reason == "" {
		t.Fatalf("status C: %+v", st)
	}
	// 폐기된 노드는 peer에서 빠진다.
	if err := st.RevokeNodeCerts(ctx, nb.ID); err != nil {
		t.Fatal(err)
	}
	hub.reqs = map[string]mesh.Config{}
	m.SyncMesh(ctx)
	if c := hub.reqs[na.ID]; len(c.Peers) != 0 || len(c.Imports) != 0 {
		t.Fatalf("revoked node still a peer: %+v", c)
	}
	st.RecordNodeCert(ctx, "abb2", nb.ID, time.Now().Add(24*time.Hour))

	refs, err := m.CrossNodeRefs(ctx, env.ID, "web", m.HomeNode(ctx, web), web.Variables)
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := refs["DATABASE_URL"]; !ok || !r.Mesh || len(r.Services) != 1 || r.Services[0] != "db" {
		t.Fatalf("DATABASE_URL: %+v", refs)
	}
	if r, ok := refs["CACHE"]; !ok || r.Mesh {
		t.Fatalf("CACHE (node without mesh): %+v", refs)
	}
	if _, ok := refs["PLAIN"]; ok {
		t.Fatal("PLAIN marked cross-node")
	}
}
