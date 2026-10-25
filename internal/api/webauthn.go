package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/turin-dev/aven/internal/store"
)

const (
	webAuthnChallengeTTL = 5 * time.Minute
	maxPasskeys          = 20
)

// webAuthnUser는 aven 사용자를 WebAuthn 라이브러리의 User로 감싼다. WebAuthn user handle은 사용자 ID의 해시다(이메일 등을 인증기에 남기지 않는다).
type webAuthnUser struct {
	user        *store.User
	credentials []webauthn.Credential
}

func (u *webAuthnUser) WebAuthnID() []byte {
	h := sha256.Sum256([]byte("keel-webauthn-user\x00" + u.user.ID))
	return h[:]
}

func (u *webAuthnUser) WebAuthnName() string { return u.user.Email }

func (u *webAuthnUser) WebAuthnDisplayName() string {
	if u.user.Name != "" {
		return u.user.Name
	}
	return u.user.Email
}

func (u *webAuthnUser) WebAuthnCredentials() []webauthn.Credential {
	return append([]webauthn.Credential(nil), u.credentials...)
}

// webAuthnOrigin은 요청의 origin과 RP ID(호스트 이름)를 정한다. 같은 origin 요청만 받고, IP 주소는 WebAuthn이 허용하지 않아 거부한다.
// 패스키는 RP ID별로 저장되므로 다른 주소(호스트)로 접속하면 거기서 따로 등록해야 한다.
func webAuthnOrigin(s *Server, r *http.Request) (origin, rpID string, err error) {
	if !sameOrigin(r) || r.Host == "" {
		return "", "", errors.New("origin mismatch")
	}
	raw := r.Header.Get("Origin")
	if raw == "" {
		scheme := "http"
		if s.secureTransport(r) {
			scheme = "https"
		}
		raw = scheme + "://" + r.Host
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") || !strings.EqualFold(u.Host, r.Host) {
		return "", "", errors.New("invalid origin")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" || (net.ParseIP(host) != nil && host != "localhost") {
		return "", "", errors.New("WebAuthn needs a DNS hostname or localhost")
	}
	return u.Scheme + "://" + u.Host, host, nil
}

func newWebAuthn(origin, rpID string) (*webauthn.WebAuthn, error) {
	return webauthn.New(&webauthn.Config{
		RPDisplayName: "aven",
		RPID:          rpID,
		RPOrigins:     []string{origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		},
	})
}

func (s *Server) loadWebAuthnUser(ctx context.Context, u *store.User, rpID string) (*webAuthnUser, error) {
	records, err := s.Store.PasskeyRecords(ctx, u.ID, rpID)
	if err != nil {
		return nil, err
	}
	w := &webAuthnUser{user: u, credentials: make([]webauthn.Credential, 0, len(records))}
	for _, p := range records {
		var c webauthn.Credential
		if err := json.Unmarshal([]byte(p.CredentialJSON), &c); err != nil {
			return nil, fmt.Errorf("decode passkey credential: %w", err)
		}
		w.credentials = append(w.credentials, c)
	}
	return w, nil
}

func createWebAuthnChallenge(ctx context.Context, s *Server, c *store.WebAuthnChallenge) (string, error) {
	id := randomToken(32)
	c.ExpiresAt = time.Now().UTC().Add(webAuthnChallengeTTL)
	if err := s.Store.CreateWebAuthnChallenge(ctx, hashSecret(id), c); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) challengeErr(w http.ResponseWriter, err error) {
	if errors.Is(err, store.ErrTooManyChallenges) {
		tooMany(w, time.Minute, "패스키 요청이 너무 많습니다. 잠시 뒤 다시 시도하세요")
		return
	}
	s.storeErr(w, err)
}

func originForChallenge(s *Server, r *http.Request, c *store.WebAuthnChallenge) bool {
	origin, rpID, err := webAuthnOrigin(s, r)
	return err == nil && origin == c.Origin && rpID == c.RPID
}

func (s *Server) requireBrowserSession(w http.ResponseWriter, r *http.Request) (*principal, bool) {
	p := principalFrom(r)
	if p == nil || p.Via != viaSession || p.SessionID == "" {
		writeErr(w, 403, "패스키 관리에는 브라우저 세션이 필요합니다")
		return nil, false
	}
	return p, true
}

func (s *Server) listPasskeys(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	rows, err := s.Store.ListPasskeys(r.Context(), p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) beginPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "패스키"
	}
	if utf8.RuneCountInString(name) > 80 || strings.ContainsAny(name, "\r\n\t") {
		writeErr(w, 400, "패스키 이름은 1..80자여야 합니다")
		return
	}
	origin, rpID, err := webAuthnOrigin(s, r)
	if err != nil {
		writeErr(w, 400, "이 주소에서는 패스키를 등록할 수 없습니다")
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		writeErr(w, 400, "WebAuthn relying party 설정이 올바르지 않습니다")
		return
	}
	u, err := s.Store.GetUser(r.Context(), p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	// 훔친 세션 쿠키만으로 로그인 수단을 늘리지 못하게 현재 비밀번호를 다시 확인한다.
	if !s.confirmPassword(w, r, u, body.Password) {
		return
	}
	wu, err := s.loadWebAuthnUser(r.Context(), u, rpID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if n, err := s.Store.CountPasskeys(r.Context(), u.ID); err != nil {
		s.storeErr(w, err)
		return
	} else if n >= maxPasskeys {
		writeErr(w, 409, fmt.Sprintf("패스키는 계정당 %d개까지 등록할 수 있습니다", maxPasskeys))
		return
	}
	exclude := make([]protocol.CredentialDescriptor, 0, len(wu.credentials))
	for i := range wu.credentials {
		exclude = append(exclude, wu.credentials[i].Descriptor())
	}
	options, session, err := wa.BeginRegistration(wu,
		webauthn.WithExclusions(exclude),
		webauthn.WithAuthenticatorSelection(protocol.AuthenticatorSelection{
			ResidentKey:        protocol.ResidentKeyRequirementRequired,
			RequireResidentKey: protocol.ResidentKeyRequired(),
			UserVerification:   protocol.VerificationRequired,
		}),
	)
	if err != nil {
		writeErr(w, 400, "패스키 등록을 시작하지 못했습니다")
		return
	}
	state, err := json.Marshal(session)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	challengeID, err := createWebAuthnChallenge(r.Context(), s, &store.WebAuthnChallenge{
		UserID: u.ID, SessionID: p.SessionID, Purpose: "register", Name: name,
		Origin: origin, RPID: rpID, SessionData: string(state),
	})
	if err != nil {
		s.challengeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"challenge_id": challengeID, "options": options})
}

func (s *Server) finishPasskeyRegistration(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	c, err := s.Store.ConsumeWebAuthnChallenge(r.Context(), hashSecret(r.PathValue("id")), "register", p.SessionID)
	if err != nil {
		writeErr(w, 400, "패스키 등록 요청이 만료되었거나 올바르지 않습니다")
		return
	}
	if c.UserID != p.User.ID || !originForChallenge(s, r, c) {
		writeErr(w, 400, "패스키 등록 요청이 올바르지 않습니다")
		return
	}
	wa, err := newWebAuthn(c.Origin, c.RPID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	u, err := s.Store.GetUser(r.Context(), c.UserID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	wu, err := s.loadWebAuthnUser(r.Context(), u, c.RPID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(c.SessionData), &session); err != nil {
		s.storeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	credential, err := wa.FinishRegistration(wu, session, r)
	if err != nil {
		writeErr(w, 400, "패스키를 확인할 수 없습니다")
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.CreatePasskey(r.Context(), &store.Passkey{
		UserID: u.ID, RPID: c.RPID, Name: c.Name,
		CredentialID: base64.RawURLEncoding.EncodeToString(credential.ID), CredentialJSON: string(encoded),
	}); err != nil {
		if errors.Is(err, store.ErrConflict) {
			writeErr(w, 409, "이 패스키는 이미 aven 계정에 등록되어 있습니다")
			return
		}
		s.storeErr(w, err)
		return
	}
	codes, err := s.ensureRecoveryCodes(r.Context(), u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	s.audit(r, "user.updated", u.ID, u.Display()+" 패스키 추가")
	writeJSON(w, 200, map[string]any{"recovery_codes": codes})
}

func (s *Server) deletePasskey(w http.ResponseWriter, r *http.Request) {
	p, ok := s.requireBrowserSession(w, r)
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	u, err := s.Store.GetUser(r.Context(), p.User.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if !s.confirmPassword(w, r, u, body.Password) {
		return
	}
	count, err := s.Store.CountPasskeys(r.Context(), u.ID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	enforced, err := s.Store.Workspace2FAEnforced(r.Context())
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if enforced && !u.TOTPEnabled && count <= 1 {
		writeErr(w, 409, "작업 공간에서 2단계 인증을 요구하므로 마지막 패스키를 삭제할 수 없습니다")
		return
	}
	id := r.PathValue("id")
	if err := s.Store.DeletePasskey(r.Context(), u.ID, id); err != nil {
		s.storeErr(w, err)
		return
	}
	if !u.TOTPEnabled && count <= 1 {
		if err := s.Store.SetRecoveryCodes(r.Context(), u.ID, nil); err != nil {
			s.storeErr(w, err)
			return
		}
	}
	s.audit(r, "user.updated", u.ID, u.Display()+" 패스키 삭제")
	w.WriteHeader(204)
}

// beginPasskeyLogin: email/password가 있으면 두 번째 단계(login_factor), 없으면 비밀번호 없는 로그인(login_discoverable)을 시작한다.
func (s *Server) beginPasskeyLogin(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	origin, rpID, err := webAuthnOrigin(s, r)
	if err != nil {
		writeErr(w, 400, "이 주소에서는 패스키 로그인을 사용할 수 없습니다")
		return
	}
	wa, err := newWebAuthn(origin, rpID)
	if err != nil {
		writeErr(w, 400, "WebAuthn relying party 설정이 올바르지 않습니다")
		return
	}
	var options any
	var session *webauthn.SessionData
	var userID, purpose, acct string
	if body.Password != "" {
		email := store.NormalizeEmail(body.Email)
		acct = email
		if email == "" || email == store.BuiltinUserEmail {
			acct = "admin"
		}
		if s.lockedOut(w, r, acct, "로그인 시도가 너무 많습니다") {
			return
		}
		u := s.checkCredentials(r.Context(), email, body.Password)
		if u == nil || !passkeyLoginAllowed(s, u) {
			s.authFailed(w, r, acct, "로그인 시도가 너무 많습니다", "이메일 또는 비밀번호가 올바르지 않습니다")
			return
		}
		wu, err := s.loadWebAuthnUser(r.Context(), u, rpID)
		if err != nil {
			s.storeErr(w, err)
			return
		}
		if len(wu.credentials) == 0 {
			writeErr(w, 400, "이 주소에 등록된 패스키가 없습니다")
			return
		}
		options, session, err = wa.BeginLogin(wu, webauthn.WithUserVerification(protocol.VerificationRequired))
		userID, purpose, acct = u.ID, "login_factor", "passkey:"+u.ID
	} else {
		acct = discoverableAcct(s, r)
		if s.lockedOut(w, r, acct, "로그인 시도가 너무 많습니다") {
			return
		}
		options, session, err = wa.BeginDiscoverableLogin(webauthn.WithUserVerification(protocol.VerificationRequired))
		purpose = "login_discoverable"
	}
	if err != nil {
		writeErr(w, 400, "패스키 로그인을 시작하지 못했습니다")
		return
	}
	state, err := json.Marshal(session)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	challengeID, err := createWebAuthnChallenge(r.Context(), s, &store.WebAuthnChallenge{
		UserID: userID, Purpose: purpose, Origin: origin, RPID: rpID, SessionData: string(state),
	})
	if err != nil {
		s.challengeErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"challenge_id": challengeID, "flow": purpose, "options": options})
}

func (s *Server) finishPasskeyLogin(w http.ResponseWriter, r *http.Request, purpose string) {
	if !sameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	challengeID := r.PathValue("challenge")
	c, err := s.Store.ConsumeWebAuthnChallenge(r.Context(), hashSecret(challengeID), purpose, "")
	if err != nil {
		writeErr(w, 400, "패스키 로그인 요청이 만료되었거나 올바르지 않습니다")
		return
	}
	if !originForChallenge(s, r, c) {
		writeErr(w, 400, "패스키 로그인 요청이 올바르지 않습니다")
		return
	}
	wa, err := newWebAuthn(c.Origin, c.RPID)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	var session webauthn.SessionData
	if err := json.Unmarshal([]byte(c.SessionData), &session); err != nil {
		s.storeErr(w, err)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var wu *webAuthnUser
	var credential *webauthn.Credential
	acct := discoverableAcct(s, r)
	if purpose == "login_factor" {
		acct = "passkey:" + c.UserID
	}
	if s.lockedOut(w, r, acct, "로그인 시도가 너무 많습니다") {
		return
	}
	if purpose == "login_factor" {
		var u *store.User
		u, err = s.Store.GetUser(r.Context(), c.UserID)
		if err == nil && !passkeyLoginAllowed(s, u) {
			err = store.ErrNotFound
		}
		if err == nil {
			wu, err = s.loadWebAuthnUser(r.Context(), u, c.RPID)
		}
		if err == nil {
			credential, err = wa.FinishLogin(wu, session, r)
		}
	} else {
		var returned webauthn.User
		returned, credential, err = wa.FinishPasskeyLogin(func(rawID, _ []byte) (webauthn.User, error) {
			record, e := s.Store.PasskeyByCredentialID(r.Context(), c.RPID, base64.RawURLEncoding.EncodeToString(rawID))
			if e != nil {
				return nil, e
			}
			u, e := s.Store.GetUser(r.Context(), record.UserID)
			if e != nil || !passkeyLoginAllowed(s, u) {
				return nil, store.ErrNotFound
			}
			return s.loadWebAuthnUser(r.Context(), u, c.RPID)
		}, session, r)
		if err == nil {
			wu, _ = returned.(*webAuthnUser)
		}
	}
	if err != nil || wu == nil || credential == nil {
		s.authFailed(w, r, acct, "로그인 시도가 너무 많습니다", "패스키를 확인할 수 없습니다")
		return
	}
	if credential.Authenticator.CloneWarning {
		s.authFailed(w, r, acct, "로그인 시도가 너무 많습니다", "패스키 서명 카운터가 올바르지 않아 로그인을 거부했습니다")
		return
	}
	encoded, err := json.Marshal(credential)
	if err != nil {
		s.storeErr(w, err)
		return
	}
	record, err := s.Store.PasskeyByCredentialID(r.Context(), c.RPID, base64.RawURLEncoding.EncodeToString(credential.ID))
	if err != nil {
		s.storeErr(w, err)
		return
	}
	if err := s.Store.UpdatePasskeyCredential(r.Context(), wu.user.ID, record.ID, string(encoded)); err != nil {
		s.storeErr(w, err)
		return
	}
	s.authPassed(r, acct)
	if err := s.startSession(r.Context(), w, r, wu.user); err != nil {
		s.storeErr(w, err)
		return
	}
	s.Store.TouchUserLogin(r.Context(), wu.user.ID)
	s.audit(withPrincipal(r, &principal{User: wu.user, Via: viaSession}), "auth.login", wu.user.ID, wu.user.Display()+" 패스키 로그인")
	w.WriteHeader(204)
}

func (s *Server) finishPasskeyFactorLogin(w http.ResponseWriter, r *http.Request) {
	s.finishPasskeyLogin(w, r, "login_factor")
}

func (s *Server) finishPasskeyDiscoverableLogin(w http.ResponseWriter, r *http.Request) {
	s.finishPasskeyLogin(w, r, "login_discoverable")
}

// discoverableAcct는 비밀번호 없는 passkey 로그인의 잠금 키다. 계정을 모르므로 IP별로 센다(전역 키는 모두를 잠글 수 있다).
func discoverableAcct(s *Server, r *http.Request) string {
	return "passkey-ip:" + limitKey(s.clientIP(r))
}

// passkeyLoginAllowed: 비활성 계정과, KEEL_ADMIN_PASSWORD가 없어 세션을 만들 수 없는 기본 계정은 passkey로도 로그인하지 못한다.
func passkeyLoginAllowed(s *Server, u *store.User) bool {
	return !u.Disabled && (!u.Builtin || s.AdminPassword != "")
}

// confirmPassword는 민감한 계정 변경 전에 현재 비밀번호를 rate limit을 걸어 확인한다(실패 응답까지 쓴다).
func (s *Server) confirmPassword(w http.ResponseWriter, r *http.Request, u *store.User, password string) bool {
	if password == "" {
		writeErr(w, 400, "현재 비밀번호(password)가 필요합니다")
		return false
	}
	acct := "pw:" + u.ID
	if s.lockedOut(w, r, acct, "비밀번호 확인 시도가 너무 많습니다") {
		return false
	}
	if !s.checkUserPassword(u, password) {
		s.authFailed(w, r, acct, "비밀번호 확인 시도가 너무 많습니다", "비밀번호가 올바르지 않습니다")
		return false
	}
	s.authPassed(r, acct)
	return true
}

// ensureRecoveryCodes는 복구 코드가 없을 때만 새로 만들어 돌려준다(첫 패스키를 등록할 때).
func (s *Server) ensureRecoveryCodes(ctx context.Context, userID string) ([]string, error) {
	n, err := s.Store.CountRecoveryCodes(ctx, userID)
	if err != nil || n != 0 {
		return nil, err
	}
	codes := NewRecoveryCodes()
	hashes := make([]string, len(codes))
	for i, code := range codes {
		hashes[i] = hashSecret(code)
	}
	return codes, s.Store.SetRecoveryCodes(ctx, userID, hashes)
}
