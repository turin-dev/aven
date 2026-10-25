package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

var notifyEvents = map[string]bool{"deploy.success": true, "deploy.failed": true, "deploy.crashed": true, "volume.backup.failed": true,
	"volume.near_limit": true, "volume.limit_exceeded": true}

// listProjects는 요청자에게 보이는 프로젝트만 돌려준다(작업 공간 member/viewer는 멤버인 프로젝트만, 범위 토큰은 그 프로젝트만).
func (s *Server) listProjects(w http.ResponseWriter, r *http.Request) {
	ps, err := s.Store.ListProjects(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	wsFilter := r.URL.Query().Get("workspace") // 비우면 모든 작업 공간(CLI 호환)
	out := []*store.Project{}
	for _, p := range ps {
		if wsFilter != "" && p.WorkspaceID != wsFilter {
			continue
		}
		if lv := s.visibleLevel(r, p.ID); lv > lvNone {
			out = append(out, maskProject(p, lv >= lvMember))
		}
	}
	writeJSON(w, 200, out)
}

// maskProject는 viewer에게 알림 webhook URL(보통 비밀 토큰이 들어 있다)을 숨긴다.
func maskProject(p *store.Project, secrets bool) *store.Project {
	if secrets || p.NotifyWebhookURL == nil {
		return p
	}
	cp := *p
	m := MaskedValue
	cp.NotifyWebhookURL = &m
	return &cp
}

func validProjectName(n string) bool {
	return n != "" && len(n) <= 64 && !strings.ContainsAny(n, "\n\r\t")
}

func (s *Server) createProject(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		WorkspaceID string `json:"workspace_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	in.Name = strings.TrimSpace(in.Name)
	if !validProjectName(in.Name) {
		writeErr(w, 400, "name is required (max 64 chars)")
		return
	}
	wsID, ok := s.createTargetWorkspace(w, r, in.WorkspaceID)
	if !ok {
		return
	}
	p := &store.Project{Name: in.Name, Description: in.Description, WorkspaceID: wsID}
	pr := principalFrom(r)
	// 프로젝트는 만든 사람의 GitHub 연결로 저장소에 접근한다(연결이 있으면). 나중에 PUT /api/projects/{pid}/github로 바꾼다.
	if pr != nil && pr.Via != viaEnvToken && s.Deploy != nil && s.Deploy.UserGitHubConnection(r.Context(), pr.User.ID) != nil {
		p.GitHubUserID = &pr.User.ID
	}
	var env *store.Environment
	err := s.Store.Tx(r.Context(), func(tx *store.Store) error {
		var err error
		env, err = tx.CreateProject(r.Context(), p)
		if err != nil {
			return err
		}
		if pr != nil && pr.Via != viaEnvToken {
			return tx.SetMember(r.Context(), p.ID, pr.User.ID, store.RoleAdmin)
		}
		return nil
	})
	if err != nil {
		s.storeErr(w, err)
		return
	}

	s.Deploy.EnsureEnvironment(r.Context(), env)
	s.Events.Emit(r.Context(), p.ID, nil, nil, "project.created", "프로젝트 "+p.Name+" 생성")
	writeJSON(w, 201, p)
}

func (s *Server) getProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	envs, err := s.Store.ListEnvironments(r.Context(), p.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	svcs, err := s.Store.ListServices(r.Context(), p.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	hold, err := s.Store.ProjectOwnerHold(r.Context(), p.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if hold != nil {
		hold.StoppedInstances = nil // 멤버에게는 정지 사실과 사유만 보인다
	}
	writeJSON(w, 200, struct {
		*store.Project
		Environments  []*store.Environment  `json:"environments"`
		Services      []*store.Service      `json:"services"`
		GitHubAccount *projectGitHubAccount `json:"github_account"`
		// GitHubBorrowed면 요청한 서버 owner가 멤버가 아닌 채로 다른 사람의 GitHub 연결로 배포하게 된다(githubBorrowed).
		GitHubBorrowed bool `json:"github_borrowed"`
		// OwnerHold가 있으면 서버 owner가 이 프로젝트(또는 그 작업 공간)를 정지했다.
		OwnerHold *store.OwnerHold `json:"owner_hold"`
		usageLimitFields
	}{maskProject(p, canSeeSecrets(r)), envs, svcs, s.projectGitHub(r.Context(), p), s.githubBorrowed(r.Context(), principalUserOf(r), p), hold, s.usageLimitFields(r.Context(), p.ID)})
}

func (s *Server) updateProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name                 *string   `json:"name"`
		Description          *string   `json:"description"`
		DefaultEnvironmentID *string   `json:"default_environment_id"`
		NotifyWebhookURL     **string  `json:"notify_webhook_url"`
		NotifyEvents         *[]string `json:"notify_events"`
		PREnvironments       *struct {
			Enabled           *bool    `json:"enabled"`
			BaseEnvironmentID **string `json:"base_environment_id"`
			AutoDelete        *bool    `json:"auto_delete"`
			Domain            *bool    `json:"domain"`
			Focused           *bool    `json:"focused"`
		} `json:"pr_environments"`
		PRFromForks *bool           `json:"pr_from_forks"`
		PRFromBots  *bool           `json:"pr_from_bots"`
		UsageAlert  json.RawMessage `json:"usage_alert"`
		UsageLimit  json.RawMessage `json:"usage_limit"`
	}
	if !decode(w, r, &in) {
		return
	}
	var newLimit *store.UsageLimit // usagelimit.go
	if len(in.UsageLimit) > 0 {
		if newLimit, err = parseUsageLimit(in.UsageLimit); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
	}
	if len(in.UsageAlert) > 0 {
		var ua *store.UsageAlert // null 또는 {}면 끈다
		if err := json.Unmarshal(in.UsageAlert, &ua); err != nil {
			writeErr(w, 400, "invalid usage_alert: "+err.Error())
			return
		}
		if ua != nil {
			for _, v := range []*float64{ua.CPUCoreHours, ua.MemoryGBHours, ua.EgressGB} {
				if v != nil && (*v <= 0 || *v > 1e9) {
					writeErr(w, 400, "usage_alert values must be positive numbers")
					return
				}
			}
			if ua.Empty() {
				ua = nil
			}
		}
		p.UsageAlert = ua
	}
	if pr := in.PREnvironments; pr != nil {
		if pr.Enabled != nil {
			p.PREnvironments.Enabled = *pr.Enabled
		}
		if pr.AutoDelete != nil {
			p.PREnvironments.AutoDelete = *pr.AutoDelete
		}
		if pr.Domain != nil {
			p.PREnvironments.Domain = *pr.Domain
		}
		if pr.Focused != nil {
			p.PREnvironments.Focused = *pr.Focused
		}
		if pr.BaseEnvironmentID != nil {
			id := *pr.BaseEnvironmentID
			if id != nil && *id == "" {
				id = nil
			}
			if id != nil {
				e, err := s.Store.GetEnvironment(r.Context(), *id)
				if err != nil || e.ProjectID != p.ID || e.Kind != store.EnvStandard {
					writeErr(w, 400, "pr_environments.base_environment_id must be a standard environment of this project")
					return
				}
			}
			p.PREnvironments.BaseEnvironmentID = id
		}
	}
	if in.PRFromForks != nil {
		p.PRFromForks = *in.PRFromForks
	}
	if in.PRFromBots != nil {
		p.PRFromBots = *in.PRFromBots
	}
	if in.Name != nil {
		n := strings.TrimSpace(*in.Name)
		if !validProjectName(n) {
			writeErr(w, 400, "invalid name")
			return
		}
		p.Name = n
	}
	if in.Description != nil {
		p.Description = *in.Description
	}
	if in.DefaultEnvironmentID != nil {
		e, err := s.Store.GetEnvironment(r.Context(), *in.DefaultEnvironmentID)
		if err != nil || e.ProjectID != p.ID {
			writeErr(w, 400, "default_environment_id must be an environment of this project")
			return
		}
		p.DefaultEnvironmentID = e.ID
	}
	if in.NotifyWebhookURL != nil {
		p.NotifyWebhookURL = *in.NotifyWebhookURL
		if p.NotifyWebhookURL != nil {
			u, err := url.Parse(*p.NotifyWebhookURL)
			if *p.NotifyWebhookURL == "" {
				p.NotifyWebhookURL = nil
			} else if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
				writeErr(w, 400, "notify_webhook_url must be an http(s) URL")
				return
			}
		}
	}
	if in.NotifyEvents != nil {
		for _, e := range *in.NotifyEvents {
			if !notifyEvents[e] {
				writeErr(w, 400, "unknown notify event: "+e)
				return
			}
		}
		p.NotifyEvents = *in.NotifyEvents
	}
	if err := s.Store.UpdateProject(r.Context(), p); err != nil {
		s.storeErr(w, err)
		return
	}
	if len(in.UsageLimit) > 0 {
		if err := s.applyUsageLimit(r.Context(), p, newLimit); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	writeJSON(w, 200, struct {
		*store.Project
		usageLimitFields
	}{p, s.usageLimitFields(r.Context(), p.ID)})
}

func (s *Server) deleteProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.destroyProject(context.WithoutCancel(r.Context()), p); err != nil {
		s.storeErr(w, err)
		return
	}
	w.WriteHeader(204)
}

// destroyProject는 프로젝트의 샌드박스, 서비스, 컨테이너, 볼륨을 정리하고 프로젝트를 지운다.
func (s *Server) destroyProject(ctx context.Context, p *store.Project) error {
	envs, err := s.Store.ListEnvironments(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, env := range envs {
		if err := s.cleanupSandboxEnvironment(ctx, env.ID); err != nil {
			return err
		}
	}
	return s.Deploy.DeleteProject(ctx, p)
}

// ---- environments ----

func (s *Server) createEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name     string  `json:"name"`
		CopyFrom *string `json:"copy_from"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !nameRe.MatchString(in.Name) {
		writeErr(w, 400, "name must be lowercase letters, digits and '-' (max 40)")
		return
	}
	var src *store.Environment
	if in.CopyFrom != nil && *in.CopyFrom != "" {
		src, err = s.Store.GetEnvironment(ctx, *in.CopyFrom)
		if err != nil || src.ProjectID != p.ID {
			writeErr(w, 400, "copy_from must be an environment of this project")
			return
		}
		// 제한된 환경의 설정·변수를 복사하려면 그 환경에 접근할 수 있어야 한다.
		if ok, err := s.canUseEnvironment(r, src); err != nil || !ok {
			s.envAccessErr(w, src, err)
			return
		}
	}
	env := &store.Environment{ProjectID: p.ID, Name: in.Name}
	if err := s.forkEnvironment(ctx, p, env, src, forkOpts{}); err != nil {
		var pe *populateError
		switch {
		case errors.Is(err, store.ErrResourceLimit):
			s.storeErr(w, err)
		case errors.As(err, &pe):
			s.Log.Error("populate environment", "err", pe.err)
			writeErr(w, 500, "create environment: "+pe.err.Error())
		case err == store.ErrConflict:
			writeErr(w, 409, "environment already exists: "+in.Name)
		default:
			s.storeErr(w, err)
		}
		return
	}
	msg := "환경 " + env.Name + " 생성"
	if src != nil {
		msg += " (" + src.Name + "에서 복사)"
	}
	s.Events.Emit(ctx, p.ID, &env.ID, nil, "environment.created", msg)
	writeJSON(w, 201, env)
}

// forkOpts는 환경을 채우는 방법이다.
type forkOpts struct {
	// PreviewDomains가 nil이면 copy_from 규칙(생성 도메인만 새로 만든다)을 따른다.
	// 아니면 PR 미리보기 규칙: true면 원본에 도메인이 하나라도 있던 서비스에 생성 도메인 하나를 붙이고, false면 도메인을 두지 않는다.
	PreviewDomains *bool
}

// populateError는 환경 행은 만들었지만 인스턴스를 채우다 실패한 경우다(환경은 지워진 상태).
type populateError struct{ err error }

func (e *populateError) Error() string { return "populate environment: " + e.err.Error() }
func (e *populateError) Unwrap() error { return e.err }

// forkEnvironment는 env(이름·종류를 채운 상태)를 만들고 src에서 서비스 인스턴스를 채운 뒤 네트워크를 준비한다.
// src가 nil이면 기본 환경에서 소스만 가져온다. 채우다 실패하면 만든 환경을 지우고 *populateError를 돌려준다.
func (s *Server) forkEnvironment(ctx context.Context, p *store.Project, env *store.Environment, src *store.Environment, o forkOpts) error {
	if env.Kind == store.EnvPreview && src != nil {
		baseID := src.ID
		env.BaseEnvironmentID = &baseID
	}
	if src != nil && src.Restricted {
		env.Restricted = true // 제한된 환경의 비밀을 복사한 환경(PR 미리보기 포함)도 제한된다. admin이 풀 수 있다
	}
	if err := s.Store.CreateEnvironment(ctx, env); err != nil {
		return err
	}
	if err := s.populateEnvironment(ctx, p, env, src, o); err != nil {
		s.deleteEnvResources(context.WithoutCancel(ctx), env)
		return &populateError{err}
	}
	s.ensureEnvResources(ctx, env)
	return nil
}

// ensureEnvResources / deleteEnvResources는 환경의 docker 리소스 작업이다(테스트에서는 envOps로 바꿔 끼운다).
func (s *Server) ensureEnvResources(ctx context.Context, env *store.Environment) error {
	if s.envOps != nil {
		return s.envOps.EnsureEnvironment(ctx, env)
	}
	return s.Deploy.EnsureEnvironment(ctx, env)
}

func (s *Server) deleteEnvResources(ctx context.Context, env *store.Environment) error {
	if err := s.cleanupSandboxEnvironment(ctx, env.ID); err != nil {
		return err
	}
	if s.envOps != nil {
		return s.envOps.DeleteEnvironment(ctx, env)
	}
	return s.Deploy.DeleteEnvironment(ctx, env)
}

// populateEnvironment는 새 환경에 모든 서비스의 인스턴스를 만든다.
// src가 있으면 설정·변수·공유 변수를 복사하고(볼륨 데이터는 복사하지 않음), 없으면 소스만 가져온 기본 설정으로 만든다.
func (s *Server) populateEnvironment(ctx context.Context, p *store.Project, env *store.Environment, src *store.Environment, o forkOpts) error {
	policies, err := s.accessPolicies(ctx) // 공개 네트워킹 guardrail이 켜져 있으면 도메인·TCP 프록시를 자동으로 만들지 않는다
	if err != nil {
		return err
	}
	svcs, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		return err
	}
	from := src
	if from == nil {
		from, err = s.Store.GetEnvironment(ctx, p.DefaultEnvironmentID)
		if err != nil {
			return err
		}
	}
	if src != nil {
		shared, err := s.Store.GetSharedVars(ctx, src.ID)
		if err != nil {
			return err
		}

		shared = withoutSealed(shared, s.sharedSealed(ctx, src.ID, shared))
		if err := s.Store.SetSharedVars(ctx, env.ID, shared); err != nil {
			return err
		}
	}
	for _, svc := range svcs {
		orig, err := s.Store.GetInstanceFor(ctx, svc.ID, from.ID)
		if err != nil && err != store.ErrNotFound {
			return err
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID}
		var mounts []string
		if orig != nil {
			vols, _ := s.Store.ListVolumesByInstance(ctx, orig.ID)
			for _, v := range vols {
				mounts = append(mounts, v.MountPath)
			}
		}
		if src != nil && orig != nil {
			in.Spec = orig.Spec
			in.Variables = withoutSealed(orig.Variables, s.instanceSealed(ctx, orig))
			in.Canvas = orig.Canvas
			in.Spec.Domains = nil
			switch {
			case o.PreviewDomains == nil && !policies.RestrictGeneratedDomains:
				for _, d := range orig.Domains {
					if d.Kind == "generated" {
						if host, err := s.generateDomain(ctx, svc.Name, env.Name); err == nil {
							in.Spec.Domains = append(in.Spec.Domains, store.Domain{Host: host, Kind: "generated", TargetPort: d.TargetPort})
						}
					}
				}
			case o.PreviewDomains != nil && *o.PreviewDomains && len(orig.Domains) > 0 && !policies.RestrictGeneratedDomains:
				// 미리보기 환경: 커스텀 도메인은 운영 호스트라 복사하지 않고, 생성 도메인 하나로 바꾼다(KEEL_BASE_DOMAIN이 없으면 없음).
				if host, err := s.generateDomain(ctx, svc.Name, env.Name); err == nil {
					in.Spec.Domains = []store.Domain{{Host: host, Kind: "generated", TargetPort: orig.Domains[0].TargetPort}}
				}
			}
			if orig.TCPProxy != nil && !policies.RestrictTCPProxy {
				if port, err := s.Deploy.AllocateTCPPort(ctx); err == nil {
					in.TCPProxy = &store.TCPProxy{ContainerPort: orig.TCPProxy.ContainerPort, PublicPort: port}
				} else {
					in.TCPProxy = nil
				}
			} else {
				in.TCPProxy = nil
			}
		} else {
			if orig != nil {
				in.RepoURL, in.Branch, in.RootDir, in.Image = orig.RepoURL, orig.Branch, orig.RootDir, orig.Image
				in.Builder, in.DockerfilePath, in.Canvas = orig.Builder, orig.DockerfilePath, orig.Canvas
			}
			if svc.DatabaseKind != nil {
				def, _ := catalog.DatabaseService(*svc.DatabaseKind)
				def.ApplyToInstance(in)
				mounts = def.Volumes
			} else {
				mounts = nil
			}
		}
		if err := s.Store.CreateInstance(ctx, in); err != nil {
			return err
		}
		if err := s.copyFunctionCode(ctx, svc, orig, in); err != nil {
			return err
		}
		for _, mp := range mounts {
			v := &store.Volume{ProjectID: p.ID, EnvironmentID: env.ID, InstanceID: &in.ID, Name: volumeName(svc.Name, mp), MountPath: mp}
			if err := s.Store.CreateVolume(ctx, v); err != nil {
				return err
			}
		}
	}
	return nil
}

func copyMap(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func volumeName(svc, mount string) string {
	base := strings.Trim(strings.ReplaceAll(mount, "/", "-"), "-")
	if base == "" {
		base = "data"
	}
	if i := strings.LastIndex(base, "-"); i >= 0 {
		base = base[i+1:]
	}
	return svc + "-" + base
}

func (s *Server) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	env, err := s.Store.GetEnvironment(r.Context(), r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		Name       *string `json:"name"`
		Restricted *bool   `json:"restricted"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Name == nil && in.Restricted == nil {
		writeErr(w, 400, "name or restricted is required")
		return
	}
	// 제한(Environment RBAC)은 프로젝트 admin만 바꾼다. 아무것도 쓰기 전에 검사한다.
	changeRestriction := in.Restricted != nil && *in.Restricted != env.Restricted
	if changeRestriction && grantLevel(r) < lvAdmin {
		writeErr(w, 403, "환경 제한은 프로젝트 admin만 바꿀 수 있습니다")
		return
	}
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if !nameRe.MatchString(name) {
			writeErr(w, 400, "name must be lowercase letters, digits and '-' (max 40)")
			return
		}
		if err := s.Store.RenameEnvironment(r.Context(), env.ID, name); err != nil {
			if err == store.ErrConflict {
				writeErr(w, 409, "environment already exists: "+name)
				return
			}
			s.storeErr(w, err)
			return
		}
		env.Name = name
	}
	if changeRestriction {
		if err := s.Store.SetEnvironmentRestricted(r.Context(), env.ID, *in.Restricted); err != nil {
			s.storeErr(w, err)
			return
		}
		env.Restricted = *in.Restricted
		msg := "환경 " + env.Name + "을 제한했습니다(프로젝트 admin만 접근)"
		if !env.Restricted {
			msg = "환경 " + env.Name + "의 제한을 풀었습니다"
		}
		s.audit(r, "environment.restricted", env.ID, msg)
	}
	writeJSON(w, 200, env)
}

func (s *Server) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, err := s.Store.GetEnvironment(ctx, r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	envs, err := s.Store.ListEnvironments(ctx, env.ProjectID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if len(envs) <= 1 {
		writeErr(w, 409, "the last environment cannot be deleted")
		return
	}
	p, err := s.Store.GetProject(ctx, env.ProjectID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.deleteEnvResources(context.WithoutCancel(ctx), env); err != nil {
		s.storeErr(w, err)
		return
	}
	if p.PREnvironments.BaseEnvironmentID != nil && *p.PREnvironments.BaseEnvironmentID == env.ID {
		p.PREnvironments.BaseEnvironmentID = nil // 기본 환경으로 돌아간다
		s.Store.UpdateProject(ctx, p)
	}
	if p.DefaultEnvironmentID == env.ID {
		for _, e := range envs {
			if e.ID != env.ID {
				p.DefaultEnvironmentID = e.ID
				s.Store.UpdateProject(ctx, p)
				break
			}
		}
	}
	s.Events.Emit(ctx, p.ID, nil, nil, "environment.deleted", "환경 "+env.Name+" 삭제")
	w.WriteHeader(204)
}

func (s *Server) envAndProject(w http.ResponseWriter, r *http.Request) (*store.Environment, *store.Project, bool) {
	env, err := s.Store.GetEnvironment(r.Context(), r.PathValue("eid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, nil, false
	}
	p, err := s.Store.GetProject(r.Context(), env.ProjectID)
	if err != nil {
		s.storeErr(w, err)
		return nil, nil, false
	}
	return env, p, true
}

func (s *Server) canvas(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	svcs, err := s.Store.ListServices(ctx, p.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type item struct {
		*store.Service
		Instance         *instanceView     `json:"instance"`
		LatestDeployment *store.Deployment `json:"latest_deployment"`
		Volumes          []*store.Volume   `json:"volumes"`
	}
	items := []item{}
	var latest []*store.Deployment
	for _, svc := range svcs {
		in, err := s.Store.GetInstanceFor(ctx, svc.ID, env.ID)
		if err != nil {
			continue
		}
		it := item{Service: svc, Instance: s.viewInstance(r, svc, env, in), Volumes: []*store.Volume{}}
		if d, err := s.Store.LatestDeployment(ctx, in.ID); err == nil {
			it.LatestDeployment = d
			latest = append(latest, d)
		}
		if vols, err := s.Store.ListVolumesByInstance(ctx, in.ID); err == nil {
			it.Volumes = s.fillVolumes(vols)
		}
		items = append(items, it)
	}
	s.Deploy.FillReplicas(ctx, latest...)
	groups, err := s.liveGroups(ctx, env)
	if err != nil {
		groups = []*store.CanvasGroup{}
	}
	writeJSON(w, 200, map[string]any{"environment": env, "project": maskProject(p, canSeeSecrets(r)), "services": items, "stacks": s.stackViews(ctx, p, env), "groups": groups})
}

func (s *Server) fillVolumes(vols []*store.Volume) []*store.Volume {
	for _, v := range vols {
		if s.Metrics != nil {
			v.SizeBytes = s.Metrics.VolumeSize(v.DockerName)
		}
	}
	return vols
}

func validateVars(m map[string]string) error {
	for k, v := range m {
		if !envKeyRe.MatchString(k) || len(k) > 256 {
			return fmt.Errorf("invalid variable name: %s", k)
		}
		if len(v) > 64*1024 {
			return fmt.Errorf("variable %s is too large", k)
		}
	}
	return nil
}

func (s *Server) getSharedVars(w http.ResponseWriter, r *http.Request) {
	env, _, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	v, err := s.Store.GetSharedVars(r.Context(), env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	sealed := s.sharedSealed(r.Context(), env.ID, v)
	if !canSeeSecrets(r) {
		v = maskedVars(v)
	}
	v = hideSealed(v, sealed)
	// detail=1이면 {values, sealed}로 봉인된 이름도 준다(기본은 예전처럼 문자열 맵).
	if q := r.URL.Query().Get("detail"); q == "1" || q == "true" {
		writeJSON(w, 200, sharedVarsView{Values: v, Sealed: store.SortedKeys(sealed)})
		return
	}
	writeJSON(w, 200, v)
}

func (s *Server) putSharedVars(w http.ResponseWriter, r *http.Request) {
	env, p, ok := s.envAndProject(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	if !decode(w, r, &raw) {
		return
	}
	body, err := parseSharedVarsBody(raw)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	vars := body.Values
	old, err := s.Store.GetSharedVars(r.Context(), env.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	sealed := s.sharedSealed(r.Context(), env.ID, old)
	keepSealed(old, vars, sealed, false)
	if err := sealRequest(body.Sealed, vars); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := validateVars(vars); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if err := s.Store.SealVariables(r.Context(), store.SharedSealOwner(env.ID), vars, resealKeys(sealed, body.Sealed)); err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.SetSharedVars(r.Context(), env.ID, vars); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Events.Emit(r.Context(), p.ID, &env.ID, nil, "variables.updated", env.Name+" 환경의 공유 변수를 변경했습니다")
	w.WriteHeader(204)
}

// ---- events ----

func (s *Server) listEvents(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	evs, err := s.Store.ListEvents(r.Context(), p.ID, r.URL.Query().Get("environment_id"), limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, evs)
}

func (s *Server) streamEvents(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	ch, cancel := s.Events.Subscribe(p.ID)
	defer cancel()
	e, ok := newSSE(w)
	if !ok {
		return
	}
	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			fmt.Fprint(w, ": ping\n\n")
			e.f.Flush()
		case ev, ok := <-ch:
			if !ok {
				return
			}
			e.sendJSON("event", ev)
			e.f.Flush()
		}
	}
}

// ---- private registries ----

type registryView struct {
	Host        string `json:"host"`
	Username    string `json:"username"`
	HasPassword bool   `json:"has_password"`
}

var registryHostRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9.-]*[a-zA-Z0-9])?(:[0-9]{1,5})?$`)

func (s *Server) getRegistries(w http.ResponseWriter, r *http.Request) {
	regs, err := s.Store.GetRegistries(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []registryView{}
	for _, g := range regs {
		out = append(out, registryView{Host: g.Host, Username: g.Username, HasPassword: g.Password != ""})
	}
	writeJSON(w, 200, out)
}

func (s *Server) putRegistries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := r.PathValue("pid")
	old, err := s.Store.GetRegistries(ctx, pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in []struct {
		Host     string  `json:"host"`
		Username string  `json:"username"`
		Password *string `json:"password"`
	}
	if !decode(w, r, &in) {
		return
	}
	seen := map[string]bool{}
	regs := []store.Registry{}
	for _, g := range in {
		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(g.Host), "https://"), "http://"), "/")
		if !registryHostRe.MatchString(host) {
			writeErr(w, 400, "invalid registry host: "+g.Host)
			return
		}
		if seen[host] {
			writeErr(w, 400, "duplicate registry host: "+host)
			return
		}
		seen[host] = true
		if strings.TrimSpace(g.Username) == "" {
			writeErr(w, 400, "username is required for "+host)
			return
		}
		reg := store.Registry{Host: host, Username: strings.TrimSpace(g.Username)}
		if g.Password != nil {
			reg.Password = *g.Password
		} else {
			for _, o := range old {
				if o.Host == host {
					reg.Password = o.Password
				}
			}
		}
		if reg.Password == "" {
			writeErr(w, 400, "password is required for new registry "+host)
			return
		}
		regs = append(regs, reg)
	}
	if err := s.Store.SetRegistries(ctx, pid, regs); err != nil {
		s.storeErr(w, err)
		return
	}
	out := []registryView{}
	for _, g := range regs {
		out = append(out, registryView{Host: g.Host, Username: g.Username, HasPassword: true})
	}
	writeJSON(w, 200, out)
}
