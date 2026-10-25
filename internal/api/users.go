package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/store"
)

// ---- 사용자, 초대, 세션, 2FA, API 토큰, 프로젝트 멤버, 감사 기록 ----

const (
	inviteTTL      = 7 * 24 * time.Hour
	apiTokenPrefix = "keel_"
	maxTokens      = 100
)

// audit은 작업 공간 수준 기록을 남긴다(실패해도 요청을 막지 않는다).
func (s *Server) audit(r *http.Request, kind, target, msg string) {
	if s.Store == nil {
		return
	}
	a := &store.AuditEntry{Kind: kind, Target: target, Message: msg, IP: limitKey(s.clientIP(r))}
	if p := principalFrom(r); p != nil {
		a.ActorID, a.Actor = p.User.ID, p.display()
	}
	if err := s.Store.CreateAudit(context.WithoutCancel(r.Context()), a); err != nil {
		s.Log.Warn("audit", "err", err)
	}
}

func validName(n string) bool {
	return n != "" && utf8.RuneCountInString(n) <= 100 && !strings.ContainsAny(n, "\n\r\t")
}

func validEmail(e string) bool {
	at := strings.LastIndexByte(e, '@')
	return len(e) <= 254 && at > 0 && at < len(e)-1 && !strings.ContainsAny(e, " \t\r\n<>\",;")
}

// ---- /auth/me ----

type tokenInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	Scope         string  `json:"scope"`
	ProjectID     *string `json:"project_id"`
	EnvironmentID *string `json:"environment_id"`
	WorkspaceID   *string `json:"workspace_id"`
	Role          string  `json:"role"`
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	u := p.User
	if p.OAuth != nil {
		// OAuth 토큰에는 승인한 scope의 claim만 보인다(/oauth/me와 같다).
		out := oauthUserClaims(u, p.OAuth.Scopes)
		out["id"], out["auth"], out["scopes"] = u.ID, p.Via, p.OAuth.Scopes
		writeJSON(w, http.StatusOK, out)
		return
	}
	enforced, setupRequired, err := s.mfaSetupRequired(r.Context(), u)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	passkeyCount := 0
	if s.Store != nil {
		passkeyCount, err = s.Store.CountPasskeys(r.Context(), u.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
	}
	out := map[string]any{
		// user는 v1 호환 필드다(예전에는 항상 "admin").
		"user": u.Display(), "id": u.ID, "email": u.Email, "name": u.Name, "role": u.Role,
		"totp_enabled": u.TOTPEnabled, "builtin": u.Builtin, "auth": p.Via, "token": nil,
		"passkey_count": passkeyCount, "mfa_enabled": u.TOTPEnabled || passkeyCount > 0,
		"mfa_enforced": enforced, "mfa_setup_required": setupRequired && p.Via == viaSession,
		"instance_admin": s.instanceLevel(p) >= lvAdmin, "instance_role": u.Role,
		"workspace_admin": false, "can_create_projects": false,
	}
	if p.ViewAsBy != nil {
		out["view_as"] = map[string]any{"by": p.ViewAsBy.Display(), "by_id": p.ViewAsBy.ID, "until": p.ViewAsUntil}
		out["mfa_setup_required"] = false
	}
	if t := p.Token; t != nil {
		out["token"] = tokenInfo{t.ID, t.Name, t.Scope, t.ProjectID, t.EnvironmentID, t.WorkspaceID, t.Role}
	}
	projects := map[string]string{}
	if s.Store != nil {
		ps, err := s.Store.ListProjects(r.Context())
		if err != nil {
			s.storeErr(w, err)
			return
		}
		for _, pr := range ps {
			if lv, _ := s.projectLevel(r.Context(), p, pr.ID, ""); lv > lvNone {
				// environment 토큰은 프로젝트 수준에서 viewer로 보이지만 자기 환경에서는 토큰 역할만큼 쓸 수 있다.
				projects[pr.ID] = levelRole(lv)
			}
		}
	}
	out["projects"] = projects
	out["instance_owner"] = p.OAuth == nil && s.instanceLevel(p) >= lvOwner
	out["admin_caps"] = s.allowedAdminCaps(r.Context(), p)
	if s.isSuperAdmin(u) {
		out["instance_role"] = store.RoleOwner
	}
	if s.Store != nil {
		wss, err := s.visibleWorkspaces(r.Context(), p)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		// 개인 작업 공간이 있으므로 member 이상인 작업 공간이 하나라도 있으면 프로젝트를 만들 수 있다.
		for _, ws := range wss {
			if ws.Default {
				out["workspace_admin"] = roleLevel(ws.Role) >= lvAdmin
			}
			if roleLevel(ws.Role) >= lvMember {
				out["can_create_projects"] = true
			}
		}
		out["workspaces"] = wss
	}
	// 처음 로그인한 사용자에게 GitHub 연결을 권하려고 쓴다(웹 UI의 GitHubOnboarding).
	out["github_app_configured"] = s.GitHubClientID != "" && s.GitHubClientSecret != ""
	out["github_connected"] = s.Deploy != nil && s.Deploy.UserGitHubConnection(r.Context(), u.ID) != nil
	writeJSON(w, 200, out)
}

func (s *Server) patchMe(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(r)
	var body struct {
		Name            *string `json:"name"`
		CurrentPassword string  `json:"current_password"`
		NewPassword     *string `json:"new_password"`
	}
	if !decode(w, r, &body) {
		return
	}
	u, err := s.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if !validName(n) {
			writeErr(w, 400, "name은 1..100자여야 합니다")
			return
		}
		u.Name = n
	}
	pwChanged := false
	if body.NewPassword != nil {
		if u.Builtin {
			writeErr(w, 400, "기본 admin 계정의 비밀번호는 KEEL_ADMIN_PASSWORD 환경변수로 바꿉니다")
			return
		}
		acct := "pw:" + u.ID
		if s.lockedOut(w, r, acct, "비밀번호 확인 시도가 너무 많습니다") {
			return
		}
		if !s.checkUserPassword(u, body.CurrentPassword) {
			s.authFailed(w, r, acct, "비밀번호 확인 시도가 너무 많습니다", "현재 비밀번호가 올바르지 않습니다")
			return
		}
		s.authPassed(r, acct)
		if err := checkPasswordPolicy(*body.NewPassword); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		h, err := HashPassword(*body.NewPassword)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		u.PasswordHash, pwChanged = h, true
	}
	if err := s.Store.UpdateUser(ctx, u); err != nil {
		s.storeErr(w, err)
		return
	}
	if pwChanged {
		// 비밀번호를 바꾸면 지금 세션을 뺀 모든 세션을 끊는다.
		s.Store.DeleteUserSessions(ctx, u.ID, p.SessionID)
		s.kickInboxStreams()
		s.audit(r, "user.updated", u.ID, u.Display()+" 비밀번호 변경")
	} else if body.Name != nil {
		s.audit(r, "user.updated", u.ID, u.Display()+" 프로필 변경")
	}
	writeJSON(w, 200, u)
}

// ---- sessions ----

func (s *Server) listMySessions(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	ses, err := s.Store.ListSessions(r.Context(), p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	type view struct {
		*store.Session
		Current bool `json:"current"`
	}
	out := []view{}
	for _, se := range ses {
		out = append(out, view{se, se.ID == p.SessionID})
	}
	writeJSON(w, 200, out)
}

func (s *Server) deleteMySession(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	if err := s.Store.DeleteSession(r.Context(), p.User.ID, r.PathValue("sesid")); err != nil {
		s.storeErr(w, err)
		return
	}
	s.kickInboxStreams()
	w.WriteHeader(204)
}

// ---- 2FA (TOTP) ----

func (s *Server) totpSetup(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	u, err := s.Store.GetUser(r.Context(), p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if u.TOTPEnabled {
		writeErr(w, 409, "2단계 인증이 이미 켜져 있습니다")
		return
	}
	secret := NewTOTPSecret()
	if err := s.Store.SetUserTOTP(r.Context(), u.ID, secret, false); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"secret": secret, "otpauth_url": OTPAuthURL("aven", u.Email, secret)})
}

// verifyCode는 rate limit을 걸어 TOTP/복구 코드를 확인한다(실패 응답까지 쓴다).
func (s *Server) verifyCode(w http.ResponseWriter, r *http.Request, u *store.User, code string, allowRecovery bool) bool {
	acct := "2fa:" + u.ID
	if s.lockedOut(w, r, acct, "인증 코드 시도가 너무 많습니다") {
		return false
	}
	var ok bool
	var err error
	if !allowRecovery && len(strings.TrimSpace(code)) != totpDigits {
		ok = false
	} else {
		ok, err = s.checkSecondFactor(r.Context(), u, code)
	}
	if err != nil {
		s.storeErr(w, err)
		return false
	}
	if !ok {
		s.authFailed(w, r, acct, "인증 코드 시도가 너무 많습니다", "인증 코드가 올바르지 않습니다")
		return false
	}
	s.authPassed(r, acct)
	return true
}

func (s *Server) newRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	codes := NewRecoveryCodes()
	hashes := make([]string, len(codes))
	for i, c := range codes {
		hashes[i] = hashSecret(c)
	}
	return codes, s.Store.SetRecoveryCodes(ctx, userID, hashes)
}

func (s *Server) totpEnable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(r)
	var body struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &body) {
		return
	}
	u, err := s.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if u.TOTPEnabled {
		writeErr(w, 409, "2단계 인증이 이미 켜져 있습니다")
		return
	}
	if u.TOTPSecret == "" {
		writeErr(w, 400, "먼저 POST /api/auth/2fa/setup으로 비밀을 만드세요")
		return
	}
	acct := "2fa:" + u.ID
	if s.lockedOut(w, r, acct, "인증 코드 시도가 너무 많습니다") {
		return
	}
	step, ok := MatchTOTP(u.TOTPSecret, body.Code, time.Now())
	if !ok {
		s.authFailed(w, r, acct, "인증 코드 시도가 너무 많습니다", "인증 코드가 올바르지 않습니다")
		return
	}
	s.authPassed(r, acct)
	if err := s.Store.SetUserTOTP(ctx, u.ID, u.TOTPSecret, true); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Store.UseTOTPStep(ctx, u.ID, step)
	codes, err := s.newRecoveryCodes(ctx, u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user.updated", u.ID, u.Display()+" 2단계 인증 켬")
	writeJSON(w, 200, map[string]any{"recovery_codes": codes})
}

func (s *Server) totpDisable(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(r)
	var body struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	u, err := s.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !u.TOTPEnabled {
		if u.TOTPSecret != "" { // 켜지 않은 설정 중 비밀은 그냥 지운다
			s.Store.SetUserTOTP(ctx, u.ID, "", false)
		}
		writeErr(w, 400, "2단계 인증이 꺼져 있습니다")
		return
	}
	switch {
	case body.Code != "":
		if !s.verifyCode(w, r, u, body.Code, true) {
			return
		}
	case body.Password != "":
		acct := "pw:" + u.ID
		if s.lockedOut(w, r, acct, "비밀번호 확인 시도가 너무 많습니다") {
			return
		}
		if !s.checkUserPassword(u, body.Password) {
			s.authFailed(w, r, acct, "비밀번호 확인 시도가 너무 많습니다", "비밀번호가 올바르지 않습니다")
			return
		}
		s.authPassed(r, acct)
	default:
		writeErr(w, 400, "code 또는 password가 필요합니다")
		return
	}
	passkeys, err := s.Store.CountPasskeys(ctx, u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	enforced, err := s.Store.Workspace2FAEnforced(ctx)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if enforced && passkeys == 0 {
		writeErr(w, 409, "작업 공간에서 2단계 인증을 요구하므로 마지막 인증 방법을 끌 수 없습니다")
		return
	}
	// passkey가 남아 있으면 복구 코드도 남긴다(passkey와 함께 쓰는 수단이다). 마지막 수단이면 함께 지운다.
	disable := s.Store.ClearUserTOTP
	if passkeys == 0 {
		disable = func(ctx context.Context, id string) error { return s.Store.SetUserTOTP(ctx, id, "", false) }
	}
	if err := disable(ctx, u.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user.updated", u.ID, u.Display()+" 2단계 인증 끔")
	w.WriteHeader(204)
}

func (s *Server) totpRecoveryCodes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(r)
	var body struct {
		Code     string `json:"code"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	u, err := s.Store.GetUser(ctx, p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	hasFactor, err := s.hasMFAFactor(ctx, u)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !hasFactor {
		writeErr(w, 400, "2단계 인증이 꺼져 있습니다")
		return
	}
	if body.Code != "" {
		if !s.verifyCode(w, r, u, body.Code, false) {
			return
		}
	} else if body.Password != "" && !u.TOTPEnabled {
		// 인증 앱이 없는(passkey만 쓰는) 계정은 현재 비밀번호로 확인한다.
		acct := "pw:" + u.ID
		if s.lockedOut(w, r, acct, "비밀번호 확인 시도가 너무 많습니다") {
			return
		}
		if !s.checkUserPassword(u, body.Password) {
			s.authFailed(w, r, acct, "비밀번호 확인 시도가 너무 많습니다", "비밀번호가 올바르지 않습니다")
			return
		}
		s.authPassed(r, acct)
	} else {
		if u.TOTPEnabled {
			writeErr(w, 400, "인증 앱의 code가 필요합니다")
		} else {
			writeErr(w, 400, "password가 필요합니다")
		}
		return
	}
	codes, err := s.newRecoveryCodes(ctx, u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user.updated", u.ID, u.Display()+" 복구 코드 다시 만듦")
	writeJSON(w, 200, map[string]any{"recovery_codes": codes})
}

// ---- users (인스턴스 admin) ----

func (s *Server) listUsers(w http.ResponseWriter, r *http.Request) {
	us, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	for _, u := range us {
		if u.Builtin {
			u.Role = store.RoleOwner
		}
	}
	counts, err := s.Store.PasskeyCounts(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	out := make([]mfaUserView, len(us))
	for i, u := range us {
		out[i] = mfaUserView{u, counts[u.ID]}
	}
	writeJSON(w, 200, out)
}

func (s *Server) getUser(w http.ResponseWriter, r *http.Request) {
	u, err := s.Store.GetUser(r.Context(), r.PathValue("uid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	n, err := s.Store.CountPasskeys(r.Context(), u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, mfaUserView{u, n})
}

// canManageUser: owner 계정(과 owner 역할 부여)은 owner만 바꿀 수 있다.
func (s *Server) canManageUser(actor *principal, target *store.User) bool {
	return target.Role != store.RoleOwner || s.instanceLevel(actor) >= lvOwner
}

func (s *Server) patchUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := principalFrom(r)
	u, err := s.Store.GetUser(ctx, r.PathValue("uid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var body struct {
		Name     *string `json:"name"`
		Role     *string `json:"role"`
		Disabled *bool   `json:"disabled"`
		Password *string `json:"password"`
		Reset2FA bool    `json:"reset_2fa"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.canManageUser(actor, u) {
		writeErr(w, 403, "owner 계정은 owner만 바꿀 수 있습니다")
		return
	}
	wasActiveOwner := u.Role == store.RoleOwner && !u.Disabled
	var changes []string
	if body.Name != nil {
		n := strings.TrimSpace(*body.Name)
		if !validName(n) {
			writeErr(w, 400, "name은 1..100자여야 합니다")
			return
		}
		u.Name = n
		changes = append(changes, "이름")
	}
	if body.Role != nil && *body.Role != u.Role {
		switch {
		case !store.ValidWorkspaceRole(*body.Role):
			writeErr(w, 400, "role must be owner, admin, member or viewer")
			return
		case u.Builtin:
			writeErr(w, 400, "기본 admin 계정은 항상 owner입니다")
			return
		case *body.Role == store.RoleOwner && s.instanceLevel(actor) < lvOwner:
			writeErr(w, 403, "owner 역할은 owner만 줄 수 있습니다")
			return
		}
		u.Role = *body.Role
		changes = append(changes, "역할 "+u.Role)
	}
	if body.Disabled != nil && *body.Disabled != u.Disabled {
		if *body.Disabled && u.ID == actor.User.ID {
			writeErr(w, 400, "자기 계정은 비활성화할 수 없습니다")
			return
		}
		u.Disabled = *body.Disabled
		if u.Disabled {
			changes = append(changes, "비활성화")
		} else {
			changes = append(changes, "활성화")
		}
	}
	if body.Password != nil {
		if u.Builtin {
			writeErr(w, 400, "기본 admin 계정의 비밀번호는 KEEL_ADMIN_PASSWORD 환경변수로 바꿉니다")
			return
		}
		if err := checkPasswordPolicy(*body.Password); err != nil {
			writeErr(w, 400, err.Error())
			return
		}
		h, err := HashPassword(*body.Password)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		u.PasswordHash = h
		changes = append(changes, "비밀번호 재설정")
	}
	if wasActiveOwner && (u.Role != store.RoleOwner || u.Disabled) {
		n, err := s.Store.CountActiveOwners(ctx)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if n <= 1 {
			writeErr(w, 409, "마지막 owner는 강등하거나 비활성화할 수 없습니다")
			return
		}
	}
	if err := s.Store.UpdateUser(ctx, u); err != nil {
		s.storeErr(w, err)
		return
	}
	if body.Reset2FA {
		// TOTP, passkey, 복구 코드를 모두 지운다(작업 공간이 2FA를 요구하면 다음 요청부터 다시 설정해야 한다).
		if err := s.Store.ResetUserMFA(ctx, u.ID); err != nil {
			s.storeErr(w, err)
			return
		}
		u.TOTPEnabled = false
		changes = append(changes, "2단계 인증 초기화")
	}
	if u.Disabled || body.Password != nil {
		s.Store.DeleteUserSessions(ctx, u.ID, "")
		s.kickInboxStreams()
	}
	if len(changes) > 0 {
		s.audit(r, "user.updated", u.ID, fmt.Sprintf("%s: %s", u.Email, strings.Join(changes, ", ")))
	}
	writeJSON(w, 200, u)
}

func (s *Server) deleteUser(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := principalFrom(r)
	u, err := s.Store.GetUser(ctx, r.PathValue("uid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	switch {
	case u.Builtin:
		writeErr(w, 400, "기본 admin 계정은 지울 수 없습니다(비활성화는 할 수 있습니다)")
		return
	case u.ID == actor.User.ID:
		writeErr(w, 400, "자기 계정은 지울 수 없습니다")
		return
	case !s.canManageUser(actor, u):
		writeErr(w, 403, "owner 계정은 owner만 지울 수 있습니다")
		return
	}
	if u.Role == store.RoleOwner && !u.Disabled {
		if n, err := s.Store.CountActiveOwners(ctx); err != nil || n <= 1 {
			writeErr(w, 409, "마지막 owner는 지울 수 없습니다")
			return
		}
	}
	if err := s.Store.DeleteUser(ctx, u.ID); err != nil {
		if errors.Is(err, store.ErrWorkspaceNotEmpty) {
			writeErr(w, 409, "이 사용자의 개인 작업 공간에 프로젝트가 남아 있습니다. 먼저 옮기거나 지우세요")
			return
		}
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user.deleted", u.ID, u.Email+" 삭제")
	w.WriteHeader(204)
}

// ---- invites ----

// publicBase는 사용자에게 보여 줄 링크의 앞부분이다(KEEL_PUBLIC_URL, 없으면 요청 Host).
func (s *Server) publicBase(r *http.Request) string {
	if s.PublicURL != "" {
		return strings.TrimRight(s.PublicURL, "/")
	}
	scheme := "http"
	if s.secureTransport(r) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (s *Server) createInvite(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	actor := principalFrom(r)
	var body struct {
		Email       string  `json:"email"`
		Role        string  `json:"role"`
		ProjectID   *string `json:"project_id"`
		ProjectRole *string `json:"project_role"`
		SendEmail   bool    `json:"send_email"`
	}
	if !decode(w, r, &body) {
		return
	}
	email := store.NormalizeEmail(body.Email)
	if !validEmail(email) || email == store.BuiltinUserEmail {
		writeErr(w, 400, "올바른 이메일이 필요합니다")
		return
	}
	if body.Role == "" {
		body.Role = store.RoleMember
	}
	if !store.ValidWorkspaceRole(body.Role) {
		writeErr(w, 400, "role must be owner, admin, member or viewer")
		return
	}
	wsLv := s.instanceLevel(actor)
	workspaceID := r.PathValue("wsid")
	var workspaceRole *string
	if workspaceID != "" {
		lv, err := s.workspaceAccess(ctx, actor, workspaceID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if lv < lvAdmin || roleLevel(body.Role) > lv {
			writeErr(w, 403, "작업 공간 역할을 부여할 권한이 없습니다")
			return
		}
		role := body.Role
		workspaceRole = &role
		body.Role = store.RoleMember // workspace invitations never grant instance administration
	}
	if workspaceID == "" && roleLevel(body.Role) > lvMember && roleLevel(body.Role) > wsLv {
		writeErr(w, 403, "자기보다 높은 역할로 초대할 수 없습니다")
		return
	}
	if body.ProjectID != nil && *body.ProjectID == "" {
		body.ProjectID = nil
	}
	if wsLv < lvAdmin && workspaceID == "" {
		// 인스턴스 admin이 아니면 자기가 admin인 프로젝트로만, 인스턴스 member/viewer 역할로만 초대할 수 있다.
		if body.ProjectID == nil || roleLevel(body.Role) > lvMember {
			writeErr(w, 403, "인스턴스 admin이 아니면 프로젝트 초대(member/viewer)만 할 수 있습니다")
			return
		}
	}
	if body.ProjectID != nil {
		project, err := s.Store.GetProject(ctx, *body.ProjectID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if workspaceID != "" && workspaceID != project.WorkspaceID {
			writeErr(w, 400, "project is in another workspace")
			return
		}
		workspaceID = project.WorkspaceID
		if workspaceRole == nil {
			role := store.RoleViewer
			workspaceRole = &role
		}
		lv, err := s.userProjectLevel(ctx, actor.User, *body.ProjectID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if lv == lvNone {
			writeErr(w, 404, "not found")
			return
		}
		if lv < lvAdmin {
			writeErr(w, 403, "프로젝트 admin만 초대할 수 있습니다")
			return
		}
		if body.ProjectRole == nil {
			m := store.RoleMember
			body.ProjectRole = &m
		}
		if !store.ValidProjectRole(*body.ProjectRole) {
			writeErr(w, 400, "project_role must be admin, member or viewer")
			return
		}
	} else {
		body.ProjectRole = nil
	}
	if _, err := s.Store.GetUserByEmail(ctx, email); err == nil {
		writeErr(w, 409, "이미 있는 사용자입니다")
		return
	} else if !errors.Is(err, store.ErrNotFound) {
		s.storeErr(w, err)
		return
	}
	tok := randomToken(32)
	in := &store.Invite{Email: email, Role: body.Role, ProjectID: body.ProjectID, ProjectRole: body.ProjectRole, CreatedBy: actor.User.ID, ExpiresAt: time.Now().Add(inviteTTL).UTC()}
	if workspaceID != "" {
		in.WorkspaceID, in.WorkspaceRole = &workspaceID, workspaceRole
	}
	if err := s.Store.CreateInvite(ctx, in, hashSecret(tok)); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "invite.created", in.ID, fmt.Sprintf("%s를 %s(으)로 초대", email, body.Role))
	// 토큰은 URL fragment(#)에 넣어 서버 로그나 Referer에 남지 않게 한다. 한 번만 보여 준다.
	link := s.publicBase(r) + "/invite/#token=" + tok
	out := map[string]any{"invite": in, "token": tok, "url": link}
	if body.SendEmail {
		// 메일 링크는 요청 Host가 아니라 KEEL_PUBLIC_URL이 있으면 그것을 쓴다(publicBase).
		out["email_sent"] = false
		if m := s.mailer(); m == nil {
			out["email_error"] = "SMTP가 설정되지 않았습니다"
		} else {
			mctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 60*time.Second)
			subject, text := notify.InviteEmail(actor.User.Display(), link, in.ExpiresAt)
			err := m.Send(mctx, []string{email}, subject, text)
			cancel()
			if err != nil {
				s.Log.Warn("invite email", "err", err)
				out["email_error"] = "메일을 보내지 못했습니다"
			} else {
				out["email_sent"] = true
				s.audit(r, "invite.emailed", in.ID, email+"에게 초대 메일 전송")
			}
		}
	}
	writeJSON(w, 201, out)
}

func (s *Server) listInvites(w http.ResponseWriter, r *http.Request) {
	ins, err := s.Store.ListInvites(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ins)
}

func (s *Server) deleteInvite(w http.ResponseWriter, r *http.Request) {
	in, err := s.Store.GetInvite(r.Context(), r.PathValue("iid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.DeleteInvite(r.Context(), in.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "invite.revoked", in.ID, in.Email+" 초대 취소")
	w.WriteHeader(204)
}

// acceptInvite는 인증 없이 부르는 라우트다: 초대 토큰이 자격이다. 사용자를 만들고 바로 로그인시킨다.
func (s *Server) acceptInvite(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	if s.Store == nil {
		writeErr(w, 503, "store unavailable")
		return
	}
	ctx := r.Context()
	if m := s.maintenance(ctx); m.Enabled {
		// 점검 중에는 새 계정을 만들지 않는다(초대 링크는 점검이 끝난 뒤에도 유효하다).
		writeJSON(w, 503, map[string]any{"error": m.Message, "maintenance": true})
		return
	}
	ip := limitKey(s.clientIP(r))
	l := s.lim()
	if wait := l.loginIP.wait(ip); wait > 0 {
		tooMany(w, wait, "시도가 너무 많습니다")
		return
	}
	var body struct {
		Token    string `json:"token"`
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	name := strings.TrimSpace(body.Name)
	if !validName(name) {
		writeErr(w, 400, "name은 1..100자여야 합니다")
		return
	}
	if err := checkPasswordPolicy(body.Password); err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	if body.Token == "" || len(body.Token) > 200 {
		l.loginIP.fail(ip)
		writeErr(w, 400, "초대 링크가 올바르지 않거나 만료되었습니다")
		return
	}
	hash, err := HashPassword(body.Password)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	u := &store.User{Name: name, PasswordHash: hash}
	in, err := s.Store.AcceptInvite(ctx, hashSecret(body.Token), u)
	switch {
	case errors.Is(err, store.ErrNotFound):
		l.loginIP.fail(ip)
		time.Sleep(loginDelay)
		writeErr(w, 400, "초대 링크가 올바르지 않거나 만료되었습니다")
		return
	case errors.Is(err, store.ErrConflict):
		writeErr(w, 409, "이미 있는 사용자입니다")
		return
	case err != nil:
		s.storeErr(w, err)
		return
	}
	r = withPrincipal(r, &principal{User: u, Via: viaSession})
	s.audit(r, "user.created", u.ID, fmt.Sprintf("%s 가입(초대 %s, 역할 %s)", u.Email, in.ID, u.Role))
	if in.ProjectID != nil && in.ProjectRole != nil && s.Events != nil {
		s.Events.Emit(r.Context(), *in.ProjectID, nil, nil, "member.added", fmt.Sprintf("%s님이 %s(으)로 참여했습니다", u.Display(), *in.ProjectRole))
	}
	if err := s.startSession(ctx, w, r, u); err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 201, u)
}

// ---- API tokens ----

func (s *Server) listTokens(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	uid := p.User.ID
	if r.URL.Query().Get("all") == "1" {
		if s.instanceLevel(p) < lvAdmin {
			writeErr(w, 403, "권한이 없습니다")
			return
		}
		uid = ""
	}
	ts, err := s.Store.ListAPITokens(r.Context(), uid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ts)
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p := principalFrom(r)
	var body struct {
		Name          string  `json:"name"`
		Scope         string  `json:"scope"`
		WorkspaceID   *string `json:"workspace_id"`
		ProjectID     *string `json:"project_id"`
		EnvironmentID *string `json:"environment_id"`
		Role          string  `json:"role"`
		ExpiresInDays *int    `json:"expires_in_days"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if !validName(name) {
		writeErr(w, 400, "name은 1..100자여야 합니다")
		return
	}
	if body.Role == "" {
		body.Role = store.RoleMember
	}
	if !store.ValidProjectRole(body.Role) {
		writeErr(w, 400, "role must be admin, member or viewer")
		return
	}
	t := &store.APIToken{UserID: p.User.ID, Name: name, Role: body.Role, Scope: store.ScopeWorkspace}
	var limit int
	switch {
	case body.Scope == store.ScopeInstance:
		if body.WorkspaceID != nil || body.ProjectID != nil || body.EnvironmentID != nil {
			writeErr(w, 400, "instance tokens cannot select a workspace, project or environment")
			return
		}
		t.Scope = store.ScopeInstance
		if s.instanceLevel(p) < lvAdmin {
			writeErr(w, 403, "인스턴스 관리자만 운영 토큰을 만들 수 있습니다")
			return
		}
		limit = s.instanceLevel(p)
	case body.EnvironmentID != nil && *body.EnvironmentID != "":
		env, err := s.Store.GetEnvironment(ctx, *body.EnvironmentID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if body.ProjectID != nil && *body.ProjectID != "" && *body.ProjectID != env.ProjectID {
			writeErr(w, 400, "environment_id가 project_id의 환경이 아닙니다")
			return
		}
		t.Scope, t.ProjectID, t.EnvironmentID = store.ScopeEnvironment, &env.ProjectID, &env.ID
		lv, err := s.userProjectLevel(ctx, p.User, env.ProjectID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if lv > lvNone && t.Role == store.RoleAdmin {
			writeErr(w, 400, "environment 토큰의 역할은 member 또는 viewer입니다")
			return
		}
		limit = lv
	case body.ProjectID != nil && *body.ProjectID != "":
		pr, err := s.Store.GetProject(ctx, *body.ProjectID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		t.Scope, t.ProjectID = store.ScopeProject, &pr.ID
		lv, err := s.userProjectLevel(ctx, p.User, pr.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		limit = lv
	default:
		wsID := store.DefaultWorkspaceID
		if body.WorkspaceID != nil {
			wsID = *body.WorkspaceID
		}
		if wsID == "" {
			writeErr(w, 400, "workspace_id is required")
			return
		}
		t.WorkspaceID = &wsID
		lv, err := s.workspaceAccess(ctx, p, wsID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		limit = lv
	}
	if body.Scope != "" && body.Scope != t.Scope {
		writeErr(w, 400, "scope does not match the selected resource")
		return
	}
	if t.ProjectID != nil {
		project, err := s.Store.GetProject(ctx, *t.ProjectID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if body.WorkspaceID != nil && *body.WorkspaceID != project.WorkspaceID {
			writeErr(w, 400, "workspace_id does not match project")
			return
		}
		t.WorkspaceID = &project.WorkspaceID
	}
	if limit == lvNone {
		writeErr(w, 404, "not found")
		return
	}
	if roleLevel(t.Role) > limit {
		writeErr(w, 403, "자기 권한보다 높은 역할의 토큰은 만들 수 없습니다")
		return
	}
	if body.ExpiresInDays != nil {
		d := *body.ExpiresInDays
		if d < 1 || d > 3650 {
			writeErr(w, 400, "expires_in_days must be 1..3650 or null")
			return
		}
		exp := time.Now().Add(time.Duration(d) * 24 * time.Hour).UTC()
		t.ExpiresAt = &exp
	}
	if mine, err := s.Store.ListAPITokens(ctx, p.User.ID); err == nil && len(mine) >= maxTokens {
		writeErr(w, 409, fmt.Sprintf("토큰은 사용자당 %d개까지입니다", maxTokens))
		return
	}
	secret := apiTokenPrefix + strings.ToLower(b32.EncodeToString(randomRaw(25)))
	t.Prefix = secret[:len(apiTokenPrefix)+8]
	if err := s.Store.CreateAPIToken(ctx, t, hashSecret(secret)); err != nil {
		s.storeErr(w, err)
		return
	}
	scope := t.Scope
	if t.ProjectID != nil {
		scope += " " + *t.ProjectID
	}
	if t.EnvironmentID != nil {
		scope += "/" + *t.EnvironmentID
	}
	s.audit(r, "token.created", t.ID, fmt.Sprintf("토큰 %s 생성 (%s, %s)", t.Name, scope, t.Role))
	// token 값은 이 응답에서 한 번만 보여 준다(DB에는 SHA-256만 남는다).
	writeJSON(w, 201, struct {
		*store.APIToken
		Token string `json:"token"`
	}{t, secret})
}

func (s *Server) deleteToken(w http.ResponseWriter, r *http.Request) {
	p := principalFrom(r)
	t, err := s.Store.GetAPIToken(r.Context(), r.PathValue("tid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if t.UserID != p.User.ID && s.instanceLevel(p) < lvAdmin {
		writeErr(w, 404, "not found")
		return
	}
	if err := s.Store.DeleteAPIToken(r.Context(), t.ID); err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "token.revoked", t.ID, "토큰 "+t.Name+" 폐기")
	w.WriteHeader(204)
}

// ---- project members ----

func (s *Server) listMembers(w http.ResponseWriter, r *http.Request) {
	ms, err := s.Store.ListMembers(r.Context(), r.PathValue("pid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, ms)
}

func (s *Server) putMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid := r.PathValue("pid")
	var body struct {
		Role string `json:"role"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !store.ValidProjectRole(body.Role) {
		writeErr(w, 400, "role must be admin, member or viewer")
		return
	}
	u, err := s.Store.GetUser(ctx, r.PathValue("uid"))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	prj, err := s.Store.GetProject(ctx, pid)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if role, err := s.Store.WorkspaceRole(ctx, prj.WorkspaceID, u); err != nil || role == "" {
		if err != nil {
			s.storeErr(w, err)
		} else {
			writeErr(w, 400, store.ErrWorkspaceMembership.Error())
		}
		return
	}
	if err := s.Store.SetMember(ctx, pid, u.ID, body.Role); err != nil {
		s.storeErr(w, err)
		return
	}
	msg := fmt.Sprintf("%s님을 %s(으)로 지정했습니다", u.Display(), body.Role)
	s.Events.Emit(ctx, pid, nil, nil, "member.added", msg)
	s.audit(r, "member.added", pid+":"+u.ID, msg)
	ms, _ := s.Store.ListMembers(ctx, pid)
	for _, m := range ms {
		if m.UserID == u.ID {
			writeJSON(w, 200, m)
			return
		}
	}
	w.WriteHeader(204)
}

func (s *Server) deleteMember(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pid, uid := r.PathValue("pid"), r.PathValue("uid")
	if err := s.Store.RemoveMember(ctx, pid, uid); err != nil {
		s.storeErr(w, err)
		return
	}
	who := uid
	if u, err := s.Store.GetUser(ctx, uid); err == nil {
		who = u.Display()
	}
	msg := who + "님을 프로젝트에서 내보냈습니다"
	s.Events.Emit(ctx, pid, nil, nil, "member.removed", msg)
	s.audit(r, "member.removed", pid+":"+uid, msg)
	w.WriteHeader(204)
}

// ---- audit ----

func (s *Server) listAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	out, err := s.Store.ListAudit(r.Context(), limit)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, out)
}
