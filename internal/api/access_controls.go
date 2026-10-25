package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/turin-dev/aven/internal/guardrails"
	"github.com/turin-dev/aven/internal/store"
)

//
// GET   /api/access-controls                 작업 공간 admin: 접근 그룹 적용 여부 + guardrail 정책 전체
// PATCH /api/access-controls                 작업 공간 admin(토큰 불가)
// GET   /api/access-controls/policies        모든 사용자: 대시보드 배너·버튼 비활성화용 정책 요약(승인된 조직 목록 제외)
// GET/POST /api/access-groups, PUT/DELETE /api/access-groups/{gid}   작업 공간 admin(토큰 불가)
// GET   /api/projects/{pid}/access-groups    프로젝트 viewer: 프로젝트가 속한 그룹과 그룹으로 접근하는 사람
// PUT   /api/projects/{pid}/access-groups    작업 공간 admin(토큰 불가): 프로젝트가 속한 그룹 바꾸기 {group_ids}
//
// 권한 규칙(authz.go와 함께):
//   - 접근 그룹은 켜져 있을 때만 그룹의 역할을 그룹 프로젝트에 준다. 직접 부여한 역할과 겹치면 높은 쪽(권한을 줄이지 않는다).
//     그룹의 admin 역할은 그 프로젝트의 admin일 뿐 작업 공간 admin이 아니고 guardrail도 우회하지 않는다.
//   - 제한된 환경(restricted)은 프로젝트 admin(그룹 admin, 작업 공간 admin 포함)만 리소스에 접근한다. 다른 사람은
//     환경이 있다는 것만 보고(프로젝트의 environments 목록) 그 환경의 라우트는 403이다. git push 자동 배포는 그대로 된다.
//   - 생성 도메인·TCP 프록시 제한은 작업 공간 admin이 아닌 사용자에게, 자동 생성(템플릿, 새 환경)은 모두에게 적용된다.
//   - 배포 출처 제한은 모두(admin 포함)에게 적용된다(internal/guardrails).

var githubOrgName = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,37}[A-Za-z0-9])?$`)

var (
	errGeneratedDomainRestricted = errors.New("작업 공간 정책으로 생성 도메인은 작업 공간 admin만 만들 수 있습니다 (custom domain은 사용할 수 있습니다)")
	errTCPProxyRestricted        = errors.New("작업 공간 정책으로 TCP 프록시는 작업 공간 admin만 만들 수 있습니다")
)

// instanceAdminRequest는 요청 주체가 인스턴스 admin(또는 owner)인지다. 작업 공간/프로젝트/환경 토큰은 제외한다.
// 주체가 없으면(핸들러를 직접 부르는 테스트) 제한이 없다.
func (s *Server) instanceAdminRequest(r *http.Request) bool {
	p := principalFrom(r)
	return p == nil || s.instanceLevel(p) >= lvAdmin
}

// accessPolicies는 저장된 정책이다(Store가 없는 테스트 서버는 모두 꺼짐).
func (s *Server) accessPolicies(ctx context.Context) (*store.AccessControlSettings, error) {
	if s.Store == nil {
		return &store.AccessControlSettings{ApprovedGitHubOrgs: []string{}}, nil
	}
	return s.Store.AccessControlSettings(ctx)
}

// isGeneratedHost는 host가 KEEL_BASE_DOMAIN 아래(생성 도메인 영역)인지다.
func (s *Server) isGeneratedHost(host string) bool {
	if s.Deploy == nil || s.Deploy.Cfg.BaseDomain == "" {
		return false
	}
	host, base := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(host), ".")), strings.ToLower(strings.Trim(s.Deploy.Cfg.BaseDomain, "."))
	return base != "" && (host == base || strings.HasSuffix(host, "."+base))
}

// checkGeneratedDomainPolicy는 생성 도메인 하나를 새로 만드는 요청을 검사한다(generated면 host와 무관하게).
func (s *Server) checkGeneratedDomainPolicy(ctx context.Context, r *http.Request, host string, generated bool) error {
	p, err := s.accessPolicies(ctx)
	if err != nil {
		return err
	}
	if p.RestrictGeneratedDomains && (generated || s.isGeneratedHost(host)) && !s.instanceAdminRequest(r) {
		return errGeneratedDomainRestricted
	}
	return nil
}

// checkNewGeneratedDomains는 도메인 목록 변경(PATCH instance)에서 새로 생긴 생성 도메인을 검사한다(기존 것은 유지 가능).
func (s *Server) checkNewGeneratedDomains(ctx context.Context, r *http.Request, before, after []store.Domain) error {
	p, err := s.accessPolicies(ctx)
	if err != nil {
		return err
	}
	if !p.RestrictGeneratedDomains || s.instanceAdminRequest(r) {
		return nil
	}
	existing := make(map[string]bool, len(before))
	for _, d := range before {
		existing[strings.ToLower(d.Host)] = true
	}
	for _, d := range after {
		if (d.Kind == "generated" || s.isGeneratedHost(d.Host)) && !existing[strings.ToLower(d.Host)] {
			return errGeneratedDomainRestricted
		}
	}
	return nil
}

// checkTCPProxyPolicy는 TCP 프록시를 새로 만드는 요청을 검사한다.
func (s *Server) checkTCPProxyPolicy(ctx context.Context, r *http.Request) error {
	p, err := s.accessPolicies(ctx)
	if err != nil {
		return err
	}
	if p.RestrictTCPProxy && !s.instanceAdminRequest(r) {
		return errTCPProxyRestricted
	}
	return nil
}

// checkSourcePolicy는 배포 출처 guardrail이다(admin 포함 모두).
func (s *Server) checkSourcePolicy(ctx context.Context, src guardrails.Source) error {
	p, err := s.accessPolicies(ctx)
	if err != nil {
		return err
	}
	return guardrails.Check(p, src)
}

// policyErr는 guardrail 오류면 403, 아니면 storeErr로 응답한다.
func (s *Server) policyErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errGeneratedDomainRestricted) || errors.Is(err, errTCPProxyRestricted) || errors.Is(err, store.ErrDeploymentSourceRestricted) {
		writeErr(w, 403, err.Error())
		return
	}
	s.storeErr(w, err)
}

// canUseEnvironment는 요청 주체가 환경 env의 리소스를 쓸 수 있는지다(본문으로 다른 환경을 가리키는 요청: copy_from, sync from 등).
// 범위 밖 토큰과 제한된 환경(프로젝트 admin이 아니면)은 거부한다.
func (s *Server) canUseEnvironment(r *http.Request, env *store.Environment) (bool, error) {
	p := principalFrom(r)
	if p == nil {
		return true, nil
	}
	lv, err := s.projectLevel(r.Context(), p, env.ProjectID, env.ID)
	return lv > lvNone, err
}

// envAccessErr는 canUseEnvironment가 거부한 요청의 응답이다(제한된 환경이면 403, 범위 밖이면 404).
func (s *Server) envAccessErr(w http.ResponseWriter, env *store.Environment, err error) {
	switch {
	case err != nil:
		s.storeErr(w, err)
	case env.Restricted:
		writeErr(w, 403, errRestrictedEnv)
	default:
		writeErr(w, 404, "not found")
	}
}

func sameStr(a, b *string) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

const errRestrictedEnv = "제한된 환경입니다: 프로젝트 admin만 이 환경의 리소스에 접근할 수 있습니다"

// ---- 설정 ----

func (s *Server) getAccessControlSettings(w http.ResponseWriter, r *http.Request) {
	p, err := s.accessPolicies(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}

// getAccessPolicies는 모든 사용자에게 보이는 정책 요약이다(UI가 막힌 버튼을 미리 끄고 배너를 보인다).
func (s *Server) getAccessPolicies(w http.ResponseWriter, r *http.Request) {
	p, err := s.accessPolicies(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{
		"access_groups_enabled":       p.AccessGroupsEnabled,
		"restrict_generated_domains":  p.RestrictGeneratedDomains,
		"restrict_tcp_proxy":          p.RestrictTCPProxy,
		"restrict_deployment_sources": p.RestrictDeploymentSources,
		"instance_admin":              s.instanceAdminRequest(r),
		"workspace_admin":             s.instanceAdminRequest(r), // deprecated alias
	})
}

func (s *Server) patchAccessControlSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	cur, err := s.accessPolicies(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		AccessGroupsEnabled       *bool     `json:"access_groups_enabled"`
		RestrictGeneratedDomains  *bool     `json:"restrict_generated_domains"`
		RestrictTCPProxy          *bool     `json:"restrict_tcp_proxy"`
		RestrictDeploymentSources *bool     `json:"restrict_deployment_sources"`
		ApprovedGitHubOrgs        *[]string `json:"approved_github_orgs"`
	}
	if !decode(w, r, &in) {
		return
	}
	next := *cur
	next.ApprovedGitHubOrgs = append([]string{}, cur.ApprovedGitHubOrgs...)
	set := func(dst *bool, v *bool) {
		if v != nil {
			*dst = *v
		}
	}
	if in.AccessGroupsEnabled != nil {
		lv, err := s.workspaceAccess(ctx, principalFrom(r), store.DefaultWorkspaceID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if lv < lvAdmin {
			writeErr(w, 403, "기본 작업 공간의 접근 그룹은 그 공간의 admin만 관리할 수 있습니다")
			return
		}
	}
	set(&next.AccessGroupsEnabled, in.AccessGroupsEnabled)
	set(&next.RestrictGeneratedDomains, in.RestrictGeneratedDomains)
	set(&next.RestrictTCPProxy, in.RestrictTCPProxy)
	set(&next.RestrictDeploymentSources, in.RestrictDeploymentSources)
	if in.ApprovedGitHubOrgs != nil {
		orgs, ok := normalizeGitHubOrgs(*in.ApprovedGitHubOrgs)
		if !ok {
			writeErr(w, 400, "approved_github_orgs must contain at most 100 valid GitHub organization names")
			return
		}
		next.ApprovedGitHubOrgs = orgs
	}
	if next.RestrictDeploymentSources && len(next.ApprovedGitHubOrgs) == 0 {
		writeErr(w, 400, "승인된 GitHub 조직이 하나 이상 있어야 배포 출처 제한을 켤 수 있습니다 (켜져 있는 동안 마지막 조직은 지울 수 없습니다)")
		return
	}
	if err := s.Store.SetAccessControlSettings(ctx, &next); err != nil {
		s.storeErr(w, err)
		return
	}
	onOff := func(v bool) string {
		if v {
			return "켰습니다"
		}
		return "껐습니다"
	}
	for _, c := range []struct {
		kind, label string
		before, now bool
	}{
		{"access_groups.enforcement", "접근 그룹 적용을", cur.AccessGroupsEnabled, next.AccessGroupsEnabled},
		{"guardrail.generated_domains", "생성 도메인 제한을", cur.RestrictGeneratedDomains, next.RestrictGeneratedDomains},
		{"guardrail.tcp_proxy", "TCP 프록시 제한을", cur.RestrictTCPProxy, next.RestrictTCPProxy},
		{"guardrail.deployment_sources", "배포 출처 제한을", cur.RestrictDeploymentSources, next.RestrictDeploymentSources},
	} {
		if c.before != c.now {
			s.audit(r, c.kind, "workspace", c.label+" "+onOff(c.now))
		}
	}
	if strings.Join(cur.ApprovedGitHubOrgs, ",") != strings.Join(next.ApprovedGitHubOrgs, ",") {
		s.audit(r, "guardrail.approved_sources", "workspace", "승인된 GitHub 조직을 변경했습니다: "+strings.Join(next.ApprovedGitHubOrgs, ", "))
	}
	writeJSON(w, 200, &next)
}

func normalizeGitHubOrgs(orgs []string) ([]string, bool) {
	if len(orgs) > 100 {
		return nil, false
	}
	seen := make(map[string]bool, len(orgs))
	out := make([]string, 0, len(orgs))
	for _, org := range orgs {
		org = strings.TrimSpace(org)
		if !githubOrgName.MatchString(org) {
			return nil, false
		}
		key := strings.ToLower(org)
		if !seen[key] {
			seen[key] = true
			out = append(out, key)
		}
	}
	return out, true
}

// ---- 접근 그룹 ----

func (s *Server) listAccessGroups(w http.ResponseWriter, r *http.Request) {
	groups, err := s.Store.ListAccessGroups(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	wsID, err := s.requestWorkspace(r)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []*store.AccessGroup{}
	for _, g := range groups {
		if g.WorkspaceID == wsID {
			out = append(out, g)
		}
	}
	writeJSON(w, 200, out)
}

// validateGroupRefs는 user_ids/project_ids가 모두 있는지 본다(400 메시지, 없으면 "").
func (s *Server) validateGroupRefs(ctx context.Context, g *store.AccessGroup) (string, error) {
	users, err := s.Store.ListWorkspaceMembers(ctx, g.WorkspaceID)
	if err != nil {
		return "", err
	}
	known := map[string]bool{}
	for _, u := range users {
		known[u.UserID] = true
	}
	for _, id := range g.UserIDs {
		if !known[id] {
			return "unknown user in user_ids: " + id, nil
		}
	}
	projects, err := s.Store.ListProjects(ctx)
	if err != nil {
		return "", err
	}
	known = map[string]bool{}
	for _, p := range projects {
		// 그룹은 같은 작업 공간의 프로젝트만 포함할 수 있다.
		if p.WorkspaceID == g.WorkspaceID {
			known[p.ID] = true
		}
	}
	for _, id := range g.ProjectIDs {
		if !known[id] {
			return "unknown project in project_ids: " + id, nil
		}
	}
	return "", nil
}

func (s *Server) saveAccessGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var in struct {
		Name       string   `json:"name"`
		Role       string   `json:"role"`
		UserIDs    []string `json:"user_ids"`
		ProjectIDs []string `json:"project_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	wsID, err := s.requestWorkspace(r)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	g := &store.AccessGroup{WorkspaceID: wsID, Name: strings.TrimSpace(in.Name), Role: in.Role, UserIDs: in.UserIDs, ProjectIDs: in.ProjectIDs}
	if !validName(g.Name) {
		writeErr(w, 400, "name is required (max 100 chars)")
		return
	}
	if !store.ValidProjectRole(g.Role) {
		writeErr(w, 400, "role must be admin, member or viewer")
		return
	}
	update := r.Method == http.MethodPut
	var before *store.AccessGroup
	if update {
		var err error
		if before, err = s.Store.GetAccessGroup(ctx, r.PathValue("gid")); err != nil {
			s.storeErr(w, err)
			return
		}
		if before.WorkspaceID != wsID {
			writeErr(w, 404, "not found")
			return
		}
		g.ID, g.CreatedAt = before.ID, before.CreatedAt
	}
	if msg, err := s.validateGroupRefs(ctx, g); err != nil || msg != "" {
		if err != nil {
			s.storeErr(w, err)
		} else {
			writeErr(w, 400, msg)
		}
		return
	}
	if err := s.Store.SaveAccessGroup(ctx, g); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "an access group with this name already exists")
			return
		}
		s.storeErr(w, err)
		return
	}
	if !update {
		s.audit(r, "access_group.created", g.ID, fmt.Sprintf("접근 그룹 %s(%s)을 만들었습니다: 사용자 %d명, 프로젝트 %d개", g.Name, g.Role, len(g.UserIDs), len(g.ProjectIDs)))
		writeJSON(w, 201, g)
		return
	}
	if before.Name != g.Name || before.Role != g.Role {
		s.audit(r, "access_group.updated", g.ID, fmt.Sprintf("접근 그룹 %s → %s(%s)", before.Name, g.Name, g.Role))
	}
	if add, rm := diffIDs(before.UserIDs, g.UserIDs); len(add)+len(rm) > 0 {
		s.audit(r, "access_group.members_changed", g.ID, fmt.Sprintf("접근 그룹 %s 사용자: +%d −%d", g.Name, len(add), len(rm)))
	}
	if add, rm := diffIDs(before.ProjectIDs, g.ProjectIDs); len(add)+len(rm) > 0 {
		s.audit(r, "access_group.projects_changed", g.ID, fmt.Sprintf("접근 그룹 %s 프로젝트: +%d −%d", g.Name, len(add), len(rm)))
	}
	writeJSON(w, 200, g)
}

// diffIDs는 before → after에서 더해진 것과 빠진 것이다.
func diffIDs(before, after []string) (add, rm []string) {
	in := map[string]bool{}
	for _, id := range before {
		in[id] = true
	}
	out := map[string]bool{}
	for _, id := range after {
		out[id] = true
		if !in[id] {
			add = append(add, id)
		}
	}
	for _, id := range before {
		if !out[id] {
			rm = append(rm, id)
		}
	}
	return add, rm
}

func (s *Server) deleteAccessGroup(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	g, err := s.Store.GetAccessGroup(ctx, r.PathValue("gid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	wsID, err := s.requestWorkspace(r)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if g.WorkspaceID != wsID {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.Store.DeleteAccessGroup(ctx, g.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "access_group.deleted", g.ID, "접근 그룹 "+g.Name+"을 삭제했습니다")
	w.WriteHeader(204)
}

// ---- 프로젝트의 그룹 ----

type projectAccessGroup struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Role      string `json:"role"`
	UserCount int    `json:"user_count"`
}

// getProjectAccessGroups는 프로젝트가 속한 그룹과, 그룹으로 접근하는 사람(group_grants)이다.
// 그룹이 꺼져 있으면 group_grants는 권한을 주지 않는다(enabled=false로 표시).
func (s *Server) getProjectAccessGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := r.PathValue("pid")
	prj, err := s.Store.GetProject(ctx, pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	enabled, err := s.Store.WorkspaceAccessGroupsEnabled(ctx, prj.WorkspaceID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	groups, err := s.Store.ListAccessGroups(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := []projectAccessGroup{}
	for _, g := range groups {
		if g.WorkspaceID != prj.WorkspaceID {
			continue
		}
		for _, id := range g.ProjectIDs {
			if id == pid {
				out = append(out, projectAccessGroup{g.ID, g.Name, g.Role, len(g.UserIDs)})
				break
			}
		}
	}
	grants, err := s.Store.ProjectGroupGrants(ctx, pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"enabled": enabled, "groups": out, "group_grants": grants})
}

func (s *Server) putProjectAccessGroups(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pr, err := s.Store.GetProject(ctx, r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		GroupIDs []string `json:"group_ids"`
	}
	if !decode(w, r, &in) {
		return
	}
	groups, err := s.Store.ListAccessGroups(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	known := map[string]string{}
	for _, g := range groups {
		if g.WorkspaceID == pr.WorkspaceID {
			known[g.ID] = g.Name
		}
	}
	names := []string{}
	for _, id := range in.GroupIDs {
		name, ok := known[id]
		if !ok {
			writeErr(w, 400, "unknown access group: "+id)
			return
		}
		names = append(names, name)
	}
	if err := s.Store.SetProjectAccessGroups(ctx, pr.ID, in.GroupIDs); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "access_group.projects_changed", pr.ID, fmt.Sprintf("프로젝트 %s의 접근 그룹: %s", pr.Name, strings.Join(names, ", ")))
	s.getProjectAccessGroups(w, r)
}

func (s *Server) getWorkspaceGroupSettings(w http.ResponseWriter, r *http.Request) {
	wsID, err := s.requestWorkspace(r)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	enabled, err := s.Store.WorkspaceAccessGroupsEnabled(r.Context(), wsID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"access_groups_enabled": enabled})
}
func (s *Server) patchWorkspaceGroupSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled bool `json:"access_groups_enabled"`
	}
	if !decode(w, r, &in) {
		return
	}
	wsID, err := s.requestWorkspace(r)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.SetWorkspaceAccessGroupsEnabled(r.Context(), wsID, in.Enabled); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "access_groups.enforcement", wsID, "작업 공간 접근 그룹 적용 설정을 변경했습니다")
	s.getWorkspaceGroupSettings(w, r)
}
