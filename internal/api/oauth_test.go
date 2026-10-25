package api

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func init() {
	// OAuth 자기 서비스 라우트(TestRouteAuthzMatrix): 자기 승인과 자기 인가 요청만 다루며 핸들러가 소유자를 검사한다.
	selfService["PATCH /api/oauth/authorized-apps/{grantid}"] = [2]bool{true, true}
	selfService["DELETE /api/oauth/authorized-apps/{grantid}"] = [2]bool{true, true}
	selfService["POST /api/oauth/requests/{requestid}"] = [2]bool{true, true}
}

const (
	oauthTestIssuer   = "https://aven.example.com/oauth"
	oauthTestRedirect = "https://app.example.com/callback"
	oauthTestVerifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk_extra_entropy"
)

type oauthFixture struct {
	t                *testing.T
	s                *Server
	h                http.Handler
	st               *store.Store
	admin, member    *store.User
	adminC, memberC  string
	p1, p2           *store.Project
	env2             *store.Environment // p2의 기본 환경
	clientID, secret string             // confidential 앱
	publicID         string             // public 앱
}

func newOAuthFixture(t *testing.T) *oauthFixture {
	t.Helper()
	ctx := context.Background()
	st := testStore(t)
	s := &Server{Store: st, SessionKey: []byte("0123456789abcdef0123456789abcdef"), PublicURL: "https://aven.example.com",
		Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f := &oauthFixture{t: t, s: s, h: s.Handler(), st: st}
	f.admin = testUser(t, st, "admin@example.com", store.RoleAdmin)
	f.member = testUser(t, st, "member@example.com", store.RoleMember)
	f.adminC, f.memberC = testSessionCookie(t, s, f.admin), testSessionCookie(t, s, f.member)
	f.p1, f.p2 = &store.Project{Name: "shared"}, &store.Project{Name: "private"}
	for _, p := range []*store.Project{f.p1, f.p2} {
		env, err := st.CreateProject(ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		f.env2 = env
		if err := st.SetMember(ctx, p.ID, f.member.ID, store.RoleMember); err != nil {
			t.Fatal(err)
		}
	}
	w := req(f.h, "POST", "/api/oauth/apps", `{"name":"Acme","description":"CI dashboard","client_type":"confidential","redirect_uris":["`+oauthTestRedirect+`","http://localhost:3000/cb"]}`, withCookie(f.adminC))
	if w.Code != 201 {
		t.Fatalf("create app: %d %s", w.Code, w.Body)
	}
	created := decodeBody[struct {
		App    store.OAuthApp `json:"app"`
		Secret string         `json:"client_secret"`
	}](t, w)
	f.clientID, f.secret = created.App.ClientID, created.Secret
	if !strings.HasPrefix(f.secret, oauthClientSecretPrefix) {
		t.Fatalf("client secret %q", f.secret)
	}
	w = req(f.h, "POST", "/api/oauth/apps", `{"name":"CLI","client_type":"public","redirect_uris":["http://127.0.0.1:8765/cb","com.example.cli:/cb"]}`, withCookie(f.adminC))
	if w.Code != 201 {
		t.Fatalf("create public app: %d %s", w.Code, w.Body)
	}
	pub := decodeBody[struct {
		App    store.OAuthApp `json:"app"`
		Secret *string        `json:"client_secret"`
	}](t, w)
	if pub.Secret != nil {
		t.Fatal("public app got a client secret")
	}
	f.publicID = pub.App.ClientID
	return f
}

func authorizeURL(clientID, redirect, scope string, extra url.Values) string {
	q := url.Values{"response_type": {"code"}, "client_id": {clientID}, "redirect_uri": {redirect}, "scope": {scope}, "state": {"st4te"}}
	for k, v := range extra {
		q[k] = v
	}
	return "/oauth/auth?" + q.Encode()
}

func pkce() url.Values {
	return url.Values{"code_challenge": {pkceS256(oauthTestVerifier)}, "code_challenge_method": {"S256"}}
}

func merge(a, b url.Values) url.Values {
	out := url.Values{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

// consent는 authorize → 동의 화면 → 승인까지 진행하고 앱으로 돌아갈 URL을 돌려준다.
func (f *oauthFixture) consent(cookie, authorize string, decision map[string]any) *url.URL {
	f.t.Helper()
	w := req(f.h, "GET", authorize, "", withCookie(cookie))
	loc, _ := url.Parse(w.Header().Get("Location"))
	if w.Code != http.StatusSeeOther || loc == nil || loc.Path != "/oauth/consent/" {
		f.t.Fatalf("authorize: %d %s (%s)", w.Code, w.Header().Get("Location"), w.Body)
	}
	frag, _ := url.ParseQuery(loc.Fragment)
	reqID := frag.Get("request")
	if w := req(f.h, "GET", "/api/oauth/requests/"+reqID, "", withCookie(cookie)); w.Code != 200 {
		f.t.Fatalf("consent request: %d %s", w.Code, w.Body)
	}
	body, _ := json.Marshal(decision)
	w = req(f.h, "POST", "/api/oauth/requests/"+reqID, string(body), withCookie(cookie))
	if w.Code != 200 {
		f.t.Fatalf("finish consent: %d %s", w.Code, w.Body)
	}
	out, err := url.Parse(decodeBody[map[string]string](f.t, w)["redirect_uri"])
	if err != nil {
		f.t.Fatal(err)
	}
	return out
}

func tokenReq(h http.Handler, form url.Values, basicID, basicSecret string) *httptest.ResponseRecorder {
	return req(h, "POST", "/oauth/token", form.Encode(), func(r *http.Request) {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if basicID != "" {
			r.SetBasicAuth(url.QueryEscape(basicID), url.QueryEscape(basicSecret))
		}
	})
}

type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
	Error        string `json:"error"`
}

func (f *oauthFixture) exchange(code, verifier string) (*httptest.ResponseRecorder, tokenResponse) {
	f.t.Helper()
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedirect}}
	if verifier != "" {
		form.Set("code_verifier", verifier)
	}
	w := tokenReq(f.h, form, f.clientID, f.secret)
	var tr tokenResponse
	json.Unmarshal(w.Body.Bytes(), &tr)
	return w, tr
}

// verifyIDToken은 JWKS 공개 키로 ID token 서명을 검사하고 claim을 돌려준다.
func (f *oauthFixture) verifyIDToken(tok string) map[string]any {
	f.t.Helper()
	jwks := decodeBody[struct {
		Keys []map[string]string `json:"keys"`
	}](f.t, req(f.h, "GET", "/oauth/jwks", "", nil))
	if len(jwks.Keys) != 1 || jwks.Keys[0]["kty"] != "OKP" || jwks.Keys[0]["crv"] != "Ed25519" {
		f.t.Fatalf("jwks %+v", jwks)
	}
	pub, _ := base64.RawURLEncoding.DecodeString(jwks.Keys[0]["x"])
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		f.t.Fatalf("id token %q", tok)
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if !ed25519.Verify(ed25519.PublicKey(pub), []byte(parts[0]+"."+parts[1]), sig) {
		f.t.Fatal("ID token signature does not verify against JWKS")
	}
	var header, claims map[string]any
	hb, _ := base64.RawURLEncoding.DecodeString(parts[0])
	cb, _ := base64.RawURLEncoding.DecodeString(parts[1])
	json.Unmarshal(hb, &header)
	json.Unmarshal(cb, &claims)
	if header["alg"] != "EdDSA" || header["kid"] != jwks.Keys[0]["kid"] {
		f.t.Fatalf("id token header %v", header)
	}
	return claims
}

func TestOAuthDiscovery(t *testing.T) {
	f := newOAuthFixture(t)
	for _, path := range []string{"/.well-known/openid-configuration", "/oauth/.well-known/openid-configuration"} {
		w := req(f.h, "GET", path, "", nil)
		d := decodeBody[map[string]any](t, w)
		if w.Code != 200 || d["issuer"] != oauthTestIssuer || d["authorization_endpoint"] != oauthTestIssuer+"/auth" ||
			d["token_endpoint"] != oauthTestIssuer+"/token" || d["jwks_uri"] != oauthTestIssuer+"/jwks" || d["userinfo_endpoint"] != oauthTestIssuer+"/me" {
			t.Fatalf("%s: %d %v", path, w.Code, d)
		}
		if w.Header().Get("Access-Control-Allow-Origin") != "*" {
			t.Error("discovery should allow CORS")
		}
	}
	// 서명 키는 session.key에서 유도되어 재시작해도 같다.
	k1, kid1 := f.s.oauthSigningKey()
	k2, kid2 := (&Server{SessionKey: f.s.SessionKey}).oauthSigningKey()
	if !k1.Equal(k2) || kid1 != kid2 || kid1 == "" {
		t.Error("signing key is not deterministic")
	}
	if _, kid := (&Server{SessionKey: []byte("short")}).oauthSigningKey(); kid != "" {
		t.Error("short session key must not produce a signing key")
	}
}

func TestOAuthAuthorizationCodeFlow(t *testing.T) {
	f := newOAuthFixture(t)
	scope := "openid email profile offline_access project:viewer"
	back := f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, scope, merge(pkce(), url.Values{"nonce": {"n-0S6"}, "prompt": {"consent"}})),
		map[string]any{"decision": "approve", "scopes": strings.Fields(scope), "project_ids": []string{f.p1.ID}})
	if got := back.Scheme + "://" + back.Host + back.Path; got != oauthTestRedirect {
		t.Fatalf("redirected to %s", back)
	}
	q := back.Query()
	if q.Get("state") != "st4te" || q.Get("iss") != oauthTestIssuer || !strings.HasPrefix(q.Get("code"), oauthCodePrefix) {
		t.Fatalf("authorization response %v", q)
	}
	code := q.Get("code")

	w, tr := f.exchange(code, oauthTestVerifier)
	if w.Code != 200 || tr.TokenType != "Bearer" || !strings.HasPrefix(tr.AccessToken, oauthAccessTokenPrefix) || !strings.HasPrefix(tr.RefreshToken, oauthRefreshTokenPrefix) {
		t.Fatalf("token: %d %s", w.Code, w.Body)
	}
	if w.Header().Get("Cache-Control") != "no-store" || tr.ExpiresIn < 3500 || tr.ExpiresIn > 3600 || tr.Scope != "email offline_access openid profile project:viewer" {
		t.Fatalf("token response headers/fields: %s %+v", w.Header().Get("Cache-Control"), tr)
	}
	claims := f.verifyIDToken(tr.IDToken)
	if claims["iss"] != oauthTestIssuer || claims["aud"] != f.clientID || claims["sub"] != f.member.ID || claims["nonce"] != "n-0S6" || claims["email"] != f.member.Email {
		t.Fatalf("id token claims %v", claims)
	}
	if exp, _ := claims["exp"].(float64); int64(exp) <= time.Now().Unix() {
		t.Fatal("id token already expired")
	}

	// 토큰과 비밀은 해시만 저장한다.
	atID, atSecret, _ := parseOAuthOpaque(tr.AccessToken, oauthAccessTokenPrefix)
	if at, _, err := f.st.GetOAuthAccessToken(context.Background(), atID); err != nil || at.TokenHash != hashSecret(atSecret) || strings.Contains(at.TokenHash, atSecret) {
		t.Fatalf("access token storage: %+v %v", at, err)
	}
	secID, secSecret, _ := parseOAuthOpaque(f.secret, oauthClientSecretPrefix)
	if sec, err := f.st.GetOAuthClientSecret(context.Background(), secID); err != nil || sec.SecretHash != hashSecret(secSecret) {
		t.Fatalf("client secret storage: %+v %v", sec, err)
	}
	codeID, codeSecret, _ := parseOAuthOpaque(code, oauthCodePrefix)
	if c, err := f.st.GetOAuthAuthorizationCode(context.Background(), codeID); err != nil || c.CodeHash != hashSecret(codeSecret) || c.UsedAt == nil {
		t.Fatalf("code storage: %+v %v", c, err)
	}

	// userinfo: 승인한 scope의 claim만.
	me := decodeBody[map[string]any](t, req(f.h, "GET", "/oauth/me", "", withBearer(tr.AccessToken)))
	if me["sub"] != f.member.ID || me["email"] != f.member.Email || me["name"] != f.member.Display() {
		t.Fatalf("userinfo %v", me)
	}
	if w := req(f.h, "GET", "/oauth/me", "", withBearer("keel_at_bogus.AAAA")); w.Code != 401 || !strings.Contains(w.Header().Get("WWW-Authenticate"), "invalid_token") {
		t.Fatalf("userinfo with bad token: %d", w.Code)
	}

	// Aven API: 선택한 프로젝트만 보이고 viewer 권한으로 제한된다(사용자는 member).
	projects := decodeBody[[]store.Project](t, req(f.h, "GET", "/api/projects", "", withBearer(tr.AccessToken)))
	if len(projects) != 1 || projects[0].ID != f.p1.ID {
		t.Fatalf("projects visible to token: %+v", projects)
	}
	if w := req(f.h, "GET", "/api/projects/"+f.p2.ID, "", withBearer(tr.AccessToken)); w.Code != 404 {
		t.Fatalf("unshared project: %d", w.Code)
	}
	if w := req(f.h, "PATCH", "/api/projects/"+f.p1.ID, `{"name":"x"}`, withBearer(tr.AccessToken)); w.Code != 403 {
		t.Fatalf("write with project:viewer: %d", w.Code)
	}
	if w := req(f.h, "GET", "/api/projects/"+f.p1.ID, "", withBearer(tr.AccessToken)); w.Code != 200 {
		t.Fatalf("shared project: %d %s", w.Code, w.Body)
	}
	// 계정 관리(토큰 발급 등)와 작업 공간 권한은 없다.
	for _, rt := range [][2]string{{"POST", "/api/tokens"}, {"GET", "/api/oauth/authorized-apps"}, {"GET", "/api/users"}, {"GET", "/api/templates"}, {"GET", "/api/notifications"}} {
		if w := req(f.h, rt[0], rt[1], `{"name":"x"}`, withBearer(tr.AccessToken)); w.Code != 403 {
			t.Errorf("%s %s with OAuth token: %d", rt[0], rt[1], w.Code)
		}
	}
	if me := decodeBody[map[string]any](t, req(f.h, "GET", "/api/auth/me", "", withBearer(tr.AccessToken))); me["auth"] != "oauth" || me["role"] != nil {
		t.Fatalf("/api/auth/me leaks account data to OAuth: %v", me)
	}

	// 승인된 앱 목록에 보인다.
	apps := decodeBody[[]store.OAuthAuthorizedApp](t, req(f.h, "GET", "/api/oauth/authorized-apps", "", withCookie(f.memberC)))
	if len(apps) != 1 || apps[0].ClientID != f.clientID || len(apps[0].ProjectIDs) != 1 || apps[0].LastUsedAt == nil {
		t.Fatalf("authorized apps %+v", apps)
	}

	// 코드 재사용: 거부하고 그 코드로 받은 토큰을 모두 폐기한다.
	if w, _ := f.exchange(code, oauthTestVerifier); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("code replay: %d %s", w.Code, w.Body)
	}
	if w := req(f.h, "GET", "/oauth/me", "", withBearer(tr.AccessToken)); w.Code != 401 {
		t.Fatalf("access token still valid after code replay: %d", w.Code)
	}
	if w := tokenReq(f.h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tr.RefreshToken}}, f.clientID, f.secret); w.Code != 400 {
		t.Fatalf("refresh token still valid after code replay: %d", w.Code)
	}
}

func TestOAuthRefreshRotation(t *testing.T) {
	f := newOAuthFixture(t)
	scope := "openid offline_access workspace:viewer"
	back := f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, scope, merge(pkce(), url.Values{"prompt": {"consent"}})),
		map[string]any{"decision": "approve", "scopes": strings.Fields(scope)})
	_, tr := f.exchange(back.Query().Get("code"), oauthTestVerifier)
	if tr.RefreshToken == "" {
		t.Fatalf("no refresh token: %+v", tr)
	}
	// workspace:viewer: 멤버인 모든 프로젝트를 읽을 수 있다.
	if projects := decodeBody[[]store.Project](t, req(f.h, "GET", "/api/projects", "", withBearer(tr.AccessToken))); len(projects) != 2 {
		t.Fatalf("workspace scope projects: %d", len(projects))
	}
	refresh := func(rt string, extra url.Values) (*httptest.ResponseRecorder, tokenResponse) {
		form := merge(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {rt}}, extra)
		// client_secret_post도 지원한다.
		form.Set("client_id", f.clientID)
		form.Set("client_secret", f.secret)
		w := tokenReq(f.h, form, "", "")
		var out tokenResponse
		json.Unmarshal(w.Body.Bytes(), &out)
		return w, out
	}
	// 승인보다 넓은 scope 요청은 거부하고, 이때 refresh token은 소모되지 않는다.
	if w, _ := refresh(tr.RefreshToken, url.Values{"scope": {"openid workspace:admin"}}); w.Code != 400 {
		t.Fatalf("scope escalation on refresh: %d", w.Code)
	}
	w, tr2 := refresh(tr.RefreshToken, nil)
	if w.Code != 200 || tr2.RefreshToken == "" || tr2.RefreshToken == tr.RefreshToken || tr2.AccessToken == tr.AccessToken {
		t.Fatalf("refresh: %d %s", w.Code, w.Body)
	}
	f.verifyIDToken(tr2.IDToken)
	if w, _ := refresh(tr.RefreshToken, nil); w.Code != 400 {
		t.Fatalf("rotated refresh token reused: %d", w.Code)
	}
	if w := req(f.h, "GET", "/oauth/me", "", withBearer(tr2.AccessToken)); w.Code != 200 {
		t.Fatalf("new access token: %d", w.Code)
	}
	// 다른 앱의 자격으로는 refresh할 수 없다.
	if w := tokenReq(f.h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tr2.RefreshToken}, "client_id": {f.publicID}}, "", ""); w.Code != 400 {
		t.Fatalf("refresh by another client: %d", w.Code)
	}
	// 비활성화한 사용자의 토큰은 쓸 수 없다.
	f.member.Disabled = true
	if err := f.st.UpdateUser(context.Background(), f.member); err != nil {
		t.Fatal(err)
	}
	if w := req(f.h, "GET", "/oauth/me", "", withBearer(tr2.AccessToken)); w.Code != 401 {
		t.Fatalf("disabled user's token: %d", w.Code)
	}
}

func TestOAuthAuthorizeValidation(t *testing.T) {
	f := newOAuthFixture(t)
	good := authorizeURL(f.clientID, oauthTestRedirect, "openid", pkce())
	// client_id나 redirect_uri가 틀리면 앱으로 redirect하지 않는다(open redirect 방지).
	for _, bad := range []string{
		authorizeURL("keel_client_nope", oauthTestRedirect, "openid", pkce()),
		authorizeURL(f.clientID, oauthTestRedirect+"/", "openid", pkce()),
		authorizeURL(f.clientID, "https://APP.example.com/callback", "openid", pkce()),
		authorizeURL(f.clientID, oauthTestRedirect+"?x=1", "openid", pkce()),
		authorizeURL(f.clientID, "https://evil.example.com/callback", "openid", pkce()),
		"/oauth/auth?response_type=code&client_id=" + f.clientID,
		good + "&redirect_uri=" + url.QueryEscape("https://evil.example.com/cb"),
	} {
		w := req(f.h, "GET", bad, "", withCookie(f.memberC))
		if w.Code != 400 || w.Header().Get("Location") != "" {
			t.Errorf("%s: %d location=%q", bad, w.Code, w.Header().Get("Location"))
		}
	}
	// 그 밖의 오류는 등록된 redirect URI로 error를 돌려준다.
	errorOf := func(u string, cookie string) url.Values {
		t.Helper()
		w := req(f.h, "GET", u, "", withCookie(cookie))
		loc, err := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusSeeOther || err != nil || !strings.HasPrefix(loc.String(), oauthTestRedirect+"?") {
			t.Fatalf("%s: %d %q", u, w.Code, w.Header().Get("Location"))
		}
		return loc.Query()
	}
	for u, want := range map[string]string{
		strings.Replace(good, "response_type=code", "response_type=token", 1):                                                                    "unsupported_response_type",
		authorizeURL(f.clientID, oauthTestRedirect, "email", pkce()):                                                                             "invalid_scope",
		authorizeURL(f.clientID, oauthTestRedirect, "openid admin", pkce()):                                                                      "invalid_scope",
		authorizeURL(f.clientID, oauthTestRedirect, "openid", url.Values{"code_challenge_method": {"S256"}}):                                     "invalid_request",
		authorizeURL(f.clientID, oauthTestRedirect, "openid", url.Values{"code_challenge": {pkceS256("x")}, "code_challenge_method": {"plain"}}): "invalid_request",
		authorizeURL(f.clientID, oauthTestRedirect, "openid", url.Values{"code_challenge": {"short"}, "code_challenge_method": {"S256"}}):        "invalid_request",
		authorizeURL(f.clientID, oauthTestRedirect, "openid", merge(pkce(), url.Values{"prompt": {"login"}})):                                    "invalid_request",
		good + "&scope=openid":                  "invalid_request", // 반복된 파라미터
		good + "&request_uri=urn%3Aexample%3Ax": "request_uri_not_supported",
		authorizeURL(f.clientID, oauthTestRedirect, "openid", merge(pkce(), url.Values{"prompt": {"none"}})): "consent_required",
	} {
		q := errorOf(u, f.memberC)
		if q.Get("error") != want || q.Get("iss") != oauthTestIssuer {
			t.Errorf("%s: got %v, want %s", u, q, want)
		}
		if q.Get("state") != "st4te" && !strings.Contains(u, "&scope=openid") {
			t.Errorf("%s: state not echoed: %v", u, q)
		}
	}
	// public 앱은 PKCE 필수.
	w0 := req(f.h, "GET", authorizeURL(f.publicID, "http://127.0.0.1:8765/cb", "openid", nil), "", withCookie(f.memberC))
	if loc, _ := url.Parse(w0.Header().Get("Location")); loc == nil || loc.Host != "127.0.0.1:8765" || loc.Query().Get("error") != "invalid_request" {
		t.Fatalf("public client without PKCE: %d %s", w0.Code, w0.Header().Get("Location"))
	}
	// 로그인하지 않았으면 로그인 화면으로(prompt=none이면 login_required).
	w := req(f.h, "GET", good, "", nil)
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login/?next=") {
		t.Fatalf("not logged in: %d %s", w.Code, loc)
	} else if next, _ := url.Parse(loc); next.Query().Get("next") != good {
		t.Fatalf("login next = %q", next.Query().Get("next"))
	}
	if q := errorOf(authorizeURL(f.clientID, oauthTestRedirect, "openid", merge(pkce(), url.Values{"prompt": {"none"}})), ""); q.Get("error") != "login_required" {
		t.Fatalf("prompt=none logged out: %v", q)
	}
	// 다른 사이트에서 넘어온 이동은 SameSite=Strict 쿠키가 없으므로 같은 origin에서 한 번 더 이동한다.
	w = req(f.h, "GET", good, "", func(r *http.Request) { r.Header.Set("Sec-Fetch-Site", "cross-site") })
	if w.Code != 200 || !strings.Contains(w.Body.String(), `http-equiv="refresh"`) || strings.Contains(w.Body.String(), "<script") {
		t.Fatalf("cross-site bounce: %d %s", w.Code, w.Body)
	}
	// offline_access는 prompt=consent 없이는 무시된다.
	w = req(f.h, "GET", authorizeURL(f.clientID, oauthTestRedirect, "openid offline_access", pkce()), "", withCookie(f.memberC))
	loc, _ := url.Parse(w.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	if loc.RawQuery != "" || frag.Get("request") == "" {
		t.Fatalf("consent redirect must carry the request in the fragment: %s", loc)
	}
	cr := decodeBody[struct {
		Scopes []struct{ Name string } `json:"scopes"`
	}](t, req(f.h, "GET", "/api/oauth/requests/"+frag.Get("request"), "", withCookie(f.memberC)))
	if len(cr.Scopes) != 1 || cr.Scopes[0].Name != "openid" {
		t.Fatalf("offline_access without prompt=consent: %+v", cr.Scopes)
	}
	// 동의 요청은 만든 사용자만 볼 수 있다.
	if w := req(f.h, "GET", "/api/oauth/requests/"+frag.Get("request"), "", withCookie(f.adminC)); w.Code != 404 {
		t.Fatalf("someone else's consent request: %d", w.Code)
	}
}

func TestOAuthConsentDecisions(t *testing.T) {
	f := newOAuthFixture(t)
	authorize := authorizeURL(f.clientID, oauthTestRedirect, "openid email project:member", pkce())
	// 거부하면 access_denied.
	back := f.consent(f.memberC, authorize, map[string]any{"decision": "deny"})
	if back.Query().Get("error") != "access_denied" || back.Query().Get("state") != "st4te" || back.Query().Get("code") != "" {
		t.Fatalf("deny: %v", back.Query())
	}
	start := func() string {
		w := req(f.h, "GET", authorize, "", withCookie(f.memberC))
		loc, _ := url.Parse(w.Header().Get("Location"))
		frag, _ := url.ParseQuery(loc.Fragment)
		return frag.Get("request")
	}
	post := func(id string, body string) int {
		return req(f.h, "POST", "/api/oauth/requests/"+id, body, withCookie(f.memberC)).Code
	}
	id := start()
	for _, bad := range []string{
		`{"decision":"approve","scopes":["email"]}`,                                              // openid 필수
		`{"decision":"approve","scopes":["openid","workspace:admin"]}`,                           // 요청하지 않은 scope
		`{"decision":"approve","scopes":["openid","project:member"],"project_ids":[]}`,           // 프로젝트 필수
		`{"decision":"approve","scopes":["openid","project:member"],"project_ids":["prj_nope"]}`, // 볼 수 없는 프로젝트
		`{"decision":"approve","scopes":["openid"],"project_ids":["` + f.p1.ID + `"]}`,           // project scope 없이 프로젝트
		`{"decision":"maybe"}`,
	} {
		if code := post(id, bad); code != 400 {
			t.Errorf("%s: %d", bad, code)
		}
	}
	// API 토큰으로는 동의할 수 없다.
	tok := &store.APIToken{UserID: f.member.ID, Name: "t", Prefix: "keel_m", Scope: store.ScopeWorkspace, Role: store.RoleMember}
	if err := f.st.CreateAPIToken(context.Background(), tok, hashSecret("keel_membertoken")); err != nil {
		t.Fatal(err)
	}
	if w := req(f.h, "POST", "/api/oauth/requests/"+id, `{"decision":"approve","scopes":["openid"]}`, withBearer("keel_membertoken")); w.Code != 403 {
		t.Fatalf("consent with API token: %d", w.Code)
	}
	// 일부 scope만 승인할 수 있다.
	if code := post(id, `{"decision":"approve","scopes":["openid","project:member"],"project_ids":["`+f.p2.ID+`"]}`); code != 200 {
		t.Fatalf("approve: %d", code)
	}
	if code := post(id, `{"decision":"approve","scopes":["openid"]}`); code != 404 {
		t.Fatalf("request processed twice: %d", code)
	}
	// 승인된 앱에서 공유한 프로젝트를 바꿀 수 있다(코드를 교환해 승인을 만든 뒤).
	_ = id
	back = f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, "openid project:member", merge(pkce(), url.Values{"prompt": {"consent"}})),
		map[string]any{"decision": "approve", "scopes": []string{"openid", "project:member"}, "project_ids": []string{f.p2.ID}})
	_, tr := f.exchange(back.Query().Get("code"), oauthTestVerifier)
	if w := req(f.h, "POST", "/api/environments/"+f.env2.ID+"/groups", `{"name":"oauth"}`, withBearer(tr.AccessToken)); w.Code != 201 {
		t.Fatalf("project:member write: %d %s", w.Code, w.Body)
	}
	if w := req(f.h, "PATCH", "/api/projects/"+f.p2.ID, `{"description":"x"}`, withBearer(tr.AccessToken)); w.Code != 403 {
		t.Fatalf("project admin action with project:member: %d", w.Code)
	}
	apps := decodeBody[[]store.OAuthAuthorizedApp](t, req(f.h, "GET", "/api/oauth/authorized-apps", "", withCookie(f.memberC)))
	if len(apps) != 1 {
		t.Fatalf("authorized apps: %+v", apps)
	}
	if w := req(f.h, "PATCH", "/api/oauth/authorized-apps/"+apps[0].GrantID, `{"project_ids":["`+f.p1.ID+`"]}`, withCookie(f.adminC)); w.Code != 404 {
		t.Fatalf("someone else's grant: %d", w.Code)
	}
	if w := req(f.h, "PATCH", "/api/oauth/authorized-apps/"+apps[0].GrantID, `{"project_ids":["`+f.p1.ID+`"]}`, withCookie(f.memberC)); w.Code != 204 {
		t.Fatalf("change shared projects: %d %s", w.Code, w.Body)
	}
	if w := req(f.h, "GET", "/api/projects/"+f.p2.ID, "", withBearer(tr.AccessToken)); w.Code != 404 {
		t.Fatalf("unshared project still visible: %d", w.Code)
	}
	if w := req(f.h, "DELETE", "/api/oauth/authorized-apps/"+apps[0].GrantID, "", withCookie(f.memberC)); w.Code != 204 {
		t.Fatalf("revoke: %d", w.Code)
	}
	if w := req(f.h, "GET", "/api/projects/"+f.p1.ID, "", withBearer(tr.AccessToken)); w.Code != 401 {
		t.Fatalf("token after revoking app: %d", w.Code)
	}
}

func TestOAuthAutoConsent(t *testing.T) {
	f := newOAuthFixture(t)
	back := f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, "openid email", pkce()),
		map[string]any{"decision": "approve", "scopes": []string{"openid", "email"}})
	if w, _ := f.exchange(back.Query().Get("code"), oauthTestVerifier); w.Code != 200 {
		t.Fatalf("exchange: %d", w.Code)
	}
	// 같거나 좁은 scope는 동의 화면 없이 바로 코드를 받는다(prompt=none도).
	for _, extra := range []url.Values{pkce(), merge(pkce(), url.Values{"prompt": {"none"}})} {
		w := req(f.h, "GET", authorizeURL(f.clientID, oauthTestRedirect, "openid", extra), "", withCookie(f.memberC))
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusSeeOther || !strings.HasPrefix(loc.String(), oauthTestRedirect) || loc.Query().Get("code") == "" {
			t.Fatalf("auto consent: %d %s", w.Code, loc)
		}
		w2, tr := f.exchange(loc.Query().Get("code"), oauthTestVerifier)
		if w2.Code != 200 || tr.RefreshToken != "" {
			t.Fatalf("auto consent exchange: %d %+v", w2.Code, tr)
		}
	}
	// 더 넓은 scope, prompt=consent는 동의 화면으로.
	for _, u := range []string{
		authorizeURL(f.clientID, oauthTestRedirect, "openid profile", pkce()),
		authorizeURL(f.clientID, oauthTestRedirect, "openid", merge(pkce(), url.Values{"prompt": {"consent"}})),
	} {
		if loc := req(f.h, "GET", u, "", withCookie(f.memberC)).Header().Get("Location"); !strings.HasPrefix(loc, "/oauth/consent/") {
			t.Errorf("%s: %s", u, loc)
		}
	}
}

func TestOAuthClientAuthAndPKCE(t *testing.T) {
	f := newOAuthFixture(t)
	codeFor := func(extra url.Values) string {
		back := f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, "openid", merge(extra, url.Values{"prompt": {"consent"}})),
			map[string]any{"decision": "approve", "scopes": []string{"openid"}})
		return back.Query().Get("code")
	}
	form := func(code string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedirect}, "code_verifier": {oauthTestVerifier}}
	}
	code := codeFor(pkce())
	// 잘못된 client 인증은 401 invalid_client이고 코드는 소모되지 않는다.
	for name, w := range map[string]*httptest.ResponseRecorder{
		"wrong secret":   tokenReq(f.h, form(code), f.clientID, f.secret[:len(f.secret)-2]+"AA"),
		"no secret":      tokenReq(f.h, merge(form(code), url.Values{"client_id": {f.clientID}}), "", ""),
		"basic and post": tokenReq(f.h, merge(form(code), url.Values{"client_secret": {f.secret}}), f.clientID, f.secret),
		"other client":   tokenReq(f.h, form(code), f.publicID, ""),
		"unknown client": tokenReq(f.h, form(code), "keel_client_nope", f.secret),
	} {
		if w.Code != 401 || !strings.Contains(w.Body.String(), "invalid_client") {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// 잘못된 redirect_uri, PKCE verifier는 invalid_grant이고 코드는 버려진다.
	if w := tokenReq(f.h, merge(form(code), url.Values{"code_verifier": {strings.Repeat("a", 43)}}), f.clientID, f.secret); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_grant") {
		t.Fatalf("wrong verifier: %d %s", w.Code, w.Body)
	}
	if w := tokenReq(f.h, form(code), f.clientID, f.secret); w.Code != 400 {
		t.Fatalf("code usable after failed PKCE: %d", w.Code)
	}
	code = codeFor(pkce())
	if w := tokenReq(f.h, merge(form(code), url.Values{"redirect_uri": {"http://localhost:3000/cb"}}), f.clientID, f.secret); w.Code != 400 {
		t.Fatalf("redirect_uri mismatch: %d", w.Code)
	}
	code = codeFor(pkce())
	if w := tokenReq(f.h, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedirect}}, f.clientID, f.secret); w.Code != 400 {
		t.Fatalf("missing verifier: %d", w.Code)
	}
	// confidential 앱은 PKCE 없이도 되지만, challenge 없이 verifier를 보내면 거부한다(다운그레이드 방지).
	code = codeFor(nil)
	if w := tokenReq(f.h, form(code), f.clientID, f.secret); w.Code != 400 {
		t.Fatalf("verifier without challenge: %d", w.Code)
	}
	code = codeFor(nil)
	if w := tokenReq(f.h, url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {oauthTestRedirect}}, f.clientID, f.secret); w.Code != 200 {
		t.Fatalf("confidential without PKCE: %d %s", w.Code, w.Body)
	}
	// 요청 형식: JSON, query string, 반복 파라미터는 거부.
	if w := req(f.h, "POST", "/oauth/token", `{"grant_type":"authorization_code"}`, func(r *http.Request) { r.Header.Set("Content-Type", "application/json") }); w.Code != 400 {
		t.Errorf("json body: %d", w.Code)
	}
	if w := req(f.h, "POST", "/oauth/token?grant_type=authorization_code", "", func(r *http.Request) { r.Header.Set("Content-Type", "application/x-www-form-urlencoded") }); w.Code != 400 {
		t.Errorf("query params: %d", w.Code)
	}
	if w := tokenReq(f.h, url.Values{"grant_type": {"refresh_token", "authorization_code"}}, f.clientID, f.secret); w.Code != 400 {
		t.Errorf("repeated param: %d", w.Code)
	}
	if w := tokenReq(f.h, url.Values{"grant_type": {"password"}}, f.clientID, f.secret); !strings.Contains(w.Body.String(), "unsupported_grant_type") {
		t.Errorf("password grant: %d %s", w.Code, w.Body)
	}
	if w := req(f.h, "GET", "/oauth/token", "", nil); w.Code != 405 {
		t.Errorf("GET token endpoint: %d", w.Code)
	}

	// public 앱: PKCE만, secret을 보내면 실패.
	pubRedirect := "http://127.0.0.1:8765/cb"
	back := f.consent(f.memberC, authorizeURL(f.publicID, pubRedirect, "openid", pkce()), map[string]any{"decision": "approve", "scopes": []string{"openid"}})
	pubForm := url.Values{"grant_type": {"authorization_code"}, "code": {back.Query().Get("code")}, "redirect_uri": {pubRedirect}, "code_verifier": {oauthTestVerifier}, "client_id": {f.publicID}}
	if w := tokenReq(f.h, merge(pubForm, url.Values{"client_secret": {f.secret}}), "", ""); w.Code != 401 {
		t.Fatalf("public client with secret: %d", w.Code)
	}
	if w := tokenReq(f.h, pubForm, "", ""); w.Code != 200 {
		t.Fatalf("public client exchange: %d %s", w.Code, w.Body)
	}

	// 폐기한 client secret은 더 이상 통하지 않는다.
	apps := decodeBody[[]store.OAuthApp](t, req(f.h, "GET", "/api/oauth/apps", "", withCookie(f.adminC)))
	var appID string
	for _, a := range apps {
		if a.ClientID == f.clientID {
			appID = a.ID
		}
	}
	secrets := decodeBody[[]store.OAuthClientSecret](t, req(f.h, "GET", "/api/oauth/apps/"+appID+"/secrets", "", withCookie(f.adminC)))
	if w := req(f.h, "DELETE", "/api/oauth/apps/"+appID+"/secrets/"+secrets[0].ID, "", withCookie(f.adminC)); w.Code != 204 {
		t.Fatalf("revoke secret: %d", w.Code)
	}
	code = codeFor(pkce())
	if w := tokenReq(f.h, form(code), f.clientID, f.secret); w.Code != 401 {
		t.Fatalf("revoked secret: %d", w.Code)
	}
}

func TestOAuthRevokeEndpointAndAppDeletion(t *testing.T) {
	f := newOAuthFixture(t)
	back := f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, "openid offline_access", merge(pkce(), url.Values{"prompt": {"consent"}})),
		map[string]any{"decision": "approve", "scopes": []string{"openid", "offline_access"}})
	_, tr := f.exchange(back.Query().Get("code"), oauthTestVerifier)
	revoke := func(token, id, secret string) int {
		return req(f.h, "POST", "/oauth/revoke", url.Values{"token": {token}}.Encode(), func(r *http.Request) {
			r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			r.SetBasicAuth(id, secret)
		}).Code
	}
	// 모르는 토큰도 200(RFC 7009), 다른 앱의 토큰은 건드리지 않는다.
	if code := revoke("keel_rt_unknown.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", f.clientID, f.secret); code != 200 {
		t.Fatalf("unknown token: %d", code)
	}
	if code := revoke(tr.RefreshToken, f.clientID, "wrong"); code != 401 {
		t.Fatalf("revoke with bad client auth: %d", code)
	}
	if code := revoke(tr.RefreshToken, f.clientID, f.secret); code != 200 {
		t.Fatalf("revoke: %d", code)
	}
	if w := req(f.h, "GET", "/oauth/me", "", withBearer(tr.AccessToken)); w.Code != 401 {
		t.Fatalf("access token survives refresh token revocation: %d", w.Code)
	}
	if w := tokenReq(f.h, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tr.RefreshToken}}, f.clientID, f.secret); w.Code != 400 {
		t.Fatalf("revoked refresh token: %d", w.Code)
	}

	// 앱을 지우면 모든 토큰이 무효가 된다. 앱 관리는 작업 공간 admin만.
	back = f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, "openid", merge(pkce(), url.Values{"prompt": {"consent"}})), map[string]any{"decision": "approve", "scopes": []string{"openid"}})
	_, tr = f.exchange(back.Query().Get("code"), oauthTestVerifier)
	apps := decodeBody[[]store.OAuthApp](t, req(f.h, "GET", "/api/oauth/apps", "", withCookie(f.adminC)))
	if w := req(f.h, "GET", "/api/oauth/apps", "", withCookie(f.memberC)); w.Code != 403 {
		t.Fatalf("member lists apps: %d", w.Code)
	}
	for _, a := range apps {
		if w := req(f.h, "DELETE", "/api/oauth/apps/"+a.ID, "", withCookie(f.memberC)); w.Code != 403 {
			t.Fatalf("member deletes app: %d", w.Code)
		}
		if w := req(f.h, "DELETE", "/api/oauth/apps/"+a.ID, "", withCookie(f.adminC)); w.Code != 204 {
			t.Fatalf("delete app: %d", w.Code)
		}
	}
	if w := req(f.h, "GET", "/oauth/me", "", withBearer(tr.AccessToken)); w.Code != 401 {
		t.Fatalf("token after app deletion: %d", w.Code)
	}
}

func TestOAuthRedirectURIValidation(t *testing.T) {
	for _, tc := range []struct {
		uri, typ string
		ok       bool
	}{
		{"https://app.example.com/cb", "confidential", true},
		{"https://app.example.com/cb?tenant=1", "confidential", true},
		{"http://localhost:3000/cb", "confidential", true},
		{"http://127.0.0.1:8080/cb", "public", true},
		{"http://[::1]:8080/cb", "public", true},
		{"com.example.app:/oauth", "public", true},
		{"com.example.app:/oauth", "confidential", false},
		{"myapp://cb", "public", false},
		{"http://app.example.com/cb", "confidential", false},
		{"https://app.example.com/cb#frag", "confidential", false},
		{"https://user:pw@app.example.com/cb", "confidential", false},
		{"https://*.example.com/cb", "confidential", false},
		{"javascript:alert(1)", "public", false},
		{"data:text/html,x", "public", false},
		{"/relative", "confidential", false},
		{"https:///nohost", "confidential", false},
		{" https://app.example.com/cb", "confidential", false},
	} {
		if err := validateOAuthRedirectURIs([]string{tc.uri}, tc.typ); (err == nil) != tc.ok {
			t.Errorf("%q (%s): err=%v, want ok=%v", tc.uri, tc.typ, err, tc.ok)
		}
	}
	if validateOAuthRedirectURIs(nil, "public") == nil || validateOAuthRedirectURIs([]string{"https://a.example/cb", "https://a.example/cb"}, "public") == nil {
		t.Error("empty or duplicate redirect URIs accepted")
	}
}

func TestOAuthScopeLevels(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	s := &Server{Store: st}
	member := testUser(t, st, "m@example.com", store.RoleMember)
	owner := testUser(t, st, "o@example.com", store.RoleOwner)
	p := &store.Project{Name: "p"}
	if _, err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMember(ctx, p.ID, member.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		u        *store.User
		scopes   []string
		projects []string
		ws, proj int
	}{
		{member, []string{"openid", "workspace:admin"}, nil, lvMember, lvMember}, // 사용자 역할이 상한
		{owner, []string{"openid", "workspace:admin"}, nil, lvAdmin, lvAdmin},    // scope가 상한(owner 작업은 불가)
		{owner, []string{"openid", "workspace:viewer"}, nil, lvViewer, lvViewer},
		{owner, []string{"openid", "project:member"}, []string{p.ID}, lvNone, lvMember},
		{owner, []string{"openid", "project:member"}, []string{"prj_other"}, lvNone, lvNone},
		{member, []string{"openid", "email", "profile"}, nil, lvNone, lvNone},
	} {
		pr := &principal{User: tc.u, Via: viaOAuth, OAuth: &store.OAuthGrant{Scopes: tc.scopes, ProjectIDs: tc.projects}}
		lv, err := s.projectLevel(ctx, pr, p.ID, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, err := s.workspaceAccess(ctx, pr, store.DefaultWorkspaceID); err != nil || got != tc.ws || lv != tc.proj {
			t.Errorf("%s %v: workspace=%d project=%d, want %d %d", tc.u.Role, tc.scopes, got, lv, tc.ws, tc.proj)
		}
	}
	if _, err := parseOAuthScopes("openid openid email"); err != nil {
		t.Error("duplicate scopes should be tolerated")
	}
	for _, bad := range []string{"", "email", "openid unknown"} {
		if _, err := parseOAuthScopes(bad); err == nil {
			t.Errorf("parseOAuthScopes(%q) accepted", bad)
		}
	}
	for _, bad := range []string{"keel_at_x", "keel_at_.AAAA", "keel_at_ID.AAAA", "keel_at_id." + strings.Repeat("A", 20), "keel_rt_id." + strings.Repeat("A", 43)} {
		if _, _, ok := parseOAuthOpaque(bad, oauthAccessTokenPrefix); ok {
			t.Errorf("parseOAuthOpaque(%q) accepted", bad)
		}
	}
	_, v, _ := newOAuthOpaque(oauthAccessTokenPrefix, "oat")
	if _, _, ok := parseOAuthOpaque(v, oauthAccessTokenPrefix); !ok {
		t.Errorf("generated token %q does not parse", v)
	}
}
