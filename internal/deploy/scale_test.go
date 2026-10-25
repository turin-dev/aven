package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func pl(ps ...any) []store.Placement {
	var out []store.Placement
	for i := 0; i < len(ps); i += 2 {
		out = append(out, store.Placement{NodeID: ps[i].(string), Replicas: ps[i+1].(int)})
	}
	return out
}

func counts(ps []store.Placement) []int {
	var out []int
	for _, p := range ps {
		out = append(out, p.Replicas)
	}
	return out
}

func TestDistribute(t *testing.T) {
	for _, tc := range []struct {
		total int
		w     []store.Placement
		want  []int
	}{
		{3, pl("a", 1), []int{3}},
		{4, pl("a", 1, "b", 1), []int{2, 2}},
		{5, pl("a", 1, "b", 1), []int{3, 2}},            // 같으면 앞 노드
		{6, pl("a", 2, "b", 1), []int{4, 2}},            // 비율 2:1
		{3, pl("a", 2, "b", 1, "c", 1), []int{1, 1, 1}}, // 노드마다 최소 1
		{1, pl("a", 1, "b", 1), []int{1, 1}},            // total이 노드 수보다 작으면 노드마다 1
		{2, pl("a", 0, "b", 1), []int{0, 2}},            // weight 0(주 노드가 region에 없을 때)은 0
		{10, pl("a", 1, "b", 3), []int{3, 7}},
	} {
		got := counts(Distribute(tc.total, tc.w))
		if fmt.Sprint(got) != fmt.Sprint(tc.want) {
			t.Errorf("Distribute(%d, %v) = %v, want %v", tc.total, tc.w, got, tc.want)
		}
	}
}

func TestHPADesired(t *testing.T) {
	f := func(v float64) []HPAMetric { return []HPAMetric{{Name: "cpu", Current: v, Target: 50}} }
	for _, tc := range []struct {
		cur  int
		ms   []HPAMetric
		want int
	}{
		{2, f(100), 4}, // 2배 부하 → 2배
		{2, f(54), 2},  // 1.08: tolerance 안
		{2, f(46), 2},  // 0.92: tolerance 안
		{3, f(56), 4},  // 1.12 → ceil(3.36)=4
		{4, f(10), 1},  // ceil(0.8)=1
		{4, f(25), 2},  // 정확히 절반
		{2, nil, 2},    // 지표 없음
		{0, f(100), 2}, // current 0은 1로 본다
		{2, []HPAMetric{{"cpu", 30, 50}, {"rps", 30, 10}}, 6}, // 가장 큰 비율(rps ×3)
		{2, []HPAMetric{{"cpu", 30, 0}}, 2},                   // target 0은 건너뛴다
	} {
		got, _, _ := HPADesired(tc.cur, tc.ms, AutoscaleTolerance)
		if got != tc.want {
			t.Errorf("HPADesired(%d, %v) = %d, want %d", tc.cur, tc.ms, got, tc.want)
		}
	}
	if _, _, name := HPADesired(2, []HPAMetric{{"cpu", 30, 50}, {"rps", 30, 10}}, 0.1); name != "rps" {
		t.Errorf("metric name %q", name)
	}
}

func TestHPADecideClampCooldownStabilization(t *testing.T) {
	cfg := &store.Autoscale{Enabled: true, MinReplicas: 2, MaxReplicas: 5}
	cfg.Defaults()
	t0 := time.Unix(1_700_000_000, 0)
	var s HPAState
	// 범위 밖이면 cooldown과 상관없이 맞춘다.
	if d := s.Decide(cfg, 1, 1, t0); !d.Act || d.Target != 2 {
		t.Fatalf("below min: %+v", d)
	}
	if d := s.Decide(cfg, 7, 7, t0); !d.Act || d.Target != 5 {
		t.Fatalf("above max: %+v", d)
	}
	// 추천 값은 max로 잘린다.
	s = HPAState{}
	if d := s.Decide(cfg, 2, 9, t0); !d.Act || d.Target != 5 {
		t.Fatalf("clamp: %+v", d)
	}
	s.LastScale = t0
	// scale up cooldown(60초) 안에서는 올리지 않는다.
	if d := s.Decide(cfg, 3, 4, t0.Add(30*time.Second)); d.Act || !strings.Contains(d.Reason, "cooldown") {
		t.Fatalf("up cooldown: %+v", d)
	}
	if d := s.Decide(cfg, 3, 4, t0.Add(61*time.Second)); !d.Act || d.Target != 4 {
		t.Fatalf("after up cooldown: %+v", d)
	}
	// stabilization: 최근 300초 안에 4를 추천했으므로 2로 바로 내리지 않는다.
	s = HPAState{}
	s.Decide(cfg, 4, 4, t0)
	if d := s.Decide(cfg, 4, 2, t0.Add(60*time.Second)); d.Act {
		t.Fatalf("stabilization ignored: %+v", d)
	}
	// 3을 추천한 기록만 남으면 3까지만 내린다(4 기록은 window 밖).
	s = HPAState{}
	s.Decide(cfg, 4, 4, t0)
	s.Decide(cfg, 4, 3, t0.Add(200*time.Second))
	if d := s.Decide(cfg, 4, 2, t0.Add(320*time.Second)); !d.Act || d.Target != 3 {
		t.Fatalf("stabilized target: %+v", d)
	}
	// scale down cooldown(마지막 변경 후 300초)
	s = HPAState{LastScale: t0}
	if d := s.Decide(cfg, 4, 2, t0.Add(100*time.Second)); d.Act || !strings.Contains(d.Reason, "cooldown") {
		t.Fatalf("down cooldown: %+v", d)
	}
	// 범위 안이면 아무것도 하지 않는다.
	s = HPAState{}
	if d := s.Decide(cfg, 3, 3, t0); d.Act || d.Target != 3 {
		t.Fatalf("steady: %+v", d)
	}
}

func TestAutoscaleSkipReason(t *testing.T) {
	dep := "dep_x"
	cron := "* * * * *"
	on := &store.Autoscale{Enabled: true, MinReplicas: 1, MaxReplicas: 3}
	mk := func(f func(in *store.Instance)) *store.Instance {
		in := &store.Instance{Status: store.InstOnline, CurrentDeploymentID: &dep}
		in.Autoscale = on
		f(in)
		return in
	}
	if r := AutoscaleSkipReason(mk(func(*store.Instance) {}), false, false); r != "" {
		t.Fatalf("online: %q", r)
	}
	for name, tc := range map[string]struct {
		in        *store.Instance
		vols, dep bool
	}{
		"disabled":  {mk(func(in *store.Instance) { in.Autoscale = &store.Autoscale{} }), false, false},
		"volumes":   {mk(func(*store.Instance) {}), true, false},
		"deploying": {mk(func(*store.Instance) {}), false, true},
		"sleeping":  {mk(func(in *store.Instance) { in.Status = store.InstSleeping }), false, false},
		"stopped":   {mk(func(in *store.Instance) { in.Status = store.InstOffline }), false, false},
		"cron":      {mk(func(in *store.Instance) { in.CronSchedule = &cron }), false, false},
		"crashed":   {mk(func(in *store.Instance) { in.Status = store.InstCrashed }), false, false},
		"never":     {mk(func(in *store.Instance) { in.CurrentDeploymentID = nil }), false, false},
	} {
		if r := AutoscaleSkipReason(tc.in, tc.vols, tc.dep); r == "" {
			t.Errorf("%s: not skipped", name)
		}
	}
}

func TestAverageReplicaUsage(t *testing.T) {
	rows := []store.MetricRow{
		{CPU: 120, Mem: 200, MemLimit: 1000, Replicas: 2}, // replica당 60%, 메모리 20%
		{CPU: 60, Mem: 300, MemLimit: 1000, Replicas: 3},  // 20%, 30%
		{CPU: 999, Replicas: 0},                           // replica 없음: 무시
	}
	cpu, mem := AverageReplicaUsage(rows, nil)
	if cpu == nil || *cpu != 40 || mem == nil || *mem != 25 {
		t.Fatalf("cpu=%v mem=%v", cpu, mem)
	}
	lim := 0.5
	if cpu, _ := AverageReplicaUsage(rows[:1], &lim); *cpu != 120 { // 60% of a core / 0.5 core limit
		t.Fatalf("limit-relative cpu %v", *cpu)
	}
	if cpu, mem := AverageReplicaUsage(nil, nil); cpu != nil || mem != nil {
		t.Fatal("no data must be nil")
	}
}

func TestHTTPRequestRate(t *testing.T) {
	h := NewHTTPLogHub(5) // ring은 5개뿐이어도 요청 수는 모두 센다
	now := time.Unix(1_700_000_000, 0)
	h.now = func() time.Time { return now }
	h.SetHosts(map[string]string{"a.example.com": "ins_a"})
	for i := 0; i < 240; i++ {
		h.Add(proxy.AccessLog{Host: "a.example.com"})
	}
	if r := h.RequestRate("ins_a", 2*time.Minute); r != 2 {
		t.Fatalf("rate %v, want 2", r)
	}
	now = now.Add(5 * time.Minute)
	if r := h.RequestRate("ins_a", 2*time.Minute); r != 0 {
		t.Fatalf("old requests counted: %v", r)
	}
	if h.RequestRate("ins_b", time.Minute) != 0 {
		t.Fatal("unknown instance")
	}
}

func TestMapRegions(t *testing.T) {
	nodes := []*store.Node{
		{ID: "node_local", Name: "home", Schedulable: true, Labels: map[string]string{"region": "eu"}},
		{ID: "nod_us1", Name: "us-1", Schedulable: true, Labels: map[string]string{"region": "us"}},
		{ID: "nod_us2", Name: "us-2", Schedulable: true, Labels: map[string]string{"region": "us"}},
		{ID: "nod_ap", Name: "ap-1", Schedulable: true, Labels: map[string]string{"region": "ap"}},
	}
	online := func(id string) bool { return id != "nod_us1" && id != "nod_ap" }
	regs := []config.RegionReplicas{{Region: "ap", Replicas: 1}, {Region: "eu", Replicas: 2}, {Region: "mars", Replicas: 1}, {Region: "us", Replicas: 3}}
	got, missing := MapRegions(nodes, regs, "node_local", online)
	// 배포 노드(eu)가 맨 앞, us는 연결된 us-2, ap는 연결된 노드가 없어 빠진다.
	if fmt.Sprint(got) != fmt.Sprint(pl("node_local", 2, "nod_us2", 3)) {
		t.Fatalf("placement %+v", got)
	}
	if fmt.Sprint(missing) != "[ap mars]" {
		t.Fatalf("missing %v", missing)
	}
	// 배포 노드가 region에 맞으면 연결 여부와 상관없이 그 노드를 쓴다.
	got, _ = MapRegions(nodes, []config.RegionReplicas{{Region: "us", Replicas: 1}, {Region: "ap", Replicas: 1}}, "nod_us1", func(string) bool { return true })
	if fmt.Sprint(got) != fmt.Sprint(pl("nod_us1", 1, "nod_ap", 1)) {
		t.Fatalf("here first: %+v", got)
	}
	// 두 region이 같은 노드로 가면 replica를 합친다.
	got, _ = MapRegions(nodes, []config.RegionReplicas{{Region: "eu", Replicas: 1}, {Region: "home", Replicas: 2}}, "nod_x", online)
	if fmt.Sprint(got) != fmt.Sprint(pl("node_local", 3)) {
		t.Fatalf("merged: %+v", got)
	}
}

func TestDesiredMeshSkipsLocalReplicas(t *testing.T) {
	const L, A, B = "node_local", "nod_aaaaaaaaaaaaa", "nod_bbbbbbbbbbbbb"
	const env = "env_one0000000001"
	nodes := []MeshNode{{ID: L, Addr: "10.0.0.1:7443", Serials: []string{"aa"}}, {ID: A, Addr: "10.0.0.2:7443", Serials: []string{"bb"}}, {ID: B, Addr: "10.0.0.3:7443", Serials: []string{"cc"}}}
	web := func(n string) MeshPlacement {
		return MeshPlacement{EnvID: env, EnvName: "production", ServiceID: "svc_web0000000001", Service: "web", Node: n, Ports: []int{8080}}
	}
	ps := []MeshPlacement{
		web(L), web(A), // web은 L(주 노드)과 A에 replica가 있다
		{EnvID: env, EnvName: "production", ServiceID: "svc_db00000000001", Service: "db", Node: B, Ports: []int{5432}},
	}
	got := DesiredMesh(nodes, ps)
	for id, c := range got {
		if err := mesh.Validate(c, id); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
	}
	imports := func(id string) []string {
		var out []string
		for _, im := range got[id].Imports {
			out = append(out, im.Service+"@"+im.Node)
		}
		sort.Strings(out)
		return out
	}
	// L과 A는 web replica가 자기 노드에 있으므로 web을 가져오지 않는다.
	if fmt.Sprint(imports(L)) != "[db@"+B+"]" || fmt.Sprint(imports(A)) != "[db@"+B+"]" {
		t.Fatalf("L=%v A=%v", imports(L), imports(A))
	}
	// B는 web을 한 번만(주 노드 L에서) 가져온다.
	if fmt.Sprint(imports(B)) != "[web@"+L+"]" {
		t.Fatalf("B=%v", imports(B))
	}
	// web은 L과 A 둘 다에서 export된다(각자 자기 replica).
	if len(got[L].Exports) != 1 || len(got[A].Exports) != 1 || got[A].Exports[0].Service != "web" {
		t.Fatalf("exports L=%+v A=%+v", got[L].Exports, got[A].Exports)
	}
}

func TestReplicaIndexAndAssign(t *testing.T) {
	d := &store.Deployment{ID: "dep_abcdefgh1234", NodeID: "node_local"}
	assignContainers("web", d, pl("node_local", 2, "nod_b", 1))
	if fmt.Sprint(d.Containers) != "[keel-web-abcdefgh-1 keel-web-abcdefgh-2]" || len(d.Extra) != 1 || d.Extra[0].Containers[0] != "keel-web-abcdefgh-3" {
		t.Fatalf("%v %+v", d.Containers, d.Extra)
	}
	if replicaIndex("keel-web-abcdefgh-3") != 2 || replicaIndex("weird") != 0 {
		t.Fatal("replicaIndex")
	}
	if d.NodeOf("keel-web-abcdefgh-3") != "nod_b" || d.NodeOf("keel-web-abcdefgh-1") != "node_local" || len(d.AllContainers()) != 3 {
		t.Fatal("NodeOf/AllContainers")
	}
}

// ---------------- fake 드라이버로 ScaleReplicas / 여러 노드 배포 ----------------

// opLog는 로컬 fake 드라이버와 원격 fake hub가 함께 쓰는 호출 기록이다(순서 확인용).
type opLog struct {
	mu  sync.Mutex
	ops []string
}

func (l *opLog) add(s string) {
	l.mu.Lock()
	l.ops = append(l.ops, s)
	l.mu.Unlock()
}

func (l *opLog) list() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.ops...)
}

func (l *opLog) reset() {
	l.mu.Lock()
	l.ops = nil
	l.mu.Unlock()
}

// prefixed는 prefix로 시작하고 upstream이 하나뿐인(쉼표 없는) 기록의 위치다.
func (l *opLog) prefixed(prefix string) int {
	for i, o := range l.list() {
		if strings.HasPrefix(o, prefix) && !strings.Contains(o, ",") {
			return i
		}
	}
	return -1
}

func (l *opLog) index(s string) int {
	for i, o := range l.list() {
		if o == s {
			return i
		}
	}
	return -1
}

// fakeNode는 한 노드의 컨테이너 상태다.
type fakeNode struct {
	mu        sync.Mutex
	name      string
	log       *opLog
	running   map[string]bool
	images    map[string]bool
	probeFail bool
}

func newFakeNode(name string, log *opLog) *fakeNode {
	return &fakeNode{name: name, log: log, running: map[string]bool{}, images: map[string]bool{}}
}

func (f *fakeNode) run(name string) {
	f.mu.Lock()
	f.running[name] = true
	f.mu.Unlock()
	f.log.add(f.name + ":run:" + name)
}

func (f *fakeNode) remove(name string) {
	f.mu.Lock()
	delete(f.running, name)
	f.mu.Unlock()
	f.log.add(f.name + ":remove:" + name)
}

func (f *fakeNode) states(names []string) map[string]*node.ContainerState {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		if f.running[n] {
			out[n] = &node.ContainerState{Name: n, Running: true, Status: "running"}
		}
	}
	return out
}

func (f *fakeNode) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for n := range f.running {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// localFake는 로컬 노드 Driver다. 쓰지 않는 메서드는 embedded nil Driver라 부르면 panic한다(예상 밖 호출을 드러낸다).
type localFake struct {
	Driver
	n *fakeNode
}

func (l *localFake) Pull(_ context.Context, img string, _ *node.RegistryAuth, _ func(string)) error {
	l.n.log.add("local:pull:" + img)
	l.n.images[img] = true
	return nil
}
func (l *localFake) ImageExists(_ context.Context, img string) bool      { return l.n.images[img] }
func (l *localFake) ImageDigest(context.Context, string) (string, error) { return "", nil }
func (l *localFake) ExposedPorts(context.Context, string) []int          { return nil }
func (l *localFake) ImagesByRepo(context.Context, string) []string       { return nil }
func (l *localFake) RemoveImage(context.Context, string)                 {}
func (l *localFake) EnsureNamedNetwork(context.Context, string, map[string]string) error {
	return nil
}
func (l *localFake) Run(_ context.Context, s node.RunSpec) error { l.n.run(s.Name); return nil }
func (l *localFake) Remove(_ context.Context, name string, _ int) error {
	l.n.remove(name)
	return nil
}
func (l *localFake) Stop(_ context.Context, name string, grace int) error {
	l.n.log.add(fmt.Sprintf("local:stop:%s:%d", name, grace))
	return nil
}
func (l *localFake) Inspect(_ context.Context, name string) (*node.ContainerState, error) {
	if st := l.n.states([]string{name})[name]; st != nil {
		return st, nil
	}
	return nil, node.ErrNoContainer
}
func (l *localFake) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	return l.n.states(names), nil
}
func (l *localFake) ListManaged(context.Context) (map[string]map[string]string, error) {
	return map[string]map[string]string{}, nil
}
func (l *localFake) Probe(context.Context, string, string, string, int, string) (bool, string) {
	if l.n.probeFail {
		return false, "connection refused"
	}
	return true, "200 OK"
}
func (l *localFake) TailLogs(context.Context, string, int) []string { return nil }
func (l *localFake) FollowLogs(context.Context, string, string, bool, func(node.LogLine)) error {
	return nil
}

// nodesHub는 원격 노드 agent를 흉내 낸다(노드별 fakeNode).
type nodesHub struct {
	nodes map[string]*fakeNode
	log   *opLog
}

func (h *nodesHub) Online(id string) bool { return h.nodes[id] != nil }
func (h *nodesHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *nodesHub) CallStream(_ context.Context, nodeID, op string, req, res any, _ func(json.RawMessage)) error {
	n := h.nodes[nodeID]
	if n == nil {
		return agentprotocol.ErrOffline
	}
	switch op {
	case agentprotocol.OpContainerRun:
		n.run(req.(agentprotocol.RunRequest).Spec.Name)
	case agentprotocol.OpContainerRemove:
		n.remove(req.(agentprotocol.ContainerRequest).Name)
	case agentprotocol.OpContainerStop:
		r := req.(agentprotocol.ContainerRequest)
		h.log.add(fmt.Sprintf("%s:stop:%s:%d", n.name, r.Name, r.Grace))
	case agentprotocol.OpContainerInspct:
		return setResult(res, n.states(req.(agentprotocol.NamesRequest).Names))
	case agentprotocol.OpContainerProbe:
		if n.probeFail {
			return setResult(res, agentprotocol.ProbeResult{OK: false, Detail: "connection refused"})
		}
		return setResult(res, agentprotocol.ProbeResult{OK: true, Detail: "200 OK"})
	case agentprotocol.OpImageExists:
		return setResult(res, n.images[req.(agentprotocol.ImageRequest).Image])
	case agentprotocol.OpImagePull:
		img := req.(agentprotocol.PullRequest).Image
		n.images[img] = true
		h.log.add(n.name + ":pull:" + img)
	case agentprotocol.OpProxyApply:
		h.log.add(n.name + ":proxy:" + routeUpstreams(req.(agentprotocol.ProxyRequest).Routes))
	case agentprotocol.OpContainerList:
		return setResult(res, map[string]map[string]string{})
	}
	return nil
}

func routeUpstreams(rs []proxy.Route) string {
	var ups []string
	for _, r := range rs {
		ups = append(ups, r.Upstreams...)
	}
	sort.Strings(ups)
	return strings.Join(ups, ",")
}

type scaleFixture struct {
	m      *Manager
	st     *store.Store
	log    *opLog
	local  *fakeNode
	remote *fakeNode
	rid    string
	in     *store.Instance
	svc    *store.Service
	env    *store.Environment
}

func newScaleFixture(t *testing.T) *scaleFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.CreateJoinToken(ctx, "h-r", "remote", time.Now().Add(time.Hour))
	rn, err := st.ConsumeJoinToken(ctx, "h-r", "")
	if err != nil {
		t.Fatal(err)
	}
	log := &opLog{}
	f := &scaleFixture{st: st, log: log, local: newFakeNode("local", log), remote: newFakeNode("remote", log), rid: rn.ID}
	reg, err := logbook.NewRegistry(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	hub := &nodesHub{nodes: map[string]*fakeNode{rn.ID: f.remote}, log: log}
	f.m = &Manager{Store: st, Agents: hub, Logs: reg, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Cfg: Config{DataDir: t.TempDir()},
		localDrv: &localFake{n: f.local}}
	f.m.localProxy = func(_ context.Context, rs []proxy.Route) error {
		log.add("local:proxy:" + routeUpstreams(rs))
		return nil
	}
	f.m.Dispatch = func(string, string) {}
	if err := f.m.Init(ctx); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Name: "acme"}
	if f.env, err = st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	f.svc = &store.Service{ProjectID: p.ID, Name: "web", SourceType: "image"}
	if err := st.CreateService(ctx, f.svc); err != nil {
		t.Fatal(err)
	}
	img := "nginx:1"
	in := &store.Instance{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, NodeID: store.LocalNodeID}
	in.Image = &img
	in.HealthcheckTimeoutS = 2
	in.Domains = []store.Domain{{Host: "web.example.com", Kind: "custom"}}
	in.Placement = pl(store.LocalNodeID, 1, rn.ID, 1)
	in.Replicas = 2
	if err := st.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	f.in = in
	return f
}

// live는 placement대로 이미 떠 있는 live 배포를 만든다.
func (f *scaleFixture) live(t *testing.T) *store.Deployment {
	t.Helper()
	ctx := context.Background()
	img := "nginx:1"
	bu := "image"
	d := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Trigger: "manual", Spec: f.in.Spec, ReqSpec: f.in.Spec,
		NodeID: store.LocalNodeID, Status: store.DepLive, Image: &img, BuilderUsed: &bu, Env: map[string]string{"PORT": "80"}}
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	assignContainers("web", d, pl(store.LocalNodeID, 1, f.rid, 1))
	f.st.UpdateDeployment(ctx, d)
	f.local.run(d.Containers[0])
	f.remote.run(d.Extra[0].Containers[0])
	f.st.SetInstanceStatus(ctx, f.in.ID, store.InstOnline, &d.ID)
	f.in, _ = f.st.GetInstance(ctx, f.in.ID)
	return d
}

func TestScaleReplicasAcrossNodesAndDrainOrder(t *testing.T) {
	old := scaleDownPause
	scaleDownPause = 0
	t.Cleanup(func() { scaleDownPause = old })
	ctx := context.Background()
	f := newScaleFixture(t)
	d := f.live(t)
	tag := strings.TrimPrefix(d.ID, "dep_")[:8]
	name := func(i int) string { return fmt.Sprintf("keel-web-%s-%d", tag, i) }

	// 2 → 4: 노드마다 하나씩 늘어난다(placement 1:1).
	res, err := f.m.ScaleReplicas(ctx, f.in, 4, "test")
	if err != nil || res.From != 2 || res.To != 4 {
		t.Fatalf("scale up: %+v %v", res, err)
	}
	if fmt.Sprint(f.local.names()) != fmt.Sprint([]string{name(1), name(3)}) || fmt.Sprint(f.remote.names()) != fmt.Sprint([]string{name(2), name(4)}) {
		t.Fatalf("containers local=%v remote=%v", f.local.names(), f.remote.names())
	}
	got, _ := f.st.GetDeployment(ctx, d.ID)
	if len(got.AllContainers()) != 4 || got.NodeOf(name(4)) != f.rid || got.Spec.Replicas != 2 {
		t.Fatalf("deployment after scale up: %v %+v replicas=%d", got.Containers, got.Extra, got.Spec.Replicas)
	}
	// 새 replica는 healthcheck 뒤에 프록시에 들어간다: 노드마다 자기 replica만 upstream이다.
	if i := f.log.index("local:proxy:" + name(1) + ":80," + name(3) + ":80"); i < 0 || i < f.log.index("local:run:"+name(3)) {
		t.Fatalf("local proxy after run: %v", f.log.list())
	}
	if f.log.index("remote:proxy:"+name(2)+":80,"+name(4)+":80") < 0 {
		t.Fatalf("remote proxy: %v", f.log.list())
	}
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if in.Replicas != 2 || f.m.CurrentReplicas(ctx, in) != 4 {
		t.Fatalf("baseline replicas must stay: %d current=%d", in.Replicas, f.m.CurrentReplicas(ctx, in))
	}

	// 4 → 2: 프록시에서 먼저 빼고, 그 다음 draining_seconds로 멈추고 지운다.
	grace := 7
	d2, _ := f.st.GetDeployment(ctx, d.ID)
	d2.Spec.DrainingSeconds = &grace
	f.st.UpdateDeployment(ctx, d2)
	f.log.reset()
	if _, err := f.m.ScaleReplicas(ctx, in, 2, "test"); err != nil {
		t.Fatal(err)
	}
	ops := f.log.list()
	pLocal, pRemote := f.log.index("local:proxy:"+name(1)+":80"), f.log.index("remote:proxy:"+name(2)+":80")
	sLocal, sRemote := f.log.index(fmt.Sprintf("local:stop:%s:7", name(3))), f.log.index(fmt.Sprintf("remote:stop:%s:7", name(4)))
	rLocal := f.log.index("local:remove:" + name(3))
	if pLocal < 0 || pRemote < 0 || sLocal < 0 || sRemote < 0 || rLocal < 0 || !(pLocal < sLocal && pRemote < sRemote && sLocal < rLocal) {
		t.Fatalf("drain order wrong: %v", ops)
	}
	if fmt.Sprint(f.local.names()) != fmt.Sprint([]string{name(1)}) || fmt.Sprint(f.remote.names()) != fmt.Sprint([]string{name(2)}) {
		t.Fatalf("after scale down local=%v remote=%v", f.local.names(), f.remote.names())
	}
	// placement 노드보다 적게는 줄일 수 없다(노드마다 1개): 1을 요청해도 2.
	if res, err := f.m.ScaleReplicas(ctx, in, 1, "test"); err != nil || res.To != 2 {
		t.Fatalf("min per node: %+v %v", res, err)
	}
}

func TestScaleReplicasRefusesAndRollsBack(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	if _, err := f.m.ScaleReplicas(ctx, f.in, 3, "x"); err == nil {
		t.Fatal("scaled an instance that was never deployed")
	}
	d := f.live(t)
	// 배포가 진행 중이면 거부한다.
	q := &store.Deployment{ServiceID: f.svc.ID, EnvironmentID: f.env.ID, InstanceID: f.in.ID, Trigger: "manual", Spec: f.in.Spec, ReqSpec: f.in.Spec}
	f.st.CreateDeployment(ctx, q)
	if _, err := f.m.ScaleReplicas(ctx, f.in, 4, "x"); err == nil || !strings.Contains(err.Error(), "배포가 진행 중") {
		t.Fatalf("in-progress deploy: %v", err)
	}
	f.st.TransitionDeployment(ctx, q.ID, store.DepQueued, store.DepCancelled, nil)
	// 원격 노드의 새 replica가 healthcheck에 실패하면 새 컨테이너만 지우고 목록은 그대로다.
	f.remote.probeFail = true
	if _, err := f.m.ScaleReplicas(ctx, f.in, 4, "x"); err == nil {
		t.Fatal("unhealthy scale-up succeeded")
	}
	got, _ := f.st.GetDeployment(ctx, d.ID)
	if len(got.AllContainers()) != 2 || len(f.local.names()) != 1 || len(f.remote.names()) != 1 {
		t.Fatalf("rollback: deployment=%v local=%v remote=%v", got.AllContainers(), f.local.names(), f.remote.names())
	}
}

func TestScaleManualUpdatesBaseline(t *testing.T) {
	old := scaleDownPause
	scaleDownPause = 0
	t.Cleanup(func() { scaleDownPause = old })
	ctx := context.Background()
	f := newScaleFixture(t)
	d := f.live(t)
	if _, err := f.m.ScaleManual(ctx, f.in, 4); err != nil {
		t.Fatal(err)
	}
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	got, _ := f.st.GetDeployment(ctx, d.ID)
	if in.Replicas != 4 || fmt.Sprint(counts(in.Placement)) != "[2 2]" || got.ReqSpec.Replicas != 4 || fmt.Sprint(counts(got.ReqSpec.Placement)) != "[2 2]" {
		t.Fatalf("baseline: in=%d %+v req=%d %+v", in.Replicas, in.Placement, got.ReqSpec.Replicas, got.ReqSpec.Placement)
	}
}

func TestAutoscaleOnceScalesUpFromMetrics(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	f.live(t)
	cpu := 50.0
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	in.Autoscale = &store.Autoscale{Enabled: true, MinReplicas: 2, MaxReplicas: 6, TargetCPUPercent: &cpu}
	f.st.UpdateInstanceConfig(ctx, in)
	now := time.Now()
	// replica 2개, 합계 CPU 200% → replica당 100% (목표 50%의 2배) → 4개
	for i := 0; i < 6; i++ {
		f.st.InsertMetrics(ctx, in.ID, []store.MetricRow{{T: now.Add(-time.Duration(i*10) * time.Second).Unix(), Resolution: 10, CPU: 200, Mem: 100, MemLimit: 1000, Replicas: 2}})
	}
	f.m.AutoscaleOnce(ctx, now)
	in, _ = f.st.GetInstance(ctx, in.ID)
	if n := f.m.CurrentReplicas(ctx, in); n != 4 {
		t.Fatalf("current replicas %d, status %+v", n, f.m.AutoscaleStatusOf(in))
	}
	st := f.m.AutoscaleStatusOf(in)
	if st == nil || st.Desired == nil || *st.Desired != 4 || st.LastScaleAt == nil || st.Metrics.CPU == nil || *st.Metrics.CPU != 100 || !strings.Contains(st.LastReason, "cpu") {
		t.Fatalf("status %+v", st)
	}
	if in.Replicas != 2 {
		t.Fatalf("autoscale must not change the baseline: %d", in.Replicas)
	}
	// 바로 다시 평가하면 scale up cooldown 때문에 그대로다.
	f.m.AutoscaleOnce(ctx, now.Add(10*time.Second))
	if n := f.m.CurrentReplicas(ctx, in); n != 4 {
		t.Fatalf("cooldown ignored: %d", n)
	}
	// 중지된 인스턴스는 건너뛰고 이유를 남긴다.
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil)
	in, _ = f.st.GetInstance(ctx, in.ID)
	f.m.AutoscaleOnce(ctx, now.Add(2*time.Minute))
	if st := f.m.AutoscaleStatusOf(in); st == nil || !strings.Contains(st.Reason, "중지") {
		t.Fatalf("skip reason: %+v", st)
	}
}

func TestApplyConfigMultiRegionSetsPlacement(t *testing.T) {
	ctx := context.Background()
	f := newScaleFixture(t)
	ln, err := f.st.LocalNode(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Parse("railway.json", []byte(fmt.Sprintf(`{"deploy":{"multiRegionConfig":{%q:{"numReplicas":2},"remote":{"numReplicas":1},"nowhere":{}}}}`, ln.Name)))
	if err != nil {
		t.Fatal(err)
	}
	tg, err := f.m.Load(ctx, f.in.ID)
	if err != nil {
		t.Fatal(err)
	}
	sp := f.in.Spec
	sp.Placement = nil
	d := &store.Deployment{ID: "dep_cfgtest00000", NodeID: store.LocalNodeID, Spec: sp, Trigger: "manual"}
	if err := f.m.applyConfig(ctx, tg, d, testBook(t), "railway.json", cfg); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(d.Spec.Placement) != fmt.Sprint(pl(store.LocalNodeID, 2, f.rid, 1)) || d.Spec.Replicas != 3 {
		t.Fatalf("placement %+v replicas %d", d.Spec.Placement, d.Spec.Replicas)
	}
	// 인스턴스 설정은 바뀌지 않는다(배포 스냅샷에만 남는다).
	if in, _ := f.st.GetInstance(ctx, f.in.ID); len(in.Placement) != 2 || in.Placement[1].Replicas != 1 {
		t.Fatalf("instance placement changed: %+v", in.Placement)
	}
	// 볼륨이 있으면 무시한다.
	vol := &store.Volume{ProjectID: f.svc.ProjectID, EnvironmentID: f.env.ID, InstanceID: &f.in.ID, Name: "data", MountPath: "/data"}
	f.st.CreateVolume(ctx, vol)
	d2 := &store.Deployment{ID: "dep_cfgtest00001", NodeID: store.LocalNodeID, Spec: sp, Trigger: "manual"}
	if err := f.m.applyConfig(ctx, tg, d2, testBook(t), "railway.json", cfg); err != nil || d2.Spec.Placement != nil {
		t.Fatalf("volume service got a placement: %+v %v", d2.Spec.Placement, err)
	}
}

// executeDeploy는 인스턴스 설정으로 새 배포를 만들고 바로 실행한다(Dispatch를 막아 두었으므로 동기 실행).
func (f *scaleFixture) executeDeploy(t *testing.T) *store.Deployment {
	t.Helper()
	ctx := context.Background()
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	d, err := f.m.Enqueue(ctx, in, EnqueueOpts{Trigger: "manual"})
	if err != nil {
		t.Fatal(err)
	}
	f.m.execute(d.ID)
	d, _ = f.st.GetDeployment(ctx, d.ID)
	return d
}

func TestMultiNodeDeploySuccess(t *testing.T) {
	f := newScaleFixture(t)
	ctx := context.Background()
	d := f.executeDeploy(t)
	if d.Status != store.DepLive {
		t.Fatalf("status %s err %v ops %v", d.Status, d.Error, f.log.list())
	}
	// 이미지는 노드마다 pull한다.
	if f.log.index("local:pull:nginx:1") < 0 || f.log.index("remote:pull:nginx:1") < 0 {
		t.Fatalf("pulls: %v", f.log.list())
	}
	if len(d.Containers) != 1 || len(d.Extra) != 1 || d.Extra[0].NodeID != f.rid || len(f.local.names()) != 1 || len(f.remote.names()) != 1 {
		t.Fatalf("containers %v %+v", d.Containers, d.Extra)
	}
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if in.Status != store.InstOnline || in.CurrentDeploymentID == nil || *in.CurrentDeploymentID != d.ID {
		t.Fatalf("instance %s", in.Status)
	}
	// 각 노드의 proxy에는 자기 노드 replica만 있다(upstream 하나, 포트는 기본값).
	if f.log.prefixed("local:proxy:"+d.Containers[0]+":") < 0 || f.log.prefixed("remote:proxy:"+d.Extra[0].Containers[0]+":") < 0 {
		t.Fatalf("proxy routes: %v", f.log.list())
	}
	for _, o := range f.log.list() {
		if strings.HasPrefix(o, "local:proxy:") && strings.Contains(o, d.Extra[0].Containers[0]) {
			t.Fatalf("remote replica routed from the local proxy: %s", o)
		}
	}
	if nodes := f.m.ServingNodes(ctx, in); fmt.Sprint(nodes) != fmt.Sprint([]string{store.LocalNodeID, f.rid}) {
		t.Fatalf("serving nodes %v", nodes)
	}
	f.m.FillReplicas(ctx, d)
	if len(d.Replicas) != 2 || d.Replicas[1].NodeID != f.rid || d.Replicas[1].Status != "running" {
		t.Fatalf("replicas %+v", d.Replicas)
	}
}

func TestMultiNodeDeployRollsBackAllNodes(t *testing.T) {
	f := newScaleFixture(t)
	ctx := context.Background()
	first := f.executeDeploy(t)
	if first.Status != store.DepLive {
		t.Fatalf("first deploy: %s %v", first.Status, first.Error)
	}
	before := append(f.local.names(), f.remote.names()...)
	// 두 번째 배포: 원격 노드 replica가 healthcheck에 실패한다 → 두 노드의 새 컨테이너를 모두 지우고 이전 배포가 계속 live다.
	f.remote.probeFail = true
	d := f.executeDeploy(t)
	if d.Status != store.DepFailed {
		t.Fatalf("status %s", d.Status)
	}
	after := append(f.local.names(), f.remote.names()...)
	if fmt.Sprint(before) != fmt.Sprint(after) {
		t.Fatalf("new containers left behind: before=%v after=%v", before, after)
	}
	if f.log.index("local:remove:"+d.Containers[0]) < 0 || f.log.index("remote:remove:"+d.Extra[0].Containers[0]) < 0 {
		t.Fatalf("rollback did not remove on every node: %v", f.log.list())
	}
	in, _ := f.st.GetInstance(ctx, f.in.ID)
	if in.Status != store.InstOnline || *in.CurrentDeploymentID != first.ID {
		t.Fatalf("previous deployment must keep serving: %s %v", in.Status, *in.CurrentDeploymentID)
	}
}
