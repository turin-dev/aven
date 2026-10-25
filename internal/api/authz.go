package api

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// ---- 권한(RBAC) ----
//
// 작업 공간 역할: owner > admin > member > viewer. owner/admin은 모든 프로젝트에서 프로젝트 admin과 같다.
// member/viewer는 project_members에 있는 프로젝트(와 접근 그룹이 켜져 있으면 그룹이 준 프로젝트)만 보이고, 그 프로젝트 역할(admin|member|viewer)을 따른다.
// 제한된 환경(restricted)의 리소스는 프로젝트 admin만 접근한다(access_controls.go).
// 인스턴스 역할은 운영 권한만 부여하고 작업 공간 멤버십은 별도로 확인한다.
// 예외: 인스턴스 owner(KEEL_SUPERADMINS 포함)는 막히는 것이 없다. 모든 작업 공간에서 owner, 모든 프로젝트에서 admin이다
// (instanceOwner). 그 사람이 만든 API 토큰·OAuth 승인은 여전히 자기 범위와 역할로 좁혀진다.
// API 토큰은 만든 사용자의 권한을 넘지 못하고(매 요청마다 min(사용자 권한, 토큰 역할)), 범위(instance|workspace|project|environment)
// 밖의 리소스는 없는 것처럼(404) 보인다. environment 토큰은 그 환경의 리소스만 바꿀 수 있고 프로젝트 수준 리소스는 읽기만 한다.

// 권한 수준. 프로젝트 역할 viewer=1, member=2, admin=3. 작업 공간 owner=4.
const (
	lvNone   = 0
	lvViewer = 1
	lvMember = 2
	lvAdmin  = 3
	lvOwner  = 4
)

func roleLevel(role string) int { return store.RoleLevel(role) }

func levelRole(lv int) string {
	switch {
	case lv >= lvAdmin:
		return store.RoleAdmin
	case lv == lvMember:
		return store.RoleMember
	case lv == lvViewer:
		return store.RoleViewer
	}
	return ""
}

type accessKind int

const (
	accAny       accessKind = iota // 인증만 필요(목록은 핸들러가 보이는 것만 거른다)
	accSelf                        // 자기 계정 관리(세션, 2FA, 토큰). 범위 있는 API 토큰은 쓸 수 없다
	accInstance                    // 인스턴스 운영 역할
	accWorkspace                   // 경로의 작업 공간 멤버십
	accProject                     // 경로의 리소스가 속한 프로젝트(와 환경)에서 level 이상
)

// access는 라우트마다 선언하는 필요 권한이다.
type access struct {
	Kind  accessKind
	Level int
	// NoToken이면 범위 있는 API 토큰(keel_)은 거부한다(사용자·멤버·토큰 관리). KEEL_API_TOKEN(owner)은 통과한다.
	NoToken bool
	// Cap은 인스턴스 라우트가 속한 어드민 기능이다(register가 채운다). 서버 owner가 서버 관리자에게 막을 수 있다(owner.go).
	Cap string
	// MaintOK면 점검 모드에서도 owner가 아닌 사용자가 쓰기 요청을 할 수 있다(register가 채운다).
	MaintOK bool
}

func anyUser() access              { return access{Kind: accAny} }
func selfOnly() access             { return access{Kind: accSelf, NoToken: true} }
func instance(level int) access    { return access{Kind: accInstance, Level: level} }
func ws(level int) access          { return access{Kind: accWorkspace, Level: level} }
func instanceAdminNoToken() access { return access{Kind: accInstance, Level: lvAdmin, NoToken: true} }
func wsAdminNoToken() access       { return access{Kind: accWorkspace, Level: lvAdmin, NoToken: true} }
func ownerOnly() access            { return access{Kind: accInstance, Level: lvOwner, NoToken: true} }
func proj(level int) access        { return access{Kind: accProject, Level: level} }
func projNoToken(l int) access     { return access{Kind: accProject, Level: l, NoToken: true} }

// Route는 등록된 인증 라우트와 그 권한 선언이다(권한 누락을 막는 테스트가 전부 돌아본다).
type Route struct {
	Pattern string
	Access  access
}

// Routes는 Handler()가 등록한 인증 라우트 목록이다.
func (s *Server) Routes() []Route { return append([]Route(nil), s.routes...) }

// target은 리소스가 속한 프로젝트와 환경이다(환경이 없는 프로젝트 수준 리소스면 eid가 비어 있다).
type target struct{ pid, eid string }

type resolver func(ctx context.Context, r *http.Request) (target, error)

// resolverFor는 라우트 패턴의 경로 변수로 리소스의 프로젝트를 찾는 방법을 고른다.
// 프로젝트 권한 라우트인데 찾을 방법이 없으면 시작할 때 panic한다(새 라우트가 권한 검사를 빠뜨리지 않게).
func (s *Server) resolverFor(pattern string) resolver {
	has := func(v string) bool { return strings.Contains(pattern, "{"+v+"}") }
	switch {
	case has("did"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			d, err := s.Store.GetDeployment(ctx, r.PathValue("did"))
			if err != nil {
				return target{}, err
			}
			return s.envTarget(ctx, d.EnvironmentID)
		}
	case has("rid"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			run, err := s.Store.GetCronRun(ctx, r.PathValue("rid"))
			if err != nil {
				return target{}, err
			}
			d, err := s.Store.GetDeployment(ctx, run.DeploymentID)
			if err != nil {
				return target{}, err
			}
			return s.envTarget(ctx, d.EnvironmentID)
		}
	case has("bid"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			b, err := s.Store.GetBackup(ctx, r.PathValue("bid"))
			if err != nil {
				return target{}, err
			}
			v, err := s.Store.GetVolume(ctx, b.VolumeID)
			if err != nil {
				return target{}, err
			}
			return target{v.ProjectID, v.EnvironmentID}, nil
		}
	case strings.Contains(pattern, "/db-backups/{id}"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			b, err := s.Store.GetDBBackup(ctx, r.PathValue("id"))
			if err != nil {
				return target{}, err
			}
			return s.envTarget(ctx, b.EnvironmentID)
		}
	case has("sbid"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			b, err := s.Store.GetSandbox(ctx, r.PathValue("sbid"))
			if err != nil {
				return target{}, err
			}
			return target{pid: b.ProjectID, eid: b.EnvironmentID}, nil
		}
	case has("cpid"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			c, err := s.Store.GetSandboxCheckpoint(ctx, r.PathValue("cpid"))
			if err != nil {
				return target{}, err
			}
			return target{pid: c.ProjectID, eid: c.EnvironmentID}, nil
		}
	case has("vid"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			v, err := s.Store.GetVolume(ctx, r.PathValue("vid"))
			if err != nil {
				return target{}, err
			}
			return target{v.ProjectID, v.EnvironmentID}, nil
		}
	case has("sid"):
		withEnv := has("eid")
		return func(ctx context.Context, r *http.Request) (target, error) {
			svc, err := s.Store.GetService(ctx, r.PathValue("sid"))
			if err != nil {
				return target{}, err
			}
			t := target{pid: svc.ProjectID}
			if withEnv {
				env, err := s.Store.GetEnvironment(ctx, r.PathValue("eid"))
				if err != nil {
					return target{}, err
				}
				if env.ProjectID != svc.ProjectID {
					return target{}, store.ErrNotFound
				}
				t.eid = env.ID
			}
			return t, nil
		}
	case has("eid"):
		withProject := has("pid")
		return func(ctx context.Context, r *http.Request) (target, error) {
			t, err := s.envTarget(ctx, r.PathValue("eid"))
			if err != nil {
				return target{}, err
			}
			if withProject && t.pid != r.PathValue("pid") {
				return target{}, store.ErrNotFound
			}
			return t, nil
		}
	case has("pid"):
		return func(ctx context.Context, r *http.Request) (target, error) {
			p, err := s.Store.GetProject(ctx, r.PathValue("pid"))
			if err != nil {
				return target{}, err
			}
			return target{pid: p.ID}, nil
		}
	}
	return nil
}

func (s *Server) envTarget(ctx context.Context, eid string) (target, error) {
	env, err := s.Store.GetEnvironment(ctx, eid)
	if err != nil {
		return target{}, err
	}
	return target{env.ProjectID, env.ID}, nil
}

// instanceLevel grants access to instance operations only. Workspace/project
// tokens and OAuth workspace scopes cannot administer the server.
func (s *Server) instanceLevel(p *principal) int {
	if p == nil || p.User == nil || p.User.Disabled {
		return lvNone
	}
	lv := roleLevel(p.User.Role)
	if s.isSuperAdmin(p.User) {
		lv = lvOwner
	}
	if p.OAuth != nil {
		return lvNone
	}
	if t := p.Token; t != nil {
		if t.Scope != store.ScopeInstance {
			return lvNone
		}
		lv = min(lv, roleLevel(t.Role))
	}
	return lv
}

// instanceOwner는 u가 활성 인스턴스 owner(users.role=owner 또는 KEEL_SUPERADMINS)인지다.
// 인스턴스 owner는 멤버십과 무관하게 모든 작업 공간의 owner, 모든 프로젝트의 admin이다.
func (s *Server) instanceOwner(u *store.User) bool {
	return u != nil && !u.Disabled && (u.Role == store.RoleOwner || s.isSuperAdmin(u))
}

// userProjectLevel은 토큰 제한 없이 사용자가 프로젝트에서 가진 권한이다.
// 모든 작업 공간에서 명시적인 멤버십과 프로젝트/그룹 역할을 확인한다.
func (s *Server) userProjectLevel(ctx context.Context, u *store.User, pid string) (int, error) {
	// aven-server 자신을 배포하는 프로젝트는 서버 owner만 본다(배포하면 호스트 root로 코드가 돈다).
	if !s.instanceOwner(u) && s.isSelfProject(ctx, pid) {
		return lvNone, nil
	}
	if s.instanceOwner(u) {
		if s.Store != nil {
			if _, err := s.Store.GetProject(ctx, pid); err != nil {
				if errors.Is(err, store.ErrNotFound) {
					return lvNone, nil
				}
				return lvNone, err
			}
		}
		return lvAdmin, nil
	}
	if s.Store == nil {
		if roleLevel(u.Role) >= lvAdmin {
			return lvAdmin, nil
		}
		return lvNone, nil
	}
	role, err := s.Store.EffectiveProjectRole(ctx, pid, u)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return lvNone, nil
		}
		return lvNone, err
	}
	return roleLevel(role), nil
}

// projectLevel은 주체가 프로젝트 pid(환경 eid, 프로젝트 수준이면 "")에서 가진 권한이다(lvNone이면 보이지 않음).
func (s *Server) projectLevel(ctx context.Context, p *principal, pid, eid string) (int, error) {
	if p.Via == viaEnvToken {
		return lvAdmin, nil // KEEL_API_TOKEN(운영 자동화)은 모든 작업 공간의 프로젝트에 접근한다
	}
	lv, err := s.userProjectLevel(ctx, p.User, pid)
	if err != nil || lv == lvNone {
		return lvNone, err
	}
	// 제한된 환경(Environment RBAC)은 사용자가 프로젝트 admin일 때만 보인다. 토큰 역할이 아니라 만든 사용자의 권한으로
	// 판단하므로 admin이 만든 environment 토큰(역할 상한 member)은 그 환경에서 계속 쓸 수 있다.
	if eid != "" && lv < lvAdmin {
		if restricted, err := s.envRestricted(ctx, eid); err != nil || restricted {
			return lvNone, err
		}
	}
	if p.OAuth != nil {
		workspaceScope := oauthScopeLevel(p.OAuth.Scopes, "workspace:")
		projectScope := lvNone
		if containsString(p.OAuth.ProjectIDs, pid) {
			projectScope = oauthScopeLevel(p.OAuth.Scopes, "project:")
		}
		lv = min(lv, max(workspaceScope, projectScope))
		if lv == lvNone {
			return lvNone, nil
		}
	}
	if t := p.Token; t != nil {
		if t.WorkspaceID == nil {
			return lvNone, nil
		}
		project, err := s.Store.GetProject(ctx, pid)
		if err != nil {
			return lvNone, err
		}
		if *t.WorkspaceID != project.WorkspaceID {
			return lvNone, nil
		}
		switch t.Scope {
		case store.ScopeWorkspace:
		case store.ScopeInstance:
			return lvNone, nil
		case store.ScopeProject:
			if t.ProjectID == nil || *t.ProjectID != pid {
				return lvNone, nil
			}
		case store.ScopeEnvironment:
			if t.ProjectID == nil || *t.ProjectID != pid || t.EnvironmentID == nil {
				return lvNone, nil
			}
			if eid == "" {
				lv = min(lv, lvViewer) // 프로젝트 수준 리소스(설정, 서비스 정의, 멤버)는 읽기만
			} else if *t.EnvironmentID != eid {
				return lvNone, nil
			}
			lv = min(lv, lvMember) // 환경 토큰은 환경 자체를 지우는 등 admin 작업을 할 수 없다
		default:
			return lvNone, nil
		}
		lv = min(lv, roleLevel(t.Role))
	}
	return lv, nil
}

func (s *Server) envRestricted(ctx context.Context, eid string) (bool, error) {
	env, err := s.Store.GetEnvironment(ctx, eid)
	if err != nil {
		return false, err
	}
	return env.Restricted, nil
}

type grantKey struct{}

// grantLevel은 프로젝트 라우트에서 계산한 권한이다. 핸들러를 직접 부르는 테스트(주체 없음)는 제한이 없다.
func grantLevel(r *http.Request) int {
	if v, ok := r.Context().Value(grantKey{}).(int); ok {
		return v
	}
	if principalFrom(r) == nil {
		return lvOwner
	}
	return lvNone
}

// canSeeSecrets: 변수 값(원본/해석된 값), 알림 webhook URL 같은 비밀을 볼 수 있는지(member 이상).
func canSeeSecrets(r *http.Request) bool { return grantLevel(r) >= lvMember }

// visibleLevel은 목록 핸들러(프로젝트 목록 등)에서 쓰는 권한이다. 주체가 없으면(직접 호출) 제한이 없다.
func (s *Server) visibleLevel(r *http.Request, pid string) int {
	p := principalFrom(r)
	if p == nil {
		return lvOwner
	}
	lv, err := s.projectLevel(r.Context(), p, pid, "")
	if err != nil {
		return lvNone
	}
	return lv
}

// authorize는 라우트의 권한 선언을 검사한다. 보이지 않는 리소스는 404, 보이지만 권한이 모자라면 403이다.
func (s *Server) authorize(a access, res resolver, h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if p == nil {
			writeErr(w, 401, "unauthorized")
			return
		}
		if a.NoToken && (p.Token != nil || p.OAuth != nil) {
			writeErr(w, 403, "API 토큰으로는 이 작업을 할 수 없습니다")
			return
		}
		if p.OAuth != nil && a.Kind == accAny && !s.oauthAnyRouteAllowed(p, r.URL.Path) {
			writeErr(w, 403, "요청한 OAuth scope에 이 API 권한이 없습니다")
			return
		}
		if !s.ownerGate(w, r, p, a) {
			return
		}
		switch a.Kind {
		case accAny, accSelf:
		case accInstance:
			if s.instanceLevel(p) < a.Level {
				writeErr(w, 403, "권한이 없습니다")
				return
			}
		case accWorkspace:
			wsID, err := s.requestWorkspace(r)
			if err != nil {
				s.storeErr(w, err)
				return
			}
			lv, err := s.workspaceAccess(r.Context(), p, wsID)
			if err != nil {
				s.storeErr(w, err)
				return
			}
			if lv == lvNone {
				writeErr(w, 404, "not found")
				return
			}
			if lv < a.Level {
				writeErr(w, 403, "권한이 없습니다")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), workspaceGrantKey{}, wsID))
		case accProject:
			t, err := res(r.Context(), r)
			if err != nil {
				if errors.Is(err, store.ErrNotFound) {
					writeErr(w, 404, "not found")
					return
				}
				s.storeErr(w, err)
				return
			}
			lv, err := s.projectLevel(r.Context(), p, t.pid, t.eid)
			if err != nil {
				s.storeErr(w, err)
				return
			}
			if lv == lvNone {
				// 제한된 환경은 프로젝트 멤버에게 있다는 것까지는 보이므로(프로젝트의 환경 목록) 404 대신 403이다.
				if t.eid != "" {
					ulv, _ := s.userProjectLevel(r.Context(), p.User, t.pid)
					if plv, err := s.projectLevel(r.Context(), p, t.pid, ""); err == nil && plv > lvNone && ulv < lvAdmin {
						if restricted, err := s.envRestricted(r.Context(), t.eid); err == nil && restricted {
							writeErr(w, 403, errRestrictedEnv)
							return
						}
					}
				}
				writeErr(w, 404, "not found")
				return
			}
			if lv < a.Level {
				writeErr(w, 403, "권한이 없습니다")
				return
			}
			r = r.WithContext(context.WithValue(r.Context(), grantKey{}, lv))
		default:
			writeErr(w, 403, "권한이 없습니다")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// authzProbe가 켜져 있으면(테스트) 권한 검사를 통과한 요청에 핸들러 대신 299를 돌려준다.
const probeStatus = 299

func probeHandler(w http.ResponseWriter, r *http.Request) { w.WriteHeader(probeStatus) }

// register는 인증 + 권한 검사를 붙여 라우트를 등록한다.
func (s *Server) register(mux *http.ServeMux, pattern string, a access, f http.HandlerFunc) {
	var res resolver
	if a.Kind == accProject {
		res = s.resolverFor(pattern)
		if res == nil {
			panic("api: project route without a resource path variable: " + pattern)
		}
	}
	if (a.Kind == accWorkspace || a.Kind == accInstance) && a.Level < lvViewer {
		panic("api: workspace route without a level: " + pattern)
	}
	if a.Kind == accInstance {
		a.Cap = adminCapFor(pattern)
	}
	a.MaintOK = maintenanceExempt[pattern]
	if s.authzProbe {
		f = probeHandler
	}
	s.routes = append(s.routes, Route{Pattern: pattern, Access: a})
	mux.Handle(pattern, s.requireAuth(s.authorize(a, res, s.requireMFA(f))))
}

// isSuperAdmin은 u가 KEEL_SUPERADMINS에 있는 활성 계정인지다(인스턴스 운영 owner, 작업 공간 멤버십은 별도).
func (s *Server) isSuperAdmin(u *store.User) bool {
	return u != nil && !u.Disabled && s.SuperAdmins[store.NormalizeEmail(u.Email)]
}

// requestWorkspace resolves the workspace for workspace routes. Unqualified
// legacy routes address only the default workspace.
type workspaceGrantKey struct{}

func (s *Server) requestWorkspace(r *http.Request) (string, error) {
	if id := r.PathValue("wsid"); id != "" {
		return id, nil
	}
	if pid := r.PathValue("pid"); pid != "" {
		p, err := s.Store.GetProject(r.Context(), pid)
		if err != nil {
			return "", err
		}
		return p.WorkspaceID, nil
	}
	return store.DefaultWorkspaceID, nil
}
