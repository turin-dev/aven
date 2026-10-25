package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// ---- 작업 공간(사람별 분리) ----
//
// GET    /api/workspaces                         → 내가 볼 수 있는 작업 공간(기본 + 개인 + 멤버인 팀). 개인 작업 공간은 없으면 만든다.
// POST   /api/workspaces {name}                  → 팀 작업 공간을 만든다(만든 사람이 owner)
// PATCH  /api/workspaces/{wsid} {name}           → 이름 변경(작업 공간 admin)
// DELETE /api/workspaces/{wsid}                  → 빈 팀 작업 공간 삭제(owner). 기본·개인 작업 공간은 지울 수 없다
// GET    /api/workspaces/{wsid}/members          → 해당 공간의 멤버 목록(모든 종류의 공간에서 동일)
// POST   /api/workspaces/{wsid}/members {email, role}, PATCH/DELETE /api/workspaces/{wsid}/members/{uid}
// PUT    /api/projects/{pid}/workspace {workspace_id} → 프로젝트 이동(프로젝트 admin + 대상 작업 공간 member 이상)
// PUT    /api/projects/{pid}/github {user_id|null}    → 프로젝트가 쓸 GitHub 연결(프로젝트 admin, 자기 연결 또는 해제만)

// workspaceAccess는 주체가 작업 공간 wsID에서 가진 권한 수준이다(멤버가 아니면 lvNone). 토큰과 OAuth scope는 이 멤버십을 상한으로 삼는다.
// KEEL_API_TOKEN(운영 자동화)은 모든 작업 공간에서 owner다.
func (s *Server) workspaceAccess(ctx context.Context, p *principal, wsID string) (int, error) {
	if p.Via == viaEnvToken {
		return lvOwner, nil
	}
	role, err := s.Store.WorkspaceRole(ctx, wsID, p.User)
	if err != nil {
		return lvNone, err
	}

	lv := roleLevel(role)
	if s.instanceOwner(p.User) {
		// 인스턴스 owner는 모든 작업 공간의 owner다(토큰·OAuth는 아래에서 좁혀진다).
		if _, err := s.Store.GetWorkspace(ctx, wsID); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return lvNone, nil
			}
			return lvNone, err
		}
		lv = lvOwner
	}
	if p.OAuth != nil {
		return min(lv, oauthScopeLevel(p.OAuth.Scopes, "workspace:")), nil
	}
	if t := p.Token; t != nil {
		if t.Scope != store.ScopeWorkspace || t.WorkspaceID == nil || *t.WorkspaceID != wsID {
			return lvNone, nil
		}
		lv = min(lv, roleLevel(t.Role))
	}
	return lv, nil
}

type workspaceView struct {
	*store.Workspace
	Role string `json:"role"`
}

// visibleWorkspaces는 주체가 멤버인 작업 공간과 역할이다(개인 작업 공간을 처음 보면 만든다).
func (s *Server) visibleWorkspaces(ctx context.Context, p *principal) ([]workspaceView, error) {
	if p.Via == viaEnvToken {
		all, err := s.Store.ListWorkspaces(ctx)
		if err != nil {
			return nil, err
		}
		out := make([]workspaceView, 0, len(all))
		for _, w := range all {
			out = append(out, workspaceView{w, store.RoleOwner})
		}
		return out, nil
	}
	if p.Token == nil && p.OAuth == nil {
		if _, err := s.Store.EnsurePersonalWorkspace(ctx, p.User); err != nil {
			return nil, err
		}
	}
	ws, err := s.Store.UserWorkspaces(ctx, p.User.ID)
	if err == nil && s.instanceOwner(p.User) {
		// 인스턴스 owner는 모든 작업 공간을 본다. 자기 작업 공간(개인 먼저)이 앞에 오도록 나머지는 뒤에 붙인다.
		var all []*store.Workspace
		if all, err = s.Store.ListWorkspaces(ctx); err == nil {
			mine := map[string]bool{}
			for _, w := range ws {
				mine[w.ID] = true
			}
			for _, w := range all {
				if !mine[w.ID] {
					ws = append(ws, w)
				}
			}
		}
	}

	if err != nil {
		return nil, err
	}
	out := make([]workspaceView, 0, len(ws))
	for _, w := range ws {
		lv, err := s.workspaceAccess(ctx, p, w.ID)
		if err != nil {
			return nil, err
		}
		if lv == lvNone {
			continue
		}
		role := levelRole(lv)
		if lv == lvOwner {
			role = store.RoleOwner
		}
		out = append(out, workspaceView{w, role})
	}
	return out, nil
}

func (s *Server) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	out, err := s.visibleWorkspaces(r.Context(), principalFrom(r))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

func (s *Server) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if !validName(name) {
		writeErr(w, 400, "name is required (max 100 chars)")
		return
	}
	p := principalFrom(r)
	ws, err := s.Store.CreateWorkspace(r.Context(), name, p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.created", ws.ID, fmt.Sprintf("작업 공간 %s를 만들었습니다", name))
	writeJSON(w, 201, workspaceView{ws, store.RoleOwner})
}

// loadWorkspace는 경로의 작업 공간과 주체의 권한을 확인한다. 멤버가 아니면 404(있는지도 알리지 않는다), 권한이 모자라면 403.
func (s *Server) loadWorkspace(w http.ResponseWriter, r *http.Request, need int) (*store.Workspace, int, bool) {
	ws, err := s.Store.GetWorkspace(r.Context(), r.PathValue("wsid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, 0, false
	}
	lv, err := s.workspaceAccess(r.Context(), principalFrom(r), ws.ID)
	if err != nil {
		s.storeErr(w, err)
		return nil, 0, false
	}
	if lv == lvNone {
		writeErr(w, 404, "not found")
		return nil, 0, false
	}
	if lv < need {
		writeErr(w, 403, "권한이 없습니다")
		return nil, 0, false
	}
	return ws, lv, true
}

func (s *Server) patchWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.loadWorkspace(w, r, lvAdmin)
	if !ok {
		return
	}
	var in struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &in) {
		return
	}
	name := strings.TrimSpace(in.Name)
	if !validName(name) {
		writeErr(w, 400, "name is required (max 100 chars)")
		return
	}
	if err := s.Store.RenameWorkspace(r.Context(), ws.ID, name); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.renamed", ws.ID, fmt.Sprintf("작업 공간 %s의 이름을 %s(으)로 바꿨습니다", ws.Name, name))
	ws.Name = name
	writeJSON(w, 200, ws)
}

func (s *Server) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.loadWorkspace(w, r, lvOwner)
	if !ok {
		return
	}
	if ws.Default || ws.Personal {
		writeErr(w, 400, "기본 작업 공간과 개인 작업 공간은 지울 수 없습니다")
		return
	}
	if err := s.Store.DeleteWorkspace(r.Context(), ws.ID); err != nil {
		if errors.Is(err, store.ErrWorkspaceNotEmpty) {
			writeErr(w, 409, "작업 공간에 프로젝트가 남아 있습니다. 먼저 옮기거나 지우세요")
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.deleted", ws.ID, fmt.Sprintf("작업 공간 %s를 지웠습니다", ws.Name))
	w.WriteHeader(204)
}

func (s *Server) listWorkspaceMembersAPI(w http.ResponseWriter, r *http.Request) {
	ws, _, ok := s.loadWorkspace(w, r, lvViewer)
	if !ok {
		return
	}
	ms, err := s.Store.ListWorkspaceMembers(r.Context(), ws.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ms)
}

// checkMemberRole은 actor(lv)가 role을 줄 수 있는지다: 자기보다 높은 역할은 줄 수 없고 owner는 owner만 준다.
func checkMemberRole(w http.ResponseWriter, lv int, role string) bool {
	if !store.ValidWorkspaceRole(role) {
		writeErr(w, 400, "role must be owner, admin, member or viewer")
		return false
	}
	if roleLevel(role) > lv {
		writeErr(w, 403, "자기 권한보다 높은 역할은 줄 수 없습니다")
		return false
	}
	return true
}

func (s *Server) addWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	ws, lv, ok := s.loadWorkspace(w, r, lvAdmin)
	if !ok {
		return
	}
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Role == "" {
		in.Role = store.RoleMember
	}
	if !checkMemberRole(w, lv, in.Role) {
		return
	}
	u, err := s.Store.GetUserByEmail(r.Context(), in.Email)
	if err != nil || u.Disabled {
		writeErr(w, 404, "이 이메일의 사용자가 없습니다. 먼저 aven에 초대하세요")
		return
	}
	if cur, _ := s.Store.WorkspaceRole(r.Context(), ws.ID, u); cur != "" {
		writeErr(w, 409, "이미 멤버입니다")
		return
	}
	if err := s.Store.SetWorkspaceMember(r.Context(), ws.ID, u.ID, in.Role); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.member_added", ws.ID, fmt.Sprintf("%s를 작업 공간 %s에 %s(으)로 추가했습니다", u.Display(), ws.Name, in.Role))
	writeJSON(w, 201, map[string]any{"workspace_id": ws.ID, "user_id": u.ID, "role": in.Role, "email": u.Email, "name": u.Name})
}

func (s *Server) patchWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	ws, lv, ok := s.loadWorkspace(w, r, lvAdmin)
	if !ok {
		return
	}
	var in struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &in) {
		return
	}
	if !checkMemberRole(w, lv, in.Role) {
		return
	}
	u, cur, ok := s.workspaceMemberTarget(w, r, ws)
	if !ok {
		return
	}
	if roleLevel(cur) > lv {
		writeErr(w, 403, "자기보다 높은 역할의 멤버는 바꿀 수 없습니다")
		return
	}
	if ws.Personal && ws.OwnerID != nil && *ws.OwnerID == u.ID && in.Role != store.RoleOwner {
		writeErr(w, 400, "개인 작업 공간 주인의 역할은 바꿀 수 없습니다")
		return
	}
	if cur == store.RoleOwner && in.Role != store.RoleOwner && !s.otherOwnerLeft(w, r, ws) {
		return
	}
	if err := s.Store.SetWorkspaceMember(r.Context(), ws.ID, u.ID, in.Role); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.member_updated", ws.ID, fmt.Sprintf("작업 공간 %s에서 %s의 역할을 %s(으)로 바꿨습니다", ws.Name, u.Display(), in.Role))
	writeJSON(w, 200, map[string]any{"workspace_id": ws.ID, "user_id": u.ID, "role": in.Role})
}

func (s *Server) removeWorkspaceMember(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	// 자기 자신은 viewer여도 나갈 수 있다. 다른 사람을 빼려면 admin이다.
	need := lvAdmin
	if r.PathValue("uid") == p.User.ID {
		need = lvViewer
	}
	ws, lv, ok := s.loadWorkspace(w, r, need)
	if !ok {
		return
	}
	u, cur, ok := s.workspaceMemberTarget(w, r, ws)
	if !ok {
		return
	}
	if u.ID != p.User.ID && roleLevel(cur) > lv {
		writeErr(w, 403, "자기보다 높은 역할의 멤버는 뺄 수 없습니다")
		return
	}
	if ws.Personal && ws.OwnerID != nil && *ws.OwnerID == u.ID {
		writeErr(w, 400, "개인 작업 공간의 주인은 뺄 수 없습니다")
		return
	}
	if cur == store.RoleOwner && !s.otherOwnerLeft(w, r, ws) {
		return
	}
	if err := s.Store.RemoveWorkspaceMember(r.Context(), ws.ID, u.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "workspace.member_removed", ws.ID, fmt.Sprintf("작업 공간 %s에서 %s를 뺐습니다", ws.Name, u.Display()))
	w.WriteHeader(204)
}

func (s *Server) workspaceMemberTarget(w http.ResponseWriter, r *http.Request, ws *store.Workspace) (*store.User, string, bool) {
	u, err := s.Store.GetUser(r.Context(), r.PathValue("uid"))
	if err != nil {
		s.storeErr(w, err)
		return nil, "", false
	}
	cur, err := s.Store.WorkspaceRole(r.Context(), ws.ID, u)
	if err != nil {
		s.storeErr(w, err)
		return nil, "", false
	}
	if cur == "" {
		writeErr(w, 404, "not found")
		return nil, "", false
	}
	return u, cur, true
}

func (s *Server) otherOwnerLeft(w http.ResponseWriter, r *http.Request, ws *store.Workspace) bool {
	n, err := s.Store.CountWorkspaceOwners(r.Context(), ws.ID)
	if err != nil {
		s.storeErr(w, err)
		return false
	}
	if n <= 1 {
		writeErr(w, 400, "작업 공간에는 owner가 한 명 이상 있어야 합니다")
		return false
	}
	return true
}

// createTargetWorkspace는 프로젝트를 만들거나 옮길 작업 공간을 확인한다(member 이상). 비우면 기본 작업 공간이다(CLI/IaC 호환).
func (s *Server) createTargetWorkspace(w http.ResponseWriter, r *http.Request, wsID string) (string, bool) {
	p := principalFrom(r)
	if wsID == "" {
		wsID = store.DefaultWorkspaceID
		if p != nil && p.Token != nil && p.Token.Scope == store.ScopeWorkspace && p.Token.WorkspaceID != nil {
			wsID = *p.Token.WorkspaceID
		}
	}
	if p == nil {
		return wsID, true
	}
	ws, err := s.Store.GetWorkspace(r.Context(), wsID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, 404, "workspace not found")
			return "", false
		}
		s.storeErr(w, err)
		return "", false
	}
	lv, err := s.workspaceAccess(r.Context(), p, ws.ID)
	if err != nil {
		s.storeErr(w, err)
		return "", false
	}
	if lv == lvNone {
		writeErr(w, 404, "workspace not found")
		return "", false
	}
	if lv < lvMember {
		writeErr(w, 403, "이 작업 공간에 프로젝트를 만들 권한이 없습니다")
		return "", false
	}
	return ws.ID, true
}

func (s *Server) moveProject(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		WorkspaceID string `json:"workspace_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.WorkspaceID == "" {
		writeErr(w, 400, "workspace_id is required")
		return
	}
	wsID, ok := s.createTargetWorkspace(w, r, in.WorkspaceID)
	if !ok {
		return
	}
	if wsID == p.WorkspaceID {
		writeJSON(w, 200, p)
		return
	}
	if err := s.Store.MoveProject(r.Context(), p.ID, wsID, principalFrom(r).User.ID); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "대상 작업 공간에 같은 이름의 프로젝트가 있습니다")
			return
		}
		s.storeErr(w, err)
		return
	}

	p.WorkspaceID, p.GitHubUserID = wsID, nil
	s.Events.Emit(r.Context(), p.ID, nil, nil, "project.moved", "프로젝트 "+p.Name+"를 다른 작업 공간으로 옮겼습니다")
	s.audit(r, "project.moved", p.ID, fmt.Sprintf("프로젝트 %s를 작업 공간 %s(으)로 옮겼습니다", p.Name, wsID))
	writeJSON(w, 200, p)
}

// projectGitHubAccount는 프로젝트가 쓰는 GitHub 연결의 공개 정보다(토큰은 없다).
type projectGitHubAccount struct {
	UserID   string `json:"user_id"`
	UserName string `json:"user_name"`
	Login    string `json:"login,omitempty"`
	// Connected가 false면 지정된 사용자가 GitHub 연결을 해제했다(배포는 KEEL_GITHUB_TOKEN으로만 한다).
	Connected bool `json:"connected"`
}

func (s *Server) projectGitHub(ctx context.Context, p *store.Project) *projectGitHubAccount {
	if p.GitHubUserID == nil || s.Store == nil {
		return nil
	}
	a := &projectGitHubAccount{UserID: *p.GitHubUserID}
	if u, err := s.Store.GetUser(ctx, a.UserID); err == nil {
		a.UserName = u.Display()
	}
	if c, err := s.Store.GetGitHubConnection(ctx, a.UserID); err == nil {
		a.Login, a.Connected = c.Login, true
	}
	return a
}

// githubBorrowed는 서버 owner가 프로젝트 멤버십 없이(owner 권한으로만) 다른 사람의 GitHub 연결로 배포하게 되는지다.
// 막지는 않는다: 프로젝트 화면에 경고를 띄우고 그 배포를 감사 기록에 남긴다(docs/permissions.md).
func (s *Server) githubBorrowed(ctx context.Context, u *store.User, p *store.Project) bool {
	if s.Store == nil || s.Deploy == nil || !s.instanceOwner(u) || p.GitHubUserID == nil || *p.GitHubUserID == u.ID {
		return false
	}
	if s.Deploy.UserGitHubToken(ctx, *p.GitHubUserID) == "" {
		return false // 연결이 없으면 KEEL_GITHUB_TOKEN을 쓴다
	}
	role, err := s.Store.EffectiveProjectRole(ctx, p.ID, u)
	return errors.Is(err, store.ErrNotFound) || (err == nil && role == "")
}

// principalUserOf는 요청한 사용자다(인증 없이 부르는 테스트에서는 nil).
func principalUserOf(r *http.Request) *store.User {
	if pr := principalFrom(r); pr != nil {
		return pr.User
	}
	return nil
}

// auditBorrowedGitHub은 deploy.Manager.EnqueueHook이다. API 요청으로 만든 GitHub 저장소 배포가 githubBorrowed면 감사 기록을 남긴다.
// (웹훅·스케줄 배포는 요청한 주체가 없어 기록하지 않는다.)
func (s *Server) auditBorrowedGitHub(ctx context.Context, d *store.Deployment) {
	pr, _ := ctx.Value(principalKey{}).(*principal)
	if pr == nil || d.Spec.RepoURL == nil || !strings.HasPrefix(*d.Spec.RepoURL, "https://github.com/") {
		return
	}
	svc, err := s.Store.GetService(ctx, d.ServiceID)
	if err != nil {
		return
	}
	p, err := s.Store.GetProject(ctx, svc.ProjectID)
	if err != nil || !s.githubBorrowed(ctx, pr.User, p) {
		return
	}
	who := *p.GitHubUserID
	if a := s.projectGitHub(ctx, p); a != nil {
		who = a.UserName
		if a.Login != "" {
			who += " (@" + a.Login + ")"
		}
	}
	a := &store.AuditEntry{Kind: "project.github_borrowed", Target: p.ID, ActorID: pr.User.ID, Actor: pr.display(),
		Message: fmt.Sprintf("서버 owner 권한으로 %s의 GitHub 연결을 써서 프로젝트 %s의 %s 배포(%s, %s)를 요청했습니다", who, p.Name, svc.Name, d.ID, d.Trigger)}
	if err := s.Store.CreateAudit(context.WithoutCancel(ctx), a); err != nil {
		s.Log.Warn("audit", "err", err)
	}
}

// setProjectGitHub은 프로젝트의 GitHub 연결을 요청한 사용자의 연결로 바꾸거나(user_id = 자기 id) 해제한다(null).
// 다른 사람의 연결은 서버 owner만 브라우저 세션으로 지정할 수 있다(그 사람의 비공개 저장소에 접근하게 되므로 owner.* 감사 기록).
func (s *Server) setProjectGitHub(w http.ResponseWriter, r *http.Request) {
	p, err := s.Store.GetProject(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		UserID *string `json:"user_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	pr := principalFrom(r)
	if in.UserID != nil && *in.UserID == "" {
		in.UserID = nil
	}
	var other *store.User // 서버 owner가 지정한 다른 사람
	if in.UserID != nil {
		if pr == nil {
			writeErr(w, 403, "자기 GitHub 연결만 프로젝트에 지정할 수 있습니다")
			return
		}
		if *in.UserID != pr.User.ID {
			if !s.instanceOwner(pr.User) || pr.Token != nil || pr.OAuth != nil {
				writeErr(w, 403, "자기 GitHub 연결만 프로젝트에 지정할 수 있습니다")
				return
			}
			u, err := s.Store.GetUser(r.Context(), *in.UserID)
			if errors.Is(err, store.ErrNotFound) || (err == nil && u.Disabled) {
				writeErr(w, 400, "활성 사용자가 아닙니다")
				return
			} else if err != nil {
				s.storeErr(w, err)
				return
			}
			other = u
		}
		if s.Deploy.UserGitHubConnection(r.Context(), *in.UserID) == nil {
			if other != nil {
				writeErr(w, 409, other.Display()+"님은 GitHub 계정을 연결하지 않았습니다")
				return
			}
			writeErr(w, 409, "GitHub 계정을 먼저 연결하세요")
			return
		}
	}
	if err := s.Store.SetProjectGitHubUser(r.Context(), p.ID, in.UserID); err != nil {
		s.storeErr(w, err)
		return
	}
	p.GitHubUserID = in.UserID
	switch {
	case other != nil:
		s.audit(r, "owner.github_assigned", p.ID, "서버 owner 권한으로 프로젝트 "+p.Name+"의 GitHub 연결을 "+other.Display()+"의 계정으로 바꿨습니다")
	case in.UserID != nil:
		s.audit(r, "project.github_updated", p.ID, "프로젝트 "+p.Name+"의 GitHub 연결을 "+pr.User.Display()+"의 계정으로 바꿨습니다")
	default:
		s.audit(r, "project.github_updated", p.ID, "프로젝트 "+p.Name+"의 GitHub 연결을 해제했습니다")
	}
	writeJSON(w, 200, map[string]any{"github_account": s.projectGitHub(r.Context(), p)})
}

func (s *Server) getWorkspace(w http.ResponseWriter, r *http.Request) {
	ws, lv, ok := s.loadWorkspace(w, r, lvViewer)
	if !ok {
		return
	}
	role := levelRole(lv)
	if lv == lvOwner {
		role = store.RoleOwner
	}
	writeJSON(w, 200, workspaceView{ws, role})
}
