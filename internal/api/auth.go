package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

// ---- auth: 서버 측 세션(쿠키에는 무작위 토큰, DB에는 SHA-256만) ----

const cookieName = "keel_session"

// sessionTTL은 마지막 사용으로부터의 세션 수명이다(sliding). sessionTouch마다 한 번 만료를 늦춘다.
const (
	sessionTTL   = 30 * 24 * time.Hour
	sessionTouch = time.Minute
	tokenTouch   = time.Minute
)

// ctEqual은 길이가 달라도 시간이 새지 않게 SHA-256 다이제스트끼리 상수 시간으로 비교한다.
func ctEqual(a, b string) bool {
	x, y := sha256.Sum256([]byte(a)), sha256.Sum256([]byte(b))
	return subtle.ConstantTimeCompare(x[:], y[:]) == 1
}

// 인증 방식
const (
	viaSession  = "session"
	viaToken    = "token"     // keel_ API 토큰
	viaEnvToken = "env_token" // KEEL_API_TOKEN(옛 방식, owner 권한)
	viaSAML     = "saml"
	viaOAuth    = "oauth" // OAuth access token
)

// principal은 인증된 요청의 주체다. Token이 있으면 그 범위와 역할로 권한이 더 줄어든다.
type principal struct {
	User      *store.User
	Via       string
	SessionID string
	Token     *store.APIToken
	SAML      bool
	OAuth     *store.OAuthGrant
	// ViewAsBy가 있으면 서버 owner(ViewAsBy)가 "사용자로 보기"로 User의 시점을 읽기 전용으로 보고 있다(ViewAsUntil까지).
	ViewAsBy    *store.User
	ViewAsUntil time.Time
}

func (p *principal) display() string {
	if p.ViewAsBy != nil {
		return p.ViewAsBy.Display() + " (" + p.User.Display() + "로 보기)"
	}
	switch p.Via {
	case viaToken:
		return p.User.Display() + " (토큰 " + p.Token.Name + ")"
	case viaEnvToken:
		return p.User.Display() + " (API token)"
	case viaOAuth:
		return p.User.Display() + " (OAuth)"
	}
	return p.User.Display()
}

type principalKey struct{}

// principalFrom은 requireAuth가 붙인 주체다. 핸들러를 직접 부르는 테스트에서는 nil이다.
func principalFrom(r *http.Request) *principal {
	p, _ := r.Context().Value(principalKey{}).(*principal)
	return p
}

func withPrincipal(r *http.Request, p *principal) *http.Request {
	ctx := context.WithValue(r.Context(), principalKey{}, p)
	ctx = events.WithActor(ctx, p.display())
	return r.WithContext(ctx)
}

// builtinUser는 KEEL_ADMIN_PASSWORD / KEEL_API_TOKEN이 가리키는 기본 owner 계정이다.
func (s *Server) builtinUser(ctx context.Context) (*store.User, error) {
	if s.Store == nil {
		return &store.User{ID: store.BuiltinUserID, Email: store.BuiltinUserEmail, Name: "admin", Role: store.RoleOwner, Builtin: true}, nil
	}
	u, err := s.Store.GetUser(ctx, store.BuiltinUserID)
	if err != nil {
		return nil, err
	}
	u.Role = store.RoleOwner // 기본 계정은 항상 owner다
	return u, nil
}

// adminTag는 KEEL_ADMIN_PASSWORD의 지문이다. 기본 계정 세션에 저장해 두고, 환경변수 비밀번호가 바뀌면 그 세션을 무효로 한다.
func (s *Server) adminTag() string {
	mac := hmac.New(sha256.New, s.SessionKey)
	mac.Write([]byte("keel-admin-password\x00" + s.AdminPassword))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func randomRaw(n int) []byte {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func randomToken(n int) string { return base64.RawURLEncoding.EncodeToString(randomRaw(n)) }

// bearerPrincipal은 Authorization: Bearer를 검사한다. KEEL_API_TOKEN(owner)을 먼저 보고, 그다음 keel_ API 토큰을 본다.
func (s *Server) bearerPrincipal(ctx context.Context, tok string) *principal {
	if s.APIToken != "" && ctEqual(tok, s.APIToken) {
		u, err := s.builtinUser(ctx)
		if err != nil {
			return nil
		}
		// KEEL_API_TOKEN은 운영자가 환경변수로 관리하는 owner 자격이라 기본 계정의 비활성화와 무관하게 동작한다.
		u.Disabled = false
		return &principal{User: u, Via: viaEnvToken}
	}
	if strings.HasPrefix(tok, oauthAccessTokenPrefix) {
		return s.oauthPrincipal(ctx, tok)
	}
	if !strings.HasPrefix(tok, apiTokenPrefix) || s.Store == nil || len(tok) > 200 {
		return nil
	}
	t, err := s.Store.GetAPITokenByHash(ctx, hashSecret(tok))
	if err != nil {
		return nil
	}
	u, err := s.Store.GetUser(ctx, t.UserID)
	if err != nil || u.Disabled {
		return nil
	}
	if u.Builtin {
		u.Role = store.RoleOwner
	}
	if t.LastUsedAt == nil || time.Since(*t.LastUsedAt) > tokenTouch {
		s.Store.TouchAPIToken(context.WithoutCancel(ctx), t.ID)
	}
	return &principal{User: u, Via: viaToken, Token: t}
}

// sessionPrincipal은 세션 쿠키를 검사한다. refresh가 true면 쿠키 만료도 늦춰서 다시 보내야 한다.
func (s *Server) sessionPrincipal(ctx context.Context, value string) (p *principal, refresh bool) {
	if s.Store == nil || value == "" || len(value) > 200 {
		return nil, false
	}
	se, err := s.Store.GetSessionByHash(ctx, hashSecret(value))
	if err != nil {
		return nil, false
	}
	u, err := s.Store.GetUser(ctx, se.UserID)
	if err != nil || u.Disabled {
		return nil, false
	}
	if u.Builtin {
		// 기본 계정 세션은 만들 때의 KEEL_ADMIN_PASSWORD로만 유효하다(환경변수로 비밀번호를 바꾸면 모두 끊긴다).
		if s.AdminPassword == "" || !hmac.Equal([]byte(se.AuthTag), []byte(s.adminTag())) {
			return nil, false
		}
		u.Role = store.RoleOwner
	}
	if now := time.Now(); now.Sub(se.LastSeen) > sessionTouch {
		s.Store.TouchSession(context.WithoutCancel(ctx), se.ID, now, now.Add(sessionTTL))
		refresh = true
	}
	p = &principal{User: u, Via: viaSession, SessionID: se.ID, SAML: se.SAMLAuthenticated}
	if vp := s.viewAsPrincipal(ctx, p); vp != nil {
		return vp, refresh
	}
	return p, refresh
}

func (s *Server) requireAuth(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := limitKey(s.clientIP(r))
		if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
			// 잘못된 API token을 반복해서 시도하는 IP는 잠근다(올바른 token은 잠금 상태에서도 통과하지 못하게 먼저 검사).
			if wait := s.lim().agentIP.wait(key); wait > 0 {
				tooMany(w, wait, "인증 시도가 너무 많습니다")
				return
			}
			if p := s.bearerPrincipal(r.Context(), tok); p != nil {
				h.ServeHTTP(w, withPrincipal(r, p))
				return
			}
			s.lim().agentIP.fail(key)
		}
		if c, err := r.Cookie(cookieName); err == nil {
			if p, refresh := s.sessionPrincipal(r.Context(), c.Value); p != nil {
				if p.ViewAsBy != nil {
					if !viewAsAllowed(r) {
						writeJSON(w, 403, map[string]any{"error": "사용자로 보기 중에는 읽기만 할 수 있습니다. 보기를 끝낸 뒤 다시 시도하세요", "view_as": true})
						return
					}
				} else if !s.samlAuthAllowed(r.Context(), w, p) {
					return
				}
				// 쿠키 인증의 상태 변경 요청은 CSRF 방지를 위해 같은 origin에서 온 것만 허용한다.
				if !safeMethod(r.Method) && !sameOrigin(r) {
					writeErr(w, 403, "cross-origin request rejected")
					return
				}
				if refresh {
					exp := time.Now().Add(sessionTTL)
					http.SetCookie(w, s.sessionCookie(r, c.Value, exp, 0))
				}
				h.ServeHTTP(w, withPrincipal(r, p))
				return
			}
		}
		writeErr(w, 401, "unauthorized")
	})
}

// startSession은 새 세션을 만들고 쿠키를 붙인다.
func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *store.User) error {
	return s.startSessionWithMethod(ctx, w, r, u, false)
}

func (s *Server) startSAMLSession(ctx context.Context, w http.ResponseWriter, r *http.Request, u *store.User) error {
	return s.startSessionWithMethod(ctx, w, r, u, true)
}

func (s *Server) startSessionWithMethod(ctx context.Context, w http.ResponseWriter, r *http.Request, u *store.User, samlAuthenticated bool) error {
	if s.loginBlocked(ctx, u) {
		return errMaintenanceLogin
	}
	tok := randomToken(32)
	exp := time.Now().Add(sessionTTL)
	se := &store.Session{UserID: u.ID, ExpiresAt: exp, IP: limitKey(s.clientIP(r)), UserAgent: truncate(r.UserAgent(), 200), SAMLAuthenticated: samlAuthenticated}
	if u.Builtin {
		se.AuthTag = s.adminTag()
	}
	s.Store.PruneSessions(ctx)
	if err := s.Store.CreateSession(ctx, se, hashSecret(tok)); err != nil {
		return err
	}
	http.SetCookie(w, s.sessionCookie(r, tok, exp, 0))
	return nil
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// sameOrigin은 브라우저가 보낸 상태 변경 요청이 같은 origin에서 왔는지 본다.
// 브라우저는 Sec-Fetch-Site/Origin을 항상 붙이므로 cross-site 요청은 여기서 걸린다.
// 둘 다 없는 요청(curl, CLI)은 브라우저가 아니므로 CSRF 대상이 아니다.
func sameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin":
		return true
	case "", "none":
	default: // same-site(다른 subdomain), cross-site
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return false // "null" 포함
	}
	return strings.EqualFold(u.Host, r.Host)
}

// secureHeaders는 모든 응답(UI, API)에 보안 헤더를 붙인다. HSTS는 HTTPS로 들어온 요청에만 붙인다.
// CSP는 clickjacking/base 태그/form 이동만 막는 최소 정책이다(Next static export의 inline script를 깨지 않기 위해 script-src는 두지 않는다).
func (s *Server) secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Content-Security-Policy", "frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
		hd.Set("Referrer-Policy", "same-origin")
		hd.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=()")
		if s.secureTransport(r) {
			hd.Set("Strict-Transport-Security", "max-age=31536000")
			hd.Set("Cross-Origin-Opener-Policy", "same-origin") // 신뢰할 수 없는 origin(plain http)에서는 브라우저가 무시하며 경고만 낸다
		}
		h.ServeHTTP(w, r)
	})
}

func (s *Server) sessionCookie(r *http.Request, value string, exp time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: cookieName, Value: value, Path: "/", Expires: exp, MaxAge: maxAge,
		HttpOnly: true, Secure: s.SecureCookie || s.secureTransport(r), SameSite: http.SameSiteStrictMode,
	}
}

const loginDelay = 300 * time.Millisecond

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) { // login CSRF 방지
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		TOTPCode string `json:"totp_code"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	ctx := r.Context()
	email := store.NormalizeEmail(body.Email)
	if email != "" && email != store.BuiltinUserEmail && s.Store != nil {
		cfg, err := s.Store.GetSAMLConfig(ctx)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if cfg.EnforceSSO && verifiedSAMLDomain(cfg, emailDomain(email)) {
			writeErr(w, 403, "sso_required")
			return
		}
	}
	acct := email
	if email == "" || email == store.BuiltinUserEmail {
		acct = "admin"
	}
	// 잠겨 있으면 비밀번호가 맞아도 검사하지 않는다.
	if s.lockedOut(w, r, acct, "로그인 시도가 너무 많습니다") {
		return
	}
	badCreds := "이메일 또는 비밀번호가 올바르지 않습니다"
	if email == "" {
		badCreds = "비밀번호가 올바르지 않습니다"
	}
	u := s.checkCredentials(ctx, email, body.Password)
	if u == nil {
		s.Log.Warn("login failed", "ip", limitKey(s.clientIP(r)))
		s.authFailed(w, r, acct, "로그인 시도가 너무 많습니다", badCreds)
		return
	}
	if u.Disabled {
		writeErr(w, 403, "비활성화된 계정입니다")
		return
	}
	passkeyCount := 0
	if s.Store != nil {
		n, err := s.Store.CountPasskeys(ctx, u.ID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		passkeyCount = n
	}
	if u.TOTPEnabled || passkeyCount > 0 {
		if strings.TrimSpace(body.TOTPCode) == "" {
			// UI가 두 번째 인증을 물어볼 수 있게 고정된 오류 문자열을 쓴다(실패로 세지 않는다: 비밀번호는 맞았다).
			// methods는 쓸 수 있는 수단이다(passkey만 있는 계정은 passkey 또는 복구 코드).
			writeJSON(w, 401, map[string]any{"error": "totp_required", "methods": secondFactorMethods(u.TOTPEnabled, passkeyCount > 0)})
			return
		}
		ok, err := s.checkSecondFactor(ctx, u, body.TOTPCode)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if !ok {
			s.Log.Warn("login 2fa failed", "ip", limitKey(s.clientIP(r)))
			s.authFailed(w, r, acct, "로그인 시도가 너무 많습니다", "인증 코드가 올바르지 않습니다")
			return
		}
	}
	s.authPassed(r, acct)
	if s.Store == nil {
		writeErr(w, 503, "store unavailable")
		return
	}
	if err := s.startSession(ctx, w, r, u); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Store.TouchUserLogin(ctx, u.ID)
	s.audit(withPrincipal(r, &principal{User: u, Via: viaSession}), "auth.login", u.ID, u.Display()+" 로그인")
	w.WriteHeader(204)
}

// checkCredentials는 이메일/비밀번호를 검사해 사용자를 돌려준다(틀리면 nil).
// 이메일이 없거나 admin@local이면 기본 owner 계정이고 KEEL_ADMIN_PASSWORD와 상수 시간으로 비교한다.
func (s *Server) checkCredentials(ctx context.Context, email, password string) *store.User {
	if email == "" || email == store.BuiltinUserEmail {
		if s.AdminPassword == "" || !ctEqual(password, s.AdminPassword) {
			return nil
		}
		u, err := s.builtinUser(ctx)
		if err != nil {
			return nil
		}
		return u
	}
	if s.Store == nil || len(password) > maxPasswordLen {
		return nil
	}
	u, err := s.Store.GetUserByEmail(ctx, email)
	if err != nil || u.Builtin || u.PasswordHash == "" {
		VerifyPassword(dummyHash, password) // 없는 계정도 같은 시간이 걸리게
		return nil
	}
	if !VerifyPassword(u.PasswordHash, password) {
		return nil
	}
	return u
}

// checkUserPassword는 이미 아는 사용자의 비밀번호를 확인한다(기본 계정은 KEEL_ADMIN_PASSWORD).
func (s *Server) checkUserPassword(u *store.User, password string) bool {
	if u.Builtin {
		return s.AdminPassword != "" && ctEqual(password, s.AdminPassword)
	}
	if u.PasswordHash == "" || len(password) > maxPasswordLen {
		VerifyPassword(dummyHash, password)
		return false
	}
	return VerifyPassword(u.PasswordHash, password)
}

// checkSecondFactor는 TOTP 코드(6자리) 또는 일회용 복구 코드를 확인한다. TOTP는 같은 step을 두 번 쓸 수 없다.
func (s *Server) checkSecondFactor(ctx context.Context, u *store.User, code string) (bool, error) {
	code = strings.TrimSpace(code)
	if len(code) == totpDigits {
		if !u.TOTPEnabled { // 설정만 시작하고 확인하지 않은 비밀은 쓰지 않는다
			return false, nil
		}
		step, ok := MatchTOTP(u.TOTPSecret, code, time.Now())
		if !ok {
			return false, nil
		}
		return s.Store.UseTOTPStep(ctx, u.ID, step)
	}
	rc := normalizeRecovery(code)
	if rc == "" {
		return false, nil
	}
	return s.Store.UseRecoveryCode(ctx, u.ID, hashSecret(rc))
}

// lockedOut은 IP나 계정이 잠겨 있으면 429를 쓰고 true를 돌려준다.
func (s *Server) lockedOut(w http.ResponseWriter, r *http.Request, acct, msg string) bool {
	l := s.lim()
	if wait := max(l.loginIP.wait(limitKey(s.clientIP(r))), l.loginAcct.wait(acct)); wait > 0 {
		tooMany(w, wait, msg)
		return true
	}
	return false
}

// authFailed는 실패를 IP와 계정 양쪽에 세고, 잠시 기다린 뒤 401(또는 잠겼으면 429)을 쓴다.
func (s *Server) authFailed(w http.ResponseWriter, r *http.Request, acct, lockMsg, msg string) {
	l := s.lim()
	ip := limitKey(s.clientIP(r))
	l.loginIP.fail(ip)
	l.loginAcct.fail(acct)
	time.Sleep(loginDelay)
	if wait := max(l.loginIP.wait(ip), l.loginAcct.wait(acct)); wait > 0 {
		tooMany(w, wait, lockMsg)
		return
	}
	writeErr(w, 401, msg)
}

func (s *Server) authPassed(r *http.Request, acct string) {
	l := s.lim()
	l.loginIP.reset(limitKey(s.clientIP(r)))
	l.loginAcct.reset(acct)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	if c, err := r.Cookie(cookieName); err == nil && c.Value != "" && s.Store != nil && len(c.Value) <= 200 {
		if err := s.Store.DeleteSessionByHash(r.Context(), hashSecret(c.Value)); err != nil && !errors.Is(err, store.ErrNotFound) {
			s.Log.Warn("logout", "err", err)
		}
		s.kickInboxStreams()
	}
	http.SetCookie(w, s.sessionCookie(r, "", time.Time{}, -1))
	w.WriteHeader(204)
}

// ---- SSE ----

type sse struct {
	w http.ResponseWriter
	f http.Flusher
}

func newSSE(w http.ResponseWriter) (*sse, bool) {
	f, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	f.Flush()
	return &sse{w, f}, true
}

func (e *sse) send(event, data string) {
	for _, line := range strings.Split(data, "\n") {
		fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, strings.TrimRight(line, "\r"))
	}
}

// ---- SPA ----

// spaHandler는 Next.js static export 결과를 서빙한다.
// trailingSlash export라서 /apps/detail/ → apps/detail/index.html 형태다. 없는 경로는 index.html로 보낸다.
func spaHandler(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p == "" {
			p = "."
		}
		p = strings.TrimSuffix(p, "/")
		if p == "" {
			p = "."
		}
		if st, err := fs.Stat(web, p); err == nil {
			if st.IsDir() {
				if _, err := fs.Stat(web, p+"/index.html"); err != nil && p != "." {
					serveIndex(w, r, web)
					return
				}
			}
			if strings.HasPrefix(r.URL.Path, "/_next/static/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			}
			files.ServeHTTP(w, r)
			return
		}
		if _, err := fs.Stat(web, p+".html"); err == nil {
			http.ServeFileFS(w, r, web, p+".html")
			return
		}
		serveIndex(w, r, web)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, web fs.FS) {
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFileFS(w, r, web, "index.html")
}

func (e *sse) sendJSON(event string, v any) {
	b, _ := json.Marshal(v)
	fmt.Fprintf(e.w, "event: %s\ndata: %s\n\n", event, b)
}
