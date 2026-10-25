package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/ha"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// ---- database high availability ----
//
// GET    /api/services/{sid}/instances/{eid}/ha              전환 가능 여부와 동반 템플릿 선택지, 클러스터 상태(구성원 역할/상태)
// POST   /api/services/{sid}/instances/{eid}/ha              {replicas?, internal?, edge?, auto_failover?, skip_backup?} HA로 전환
// PATCH  /api/services/{sid}/instances/{eid}/ha              {auto_failover?, replicas?} 자동 failover 켜기/끄기, replica 수 바꾸기
// DELETE /api/services/{sid}/instances/{eid}/ha              단독으로 되돌리기(원래 서비스가 주 노드일 때만)
// POST   /api/services/{sid}/instances/{eid}/ha/switchover   {to: service_id} 주 노드 옮기기(Make Leader)
// GET    /api/environments/{eid}/ha                          환경의 클러스터 목록(캔버스 배지)
//
// 동반 템플릿(catalog/ha.go)이 클러스터 모양을 정하고, Aven 제어부가 조정자 노릇을 한다: 10초마다 모든 데이터 노드의
// 역할과 복제 위치를 컨테이너 안 exec로 확인하고(probe), Postgres는 주 노드가 30초 동안 응답하지 않으면 가장 최신인
// replica를 승격한다. Redis(Sentinel)와 MongoDB(replica set)는 엔진이 스스로 주 노드를 뽑고, Aven은 결과를 기록한다.

const (
	haProbeEvery    = 10 * time.Second
	haProbeTimeout  = 15 * time.Second
	haFailAfter     = 3 // 연속 실패 횟수(×10초)가 이만큼이면 Postgres 주 노드를 잃었다고 본다
	haFenceCooldown = 2 * time.Minute
	haSwitchTimeout = 2 * time.Minute
)

func (s *Server) haRoutes(h func(string, access, http.HandlerFunc), R, M access) {
	h("GET /api/services/{sid}/instances/{eid}/ha", R, s.getHA)
	h("POST /api/services/{sid}/instances/{eid}/ha", M, s.convertHA)
	h("PATCH /api/services/{sid}/instances/{eid}/ha", M, s.patchHA)
	h("DELETE /api/services/{sid}/instances/{eid}/ha", M, s.revertHA)
	h("POST /api/services/{sid}/instances/{eid}/ha/switchover", M, s.switchoverHA)
	h("GET /api/environments/{eid}/ha", R, s.listEnvHA)
}

// ---- 테스트용 주입점 ----

// haGo는 전환 뒤 배포처럼 오래 걸리는 작업을 뒤에서 실행한다(테스트는 바로 실행한다).
var haGo = func(f func()) { go f() }

// haBackup은 전환 전에 root 볼륨을 백업한다.
var haBackup = func(s *Server, ctx context.Context, v *store.Volume) error {
	_, err := s.Deploy.CreateBackup(ctx, v)
	return err
}

// haDeleteService는 구성원 서비스를 지운다(컨테이너, 라우트 정리 포함).
var haDeleteService = func(s *Server, ctx context.Context, svc *store.Service) error {
	return s.Deploy.DeleteService(ctx, svc)
}

// haSleep은 switchover 중 기다린다(테스트는 기다리지 않는다).
var haSleep = func(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// ---- 실행 상태(메모리) ----

type haMemberState struct {
	OK        bool
	Probe     ha.Probe
	Err       string
	Fails     int
	CheckedAt time.Time
	FencedAt  time.Time
}

type haRuntime struct {
	mu      sync.Mutex
	members map[string]*haMemberState
	busy    bool // 전환/되돌리기/switchover/failover 진행 중
}

var haStates = struct {
	sync.Mutex
	m map[string]*haRuntime
}{m: map[string]*haRuntime{}}

func haRT(clusterID string) *haRuntime {
	haStates.Lock()
	defer haStates.Unlock()
	rt := haStates.m[clusterID]
	if rt == nil {
		rt = &haRuntime{members: map[string]*haMemberState{}}
		haStates.m[clusterID] = rt
	}
	return rt
}

func haForget(clusterID string) {
	haStates.Lock()
	delete(haStates.m, clusterID)
	haStates.Unlock()
}

// tryBusy는 작업 하나만 진행하게 한다. 이미 진행 중이면 false.
func (rt *haRuntime) tryBusy() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.busy {
		return false
	}
	rt.busy = true
	return true
}

func (rt *haRuntime) done() {
	rt.mu.Lock()
	rt.busy = false
	rt.mu.Unlock()
}

func (rt *haRuntime) isBusy() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.busy
}

func (rt *haRuntime) state(serviceID string) haMemberState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if st := rt.members[serviceID]; st != nil {
		return *st
	}
	return haMemberState{}
}

func (rt *haRuntime) record(serviceID string, p ha.Probe, err error) haMemberState {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	st := rt.members[serviceID]
	if st == nil {
		st = &haMemberState{}
		rt.members[serviceID] = st
	}
	st.CheckedAt = time.Now().UTC()
	if err != nil {
		st.OK, st.Err = false, err.Error()
		st.Fails++
	} else {
		st.OK, st.Err, st.Probe, st.Fails = true, "", p, 0
	}
	return *st
}

// ---- 동반 템플릿 찾기와 전환 조건 ----

// haPlan은 서비스 하나를 전환할 때 쓸 동반 템플릿과 그 root 원형이다.
type haPlan struct {
	kind          string
	standaloneKey string
	companionKey  string
	companion     *catalog.Definition
	root          *catalog.ServiceSpec
	version       ha.Version
}

// haDefinition은 키로 템플릿 정의를 찾는다(내장 → 사용자 템플릿).
func (s *Server) haDefinition(ctx context.Context, r *http.Request, key string) (*catalog.Definition, error) {
	if r != nil {
		f, err := s.findTemplate(r, key)
		if err != nil {
			return nil, err
		}
		return f.def, nil
	}
	if t, ok := catalog.Templates[key]; ok {
		return t.Definition, nil
	}
	ut, err := s.Store.GetTemplateByKey(ctx, key)
	if err != nil {
		return nil, err
	}
	return parseStored(ut)
}

// haStandalone은 서비스가 만들어진 단독 템플릿과 그 템플릿이 가리키는 동반 템플릿 키다. 서비스의 템플릿에 HA 선언이
// 없으면(여러 서비스 템플릿의 DB 등) 같은 종류의 내장 DB 템플릿을 쓴다.
func (s *Server) haStandalone(ctx context.Context, r *http.Request, svc *store.Service) (string, string) {
	kind := *svc.DatabaseKind
	if svc.Template != nil && *svc.Template != "" {
		if d, err := s.haDefinition(ctx, r, *svc.Template); err == nil {
			for _, sv := range d.Services {
				if sv.Source.Database == kind && sv.HATemplateCode != "" {
					return *svc.Template, sv.HATemplateCode
				}
			}
		}
	}
	if t, ok := catalog.Templates[kind]; ok {
		return kind, t.Definition.HATemplateCode()
	}
	return "", ""
}

// haEligibility는 전환 계획과 전환을 막는 문제 목록이다(plan은 동반 템플릿을 찾지 못하면 nil).
func (s *Server) haEligibility(ctx context.Context, r *http.Request, c *instCtx) (*haPlan, []string) {
	if c.svc.DatabaseKind == nil {
		return nil, []string{"데이터베이스 서비스만 HA로 전환할 수 있습니다"}
	}
	kind := *c.svc.DatabaseKind
	if !ha.Supported(kind) {
		return nil, []string{fmt.Sprintf("%s는 아직 HA 전환을 지원하지 않습니다(지원: postgres, redis, mongo)", kind)}
	}
	std, code := s.haStandalone(ctx, r, c.svc)
	if code == "" {
		return nil, []string{"이 서비스의 템플릿에는 HA 동반 템플릿(haTemplateCode)이 없습니다"}
	}
	comp, err := s.haDefinition(ctx, r, code)
	if err != nil {
		return nil, []string{fmt.Sprintf("HA 동반 템플릿 %q를 찾을 수 없습니다", code)}
	}
	root := comp.HACompanionRoot()
	if root == nil || root.Source.Database != kind {
		return nil, []string{fmt.Sprintf("템플릿 %q는 %s의 HA 동반 템플릿이 아닙니다", code, kind)}
	}
	plan := &haPlan{kind: kind, standaloneKey: std, companionKey: code, companion: comp, root: root}
	var probs []string
	image := ""
	if c.in.Image != nil {
		image = *c.in.Image
	}
	v, ip := ha.CheckImage(root.HAConversionConfig, image)
	plan.version = v
	probs = append(probs, ip...)
	if sc := c.in.StartCommand; sc != nil && strings.TrimSpace(*sc) != "" && *sc != catalog.Databases[kind].Start {
		probs = append(probs, "사용자 시작 명령이 있습니다. 클러스터 이미지가 시작을 관리하므로 시작 명령을 템플릿 기본값으로 되돌린 뒤 다시 배포하세요")
	}
	if c.in.Replicas > 1 || len(c.in.Placement) > 1 {
		probs = append(probs, "replica가 하나인 서비스만 전환할 수 있습니다")
	}
	if c.in.SleepWhenIdle {
		probs = append(probs, "유휴 시 잠자기(sleep_when_idle)를 끄세요")
	}
	if vols, _ := s.Store.ListVolumesByInstance(ctx, c.in.ID); len(vols) == 0 {
		probs = append(probs, "데이터 볼륨이 붙은 서비스만 전환할 수 있습니다")
	}
	if c.in.CurrentDeploymentID == nil || c.in.Status != store.InstOnline {
		probs = append(probs, "실행 중인 배포가 없습니다. 먼저 배포하고 online이 될 때까지 기다리세요")
	}
	return plan, probs
}

// haCandidateNodes는 구성원을 놓을 수 있는 노드다(배포 가능하고 연결된 노드, 로컬 노드가 처음).
func (s *Server) haCandidateNodes(ctx context.Context) []string {
	nodes, err := s.Store.ListNodes(ctx)
	if err != nil {
		return []string{store.LocalNodeID}
	}
	var out []string
	for _, n := range nodes {
		if !n.Schedulable || n.Draining {
			continue
		}
		if n.IsLocal {
			out = append([]string{store.NormNode(n.ID)}, out...)
		} else if s.Agents != nil && s.Agents.Online(n.ID) {
			out = append(out, n.ID)
		}
	}
	if len(out) == 0 {
		out = []string{store.LocalNodeID}
	}
	return out
}

// ---- 클러스터 읽기 ----

// haCluster는 클러스터와 구성원 서비스/인스턴스다.
type haCluster struct {
	c       *store.HACluster
	members []store.HAMember
	svcs    map[string]*store.Service
	insts   map[string]*store.Instance
	env     *store.Environment
}

func (s *Server) loadHACluster(ctx context.Context, c *store.HACluster) (*haCluster, error) {
	members, err := s.Store.ListHAMembers(ctx, c.ID)
	if err != nil {
		return nil, err
	}
	env, err := s.Store.GetEnvironment(ctx, c.EnvironmentID)
	if err != nil {
		return nil, err
	}
	hc := &haCluster{c: c, svcs: map[string]*store.Service{}, insts: map[string]*store.Instance{}, env: env}
	for _, m := range members {
		svc, err := s.Store.GetService(ctx, m.ServiceID)
		if err != nil {
			continue
		}
		in, err := s.Store.GetInstanceFor(ctx, m.ServiceID, c.EnvironmentID)
		if err != nil {
			continue
		}
		hc.members = append(hc.members, m)
		hc.svcs[m.ServiceID], hc.insts[m.ServiceID] = svc, in
	}
	return hc, nil
}

func isDataRole(role string) bool { return role == catalog.RoleRoot || role == catalog.RoleReplica }

func (hc *haCluster) data() []store.HAMember {
	var out []store.HAMember
	for _, m := range hc.members {
		if isDataRole(m.Role) {
			out = append(out, m)
		}
	}
	return out
}

func (hc *haCluster) byRole(role string) []store.HAMember {
	var out []store.HAMember
	for _, m := range hc.members {
		if m.Role == role {
			out = append(out, m)
		}
	}
	return out
}

func (hc *haCluster) name(id string) string {
	if sv := hc.svcs[id]; sv != nil {
		return sv.Name
	}
	return ""
}

func (hc *haCluster) member(id string) (store.HAMember, bool) {
	for _, m := range hc.members {
		if m.ServiceID == id {
			return m, true
		}
	}
	return store.HAMember{}, false
}

// topology는 primary를 주 노드로 둔 클러스터 모양이다.
func (hc *haCluster) topology(primary string) ha.Topology {
	t := ha.Topology{RootName: hc.name(hc.c.RootServiceID), Primary: hc.name(primary)}
	for _, m := range hc.members {
		switch m.Role {
		case catalog.RoleRoot, catalog.RoleReplica:
			t.Data = append(t.Data, hc.name(m.ServiceID))
		case catalog.RoleInternal:
			t.Internal = append(t.Internal, hc.name(m.ServiceID))
		case catalog.RoleEdge:
			t.Edge = hc.name(m.ServiceID)
		}
	}
	return t
}

// ---- views ----

type haSelectorView struct {
	Role string `json:"role"`
	*catalog.HASelector
	Default int `json:"default"`
}

type haCompanionView struct {
	Key               string           `json:"key"`
	Name              string           `json:"name"`
	Description       string           `json:"description"`
	Selectors         []haSelectorView `json:"selectors"`
	SupportedVersions []int            `json:"supported_versions"`
	Version           string           `json:"version,omitempty"`
}

type haMemberView struct {
	ServiceID      string     `json:"service_id"`
	Name           string     `json:"name"`
	Role           string     `json:"role"` // root | replica | internal | edge
	Ordinal        int        `json:"ordinal"`
	NodeID         string     `json:"node_id"`
	InstanceStatus string     `json:"instance_status"`
	DBRole         string     `json:"db_role"` // primary | replica | uninitialized | recovering | unknown (데이터 노드), "" (edge)
	Primary        bool       `json:"primary"` // Aven이 기록한 주 노드
	Healthy        bool       `json:"healthy"`
	Streaming      bool       `json:"streaming"`
	LagBytes       *uint64    `json:"lag_bytes,omitempty"`
	Error          string     `json:"error,omitempty"`
	CheckedAt      *time.Time `json:"checked_at,omitempty"`
}

type haClusterView struct {
	ID                 string         `json:"id"`
	Kind               string         `json:"kind"`
	EnvironmentID      string         `json:"environment_id"`
	RootServiceID      string         `json:"root_service_id"`
	PrimaryServiceID   string         `json:"primary_service_id"`
	Status             string         `json:"status"`
	AutoFailover       bool           `json:"auto_failover"`
	KeelFailover       bool           `json:"keel_failover"` // Aven이 승격하는 엔진(postgres). 아니면 엔진이 스스로 뽑는다
	Busy               bool           `json:"busy"`
	Message            string         `json:"message"`
	CompanionTemplate  string         `json:"companion_template"`
	StandaloneTemplate string         `json:"standalone_template"`
	Counts             map[string]int `json:"counts"`
	ReplicaOptions     []int          `json:"replica_options"` // 동반 템플릿의 replica 선택지(scale)
	Endpoint           *haEndpoint    `json:"endpoint"`
	Members            []haMemberView `json:"members"`
	CreatedAt          time.Time      `json:"created_at"`
}

type haEndpoint struct {
	ServiceID   string `json:"service_id"`
	Name        string `json:"name"`
	PrivateHost string `json:"private_host"`
	URLVariable string `json:"url_variable"`
}

var haURLVar = map[string]string{"postgres": "DATABASE_URL", "redis": "REDIS_URL", "mongo": "MONGO_URL", "mysql": "MYSQL_URL"}

func (s *Server) haView(ctx context.Context, hc *haCluster) haClusterView {
	c := hc.c
	rt := haRT(c.ID)
	v := haClusterView{ID: c.ID, Kind: c.Kind, EnvironmentID: c.EnvironmentID, RootServiceID: c.RootServiceID, PrimaryServiceID: c.PrimaryServiceID,
		Status: c.Status, AutoFailover: c.AutoFailover, KeelFailover: ha.AutoFailoverByKeel(c.Kind), Busy: rt.isBusy(), Message: c.Message,
		CompanionTemplate: c.CompanionTemplate, StandaloneTemplate: c.StandaloneTemplate, Counts: c.Saved.Counts, Members: []haMemberView{}, CreatedAt: c.CreatedAt}
	if v.Counts == nil {
		v.Counts = map[string]int{}
	}
	v.ReplicaOptions = []int{}
	if comp, err := s.haDefinition(ctx, nil, c.CompanionTemplate); err == nil {
		if root := comp.HACompanionRoot(); root != nil && root.HAConversionConfig.Replica != nil {
			v.ReplicaOptions = root.HAConversionConfig.Replica.Options
		}
	}
	ep := c.RootServiceID
	if edges := hc.byRole(catalog.RoleEdge); len(edges) > 0 {
		ep = edges[0].ServiceID
	}
	v.Endpoint = &haEndpoint{ServiceID: ep, Name: hc.name(ep), PrivateHost: deploy.PrivateDomain(hc.name(ep), hc.env.Name), URLVariable: haURLVar[c.Kind]}
	primaryOffset := rt.state(c.PrimaryServiceID).Probe.Offset
	for _, m := range hc.members {
		in := hc.insts[m.ServiceID]
		mv := haMemberView{ServiceID: m.ServiceID, Name: hc.name(m.ServiceID), Role: m.Role, Ordinal: m.Ordinal,
			NodeID: store.NormNode(in.NodeID), InstanceStatus: in.Status, Primary: m.ServiceID == c.PrimaryServiceID}
		if isDataRole(m.Role) {
			st := rt.state(m.ServiceID)
			mv.DBRole, mv.Error = "unknown", st.Err
			if !st.CheckedAt.IsZero() {
				t := st.CheckedAt
				mv.CheckedAt = &t
			}
			if st.OK {
				mv.DBRole, mv.Streaming = st.Probe.Role, st.Probe.Streaming
				mv.Healthy = in.Status == store.InstOnline
				if st.Probe.Role == "replica" && c.Kind != "mongo" && primaryOffset > 0 {
					lag := ha.LagBytes(primaryOffset, st.Probe.Offset)
					mv.LagBytes = &lag
				}
			}
		} else {
			mv.Healthy = in.Status == store.InstOnline
		}
		v.Members = append(v.Members, mv)
	}
	return v
}

func companionView(p *haPlan) *haCompanionView {
	cfg := p.root.HAConversionConfig
	v := &haCompanionView{Key: p.companionKey, Name: p.companion.Name, Description: cfg.Description, Selectors: []haSelectorView{},
		SupportedVersions: cfg.SupportedImageMajorVersions}
	if p.version.Major > 0 {
		v.Version = p.version.String()
	}
	for _, role := range []string{catalog.RoleReplica, catalog.RoleInternal, catalog.RoleEdge} {
		if sel := cfg.Selector(role); sel != nil && p.companion.HAPrototype(role) != nil {
			v.Selectors = append(v.Selectors, haSelectorView{Role: role, HASelector: sel, Default: sel.Default()})
		}
	}
	return v
}

func (s *Server) getHA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	out := map[string]any{"cluster": nil}
	if cl, err := s.Store.GetHAClusterForService(ctx, c.svc.ID, c.env.ID); err == nil {
		hc, err := s.loadHACluster(ctx, cl)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		v := s.haView(ctx, hc)
		out["cluster"] = &v
		out["eligible"], out["problems"] = false, []string{}
		writeJSON(w, 200, out)
		return
	}
	plan, probs := s.haEligibility(ctx, r, c)
	out["eligible"], out["problems"] = len(probs) == 0, nonNil(probs)
	if plan != nil {
		out["companion"] = companionView(plan)
	}
	writeJSON(w, 200, out)
}

func (s *Server) listEnvHA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cls, err := s.Store.ListHAClusters(ctx, r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []haClusterView{}
	for _, cl := range cls {
		if hc, err := s.loadHACluster(ctx, cl); err == nil {
			out = append(out, s.haView(ctx, hc))
		}
	}
	writeJSON(w, 200, out)
}

// ---- 전환(convert) ----

type haConvertBody struct {
	Replicas     *int  `json:"replicas"`
	Internal     *int  `json:"internal"`
	Edge         *int  `json:"edge"`
	AutoFailover *bool `json:"auto_failover"`
	SkipBackup   bool  `json:"skip_backup"`
}

// haMemberDefs는 동반 템플릿을 한 번 계산한 서비스 정의다(비밀값은 이때 만든다). 원형 이름 → 정의.
func haMemberDefs(comp *catalog.Definition) (map[string]catalog.ServiceDef, error) {
	defs, _, err := comp.Instantiate(nil)
	if err != nil {
		return nil, err
	}
	out := map[string]catalog.ServiceDef{}
	for _, d := range defs {
		out[d.Name] = d
	}
	return out, nil
}

// templateVars는 원형이 선언한 변수만 남긴다(DB 원형에 붙는 기본 DB 변수와 새 비밀번호는 버린다). rename으로 원형
// 서비스 이름 참조를 실제 이름으로 바꾼다.
func templateVars(spec *catalog.ServiceSpec, def catalog.ServiceDef, rename func(string) string) map[string]string {
	out := map[string]string{}
	for k := range spec.Variables {
		out[k] = rename(def.Variables[k])
	}
	return out
}

func (s *Server) convertHA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var body haConvertBody
	if !decode(w, r, &body) {
		return
	}
	if _, err := s.Store.GetHAClusterForService(ctx, c.svc.ID, c.env.ID); err == nil {
		writeErr(w, 409, "이미 HA 클러스터에 속한 서비스입니다")
		return
	}
	plan, probs := s.haEligibility(ctx, r, c)
	if len(probs) > 0 {
		writeJSON(w, 409, map[string]any{"error": probs[0], "problems": probs})
		return
	}
	cfg := plan.root.HAConversionConfig
	protos := map[string]bool{}
	requested := map[string]int{}
	for role, p := range map[string]*int{catalog.RoleReplica: body.Replicas, catalog.RoleInternal: body.Internal, catalog.RoleEdge: body.Edge} {
		if plan.companion.HAPrototype(role) != nil {
			protos[role] = true
		}
		if p != nil {
			requested[role] = *p
		}
	}
	counts, err := ha.Counts(cfg, requested, protos)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	defs, err := haMemberDefs(plan.companion)
	if err != nil {
		writeErr(w, 400, "HA 템플릿을 계산하지 못했습니다: "+err.Error())
		return
	}
	cl, migrated, err := s.haCreate(ctx, c, plan, counts, defs, body.AutoFailover == nil || *body.AutoFailover)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	hc, err := s.loadHACluster(ctx, cl)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "database.ha.converted",
		fmt.Sprintf("%s (%s): HA 클러스터로 전환 (%s)", c.svc.Name, c.env.Name, haCountsText(counts)))
	rt := haRT(cl.ID)
	rt.tryBusy()
	bg := context.WithoutCancel(ctx)
	haGo(func() {
		defer rt.done()
		s.haDeployConverted(bg, hc, !body.SkipBackup)
	})
	v := s.haView(ctx, hc)
	writeJSON(w, 202, map[string]any{"cluster": v, "migrated_services": nonNil(migrated)})
}

func haCountsText(counts map[string]int) string {
	var parts []string
	for _, role := range ha.SortedRoles(counts) {
		parts = append(parts, fmt.Sprintf("%s %d", role, counts[role]))
	}
	return strings.Join(parts, ", ")
}

// haCreate는 구성원 서비스를 만들고 root와 배선 변수, TCP proxy, 다른 서비스의 참조를 바꾼 뒤 클러스터를 저장한다.
// 배포는 하지 않는다(haDeployConverted). 참조를 바꾼 서비스 이름을 돌려준다.
func (s *Server) haCreate(ctx context.Context, c *instCtx, plan *haPlan, counts map[string]int, defs map[string]catalog.ServiceDef, auto bool) (*store.HACluster, []string, error) {
	rootSvc, rootIn := c.svc, c.in
	rootProto := plan.root
	wiring := rootProto.ClusterWiring
	nodes := s.haCandidateNodes(ctx)
	rootNode := store.NormNode(s.Deploy.HomeNode(ctx, rootIn))
	used := map[string]int{rootNode: 1}
	edgeName := ""
	if p := plan.companion.HAPrototype(catalog.RoleEdge); p != nil {
		edgeName = ha.MemberName(rootSvc.Name, rootProto.Name, p.Name, 0, false)
	}
	members := []store.HAMember{{ServiceID: rootSvc.ID, Role: catalog.RoleRoot}}
	created := map[string]*store.Instance{} // service id → 새 인스턴스

	var protoEdgeName string
	if p := plan.companion.HAPrototype(catalog.RoleEdge); p != nil {
		protoEdgeName = p.Name
	}
	var actualEdge string
	rename := func(v string) string {
		v = renameRef(v, rootProto.Name, rootSvc.Name)
		if protoEdgeName != "" && actualEdge != "" {
			v = renameRef(v, protoEdgeName, actualEdge)
		}
		return v
	}
	for _, role := range []string{catalog.RoleReplica, catalog.RoleInternal, catalog.RoleEdge} {
		proto := plan.companion.HAPrototype(role)
		n := counts[role]
		if proto == nil || n == 0 {
			continue
		}
		if role == catalog.RoleEdge {
			// edge는 서비스 하나에 replica n개(여러 노드에 나눠 띄운다)
			name, err := s.uniqueServiceName(ctx, rootSvc.ProjectID, edgeName)
			if err != nil {
				return nil, nil, err
			}
			actualEdge = name
			order, per := ha.Placement(ha.SpreadNodes(nodes, nil, n))
			svc, in, err := s.haCreateMember(ctx, c, plan, proto, defs[proto.Name], name, order[0], rename)
			if err != nil {
				return nil, nil, err
			}
			in.Replicas = n
			if len(order) > 1 {
				for _, id := range order {
					in.Placement = append(in.Placement, store.Placement{NodeID: id, Replicas: per[id]})
				}
			}
			created[svc.ID] = in
			members = append(members, store.HAMember{ServiceID: svc.ID, Role: role})
			continue
		}
		placed := ha.SpreadNodes(nodes, used, n)
		for i := 0; i < n; i++ {
			used[placed[i]]++
			name, err := s.uniqueServiceName(ctx, rootSvc.ProjectID, ha.MemberName(rootSvc.Name, rootProto.Name, proto.Name, i+1, true))
			if err != nil {
				return nil, nil, err
			}
			svc, in, err := s.haCreateMember(ctx, c, plan, proto, defs[proto.Name], name, placed[i], rename)
			if err != nil {
				return nil, nil, err
			}
			created[svc.ID] = in
			members = append(members, store.HAMember{ServiceID: svc.ID, Role: role, Ordinal: i + 1})
		}
	}

	cl := &store.HACluster{ProjectID: c.p.ID, EnvironmentID: c.env.ID, RootServiceID: rootSvc.ID, Kind: plan.kind,
		CompanionTemplate: plan.companionKey, StandaloneTemplate: rootProto.RevertsToTemplateCode, PrimaryServiceID: rootSvc.ID,
		Status: store.HAProvisioning, AutoFailover: auto, Message: "구성원을 배포하는 중입니다"}
	if cl.StandaloneTemplate == "" {
		cl.StandaloneTemplate = plan.standaloneKey
	}
	// root: 시작 명령, 사설 포트, 원형 변수와 배선 변수. 바꾼 값은 되돌리기용으로 저장한다.
	saved := store.HASaved{Vars: map[string]*string{}, Counts: counts, PrivatePorts: rootIn.PrivatePorts}
	if rootIn.StartCommand != nil {
		sc := *rootIn.StartCommand
		saved.StartCommand = &sc
	}
	setRoot := func(k, v string) {
		if _, seen := saved.Vars[k]; !seen {
			if old, ok := rootIn.Variables[k]; ok {
				saved.Vars[k] = &old
			} else {
				saved.Vars[k] = nil
			}
		}
		rootIn.Variables[k] = v
	}
	rootDef := defs[rootProto.Name]
	for k, v := range templateVars(rootProto, rootDef, rename) {
		setRoot(k, v)
	}
	if rootProto.StartCommand != nil {
		sc := *rootProto.StartCommand
		rootIn.StartCommand = &sc
	}
	if rootProto.PrivatePorts != nil {
		rootIn.PrivatePorts = append([]int{}, rootProto.PrivatePorts...)
	}

	hc := &haCluster{c: cl, members: members, svcs: map[string]*store.Service{rootSvc.ID: rootSvc}, insts: map[string]*store.Instance{rootSvc.ID: rootIn}, env: c.env}
	for id, in := range created {
		svc, err := s.Store.GetService(ctx, id)
		if err != nil {
			return nil, nil, err
		}
		hc.svcs[id], hc.insts[id] = svc, in
	}
	top := hc.topology(rootSvc.ID)
	for k, v := range ha.DataVars(wiring, top, rootSvc.Name, "") {
		setRoot(k, v)
	}
	for k, v := range haEngineRootVars(plan.kind, hc) {
		setRoot(k, v)
	}
	for _, m := range members[1:] {
		in := hc.insts[m.ServiceID]
		var wv map[string]string
		switch m.Role {
		case catalog.RoleReplica:
			wv = ha.DataVars(wiring, top, hc.name(m.ServiceID), "")
		case catalog.RoleInternal:
			wv = ha.InternalVars(wiring, top, hc.name(m.ServiceID))
		case catalog.RoleEdge:
			wv = ha.EdgeVars(wiring, top)
		}
		for k, v := range wv {
			in.Variables[k] = v
		}
	}

	if edges := hc.byRole(catalog.RoleEdge); len(edges) > 0 && rootIn.TCPProxy != nil {
		edge := hc.insts[edges[0].ServiceID]
		port, err := s.Deploy.AllocateTCPPortFor(ctx, s.Deploy.HomeNode(ctx, edge))
		if err != nil {
			s.Log.Warn("ha: tcp proxy for edge", "err", err)
		} else {
			edge.TCPProxy = &store.TCPProxy{ContainerPort: edge.Spec.EffectivePort(edge.Variables), PublicPort: port}
			rootIn.TCPProxy = nil
			saved.TCPProxyMoved = true
		}
	}
	cl.Saved = saved
	for _, m := range members {
		if err := s.Store.UpdateInstanceConfig(ctx, hc.insts[m.ServiceID]); err != nil {
			return nil, nil, err
		}
	}
	if err := s.Store.CreateHACluster(ctx, cl, members); err != nil {
		return nil, nil, err
	}
	if saved.TCPProxyMoved {
		for _, m := range []store.HAMember{members[0], hc.byRole(catalog.RoleEdge)[0]} {
			if err := s.Deploy.SyncTCP(ctx, hc.insts[m.ServiceID].ID); err != nil {
				s.Log.Warn("ha: tcp proxy", "err", err)
			}
		}
	}
	// 다른 서비스가 root의 연결 변수를 참조하면 edge의 같은 변수를 보게 한다(배포는 사용자가 한다).
	var migrated []string
	if top.Edge != "" {
		edgeProto := plan.companion.HAPrototype(catalog.RoleEdge)
		keys := map[string]bool{}
		for k := range edgeProto.Variables {
			keys[k] = true
		}
		var err error
		if migrated, err = s.haMigrateRefs(ctx, hc, rootSvc.Name, top.Edge, keys); err != nil {
			return nil, nil, err
		}
	}
	return cl, migrated, nil
}

// renameRef는 ${{ from.X }} 참조를 ${{ to.X }}로 바꾼다.
func renameRef(v, from, to string) string {
	if from == to || !strings.Contains(v, "${{") {
		return v
	}
	keys := map[string]bool{}
	for _, ref := range vars.References(v) {
		if scope, name, ok := strings.Cut(ref, "."); ok && scope == from {
			keys[name] = true
		}
	}
	return ha.MigrateRefs(v, from, to, keys)
}

// haEngineRootVars는 엔진별로 root에 더 넣는 변수다. mongo는 edge가 없으므로 root의 MONGO_URL이
// replica set 전체(replicaSet=rs0)를 가리키게 해서 기존 참조가 그대로 클러스터로 연결되게 한다.
func haEngineRootVars(kind string, hc *haCluster) map[string]string {
	if kind != "mongo" {
		return nil
	}
	var hosts []string
	for _, m := range hc.data() {
		hosts = append(hosts, ha.HostRef(hc.name(m.ServiceID))+":27017")
	}
	return map[string]string{
		"MONGO_URL": "mongodb://${{MONGOUSER}}:${{MONGOPASSWORD}}@" + strings.Join(hosts, ",") + "/?replicaSet=${{KEEL_HA_REPLSET}}&authSource=admin",
	}
}

// haCreateMember는 구성원 서비스 하나와 이 환경의 인스턴스(+볼륨)를 만든다. 다른 환경에는 인스턴스를 만들지 않는다.
func (s *Server) haCreateMember(ctx context.Context, c *instCtx, plan *haPlan, proto *catalog.ServiceSpec, def catalog.ServiceDef, name, nodeID string, rename func(string) string) (*store.Service, *store.Instance, error) {
	tmpl := plan.companionKey
	svc := &store.Service{ProjectID: c.p.ID, Name: name, SourceType: def.SourceType, Template: &tmpl}
	if def.Icon != "" {
		icon := def.Icon
		svc.Icon = &icon
	}
	d := def
	d.Variables = templateVars(proto, def, rename)
	if proto.Source.Database != "" {
		k := plan.kind
		svc.DatabaseKind = &k
		if c.in.Image != nil {
			d.Image = *c.in.Image // 데이터 노드는 root와 같은 이미지(같은 버전)를 쓴다
		}
	}
	if err := s.Store.CreateService(ctx, svc); err != nil {
		return nil, nil, err
	}
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: c.env.ID, NodeID: nodeID}
	in.Spec.Defaults()
	d.ApplyToInstance(in)
	if err := s.Store.CreateInstance(ctx, in); err != nil {
		return nil, nil, err
	}
	existing, _ := s.Store.ListVolumesByEnv(ctx, c.env.ID)
	taken := map[string]bool{}
	for _, v := range existing {
		taken[v.Name] = true
	}
	for _, mp := range d.Volumes {
		vn := volumeName(svc.Name, mp)
		// 되돌리기는 구성원 볼륨을 지우지 않는다: 같은 이름이 남아 있으면 번호를 붙인다.
		for i := 2; taken[vn]; i++ {
			vn = fmt.Sprintf("%s-%d", volumeName(svc.Name, mp), i)
		}
		taken[vn] = true
		v := &store.Volume{ProjectID: c.p.ID, EnvironmentID: c.env.ID, InstanceID: &in.ID, Name: vn, MountPath: mp}
		if lim, ok := d.VolumeLimits[mp]; ok {
			l := lim
			v.SizeLimitMB = &l
		}
		if err := s.Store.CreateVolume(ctx, v); err != nil {
			return nil, nil, err
		}
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &svc.ID, "service.created", "서비스 "+svc.Name+" 생성 (HA 구성원)")
	return svc, in, nil
}

// haMigrateRefs는 클러스터 밖 서비스와 공유 변수에서 ${{ from.KEY }}를 ${{ to.KEY }}로 바꾼다(KEY는 keys).
// 바꾼 서비스 이름을 돌려준다.
func (s *Server) haMigrateRefs(ctx context.Context, hc *haCluster, from, to string, keys map[string]bool) ([]string, error) {
	insts, err := s.Store.ListInstancesByEnv(ctx, hc.c.EnvironmentID)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, in := range insts {
		if _, member := hc.insts[in.ServiceID]; member {
			continue
		}
		changed := false
		for k, v := range in.Variables {
			if nv := ha.MigrateRefs(v, from, to, keys); nv != v {
				in.Variables[k], changed = nv, true
			}
		}
		if !changed {
			continue
		}
		if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
			return nil, err
		}
		if svc, err := s.Store.GetService(ctx, in.ServiceID); err == nil {
			names = append(names, svc.Name)
		}
	}
	shared, err := s.Store.GetSharedVars(ctx, hc.c.EnvironmentID)
	if err == nil {
		changed := false
		for k, v := range shared {
			if nv := ha.MigrateRefs(v, from, to, keys); nv != v {
				shared[k], changed = nv, true
			}
		}
		if changed {
			if err := s.Store.SetSharedVars(ctx, hc.c.EnvironmentID, shared); err != nil {
				return nil, err
			}
			names = append(names, "shared")
		}
	}
	sort.Strings(names)
	return names, nil
}

// haDeployConverted는 root 볼륨을 백업하고 root, 데이터 노드, 조정 노드, edge 순서로 배포한다.
func (s *Server) haDeployConverted(ctx context.Context, hc *haCluster, backup bool) {
	root := hc.insts[hc.c.RootServiceID]
	if backup {
		vols, _ := s.Store.ListVolumesByInstance(ctx, root.ID)
		for _, v := range vols {
			if err := haBackup(s, ctx, v); err != nil {
				s.Log.Warn("ha: backup before conversion", "volume", v.Name, "err", err)
				s.haNote(ctx, hc.c, "전환 전 볼륨 백업 실패: "+err.Error())
			}
		}
	}
	for _, m := range hc.members {
		s.haEnqueue(ctx, hc, m.ServiceID, "ha")
	}
}

// haEnqueue는 구성원을 다시 배포한다(인스턴스의 현재 설정과 변수로). 실패는 기록만 한다.
func (s *Server) haEnqueue(ctx context.Context, hc *haCluster, serviceID, trigger string) error {
	in, err := s.Store.GetInstanceFor(ctx, serviceID, hc.c.EnvironmentID)
	if err != nil {
		return err
	}
	if _, err := s.Deploy.Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: trigger}); err != nil {
		s.Log.Warn("ha: deploy member", "service", hc.name(serviceID), "err", err)
		return err
	}
	return nil
}

// haNote는 클러스터 메시지를 바꾸고 저장한다.
func (s *Server) haNote(ctx context.Context, c *store.HACluster, msg string) {
	c.Message = msg
	if err := s.Store.UpdateHACluster(ctx, c); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.Log.Warn("ha: update cluster", "err", err)
	}
}

// ---- 설정 바꾸기(PATCH) ----

func (s *Server) loadHAFor(w http.ResponseWriter, r *http.Request) (*instCtx, *haCluster, bool) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return nil, nil, false
	}
	cl, err := s.Store.GetHAClusterForService(r.Context(), c.svc.ID, c.env.ID)
	if err != nil {
		writeErr(w, 404, "HA 클러스터가 아닙니다")
		return nil, nil, false
	}
	hc, err := s.loadHACluster(r.Context(), cl)
	if err != nil {
		s.storeErr(w, err)
		return nil, nil, false
	}
	return c, hc, true
}

func (s *Server) patchHA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, hc, ok := s.loadHAFor(w, r)
	if !ok {
		return
	}
	var body struct {
		AutoFailover *bool `json:"auto_failover"`
		Replicas     *int  `json:"replicas"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.AutoFailover != nil && *body.AutoFailover != hc.c.AutoFailover {
		hc.c.AutoFailover = *body.AutoFailover
		state := "껐습니다"
		if hc.c.AutoFailover {
			state = "켰습니다"
		}
		s.haNote(ctx, hc.c, "자동 failover를 "+state)
	}
	if body.Replicas != nil && *body.Replicas != len(hc.byRole(catalog.RoleReplica)) {
		rt := haRT(hc.c.ID)
		if !rt.tryBusy() {
			writeErr(w, 409, "다른 HA 작업이 진행 중입니다")
			return
		}
		err := s.haScale(ctx, r, c, hc, *body.Replicas)
		rt.done()
		if err != nil {
			var ve *haValidation
			if errors.As(err, &ve) {
				writeErr(w, ve.code, ve.msg)
				return
			}
			s.storeErr(w, err)
			return
		}
		cl, err := s.Store.GetHACluster(ctx, hc.c.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if hc, err = s.loadHACluster(ctx, cl); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	writeJSON(w, 200, map[string]any{"cluster": s.haView(ctx, hc)})
}

type haValidation struct {
	code int
	msg  string
}

func (e *haValidation) Error() string { return e.msg }

// haScale은 replica 수를 n으로 바꾼다. 늘리면 새 replica를 만들어 배포하고, 줄이면 번호가 큰 replica부터
// 지운다(주 노드는 지우지 않는다. 볼륨은 남긴다). edge와 데이터 노드의 배선 변수를 다시 채운다.
func (s *Server) haScale(ctx context.Context, r *http.Request, c *instCtx, hc *haCluster, n int) error {
	comp, err := s.haDefinition(ctx, r, hc.c.CompanionTemplate)
	if err != nil {
		return &haValidation{409, "HA 동반 템플릿을 찾을 수 없습니다"}
	}
	rootProto := comp.HACompanionRoot()
	proto := comp.HAPrototype(catalog.RoleReplica)
	if rootProto == nil || proto == nil {
		return &haValidation{409, "HA 동반 템플릿에 replica 원형이 없습니다"}
	}
	if sel := rootProto.HAConversionConfig.Replica; sel != nil && !sel.Allows(n) {
		return &haValidation{400, fmt.Sprintf("replica 수는 %v 중 하나여야 합니다", sel.Options)}
	}
	rootSvc := hc.svcs[hc.c.RootServiceID]
	rootIn := hc.insts[hc.c.RootServiceID]
	reps := hc.byRole(catalog.RoleReplica)
	var added, removed []string
	if n > len(reps) {
		defs, err := haMemberDefs(comp)
		if err != nil {
			return err
		}
		plan := &haPlan{kind: hc.c.Kind, companionKey: hc.c.CompanionTemplate, companion: comp, root: rootProto}
		rc := &instCtx{svc: rootSvc, env: hc.env, p: c.p, in: rootIn}
		used := map[string]int{}
		next := 0
		for _, m := range hc.data() {
			used[store.NormNode(hc.insts[m.ServiceID].NodeID)]++
			if m.Ordinal > next {
				next = m.Ordinal
			}
		}
		nodes := ha.SpreadNodes(s.haCandidateNodes(ctx), used, n-len(reps))
		rename := func(v string) string { return renameRef(v, rootProto.Name, rootSvc.Name) }
		for i, nodeID := range nodes {
			ord := next + i + 1
			name, err := s.uniqueServiceName(ctx, rootSvc.ProjectID, ha.MemberName(rootSvc.Name, rootProto.Name, proto.Name, ord, true))
			if err != nil {
				return err
			}
			svc, in, err := s.haCreateMember(ctx, rc, plan, proto, defs[proto.Name], name, nodeID, rename)
			if err != nil {
				return err
			}
			m := store.HAMember{ServiceID: svc.ID, Role: catalog.RoleReplica, Ordinal: ord}
			if err := s.Store.AddHAMember(ctx, hc.c.ID, m); err != nil {
				return err
			}
			hc.members = append(hc.members, m)
			hc.svcs[svc.ID], hc.insts[svc.ID] = svc, in
			added = append(added, svc.ID)
		}
	} else {
		sort.Slice(reps, func(i, j int) bool { return reps[i].Ordinal > reps[j].Ordinal })
		for _, m := range reps {
			if len(reps)-len(removed) <= n {
				break
			}
			if m.ServiceID == hc.c.PrimaryServiceID {
				continue
			}
			removed = append(removed, m.ServiceID)
		}
		if len(reps)-len(removed) > n {
			return &haValidation{409, "주 노드인 replica는 지울 수 없습니다. 먼저 다른 노드를 주 노드로 만드세요"}
		}
		for _, id := range removed {
			s.Store.RemoveHAMember(ctx, hc.c.ID, id)
			s.haDeleteMember(ctx, hc, id)
		}
		keep := hc.members[:0]
		for _, m := range hc.members {
			if !containsString(removed, m.ServiceID) {
				keep = append(keep, m)
			}
		}
		hc.members = keep
	}
	// 배선을 다시 채운다: 새 구성원은 배포하고, 구성원 목록을 보는 edge도 다시 배포한다(Postgres edge는 주 노드만 보므로 그대로).
	if err := s.haRewire(ctx, hc, comp, hc.c.PrimaryServiceID, "", ""); err != nil {
		return err
	}
	hc.c.Saved.Counts[catalog.RoleReplica] = n
	if len(added) > 0 {
		hc.c.Status = store.HAProvisioning
	}
	s.haNote(ctx, hc.c, fmt.Sprintf("replica 수를 %d개로 바꿨습니다", n))
	for _, id := range added {
		s.haEnqueue(ctx, hc, id, "ha")
	}
	if w := rootProto.ClusterWiring; w != nil && !w.EdgePrimaryOnly && w.DataNodesVariable != "" {
		for _, e := range hc.byRole(catalog.RoleEdge) {
			s.haEnqueue(ctx, hc, e.ServiceID, "ha")
		}
	}
	s.Events.Emit(ctx, hc.c.ProjectID, &hc.c.EnvironmentID, &hc.c.RootServiceID, "database.ha.scaled",
		fmt.Sprintf("%s (%s): HA replica %d개", rootSvc.Name, hc.env.Name, n))
	return nil
}

// haRewire는 primary를 주 노드로 두고 모든 구성원의 배선 변수를 다시 채워 저장한다(배포는 하지 않는다).
// rejoinFor 구성원은 rejoin 방법을 받는다(강등된 이전 주 노드).
func (s *Server) haRewire(ctx context.Context, hc *haCluster, comp *catalog.Definition, primary, rejoinFor, rejoin string) error {
	root := comp.HACompanionRoot()
	if root == nil {
		return errors.New("HA companion template has no root")
	}
	top := hc.topology(primary)
	for _, m := range hc.members {
		in := hc.insts[m.ServiceID]
		var wv map[string]string
		switch m.Role {
		case catalog.RoleRoot, catalog.RoleReplica:
			rj := ""
			if m.ServiceID == rejoinFor {
				rj = rejoin
			}
			wv = ha.DataVars(root.ClusterWiring, top, hc.name(m.ServiceID), rj)
		case catalog.RoleInternal:
			wv = ha.InternalVars(root.ClusterWiring, top, hc.name(m.ServiceID))
		case catalog.RoleEdge:
			wv = ha.EdgeVars(root.ClusterWiring, top)
		}
		if m.ServiceID == hc.c.RootServiceID {
			for k, v := range haEngineRootVars(hc.c.Kind, hc) {
				wv[k] = v
			}
		}
		changed := false
		for k, v := range wv {
			if in.Variables[k] != v {
				in.Variables[k], changed = v, true
			}
		}
		if changed {
			if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
				return err
			}
		}
	}
	return nil
}

// haDeleteMember는 구성원 서비스를 지운다. 볼륨은 떼어서 남긴다(되돌리기는 데이터를 지우지 않는다).
func (s *Server) haDeleteMember(ctx context.Context, hc *haCluster, serviceID string) {
	svc := hc.svcs[serviceID]
	in := hc.insts[serviceID]
	if svc == nil || in == nil {
		return
	}
	if vols, err := s.Store.ListVolumesByInstance(ctx, in.ID); err == nil {
		for _, v := range vols {
			v.InstanceID = nil
			if err := s.Store.UpdateVolume(ctx, v); err != nil {
				s.Log.Warn("ha: detach volume", "volume", v.Name, "err", err)
			}
		}
	}
	if err := haDeleteService(s, ctx, svc); err != nil {
		s.Log.Warn("ha: delete member", "service", svc.Name, "err", err)
		return
	}
	s.Events.EmitNamed(ctx, svc.ProjectID, nil, &svc.ID, &svc.Name, "service.deleted", "서비스 "+svc.Name+" 삭제 (HA 구성원)")
}

// ---- 되돌리기(revert) ----

func (s *Server) revertHA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, hc, ok := s.loadHAFor(w, r)
	if !ok {
		return
	}
	rt := haRT(hc.c.ID)
	if !rt.tryBusy() {
		writeErr(w, 409, "다른 HA 작업이 진행 중입니다")
		return
	}
	defer rt.done()
	rootID := hc.c.RootServiceID
	rootIn := hc.insts[rootID]
	// 원래 서비스가 주 노드여야 한다(다른 노드를 지우면 최신 데이터를 잃는다).
	p, err := s.haProbe(ctx, hc.c.Kind, rootIn)
	switch {
	case hc.c.PrimaryServiceID != rootID:
		writeErr(w, 409, fmt.Sprintf("원래 서비스 %s가 주 노드가 아닙니다. 먼저 %s를 주 노드로 만드세요", hc.name(rootID), hc.name(rootID)))
		return
	case err != nil:
		writeErr(w, 409, "원래 서비스의 상태를 확인하지 못했습니다: "+err.Error())
		return
	case p.Role != "primary":
		writeErr(w, 409, fmt.Sprintf("원래 서비스 %s가 주 노드가 아닙니다(현재 %s). 먼저 주 노드로 만드세요", hc.name(rootID), p.Role))
		return
	}
	if hc.c.Kind == "mongo" {
		host := ha.MongoAddr(deploy.PrivateDomain(hc.name(rootID), hc.env.Name))
		if _, err := s.haRun(ctx, rootIn, func(vs map[string]string) ha.Command { return ha.MongoStandaloneCommand(vs, host) }, haProbeTimeout); err != nil {
			writeErr(w, 502, "replica set을 한 노드로 줄이지 못했습니다: "+err.Error())
			return
		}
	}
	// 참조를 root로 되돌리고 edge의 공개 접속을 root에 다시 연다.
	comp, _ := s.haDefinition(ctx, nil, hc.c.CompanionTemplate)
	if edges := hc.byRole(catalog.RoleEdge); len(edges) > 0 {
		edgeID := edges[0].ServiceID
		keys := map[string]bool{}
		if comp != nil {
			if p := comp.HAPrototype(catalog.RoleEdge); p != nil {
				for k := range p.Variables {
					keys[k] = true
				}
			}
		}
		for k := range hc.insts[edgeID].Variables {
			keys[k] = true
		}
		if _, err := s.haMigrateRefs(ctx, hc, hc.name(edgeID), hc.name(rootID), keys); err != nil {
			s.storeErr(w, err)
			return
		}
		if hc.insts[edgeID].TCPProxy != nil && rootIn.TCPProxy == nil {
			if port, err := s.Deploy.AllocateTCPPortFor(ctx, s.Deploy.HomeNode(ctx, rootIn)); err == nil {
				rootIn.TCPProxy = &store.TCPProxy{ContainerPort: rootIn.Spec.EffectivePort(rootIn.Variables), PublicPort: port}
			} else {
				s.Log.Warn("ha: tcp proxy for root", "err", err)
			}
		}
	}
	// root 설정 복원
	saved := hc.c.Saved
	rootIn.StartCommand = saved.StartCommand
	rootIn.PrivatePorts = saved.PrivatePorts
	for k, old := range saved.Vars {
		if old == nil {
			delete(rootIn.Variables, k)
		} else {
			rootIn.Variables[k] = *old
		}
	}
	if comp != nil {
		if root := comp.HACompanionRoot(); root != nil {
			for _, k := range ha.WiringKeys(root.ClusterWiring) {
				if _, kept := saved.Vars[k]; !kept {
					delete(rootIn.Variables, k)
				}
			}
		}
	}
	if err := s.Store.UpdateInstanceConfig(ctx, rootIn); err != nil {
		s.storeErr(w, err)
		return
	}
	for _, m := range hc.members {
		if m.ServiceID != rootID {
			s.haDeleteMember(ctx, hc, m.ServiceID)
		}
	}
	if err := s.Store.DeleteHACluster(ctx, hc.c.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.storeErr(w, err)
		return
	}
	haForget(hc.c.ID)
	if rootIn.TCPProxy != nil {
		if err := s.Deploy.SyncTCP(ctx, rootIn.ID); err != nil {
			s.Log.Warn("ha: tcp proxy", "err", err)
		}
	}
	s.haEnqueue(ctx, hc, rootID, "ha")
	s.Events.Emit(ctx, hc.c.ProjectID, &hc.c.EnvironmentID, &rootID, "database.ha.reverted",
		fmt.Sprintf("%s (%s): 단독 데이터베이스로 되돌림 (구성원 볼륨은 남김)", hc.name(rootID), hc.env.Name))
	w.WriteHeader(204)
}

// ---- 주 노드 옮기기(switchover, Make Leader) ----

func (s *Server) switchoverHA(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	_, hc, ok := s.loadHAFor(w, r)
	if !ok {
		return
	}
	var body struct {
		To string `json:"to"`
	}
	if !decode(w, r, &body) {
		return
	}
	target, found := hc.member(body.To)
	if !found {
		// 이름도 받는다(CLI)
		for _, m := range hc.members {
			if hc.name(m.ServiceID) == body.To {
				target, found = m, true
			}
		}
	}
	switch {
	case !found:
		writeErr(w, 400, "to는 이 클러스터의 데이터 노드여야 합니다")
		return
	case !isDataRole(target.Role):
		writeErr(w, 400, "데이터 노드만 주 노드가 될 수 있습니다")
		return
	case target.ServiceID == hc.c.PrimaryServiceID:
		writeErr(w, 409, hc.name(target.ServiceID)+"는 이미 주 노드입니다")
		return
	}
	rt := haRT(hc.c.ID)
	st := rt.state(target.ServiceID)
	if !st.OK || st.Probe.Role != "replica" {
		writeErr(w, 409, hc.name(target.ServiceID)+"가 정상 replica가 아닙니다(상태 확인 실패 또는 복제 중이 아님)")
		return
	}
	if !rt.tryBusy() {
		writeErr(w, 409, "다른 HA 작업이 진행 중입니다")
		return
	}
	hc.c.Status = store.HASwitching
	s.haNote(ctx, hc.c, fmt.Sprintf("%s를 주 노드로 옮기는 중입니다", hc.name(target.ServiceID)))
	s.Events.Emit(ctx, hc.c.ProjectID, &hc.c.EnvironmentID, &target.ServiceID, "database.ha.switchover",
		fmt.Sprintf("%s (%s): 주 노드를 %s로 옮김(switchover)", hc.name(hc.c.RootServiceID), hc.env.Name, hc.name(target.ServiceID)))
	bg := context.WithoutCancel(ctx)
	haGo(func() {
		defer rt.done()
		var err error
		switch hc.c.Kind {
		case "postgres":
			err = s.haPostgresPromote(bg, hc, target.ServiceID, true)
		case "redis":
			err = s.haRedisSwitch(bg, hc, target.ServiceID)
		case "mongo":
			err = s.haMongoSwitch(bg, hc, target.ServiceID)
		}
		if err != nil {
			s.Log.Warn("ha: switchover", "cluster", hc.c.ID, "err", err)
			hc.c.Status = store.HAActive
			s.haNote(bg, hc.c, "주 노드 옮기기 실패: "+err.Error())
		}
	})
	writeJSON(w, 202, map[string]any{"cluster": s.haView(ctx, hc)})
}

// haPostgresPromote는 Postgres 주 노드를 target으로 옮긴다. 승격은 target을 role=primary로 다시 배포해서 한다
// (standby.signal 없이 시작하면 받은 WAL을 모두 재생하고 새 timeline으로 올라온다). 재시작해도 이전 환경변수로
// 강등되지 않게 하려는 것이다.
//   - manual(switchover): 이전 주 노드를 먼저 대기 노드로 다시 배포해 깨끗하게 멈추게 하고(남은 WAL을 replica에 보낸다),
//     더는 주 노드가 아닌 것을 확인한 뒤 target을 승격한다. 이전 주 노드는 데이터를 그대로 두고 따라간다(rejoin=standby).
//   - failover: 이전 주 노드는 응답이 없으므로 기다리지 않는다. 돌아오면 데이터를 옆으로 옮겨 두고 새 주 노드에서
//     다시 복제한다(rejoin=reclone). 이전 환경으로 다시 주 노드로 떠도 controller가 다시 배포해 막는다(fencing).
func (s *Server) haPostgresPromote(ctx context.Context, hc *haCluster, target string, manual bool) error {
	comp, err := s.haDefinition(ctx, nil, hc.c.CompanionTemplate)
	if err != nil {
		return err
	}
	old := hc.c.PrimaryServiceID
	rejoin := ha.RejoinReclone
	if manual {
		rejoin = ha.RejoinStandby
	}
	if err := s.haRewire(ctx, hc, comp, target, old, rejoin); err != nil {
		return err
	}
	if manual {
		if err := s.haEnqueue(ctx, hc, old, "ha"); err != nil {
			return fmt.Errorf("이전 주 노드 %s를 멈추지 못했습니다: %w", hc.name(old), err)
		}
		deadline := time.Now().Add(haSwitchTimeout)
		for {
			p, err := s.haProbe(ctx, "postgres", hc.insts[old])
			if err != nil || p.Role != "primary" {
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("이전 주 노드 %s가 %s 안에 멈추지 않았습니다", hc.name(old), haSwitchTimeout)
			}
			haSleep(ctx, 2*time.Second)
		}
	}
	if err := s.haEnqueue(ctx, hc, target, "ha.promote"); err != nil {
		return fmt.Errorf("%s를 승격하지 못했습니다: %w", hc.name(target), err)
	}
	hc.c.PrimaryServiceID = target
	hc.c.Status = store.HAActive
	how := "failover"
	if manual {
		how = "switchover"
	}
	s.haNote(ctx, hc.c, fmt.Sprintf("%s를 주 노드로 승격했습니다(%s)", hc.name(target), how))
	for _, m := range hc.members {
		if m.ServiceID == target || (manual && m.ServiceID == old) {
			continue
		}
		s.haEnqueue(ctx, hc, m.ServiceID, "ha")
	}
	return nil
}

// haRedisSwitch는 Sentinel에 target으로 failover하게 한다: target의 replica-priority를 가장 낮게(1) 하고
// 나머지는 100으로 둔 뒤 SENTINEL FAILOVER를 보낸다. target이 주 노드가 되면 우선순위를 기본값으로 돌린다.
func (s *Server) haRedisSwitch(ctx context.Context, hc *haCluster, target string) error {
	for _, m := range hc.data() {
		prio := 100
		if m.ServiceID == target {
			prio = 1
		}
		if _, err := s.haRun(ctx, hc.insts[m.ServiceID], func(vs map[string]string) ha.Command { return ha.RedisPriorityCommand(vs, prio) }, haProbeTimeout); err != nil && m.ServiceID == target {
			return err
		}
	}
	defer func() {
		for _, m := range hc.data() {
			s.haRun(ctx, hc.insts[m.ServiceID], func(vs map[string]string) ha.Command { return ha.RedisPriorityCommand(vs, 100) }, haProbeTimeout)
		}
	}()
	out, err := s.haRun(ctx, hc.insts[target], ha.SentinelFailoverCommand, haProbeTimeout)
	if err != nil {
		return err
	}
	if !ha.RedisOK(out) {
		return fmt.Errorf("SENTINEL FAILOVER: %s", strings.TrimSpace(out))
	}
	return s.haWaitPrimary(ctx, hc, "redis", target)
}

// haMongoSwitch는 주 노드에서 target의 priority를 가장 높게 바꾼다. target이 따라잡으면 MongoDB가 target을 뽑는다.
func (s *Server) haMongoSwitch(ctx context.Context, hc *haCluster, target string) error {
	host := ha.MongoAddr(deploy.PrivateDomain(hc.name(target), hc.env.Name))
	if _, err := s.haRun(ctx, hc.insts[hc.c.PrimaryServiceID], func(vs map[string]string) ha.Command { return ha.MongoPreferCommand(vs, host) }, haProbeTimeout); err != nil {
		return err
	}
	return s.haWaitPrimary(ctx, hc, "mongo", target)
}

// haWaitPrimary는 target이 주 노드라고 답할 때까지 기다리고 기록한다.
func (s *Server) haWaitPrimary(ctx context.Context, hc *haCluster, kind, target string) error {
	deadline := time.Now().Add(haSwitchTimeout)
	for {
		if p, err := s.haProbe(ctx, kind, hc.insts[target]); err == nil && p.Role == "primary" {
			hc.c.PrimaryServiceID, hc.c.Status = target, store.HAActive
			s.haNote(ctx, hc.c, hc.name(target)+"가 주 노드가 됐습니다(switchover)")
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%s가 %s 안에 주 노드가 되지 않았습니다", hc.name(target), haSwitchTimeout)
		}
		haSleep(ctx, 2*time.Second)
	}
}

// ---- 구성원 exec ----

// haRun은 구성원의 실행 중인 컨테이너에서 명령을 실행하고 stdout을 돌려준다. 명령은 그 컨테이너의 최종 환경변수로 만든다.
func (s *Server) haRun(ctx context.Context, in *store.Instance, build func(map[string]string) ha.Command, timeout time.Duration) (string, error) {
	cur, err := s.Store.GetInstance(ctx, in.ID)
	if err != nil {
		return "", err
	}
	d, container, _, msg := s.execTarget(ctx, cur, "")
	if d == nil {
		return "", errors.New(msg)
	}
	if d.Status != store.DepLive {
		return "", fmt.Errorf("배포 상태가 %s입니다", d.Status)
	}
	cmd := build(d.Env)
	out, err := s.dataBackend().ExecRun(ctx, d.NodeID, container, node.ExecRunRequest{Cmd: cmd.Cmd, Env: cmd.Env, Stdin: cmd.Stdin, MaxOutput: 64 << 10}, timeout)
	if err != nil {
		return "", err
	}
	if out.ExitCode != 0 {
		m := trimMsg(out.Stderr)
		if m == "" {
			m = trimMsg(out.Stdout)
		}
		if m == "" {
			m = fmt.Sprintf("exit code %d", out.ExitCode)
		}
		return "", errors.New(m)
	}
	return string(out.Stdout), nil
}

func (s *Server) haProbe(ctx context.Context, kind string, in *store.Instance) (ha.Probe, error) {
	out, err := s.haRun(ctx, in, func(vs map[string]string) ha.Command {
		c, _ := ha.ProbeCommand(kind, vs)
		return c
	}, haProbeTimeout)
	if err != nil {
		return ha.Probe{}, err
	}
	return ha.ParseProbe(kind, out)
}

// ---- controller ----

// RunHA는 HA 클러스터를 10초마다 확인한다(구성원 역할 기록, Postgres 자동 failover, 분리된 주 노드 막기,
// MongoDB replica set 초기화와 구성원 맞추기).
func (s *Server) RunHA(ctx context.Context) {
	t := time.NewTicker(haProbeEvery)
	defer t.Stop()
	for {
		s.HAOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// HAOnce는 모든 클러스터를 한 번 확인한다.
func (s *Server) HAOnce(ctx context.Context) {
	cls, err := s.Store.ListHAClusters(ctx, "")
	if err != nil {
		return
	}
	var wg sync.WaitGroup
	for _, cl := range cls {
		wg.Add(1)
		go func(cl *store.HACluster) {
			defer wg.Done()
			s.haCheck(ctx, cl)
		}(cl)
	}
	wg.Wait()
}

func (s *Server) haCheck(ctx context.Context, cl *store.HACluster) {
	hc, err := s.loadHACluster(ctx, cl)
	if err != nil {
		return
	}
	rt := haRT(cl.ID)
	data := hc.data()
	states := make([]ha.MemberState, len(data))
	var wg sync.WaitGroup
	for i, m := range data {
		wg.Add(1)
		go func(i int, m store.HAMember) {
			defer wg.Done()
			p, err := s.haProbe(ctx, cl.Kind, hc.insts[m.ServiceID])
			st := rt.record(m.ServiceID, p, err)
			states[i] = ha.MemberState{ServiceID: m.ServiceID, Name: hc.name(m.ServiceID), OK: st.OK, Probe: st.Probe, Fails: st.Fails}
		}(i, m)
	}
	wg.Wait()
	if rt.isBusy() {
		return
	}
	switch cl.Kind {
	case "postgres":
		s.haCheckPostgres(ctx, hc, rt, states)
	case "redis", "mongo":
		if cl.Kind == "mongo" {
			s.haReconcileMongo(ctx, hc, states)
		}
		if id, ok := ha.ObservedPrimary(states, cl.PrimaryServiceID); ok && id != cl.PrimaryServiceID {
			old := cl.PrimaryServiceID
			cl.PrimaryServiceID = id
			by := "Sentinel"
			if cl.Kind == "mongo" {
				by = "MongoDB"
			}
			s.haNote(ctx, cl, fmt.Sprintf("%s가 %s를 주 노드로 뽑았습니다(이전: %s)", by, hc.name(id), hc.name(old)))
			s.Events.Emit(ctx, cl.ProjectID, &cl.EnvironmentID, &id, "database.ha.failover",
				fmt.Sprintf("%s (%s): %s가 주 노드가 됨 (%s)", hc.name(cl.RootServiceID), hc.env.Name, hc.name(id), by))
		}
	}
	if cl.Status == store.HAProvisioning && haAllReady(cl.Kind, states, cl.PrimaryServiceID) {
		cl.Status = store.HAActive
		s.haNote(ctx, cl, "모든 구성원이 정상입니다")
	}
}

// haAllReady는 모든 데이터 노드가 응답하고 주 노드 하나와 복제 중인 replica로 이뤄졌는지 알려준다.
func haAllReady(kind string, states []ha.MemberState, primary string) bool {
	for _, st := range states {
		if !st.OK {
			return false
		}
		want := "replica"
		if st.ServiceID == primary {
			want = "primary"
		}
		if st.Probe.Role != want || (want == "replica" && !st.Probe.Streaming) {
			return false
		}
	}
	return len(states) > 0
}

func (s *Server) haCheckPostgres(ctx context.Context, hc *haCluster, rt *haRuntime, states []ha.MemberState) {
	cl := hc.c
	if cl.Status == store.HASwitching { // 진행하던 작업이 멈췄다(서버 재시작 등): 기록한 주 노드를 믿는다
		cl.Status = store.HAActive
		s.haNote(ctx, cl, "중단된 주 노드 전환을 정리했습니다")
	}
	var primary *ha.MemberState
	for i := range states {
		if states[i].ServiceID == cl.PrimaryServiceID {
			primary = &states[i]
		}
	}
	if primary == nil {
		return
	}
	// 분리된 주 노드 막기: 기록한 주 노드가 살아 있는데 다른 노드도 주 노드라고 답하면 그 노드를 다시 배포한다
	// (배선 변수가 replica로 바뀌어 있으므로 데이터를 옆으로 옮기고 새 주 노드에서 다시 복제한다).
	if primary.OK && primary.Probe.Role == "primary" {
		for _, st := range states {
			if st.ServiceID == cl.PrimaryServiceID || !st.OK || st.Probe.Role != "primary" {
				continue
			}
			in := hc.insts[st.ServiceID]
			ms := rt.state(st.ServiceID)
			if in.Status == store.InstDeploying || time.Since(ms.FencedAt) < haFenceCooldown {
				continue
			}
			rt.mu.Lock()
			if m := rt.members[st.ServiceID]; m != nil {
				m.FencedAt = time.Now()
			}
			rt.mu.Unlock()
			s.Log.Warn("ha: fencing stale primary", "cluster", cl.ID, "service", hc.name(st.ServiceID))
			s.haNote(ctx, cl, fmt.Sprintf("%s가 주 노드로 떠 있어서 replica로 다시 배포합니다", hc.name(st.ServiceID)))
			s.haEnqueue(ctx, hc, st.ServiceID, "ha.fence")
		}
		return
	}
	if cl.Status != store.HAActive || !cl.AutoFailover || primary.Fails < haFailAfter {
		return
	}
	if in := hc.insts[cl.PrimaryServiceID]; in.Status == store.InstDeploying {
		return // 배포 중(사용자 재배포 등)에는 응답이 없어도 기다린다
	}
	target, ok := ha.PickPromotion(states, cl.PrimaryServiceID)
	if !ok {
		s.haNoteOnce(ctx, cl, "주 노드가 응답하지 않지만 승격할 수 있는 정상 replica가 없습니다")
		return
	}
	if !rt.tryBusy() {
		return
	}
	defer rt.done()
	oldName := hc.name(cl.PrimaryServiceID)
	cl.Status = store.HASwitching
	s.haNote(ctx, cl, fmt.Sprintf("주 노드 %s가 %d초 동안 응답하지 않아 %s를 승격합니다", oldName, primary.Fails*int(haProbeEvery/time.Second), hc.name(target)))
	if err := s.haPostgresPromote(ctx, hc, target, false); err != nil {
		cl.Status = store.HAActive
		s.haNote(ctx, cl, "자동 failover 실패: "+err.Error())
		return
	}
	s.Events.Emit(ctx, cl.ProjectID, &cl.EnvironmentID, &target, "database.ha.failover",
		fmt.Sprintf("%s (%s): 주 노드 %s가 응답하지 않아 %s를 승격 (자동 failover)", hc.name(cl.RootServiceID), hc.env.Name, oldName, hc.name(target)))
}

func (s *Server) haNoteOnce(ctx context.Context, c *store.HACluster, msg string) {
	if c.Message != msg {
		s.haNote(ctx, c, msg)
	}
}

// haReconcileMongo는 replica set을 데이터 노드 목록에 맞춘다(초기화, 구성원 하나씩 추가/제거).
func (s *Server) haReconcileMongo(ctx context.Context, hc *haCluster, states []ha.MemberState) {
	var hosts []string
	for _, m := range hc.data() {
		hosts = append(hosts, ha.MongoAddr(deploy.PrivateDomain(hc.name(m.ServiceID), hc.env.Name)))
	}
	need := hc.c.Status == store.HAProvisioning
	for _, st := range states {
		if st.ServiceID == hc.c.PrimaryServiceID && st.OK && (st.Probe.Role == "uninitialized" || st.Probe.Members != len(hosts)) {
			need = true
		}
	}
	if !need {
		return
	}
	run := hc.insts[hc.c.PrimaryServiceID]
	set := hc.insts[hc.c.RootServiceID].Variables["KEEL_HA_REPLSET"]
	if set == "" {
		set = "rs0"
	}
	out, err := s.haRun(ctx, run, func(vs map[string]string) ha.Command { return ha.MongoReconcileCommand(vs, set, hosts) }, haProbeTimeout)
	if err != nil {
		s.haNoteOnce(ctx, hc.c, "replica set을 맞추지 못했습니다: "+err.Error())
		return
	}
	if action, err := ha.MongoAction(out); err == nil && action != "ok" && action != "wait" {
		s.Log.Info("ha: mongo replica set", "cluster", hc.c.ID, "action", action)
	}
}
