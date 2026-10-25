package deploy

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func TestRetireOpts(t *testing.T) {
	sp := store.Spec{}
	o := retireOptsFor(&sp)
	if o.grace != 10 || o.overlapWait(false) != 3*time.Second || o.overlapWait(true) != 0 {
		t.Errorf("defaults: %+v", o)
	}
	d := 0
	sp.DrainingSeconds, sp.OverlapSeconds = &d, 30
	o = retireOptsFor(&sp)
	if o.grace != 0 || o.overlapWait(false) != 30*time.Second || o.overlapWait(true) != 0 {
		t.Errorf("custom: %+v", o)
	}
	if sp.DrainGrace(30) != 0 || (&store.Spec{}).DrainGrace(30) != 30 {
		t.Error("DrainGrace")
	}
}

func TestCheckRequiredMount(t *testing.T) {
	vols := []*store.Volume{{MountPath: "/data"}, {MountPath: "/var/lib/x"}}
	if err := checkRequiredMount("/data", vols); err != nil {
		t.Error(err)
	}
	if err := checkRequiredMount("/uploads", vols); err == nil || !strings.Contains(err.Error(), "/uploads") {
		t.Errorf("missing mount: %v", err)
	}
	if err := checkRequiredMount("/data", nil); err == nil {
		t.Error("no volumes accepted")
	}
}

func TestWithBuildCommand(t *testing.T) {
	vars := map[string]string{"A": "1"}
	if out := withBuildCommand(&store.Spec{}, vars, nil); len(out) != 1 {
		t.Errorf("no command: %v", out)
	}
	cmd := "npm run build:prod"
	out := withBuildCommand(&store.Spec{BuildCommand: &cmd}, vars, nil)
	if out[RailpackBuildCmdVar] != cmd || out["A"] != "1" || len(vars) != 1 {
		t.Errorf("with command: %v (input %v)", out, vars)
	}
	user := map[string]string{RailpackBuildCmdVar: "make"}
	if out := withBuildCommand(&store.Spec{BuildCommand: &cmd}, user, nil); out[RailpackBuildCmdVar] != "make" {
		t.Errorf("user variable must win: %v", out)
	}
}

func TestRegionNodes(t *testing.T) {
	nodes := []*store.Node{
		{ID: "node_local", Name: "home", Schedulable: true, Labels: map[string]string{}},
		{ID: "nod_a", Name: "fra-1", Schedulable: true, Labels: map[string]string{"region": "eu-west"}},
		{ID: "nod_b", Name: "eu-west", Schedulable: false, Labels: map[string]string{}},
		{ID: "nod_c", Name: "EU-West", Schedulable: true, Labels: map[string]string{}},
	}
	got := RegionNodes(nodes, "eu-west")
	if len(got) != 2 || got[0].ID != "nod_a" || got[1].ID != "nod_c" {
		t.Errorf("RegionNodes: %+v", got)
	}
	if len(RegionNodes(nodes, "asia")) != 0 {
		t.Error("unexpected match")
	}
}

func TestHTTPLogHubWildcard(t *testing.T) {
	h := NewHTTPLogHub(10)
	h.SetHosts(map[string]string{"*.example.com": "ins_wild", "api.example.com": "ins_api"})
	for host, want := range map[string]string{"api.example.com": "ins_api", "foo.example.com": "ins_wild", "a.b.example.com": "", "example.com": ""} {
		id, ok := h.InstanceForHost(host)
		if id != want || ok != (want != "") {
			t.Errorf("%s → %q %v, want %q", host, id, ok, want)
		}
	}
	h.Add(proxy.AccessLog{Host: "foo.example.com", Path: "/"})
	if h.LastRequest("ins_wild").IsZero() {
		t.Error("wildcard access log not attributed")
	}
	m := &Manager{HTTPLogs: h}
	if !m.TLSAllowed("foo.example.com") || m.TLSAllowed("foo.other.com") {
		t.Error("TLSAllowed")
	}
}

func TestTargetPortWildcard(t *testing.T) {
	p1, p2 := 3000, 4000
	in := &store.Instance{}
	in.Domains = []store.Domain{{Host: "*.example.com", TargetPort: &p1}, {Host: "api.example.com", TargetPort: &p2}}
	d := &store.Deployment{Env: map[string]string{}}
	tg := &Target{Instance: in}
	m := &Manager{}
	for host, want := range map[string]int{"api.example.com": 4000, "x.example.com": 3000, "other.com": 8080} {
		if got := m.targetPort(tg, d, host); got != want {
			t.Errorf("%s: %d want %d", host, got, want)
		}
	}
}

// regionFixture는 원격 노드 하나(region=eu-west)와 repo 서비스 하나가 있는 store다.
type regionFixture struct {
	m          *Manager
	st         *store.Store
	t          *Target
	node       *store.Node
	mu         sync.Mutex
	dispatched []string
}

func newRegionFixture(t *testing.T) *regionFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &regionFixture{st: st}
	f.m = &Manager{Store: st, Agents: &fakeHub{}, Dispatch: func(_, dep string) { f.mu.Lock(); f.dispatched = append(f.dispatched, dep); f.mu.Unlock() }}
	if err := st.CreateJoinToken(ctx, "h", "fra-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if f.node, err = st.ConsumeJoinToken(ctx, "h", "cred"); err != nil {
		t.Fatal(err)
	}
	f.node.Labels = map[string]string{"region": "eu-west"}
	if err := st.UpdateNode(ctx, f.node); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "web", SourceType: "repo"}
	if err := st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	repo := "https://github.com/acme/app"
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
	in.RepoURL = &repo
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.t = &Target{Project: p, Env: env, Service: svc, Instance: in}
	return f
}

func (f *regionFixture) deployment(t *testing.T) *store.Deployment {
	t.Helper()
	sha := strings.Repeat("a", 40)
	d := &store.Deployment{ServiceID: f.t.Service.ID, EnvironmentID: f.t.Env.ID, InstanceID: f.t.Instance.ID, Trigger: "github",
		Spec: f.t.Instance.Spec, ReqSpec: f.t.Instance.Spec, NodeID: store.LocalNodeID, CommitSHA: &sha, Status: store.DepBuilding}
	if err := f.st.CreateDeployment(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestApplyConfigRegionMovesInstance(t *testing.T) {
	f := newRegionFixture(t)
	ctx := context.Background()
	cfg, _ := config.Parse("railway.json", []byte(`{"deploy":{"region":"eu-west","numReplicas":2}}`))
	d := f.deployment(t)
	err := f.m.applyConfig(ctx, f.t, d, book(t), "railway.json", cfg)
	if !errors.Is(err, errMoved) {
		t.Fatalf("want errMoved, got %v", err)
	}
	in, _ := f.st.GetInstance(ctx, f.t.Instance.ID)
	if in.NodeID != f.node.ID || len(f.dispatched) != 1 {
		t.Fatalf("node=%s dispatched=%v", in.NodeID, f.dispatched)
	}
	nd, _ := f.st.GetDeployment(ctx, f.dispatched[0])
	if nd.NodeID != f.node.ID || nd.CommitSHA == nil || *nd.CommitSHA != *d.CommitSHA || nd.Trigger != "github" {
		t.Errorf("new deployment: %+v", nd)
	}
	// 새 노드의 배포에서는 이미 맞는 노드라 그대로 진행한다(numReplicas 적용)
	f.t.Instance = in
	if err := f.m.applyConfig(ctx, f.t, nd, book(t), "railway.json", cfg); err != nil || nd.Spec.Replicas != 2 || len(f.dispatched) != 1 {
		t.Fatalf("second apply: %v replicas=%d dispatched=%v", err, nd.Spec.Replicas, f.dispatched)
	}
}

func TestApplyConfigRegionIgnoredWithVolumesOrUnknown(t *testing.T) {
	f := newRegionFixture(t)
	ctx := context.Background()
	// 맞는 노드가 없으면 무시
	cfg, _ := config.Parse("railway.json", []byte(`{"deploy":{"region":"mars"}}`))
	if err := f.m.applyConfig(ctx, f.t, f.deployment(t), book(t), "railway.json", cfg); err != nil {
		t.Fatal(err)
	}
	// 볼륨이 있으면 옮기지 않는다
	if err := f.st.CreateVolume(ctx, &store.Volume{ProjectID: f.t.Project.ID, EnvironmentID: f.t.Env.ID, InstanceID: &f.t.Instance.ID, Name: "data", MountPath: "/data"}); err != nil {
		t.Fatal(err)
	}
	cfg, _ = config.Parse("railway.json", []byte(`{"deploy":{"region":"eu-west"}}`))
	if err := f.m.applyConfig(ctx, f.t, f.deployment(t), book(t), "railway.json", cfg); err != nil {
		t.Fatal(err)
	}
	in, _ := f.st.GetInstance(ctx, f.t.Instance.ID)
	if in.NodeID != store.LocalNodeID || len(f.dispatched) != 0 {
		t.Fatalf("instance moved despite volumes: node=%s dispatched=%v", in.NodeID, f.dispatched)
	}
}

func TestApplyConfigEnvironmentBlock(t *testing.T) {
	f := newRegionFixture(t)
	cfg, _ := config.Parse("railway.json", []byte(`{"deploy":{"numReplicas":3,"overlapSeconds":5},
		"environments":{"production":{"deploy":{"numReplicas":1,"drainingSeconds":60}},"pr":{"deploy":{"sleepApplication":true}}}}`))
	d := f.deployment(t)
	if err := f.m.applyConfig(context.Background(), f.t, d, book(t), "railway.json", cfg); err != nil {
		t.Fatal(err)
	}
	if f.t.Env.Name != "production" || d.Spec.Replicas != 1 || d.Spec.OverlapSeconds != 5 || d.Spec.DrainGrace(10) != 60 || d.Spec.SleepWhenIdle {
		t.Errorf("env %s spec: %+v", f.t.Env.Name, d.Spec)
	}
	// 미리보기 환경은 pr 블록
	prev := *f.t.Env
	prev.Name, prev.Kind = "pr-3", store.EnvPreview
	f.t.Env = &prev
	d2 := f.deployment(t)
	if err := f.m.applyConfig(context.Background(), f.t, d2, book(t), "railway.json", cfg); err != nil {
		t.Fatal(err)
	}
	if d2.Spec.Replicas != 3 || !d2.Spec.SleepWhenIdle {
		t.Errorf("preview spec: %+v", d2.Spec)
	}
}

// book은 테스트가 끝나면 닫히는 배포 로그다.
func book(t *testing.T) *logbook.Book {
	b := testBook(t)
	t.Cleanup(func() { b.Close("live") })
	return b
}
