package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/turin-dev/aven/internal/store"
)

// ---- 서버 owner 전용 관리(docs/permissions.md의 "서버 owner 전용 관리") ----
//
// 서버 owner(users.role=owner, KEEL_SUPERADMINS, 기본 계정)만 쓰는 기능이다. 서버 관리자(admin)는 쓸 수 없고,
// 작업 공간 owner/admin과도 무관하다. 모든 라우트는 세션으로만 부를 수 있다(API 토큰·OAuth 불가).
//
//   - 관리자 권한 범위: owner가 서버 관리자에게 어드민 기능을 기능별로 허용/차단한다(authorize가 강제).
//   - 점검 모드: owner가 아닌 사용자의 쓰기 요청을 503으로 막고, 선택적으로 로그인과 GitHub 웹훅 배포도 멈춘다.
//   - 전체 공지: 대시보드 위쪽에 모든 사용자에게 보이는 배너.
//   - 전체 세션·API 토큰 통제, 소유권 이전, 비상 잠금, aven DB 즉시 백업, aven-server 재시작.
//
// 설정은 settings KV에 JSON으로 저장한다(마이그레이션 없음).

const (
	settingAdminCaps    = "owner.admin_caps"
	settingMaintenance  = "owner.maintenance"
	settingAnnouncement = "owner.announcement"
)

var serverStartedAt = time.Now()

// adminCap은 owner가 서버 관리자에게 허용/차단할 수 있는 어드민 기능 묶음이다. Paths는 그 기능의 인스턴스 라우트 경로 접두사다.
type adminCap struct {
	ID    string   `json:"id"`
	Label string   `json:"label"`
	Paths []string `json:"-"`
}

var adminCaps = []adminCap{
	{"users", "사용자·초대", []string{"/api/users", "/api/invites"}},
	{"workspaces", "작업 공간 삭제", []string{"/api/instance/workspaces"}},
	{"resources", "자원 제한", []string{"/api/instance/resource-limits"}},
	{"nodes", "서버 노드", []string{"/api/nodes"}},
	{"security", "보안(2단계 인증 정책, 가드레일)", []string{"/api/workspace/security", "/api/instance/security", "/api/access-controls"}},
	{"sso", "SAML SSO", []string{"/api/saml"}},
	{"oauth_apps", "OAuth 앱", []string{"/api/oauth/apps"}},
	{"notifications", "전체 알림·SMTP", []string{"/api/notification-channels", "/api/system/smtp"}},
	{"audit", "감사 기록", []string{"/api/audit"}},
	{"agent", "Aven Agent 설정", []string{"/api/ai/settings"}},
	{"backups", "백업 저장소", []string{"/api/backup-destinations"}},
	{"usage", "서버 사용량", []string{"/api/usage"}},
	// 라우트 접두사가 없다: 서비스 어드민 탭의 핸들러(service_admin.go)가 직접 확인한다.
	{"service_controls", "서비스 정지·배포 차단", nil},
}

// adminCapFor는 인스턴스 라우트 패턴("GET /api/users/{uid}")이 속한 기능 ID다(없으면 "", 항상 허용).
func adminCapFor(pattern string) string {
	path := pattern
	if _, p, ok := strings.Cut(pattern, " "); ok {
		path = p
	}
	for _, c := range adminCaps {
		for _, prefix := range c.Paths {
			if path == prefix || strings.HasPrefix(path, prefix+"/") {
				return c.ID
			}
		}
	}
	return ""
}

func validAdminCap(id string) bool {
	return slices.ContainsFunc(adminCaps, func(c adminCap) bool { return c.ID == id })
}

// maintenanceExempt는 점검 모드에서도 owner가 아닌 사용자가 할 수 있는 쓰기다(알림 읽음, 자기 세션 끊기).
var maintenanceExempt = map[string]bool{
	"POST /api/notifications/read":      true,
	"DELETE /api/auth/sessions/{sesid}": true,
}

type adminCapsSetting struct {
	Disabled []string `json:"disabled"`
}

type maintenanceState struct {
	Enabled       bool       `json:"enabled"`
	Message       string     `json:"message"`
	BlockLogins   bool       `json:"block_logins"`
	PauseWebhooks bool       `json:"pause_webhooks"`
	StartedAt     *time.Time `json:"started_at"`
	StartedBy     string     `json:"started_by"`
}

type announcement struct {
	Enabled   bool       `json:"enabled"`
	Message   string     `json:"message"`
	Level     string     `json:"level"` // info | warning | critical
	UpdatedAt *time.Time `json:"updated_at"`
}

const defaultMaintenanceMessage = "서버 점검 중입니다. 잠시 후 다시 시도해 주세요."

// errMaintenanceLogin은 점검 모드가 owner가 아닌 계정의 로그인을 막을 때다(storeErr가 503으로 바꾼다).
var errMaintenanceLogin = errors.New("서버 점검 중이라 owner만 로그인할 수 있습니다")

func (s *Server) loadOwnerSetting(ctx context.Context, key string, v any) {
	if s.Store == nil {
		return
	}
	raw, err := s.Store.GetSetting(ctx, key)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.Log.Warn("owner setting", "key", key, "err", err)
		}
		return
	}
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		s.Log.Warn("owner setting", "key", key, "err", err)
	}
}

func (s *Server) saveOwnerSetting(ctx context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.Store.SetSetting(ctx, key, string(b))
}

func (s *Server) maintenance(ctx context.Context) maintenanceState {
	var m maintenanceState
	s.loadOwnerSetting(ctx, settingMaintenance, &m)
	if m.Enabled && m.Message == "" {
		m.Message = defaultMaintenanceMessage
	}
	return m
}

func (s *Server) disabledAdminCaps(ctx context.Context) []string {
	var c adminCapsSetting
	s.loadOwnerSetting(ctx, settingAdminCaps, &c)
	return c.Disabled
}

// allowedAdminCaps는 주체가 쓸 수 있는 어드민 기능 ID다(owner는 전부, 서버 관리자는 owner가 차단하지 않은 것).
func (s *Server) allowedAdminCaps(ctx context.Context, p *principal) []string {
	lv := s.instanceLevel(p)
	out := []string{}
	if lv < lvAdmin {
		return out
	}
	disabled := []string{}
	if lv < lvOwner {
		disabled = s.disabledAdminCaps(ctx)
	}
	for _, c := range adminCaps {
		if !slices.Contains(disabled, c.ID) {
			out = append(out, c.ID)
		}
	}
	return out
}

// ownerPrincipal은 점검 모드와 관리자 권한 범위의 영향을 받지 않는 주체다(서버 owner, KEEL_API_TOKEN).
func (s *Server) ownerPrincipal(p *principal) bool {
	return p != nil && (p.Via == viaEnvToken || s.instanceOwner(p.User))
}

// ownerGate는 authorize가 권한 검사 전에 부른다. 점검 모드의 쓰기 차단과 서버 관리자의 기능 차단을 강제한다.
func (s *Server) ownerGate(w http.ResponseWriter, r *http.Request, p *principal, a access) bool {
	if s.Store == nil || s.ownerPrincipal(p) {
		return true
	}
	if !safeMethod(r.Method) && !a.MaintOK {
		if m := s.maintenance(r.Context()); m.Enabled {
			writeJSON(w, 503, map[string]any{"error": m.Message, "maintenance": true})
			return false
		}
	}
	if a.Kind == accInstance && a.Cap != "" && s.instanceLevel(p) >= a.Level {
		if slices.Contains(s.disabledAdminCaps(r.Context()), a.Cap) {
			writeErr(w, 403, "서버 owner가 이 관리 기능을 서버 관리자에게 허용하지 않았습니다")
			return false
		}
	}
	return true
}

// loginBlocked는 점검 모드가 u의 새 로그인을 막는지다.
func (s *Server) loginBlocked(ctx context.Context, u *store.User) bool {
	if s.Store == nil || s.instanceOwner(u) {
		return false
	}
	m := s.maintenance(ctx)
	return m.Enabled && m.BlockLogins
}

// superAdminIDs는 KEEL_SUPERADMINS 계정의 사용자 ID다(users.role과 무관하게 owner라 일괄 정리에서 뺀다).
func (s *Server) superAdminIDs(ctx context.Context) ([]string, error) {
	if len(s.SuperAdmins) == 0 {
		return nil, nil
	}
	us, err := s.Store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, u := range us {
		if s.SuperAdmins[store.NormalizeEmail(u.Email)] {
			out = append(out, u.ID)
		}
	}
	return out, nil
}

// ---- 공개 공지(모든 로그인 사용자) ----

// getInstanceNotice는 대시보드 배너용 공지와 점검 상태다(누구나 읽는다, 비밀 없음).
func (s *Server) getInstanceNotice(w http.ResponseWriter, r *http.Request) {
	var a announcement
	s.loadOwnerSetting(r.Context(), settingAnnouncement, &a)
	m := s.maintenance(r.Context())
	out := map[string]any{
		"announcement": nil,
		"maintenance":  map[string]any{"enabled": m.Enabled, "message": m.Message, "block_logins": m.BlockLogins, "started_at": m.StartedAt},
		"exempt":       s.ownerPrincipal(principalFrom(r)),
	}
	if a.Enabled && a.Message != "" {
		out["announcement"] = a
	}
	writeJSON(w, 200, out)
}

// ---- 개요 ----

type ownerAccount struct {
	ID          string     `json:"id"`
	Email       string     `json:"email"`
	Name        string     `json:"name"`
	Builtin     bool       `json:"builtin"`
	Disabled    bool       `json:"disabled"`
	TOTPEnabled bool       `json:"totp_enabled"`
	SuperAdmin  bool       `json:"superadmin"`
	Source      string     `json:"source"` // role | builtin | env
	LastLoginAt *time.Time `json:"last_login_at"`
	CreatedAt   time.Time  `json:"created_at"`
}

func (s *Server) dataDir() string {
	if s.Deploy == nil {
		return ""
	}
	return s.Deploy.Cfg.DataDir
}

func (s *Server) getOwnerOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	owners := []ownerAccount{}
	envOwners := []string{}
	for e := range s.SuperAdmins {
		envOwners = append(envOwners, e)
	}
	sort.Strings(envOwners)
	for _, u := range users {
		super := s.SuperAdmins[store.NormalizeEmail(u.Email)]
		if u.Role != store.RoleOwner && !u.Builtin && !super {
			continue
		}
		src := "role"
		switch {
		case u.Builtin:
			src = "builtin"
		case u.Role != store.RoleOwner:
			src = "env"
		}
		owners = append(owners, ownerAccount{u.ID, u.Email, u.Name, u.Builtin, u.Disabled, u.TOTPEnabled, super, src, u.LastLoginAt, u.CreatedAt})
	}
	sessions, err := s.Store.ListAllSessions(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	tokens, err := s.Store.CountUserAPITokens(ctx, "")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	disabled := s.disabledAdminCaps(ctx)
	caps := []map[string]any{}
	for _, c := range adminCaps {
		caps = append(caps, map[string]any{"id": c.ID, "label": c.Label, "allowed": !slices.Contains(disabled, c.ID)})
	}
	var ann announcement
	s.loadOwnerSetting(ctx, settingAnnouncement, &ann)
	if ann.Level == "" {
		ann.Level = "info"
	}
	var dbBytes int64
	dir := s.dataDir()
	if dir != "" {
		if fi, err := os.Stat(filepath.Join(dir, "keel.db")); err == nil {
			dbBytes = fi.Size()
		}
	}
	writeJSON(w, 200, map[string]any{
		"owners":       owners,
		"superadmins":  envOwners,
		"maintenance":  s.maintenance(ctx),
		"announcement": ann,
		"admin_caps":   caps,
		"counts":       map[string]int{"sessions": len(sessions), "api_tokens": tokens, "users": len(users)},
		"server": map[string]any{
			"version": s.Version, "node": s.NodeName, "go_version": runtime.Version(), "pid": os.Getpid(),
			"started_at": serverStartedAt, "uptime_seconds": int64(time.Since(serverStartedAt).Seconds()),
			"systemd": os.Getenv("INVOCATION_ID") != "", "data_dir": dir, "db_bytes": dbBytes,
			"public_url": s.PublicURL,
			// 비밀값은 설정 여부만 보여 준다.
			"admin_password_set": s.AdminPassword != "", "legacy_api_token_set": s.APIToken != "",
			"github_webhook_secret_set": s.GitHubSecret != "", "github_oauth_configured": s.GitHubClientID != "" && s.GitHubClientSecret != "",
			"legacy_agents_disabled": s.DisableLegacyAgents, "secure_cookie": s.SecureCookie,
			"trusted_proxies": len(s.TrustedProxies), "smtp_configured": s.mailer() != nil,
		},
	})
}

// ---- 관리자 권한 범위 ----

func (s *Server) putAdminCaps(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Disabled []string `json:"disabled"`
	}
	if !decode(w, r, &body) {
		return
	}
	next := []string{}
	for _, id := range body.Disabled {
		if !validAdminCap(id) {
			writeErr(w, 400, "알 수 없는 기능: "+id)
			return
		}
		if !slices.Contains(next, id) {
			next = append(next, id)
		}
	}
	if err := s.saveOwnerSetting(r.Context(), settingAdminCaps, adminCapsSetting{next}); err != nil {
		s.storeErr(w, err)
		return
	}
	msg := "서버 관리자에게 모든 어드민 기능을 허용했습니다"
	if len(next) > 0 {
		msg = "서버 관리자에게 차단한 기능: " + strings.Join(next, ", ")
	}
	s.audit(r, "owner.admin_caps", "instance", msg)
	writeJSON(w, 200, map[string]any{"disabled": next})
}

// ---- 점검 모드 ----

func (s *Server) putMaintenance(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled       bool   `json:"enabled"`
		Message       string `json:"message"`
		BlockLogins   bool   `json:"block_logins"`
		PauseWebhooks bool   `json:"pause_webhooks"`
	}
	if !decode(w, r, &body) {
		return
	}
	m, err := s.setMaintenance(r, body.Enabled, body.Message, body.BlockLogins, body.PauseWebhooks)
	if err != nil {
		if errors.Is(err, errBadInput) {
			writeErr(w, 400, err.Error())
			return
		}
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, m)
}

var errBadInput = errors.New("메시지는 500자 이하 한 단락이어야 합니다")

func validNotice(msg string) bool {
	return utf8.RuneCountInString(msg) <= 500 && !strings.ContainsAny(msg, "\r\t")
}

func (s *Server) setMaintenance(r *http.Request, enabled bool, msg string, blockLogins, pauseWebhooks bool) (maintenanceState, error) {
	ctx := r.Context()
	msg = strings.TrimSpace(msg)
	if !validNotice(msg) {
		return maintenanceState{}, errBadInput
	}
	prev := s.maintenance(ctx)
	m := maintenanceState{Enabled: enabled, Message: msg, BlockLogins: blockLogins, PauseWebhooks: pauseWebhooks}
	if enabled {
		m.StartedAt, m.StartedBy = prev.StartedAt, prev.StartedBy
		if !prev.Enabled || m.StartedAt == nil {
			now := time.Now().UTC()
			m.StartedAt, m.StartedBy = &now, principalFrom(r).display()
		}
	}
	if err := s.saveOwnerSetting(ctx, settingMaintenance, m); err != nil {
		return m, err
	}
	switch {
	case enabled && !prev.Enabled:
		s.audit(r, "owner.maintenance", "instance", fmt.Sprintf("점검 모드를 켰습니다(로그인 차단 %s, 웹훅 배포 중지 %s)", yesNo(blockLogins), yesNo(pauseWebhooks)))
	case !enabled && prev.Enabled:
		s.audit(r, "owner.maintenance", "instance", "점검 모드를 껐습니다")
	case enabled:
		s.audit(r, "owner.maintenance", "instance", fmt.Sprintf("점검 모드 설정을 바꿨습니다(로그인 차단 %s, 웹훅 배포 중지 %s)", yesNo(blockLogins), yesNo(pauseWebhooks)))
	}
	if m.Enabled && m.Message == "" {
		m.Message = defaultMaintenanceMessage
	}
	return m, nil
}

// ---- 전체 공지 ----

func (s *Server) putAnnouncement(w http.ResponseWriter, r *http.Request) {
	var body announcement
	if !decode(w, r, &body) {
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if !validNotice(body.Message) {
		writeErr(w, 400, errBadInput.Error())
		return
	}
	if body.Level == "" {
		body.Level = "info"
	}
	if body.Level != "info" && body.Level != "warning" && body.Level != "critical" {
		writeErr(w, 400, "level은 info, warning, critical 중 하나입니다")
		return
	}
	if body.Enabled && body.Message == "" {
		writeErr(w, 400, "공지 내용을 입력하세요")
		return
	}
	now := time.Now().UTC()
	body.UpdatedAt = &now
	if err := s.saveOwnerSetting(r.Context(), settingAnnouncement, body); err != nil {
		s.storeErr(w, err)
		return
	}
	if body.Enabled {
		s.audit(r, "owner.announcement", "instance", "전체 공지를 게시했습니다: "+truncate(body.Message, 120))
	} else {
		s.audit(r, "owner.announcement", "instance", "전체 공지를 내렸습니다")
	}
	writeJSON(w, 200, body)
}

// ---- 전체 세션 ----

func (s *Server) listAllSessions(w http.ResponseWriter, r *http.Request) {
	ses, err := s.Store.ListAllSessions(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	cur := principalFrom(r).SessionID
	type view struct {
		*store.SessionWithUser
		Current bool `json:"current"`
	}
	out := make([]view, 0, len(ses))
	for _, se := range ses {
		out = append(out, view{se, se.ID == cur})
	}
	writeJSON(w, 200, out)
}

func (s *Server) deleteAnySession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sesid")
	if id == principalFrom(r).SessionID {
		writeErr(w, 400, "지금 쓰는 세션은 여기서 끊을 수 없습니다(로그아웃하세요)")
		return
	}
	if err := s.Store.DeleteSessionByID(r.Context(), id); err != nil {
		s.storeErr(w, err)
		return
	}
	s.kickInboxStreams()
	s.audit(r, "owner.session_revoked", id, "세션 하나를 강제로 끊었습니다")
	w.WriteHeader(204)
}

// revokeSessions는 {user_id}의 세션 전부, 또는 {scope:"non_owners"}면 owner가 아닌 모든 사용자의 세션을 끊는다.
// 요청한 owner의 현재 세션은 남긴다.
func (s *Server) revokeSessions(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		UserID string `json:"user_id"`
		Scope  string `json:"scope"`
	}
	if !decode(w, r, &body) {
		return
	}
	p := principalFrom(r)
	switch {
	case body.UserID != "":
		u, err := s.Store.GetUser(ctx, body.UserID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if err := s.Store.DeleteUserSessions(ctx, u.ID, p.SessionID); err != nil {
			s.storeErr(w, err)
			return
		}
		s.kickInboxStreams()
		s.audit(r, "owner.sessions_revoked", u.ID, u.Email+"의 모든 세션을 끊었습니다")
		writeJSON(w, 200, map[string]any{"ok": true})
	case body.Scope == "non_owners":
		keep, err := s.superAdminIDs(ctx)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		n, err := s.Store.DeleteSessionsExceptOwners(ctx, keep)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		s.kickInboxStreams()
		s.audit(r, "owner.sessions_revoked", "instance", fmt.Sprintf("owner가 아닌 사용자의 세션 %d개를 끊었습니다", n))
		writeJSON(w, 200, map[string]any{"revoked": n})
	default:
		writeErr(w, 400, `user_id 또는 scope:"non_owners"가 필요합니다`)
	}
}

// ---- 전체 API 토큰 ----

func (s *Server) listAllTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	toks, err := s.Store.ListAPITokens(ctx, "")
	if err != nil {
		s.storeErr(w, err)
		return
	}
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	byID := map[string]*store.User{}
	for _, u := range users {
		byID[u.ID] = u
	}
	type view struct {
		*store.APIToken
		Email    string `json:"email"`
		UserName string `json:"user_name"`
		UserRole string `json:"user_role"`
	}
	out := make([]view, 0, len(toks))
	for _, t := range toks {
		v := view{APIToken: t}
		if u := byID[t.UserID]; u != nil {
			v.Email, v.UserName, v.UserRole = u.Email, u.Name, u.Role
		}
		out = append(out, v)
	}
	writeJSON(w, 200, out)
}

func (s *Server) deleteAnyToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	t, err := s.Store.GetAPIToken(ctx, r.PathValue("tid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.DeleteAPIToken(ctx, t.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "owner.token_revoked", t.ID, fmt.Sprintf("API 토큰 %s(%s…)을 폐기했습니다", t.Name, t.Prefix))
	w.WriteHeader(204)
}

func (s *Server) revokeTokens(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		UserID string `json:"user_id"`
		Scope  string `json:"scope"`
	}
	if !decode(w, r, &body) {
		return
	}
	switch {
	case body.UserID != "":
		u, err := s.Store.GetUser(ctx, body.UserID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if err := s.Store.DeleteUserAPITokens(ctx, u.ID); err != nil {
			s.storeErr(w, err)
			return
		}
		s.audit(r, "owner.tokens_revoked", u.ID, u.Email+"의 API 토큰을 모두 폐기했습니다")
		writeJSON(w, 200, map[string]any{"ok": true})
	case body.Scope == "non_owners":
		keep, err := s.superAdminIDs(ctx)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		n, err := s.Store.DeleteAPITokensExceptOwners(ctx, keep)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		s.audit(r, "owner.tokens_revoked", "instance", fmt.Sprintf("owner가 아닌 사용자의 API 토큰 %d개를 폐기했습니다", n))
		writeJSON(w, 200, map[string]any{"revoked": n})
	default:
		writeErr(w, 400, `user_id 또는 scope:"non_owners"가 필요합니다`)
	}
}

// ---- 소유권 이전 ----

// transferOwnership은 대상 계정을 서버 owner로 올리고, stay_owner가 아니면 요청한 owner를 서버 관리자(admin)로 내린다.
// 기본 계정과 KEEL_SUPERADMINS는 역할과 무관하게 owner라 내려가지 않는다(stay_owner와 같다).
func (s *Server) transferOwnership(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(r)
	var body struct {
		UserID    string `json:"user_id"`
		StayOwner bool   `json:"stay_owner"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.UserID == "" || body.UserID == p.User.ID {
		writeErr(w, 400, "소유권을 받을 다른 계정을 고르세요")
		return
	}
	target, err := s.Store.GetUser(ctx, body.UserID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if target.Disabled {
		writeErr(w, 409, "비활성화된 계정에는 소유권을 넘길 수 없습니다")
		return
	}
	demote := ""
	if !body.StayOwner && !p.User.Builtin && p.User.Role == store.RoleOwner {
		demote = p.User.ID
	}
	if err := s.Store.TransferOwnership(ctx, target.ID, demote); err != nil {
		if errors.Is(err, store.ErrOwnership) {
			writeErr(w, 409, err.Error())
			return
		}
		s.storeErr(w, err)
		return
	}
	stillOwner := demote == "" || s.isSuperAdmin(p.User)
	msg := target.Email + "에게 서버 owner를 줬습니다"
	if !stillOwner {
		msg = target.Email + "에게 서버 소유권을 넘기고 자신은 서버 관리자가 됐습니다"
	}
	s.audit(r, "owner.transferred", target.ID, msg)
	writeJSON(w, 200, map[string]any{"owner": target.ID, "still_owner": stillOwner})
}

// ---- 비상 잠금 ----

// lockdown은 점검 모드(로그인 차단, 웹훅 배포 중지)를 켜고 owner가 아닌 모든 세션과 API 토큰을 끊는다.
func (s *Server) lockdown(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body struct {
		Message string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	msg := strings.TrimSpace(body.Message)
	if msg == "" {
		msg = "보안 점검을 위해 서버를 잠갔습니다. 서버 owner에게 문의하세요."
	}
	if _, err := s.setMaintenance(r, true, msg, true, true); err != nil {
		if errors.Is(err, errBadInput) {
			writeErr(w, 400, err.Error())
			return
		}
		s.storeErr(w, err)
		return
	}
	keep, err := s.superAdminIDs(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	ns, err := s.Store.DeleteSessionsExceptOwners(ctx, keep)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	nt, err := s.Store.DeleteAPITokensExceptOwners(ctx, keep)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.kickInboxStreams()
	s.audit(r, "owner.lockdown", "instance", fmt.Sprintf("비상 잠금: 세션 %d개, API 토큰 %d개를 끊고 점검 모드를 켰습니다", ns, nt))
	writeJSON(w, 200, map[string]any{"sessions_revoked": ns, "tokens_revoked": nt})
}

// ---- aven DB 백업 ----

var backupName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,120}\.db$`)

func (s *Server) backupDir() (string, bool) {
	dir := s.dataDir()
	if dir == "" {
		return "", false
	}
	return filepath.Join(dir, "backups"), true
}

type keelBackup struct {
	Name       string    `json:"name"`
	Bytes      int64     `json:"bytes"`
	ModifiedAt time.Time `json:"modified_at"`
}

func (s *Server) listKeelBackups(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.backupDir()
	if !ok {
		writeErr(w, 503, "데이터 디렉터리를 알 수 없습니다")
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		s.storeErr(w, err)
		return
	}
	out := []keelBackup{}
	for _, e := range entries {
		if !e.Type().IsRegular() || !backupName.MatchString(e.Name()) {
			continue
		}
		if fi, err := e.Info(); err == nil {
			out = append(out, keelBackup{e.Name(), fi.Size(), fi.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModifiedAt.After(out[j].ModifiedAt) })
	writeJSON(w, 200, out)
}

func (s *Server) createKeelBackup(w http.ResponseWriter, r *http.Request) {
	dir, ok := s.backupDir()
	if !ok {
		writeErr(w, 503, "데이터 디렉터리를 알 수 없습니다")
		return
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.storeErr(w, err)
		return
	}
	name := "keel-owner-" + time.Now().UTC().Format("20060102-150405") + ".db"
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err == nil {
		writeErr(w, 409, "같은 이름의 백업이 있습니다. 잠시 뒤 다시 시도하세요")
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Minute)
	defer cancel()
	if err := s.Store.BackupTo(ctx, path); err != nil {
		os.Remove(path)
		s.storeErr(w, err)
		return
	}
	os.Chmod(path, 0o600)
	fi, err := os.Stat(path)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "owner.backup_created", name, fmt.Sprintf("aven DB를 백업했습니다(%d bytes)", fi.Size()))
	writeJSON(w, 201, keelBackup{name, fi.Size(), fi.ModTime()})
}

func (s *Server) keelBackupPath(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	dir, ok := s.backupDir()
	if !ok {
		writeErr(w, 503, "데이터 디렉터리를 알 수 없습니다")
		return "", "", false
	}
	name := r.PathValue("name")
	if !backupName.MatchString(name) {
		writeErr(w, 400, "잘못된 백업 이름입니다")
		return "", "", false
	}
	path := filepath.Join(dir, name)
	if fi, err := os.Lstat(path); err != nil || !fi.Mode().IsRegular() {
		writeErr(w, 404, "not found")
		return "", "", false
	}
	return name, path, true
}

func (s *Server) downloadKeelBackup(w http.ResponseWriter, r *http.Request) {
	name, path, ok := s.keelBackupPath(w, r)
	if !ok {
		return
	}
	f, err := os.Open(path)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "owner.backup_downloaded", name, "aven DB 백업을 내려받았습니다")
	w.Header().Set("Content-Type", "application/vnd.sqlite3")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

func (s *Server) deleteKeelBackup(w http.ResponseWriter, r *http.Request) {
	name, path, ok := s.keelBackupPath(w, r)
	if !ok {
		return
	}
	if err := os.Remove(path); err != nil {
		s.storeErr(w, err)
		return
	}
	// VACUUM INTO 사본에는 없지만 sqlite3 .backup으로 만든 파일 옆에 남은 WAL/SHM도 같이 지운다.
	os.Remove(path + "-wal")
	os.Remove(path + "-shm")
	s.audit(r, "owner.backup_deleted", name, "aven DB 백업을 지웠습니다")
	w.WriteHeader(204)
}

// ---- aven-server 재시작 ----

// restartServer는 systemd 아래에서 돌 때만 자기에게 SIGTERM을 보내 정상 종료한다(Restart=always가 다시 띄운다).
// 컨테이너는 KillMode=process라 그대로 돈다.
func (s *Server) restartServer(w http.ResponseWriter, r *http.Request) {
	if s.restartFn == nil && os.Getenv("INVOCATION_ID") == "" {
		writeErr(w, 409, "systemd 서비스로 실행 중이 아니라 여기서 재시작할 수 없습니다")
		return
	}
	s.audit(r, "owner.restart", "instance", "aven-server를 재시작했습니다")
	writeJSON(w, 202, map[string]any{"restarting": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	restart := s.restartFn
	if restart == nil {
		restart = func() { syscall.Kill(os.Getpid(), syscall.SIGTERM) }
	}
	time.AfterFunc(500*time.Millisecond, restart)
}

func yesNo(v bool) string {
	if v {
		return "켬"
	}
	return "끔"
}

// listGitHubConnectionOwners는 GitHub를 연결한 활성 사용자 목록이다(서버 owner가 프로젝트에 다른 사람의 연결을 지정할 때).
func (s *Server) listGitHubConnectionOwners(w http.ResponseWriter, r *http.Request) {
	out, err := s.Store.ListGitHubConnectionOwners(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}

// forceWorkspaceOwner는 서버 owner가 작업 공간 owner를 강제로 바꾼다(퇴사자 정리 등). 개인 작업 공간 owner 고정과
// 마지막 owner 유지 규칙을 우회하며, 개인 작업 공간은 팀 작업 공간이 된다. previous: keep | demote | remove.
func (s *Server) forceWorkspaceOwner(w http.ResponseWriter, r *http.Request) {
	ws, err := s.Store.GetWorkspace(r.Context(), r.PathValue("wsid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var in struct {
		UserID   string `json:"user_id"`
		Previous string `json:"previous"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Previous == "" {
		in.Previous = store.PrevOwnerKeep
	}
	if in.Previous != store.PrevOwnerKeep && in.Previous != store.PrevOwnerDemote && in.Previous != store.PrevOwnerRemove {
		writeErr(w, 400, "previous는 keep, demote, remove 중 하나입니다")
		return
	}
	u, err := s.Store.GetUser(r.Context(), in.UserID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (u.Disabled || u.Builtin)) {
		writeErr(w, 400, "새 owner는 활성 사용자여야 합니다")
		return
	} else if err != nil {
		s.storeErr(w, err)
		return
	}
	prev, converted, err := s.Store.ForceWorkspaceOwner(r.Context(), ws.ID, u.ID, in.Previous)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	names := make([]string, 0, len(prev))
	for _, id := range prev {
		if p, err := s.Store.GetUser(r.Context(), id); err == nil {
			names = append(names, p.Display())
		} else {
			names = append(names, id)
		}
	}
	action := map[string]string{store.PrevOwnerKeep: "그대로 두었습니다", store.PrevOwnerDemote: "admin으로 낮췄습니다", store.PrevOwnerRemove: "내보냈습니다"}[in.Previous]
	msg := fmt.Sprintf("작업 공간 %s의 owner로 %s을(를) 지정했습니다", ws.Name, u.Display())
	if len(names) > 0 {
		msg += fmt.Sprintf(". 기존 owner %s은(는) %s", strings.Join(names, ", "), action)
	}
	if converted {
		msg += ". 개인 작업 공간을 팀 작업 공간으로 바꿨습니다"
	}
	s.audit(r, "owner.workspace_owner_changed", ws.ID, msg)
	writeJSON(w, 200, map[string]any{"previous_owners": prev, "converted": converted})
}
