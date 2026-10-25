package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// ---- environment sync ----
//
// GET  /api/environments/{eid}/sync-preview?from=<source eid>
// POST /api/environments/{eid}/sync {from, services?, include?, deploy?}
//
// 대상 환경(eid)을 원본(from)과 같게 만드는 변경을 계산·적용한다. 일반 편집처럼 저장만 하므로(pending_changes에 보인다)
// deploy=true가 아니면 다음 배포부터 반영된다. 도메인, TCP proxy, 노드 배치, deploy key는 환경마다 다르므로 건드리지 않고,
// DB 서비스의 생성된 비밀번호 변수(catalog.CredentialKeys)는 옮기지도 지우지도 않는다(copy_from/forkEnvironment와 같은 규칙).

// syncFields는 동기화가 옮기는 설정 키다(store.Spec의 JSON 이름).
var syncFields = []string{
	"repo_url", "branch", "root_dir", "image", "builder", "dockerfile_path", "watch_paths", "auto_deploy",
	"start_command", "pre_deploy_command", "port", "healthcheck_type", "healthcheck_path", "healthcheck_timeout_s",
	"restart_policy", "restart_max_retries", "replicas", "cron_schedule", "sleep_when_idle", "sleep_after_s",
	"memory_limit_mb", "cpu_limit", "build_secrets", "skip_builds", "overlap_seconds", "draining_seconds", "wait_for_ci",
}

// MaskedValue는 미리보기에서 참조가 없는 변수 값 대신 보여 주는 표시다.
const MaskedValue = "•••"

type fieldChange struct {
	Field string          `json:"field"`
	From  json.RawMessage `json:"from"` // 대상 환경의 현재 값
	To    json.RawMessage `json:"to"`   // 원본 환경의 값(적용 후 값)
}

type varChange struct {
	Key    string  `json:"key"`
	Action string  `json:"action"` // added | changed | removed
	From   *string `json:"from"`
	To     *string `json:"to"`
}

type volumeChange struct {
	MountPath   string `json:"mount_path"`
	SizeLimitMB *int   `json:"size_limit_mb"`
}

type serviceSync struct {
	ServiceID   string `json:"service_id"`
	ServiceName string `json:"service_name"`
	// Action은 create(대상 인스턴스가 아직 한 번도 설정·배포되지 않음) | update다.
	Action    string         `json:"action"`
	Settings  []fieldChange  `json:"settings"`
	Variables []varChange    `json:"variables"`
	Volumes   []volumeChange `json:"volumes"`
	// ProtectedVariables는 DB 비밀번호라서 옮기지 않는 변수 이름이다.
	ProtectedVariables []string `json:"protected_variables"`
	// SealedVariables는 봉인되어 동기화하지 않는 변수 이름이다(원본 또는 대상에서 봉인).
	SealedVariables []string `json:"sealed_variables"`

	svc     *store.Service
	target  *store.Instance
	newSpec store.Spec
	newVars map[string]string
}

type envRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type syncPlan struct {
	From            envRef         `json:"from"`
	To              envRef         `json:"to"`
	Services        []*serviceSync `json:"services"`
	SharedVariables []varChange    `json:"shared_variables"`
	// SealedSharedVariables는 봉인되어 동기화하지 않는 공유 변수 이름이다.
	SealedSharedVariables []string `json:"sealed_shared_variables"`

	project   *store.Project
	src, dst  *store.Environment
	newShared map[string]string
}

// maskVar는 값에 ${{ }} 참조가 있으면 그대로, 아니면 MaskedValue를 돌려준다.
func maskVar(v string, ok bool) *string {
	if !ok {
		return nil
	}
	if len(vars.References(v)) > 0 {
		return &v
	}
	m := MaskedValue
	return &m
}

// DiffVars는 dst를 src와 같게 만드는 변수 변경과 적용 후 값을 돌려준다. protected 키는 dst 값을 그대로 둔다.
func DiffVars(src, dst map[string]string, protected map[string]bool) ([]varChange, map[string]string) {
	out := []varChange{}
	next := copyMap(dst)
	for k, v := range src {
		if protected[k] {
			continue
		}
		old, ok := dst[k]
		switch {
		case !ok:
			out = append(out, varChange{Key: k, Action: "added", To: maskVar(v, true)})
		case old != v:
			out = append(out, varChange{Key: k, Action: "changed", From: maskVar(old, true), To: maskVar(v, true)})
		default:
			continue
		}
		next[k] = v
	}
	for k, old := range dst {
		if _, ok := src[k]; ok || protected[k] {
			continue
		}
		out = append(out, varChange{Key: k, Action: "removed", From: maskVar(old, true)})
		delete(next, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, next
}

// DiffSpec은 dst 설정을 src와 같게 만드는 syncFields 변경과 적용 후 설정을 돌려준다(나머지 필드는 dst 그대로).
func DiffSpec(src, dst store.Spec) ([]fieldChange, store.Spec, error) {
	var sm, dm map[string]json.RawMessage
	sb, _ := json.Marshal(src)
	db, _ := json.Marshal(dst)
	json.Unmarshal(sb, &sm)
	json.Unmarshal(db, &dm)
	out := []fieldChange{}
	null := json.RawMessage("null")
	for _, f := range syncFields {
		a, okA := sm[f]
		b, okB := dm[f]
		if !okA {
			a = null
		}
		if !okB {
			b = null
		}
		if string(a) == string(b) {
			continue
		}
		out = append(out, fieldChange{Field: f, From: b, To: a})
		if okA {
			dm[f] = a
		} else {
			delete(dm, f)
		}
	}
	nb, _ := json.Marshal(dm)
	var next store.Spec
	if err := json.Unmarshal(nb, &next); err != nil {
		return nil, dst, err
	}
	next.Domains, next.TCPProxy = dst.Domains, dst.TCPProxy
	return out, next, nil
}

// pristine은 인스턴스가 한 번도 설정·배포되지 않은 기본 상태인지 알려준다(소스도 변수도 배포도 없음).
func pristine(in *store.Instance) bool {
	return in.CurrentDeploymentID == nil && (in.RepoURL == nil || *in.RepoURL == "") && (in.Image == nil || *in.Image == "") && len(in.Variables) == 0
}

func protectedKeys(svc *store.Service) map[string]bool {
	if svc.DatabaseKind == nil {
		return nil
	}
	return catalog.CredentialKeys(*svc.DatabaseKind)
}

// syncSource는 from 환경을 검사한다. 400이면 메시지를 돌려준다.
func (s *Server) syncSource(ctx context.Context, dst *store.Environment, from string) (*store.Environment, string) {
	if from == "" {
		return nil, "from is required"
	}
	if from == dst.ID {
		return nil, "an environment cannot be synced from itself"
	}
	src, err := s.Store.GetEnvironment(ctx, from)
	if err != nil || src.ProjectID != dst.ProjectID {
		return nil, "from must be another environment of the same project"
	}
	return src, ""
}

// planSync는 dst를 src와 같게 만드는 변경을 계산한다(아무것도 쓰지 않는다).
func (s *Server) planSync(ctx context.Context, p *store.Project, src, dst *store.Environment) (*syncPlan, error) {
	plan := &syncPlan{From: envRef{src.ID, src.Name}, To: envRef{dst.ID, dst.Name}, Services: []*serviceSync{},
		project: p, src: src, dst: dst}
	svcs, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for _, svc := range svcs {
		a, err := s.Store.GetInstanceFor(ctx, svc.ID, src.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		b, err := s.Store.GetInstanceFor(ctx, svc.ID, dst.ID)
		if errors.Is(err, store.ErrNotFound) {
			continue
		} else if err != nil {
			return nil, err
		}
		prot := protectedKeys(svc)
		ss := &serviceSync{ServiceID: svc.ID, ServiceName: svc.Name, Action: "update", ProtectedVariables: []string{},
			svc: svc, target: b}
		if ss.Settings, ss.newSpec, err = DiffSpec(a.Spec, b.Spec); err != nil {
			return nil, err
		}
		// 봉인된 변수(원본·대상 어느 쪽이든)는 옮기지도 지우지도 않고 값도 보여 주지 않는다.
		sealed := withKeys(s.instanceSealed(ctx, a), s.instanceSealed(ctx, b))
		ss.SealedVariables = store.SortedKeys(sealed)
		ss.Variables, ss.newVars = DiffVars(a.Variables, b.Variables, withKeys(prot, sealed))
		for k := range prot {
			if _, ok := a.Variables[k]; ok {
				ss.ProtectedVariables = append(ss.ProtectedVariables, k)
			}
		}
		sort.Strings(ss.ProtectedVariables)
		ss.Volumes = []volumeChange{}
		srcVols, err := s.Store.ListVolumesByInstance(ctx, a.ID)
		if err != nil {
			return nil, err
		}
		dstVols, err := s.Store.ListVolumesByInstance(ctx, b.ID)
		if err != nil {
			return nil, err
		}
		have := map[string]bool{}
		for _, v := range dstVols {
			have[v.MountPath] = true
		}
		for _, v := range srcVols {
			if !have[v.MountPath] {
				ss.Volumes = append(ss.Volumes, volumeChange{MountPath: v.MountPath, SizeLimitMB: v.SizeLimitMB})
			}
		}
		if len(ss.Settings)+len(ss.Variables)+len(ss.Volumes) == 0 {
			continue
		}
		if pristine(b) && !pristine(a) {
			ss.Action = "create"
		}
		plan.Services = append(plan.Services, ss)
	}
	srcShared, err := s.Store.GetSharedVars(ctx, src.ID)
	if err != nil {
		return nil, err
	}
	dstShared, err := s.Store.GetSharedVars(ctx, dst.ID)
	if err != nil {
		return nil, err
	}
	sharedSealed := withKeys(s.sharedSealed(ctx, src.ID, srcShared), s.sharedSealed(ctx, dst.ID, dstShared))
	plan.SealedSharedVariables = store.SortedKeys(sharedSealed)
	plan.SharedVariables, plan.newShared = DiffVars(srcShared, dstShared, sharedSealed)
	return plan, nil
}

func (s *Server) syncPreview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dst, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	src, msg := s.syncSource(ctx, dst, r.URL.Query().Get("from"))
	if src == nil {
		writeErr(w, 400, msg)
		return
	}
	if ok, err := s.canUseEnvironment(r, src); err != nil || !ok { // 제한된 환경·토큰 범위 밖 환경에서는 가져올 수 없다
		s.envAccessErr(w, src, err)
		return
	}
	plan, err := s.planSync(ctx, p, src, dst)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, plan)
}

type syncApplied struct {
	ServiceID    string  `json:"service_id"`
	ServiceName  string  `json:"service_name"`
	Settings     int     `json:"settings"`
	Variables    int     `json:"variables"`
	Volumes      int     `json:"volumes"`
	DeploymentID *string `json:"deployment_id"`
	// DeploySkipped는 deploy=true였지만 배포하지 못한 이유다(소스 없음 등).
	DeploySkipped *string `json:"deploy_skipped,omitempty"`
}

func (s *Server) syncEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	dst, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	var body struct {
		From     string    `json:"from"`
		Services *[]string `json:"services"`
		Include  *struct {
			Settings        bool `json:"settings"`
			Variables       bool `json:"variables"`
			SharedVariables bool `json:"shared_variables"`
			Volumes         bool `json:"volumes"`
		} `json:"include"`
		Deploy bool `json:"deploy"`
	}
	if !decode(w, r, &body) {
		return
	}
	src, msg := s.syncSource(ctx, dst, body.From)
	if src == nil {
		writeErr(w, 400, msg)
		return
	}
	if ok, err := s.canUseEnvironment(r, src); err != nil || !ok { // 제한된 환경·토큰 범위 밖 환경에서는 가져올 수 없다
		s.envAccessErr(w, src, err)
		return
	}
	incSettings, incVars, incShared, incVols := true, true, true, true
	if body.Include != nil {
		incSettings, incVars, incShared, incVols = body.Include.Settings, body.Include.Variables, body.Include.SharedVariables, body.Include.Volumes
	}
	plan, err := s.planSync(ctx, p, src, dst)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var only map[string]bool
	if body.Services != nil {
		svcs, err := s.Store.ListServices(ctx, p.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		known := map[string]bool{}
		for _, sv := range svcs {
			known[sv.ID] = true
		}
		only = map[string]bool{}
		for _, id := range *body.Services {
			if !known[id] {
				writeErr(w, 400, "services must be services of this project: "+id)
				return
			}
			only[id] = true
		}
	}

	// 1단계: 모두 검증한다(하나라도 틀리면 아무것도 쓰지 않는다).
	type work struct {
		ss          *serviceSync
		in          *store.Instance
		nSet, nVars int
		vols        []volumeChange
	}
	todo := []*work{}
	for _, ss := range plan.Services {
		if only != nil && !only[ss.ServiceID] {
			continue
		}
		in := *ss.target
		wk := &work{ss: ss, in: &in}
		if incSettings && len(ss.Settings) > 0 {
			sp := ss.newSpec
			if err := s.validateSpec(ss.svc, &sp); err != nil {
				writeErr(w, 400, fmt.Sprintf("%s: %s", ss.ServiceName, err.Error()))
				return
			}
			sp.Domains, sp.TCPProxy = ss.target.Domains, ss.target.TCPProxy
			in.Spec, wk.nSet = sp, len(ss.Settings)
			// 배포 출처 guardrail: 소스가 바뀌는 서비스는 검사한다.
			if !sameStr(in.RepoURL, ss.target.RepoURL) || !sameStr(in.Image, ss.target.Image) {
				if err := s.checkSourcePolicy(ctx, guardrails.SourceOf(ss.svc, &in.Spec)); err != nil {
					s.policyErr(w, err)
					return
				}
			}
		}
		if incVars && len(ss.Variables) > 0 {
			if err := validateVars(ss.newVars); err != nil {
				writeErr(w, 400, fmt.Sprintf("%s: %s", ss.ServiceName, err.Error()))
				return
			}
			in.Variables, wk.nVars = ss.newVars, len(ss.Variables)
		}
		if incVols {
			wk.vols = ss.Volumes
		}
		if wk.nSet+wk.nVars+len(wk.vols) == 0 {
			continue
		}
		if wk.nVars > 0 {
			if _, err := s.Deploy.Resolve(ctx, wk.in, &deploy.Override{InstanceID: in.ID, Vars: in.Variables, Spec: &in.Spec}); err != nil {
				var ce *vars.CycleError
				if errors.As(err, &ce) {
					writeErr(w, 400, fmt.Sprintf("%s: %s", ss.ServiceName, err.Error()))
					return
				}
			}
		}
		todo = append(todo, wk)
	}
	sharedChanged := incShared && len(plan.SharedVariables) > 0
	if sharedChanged {
		if err := validateVars(plan.newShared); err != nil {
			writeErr(w, 400, "shared variables: "+err.Error())
			return
		}
	}

	// 2단계: 쓴다.
	applied := []syncApplied{}
	if sharedChanged {
		if err := s.Store.SetSharedVars(ctx, dst.ID, plan.newShared); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	for _, wk := range todo {
		if wk.nSet+wk.nVars > 0 {
			if err := s.Store.UpdateInstanceConfig(ctx, wk.in); err != nil {
				s.storeErr(w, err)
				return
			}
		}
		nVols := 0
		for _, vc := range wk.vols {
			if err := s.createSyncedVolume(ctx, p, dst, wk.ss.svc, wk.in, vc); err != nil {
				s.storeErr(w, err)
				return
			}
			nVols++
		}
		applied = append(applied, syncApplied{ServiceID: wk.ss.ServiceID, ServiceName: wk.ss.ServiceName,
			Settings: wk.nSet, Variables: wk.nVars, Volumes: nVols})
	}
	if body.Deploy {
		for i, wk := range todo {
			fresh, err := s.Store.GetInstance(ctx, wk.in.ID)
			if err != nil {
				s.storeErr(w, err)
				return
			}
			if err := sourceErr(wk.ss.svc, fresh); err != nil {
				m := err.Error()
				applied[i].DeploySkipped = &m
				continue
			}
			d, err := s.Deploy.Enqueue(ctx, fresh, deploy.EnqueueOpts{Trigger: s.deployTrigger(ctx, fresh)})
			if err != nil {
				m := err.Error()
				applied[i].DeploySkipped = &m
				continue
			}
			applied[i].DeploymentID = &d.ID
		}
	}
	if len(applied) > 0 || sharedChanged {
		msg := fmt.Sprintf("환경 %s를 %s 환경에서 동기화했습니다 (서비스 %d개", dst.Name, src.Name, len(applied))
		if sharedChanged {
			msg += fmt.Sprintf(", 공유 변수 %d개", len(plan.SharedVariables))
		}
		msg += ")"
		if body.Deploy {
			msg += " 및 배포 요청"
		}
		s.Events.Emit(ctx, p.ID, &dst.ID, nil, "environment.synced", msg)
	}
	shared := 0
	if sharedChanged {
		shared = len(plan.SharedVariables)
	}
	writeJSON(w, 200, map[string]any{"from": plan.From, "to": plan.To, "services": applied, "shared_variables": shared})
}

// createSyncedVolume은 원본에만 있는 마운트 경로에 빈 볼륨을 만든다(데이터는 복사하지 않는다). 이름이 겹치면 숫자를 붙인다.
func (s *Server) createSyncedVolume(ctx context.Context, p *store.Project, env *store.Environment, svc *store.Service, in *store.Instance, vc volumeChange) error {
	base := volumeName(svc.Name, vc.MountPath)
	var err error
	for i := 1; i <= 20; i++ {
		name := base
		if i > 1 {
			suffix := fmt.Sprintf("-%d", i)
			name = strings.TrimRight(base[:min(len(base), 40-len(suffix))], "-") + suffix
		}
		v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, InstanceID: &in.ID, Name: name, MountPath: vc.MountPath, SizeLimitMB: vc.SizeLimitMB}
		if err = s.Store.CreateVolume(ctx, v); !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return err
}
