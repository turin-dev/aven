package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/iac"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// ---- project-level Infrastructure as Code (aven config) ----
//
// GET  /api/projects/{pid}/environments/{eid}/config[?include_variables=1]   현재 상태를 정의로 내보낸다
// POST /api/projects/{pid}/environments/{eid}/config/plan   {definition, show_values?}
// POST /api/projects/{pid}/environments/{eid}/config/apply  {definition, plan_hash, confirm_destructive?, deploy?}
// GET  /api/projects/{pid}/environments/{eid}/config/partials
// POST /api/projects/{pid}/environments/{eid}/config/partials/release  {partial, resources?, dry_run?, base_config_etag?}
// POST /api/projects/{pid}/environments/{eid}/config/partials/transfer {partial, to, resources?, dry_run?, base_config_etag?}
//
// apply는 일반 편집과 같은 staged 모델을 따른다: 설정·변수는 저장만 하고(pending_changes에 보인다) deploy=true일 때만
// 바뀐 서비스를 배포한다. 새 서비스는 대시보드에서 만든 것과 같이(createServiceFromDef) 모든 환경에 인스턴스를 만든다.
// 서비스 삭제는 프로젝트 전체(모든 환경)에서 일어나므로 destructive로 표시하고 confirm_destructive가 있어야 한다.
// 서비스별 config-as-code(aven.json/railway.toml)는 배포 때 저장소에서 읽혀 계속 이 설정 위에 덮어쓴다.

// iacMu는 apply와 소유권 변경을 직렬화한다(plan_hash/config_etag 비교와 쓰기 사이에 다른 apply가 끼지 않게).
var iacMu sync.Mutex

// iacDeleteService/iacSyncProxy는 실제 컨테이너·프록시를 건드리는 부분이다(테스트가 바꾼다).
var (
	iacDeleteService = func(s *Server, ctx context.Context, svc *store.Service) error {
		return s.Deploy.DeleteService(ctx, svc)
	}
	iacSyncProxy = func(s *Server, ctx context.Context) error { return s.Deploy.SyncProxy(ctx) }
)

type iacChange struct {
	Action      string          `json:"action"` // create | change | destroy
	Resource    string          `json:"resource"`
	Field       string          `json:"field,omitempty"`
	From        json.RawMessage `json:"from,omitempty"`
	To          json.RawMessage `json:"to,omitempty"`
	Destructive bool            `json:"destructive,omitempty"`
	Detail      string          `json:"detail,omitempty"`
}

type iacPlan struct {
	EnvironmentID string      `json:"environment_id"`
	Environment   string      `json:"environment"`
	Partial       string      `json:"partial,omitempty"`
	ConfigETag    string      `json:"config_etag"`
	PlanHash      string      `json:"plan_hash"`
	Changes       []iacChange `json:"changes"`
	// Claims는 이 apply로 partial이 새로 소유하게 될 주소다(변경이 없어도 apply가 기록한다).
	Claims      []string `json:"claims"`
	Errors      []string `json:"errors"`
	Warnings    []string `json:"warnings"`
	Destructive bool     `json:"destructive"`
	Summary     struct {
		Create  int `json:"create"`
		Change  int `json:"change"`
		Destroy int `json:"destroy"`
	} `json:"summary"`
}

// iacItem은 plan이 계산한 서비스 하나의 적용 작업이다.
type iacItem struct {
	decl    iac.Service
	svc     *store.Service  // 만들 서비스면 nil
	in      *store.Instance // 대상 환경의 현재 인스턴스(만들 서비스면 가상의 인스턴스)
	def     catalog.ServiceDef
	repo    *string
	branch  string
	spec    store.Spec
	vars    map[string]string
	nodeID  string
	create  bool
	destroy bool
	changed bool
	// proxy/mesh: 적용 후 프록시·mesh 동기화가 필요한 변경
	proxy, mesh, domains bool
}

func (s *Server) iacEnv(w http.ResponseWriter, r *http.Request) (*store.Project, *store.Environment, bool) {
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return nil, nil, false
	}
	if p.ID != r.PathValue("pid") {
		writeErr(w, 404, "not found")
		return nil, nil, false
	}
	return p, env, true
}

func (s *Server) exportIACConfig(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.iacEnv(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("include_variables")
	include := q == "1" || q == "true"
	if include && !canSeeSecrets(r) {
		writeErr(w, 403, "variable values require member access")
		return
	}
	d, err := s.exportIACDefinition(r.Context(), p, env, include)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, d)
}

// iacServices는 IaC가 다루는 서비스(compose 스택 소속 제외)와 대상 환경 인스턴스다.
func (s *Server) iacServices(ctx context.Context, p *store.Project, env *store.Environment) ([]*store.Service, map[string]*store.Instance, error) {
	all, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	insts, err := s.Store.ListInstancesByEnv(ctx, env.ID)
	if err != nil {
		return nil, nil, err
	}
	byService := make(map[string]*store.Instance, len(insts))
	for _, in := range insts {
		byService[in.ServiceID] = in
	}
	out := make([]*store.Service, 0, len(all))
	for _, svc := range all {
		if svc.StackID != nil || byService[svc.ID] == nil {
			continue
		}
		out = append(out, svc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, byService, nil
}

// singleRefRe는 값 전체가 참조 하나(${{ svc.KEY }})인지 본다. 그런 값은 비밀이 아니므로 그대로 내보낸다.
var singleRefRe = regexp.MustCompile(`^\$\{\{\s*[A-Za-z0-9_.-]+\s*\}\}$`)

func (s *Server) exportIACDefinition(ctx context.Context, p *store.Project, env *store.Environment, includeValues bool) (*iac.Definition, error) {
	svcs, insts, err := s.iacServices(ctx, p, env)
	if err != nil {
		return nil, err
	}
	d := &iac.Definition{Version: iac.Version, ProjectName: p.Name, Services: []iac.Service{}}
	for _, svc := range svcs {
		in := insts[svc.ID]
		entry := iac.Service{Name: svc.Name, SourceType: svc.SourceType, Settings: exportSettings(in.Spec)}
		if svc.DatabaseKind != nil {
			entry.DatabaseKind = *svc.DatabaseKind
		}
		if len(in.Variables) > 0 {
			sealed := s.instanceSealed(ctx, in)
			values := make(map[string]iac.Variable, len(in.Variables))
			for k, v := range in.Variables {
				switch {
				case sealed[k]:
					values[k] = iac.Variable{Preserve: true}
				case includeValues || singleRefRe.MatchString(v):
					values[k] = iac.Variable{Value: v}
				default:
					values[k] = iac.Variable{Preserve: true}
				}
			}
			entry.Variables = &values
		}
		d.Services = append(d.Services, entry)
	}
	return d, nil
}

// exportSettings는 IaC로 바꿀 수 있는 설정(specKeys) 중 기본값과 다른 것만 돌려준다.
// 생략한 설정은 apply가 건드리지 않으므로 기본값을 빼도 깨끗한 import는 변경 없는 plan이 된다.
// 생성된 도메인(*.base domain)과 TCP proxy는 서버가 정하는 값이라 내보내지 않는다.
func exportSettings(spec store.Spec) map[string]json.RawMessage {
	var cur, def map[string]json.RawMessage
	b, _ := json.Marshal(spec)
	json.Unmarshal(b, &cur)
	var base store.Spec
	base.Defaults()
	b, _ = json.Marshal(base)
	json.Unmarshal(b, &def)
	out := map[string]json.RawMessage{}
	for k, v := range cur {
		if !specKeys[k] || string(v) == "null" {
			continue
		}
		if k == "domains" {
			custom := []store.Domain{}
			for _, d := range spec.Domains {
				if d.Kind != "generated" {
					custom = append(custom, d)
				}
			}
			if len(custom) == 0 {
				continue
			}
			v, _ = json.Marshal(custom)
		} else if dv, ok := def[k]; ok && rawJSONEqual(v, dv) {
			continue
		}
		out[k] = v
	}
	return out
}

func rawJSONEqual(a, b json.RawMessage) bool {
	var av, bv any
	if json.Unmarshal(a, &av) != nil || json.Unmarshal(b, &bv) != nil {
		return string(a) == string(b)
	}
	return reflect.DeepEqual(av, bv)
}

func (s *Server) iacETag(ctx context.Context, p *store.Project, env *store.Environment, owners map[string]string) (string, error) {
	svcs, insts, err := s.iacServices(ctx, p, env)
	if err != nil {
		return "", err
	}
	type entry struct {
		ID, Name, Source string
		Spec             store.Spec
		Vars             map[string]string
		Node             string
	}
	state := struct {
		Services []entry
		Owners   map[string]string
	}{Owners: owners}
	for _, svc := range svcs {
		in := insts[svc.ID]
		state.Services = append(state.Services, entry{svc.ID, svc.Name, svc.SourceType, in.Spec, in.Variables, in.NodeID})
	}
	b, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:16]), nil
}

// iacOwners는 소유권 중 아직 있는 서비스를 가리키는 주소만 돌려준다(대시보드에서 지운 서비스의 기록은 무시한다).
func (s *Server) iacOwners(ctx context.Context, env *store.Environment, svcs []*store.Service) (map[string]string, error) {
	all, err := s.Store.ListIACOwnership(ctx, env.ID)
	if err != nil {
		return nil, err
	}
	live := map[string]bool{}
	for _, svc := range svcs {
		live[iac.ServiceAddress(svc.Name)] = true
	}
	out := map[string]string{}
	for addr, owner := range all {
		if live[addr] {
			out[addr] = owner
		}
	}
	return out, nil
}

type iacPlanInput struct {
	Definition iac.Definition `json:"definition"`
	ShowValues bool           `json:"show_values"`
}

func (s *Server) planIACConfig(w http.ResponseWriter, r *http.Request) {
	var input iacPlanInput
	if !decode(w, r, &input) {
		return
	}
	p, env, ok := s.iacEnv(w, r)
	if !ok {
		return
	}
	plan, _, err := s.buildIACPlan(r.Context(), p, env, &input.Definition, input.ShowValues && canSeeSecrets(r))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, plan)
}

// buildIACPlan은 정의와 현재 상태를 비교한다(아무것도 쓰지 않는다). 정의 오류는 plan.Errors에 담는다.
func (s *Server) buildIACPlan(ctx context.Context, p *store.Project, env *store.Environment, d *iac.Definition, showValues bool) (*iacPlan, []*iacItem, error) {
	plan := &iacPlan{EnvironmentID: env.ID, Environment: env.Name, Partial: d.Partial,
		Changes: []iacChange{}, Claims: []string{}, Errors: []string{}, Warnings: []string{}}
	svcs, insts, err := s.iacServices(ctx, p, env)
	if err != nil {
		return nil, nil, err
	}
	owners, err := s.iacOwners(ctx, env, svcs)
	if err != nil {
		return nil, nil, err
	}
	if plan.ConfigETag, err = s.iacETag(ctx, p, env, owners); err != nil {
		return nil, nil, err
	}
	if err := d.Validate(); err != nil {
		plan.Errors = append(plan.Errors, err.Error())
		return plan, nil, nil
	}
	canonical, err := json.Marshal(d)
	if err != nil {
		return nil, nil, err
	}
	h := sha256.Sum256(append([]byte(plan.ConfigETag+"\x00"), canonical...))
	plan.PlanHash = hex.EncodeToString(h[:])

	if d.Partial == "" && len(owners) > 0 {
		plan.Errors = append(plan.Errors, `this environment already has named IaC partials; export a partial name from this file (or release every partial with "aven config partials release") instead of managing the whole project`)
	}
	if d.ProjectName != "" && d.ProjectName != p.Name {
		plan.Warnings = append(plan.Warnings, fmt.Sprintf("project_name %q differs from the project name %q (names are not changed by apply)", d.ProjectName, p.Name))
	}
	byName := map[string]*store.Service{}
	all, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		return nil, nil, err
	}
	for _, svc := range all {
		byName[svc.Name] = svc
	}
	managed := map[string]bool{}
	for _, svc := range svcs {
		managed[svc.ID] = true
	}

	items := []*iacItem{}
	declared := map[string]bool{}
	hosts := map[string]string{}
	for _, decl := range d.Services {
		declared[decl.Name] = true
		addr := decl.Address()
		fail := func(format string, a ...any) {
			plan.Errors = append(plan.Errors, fmt.Sprintf("service %q: ", decl.Name)+fmt.Sprintf(format, a...))
		}
		if !nameRe.MatchString(decl.Name) || decl.Name == "shared" {
			fail("name must be lowercase letters, digits and '-' (max 40), and not 'shared'")
			continue
		}
		if owner := owners[addr]; owner != "" && owner != d.Partial {
			plan.Errors = append(plan.Errors, fmt.Sprintf("cannot manage service %q: already managed by partial %q", decl.Name, owner))
			continue
		}
		svc := byName[decl.Name]
		if svc != nil && !managed[svc.ID] {
			if svc.StackID != nil {
				fail("managed by a compose stack; edit the stack instead")
			} else {
				fail("has no instance in environment %q", env.Name)
			}
			continue
		}
		if svc != nil {
			if decl.SourceType != "" && decl.SourceType != svc.SourceType && !(decl.SourceType == catalog.BucketKind && svc.DatabaseKind != nil && *svc.DatabaseKind == catalog.BucketKind) {
				fail("source_type cannot be changed (%s → %s); remove and recreate the service instead", svc.SourceType, decl.SourceType)
				continue
			}
			if decl.DatabaseKind != "" && (svc.DatabaseKind == nil || decl.DatabaseKind != *svc.DatabaseKind) {
				fail("database_kind cannot be changed")
				continue
			}
		}
		it, changes, err := s.prepareIACItem(ctx, env, svc, insts[svcID(svc)], decl, showValues)
		if err != nil {
			fail("%v", err)
			continue
		}
		for _, dom := range it.spec.Domains {
			if other, ok := hosts[dom.Host]; ok && other != decl.Name {
				fail("domain %s is also declared by service %q", dom.Host, other)
			}
			hosts[dom.Host] = decl.Name
		}
		items = append(items, it)
		if it.create {
			plan.Changes = append(plan.Changes, iacChange{Action: "create", Resource: addr, Detail: createDetail(decl)})
		}
		plan.Changes = append(plan.Changes, changes...)
		if d.Partial != "" && owners[addr] != d.Partial {
			plan.Claims = append(plan.Claims, addr)
		}
	}
	for _, svc := range svcs {
		addr := iac.ServiceAddress(svc.Name)
		if declared[svc.Name] {
			continue
		}
		if d.Partial == "" || owners[addr] == d.Partial {
			plan.Changes = append(plan.Changes, iacChange{Action: "destroy", Resource: addr, Destructive: true, Detail: "deletes the service in every environment"})
			items = append(items, &iacItem{svc: svc, in: insts[svc.ID], destroy: true})
		}
	}
	resources := map[string]string{}
	for _, c := range plan.Changes {
		plan.Destructive = plan.Destructive || c.Destructive
		if resources[c.Resource] == "" || c.Action != "change" {
			resources[c.Resource] = c.Action
		}
	}
	for _, a := range resources {
		switch a {
		case "create":
			plan.Summary.Create++
		case "destroy":
			plan.Summary.Destroy++
		default:
			plan.Summary.Change++
		}
	}
	sort.Strings(plan.Claims)
	return plan, items, nil
}

func createDetail(d iac.Service) string {
	if d.DatabaseKind != "" {
		return d.DatabaseKind
	}
	return d.SourceType
}

func svcID(svc *store.Service) string {
	if svc == nil {
		return ""
	}
	return svc.ID
}

// iacServiceDef는 새 서비스의 catalog 정의다(대시보드의 서비스 추가와 같은 규칙).
func (s *Server) iacServiceDef(d iac.Service) (catalog.ServiceDef, *string, string, error) {
	str := func(key string) string {
		var v string
		if raw, ok := d.Settings[key]; ok {
			json.Unmarshal(raw, &v)
		}
		return strings.TrimSpace(v)
	}
	var def catalog.ServiceDef
	var repo *string
	branch := "main"
	switch d.SourceType {
	case "repo":
		u := str("repo_url")
		if !s.validRepoURL(u) {
			return def, nil, "", errors.New("new repo services need settings.repo_url (an https://, ssh:// or git@ URL)")
		}
		repo = &u
		if b := str("branch"); b != "" {
			branch = b
		}
		def = catalog.ServiceDef{Name: d.Name, Icon: "github", SourceType: "repo"}
	case "image":
		img := str("image")
		if img != "" && !regexpMatch(imageRe, img) {
			return def, nil, "", errors.New("invalid image (e.g. nginx:alpine)")
		}
		def = catalog.ServiceDef{Name: d.Name, Icon: "docker", SourceType: "image", Image: img}
	case "database", catalog.BucketKind:
		kind := d.DatabaseKind
		if d.SourceType == catalog.BucketKind {
			kind = catalog.BucketKind
		}
		if kind == "" {
			return def, nil, "", errors.New("new database services need database_kind (postgres, mysql, redis, mongo, bucket)")
		}
		var ok bool
		if def, ok = catalog.DatabaseService(kind); !ok {
			return def, nil, "", fmt.Errorf("unknown database_kind %q", kind)
		}
		def.Name = d.Name
	case "function":
		def = functionDef()
		def.Name = d.Name
	case "":
		return def, nil, "", errors.New("new services need source_type (repo, image, database, bucket or function)")
	default:
		return def, nil, "", fmt.Errorf("unknown source_type %q", d.SourceType)
	}
	return def, repo, branch, nil
}

// prepareIACItem은 선언 하나를 현재 인스턴스에 적용한 결과(설정·변수)와 변경 목록을 계산한다. 쓰지 않는다.
// svc가 nil이면 새 서비스이고, 대시보드에서 만들 때와 같은 기본값의 가상 인스턴스 위에 계산한다.
func (s *Server) prepareIACItem(ctx context.Context, env *store.Environment, svc *store.Service, cur *store.Instance, decl iac.Service, showValues bool) (*iacItem, []iacChange, error) {
	it := &iacItem{decl: decl, svc: svc, create: svc == nil}
	stub := svc
	if svc == nil {
		def, repo, branch, err := s.iacServiceDef(decl)
		if err != nil {
			return nil, nil, err
		}
		it.def, it.repo, it.branch = def, repo, branch
		cur = &store.Instance{EnvironmentID: env.ID, NodeID: store.LocalNodeID}
		cur.Spec.Defaults()
		if repo != nil {
			r := *repo
			cur.RepoURL, cur.Branch, cur.AutoDeploy = &r, branch, true
		}
		def.ApplyToInstance(cur)
		if def.SourceType == "repo" && def.Settings.RestartPolicy == nil {
			cur.RestartPolicy = "on_failure"
		}
		stub = &store.Service{Name: decl.Name, SourceType: def.SourceType}
	} else if cur == nil {
		return nil, nil, errors.New("instance not found")
	}
	it.in = cur
	it.spec, it.vars, it.nodeID = cur.Spec, copyMap(cur.Variables), cur.NodeID
	if it.vars == nil {
		it.vars = map[string]string{}
	}

	if len(decl.Settings) > 0 {
		next, node, err := s.iacSettings(ctx, stub, cur, decl.Settings)
		if err != nil {
			return nil, nil, err
		}
		it.spec = next
		if node != "" {
			it.nodeID = node
		}
	}
	addr := decl.Address()
	changes := []iacChange{}
	if !it.create {
		var before, after map[string]json.RawMessage
		b, _ := json.Marshal(cur.Spec)
		json.Unmarshal(b, &before)
		b, _ = json.Marshal(it.spec)
		json.Unmarshal(b, &after)
		keys := store.SortedKeys(specKeys)
		for _, k := range keys {
			if rawJSONEqual(before[k], after[k]) {
				continue
			}
			changes = append(changes, iacChange{Action: "change", Resource: addr, Field: k, From: before[k], To: after[k]})
			switch k {
			case "domains":
				it.proxy, it.domains = true, true
			case "port":
				it.proxy, it.mesh = true, true
			case "private_ports":
				it.mesh = true
			}
		}
		if store.NormNode(it.nodeID) != store.NormNode(cur.NodeID) {
			from, _ := json.Marshal(cur.NodeID)
			to, _ := json.Marshal(it.nodeID)
			changes = append(changes, iacChange{Action: "change", Resource: addr, Field: "node_id", From: from, To: to})
		}
	}

	if decl.Variables != nil {
		sealed := map[string]bool{}
		if !it.create {
			sealed = s.instanceSealed(ctx, cur)
		}
		var protected map[string]bool
		if kind := decl.DatabaseKind; it.create && kind != "" {
			protected = catalog.CredentialKeys(kind)
		} else if svc != nil {
			protected = protectedKeys(svc)
		}
		next := map[string]string{}
		for k, v := range *decl.Variables {
			if v.Preserve {
				old, ok := cur.Variables[k]
				if !ok {
					return nil, nil, fmt.Errorf("variables.%s uses preserve() but has no value in Aven", k)
				}
				next[k] = old
				continue
			}
			if sealed[k] && v.Value != cur.Variables[k] {
				return nil, nil, fmt.Errorf("variables.%s is sealed; use preserve() or remove it", k)
			}
			next[k] = v.Value
		}
		// 생성된 DB 비밀번호는 선언하지 않아도 지우지 않는다(환경 동기화·복제와 같은 규칙).
		for k := range protected {
			if _, declared := next[k]; !declared {
				if old, ok := cur.Variables[k]; ok {
					next[k] = old
				}
			}
		}
		if err := validateVars(next); err != nil {
			return nil, nil, err
		}
		if !it.create && s.Deploy != nil {
			probe := *cur
			probe.Spec, probe.Variables = it.spec, next
			if _, err := s.Deploy.Resolve(ctx, &probe, &deploy.Override{InstanceID: probe.ID, Vars: next, Spec: &probe.Spec}); err != nil {
				var cycle *vars.CycleError
				if errors.As(err, &cycle) {
					return nil, nil, err
				}
			}
		}
		if !it.create {
			keys := map[string]bool{}
			for k := range cur.Variables {
				keys[k] = true
			}
			for k := range next {
				keys[k] = true
			}
			for _, k := range store.SortedKeys(keys) {
				a, aok := cur.Variables[k]
				b, bok := next[k]
				if aok == bok && a == b {
					continue
				}
				ch := iacChange{Action: "change", Resource: addr, Field: "variables." + k, Destructive: aok && !bok}
				if aok {
					ch.From = iacVarValue(a, showValues && !sealed[k])
				}
				if bok {
					ch.To = iacVarValue(b, showValues && !sealed[k])
				}
				changes = append(changes, ch)
			}
		}
		it.vars = next
	}
	it.changed = it.create || len(changes) > 0
	return it, changes, nil
}

// iacVarValue는 plan에 보여 줄 변수 값이다. 참조가 있으면 그대로, 아니면 show가 아닐 때 «hidden».
func iacVarValue(v string, show bool) json.RawMessage {
	if !show && len(vars.References(v)) == 0 {
		v = "«hidden»"
	}
	b, _ := json.Marshal(v)
	return b
}

// iacSettings는 선언된 설정을 현재 설정 위에 덮고 PATCH /instances와 같은 검증을 한다.
// 도메인은 custom 도메인 목록을 통째로 바꾼다(생성된 도메인은 그대로 둔다). 새 주 노드가 있으면 돌려준다.
func (s *Server) iacSettings(ctx context.Context, svc *store.Service, cur *store.Instance, settings map[string]json.RawMessage) (store.Spec, string, error) {
	for k := range settings {
		switch {
		case k == "tcp_proxy":
			return cur.Spec, "", errors.New("tcp_proxy is managed from the service networking settings, not IaC")
		case !specKeys[k]:
			return cur.Spec, "", fmt.Errorf("unsupported setting %q", k)
		}
	}
	var merged map[string]json.RawMessage
	b, _ := json.Marshal(cur.Spec)
	json.Unmarshal(b, &merged)
	for k, v := range settings {
		if k != "domains" {
			merged[k] = v
		}
	}
	b, _ = json.Marshal(merged)
	var next store.Spec
	if err := json.Unmarshal(b, &next); err != nil {
		return cur.Spec, "", fmt.Errorf("invalid settings: %w", err)
	}
	next.TCPProxy, next.Domains = cur.TCPProxy, cur.Domains
	if raw, ok := settings["domains"]; ok {
		want, err := parseIACDomains(raw)
		if err != nil {
			return cur.Spec, "", err
		}
		old := map[string]store.Domain{}
		doms := []store.Domain{}
		for _, d := range cur.Domains {
			old[d.Host] = d
			if d.Kind == "generated" {
				doms = append(doms, d)
			}
		}
		for _, d := range want {
			d.Host = strings.ToLower(strings.TrimSpace(d.Host))
			if prev, ok := old[d.Host]; ok && prev.Kind == "generated" {
				continue // 생성된 도메인은 이미 들어 있다
			}
			if d.BasicAuth != nil {
				if d.BasicAuth.Password != "" {
					return cur.Spec, "", fmt.Errorf("domain %s: basic_auth passwords cannot be set from IaC; set them in the dashboard", d.Host)
				}
				if prev := old[d.Host].BasicAuth; prev == nil || !prev.HasPassword {
					return cur.Spec, "", fmt.Errorf("domain %s: basic_auth needs a password; set it in the dashboard first", d.Host)
				}
				d.BasicAuth.HasPassword = true
			}
			doms = append(doms, d)
		}
		if s.Deploy != nil {
			if doms, err = s.checkDomains(ctx, cur, doms); err != nil {
				return cur.Spec, "", err
			}
		}
		// 순서만 다르면 현재 목록을 그대로 둔다(깨끗한 import가 domains 변경으로 보이지 않게).
		if !sameDomainSet(doms, cur.Domains) {
			next.Domains = doms
		}
	}
	if err := s.validateSpec(svc, &next); err != nil {
		return cur.Spec, "", err
	}
	if _, ok := settings["private_dns_name"]; ok {
		next.PrivateDNSName = normalizePrivateDNSName(svc, next.PrivateDNSName)
		if next.PrivateDNSName != cur.PrivateDNSName {
			reason, err := s.privateDNSUnavailable(ctx, cur.EnvironmentID, svc, next.PrivateDNSName)
			if err != nil {
				return cur.Spec, "", err
			}
			if reason != "" {
				return cur.Spec, "", fmt.Errorf("private_dns_name %q: %s", next.PrivateDNSName, reason)
			}
		}
	}
	first, err := s.validatePlacement(ctx, cur, &next)
	if err != nil {
		return cur.Spec, "", err
	}
	if first != "" && store.NormNode(first) != store.NormNode(cur.NodeID) {
		if cur.ID != "" {
			vols, err := s.Store.ListVolumesByInstance(ctx, cur.ID)
			if err != nil {
				return cur.Spec, "", err
			}
			if len(vols) > 0 {
				return cur.Spec, "", errors.New("placement would move a service with volumes to another node; move it with the node setting first")
			}
		}
		return next, first, nil
	}
	return next, "", nil
}

func sameDomainSet(a, b []store.Domain) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(d store.Domain) string { j, _ := json.Marshal(d); return string(j) }
	count := map[string]int{}
	for _, d := range a {
		count[key(d)]++
	}
	for _, d := range b {
		if count[key(d)]--; count[key(d)] < 0 {
			return false
		}
	}
	return true
}

// parseIACDomains는 도메인 목록을 읽는다. 항목은 "host" 문자열 또는 {host, target_port, ...} 객체다.
func parseIACDomains(raw json.RawMessage) ([]store.Domain, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, errors.New("domains must be a list")
	}
	out := make([]store.Domain, 0, len(items))
	for _, item := range items {
		var host string
		if json.Unmarshal(item, &host) == nil {
			out = append(out, store.Domain{Host: host})
			continue
		}
		var d store.Domain
		if err := json.Unmarshal(item, &d); err != nil || d.Host == "" {
			return nil, errors.New("domains entries must be a host name or an object with host")
		}
		out = append(out, d)
	}
	return out, nil
}

type iacApplyInput struct {
	Definition         iac.Definition `json:"definition"`
	PlanHash           string         `json:"plan_hash"`
	ConfirmDestructive bool           `json:"confirm_destructive"`
	Deploy             bool           `json:"deploy"`
}

type iacApplied struct {
	Service       string  `json:"service"`
	Action        string  `json:"action"` // created | updated | deleted
	DeploymentID  *string `json:"deployment_id,omitempty"`
	DeploySkipped *string `json:"deploy_skipped,omitempty"`
}

func (s *Server) applyIACConfig(w http.ResponseWriter, r *http.Request) {
	var input iacApplyInput
	if !decode(w, r, &input) {
		return
	}
	ctx := r.Context()
	p, env, ok := s.iacEnv(w, r)
	if !ok {
		return
	}
	iacMu.Lock()
	defer iacMu.Unlock()
	plan, items, err := s.buildIACPlan(ctx, p, env, &input.Definition, false)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(plan.Errors) > 0 {
		writeJSON(w, 422, map[string]any{"error": strings.Join(plan.Errors, "; "), "plan": plan})
		return
	}
	if input.PlanHash == "" || input.PlanHash != plan.PlanHash {
		writeJSON(w, 409, map[string]any{"error": "the configuration or the environment changed since the plan; run aven config plan again", "plan": plan})
		return
	}
	if plan.Destructive && !input.ConfirmDestructive {
		writeJSON(w, 409, map[string]any{"error": "the plan contains destructive changes; pass confirm_destructive", "plan": plan})
		return
	}
	// 서비스 생성·삭제는 프로젝트 수준 작업이다(환경 토큰은 프로젝트 수준을 읽기만 한다).
	if plan.Summary.Create+plan.Summary.Destroy > 0 {
		if pr := principalFrom(r); pr != nil {
			lv, err := s.projectLevel(ctx, pr, p.ID, "")
			if err != nil {
				s.storeErr(w, err)
				return
			}
			if lv < lvMember {
				writeErr(w, 403, "creating or deleting services needs project member access (environment tokens cannot)")
				return
			}
		}
	}

	applied := []iacApplied{}
	var syncProxy, syncMesh bool
	deployable := []*iacItem{}
	for _, it := range items {
		switch {
		case it.create:
			if err := s.createIACService(ctx, p, env, it); err != nil {
				s.storeErr(w, err)
				return
			}
			syncProxy = true
			applied = append(applied, iacApplied{Service: it.decl.Name, Action: "created"})
			deployable = append(deployable, it)
		case it.destroy:
		case it.changed:
			old := *it.in
			in := *it.in
			in.Spec, in.Variables, in.NodeID = it.spec, it.vars, it.nodeID
			if it.domains {
				// 빠진 호스트의 basic auth 해시를 지운다(새 비밀번호는 IaC로 받지 않으므로 해시를 만들지 않는다).
				if err := s.applyDomainAuth(ctx, &old, in.Domains); err != nil {
					s.storeErr(w, err)
					return
				}
			}
			if err := s.Store.UpdateInstanceConfig(ctx, &in); err != nil {
				s.storeErr(w, err)
				return
			}
			*it.in = in
			syncProxy = syncProxy || it.proxy
			syncMesh = syncMesh || it.mesh
			applied = append(applied, iacApplied{Service: it.decl.Name, Action: "updated"})
			deployable = append(deployable, it)
		}
	}
	if err := s.Store.Tx(ctx, func(tx *store.Store) error {
		if input.Definition.Partial == "" {
			return tx.ClearIACOwnership(ctx, env.ID)
		}
		for _, it := range items {
			if it.destroy {
				if err := tx.DeleteIACOwnership(ctx, env.ID, iac.ServiceAddress(it.svc.Name)); err != nil {
					return err
				}
			} else if err := tx.SetIACOwnership(ctx, env.ID, it.decl.Address(), input.Definition.Partial); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		s.storeErr(w, err)
		return
	}
	for _, it := range items {
		if !it.destroy {
			continue
		}
		if err := iacDeleteService(s, context.WithoutCancel(ctx), it.svc); err != nil {
			s.storeErr(w, err)
			return
		}
		s.Store.DeleteDeployKey(ctx, it.svc.ID)
		s.Events.EmitNamed(ctx, p.ID, nil, &it.svc.ID, &it.svc.Name, "service.deleted", "서비스 "+it.svc.Name+" 삭제 (코드형 인프라)")
		applied = append(applied, iacApplied{Service: it.svc.Name, Action: "deleted"})
	}
	if syncProxy {
		if err := iacSyncProxy(s, ctx); err != nil {
			s.Log.Warn("iac proxy sync", "err", err)
		}
	}
	if syncMesh {
		s.syncMesh()
	}
	if input.Deploy {
		for i := range applied {
			var it *iacItem
			for _, d := range deployable {
				if d.decl.Name == applied[i].Service {
					it = d
				}
			}
			if it == nil {
				continue
			}
			fresh, err := s.Store.GetInstance(ctx, it.in.ID)
			if err != nil {
				s.storeErr(w, err)
				return
			}
			if err := sourceErr(it.svc, fresh); err != nil {
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
	if len(applied) > 0 || len(plan.Claims) > 0 {
		msg := fmt.Sprintf("%s 환경에 코드형 인프라 설정을 적용했습니다 (생성 %d, 변경 %d, 삭제 %d)", env.Name, plan.Summary.Create, plan.Summary.Change, plan.Summary.Destroy)
		if input.Definition.Partial != "" {
			msg += " · partial " + input.Definition.Partial
		}
		if input.Deploy {
			msg += " 및 배포 요청"
		} else if plan.Summary.Create+plan.Summary.Change > 0 {
			msg += ". 다음 배포부터 반영됩니다"
		}
		s.Events.Emit(ctx, p.ID, &env.ID, nil, "environment.config_applied", msg)
	}
	writeJSON(w, 200, map[string]any{"plan": plan, "staged": !input.Deploy, "services": applied, "claimed": plan.Claims})
}

// createIACService는 대시보드의 서비스 추가와 같은 경로로 서비스를 만들고(모든 환경), 대상 환경 인스턴스에 선언을 적용한다.
func (s *Server) createIACService(ctx context.Context, p *store.Project, env *store.Environment, it *iacItem) error {
	svc, err := s.createServiceFromDef(ctx, p, it.def, it.repo, it.branch, nil)
	if err != nil {
		return err
	}
	if svc.Name != it.decl.Name { // 동시에 같은 이름이 생겼다(plan 이후). 지우고 실패시킨다.
		iacDeleteService(s, context.WithoutCancel(ctx), svc)
		return store.ErrConflict
	}
	if svc.SourceType == "function" {
		if err := s.initFunctionCode(ctx, svc, deploy.DefaultFunctionCode); err != nil {
			return err
		}
	}
	in, err := s.Store.GetInstanceFor(ctx, svc.ID, env.ID)
	if err != nil {
		return err
	}
	it.svc = svc
	// 실제 인스턴스(생성된 도메인·비밀번호 포함) 위에 다시 계산한다. plan에서 이미 검증했다.
	fresh, _, err := s.prepareIACItem(ctx, env, svc, in, it.decl, false)
	if err != nil {
		return fmt.Errorf("%s: %w", svc.Name, err)
	}
	in.Spec, in.Variables, in.NodeID = fresh.spec, fresh.vars, fresh.nodeID
	if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
		return err
	}
	it.in = in
	return nil
}

// ---- partial ownership ----

type iacOwnershipView struct {
	EnvironmentID         string            `json:"environment_id"`
	EnvironmentName       string            `json:"environment_name"`
	ConfigETag            string            `json:"config_etag"`
	Partials              map[string]string `json:"iac_partials"`
	WholeProjectAvailable bool              `json:"whole_project_available"`
}

func (s *Server) iacOwnership(ctx context.Context, p *store.Project, env *store.Environment) (*iacOwnershipView, error) {
	svcs, _, err := s.iacServices(ctx, p, env)
	if err != nil {
		return nil, err
	}
	owners, err := s.iacOwners(ctx, env, svcs)
	if err != nil {
		return nil, err
	}
	etag, err := s.iacETag(ctx, p, env, owners)
	if err != nil {
		return nil, err
	}
	return &iacOwnershipView{EnvironmentID: env.ID, EnvironmentName: env.Name, ConfigETag: etag, Partials: owners, WholeProjectAvailable: len(owners) == 0}, nil
}

func (s *Server) listIACPartials(w http.ResponseWriter, r *http.Request) {
	p, env, ok := s.iacEnv(w, r)
	if !ok {
		return
	}
	v, err := s.iacOwnership(r.Context(), p, env)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, v)
}

type iacOwnershipInput struct {
	Partial        string   `json:"partial"`
	To             string   `json:"to"`
	Resources      []string `json:"resources"`
	DryRun         bool     `json:"dry_run"`
	BaseConfigETag string   `json:"base_config_etag"`
}

func (s *Server) releaseIACPartial(w http.ResponseWriter, r *http.Request) {
	s.changeIACOwnership(w, r, false)
}

func (s *Server) transferIACPartial(w http.ResponseWriter, r *http.Request) {
	s.changeIACOwnership(w, r, true)
}

// changeIACOwnership은 소유권 기록만 바꾼다(리소스는 바꾸지도 지우지도 배포하지도 않는다).
func (s *Server) changeIACOwnership(w http.ResponseWriter, r *http.Request, transfer bool) {
	var input iacOwnershipInput
	if !decode(w, r, &input) {
		return
	}
	ctx := r.Context()
	p, env, ok := s.iacEnv(w, r)
	if !ok {
		return
	}
	if !iac.ValidPartial(input.Partial) {
		writeErr(w, 400, "partial must be a named partial (1-64 letters, digits, . _ -)")
		return
	}
	if transfer && (!iac.ValidPartial(input.To) || input.To == input.Partial) {
		writeErr(w, 400, "to must be a different named partial (1-64 letters, digits, . _ -)")
		return
	}
	iacMu.Lock()
	defer iacMu.Unlock()
	view, err := s.iacOwnership(ctx, p, env)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if input.BaseConfigETag != "" && input.BaseConfigETag != view.ConfigETag {
		writeErr(w, 409, "configuration or ownership changed since the preview; run a fresh dry run")
		return
	}
	selected, err := selectIACResources(view.Partials, input.Partial, input.Resources)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	result := map[string]string{}
	for k, v := range view.Partials {
		result[k] = v
	}
	for _, addr := range selected {
		if transfer {
			result[addr] = input.To
		} else {
			delete(result, addr)
		}
	}
	out := map[string]any{"affected_resources": selected, "iac_partials": result, "whole_project_available": len(result) == 0,
		"dry_run": input.DryRun, "base_config_etag": view.ConfigETag}
	if input.DryRun {
		writeJSON(w, 200, out)
		return
	}
	if err := s.Store.Tx(ctx, func(tx *store.Store) error {
		for _, addr := range selected {
			var err error
			if transfer {
				err = tx.SetIACOwnership(ctx, env.ID, addr, input.To)
			} else {
				err = tx.DeleteIACOwnership(ctx, env.ID, addr)
			}
			if err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		s.storeErr(w, err)
		return
	}
	after, err := s.iacOwnership(ctx, p, env)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out["iac_partials"], out["whole_project_available"], out["config_etag"] = after.Partials, after.WholeProjectAvailable, after.ConfigETag
	verb := "해제"
	if transfer {
		verb = "이전 → " + input.To
	}
	s.Events.Emit(ctx, p.ID, &env.ID, nil, "environment.config_ownership", fmt.Sprintf("코드형 인프라 partial %s 소유권 %s: %s", input.Partial, verb, strings.Join(selected, ", ")))
	writeJSON(w, 200, out)
}

// selectIACResources는 partial이 소유한 주소 중 요청한 것(없으면 전부)을 고른다. 하나라도 다른 소유자면 오류다.
func selectIACResources(owners map[string]string, partial string, requested []string) ([]string, error) {
	selected := []string{}
	if len(requested) == 0 {
		for addr, owner := range owners {
			if owner == partial {
				selected = append(selected, addr)
			}
		}
		if len(selected) == 0 {
			return nil, fmt.Errorf("partial %q owns no resources in this environment", partial)
		}
	} else {
		seen := map[string]bool{}
		for _, addr := range requested {
			if owners[addr] != partial {
				return nil, fmt.Errorf("resource %q is not owned by partial %q", addr, partial)
			}
			if !seen[addr] {
				seen[addr] = true
				selected = append(selected, addr)
			}
		}
	}
	sort.Strings(selected)
	return selected, nil
}
