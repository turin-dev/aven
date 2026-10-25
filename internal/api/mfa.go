package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/turin-dev/aven/internal/store"
)

// mfaUserView는 사용자 응답에 등록된 passkey 수를 더한다.
type mfaUserView struct {
	*store.User
	PasskeyCount int `json:"passkey_count"`
}

// hasMFAFactor는 TOTP나 패스키가 하나라도 있는지다.
func (s *Server) hasMFAFactor(ctx context.Context, u *store.User) (bool, error) {
	if u.TOTPEnabled {
		return true, nil
	}
	if s.Store == nil {
		return false, nil
	}
	n, err := s.Store.CountPasskeys(ctx, u.ID)
	return n > 0, err
}

// mfaSetupRequired는 (작업 공간 강제 여부, 이 사용자가 아직 설정해야 하는지)다.
func (s *Server) mfaSetupRequired(ctx context.Context, u *store.User) (bool, bool, error) {
	if s.Store == nil {
		return false, false, nil
	}
	enforced, err := s.Store.Workspace2FAEnforced(ctx)
	if err != nil || !enforced {
		return enforced, false, err
	}
	has, err := s.hasMFAFactor(ctx, u)
	return enforced, !has, err
}

// requireMFA는 작업 공간 2FA 강제를 적용한다: 두 번째 수단이 없는 브라우저 세션(대시보드, CLI 로그인)은 설정용 엔드포인트만 쓸 수 있다.

func (s *Server) requireMFA(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := principalFrom(r)
		if p == nil || p.Via != viaSession || p.ViewAsBy != nil || s.Store == nil {
			next.ServeHTTP(w, r)
			return
		}
		enforced, err := s.Store.Workspace2FAEnforced(r.Context())
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if !enforced {
			next.ServeHTTP(w, r)
			return
		}
		hasFactor, err := s.hasMFAFactor(r.Context(), p.User)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if hasFactor || mfaSetupEndpoint(r.Method, r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		writeErr(w, http.StatusForbidden, "mfa_setup_required")
	})
}

// mfaSetupEndpoint는 2단계 인증이 강제됐지만 아직 설정하지 않은 세션이 쓸 수 있는 엔드포인트다(설정 화면에 필요한 것만).
func mfaSetupEndpoint(method, path string) bool {
	switch method + " " + path {
	case "GET /api/auth/me", "GET /api/auth/passkeys",
		"POST /api/auth/2fa/setup", "POST /api/auth/2fa/enable",
		"POST /api/auth/passkeys/register/begin":
		return true
	}
	if method == http.MethodPost {
		if rest, ok := strings.CutPrefix(path, "/api/auth/passkeys/register/"); ok {
			id, ok := strings.CutSuffix(rest, "/finish")
			return ok && id != "" && !strings.Contains(id, "/")
		}
	}
	return false
}

// secondFactorMethods는 로그인 두 번째 단계에서 쓸 수 있는 수단이다.
func secondFactorMethods(totp, passkey bool) []string {
	out := []string{}
	if totp {
		out = append(out, "totp")
	}
	if passkey {
		out = append(out, "passkey")
	}
	return append(out, "recovery_code")
}

func (s *Server) getWorkspaceSecurity(w http.ResponseWriter, r *http.Request) {
	required, err := s.Store.Workspace2FAEnforced(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]bool{"require_2fa": required})
}

func (s *Server) patchWorkspaceSecurity(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Require2FA bool `json:"require_2fa"`
	}
	if !decode(w, r, &body) {
		return
	}
	p := principalFrom(r)
	if body.Require2FA {
		has, err := s.hasMFAFactor(r.Context(), p.User)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if !has {
			writeErr(w, 400, "2단계 인증을 켠 뒤 작업 공간 요구 사항을 활성화하세요")
			return
		}
	}
	if err := s.Store.SetWorkspace2FAEnforced(r.Context(), body.Require2FA); err != nil {
		s.storeErr(w, err)
		return
	}
	action := "끔"
	if body.Require2FA {
		action = "켬"
	}
	s.audit(r, "workspace.security.updated", "workspace", "작업 공간 2단계 인증 요구 "+action)
	writeJSON(w, 200, map[string]bool{"require_2fa": body.Require2FA})
}
