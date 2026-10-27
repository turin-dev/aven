package deploy

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/store"
)

func TestPrivateAliases(t *testing.T) {
	if got := PrivateAliases("db", "production", ""); !slices.Equal(got, []string{"db", "db.production.keel.internal", "db.production.aven.internal"}) {
		t.Fatalf("no custom name: %v", got)
	}
	if got := PrivateAliases("db", "production", "db"); len(got) != 3 {
		t.Fatalf("same as service name: %v", got)
	}
	want := []string{"db", "db.production.keel.internal", "db.production.aven.internal", "pg", "pg.production.keel.internal", "pg.production.aven.internal"}
	if got := PrivateAliases("db", "production", "pg"); !slices.Equal(got, want) {
		t.Fatalf("custom name: %v", got)
	}
	if PrivateDNSName("db", "") != "db" || PrivateDNSName("db", "pg") != "pg" {
		t.Fatal("PrivateDNSName")
	}
}

func TestRunSpecPrivateDNSAliases(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	sv := &store.Service{ProjectID: p.ID, Name: "db", SourceType: "image"}
	if err := st.CreateService(ctx, sv); err != nil {
		t.Fatal(err)
	}
	in := &store.Instance{ServiceID: sv.ID, EnvironmentID: env.ID}
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	m := &Manager{Store: st}
	tg := &Target{Project: p, Env: env, Service: sv, Instance: in}
	img := "postgres"
	d := &store.Deployment{ID: "dep_12345678", Image: &img, Env: map[string]string{}}
	old := []string{"db", PrivateDomain("db", env.Name), AvenPrivateDomain("db", env.Name)}
	if got := m.runSpecNamed(tg, d, "keel-db-1", 0).Aliases; !slices.Equal(got, old) {
		t.Fatalf("without custom name: %v", got)
	}
	// 설정(in.Spec)만 바꾸면 반영되지 않는다: alias는 배포 스냅샷을 따른다.
	in.Spec.PrivateDNSName = "pg"
	if got := m.runSpecNamed(tg, d, "keel-db-1", 0).Aliases; !slices.Equal(got, old) {
		t.Fatalf("staged name leaked into run spec: %v", got)
	}
	d.Spec.PrivateDNSName = "pg"
	got := m.runSpecNamed(tg, d, "keel-db-1", 0).Aliases
	if !slices.Equal(got, append(old, "pg", PrivateDomain("pg", env.Name), AvenPrivateDomain("pg", env.Name))) {
		t.Fatalf("with custom name: %v", got)
	}
}

func TestPrivateDomainVarFollowsLiveDeployment(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	mk := func(name string, vars map[string]string) *store.Instance {
		sv := &store.Service{ProjectID: p.ID, Name: name, SourceType: "image"}
		if err := st.CreateService(ctx, sv); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: sv.ID, EnvironmentID: env.ID, Variables: vars}
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	db := mk("db", nil)
	web := mk("web", map[string]string{"DB_HOST": "${{db.KEEL_PRIVATE_DOMAIN}}"})
	m := &Manager{Store: st}
	resolve := func(in *store.Instance, ov *Override) map[string]string {
		t.Helper()
		in, _ = st.GetInstance(ctx, in.ID)
		res, err := m.Resolve(ctx, in, ov)
		if err != nil {
			t.Fatal(err)
		}
		return res.Vars
	}
	oldHost, newHost := PrivateDomain("db", env.Name), PrivateDomain("pg", env.Name)

	// 배포된 적 없는 서비스는 설정 값을 쓴다.
	if v := resolve(web, nil)["DB_HOST"]; v != oldHost {
		t.Fatalf("default: %s", v)
	}
	db.Spec.PrivateDNSName = "pg"
	if err := st.UpdateInstanceConfig(ctx, db); err != nil {
		t.Fatal(err)
	}
	if v := resolve(web, nil)["DB_HOST"]; v != newHost {
		t.Fatalf("never deployed, custom name: %s", v)
	}
	// live 배포가 아직 옛 이름이면 참조도 옛 이름(실제로 붙어 있는 alias)을 가리킨다.
	d := &store.Deployment{ServiceID: db.ServiceID, EnvironmentID: env.ID, InstanceID: db.ID, Trigger: "manual", Status: store.DepLive, Env: map[string]string{}}
	if err := st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	st.SetInstanceStatus(ctx, db.ID, store.InstOnline, &d.ID)
	if v := resolve(web, nil)["DB_HOST"]; v != oldHost {
		t.Fatalf("pending rename leaked: %s", v)
	}
	// db 자신의 새 배포(override)는 자기 스냅샷의 새 이름을 쓴다.
	cur, _ := st.GetInstance(ctx, db.ID)
	if v := resolve(db, &Override{InstanceID: db.ID, Spec: &cur.Spec})["KEEL_PRIVATE_DOMAIN"]; v != newHost {
		t.Fatalf("own deploy: %s", v)
	}
	// 새 이름으로 배포가 live가 되면 참조도 바뀐다.
	d2 := &store.Deployment{ServiceID: db.ServiceID, EnvironmentID: env.ID, InstanceID: db.ID, Trigger: "manual", Status: store.DepLive, Env: map[string]string{},
		Spec: cur.Spec}
	if err := st.CreateDeployment(ctx, d2); err != nil {
		t.Fatal(err)
	}
	st.SetInstanceStatus(ctx, db.ID, store.InstOnline, &d2.ID)
	if v := resolve(web, nil)["DB_HOST"]; v != newHost {
		t.Fatalf("after deploy: %s", v)
	}
	if v := resolve(db, nil)["KEEL_PRIVATE_DOMAIN"]; v != newHost {
		t.Fatalf("own var after deploy: %s", v)
	}
}

func TestDesiredMeshPrivateDNSAliases(t *testing.T) {
	const (
		L, A = "node_local", "nod_aaaaaaaaaaaaa"
		env1 = "env_one0000000001"
	)
	nodes := []MeshNode{
		{ID: L, Addr: "10.0.0.1:7443", Serials: []string{"aa"}},
		{ID: A, Addr: "100.64.0.2:7443", Serials: []string{"bb"}},
	}
	ps := []MeshPlacement{
		{EnvID: env1, EnvName: "production", ServiceID: "svc_web0000000001", Service: "web", Node: L, Ports: []int{8080}},
		{EnvID: env1, EnvName: "production", ServiceID: "svc_dbb0000000001", Service: "db", Node: A, Ports: []int{5432}, DNSName: "pg"},
	}
	got := DesiredMesh(nodes, ps)
	for id, c := range got {
		if err := mesh.Validate(c, id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	l := got[L]
	if len(l.Imports) != 1 || !slices.Equal(l.Imports[0].Aliases, []string{"pg", "pg.production.aven.internal", "pg.production.keel.internal"}) {
		t.Fatalf("local import aliases: %+v", l.Imports)
	}
	if a := got[A]; len(a.Imports) != 1 || a.Imports[0].Aliases != nil {
		t.Fatalf("web has no custom name: %+v", a.Imports)
	}
	b, _ := json.Marshal(got[A])
	if strings.Contains(string(b), "aliases") {
		t.Fatalf("empty aliases must be omitted: %s", b)
	}
	if s := WithoutAliases(l); s.Imports[0].Aliases != nil || l.Imports[0].Aliases == nil {
		t.Fatal("WithoutAliases")
	}
}

// strictMeshHub는 옛 agent처럼 모르는 필드가 있으면 mesh.ensure를 거부한다(DisallowUnknownFields와 같은 오류).
type strictMeshHub struct {
	unknown []string // 모르는 필드("aliases", "nodes")
	calls   []mesh.Config
}

func (h *strictMeshHub) Online(string) bool { return true }
func (h *strictMeshHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *strictMeshHub) CallStream(_ context.Context, _, op string, req, res any, _ func(json.RawMessage)) error {
	c := req.(mesh.Config)
	h.calls = append(h.calls, c)
	b, _ := json.Marshal(c)
	for _, f := range h.unknown {
		if strings.Contains(string(b), `"`+f+`":`) {
			return &agentErr{`json: unknown field "` + f + `"`}
		}
	}
	return setResult(res, mesh.Status{Enabled: true})
}

type agentErr struct{ s string }

func (e *agentErr) Error() string { return e.s }

func TestSendMeshConfigOldAgentFallback(t *testing.T) {
	cfg := mesh.Config{Imports: []mesh.Import{{EnvID: "env_one0000000001", EnvName: "production", Service: "db", Ports: []int{5432},
		Node: "nod_aaaaaaaaaaaaa", Nodes: []string{"nod_aaaaaaaaaaaaa", "nod_bbbbbbbbbbbbb"}, Aliases: []string{"pg"}}}}
	ctx := context.Background()
	const N = "nod_ccccccccccccc"

	// aliases만 모르는 agent: 한 번 거부 → aliases 없이 다시 보낸다. 다음부터는 처음부터 뺀다.
	hub := &strictMeshHub{unknown: []string{"aliases"}}
	m := &Manager{Agents: hub}
	var st mesh.Status
	if err := m.sendMeshConfig(ctx, N, cfg, &st); err != nil || !st.Enabled {
		t.Fatalf("fallback: %v %+v", err, st)
	}
	if len(hub.calls) != 2 || hub.calls[1].Imports[0].Aliases != nil || hub.calls[1].Imports[0].Nodes == nil {
		t.Fatalf("calls: %+v", hub.calls)
	}
	hub.calls = nil
	if err := m.sendMeshConfig(ctx, N, cfg, &st); err != nil || len(hub.calls) != 1 || hub.calls[0].Imports[0].Aliases != nil {
		t.Fatalf("remembered: %v %+v", err, hub.calls)
	}
	// 기억은 노드별이다.
	hub.calls = nil
	if err := m.sendMeshConfig(ctx, "nod_ddddddddddddd", cfg, &st); err != nil || len(hub.calls) != 2 {
		t.Fatalf("other node: %v %d", err, len(hub.calls))
	}
	// 시간이 지나면(또는 노드가 끊겼다 붙으면) 다시 aliases를 보내 본다.
	m.meshSt.noAliases[N] = time.Now().Add(-noAliasesRetry - time.Second)
	hub.unknown = nil // 그 사이 agent가 업그레이드됐다
	hub.calls = nil
	if err := m.sendMeshConfig(ctx, N, cfg, &st); err != nil || len(hub.calls) != 1 || hub.calls[0].Imports[0].Aliases == nil {
		t.Fatalf("retry after upgrade: %v %+v", err, hub.calls)
	}
	m.meshSt.noAliases = map[string]time.Time{N: time.Now()}
	m.forgetNoAliases(N)
	if _, ok := m.meshSt.noAliases[N]; ok {
		t.Fatal("forgetNoAliases")
	}

	// aliases와 nodes 둘 다 모르는 agent: 어느 쪽을 먼저 알리든 둘 다 빼고 보낸다.
	for _, order := range [][]string{{"aliases", "nodes"}, {"nodes", "aliases"}} {
		hub = &strictMeshHub{unknown: order}
		m = &Manager{Agents: hub}
		if err := m.sendMeshConfig(ctx, N, cfg, &st); err != nil {
			t.Fatalf("both %v: %v", order, err)
		}
		last := hub.calls[len(hub.calls)-1].Imports[0]
		if len(hub.calls) != 3 || last.Aliases != nil || last.Nodes != nil {
			t.Fatalf("both %v calls: %+v", order, hub.calls)
		}
	}
	// 다른 오류는 그대로 돌려준다.
	hub = &strictMeshHub{unknown: []string{"ports"}}
	m = &Manager{Agents: hub}
	if err := m.sendMeshConfig(ctx, N, cfg, &st); err == nil || len(hub.calls) != 1 {
		t.Fatalf("other error: %v %d", err, len(hub.calls))
	}
}
