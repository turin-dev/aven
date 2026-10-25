package api

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// ---- OAuth 2.0 / OpenID Connect provider ("Login with Aven") ----
//
// Authorization Code 흐름(RFC 6749)만 지원한다. public 앱은 PKCE(RFC 7636, S256)가 필수이고 confidential 앱은 권장이다.
// 비밀 값은 모두 "<prefix><id>.<secret>" 형식이며 DB에는 secret의 SHA-256만 저장한다. 비교는 상수 시간으로 한다.
// access token은 Aven API(/api/...)에 Bearer로 쓰고, 권한은 min(사용자의 Aven 역할, 승인한 scope)이다(authz.go).
// ID token은 KEEL_DATA/session.key에서 유도한 Ed25519 키로 서명한다(JWKS로 공개).

const (
	oauthAccessTokenPrefix  = "keel_at_"
	oauthRefreshTokenPrefix = "keel_rt_"
	oauthCodePrefix         = "keel_code_"
	oauthRequestPrefix      = "keel_req_"
	oauthClientSecretPrefix = "keel_secret_"
	oauthClientIDPrefix     = "keel_client_"
	oauthAccessTTL          = time.Hour
	oauthRefreshTTL         = 365 * 24 * time.Hour
	oauthCodeTTL            = 5 * time.Minute
	oauthRequestTTL         = 10 * time.Minute
	oauthIDTokenTTL         = time.Hour
)

var oauthScopeDescriptions = map[string]string{
	"openid":           "Aven 계정으로 로그인하고 고유 ID를 확인합니다.",
	"profile":          "계정의 표시 이름을 확인합니다.",
	"email":            "계정 이메일 주소를 확인합니다.",
	"offline_access":   "다시 로그인하지 않아도 계속 접근할 수 있도록 refresh token을 발급합니다.",
	"workspace:viewer": "내가 접근할 수 있는 작업 공간의 프로젝트를 Viewer 권한으로 조회합니다.",
	"workspace:member": "내가 멤버인 작업 공간에서 내 권한 안으로 프로젝트와 리소스를 만들고 변경합니다.",
	"workspace:admin":  "내가 관리하는 작업 공간의 프로젝트를 Admin 권한으로 관리합니다.",
	"project:viewer":   "선택한 프로젝트를 Viewer 권한으로 조회합니다.",
	"project:member":   "선택한 프로젝트의 리소스를 Member 권한으로 만들고 변경합니다.",
}

func oauthScopeLevel(scopes []string, prefix string) int {
	lv := lvNone
	for _, scope := range scopes {
		if !strings.HasPrefix(scope, prefix) {
			continue
		}
		switch scope {
		case "workspace:viewer", "project:viewer":
			lv = max(lv, lvViewer)
		case "workspace:member", "project:member":
			lv = max(lv, lvMember)
		case "workspace:admin":
			lv = max(lv, lvAdmin)
		}
	}
	return lv
}

func hasOAuthScope(scopes []string, want string) bool { return slices.Contains(scopes, want) }

func sortedOAuthScopes() []string {
	out := make([]string, 0, len(oauthScopeDescriptions))
	for scope := range oauthScopeDescriptions {
		out = append(out, scope)
	}
	sort.Strings(out)
	return out
}

// parseOAuthScopes는 공백으로 나눈 scope 목록을 검사하고 정렬해 돌려준다. openid는 필수다.
func parseOAuthScopes(raw string) ([]string, error) {
	parts := strings.Fields(raw)
	if len(parts) == 0 {
		return nil, errors.New("scope is required")
	}
	seen := make(map[string]bool, len(parts))
	out := make([]string, 0, len(parts))
	for _, scope := range parts {
		if _, ok := oauthScopeDescriptions[scope]; !ok {
			return nil, fmt.Errorf("unsupported scope: %.64q", scope)
		}
		if !seen[scope] { // 중복은 무시한다(RFC 6749는 순서·중복 없는 집합으로 본다)
			seen[scope] = true
			out = append(out, scope)
		}
	}
	if !seen["openid"] {
		return nil, errors.New("openid scope is required")
	}
	sort.Strings(out)
	return out, nil
}

// parseOAuthOpaque는 "<prefix><id>.<base64url secret>" 값을 나눈다. 형식이 틀리면 ok=false.
func parseOAuthOpaque(value, prefix string) (id, secret string, ok bool) {
	if len(value) > 256 {
		return "", "", false
	}
	rest, ok := strings.CutPrefix(value, prefix)
	if !ok {
		return "", "", false
	}
	id, secret, ok = strings.Cut(rest, ".")
	if !ok || id == "" || len(id) > 64 || secret == "" {
		return "", "", false
	}
	for _, ch := range id {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
			return "", "", false
		}
	}
	decoded, err := base64.RawURLEncoding.DecodeString(secret)
	if err != nil || len(decoded) < 32 {
		return "", "", false
	}
	return id, secret, true
}

// newOAuthOpaque는 새 비밀 값과 저장할 해시를 만든다.
func newOAuthOpaque(prefix, idPrefix string) (id, value, digest string) {
	id, secret := store.NewID(idPrefix), randomToken(32)
	return id, prefix + id + "." + secret, hashSecret(secret)
}

// oauthAnyRouteAllowed는 OAuth access token이 쓸 수 있는 "인증만 필요한" 라우트다(그 밖의 accAny 라우트는 거부).
func (s *Server) oauthAnyRouteAllowed(p *principal, path string) bool {
	switch {
	case path == "/api/auth/me":
		return true
	case path == "/api/projects":
		return oauthScopeLevel(p.OAuth.Scopes, "workspace:") >= lvViewer || oauthScopeLevel(p.OAuth.Scopes, "project:") >= lvViewer
	case path == "/api/system", path == "/api/nodes", strings.HasPrefix(path, "/api/nodes/"):
		return oauthScopeLevel(p.OAuth.Scopes, "workspace:") >= lvViewer
	}
	return false
}

// oauthIssuer는 issuer URL(<공개 URL>/oauth)이다. KEEL_PUBLIC_URL이 없으면 요청의 Host로 만든다.
func (s *Server) oauthIssuer(r *http.Request) string {
	base := strings.TrimSpace(s.PublicURL)
	if base == "" {
		if r.Host == "" || strings.ContainsAny(r.Host, " \t\r\n/\\@?#") {
			return ""
		}
		scheme := "http"
		if s.secureTransport(r) {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	u.Path, u.RawPath = strings.TrimRight(u.Path, "/")+"/oauth", ""
	return u.String()
}

func (s *Server) oauthSigningKey() (ed25519.PrivateKey, string) {
	if len(s.SessionKey) < 32 {
		return nil, ""
	}
	seed := sha256.Sum256(append([]byte("keel/oauth/oidc/ed25519/v1\x00"), s.SessionKey...))
	private := ed25519.NewKeyFromSeed(seed[:])
	kidHash := sha256.Sum256(private.Public().(ed25519.PublicKey))
	return private, base64.RawURLEncoding.EncodeToString(kidHash[:16])
}

// ---- discovery ----

func (s *Server) oauthDiscovery(w http.ResponseWriter, r *http.Request) {
	issuer := s.oauthIssuer(r)
	if _, kid := s.oauthSigningKey(); issuer == "" || kid == "" {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "OAuth is not configured")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                         issuer,
		"authorization_endpoint":                         issuer + "/auth",
		"token_endpoint":                                 issuer + "/token",
		"userinfo_endpoint":                              issuer + "/me",
		"jwks_uri":                                       issuer + "/jwks",
		"revocation_endpoint":                            issuer + "/revoke",
		"response_types_supported":                       []string{"code"},
		"response_modes_supported":                       []string{"query"},
		"grant_types_supported":                          []string{"authorization_code", "refresh_token"},
		"subject_types_supported":                        []string{"public"},
		"id_token_signing_alg_values_supported":          []string{"EdDSA"},
		"token_endpoint_auth_methods_supported":          []string{"client_secret_basic", "client_secret_post", "none"},
		"revocation_endpoint_auth_methods_supported":     []string{"client_secret_basic", "client_secret_post", "none"},
		"code_challenge_methods_supported":               []string{"S256"},
		"prompt_values_supported":                        []string{"consent", "none"},
		"scopes_supported":                               sortedOAuthScopes(),
		"claims_supported":                               []string{"iss", "sub", "aud", "exp", "iat", "nonce", "email", "name"},
		"authorization_response_iss_parameter_supported": true,
	})
}

func (s *Server) oauthJWKS(w http.ResponseWriter, r *http.Request) {
	private, kid := s.oauthSigningKey()
	if kid == "" {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "OAuth signing key is not configured")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=3600")
	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "OKP", "crv": "Ed25519", "use": "sig", "alg": "EdDSA", "kid": kid,
		"x": base64.RawURLEncoding.EncodeToString(private.Public().(ed25519.PublicKey)),
	}}})
}

// ---- authorization endpoint ----

// oauthAuthorize는 인가 요청을 검사한다. client_id와 redirect_uri가 확인되기 전에는 절대 redirect하지 않는다(RFC 6749 4.1.2.1).
// 로그인하지 않았으면 로그인 화면으로, 이전 승인으로 충분하면 바로 코드를 발급하고, 아니면 동의 화면으로 보낸다.
func (s *Server) oauthAuthorize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.Store == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "OAuth storage is unavailable")
		return
	}
	if len(r.URL.RawQuery) > 8192 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "request is too large")
		return
	}
	q := r.URL.Query()
	clientID, okClient := oauthSingle(q, "client_id")
	redirectURI, okRedirect := oauthSingle(q, "redirect_uri")
	if !okClient || !okRedirect || clientID == "" || redirectURI == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "client_id and redirect_uri are required exactly once")
		return
	}
	app, err := s.Store.GetOAuthAppByClientID(r.Context(), clientID)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "unknown client_id")
		return
	}
	if !registeredRedirectURI(app, redirectURI) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "redirect_uri does not exactly match a registered URI")
		return
	}
	// 여기부터 오류는 등록된 redirect URI로 돌려보낸다.
	issuer := s.oauthIssuer(r)
	state, okState := oauthSingle(q, "state")
	fail := func(code, desc string) {
		if !okState {
			state = ""
		}
		oauthRedirect(w, r, redirectURI, issuer, url.Values{"error": {code}, "error_description": {desc}}, state)
	}
	if issuer == "" {
		fail("server_error", "OAuth issuer is not configured")
		return
	}
	params := map[string]string{}
	for _, key := range []string{"response_type", "scope", "code_challenge", "code_challenge_method", "nonce", "prompt", "request_uri", "request", "response_mode"} {
		v, ok := oauthSingle(q, key)
		if !ok {
			fail("invalid_request", key+" must not be repeated")
			return
		}
		params[key] = v
	}
	if !okState {
		fail("invalid_request", "state must not be repeated")
		return
	}
	switch {
	case params["request"] != "":
		fail("request_not_supported", "request objects are not supported")
		return
	case params["request_uri"] != "":
		fail("request_uri_not_supported", "pushed authorization requests are not supported")
		return
	case params["response_type"] != "code":
		fail("unsupported_response_type", "response_type must be code")
		return
	case params["response_mode"] != "" && params["response_mode"] != "query":
		fail("invalid_request", "only response_mode=query is supported")
		return
	case len(state) > 1024 || len(params["nonce"]) > 512:
		fail("invalid_request", "state or nonce is too long")
		return
	}
	prompt := params["prompt"]
	if prompt != "" && prompt != "consent" && prompt != "none" {
		fail("invalid_request", "prompt must be consent or none")
		return
	}
	scopes, err := parseOAuthScopes(params["scope"])
	if err != nil {
		fail("invalid_scope", err.Error())
		return
	}
	if prompt != "consent" {
		// OIDC Core 11: offline_access는 prompt=consent일 때만 받아들이고, 아니면 무시한다(refresh token 없음).
		scopes = slices.DeleteFunc(scopes, func(s string) bool { return s == "offline_access" })
	}
	challenge, method := params["code_challenge"], params["code_challenge_method"]
	switch {
	case challenge == "" && method != "":
		fail("invalid_request", "code_challenge_method without code_challenge")
		return
	case challenge == "" && app.ClientType == "public":
		fail("invalid_request", "PKCE (code_challenge with S256) is required for public clients")
		return
	case challenge != "" && method != "S256":
		fail("invalid_request", "code_challenge_method must be S256")
		return
	case challenge != "" && !validPKCEChallenge(challenge):
		fail("invalid_request", "invalid code_challenge")
		return
	}

	p := s.oauthSession(w, r)
	if p == nil {
		if prompt == "none" {
			fail("login_required", "the user is not logged in")
			return
		}
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			// 세션 쿠키는 SameSite=Strict라 다른 사이트에서 넘어온 이동에는 붙지 않는다.
			// 같은 origin 문서에서 한 번 더 이동하면 쿠키가 붙으므로 이미 로그인한 사용자는 다시 로그인하지 않는다.
			oauthBounce(w, r.URL.RequestURI())
			return
		}
		http.Redirect(w, r, "/login/?"+url.Values{"next": {r.URL.RequestURI()}}.Encode(), http.StatusSeeOther)
		return
	}

	if prompt != "consent" {
		// 자동 동의: 이전 승인이 요청한 scope를 모두 덮으면 동의 화면 없이 그 승인으로 코드를 준다.
		if g, err := s.Store.ActiveOAuthGrant(r.Context(), app.ID, p.User.ID); err == nil && oauthScopesCovered(scopes, g.Scopes) {
			codeID, codeValue, codeHash := newOAuthOpaque(oauthCodePrefix, "ocod")
			code := &store.OAuthAuthorizationCode{
				ID: codeID, CodeHash: codeHash, AppID: app.ID, UserID: p.User.ID, GrantID: g.ID, RedirectURI: redirectURI,
				Scopes: scopes, ProjectIDs: g.ProjectIDs, CodeChallenge: challenge, Nonce: params["nonce"], ExpiresAt: time.Now().Add(oauthCodeTTL),
			}
			if err := s.Store.CreateOAuthAuthorizationCode(r.Context(), code); err != nil {
				fail("server_error", "could not issue authorization code")
				return
			}
			oauthRedirect(w, r, redirectURI, issuer, url.Values{"code": {codeValue}}, state)
			return
		}
		if prompt == "none" {
			fail("consent_required", "the user has not approved these scopes")
			return
		}
	}

	requestID, requestValue, requestHash := newOAuthOpaque(oauthRequestPrefix, "oreq")
	pending := &store.OAuthPendingRequest{
		ID: requestID, RequestHash: requestHash, AppID: app.ID, UserID: p.User.ID, RedirectURI: redirectURI,
		State: state, Scopes: scopes, CodeChallenge: challenge, Nonce: params["nonce"], ExpiresAt: time.Now().Add(oauthRequestTTL),
	}
	if err := s.Store.CreateOAuthPendingRequest(r.Context(), pending); err != nil {
		fail("server_error", "could not store authorization request")
		return
	}
	// 요청 토큰은 fragment에 둔다(서버 로그, Referer에 남지 않는다).
	http.Redirect(w, r, "/oauth/consent/#"+url.Values{"request": {requestValue}}.Encode(), http.StatusSeeOther)
}

// oauthSession은 브라우저 세션 쿠키의 사용자다(API 토큰으로는 인가할 수 없다).
func (s *Server) oauthSession(w http.ResponseWriter, r *http.Request) *principal {
	c, err := r.Cookie(cookieName)
	if err != nil {
		return nil
	}
	p, refresh := s.sessionPrincipal(r.Context(), c.Value)
	if p != nil && refresh {
		http.SetCookie(w, s.sessionCookie(r, c.Value, time.Now().Add(sessionTTL), 0))
	}
	return p
}

// oauthBounce는 같은 origin에서 target으로 다시 이동하는 작은 HTML이다(script 없이 meta refresh).
func oauthBounce(w http.ResponseWriter, target string) {
	esc := html.EscapeString(target)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `<!doctype html><html lang="ko"><head><meta charset="utf-8"><meta http-equiv="refresh" content="0;url=%s"><title>aven</title></head>`+
		`<body><p><a href="%s">Aven로 계속하기</a></p></body></html>`, esc, esc)
}

func oauthScopesCovered(requested, granted []string) bool {
	for _, scope := range requested {
		if !hasOAuthScope(granted, scope) {
			return false
		}
	}
	return true
}

func validPKCEChallenge(challenge string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(challenge)
	return len(challenge) == 43 && err == nil && len(decoded) == sha256.Size
}

// validPKCEVerifier: RFC 7636 4.1 code-verifier = 43*128 unreserved.
func validPKCEVerifier(verifier string) bool {
	if len(verifier) < 43 || len(verifier) > 128 {
		return false
	}
	for _, ch := range verifier {
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '-' && ch != '.' && ch != '_' && ch != '~' {
			return false
		}
	}
	return true
}

func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func oauthSingle(q url.Values, key string) (string, bool) {
	switch v := q[key]; len(v) {
	case 0:
		return "", true
	case 1:
		return v[0], true
	}
	return "", false
}

// oauthAuthorizationRedirect는 등록된 redirect URI에 결과 파라미터(+state, iss)를 붙인다. URI의 기존 query는 유지한다.
func oauthAuthorizationRedirect(redirectURI, issuer string, params url.Values, state string) (string, error) {
	u, err := url.Parse(redirectURI)
	if err != nil {
		return "", err
	}
	q := u.Query()
	for k, v := range params {
		q[k] = v
	}
	if state != "" {
		q.Set("state", state)
	}
	if issuer != "" {
		q.Set("iss", issuer) // RFC 9207 mix-up 방지
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func oauthRedirect(w http.ResponseWriter, r *http.Request, redirectURI, issuer string, params url.Values, state string) {
	target, err := oauthAuthorizationRedirect(redirectURI, issuer, params, state)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri", "registered redirect URI is invalid")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// ---- token endpoint ----

func (s *Server) oauthToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if s.Store == nil || s.oauthIssuer(r) == "" {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "OAuth is not configured")
		return
	}
	if _, kid := s.oauthSigningKey(); kid == "" {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "OAuth signing key is not configured")
		return
	}
	form, ok := oauthForm(w, r, "grant_type", "client_id", "client_secret", "code", "redirect_uri", "code_verifier", "refresh_token", "scope")
	if !ok {
		return
	}
	app, ok := s.oauthClient(w, r, form)
	if !ok {
		return
	}
	switch form.Get("grant_type") {
	case "authorization_code":
		s.oauthExchangeCode(w, r, app, form)
	case "refresh_token":
		s.oauthRefresh(w, r, app, form)
	case "":
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type must be authorization_code or refresh_token")
	}
}

// oauthForm은 token/revoke 요청 본문(application/x-www-form-urlencoded)을 읽는다. 파라미터는 query에 올 수 없고 반복될 수 없다.
func oauthForm(w http.ResponseWriter, r *http.Request, keys ...string) (url.Values, bool) {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-www-form-urlencoded" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "use application/x-www-form-urlencoded")
		return nil, false
	}
	if r.URL.RawQuery != "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "parameters must be sent in the request body")
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "invalid form body")
		return nil, false
	}
	for _, key := range keys {
		if len(r.PostForm[key]) > 1 {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", key+" must not be repeated")
			return nil, false
		}
	}
	return r.PostForm, true
}

// oauthClient는 클라이언트를 인증한다(client_secret_basic, client_secret_post, public은 none).
// 실패를 반복하는 IP는 잠근다.
func (s *Server) oauthClient(w http.ResponseWriter, r *http.Request, form url.Values) (*store.OAuthApp, bool) {
	key := limitKey(s.clientIP(r))
	if wait := s.lim().agentIP.wait(key); wait > 0 {
		tooMany(w, wait, "인증 시도가 너무 많습니다")
		return nil, false
	}
	app, err := s.authenticateOAuthClient(r, form)
	if err != nil {
		s.lim().agentIP.fail(key)
		w.Header().Set("WWW-Authenticate", `Basic realm="keel-oauth"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_client", "client authentication failed")
		return nil, false
	}
	return app, true
}

var errOAuthClient = errors.New("invalid client")

func (s *Server) authenticateOAuthClient(r *http.Request, form url.Values) (*store.OAuthApp, error) {
	clientID, secret := form.Get("client_id"), form.Get("client_secret")
	basic := r.Header.Get("Authorization") != ""
	if basic {
		if secret != "" {
			return nil, errOAuthClient // 인증 방법은 하나만(RFC 6749 2.3)
		}
		id, password, ok := r.BasicAuth()
		if !ok {
			return nil, errOAuthClient
		}
		// RFC 6749 2.3.1: client_id와 secret은 form-urlencode한 뒤 Basic으로 보낸다.
		decodedID, errID := url.QueryUnescape(id)
		decodedSecret, errSecret := url.QueryUnescape(password)
		if errID != nil || errSecret != nil || decodedID == "" || decodedSecret == "" || (clientID != "" && clientID != decodedID) {
			return nil, errOAuthClient
		}
		clientID, secret = decodedID, decodedSecret
	}
	if clientID == "" || len(clientID) > 128 {
		return nil, errOAuthClient
	}
	app, err := s.Store.GetOAuthAppByClientID(r.Context(), clientID)
	if err != nil {
		return nil, errOAuthClient
	}
	switch app.ClientType {
	case "public":
		if secret != "" {
			return nil, errOAuthClient // public 앱이 secret을 보내면 실패한다
		}
		return app, nil
	case "confidential":
		id, raw, ok := parseOAuthOpaque(secret, oauthClientSecretPrefix)
		if !ok {
			return nil, errOAuthClient
		}
		stored, err := s.Store.GetOAuthClientSecret(r.Context(), id)
		if err != nil || stored.AppID != app.ID || !ctEqual(stored.SecretHash, hashSecret(raw)) {
			return nil, errOAuthClient
		}
		return app, nil
	}
	return nil, errOAuthClient
}

func (s *Server) oauthExchangeCode(w http.ResponseWriter, r *http.Request, app *store.OAuthApp, form url.Values) {
	codeID, codeSecret, ok := parseOAuthOpaque(form.Get("code"), oauthCodePrefix)
	redirectURI, verifier := form.Get("redirect_uri"), form.Get("code_verifier")
	if !ok || redirectURI == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code and redirect_uri are required")
		return
	}
	if verifier != "" && !validPKCEVerifier(verifier) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "invalid code_verifier")
		return
	}
	// PKCE: 인가 요청에 challenge가 있었으면 verifier가 맞아야 하고, 없었으면 verifier를 보내면 안 된다(다운그레이드 방지).
	verify := func(c *store.OAuthAuthorizationCode) bool {
		if c.CodeChallenge == "" {
			return verifier == "" && app.ClientType == "confidential"
		}
		return verifier != "" && subtle.ConstantTimeCompare([]byte(pkceS256(verifier)), []byte(c.CodeChallenge)) == 1
	}
	accessID, accessValue, accessHash := newOAuthOpaque(oauthAccessTokenPrefix, "oat")
	refreshID, refreshValue, refreshHash := newOAuthOpaque(oauthRefreshTokenPrefix, "ort")
	access := &store.OAuthAccessToken{ID: accessID, TokenHash: accessHash, ExpiresAt: time.Now().Add(oauthAccessTTL)}
	// refresh token은 코드에 offline_access가 있을 때만 저장된다.
	refresh := &store.OAuthRefreshToken{ID: refreshID, TokenHash: refreshHash, ExpiresAt: time.Now().Add(oauthRefreshTTL)}
	code, grant, err := s.Store.ExchangeOAuthAuthorizationCode(r.Context(), codeID, hashSecret(codeSecret), app.ID, redirectURI, verify, access, refresh)
	if err != nil {
		if errors.Is(err, store.ErrOAuthCodeReplay) {
			s.Log.Warn("oauth authorization code reused; grant revoked", "client_id", app.ClientID)
		}
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "authorization code is invalid, expired, or already used")
		return
	}
	if !hasOAuthScope(code.Scopes, "offline_access") {
		refreshValue = ""
	}
	u, ok := s.oauthGrantUser(w, r, grant)
	if !ok {
		return
	}
	s.writeOAuthTokens(w, r, app, u, grant, code.Nonce, accessValue, refreshValue, access.ExpiresAt)
}

func (s *Server) oauthRefresh(w http.ResponseWriter, r *http.Request, app *store.OAuthApp, form url.Values) {
	tokenID, tokenSecret, ok := parseOAuthOpaque(form.Get("refresh_token"), oauthRefreshTokenPrefix)
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token is invalid")
		return
	}
	accessID, accessValue, accessHash := newOAuthOpaque(oauthAccessTokenPrefix, "oat")
	refreshID, refreshValue, refreshHash := newOAuthOpaque(oauthRefreshTokenPrefix, "ort")
	access := &store.OAuthAccessToken{ID: accessID, TokenHash: accessHash, ExpiresAt: time.Now().Add(oauthAccessTTL)}
	refresh := &store.OAuthRefreshToken{ID: refreshID, TokenHash: refreshHash, ExpiresAt: time.Now().Add(oauthRefreshTTL)}
	// RFC 6749 6: scope를 보내면 원래 승인의 일부여야 한다(승인 자체는 바꾸지 않는다).
	requested := strings.Fields(form.Get("scope"))
	grant, err := s.Store.RotateOAuthRefreshToken(r.Context(), tokenID, hashSecret(tokenSecret), app.ID,
		func(g *store.OAuthGrant) bool { return oauthScopesCovered(requested, g.Scopes) }, access, refresh)
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "refresh_token is invalid, expired, revoked, or the scope exceeds the grant")
		return
	}
	u, ok := s.oauthGrantUser(w, r, grant)
	if !ok {
		return
	}
	s.writeOAuthTokens(w, r, app, u, grant, "", accessValue, refreshValue, access.ExpiresAt)
}

func (s *Server) oauthGrantUser(w http.ResponseWriter, r *http.Request, grant *store.OAuthGrant) (*store.User, bool) {
	u, err := s.Store.GetUser(r.Context(), grant.UserID)
	if err != nil || u.Disabled {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "user account is unavailable")
		return nil, false
	}
	return u, true
}

func (s *Server) writeOAuthTokens(w http.ResponseWriter, r *http.Request, app *store.OAuthApp, u *store.User, grant *store.OAuthGrant, nonce, access, refresh string, accessExpiry time.Time) {
	idToken, err := s.signOAuthIDToken(r, app.ClientID, u, grant.Scopes, nonce)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "could not sign ID token")
		return
	}
	scopes := grant.Scopes
	if refresh == "" {
		scopes = slices.DeleteFunc(slices.Clone(scopes), func(s string) bool { return s == "offline_access" })
	}
	out := map[string]any{
		"access_token": access,
		"token_type":   "Bearer",
		"expires_in":   int(time.Until(accessExpiry).Round(time.Second).Seconds()),
		"scope":        strings.Join(scopes, " "),
		"id_token":     idToken,
	}
	if refresh != "" {
		out["refresh_token"] = refresh
	}
	writeJSON(w, http.StatusOK, out)
}

// signOAuthIDToken은 EdDSA(Ed25519)로 서명한 ID token(JWT)을 만든다.
func (s *Server) signOAuthIDToken(r *http.Request, clientID string, u *store.User, scopes []string, nonce string) (string, error) {
	private, kid := s.oauthSigningKey()
	issuer := s.oauthIssuer(r)
	if kid == "" || issuer == "" {
		return "", errors.New("OAuth signing key or issuer unavailable")
	}
	now := time.Now()
	header, _ := json.Marshal(map[string]string{"alg": "EdDSA", "kid": kid, "typ": "JWT"})
	claims := map[string]any{"iss": issuer, "sub": u.ID, "aud": clientID, "iat": now.Unix(), "exp": now.Add(oauthIDTokenTTL).Unix()}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	for k, v := range oauthUserClaims(u, scopes) {
		claims[k] = v
	}
	body, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	input := base64.RawURLEncoding.EncodeToString(header) + "." + base64.RawURLEncoding.EncodeToString(body)
	return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input))), nil
}

// oauthUserClaims는 승인한 scope에 따른 사용자 claim이다(sub는 항상).
func oauthUserClaims(u *store.User, scopes []string) map[string]any {
	claims := map[string]any{"sub": u.ID}
	if hasOAuthScope(scopes, "email") && u.Email != "" {
		claims["email"] = u.Email
	}
	if hasOAuthScope(scopes, "profile") {
		claims["name"] = u.Display()
	}
	return claims
}

// ---- userinfo / revoke ----

// oauthUserInfo는 /oauth/me(OIDC userinfo)다. OAuth access token만 받는다.
func (s *Server) oauthUserInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var p *principal
	if parts := strings.Fields(r.Header.Get("Authorization")); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") && strings.HasPrefix(parts[1], oauthAccessTokenPrefix) {
		p = s.bearerPrincipal(r.Context(), parts[1])
	}
	if p == nil || p.OAuth == nil {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		writeOAuthError(w, http.StatusUnauthorized, "invalid_token", "a valid OAuth access token is required")
		return
	}
	writeJSON(w, http.StatusOK, oauthUserClaims(p.User, p.OAuth.Scopes))
}

// oauthRevoke는 RFC 7009 토큰 폐기다. 모르는 토큰도 200을 돌려준다.
func (s *Server) oauthRevoke(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.Store == nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "OAuth storage is unavailable")
		return
	}
	form, ok := oauthForm(w, r, "client_id", "client_secret", "token", "token_type_hint")
	if !ok {
		return
	}
	app, ok := s.oauthClient(w, r, form)
	if !ok {
		return
	}
	token := form.Get("token")
	if token == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "token is required")
		return
	}
	var err error
	if id, secret, ok := parseOAuthOpaque(token, oauthRefreshTokenPrefix); ok {
		err = s.Store.RevokeOAuthToken(r.Context(), id, hashSecret(secret), app.ID, true)
	} else if id, secret, ok := parseOAuthOpaque(token, oauthAccessTokenPrefix); ok {
		err = s.Store.RevokeOAuthToken(r.Context(), id, hashSecret(secret), app.ID, false)
	}
	if err != nil {
		writeOAuthError(w, http.StatusServiceUnavailable, "server_error", "could not revoke token")
		return
	}
	w.WriteHeader(http.StatusOK)
}

// oauthCORS는 브라우저에서 동작하는 public 클라이언트(SPA)가 token/userinfo/revoke/discovery를 부를 수 있게 한다.
// 이 엔드포인트들은 쿠키를 쓰지 않으므로(Allow-Credentials 없음) 모든 origin을 허용해도 안전하다.
func oauthCORS(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		hd.Set("Access-Control-Allow-Origin", "*")
		if r.Method == http.MethodOptions {
			hd.Set("Access-Control-Allow-Methods", "GET, POST")
			hd.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			hd.Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h(w, r)
	}
}

func writeOAuthError(w http.ResponseWriter, status int, code, description string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	writeJSON(w, status, map[string]string{"error": code, "error_description": description})
}

// oauthPrincipal은 OAuth access token(keel_at_)의 주체다. 권한은 authz.go에서 승인한 scope로 줄어든다.
func (s *Server) oauthPrincipal(ctx context.Context, tok string) *principal {
	if s.Store == nil {
		return nil
	}
	id, secret, ok := parseOAuthOpaque(tok, oauthAccessTokenPrefix)
	if !ok {
		return nil
	}
	t, g, err := s.Store.GetOAuthAccessToken(ctx, id)
	if err != nil || !ctEqual(t.TokenHash, hashSecret(secret)) {
		return nil
	}
	u, err := s.Store.GetUser(ctx, g.UserID)
	if err != nil || u.Disabled {
		return nil
	}
	if u.Builtin {
		u.Role = store.RoleOwner
	}
	if g.LastUsedAt == nil || time.Since(*g.LastUsedAt) > tokenTouch {
		s.Store.TouchOAuthGrant(context.WithoutCancel(ctx), g.ID)
	}
	return &principal{User: u, Via: viaOAuth, OAuth: g}
}

// oauthMethod는 허용한 메서드(와 CORS preflight용 OPTIONS)만 통과시킨다.
func oauthMethod(allowed string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodOptions && !slices.Contains(strings.Split(allowed, ", "), r.Method) {
			w.Header().Set("Allow", allowed)
			writeOAuthError(w, http.StatusMethodNotAllowed, "invalid_request", "method not allowed")
			return
		}
		h(w, r)
	}
}
