package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// KEEL_SUPERADMINS 계정은 DB 역할과 무관하게 서버 owner다. 서버 관리자가 그 계정을 바꾸거나(비밀번호, 2FA, 비활성화, 삭제)
// 토큰을 폐기하면 owner 계정을 가져가거나 막을 수 있으므로 owner만 할 수 있어야 한다.
func TestSuperAdminCannotBeManagedByInstanceAdmin(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	f.srv.SuperAdmins = map[string]bool{"boss@example.com": true}
	boss, _ := freshAccount(t, f, "boss@example.com", store.RoleMember) // DB 역할은 member
	_, adminC := freshAccount(t, f, "ops@example.com", store.RoleAdmin)
	_, ownerC := freshAccount(t, f, "root@example.com", store.RoleOwner)
	tok := &store.APIToken{UserID: boss.ID, Name: "ci", Prefix: "keel_boss", Scope: store.ScopeInstance, Role: store.RoleAdmin}
	if err := f.st.CreateAPIToken(ctx, tok, hashSecret("keel_bosstoken")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"PATCH", "/api/users/" + boss.ID, `{"password":"attacker password"}`},
		{"PATCH", "/api/users/" + boss.ID, `{"reset_2fa":true}`},
		{"PATCH", "/api/users/" + boss.ID, `{"disabled":true}`},
		{"PATCH", "/api/users/" + boss.ID, `{"name":"renamed"}`},
		{"DELETE", "/api/users/" + boss.ID, ""},
		{"DELETE", "/api/tokens/" + tok.ID, ""},
	} {
		if w := req(h, tc.method, tc.path, tc.body, adminC); w.Code != 403 {
			t.Errorf("instance admin %s %s %s: %d %s, want 403", tc.method, tc.path, tc.body, w.Code, w.Body)
		}
	}
	got, err := f.st.GetUser(ctx, boss.ID)
	if err != nil || got.Disabled || got.Name != boss.Name || got.PasswordHash != boss.PasswordHash {
		t.Fatalf("superadmin changed by admin: %+v %v", got, err)
	}
	if _, err := f.st.GetAPIToken(ctx, tok.ID); err != nil {
		t.Fatalf("superadmin token revoked by admin: %v", err)
	}
	// 서버 owner는 여전히 관리할 수 있다.
	if w := req(h, "PATCH", "/api/users/"+boss.ID, `{"name":"renamed"}`, ownerC); w.Code != 200 {
		t.Errorf("owner manages superadmin: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", "/api/tokens/"+tok.ID, "", ownerC); w.Code != 204 {
		t.Errorf("owner revokes superadmin token: %d %s", w.Code, w.Body)
	}
	// 서버 관리자는 일반 사용자의 토큰은 계속 폐기할 수 있다.
	plain, _ := freshAccount(t, f, "plain@example.com", store.RoleMember)
	ptok := &store.APIToken{UserID: plain.ID, Name: "p", Prefix: "keel_plain", Scope: store.ScopeWorkspace, Role: store.RoleMember}
	if err := f.st.CreateAPIToken(ctx, ptok, hashSecret("keel_plaintoken")); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "DELETE", "/api/tokens/"+ptok.ID, "", adminC); w.Code != 204 {
		t.Errorf("admin revokes member token: %d %s", w.Code, w.Body)
	}
}

// 초대 링크는 만든 사람에게 돌아가므로, KEEL_SUPERADMINS 이메일로 초대를 만들 수 있으면 누구든 그 링크로 가입해 서버 owner가 된다.
func TestSuperAdminEmailInviteRequiresOwner(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	f.srv.SuperAdmins = map[string]bool{"boss@example.com": true}
	// 프로젝트 admin(서버 관리자 아님)과 서버 관리자, 자기 팀 작업 공간 owner
	padmin, padminC := freshAccount(t, f, "lead@example.com", store.RoleMember)
	if err := f.st.SetWorkspaceMember(ctx, store.DefaultWorkspaceID, padmin.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(ctx, f.p.ID, padmin.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	_, adminC := freshAccount(t, f, "ops@example.com", store.RoleAdmin)
	tenant, tenantC := freshAccount(t, f, "tenant@example.com", store.RoleMember)
	ws, err := f.st.CreateWorkspace(ctx, "Tenant", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path, body string
		auth             func(*http.Request)
	}{
		{"project admin", "/api/invites", fmt.Sprintf(`{"email":"BOSS@example.com","project_id":%q,"project_role":"viewer"}`, f.p.ID), padminC},
		{"instance admin", "/api/invites", `{"email":"boss@example.com","role":"member"}`, adminC},
		{"workspace owner", "/api/workspaces/" + ws.ID + "/invites", `{"email":"boss@example.com","role":"member"}`, tenantC},
	} {
		if w := req(h, "POST", tc.path, tc.body, tc.auth); w.Code != 403 {
			t.Errorf("%s invites a superadmin email: %d %s, want 403", tc.name, w.Code, w.Body)
		}
	}
	ins, err := f.st.ListInvites(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range ins {
		if in.Email == "boss@example.com" {
			t.Fatalf("superadmin invite was created: %+v", in)
		}
	}
	// 다른 이메일은 그대로 초대할 수 있고, 서버 owner는 superadmin 이메일도 초대할 수 있다.
	if w := req(h, "POST", "/api/invites", `{"email":"someone@example.com","role":"member"}`, adminC); w.Code != 201 {
		t.Errorf("admin invites a normal email: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", "/api/invites", `{"email":"boss@example.com","role":"member"}`, withBearer("legacy-token")); w.Code != 201 {
		t.Errorf("owner invites superadmin email: %d %s", w.Code, w.Body)
	}
}

func sessionIDOf(t *testing.T, st *store.Store, cookie string) string {
	t.Helper()
	se, err := st.GetSessionByHash(context.Background(), hashSecret(cookie))
	if err != nil {
		t.Fatal(err)
	}
	return se.ID
}

// "사용자로 보기"는 읽기 전용이다. OAuth 인가 엔드포인트는 requireAuth 밖이라 따로 막지 않으면, 대상 사용자가 예전에 승인한 앱에
// 자동 동의로 코드가 나가 서버 owner가 그 사람으로 외부 앱에 로그인할 수 있었다.
func TestOAuthAuthorizeRejectsViewAsSession(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	back := f.consent(f.memberC, authorizeURL(f.clientID, oauthTestRedirect, "openid email", pkce()),
		map[string]any{"decision": "approve", "scopes": []string{"openid", "email"}})
	if w, _ := f.exchange(back.Query().Get("code"), oauthTestVerifier); w.Code != 200 {
		t.Fatalf("exchange: %d", w.Code)
	}
	boss := testUser(t, f.st, "boss@example.com", store.RoleOwner)
	bossC := testSessionCookie(t, f.s, boss)
	if err := f.st.SetSessionViewAs(ctx, sessionIDOf(t, f.st, bossC), f.member.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if me := decodeBody[map[string]any](t, req(f.h, "GET", "/api/auth/me", "", withCookie(bossC))); me["id"] != f.member.ID {
		t.Fatalf("view-as not active: %v", me["id"])
	}
	for _, extra := range []url.Values{pkce(), merge(pkce(), url.Values{"prompt": {"none"}}), merge(pkce(), url.Values{"prompt": {"consent"}})} {
		w := req(f.h, "GET", authorizeURL(f.clientID, oauthTestRedirect, "openid", extra), "", withCookie(bossC))
		loc, _ := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusSeeOther || loc == nil || !strings.HasPrefix(loc.String(), oauthTestRedirect) ||
			loc.Query().Get("code") != "" || loc.Query().Get("error") != "access_denied" {
			t.Errorf("authorize while viewing as %v: %d %s", extra, w.Code, w.Header().Get("Location"))
		}
	}
}

// OAuth 인가는 requireAuth의 세션 조건(SSO 적용, 2단계 인증 강제)을 똑같이 지켜야 한다.
func TestOAuthAuthorizeHonorsSessionPolicies(t *testing.T) {
	f := newOAuthFixture(t)
	ctx := context.Background()
	u := authorizeURL(f.clientID, oauthTestRedirect, "openid", pkce())
	if loc := req(f.h, "GET", u, "", withCookie(f.memberC)).Header().Get("Location"); !strings.HasPrefix(loc, "/oauth/consent/") {
		t.Fatalf("baseline: %s", loc)
	}
	// 2단계 인증을 강제했는데 설정하지 않은 세션은 앱을 승인할 수 없다.
	if err := f.st.SetWorkspace2FAEnforced(ctx, true); err != nil {
		t.Fatal(err)
	}
	w := req(f.h, "GET", u, "", withCookie(f.memberC))
	if loc, _ := url.Parse(w.Header().Get("Location")); loc == nil || loc.Query().Get("error") != "access_denied" || loc.Query().Get("code") != "" {
		t.Errorf("authorize without required 2FA: %d %s", w.Code, w.Header().Get("Location"))
	}
	if err := f.st.SetWorkspace2FAEnforced(ctx, false); err != nil {
		t.Fatal(err)
	}
	// SSO가 적용된 도메인 사용자의 비밀번호 세션은 없는 것으로 보고 다시 로그인시킨다.
	cfg, err := f.st.GetSAMLConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg.EnforceSSO, cfg.Domains = true, []store.VerifiedDomain{{Domain: "example.com", Token: "t", Verified: true}}
	if err := f.st.SaveSAMLConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if req(f.h, "GET", "/api/auth/me", "", withCookie(f.memberC)).Code != 401 {
		t.Fatal("SSO enforcement baseline")
	}
	w = req(f.h, "GET", u, "", withCookie(f.memberC))
	if loc := w.Header().Get("Location"); w.Code != http.StatusSeeOther || !strings.HasPrefix(loc, "/login/") {
		t.Errorf("authorize with a non-SAML session under SSO enforcement: %d %s", w.Code, loc)
	}
}

// GitHub 연결 시작·콜백은 "사용자로 보기" 세션으로는 쓸 수 없다(owner의 GitHub 토큰이 대상 사용자 계정에 저장된다).
func TestGitHubConnectRejectsViewAsSession(t *testing.T) {
	f := newGHOAuthFixture(t)
	ctx := context.Background()
	bossC := testSessionCookie(t, f.s, f.ownerU)
	if err := f.s.Store.SetSessionViewAs(ctx, sessionIDOf(t, f.s.Store, bossC), f.otherU.ID, time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if w := req(f.h, "GET", "/api/github/oauth/start", "", withCookie(bossC)); w.Code != 403 {
		t.Errorf("start while viewing as: %d %s", w.Code, w.Body)
	}
	// 대상 사용자가 직접 시작한 state를 owner의 보기 세션으로 끝내도 연결되지 않는다.
	w := req(f.h, "GET", "/api/github/oauth/start", "", f.other)
	loc, _ := url.Parse(w.Header().Get("Location"))
	state := loc.Query().Get("state")
	w = req(f.h, "GET", "/api/github/oauth/callback?code=good-code&state="+state, "", withCookie(bossC))
	if w.Code != 302 || !strings.Contains(w.Header().Get("Location"), "github=error") {
		t.Errorf("callback while viewing as: %d %s", w.Code, w.Header().Get("Location"))
	}
	if got := f.s.Deploy.UserGitHubToken(ctx, f.otherU.ID); got != "" {
		t.Fatalf("view-as session connected GitHub for the target user: %q", got)
	}
}

// 브라우저는 URL에서 탭·줄바꿈을 지우고 "\"를 "/"로 읽는다. "/\t/evil.com"을 그대로 돌려보내면 "//evil.com"(다른 사이트)이 된다.
func TestSafeLocalRedirectRejectsBrowserNormalizedOffsiteURLs(t *testing.T) {
	for _, value := range []string{
		"", "evil.com", "https://evil.com", "//evil.com", "/\\evil.com", "\\/evil.com",
		"/\t/evil.com", "/\n/evil.com", "/\r/evil.com", "\t//evil.com", "/\x00/evil.com", "/\x1f/evil.com", "/\x7f/evil.com",
		"/ok\r\nLocation: https://evil.com", "/" + strings.Repeat("a", 2048),
	} {
		if got := safeLocalRedirect(value); got != "/" {
			t.Errorf("safeLocalRedirect(%q) = %q, want /", value, got)
		}
		if got := safeSAMLRedirect(value); got != "/" {
			t.Errorf("safeSAMLRedirect(%q) = %q, want /", value, got)
		}
	}
	for _, value := range []string{"/", "/settings/?section=sso", "/oauth/auth?client_id=a&redirect_uri=https%3A%2F%2Fapp", "/%2F%2Fevil.com", "/a//b"} {
		if got := safeLocalRedirect(value); got != value {
			t.Errorf("safeLocalRedirect(%q) = %q, want unchanged", value, got)
		}
	}
}

// /api/auth/saml/login?next=/%09/evil.com이 로그인 뒤 다른 사이트로 보내지 않아야 한다(저장된 목적지와 착지 페이지 모두).
func TestSAMLLoginNextCannotRedirectOffsite(t *testing.T) {
	f := newSAMLTestFixture(t)
	ctx := context.Background()
	for _, next := range []string{"/%09/evil.com", "/%0a/evil.com", "/%5Cevil.com", "%2F%2Fevil.com"} {
		w := httptest.NewRecorder()
		f.handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "https://aven.example.test/api/auth/saml/login?next="+next, nil))
		loc, err := url.Parse(w.Header().Get("Location"))
		if w.Code != http.StatusFound || err != nil {
			t.Fatalf("saml login %s: %d %s", next, w.Code, w.Header().Get("Location"))
		}
		relay := loc.Query().Get("RelayState")
		_, dest, err := f.server.Store.ConsumeSAMLLoginRequest(ctx, hashSecret(relay))
		if err != nil || dest != "/" {
			t.Errorf("next=%s stored destination %q (%v), want /", next, dest, err)
		}
	}
	// 예전에 저장된 위험한 목적지도 착지 페이지에서 다시 거른다.
	if got := safeSAMLRedirect("/\t/evil.com"); got != "/" {
		t.Fatalf("landing destination %q", got)
	}
}

// SAML IdP는 검증된 도메인의 기존 계정으로 로그인시키므로, 서버 관리자가 서버 owner 계정이 있는 도메인의 IdP를 바꾸거나
// 그 도메인을 더하면 owner 계정으로 로그인할 수 있게 된다. 그런 변경은 서버 owner만 해야 한다.
func TestSAMLSettingsTrustChangesOverOwnerDomainsNeedOwner(t *testing.T) {
	f := newSAMLTestFixture(t)
	ctx := context.Background()
	st := f.server.Store
	admin := testUser(t, st, "ops@example.com", store.RoleAdmin)
	owner := testUser(t, st, "root@corp.test", store.RoleOwner)
	f.server.SuperAdmins = map[string]bool{"chief@future.test": true} // 아직 가입하지 않은 superadmin
	put := func(u *store.User, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPut, "/api/saml/settings", strings.NewReader(body))
		w := httptest.NewRecorder()
		f.server.putSAMLSettings(w, withPrincipal(r, &principal{User: u, Via: viaSession, SAML: true}))
		return w
	}
	cfg, err := st.GetSAMLConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	otherIdP := strings.ReplaceAll(cfg.MetadataXML, "https://idp.example.test/metadata", "https://evil-idp.example.test/metadata")
	if otherIdP == cfg.MetadataXML {
		t.Fatal("fixture metadata did not change")
	}
	mdBody, _ := json.Marshal(map[string]any{"metadata_xml": otherIdP})
	// 도메인을 더하는 것: owner가 없는 도메인은 관리자도 되고, owner나 미가입 superadmin의 도메인은 owner만.
	if w := put(admin, `{"domains":["example.com","partner.test"]}`); w.Code != 200 {
		t.Fatalf("admin adds a domain without owners: %d %s", w.Code, w.Body)
	}
	for _, d := range []string{"corp.test", "future.test"} {
		if w := put(admin, `{"domains":["example.com","partner.test","`+d+`"]}`); w.Code != 403 {
			t.Errorf("admin adds owner domain %s: %d %s, want 403", d, w.Code, w.Body)
		}
	}
	if w := put(owner, `{"domains":["example.com","partner.test","corp.test"]}`); w.Code != 200 {
		t.Fatalf("owner adds own domain: %d %s", w.Code, w.Body)
	}
	// 이제 owner 도메인이 목록에 있으므로 IdP 교체는 owner만 한다. 지우기(SSO 끄기)는 관리자도 된다.
	if w := put(admin, string(mdBody)); w.Code != 403 {
		t.Errorf("admin replaces IdP over owner domain: %d %s, want 403", w.Code, w.Body)
	}
	if got, _ := st.GetSAMLConfig(ctx); got.MetadataXML != cfg.MetadataXML {
		t.Fatal("IdP metadata changed by admin")
	}
	if w := put(owner, string(mdBody)); w.Code != 200 {
		t.Errorf("owner replaces IdP: %d %s", w.Code, w.Body)
	}
	if w := put(admin, `{"clear_metadata":true}`); w.Code != 200 {
		t.Errorf("admin clears IdP: %d %s", w.Code, w.Body)
	}
}

// 비상 잠금과 토큰 일괄 폐기는 OAuth 앱에 준 access/refresh token도 끊어야 한다(owner 것은 남긴다).
func TestLockdownRevokesOAuthGrants(t *testing.T) {
	for _, tc := range []struct{ name, path, body string }{
		{"lockdown", "/api/owner/lockdown", `{}`},
		{"tokens non_owners", "/api/owner/tokens/revoke", `{"scope":"non_owners"}`},
		{"tokens user", "/api/owner/tokens/revoke", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOAuthFixture(t)
			boss := testUser(t, f.st, "boss@example.com", store.RoleOwner)
			bossC := testSessionCookie(t, f.s, boss)
			tokenFor := func(cookie string) tokenResponse {
				back := f.consent(cookie, authorizeURL(f.clientID, oauthTestRedirect, "openid offline_access", merge(pkce(), url.Values{"prompt": {"consent"}})),
					map[string]any{"decision": "approve", "scopes": []string{"openid", "offline_access"}})
				w, tr := f.exchange(back.Query().Get("code"), oauthTestVerifier)
				if w.Code != 200 || tr.AccessToken == "" || tr.RefreshToken == "" {
					t.Fatalf("exchange: %d %+v", w.Code, tr)
				}
				return tr
			}
			member, owner := tokenFor(f.memberC), tokenFor(bossC)
			body := tc.body
			if body == "" {
				body = fmt.Sprintf(`{"user_id":%q}`, f.member.ID)
			}
			if w := req(f.h, "POST", tc.path, body, withCookie(bossC)); w.Code != 200 {
				t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
			}
			if w := req(f.h, "GET", "/api/auth/me", "", withBearer(member.AccessToken)); w.Code != 401 {
				t.Errorf("member OAuth access token after %s: %d", tc.name, w.Code)
			}
			refresh := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {member.RefreshToken}}
			if w := tokenReq(f.h, refresh, f.clientID, f.secret); w.Code != 400 {
				t.Errorf("member refresh token after %s: %d %s", tc.name, w.Code, w.Body)
			}
			if w := req(f.h, "GET", "/api/auth/me", "", withBearer(owner.AccessToken)); w.Code != 200 {
				t.Errorf("owner OAuth access token after %s: %d", tc.name, w.Code)
			}
		})
	}
}

// 형제 subdomain(예: 배포된 서비스)이 Domain=으로 심은 세션 쿠키가 진짜 쿠키보다 앞에 오면 피해자가 공격자 계정으로 요청하게 된다
// (GitHub 연결 콜백, OAuth 승인 등). 같은 이름의 세션 쿠키가 여러 개면 어느 것도 믿지 않는다.
func TestDuplicateSessionCookiesAreRejected(t *testing.T) {
	f, h := authServer(t)
	victim, _ := freshAccount(t, f, "victim@example.com", store.RoleMember)
	attacker, _ := freshAccount(t, f, "attacker@example.com", store.RoleMember)
	vc, ac := testSessionCookie(t, f.srv, victim), testSessionCookie(t, f.srv, attacker)
	both := func(r *http.Request) {
		r.AddCookie(&http.Cookie{Name: cookieName, Value: ac}) // 더 구체적인 Path로 심어 먼저 보내진다
		r.AddCookie(&http.Cookie{Name: cookieName, Value: vc})
	}
	if w := req(h, "GET", "/api/auth/me", "", withCookie(vc)); w.Code != 200 {
		t.Fatalf("single cookie: %d", w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", both); w.Code != 401 {
		t.Errorf("duplicate session cookies on API: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", "/api/auth/view-as/stop", "", both); w.Code != 401 {
		t.Errorf("duplicate session cookies on view-as stop: %d", w.Code)
	}
	w := req(h, "GET", "/api/github/oauth/callback?bounced=1&code=x&state=y", "", both)
	if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/login/") {
		t.Errorf("duplicate session cookies on GitHub callback: %d %s", w.Code, w.Header().Get("Location"))
	}
}

// reviewedPublicRoutes는 권한 계층(requireAuth + authorize) 밖에 있는 라우트와 그 핸들러가 스스로 확인하는 자격이다.
// 새 공개 라우트는 보안 검토 후 여기에 이유와 함께 추가해야 한다(TestPublicRoutesAreReviewed).
var reviewedPublicRoutes = map[string]string{
	"GET /.well-known/openid-configuration":       "public OIDC discovery document",
	"GET /oauth/.well-known/openid-configuration": "public OIDC discovery document",
	"GET /oauth/jwks":                             "public signing key",
	"GET /oauth/auth":                             "validates client_id/redirect_uri, then the browser session (oauthSession)",
	"GET /oauth/authorize":                        "same as /oauth/auth",
	"/oauth/token":                                "OAuth client authentication + code/refresh token",
	"/oauth/me":                                   "OAuth access token",
	"/oauth/userinfo":                             "OAuth access token",
	"/oauth/revoke":                               "OAuth client authentication",
	"POST /api/auth/login":                        "password (+ second factor), rate limited, same-origin",
	"POST /api/auth/logout":                       "deletes only the caller's own session cookie",
	"POST /api/auth/view-as/stop":                 "session cookie, same-origin",
	"GET /api/auth/saml/status":                   "public login-page flags",
	"GET /api/auth/saml/login":                    "starts SP-initiated SAML login",
	"POST /api/auth/saml/acs":                     "signed SAML assertion",
	"GET /api/auth/saml/metadata":                 "public SP metadata",
	"POST /api/auth/passkeys/login/begin":         "issues a WebAuthn challenge (password for the factor flow)",
	"POST /api/auth/passkeys/login/factor/{challenge}/finish":       "single-use challenge + WebAuthn assertion",
	"POST /api/auth/passkeys/login/discoverable/{challenge}/finish": "single-use challenge + WebAuthn assertion",
	"POST /api/webhooks/github":                                     "GitHub webhook HMAC signature",
	"GET /api/github/oauth/callback":                                "session cookie + single-use state bound to the user",
	"POST /api/agent/enroll":                                        "join token over TLS",
	"POST /api/agent/upgrade":                                       "node secret over TLS",
	"GET /api/agent/connect":                                        "agent certificate challenge or legacy node secret",
	"GET /install-agent.sh":                                         "public install script",
	"GET /downloads/{file}":                                         "public agent/CLI binaries",
	"GET /install.sh":                                               "public install script",
	"GET /api/cli/downloads":                                        "public download list",
	"POST /api/auth/accept-invite":                                  "invite token, rate limited, same-origin",
	"POST /v1/traces":                                               "OTLP receiver (only when tracing is enabled)",
	"/api/":                                                         "catch-all 404",
	"/mcp":                                                          "requireAuth inside mcpHandler",
	"POST /graphql/v2":                                              "requireAuth inside graphQLHandler; inner calls go through the REST routes",
	"GET /graphql/v2":                                               "static GraphiQL page",
	"/":                                                             "embedded web UI (only when built with it)",
}

func TestPublicRoutesAreReviewed(t *testing.T) {
	f, h := authServer(t)
	public := f.srv.PublicRoutes()
	seen := map[string]bool{}
	for _, p := range public {
		if _, ok := reviewedPublicRoutes[p]; !ok {
			t.Errorf("route %q bypasses requireAuth/authorize and is not in reviewedPublicRoutes", p)
		}
		if seen[p] {
			t.Errorf("public route %q registered twice", p)
		}
		seen[p] = true
	}
	optional := map[string]bool{"POST /v1/traces": true, "/": true}
	for p := range reviewedPublicRoutes {
		if !seen[p] && !optional[p] {
			t.Errorf("reviewed public route %q is no longer registered; remove it from the list", p)
		}
	}
	for _, rt := range f.srv.Routes() {
		if seen[rt.Pattern] {
			t.Errorf("%q is registered both with and without authorization", rt.Pattern)
		}
	}
	// 공개 상태 변경 라우트는 자격 없이 부르면 성공하지 않는다(로그아웃과 패스키 챌린지 발급만 예외).
	noCredOK := map[string]bool{"POST /api/auth/logout": true, "POST /api/auth/passkeys/login/begin": true}
	for _, p := range public {
		method, path, ok := strings.Cut(p, " ")
		if !ok || method != "POST" || noCredOK[p] {
			continue
		}
		path = strings.ReplaceAll(path, "{challenge}", "x")
		if w := req(h, method, path, "{}", nil); w.Code < 300 {
			t.Errorf("%s without credentials: %d %s", p, w.Code, w.Body)
		}
	}
	// 등록되지 않은 /api/ 경로는 인증 여부와 관계없이 404다.
	if w := req(h, "GET", "/api/definitely-not-a-route", "", nil); w.Code != 404 {
		t.Errorf("unknown API path: %d", w.Code)
	}
}
