package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/config"
	"github.com/turin-dev/aven/internal/cronx"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/imageupdate"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// ---- views ----

type instanceView struct {
	*store.Instance
	Domains        []domainView `json:"domains"`
	PrivateDomain  string       `json:"private_domain"`
	PendingChanges []string     `json:"pending_changes"`
	// CurrentReplicas는 지금 도는 replica 수(모든 노드 합, 자동 확장·수동 scale 반영)다. replicas는 사용자가 정한 기준 값이다.
	CurrentReplicas int `json:"current_replicas"`
	// AutoscaleStatus는 자동 확장이 켜져 있을 때만 있다.
	AutoscaleStatus *deploy.AutoscaleStatus `json:"autoscale_status"`
	// SealedVariables는 봉인된 변수 이름이다(variables의 값은 SealedValue).
	SealedVariables []string `json:"sealed_variables"`
	// Self는 이 서비스가 aven-server 자신일 때만 있다(지금 도는 서버의 방식과 버전).
	Self *selfInfo `json:"self,omitempty"`
	// AdminHold는 서버 관리자가 이 서비스를 정지(scope service)했거나 배포를 막은(scope deploy) 기록이다.
	AdminHold *store.OwnerHold `json:"admin_hold,omitempty"`
}

func (s *Server) instanceView(ctx context.Context, svc *store.Service, env *store.Environment, in *store.Instance) *instanceView {
	v := &instanceView{Instance: in, Domains: []domainView{}, PendingChanges: []string{}}
	_, v.PrivateDomain, _ = privateDNS(in, svc, env)
	for _, d := range in.Domains {
		v.Domains = append(v.Domains, s.domainView(ctx, in, d))
	}
	if in.CurrentDeploymentID != nil {
		if d, err := s.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil {
			v.PendingChanges = s.functionPending(ctx, svc, in, d, PendingChanges(in, d))
			v.CurrentReplicas = len(d.AllContainers())
		}
	}
	if h, err := s.Store.ServiceHold(ctx, svc.ID); err == nil {
		v.AdminHold = h
	}
	if s.Deploy != nil {
		v.AutoscaleStatus = s.Deploy.AutoscaleStatusOf(in)
		if s.Deploy.IsSelfService(svc.ID) {
			v.Self = s.selfInfo()
			u := s.selfUpdateView(ctx)
			v.Self.UpdateAvailable, v.Self.UpdateBehind = u.Available, u.Behind
		}
	}
	return v
}

// viewInstance는 요청자의 권한에 맞춘 instanceView다. viewer는 변수 값을 볼 수 없다(키만 남기고 값은 MaskedValue).
func (s *Server) viewInstance(r *http.Request, svc *store.Service, env *store.Environment, in *store.Instance) *instanceView {
	v := s.instanceView(r.Context(), svc, env, in)
	// 봉인된 변수는 누구에게도 값을 보여 주지 않는다.
	sealed := s.instanceSealed(r.Context(), in)
	cp := *v.Instance
	if !canSeeSecrets(r) {
		cp.Variables = maskedVars(in.Variables)
	}
	cp.Variables = hideSealed(cp.Variables, sealed)
	v.Instance = &cp
	v.SealedVariables = store.SortedKeys(sealed)
	return v
}

func maskedVars(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = MaskedValue
	}
	return out
}

// PendingChanges는 현재 배포 스냅샷과 달라진 키 목록이다: 변수는 "variables.<KEY>", 설정은 필드 이름.
// 도메인과 TCP proxy는 바로 반영되므로 넣지 않는다.
func PendingChanges(in *store.Instance, d *store.Deployment) []string {
	out := []string{}
	keys := map[string]bool{}
	for k := range in.Variables {
		keys[k] = true
	}
	for k := range d.VarsRaw {
		keys[k] = true
	}
	for k := range keys {
		a, okA := in.Variables[k]
		b, okB := d.VarsRaw[k]
		if okA != okB || a != b {
			out = append(out, "variables."+k)
		}
	}
	var cur, snap map[string]json.RawMessage
	cb, _ := json.Marshal(in.Spec)
	sb, _ := json.Marshal(d.ReqSpec)
	json.Unmarshal(cb, &cur)
	json.Unmarshal(sb, &snap)
	// omitempty 키(placement 등)는 한쪽에만 있을 수 있으므로 양쪽 키를 모두 본다.
	all := map[string]bool{}
	for k := range cur {
		all[k] = true
	}
	for k := range snap {
		all[k] = true
	}
	for k := range all {
		// wait_for_ci는 다음 push부터, private_ports는 mesh에, autoscale은 자동 확장 루프에, outbound_ipv6는 노드 ip6tables에 바로 적용된다
		if k == "domains" || k == "tcp_proxy" || k == "wait_for_ci" || k == "skip_builds" || k == "build_node" || k == "private_ports" || k == "autoscale" ||
			k == "outbound_ipv6" || strings.HasPrefix(k, "image_update_") {
			continue
		}
		if string(snap[k]) != string(cur[k]) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// checkNodeMove는 인스턴스를 다른 노드로 옮길 수 있는지 확인한다.
// 볼륨이 있으면 move_volumes=1이 필요하다: 다음 배포 때 서비스를 멈추고 데이터를 새 노드로 복사한 뒤 시작한다
// (복사에 실패하면 이전 노드는 그대로 서비스한다).
func (s *Server) checkNodeMove(ctx context.Context, in *store.Instance, target string, moveVolumes bool) error {
	if store.IsLocalNode(target) {
		target = store.LocalNodeID
	} else if _, err := s.Store.GetNode(ctx, target); err != nil {
		return fmt.Errorf("node %s does not exist", target)
	}
	vols, err := s.Store.ListVolumesByInstance(ctx, in.ID)
	if err != nil {
		return err
	}
	// 아직 배포한 적이 없으면 볼륨은 다음 배포 때 새 노드에 만들어지므로 남는 데이터가 없다.
	if len(vols) > 0 && !moveVolumes && in.CurrentDeploymentID != nil && s.Deploy.HomeNode(ctx, in) != target {
		return errors.New("attached volumes would be orphaned on the current node; detach them or pass ?move_volumes=1 (the next deploy stops the service, copies the volume data to the new node, then starts it there)")
	}
	return nil
}

// ---- lookups ----

type instCtx struct {
	svc *store.Service
	env *store.Environment
	p   *store.Project
	in  *store.Instance
}

func (s *Server) loadInstance(w http.ResponseWriter, r *http.Request) (*instCtx, bool) {
	ctx := r.Context()
	svc, err := s.Store.GetService(ctx, r.PathValue("sid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	env, err := s.Store.GetEnvironment(ctx, r.PathValue("eid"))
	if err != nil || env.ProjectID != svc.ProjectID {
		writeErr(w, 404, "environment not found")
		return nil, false
	}
	in, err := s.Store.GetInstanceFor(ctx, svc.ID, env.ID)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	p, err := s.Store.GetProject(ctx, svc.ProjectID)
	if err != nil {
		s.storeErr(w, err)
		return nil, false
	}
	return &instCtx{svc: svc, env: env, p: p, in: in}, true
}

// ---- create service ----

func (s *Server) uniqueServiceName(ctx context.Context, projectID, base string) (string, error) {
	base = sanitizeName(base)
	svcs, err := s.Store.ListServices(ctx, projectID)
	if err != nil {
		return "", err
	}
	taken := map[string]bool{"shared": true}
	for _, sv := range svcs {
		taken[sv.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		suffix := fmt.Sprintf("-%d", i)
		b := base
		if len(b)+len(suffix) > 40 {
			b = strings.TrimRight(b[:40-len(suffix)], "-")
		}
		name = b + suffix
	}
	return name, nil
}

func sanitizeName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			b.WriteRune(c)
		default:
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "service"
	}
	return out
}

func nameFromRepo(repo string) string {
	repo = strings.TrimSuffix(strings.TrimRight(repo, "/"), ".git")
	if i := strings.LastIndexAny(repo, "/:"); i >= 0 {
		repo = repo[i+1:]
	}
	return repo
}

func nameFromImage(img string) string {
	if i := strings.LastIndex(img, "@"); i >= 0 {
		img = img[:i]
	}
	if i := strings.LastIndex(img, ":"); i > strings.LastIndex(img, "/") {
		img = img[:i]
	}
	return path.Base(img)
}

func (s *Server) validRepoURL(u string) bool {
	return !strings.HasPrefix(u, "-") && (strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "http://") ||
		strings.HasPrefix(u, "ssh://") || strings.HasPrefix(u, "git@") || s.AllowFileRepo && strings.HasPrefix(u, "file://"))
}

var imageRe = `^[a-zA-Z0-9][a-zA-Z0-9._/:@+-]{0,254}$`

// createServiceFromDef는 서비스와 모든 환경의 인스턴스(+볼륨)를 만든다.
func (s *Server) createServiceFromDef(ctx context.Context, p *store.Project, def catalog.ServiceDef, repo *string, branch string, template *string) (*store.Service, error) {
	// guardrail: 출처가 승인되지 않은 서비스는 만들지 않고, 공개 네트워킹 제한이 켜져 있으면 도메인·TCP 프록시를 자동으로 만들지 않는다.
	probe := &store.Instance{}
	probe.Spec.Defaults()
	def.ApplyToInstance(probe)
	if repo != nil {
		probe.RepoURL = repo
	}
	if err := s.checkSourcePolicy(ctx, guardrails.Source{Type: def.SourceType, DatabaseKind: def.DatabaseKind, RepoURL: probe.RepoURL, Image: probe.Image}); err != nil {
		return nil, err
	}
	policies, err := s.accessPolicies(ctx)
	if err != nil {
		return nil, err
	}
	name, err := s.uniqueServiceName(ctx, p.ID, def.Name)
	if err != nil {
		return nil, err
	}
	svc := &store.Service{ProjectID: p.ID, Name: name, SourceType: def.SourceType, Template: template}
	if def.Icon != "" {
		icon := def.Icon
		svc.Icon = &icon
	}
	if def.DatabaseKind != "" {
		k := def.DatabaseKind
		svc.DatabaseKind = &k
	}
	if err := s.Store.CreateService(ctx, svc); err != nil {
		return nil, err
	}
	created := false
	defer func() {
		if created {
			return
		}
		cleanupCtx := context.WithoutCancel(ctx)
		if err := s.Store.Tx(cleanupCtx, func(tx *store.Store) error {
			instances, err := tx.ListInstancesByService(cleanupCtx, svc.ID)
			if err != nil {
				return err
			}
			for _, in := range instances {
				volumes, err := tx.ListVolumesByInstance(cleanupCtx, in.ID)
				if err != nil {
					return err
				}
				for _, v := range volumes {
					if err := tx.DeleteVolume(cleanupCtx, v.ID); err != nil {
						return err
					}
				}
			}
			return tx.DeleteService(cleanupCtx, svc.ID)
		}); err != nil {
			s.Log.Error("clean up incomplete service", "service", svc.ID, "err", err)
		}
	}()
	envs, err := s.Store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return nil, err
	}
	for i, env := range envs {
		d := def
		if i > 0 { // 환경마다 다른 DB 비밀번호
			d = def.ForAnotherEnvironment()
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
		in.Spec.Defaults()
		if repo != nil {
			r := *repo
			in.RepoURL = &r
			in.Branch = branch
			in.AutoDeploy = true
		}
		d.ApplyToInstance(in)
		if def.SourceType == "repo" && def.Settings.RestartPolicy == nil {
			in.RestartPolicy = "on_failure"
		}
		if s.Deploy.Cfg.BaseDomain != "" && !policies.RestrictGeneratedDomains {
			gen := []catalog.DomainDef{}
			if def.Domain {
				gen = append(gen, catalog.DomainDef{})
			}
			gen = append(gen, def.Domains...)
			for j, gd := range gen {
				label := svc.Name
				if j > 0 { // 같은 인스턴스의 두 번째 도메인부터는 이름을 달리한다(아직 저장 전이라 겹침 검사에 안 잡힌다)
					label = svc.Name + "-" + itoa(j+1)
				}
				host, err := s.generateDomain(ctx, label, env.Name)
				if err != nil {
					break
				}
				dom := store.Domain{Host: host, Kind: "generated"}
				if gd.TargetPort != nil {
					tp := *gd.TargetPort
					dom.TargetPort = &tp
				}
				in.Domains = append(in.Domains, dom)
			}
		}
		if def.TCPProxyPort > 0 && !policies.RestrictTCPProxy {
			if port, err := s.Deploy.AllocateTCPPortFor(ctx, s.Deploy.HomeNode(ctx, in)); err == nil {
				in.TCPProxy = &store.TCPProxy{ContainerPort: def.TCPProxyPort, PublicPort: port}
			} else {
				s.Log.Warn("template tcp proxy", "service", svc.Name, "err", err)
			}
		}
		bucketWithoutDomain(def, in)
		if err := s.Store.CreateInstance(ctx, in); err != nil {
			return nil, err
		}
		for _, mp := range d.Volumes {
			v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, InstanceID: &in.ID, Name: volumeName(svc.Name, mp), MountPath: mp}
			if lim, ok := def.VolumeLimits[mp]; ok {
				l := lim
				v.SizeLimitMB = &l
			}
			if err := s.Store.CreateVolume(ctx, v); err != nil {
				return nil, err
			}
		}
	}
	s.Events.Emit(ctx, p.ID, nil, &svc.ID, "service.created", "서비스 "+svc.Name+" 생성")
	created = true
	return svc, nil
}

func (s *Server) createService(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name          *string `json:"name"`
		SourceType    string  `json:"source_type"`
		RepoURL       *string `json:"repo_url"`
		Branch        *string `json:"branch"`
		Image         *string `json:"image"`
		DatabaseKind  *string `json:"database_kind"`
		EnvironmentID *string `json:"environment_id"`
		Code          *string `json:"code"` // source_type=function
	}
	if !decode(w, r, &in) {
		return
	}
	var fnCode string
	var env *store.Environment
	if in.EnvironmentID != nil && *in.EnvironmentID != "" {
		env, err = s.Store.GetEnvironment(ctx, *in.EnvironmentID)
		if err != nil || env.ProjectID != p.ID {
			writeErr(w, 400, "environment_id must be an environment of this project")
			return
		}
		if ok, err := s.canUseEnvironment(r, env); err != nil || !ok {
			s.envAccessErr(w, env, err)
			return
		}
	}
	var def catalog.ServiceDef
	var repo *string
	branch := "main"
	switch in.SourceType {
	case "repo":
		if in.RepoURL == nil || !s.validRepoURL(strings.TrimSpace(*in.RepoURL)) {
			writeErr(w, 400, "repo_url must be an https://, ssh:// or git@ URL")
			return
		}
		u := strings.TrimSpace(*in.RepoURL)
		repo = &u
		if in.Branch != nil && *in.Branch != "" {
			branch = strings.TrimSpace(*in.Branch)
		}
		if !branchRe.MatchString(branch) || strings.HasPrefix(branch, "-") {
			writeErr(w, 400, "invalid branch")
			return
		}
		def = catalog.ServiceDef{Name: nameFromRepo(u), Icon: "github", SourceType: "repo"}
	case "image":
		if in.Image == nil || strings.TrimSpace(*in.Image) == "" {
			// 빈 서비스: 소스는 나중에 설정한다.
			def = catalog.ServiceDef{Name: "service", Icon: "docker", SourceType: "image"}
			if env != nil {
				writeErr(w, 400, "소스를 먼저 설정하세요")
				return
			}
			break
		}
		if !regexpMatch(imageRe, strings.TrimSpace(*in.Image)) {
			writeErr(w, 400, "invalid image (e.g. nginx:alpine)")
			return
		}
		img := strings.TrimSpace(*in.Image)
		def = catalog.ServiceDef{Name: nameFromImage(img), Icon: "docker", SourceType: "image", Image: img}
	case "database", catalog.BucketKind: // source_type "bucket"은 database + database_kind "bucket"과 같다
		if in.SourceType == catalog.BucketKind {
			k := catalog.BucketKind
			in.DatabaseKind = &k
		}
		if in.DatabaseKind == nil {
			writeErr(w, 400, "database_kind is required (postgres, mysql, redis, mongo, bucket)")
			return
		}
		d, ok := catalog.DatabaseService(*in.DatabaseKind)
		if !ok {
			writeErr(w, 400, "unknown database_kind: "+*in.DatabaseKind)
			return
		}
		def = d
	case "function":
		c, err := functionCodeArg(in.Code)
		if err != nil {
			writeErr(w, 413, err.Error())
			return
		}
		fnCode, def = c, functionDef()
	default:
		writeErr(w, 400, "source_type must be repo, image, database, bucket or function")
		return
	}
	if in.Name != nil && *in.Name != "" {
		n := strings.TrimSpace(*in.Name)
		if !nameRe.MatchString(n) || n == "shared" {
			writeErr(w, 400, "name must be lowercase letters, digits and '-' (max 40), and not 'shared'")
			return
		}
		svcs, _ := s.Store.ListServices(ctx, p.ID)
		for _, sv := range svcs {
			if sv.Name == n {
				writeErr(w, 409, "service already exists: "+n)
				return
			}
		}
		def.Name = n
	}
	svc, err := s.createServiceFromDef(ctx, p, def, repo, branch, nil)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if svc.SourceType == "function" {
		if err := s.initFunctionCode(ctx, svc, fnCode); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if env != nil {
		if inst, err := s.Store.GetInstanceFor(ctx, svc.ID, env.ID); err == nil {
			if _, err := s.Deploy.Enqueue(ctx, inst, deploy.EnqueueOpts{Trigger: "manual"}); err != nil {
				s.Log.Warn("initial deploy", "err", err)
			}
		}
	}
	writeJSON(w, 201, svc)
}

func regexpMatch(re, s string) bool {
	return regexpCache(re).MatchString(s)
}

func (s *Server) updateService(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	svc, err := s.Store.GetService(ctx, r.PathValue("sid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name *string  `json:"name"`
		Icon **string `json:"icon"`
	}
	if !decode(w, r, &in) {
		return
	}
	oldName := svc.Name
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if !nameRe.MatchString(n) || n == "shared" {
			writeErr(w, 400, "name must be lowercase letters, digits and '-' (max 40), and not 'shared'")
			return
		}
		svc.Name = n
	}
	if in.Icon != nil {
		svc.Icon = *in.Icon
	}
	if err := s.Store.UpdateService(ctx, svc); err != nil {
		if err == store.ErrConflict {
			writeErr(w, 409, "service already exists: "+svc.Name)
			return
		}
		s.storeErr(w, err)
		return
	}
	if oldName != svc.Name {
		// 다른 서비스 변수의 ${{old.X}} 참조를 새 이름으로 바꾼다.
		svcs, _ := s.Store.ListServices(ctx, svc.ProjectID)
		for _, o := range svcs {
			insts, _ := s.Store.ListInstancesByService(ctx, o.ID)
			for _, in := range insts {
				changed := false
				for k, v := range in.Variables {
					if nv := vars.RenameService(v, oldName, svc.Name); nv != v {
						in.Variables[k] = nv
						changed = true
					}
				}
				if changed {
					s.Store.UpdateInstanceConfig(ctx, in)
				}
			}
		}
	}
	writeJSON(w, 200, svc)
}

func (s *Server) deleteService(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	svc, err := s.Store.GetService(ctx, r.PathValue("sid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Deploy.DeleteService(context.WithoutCancel(ctx), svc); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Store.DeleteDeployKey(ctx, svc.ID)
	s.Events.EmitNamed(ctx, svc.ProjectID, nil, &svc.ID, &svc.Name, "service.deleted", "서비스 "+svc.Name+" 삭제")
	w.WriteHeader(204)
}

// ---- instances ----

func (s *Server) getInstance(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.viewInstance(r, c.svc, c.env, c.in))
}

// specKeys는 PATCH로 바꿀 수 있는 설정 키다(domains/tcp_proxy는 전용 엔드포인트도 있다).
var specKeys = map[string]bool{
	"repo_url": true, "branch": true, "root_dir": true, "image": true, "builder": true, "dockerfile_path": true,
	"image_update_policy": true, "image_update_window": true, "image_update_days": true,
	"image_update_start_hour": true, "image_update_end_hour": true,
	"watch_paths": true, "auto_deploy": true, "start_command": true, "pre_deploy_command": true, "port": true,
	"healthcheck_type": true, "healthcheck_path": true, "healthcheck_timeout_s": true, "restart_policy": true,
	"restart_max_retries": true, "replicas": true, "cron_schedule": true, "sleep_when_idle": true, "sleep_after_s": true, "domains": true,
	"memory_limit_mb": true, "cpu_limit": true, "build_secrets": true, "wait_for_ci": true, "skip_builds": true, "build_node": true,
	"overlap_seconds": true, "draining_seconds": true, "private_ports": true, "autoscale": true, "placement": true,
	"private_dns_name": true, // 다음 배포 때 반영된다(pending_changes에 보인다)
}

func (s *Server) patchInstance(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var patch map[string]json.RawMessage
	if !decode(w, r, &patch) {
		return
	}
	in := c.in
	nodeChanged := false
	if raw, ok := patch["node_id"]; ok {
		var id string
		if err := json.Unmarshal(raw, &id); err != nil || id == "" {
			writeErr(w, 400, "node_id must be a node ID")
			return
		}
		if id != in.NodeID {
			if err := s.checkNodeMove(ctx, in, id, r.URL.Query().Get("move_volumes") == "1"); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			in.NodeID = id
			nodeChanged = true
		}
	}
	// 아웃바운드 IPv6은 재배포 없이 바로 반영한다(specKeys/pending_changes 밖). 끌 때는 노드가 차단을 지원하는지 먼저 본다.
	egressChanged, ok := s.patchOutboundIPv6(w, r, in, c.env.ID, patch)
	if !ok {
		return
	}
	before := in.Spec
	// 설정: 현재 값 위에 바뀐 키만 덮는다(null도 그대로 반영된다).
	cur, _ := json.Marshal(in.Spec)
	var m map[string]json.RawMessage
	json.Unmarshal(cur, &m)
	specChanged := false
	for k, v := range patch {
		if specKeys[k] {
			m[k] = v
			specChanged = true
		}
	}
	if specChanged {
		b, _ := json.Marshal(m)
		var sp store.Spec
		if err := json.Unmarshal(b, &sp); err != nil {
			writeErr(w, 400, "invalid settings: "+err.Error())
			return
		}
		if _, ok := patch["domains"]; ok {
			doms, err := s.normalizeDomains(ctx, in, sp.Domains)
			if err != nil {
				domainInputErr(w, err) // 호스트 중복은 409
				return
			}
			sp.Domains = doms
			if err := s.checkNewGeneratedDomains(ctx, r, before.Domains, sp.Domains); err != nil {
				s.policyErr(w, err)
				return
			}
		}
		if err := s.validateSpec(c.svc, &sp); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if _, ok := patch["private_dns_name"]; ok {
			sp.PrivateDNSName = normalizePrivateDNSName(c.svc, sp.PrivateDNSName)
			if reason, err := s.privateDNSUnavailable(ctx, c.env.ID, c.svc, sp.PrivateDNSName); err != nil {
				s.storeErr(w, err)
				return
			} else if reason != "" {
				writeErr(w, 400, "private_dns_name: "+reason)
				return
			}
		}
		_, nodeGiven := patch["node_id"]
		_, placeGiven := patch["placement"]
		_, replicasGiven := patch["replicas"]
		if len(sp.Placement) > 0 && replicasGiven && !placeGiven && sp.Replicas != store.PlacementTotal(sp.Placement) {
			writeErr(w, 400, "placement가 있으면 replicas 대신 placement의 노드별 replica 수를 바꾸세요")
			return
		}
		first, err := s.validatePlacement(ctx, in, &sp)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		if first != "" && store.NormNode(first) != store.NormNode(in.NodeID) {
			if nodeGiven && !placeGiven {
				writeErr(w, 400, "placement가 있으면 node_id 대신 placement의 첫 노드가 주 노드입니다")
				return
			}
			if err := s.checkNodeMove(ctx, in, first, r.URL.Query().Get("move_volumes") == "1"); err != nil {
				writeErr(w, 400, err.Error())
				return
			}
			in.NodeID = first
			nodeChanged = true
		}
		sp.TCPProxy = in.TCPProxy
		in.Spec = sp
		// 배포 출처 guardrail: 소스를 바꾸면 검사한다(연결 해제는 항상 된다).
		_, repoChanged := patch["repo_url"]
		_, imageChanged := patch["image"]
		if repoChanged || imageChanged {
			if err := s.checkSourcePolicy(ctx, guardrails.SourceOf(c.svc, &in.Spec)); err != nil {
				s.policyErr(w, err)
				return
			}
		}
	}
	if len(in.Placement) > 0 && store.NormNode(in.NodeID) != store.NormNode(in.Placement[0].NodeID) {
		writeErr(w, 400, "placement가 있으면 node_id 대신 placement의 첫 노드가 주 노드입니다")
		return
	}
	varsChanged := false
	// 봉인: 지금 봉인된 키(가린 값 SealedValue를 그대로 보내면 원래 값 유지)와 새로 봉인할 키(sealed_variables).
	oldVars := copyMap(in.Variables)
	sealedBefore := s.instanceSealed(ctx, in)
	var sealAdd []string
	if raw, ok := patch["sealed_variables"]; ok {
		keys, err := parseSealList(raw)
		if err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		sealAdd = keys
	}
	if raw, ok := patch["variables"]; ok {
		var v map[string]string
		if err := json.Unmarshal(raw, &v); err != nil {
			writeErr(w, 400, "variables must be an object of strings")
			return
		}
		if v == nil {
			v = map[string]string{}
		}
		in.Variables = v
		varsChanged = true
	}
	if raw, ok := patch["variables_patch"]; ok {
		var v map[string]*string
		if err := json.Unmarshal(raw, &v); err != nil {
			writeErr(w, 400, "variables_patch must be an object of string|null")
			return
		}
		for k, val := range v {
			if val == nil {
				delete(in.Variables, k)
			} else {
				in.Variables[k] = *val
			}
		}
		varsChanged = true
	}
	if varsChanged {
		keepSealed(oldVars, in.Variables, sealedBefore, false)
	}
	if err := sealRequest(sealAdd, in.Variables); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if varsChanged {
		if err := validateVars(in.Variables); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		// 순환 참조는 저장 전에 막는다.
		if _, err := s.Deploy.Resolve(ctx, in, &deploy.Override{InstanceID: in.ID, Vars: in.Variables, Spec: &in.Spec}); err != nil {
			var ce *vars.CycleError
			if errors.As(err, &ce) {
				writeErr(w, 400, err.Error())
				return
			}
		}
	}
	if raw, ok := patch["canvas"]; ok {
		var cv *store.Canvas
		if json.Unmarshal(raw, &cv) == nil && cv != nil {
			s.Store.SetInstanceCanvas(ctx, in.ID, *cv)
			in.Canvas = cv
		}
	}
	if varsChanged || len(sealAdd) > 0 {
		// 값을 저장하기 전에 봉인을 적는다(바뀐 봉인 값이 잠깐이라도 보이지 않게).
		if err := s.Store.SealVariables(ctx, in.ID, in.Variables, resealKeys(sealedBefore, sealAdd)); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	if newly := newlySealed(sealedBefore, sealAdd); len(newly) > 0 {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "variables.updated", fmt.Sprintf("%s (%s): 변수 %s를 봉인했습니다", c.svc.Name, c.env.Name, strings.Join(newly, ", ")))
	}
	if specChanged || varsChanged || nodeChanged || egressChanged {
		if err := s.Store.UpdateInstanceConfig(ctx, in); err != nil {
			s.domainWriteErr(w, err) // 도메인 호스트 claim 경합은 409(한국어), 그 밖은 storeErr
			return
		}
	}
	if nodeChanged {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "service.updated", fmt.Sprintf("%s (%s): 노드를 %s(으)로 바꿨습니다. 다음 배포부터 적용됩니다", c.svc.Name, c.env.Name, in.NodeID))
	}
	if varsChanged {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "variables.updated", fmt.Sprintf("%s (%s): 변수를 변경했습니다", c.svc.Name, c.env.Name))
	}
	if specChanged {
		s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "service.updated", fmt.Sprintf("%s (%s): 설정을 변경했습니다", c.svc.Name, c.env.Name))
		// 도메인과 포트는 재배포 없이 바로 반영한다.
		if !reflect.DeepEqual(before.Domains, in.Domains) || !reflect.DeepEqual(before.Port, in.Port) {
			if err := s.Deploy.SyncProxy(ctx); err != nil {
				s.Log.Warn("proxy sync", "err", err)
			}
		}
		// private_ports(와 port)는 노드 간 mesh export에 바로 반영한다.
		if !reflect.DeepEqual(before.PrivatePorts, in.PrivatePorts) || !reflect.DeepEqual(before.Port, in.Port) {
			s.syncMesh()
		}
	}
	if egressChanged {
		s.applyOutboundIPv6(ctx, c, in)
	}
	if q := r.URL.Query().Get("deploy"); q == "1" || q == "true" {
		if err := sourceErr(c.svc, in); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		trigger := s.deployTrigger(ctx, in)
		if _, err := s.Deploy.Enqueue(ctx, in, deploy.EnqueueOpts{Trigger: trigger}); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	fresh, err := s.Store.GetInstance(ctx, in.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, s.viewInstance(r, c.svc, c.env, fresh))
}

func (s *Server) validateSpec(svc *store.Service, sp *store.Spec) error {
	sp.Defaults()
	switch {
	case sp.RepoURL != nil && *sp.RepoURL != "" && !s.validRepoURL(*sp.RepoURL):
		return errors.New("repo_url must be an https://, ssh:// or git@ URL")
	case !branchRe.MatchString(sp.Branch) || strings.HasPrefix(sp.Branch, "-"):
		return errors.New("invalid branch")
	case sp.Image != nil && *sp.Image != "" && !regexpMatch(imageRe, *sp.Image):
		return errors.New("invalid image")
	case sp.ImageUpdatePolicy != "" && sp.ImageUpdatePolicy != "disabled" && sp.ImageUpdatePolicy != "patch" && sp.ImageUpdatePolicy != "minor":
		return errors.New("image_update_policy must be disabled, patch or minor")
	case imageupdate.Enabled(sp.ImageUpdatePolicy) && svc.SourceType != "image":
		return errors.New("image auto updates are available for image services only")
	case imageupdate.Enabled(sp.ImageUpdatePolicy) && (sp.Image == nil || *sp.Image == ""):
		return errors.New("set an image before enabling image auto updates")
	case sp.Builder != "auto" && sp.Builder != "dockerfile" && sp.Builder != "railpack":
		return errors.New("builder must be auto, dockerfile or railpack")
	case sp.Port != nil && (*sp.Port < 1 || *sp.Port > 65535):
		return errors.New("port must be 1..65535 or null")
	case sp.HealthcheckType != "http" && sp.HealthcheckType != "tcp" && sp.HealthcheckType != "none":
		return errors.New("healthcheck_type must be http, tcp or none")
	case sp.HealthcheckTimeoutS < 1 || sp.HealthcheckTimeoutS > 3600:
		return errors.New("healthcheck_timeout_s must be 1..3600")
	case sp.RestartPolicy != "always" && sp.RestartPolicy != "on_failure" && sp.RestartPolicy != "never":
		return errors.New("restart_policy must be always, on_failure or never")
	case sp.RestartMaxRetries < 0 || sp.RestartMaxRetries > 100:
		return errors.New("restart_max_retries must be 0..100")
	case sp.Replicas < 1 || sp.Replicas > 10:
		return errors.New("replicas must be 1..10")
	case sp.MemoryLimitMB != nil && (*sp.MemoryLimitMB < 6 || *sp.MemoryLimitMB > 4194304):
		return errors.New("memory_limit_mb must be 6..4194304 or null")
	case sp.CPULimit != nil && (*sp.CPULimit < 0.01 || *sp.CPULimit > 256):
		return errors.New("cpu_limit must be 0.01..256 or null")
	case sp.BuildNode != "" && sp.BuildNode != store.BuildNodeLocal:
		return errors.New(`build_node must be "local" (build on the aven-server node) or empty (build on the deployment node)`)
	case !validSecretNames(sp.BuildSecrets):
		return errors.New("build_secrets must be a list of variable names ([A-Za-z_][A-Za-z0-9_]*)")
	case sp.SleepAfterS < 10 || sp.SleepAfterS > 7*24*3600:
		return errors.New("sleep_after_s must be 10..604800")
	case sp.OverlapSeconds < 0 || sp.OverlapSeconds > config.MaxOverlapSeconds:
		return fmt.Errorf("overlap_seconds must be 0..%d", config.MaxOverlapSeconds)
	case sp.DrainingSeconds != nil && (*sp.DrainingSeconds < 0 || *sp.DrainingSeconds > config.MaxDrainingSeconds):
		return fmt.Errorf("draining_seconds must be 0..%d or null", config.MaxDrainingSeconds)
	case !validPrivatePorts(sp.PrivatePorts):
		return errors.New("private_ports must be a list of up to 16 distinct ports (1..65535)")
	}
	if err := validateAutoscale(sp.Autoscale); err != nil {
		return err
	}
	if err := imageupdate.ValidateWindow(sp.ImageUpdateWindow, sp.ImageUpdateDays, sp.ImageUpdateStartHour, sp.ImageUpdateEndHour); err != nil {
		return err
	}
	if strings.Contains(sp.RootDir, "..") {
		return errors.New("root_dir must not contain '..'")
	}
	if !strings.HasPrefix(sp.RootDir, "/") {
		sp.RootDir = "/" + sp.RootDir
	}
	if strings.Contains(sp.DockerfilePath, "..") {
		return errors.New("dockerfile_path must not contain '..'")
	}
	if sp.CronSchedule != nil {
		c := strings.TrimSpace(*sp.CronSchedule)
		if c == "" {
			sp.CronSchedule = nil
		} else {
			if _, err := cronx.Parse(c); err != nil {
				return err
			}
			sp.CronSchedule = &c
		}
	}
	for _, p := range []**string{&sp.StartCommand, &sp.PreDeployCommand} {
		if *p != nil && strings.TrimSpace(**p) == "" {
			*p = nil
		}
	}
	return nil
}

// validateAutoscale은 자동 확장 설정을 검증하고 cooldown 기본값(올림 60초, 내림 300초)과 min_replicas 기본값(1)을 채운다.
func validateAutoscale(a *store.Autoscale) error {
	if a == nil {
		return nil
	}
	a.Defaults()
	if a.MinReplicas == 0 {
		a.MinReplicas = 1
	}
	pos := func(p *float64, hi float64) bool { return p == nil || (*p > 0 && *p <= hi) }
	switch {
	case a.MinReplicas < 1 || a.MinReplicas > deploy.MaxReplicas:
		return fmt.Errorf("autoscale.min_replicas must be 1..%d", deploy.MaxReplicas)
	case a.MaxReplicas < a.MinReplicas || a.MaxReplicas > deploy.MaxReplicas:
		return fmt.Errorf("autoscale.max_replicas must be min_replicas..%d", deploy.MaxReplicas)
	case !pos(a.TargetCPUPercent, 1000):
		return errors.New("autoscale.target_cpu_percent must be > 0 and <= 1000 or null")
	case !pos(a.TargetMemoryPercent, 100):
		return errors.New("autoscale.target_memory_percent must be > 0 and <= 100 or null")
	case !pos(a.TargetRPSPerReplica, 1e6):
		return errors.New("autoscale.target_rps_per_replica must be > 0 or null")
	case a.ScaleUpCooldownS > 86400 || a.ScaleDownCooldownS > 86400:
		return errors.New("autoscale cooldowns must be 1..86400 seconds")
	case a.Enabled && a.TargetCPUPercent == nil && a.TargetMemoryPercent == nil && a.TargetRPSPerReplica == nil:
		return errors.New("autoscale needs at least one of target_cpu_percent, target_memory_percent, target_rps_per_replica")
	}
	return nil
}

// validatePlacement는 placement를 검증하고 정규화한다(노드 ID, replicas 합계). 첫 노드(주 노드)를 돌려준다(placement가 없으면 "").
// 둘 이상의 노드에 걸치면 볼륨, cron, sleep_when_idle과 함께 쓸 수 없다.
func (s *Server) validatePlacement(ctx context.Context, in *store.Instance, sp *store.Spec) (string, error) {
	if len(sp.Placement) == 0 {
		sp.Placement = nil
		return "", nil
	}
	if len(sp.Placement) > deploy.MaxReplicas {
		return "", fmt.Errorf("placement may list at most %d nodes", deploy.MaxReplicas)
	}
	seen := map[string]bool{}
	for i := range sp.Placement {
		p := &sp.Placement[i]
		id := strings.TrimSpace(p.NodeID)
		if id == "" {
			return "", fmt.Errorf("placement[%d].node_id is required", i)
		}
		id = store.NormNode(id)
		if seen[id] {
			return "", fmt.Errorf("placement lists node %s twice", id)
		}
		seen[id] = true
		if !store.IsLocalNode(id) {
			if _, err := s.Store.GetNode(ctx, id); err != nil {
				return "", fmt.Errorf("node %s does not exist", id)
			}
		}
		if p.Replicas < 1 || p.Replicas > deploy.MaxReplicas {
			return "", fmt.Errorf("placement[%d].replicas must be 1..%d", i, deploy.MaxReplicas)
		}
		p.NodeID = id
	}
	total := store.PlacementTotal(sp.Placement)
	if total > deploy.MaxReplicas {
		return "", fmt.Errorf("placement replicas add up to %d (max %d)", total, deploy.MaxReplicas)
	}
	if len(sp.Placement) > 1 {
		vols, err := s.Store.ListVolumesByInstance(ctx, in.ID)
		if err != nil {
			return "", err
		}
		switch {
		case len(vols) > 0:
			return "", errors.New("services with volumes cannot be placed on several nodes (each replica would write its own copy); detach the volume or use a single node")
		case sp.CronSchedule != nil:
			return "", errors.New("cron services cannot be placed on several nodes")
		case sp.SleepWhenIdle:
			return "", errors.New("sleep_when_idle cannot be used with a multi-node placement")
		case sp.Autoscale != nil && sp.Autoscale.Enabled && sp.Autoscale.MinReplicas < len(sp.Placement):
			return "", errors.New("autoscale.min_replicas must be at least the number of placement nodes (every node keeps one replica)")
		}
	}
	sp.Replicas = total
	return sp.Placement[0].NodeID, nil
}

func (s *Server) putCanvas(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	var cv store.Canvas
	if !decode(w, r, &cv) {
		return
	}
	if err := s.Store.SetInstanceCanvas(r.Context(), c.in.ID, cv); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) resolvedVars(w http.ResponseWriter, r *http.Request) {
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	res, err := s.Deploy.Resolve(r.Context(), c.in, nil)
	if err != nil {
		var ce *vars.CycleError
		if errors.As(err, &ce) {
			writeErr(w, 400, err.Error())
			return
		}
		s.storeErr(w, err)
		return
	}
	// 봉인 값(자기 것이든 참조한 것이든)에서 온 변수는 값을 가린다. 계산하지 못하면 값을 하나도 보이지 않는다.
	sealedRes, err := s.sealedResolved(r.Context(), c.in)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type rv struct {
		Value  string `json:"value"`
		Sealed bool   `json:"sealed,omitempty"`
		Source string `json:"source"` // user | shared | reference | injected
		// CrossNode는 다른 노드에서 도는 서비스를 참조한다는 표시다(정보용). Mesh는 그 노드와 노드 간 사설 네트워크로 닿는지다.
		CrossNode bool     `json:"cross_node,omitempty"`
		Mesh      *bool    `json:"mesh,omitempty"`
		Services  []string `json:"cross_node_services,omitempty"`
	}
	cross, err := s.Deploy.CrossNodeRefs(r.Context(), c.in.EnvironmentID, c.svc.Name, s.Deploy.HomeNode(r.Context(), c.in), c.in.Variables)
	if err != nil {
		s.Log.Warn("cross-node references", "err", err)
		cross = nil
	}
	out := map[string]rv{}
	secrets := canSeeSecrets(r)
	for k, v := range res.Vars {
		if !secrets {
			v = MaskedValue // viewer는 해석된 값도 볼 수 없다
		}
		item := rv{Value: v, Source: VarSource(c.in.Variables, k)}
		if sealedRes[k] {
			item.Value, item.Sealed = SealedValue, true
		}
		if cr, ok := cross[k]; ok {
			m := cr.Mesh
			item.CrossNode, item.Mesh, item.Services = true, &m, cr.Services
		}
		out[k] = item
	}
	writeJSON(w, 200, out)
}

// validPrivatePorts는 private_ports를 검사한다(비어 있으면 [PORT]).
func validPrivatePorts(ports []int) bool {
	if len(ports) > 16 {
		return false
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 1 || p > 65535 || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

var sharedOnlyRe = regexp.MustCompile(`^\$\{\{\s*shared\.[A-Za-z0-9_.-]+\s*\}\}$`)

// VarSource는 해석된 변수의 출처다: 사용자 값에 참조가 없으면 user, ${{shared.X}} 하나면 shared,
// 다른 참조가 있으면 reference, 사용자 값이 없으면 injected(KEEL_*, PORT).
func VarSource(user map[string]string, key string) string {
	raw, ok := user[key]
	switch {
	case !ok:
		return "injected"
	case sharedOnlyRe.MatchString(strings.TrimSpace(raw)):
		return "shared"
	case len(vars.References(raw)) > 0:
		return "reference"
	}
	return "user"
}

var secretNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

func validSecretNames(names []string) bool {
	if len(names) > 100 {
		return false
	}
	for _, n := range names {
		if !secretNameRe.MatchString(n) {
			return false
		}
	}
	return true
}
