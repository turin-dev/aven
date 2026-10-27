package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// ---- 테스트 도우미 ----

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// testUser creates a legacy fixture with an explicit default workspace membership.
// New-account isolation tests use Store.CreateUser directly.
func testUser(t *testing.T, st *store.Store, email, role string) *store.User {
	t.Helper()
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	u := &store.User{Email: email, Name: strings.Split(email, "@")[0], Role: role, PasswordHash: h}
	if err := st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	if err := st.SetWorkspaceMember(context.Background(), store.DefaultWorkspaceID, u.ID, role); err != nil {
		t.Fatal(err)
	}
	return u
}

// testSessionCookie는 사용자 세션을 직접 만들고 쿠키 값을 돌려준다.
func testSessionCookie(t *testing.T, s *Server, u *store.User) string {
	t.Helper()
	tok := randomToken(32)
	se := &store.Session{UserID: u.ID, ExpiresAt: time.Now().Add(time.Hour)}
	if u.Builtin {
		se.AuthTag = s.adminTag()
	}
	if err := s.Store.CreateSession(context.Background(), se, hashSecret(tok)); err != nil {
		t.Fatal(err)
	}
	return tok
}

var ipSeq atomic.Int32

// req는 매번 다른 클라이언트 IP로 요청한다(rate limit이 테스트끼리 섞이지 않게).
func req(h http.Handler, method, path, body string, auth func(*http.Request)) *httptest.ResponseRecorder {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, path, rd)
	n := ipSeq.Add(1)
	r.RemoteAddr = fmt.Sprintf("198.18.%d.%d:1000", (n/250)%250, n%250+1)
	if auth != nil {
		auth(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func withCookie(v string) func(*http.Request) {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: cookieName, Value: v}) }
}

func withBearer(v string) func(*http.Request) {
	return func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+v) }
}

func sessionFrom(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == cookieName && c.Value != "" {
			return c.Value
		}
	}
	t.Fatalf("no session cookie (status %d: %s)", w.Code, w.Body.String())
	return ""
}

func decodeBody[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", w.Body.String(), err)
	}
	return v
}

// ---- 순수 함수 ----

func TestPasswordHashing(t *testing.T) {
	h, err := HashPassword("correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(h, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatalf("hash format: %s", h)
	}
	if !VerifyPassword(h, "correct horse battery") || VerifyPassword(h, "correct horse batterY") || VerifyPassword(h, "") {
		t.Error("verify")
	}
	h2, _ := HashPassword("correct horse battery")
	if h == h2 {
		t.Error("salt not random")
	}
	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=19456,t=2,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA", "$argon2id$v=19$m=1,t=1,p=1$c2FsdHNhbHQ$aGFzaGhhc2hoYXNoaGFzaA",
		strings.Replace(h, "t=2", "t=99", 1)} {
		if VerifyPassword(bad, "correct horse battery") {
			t.Errorf("malformed hash accepted: %q", bad)
		}
	}
	if checkPasswordPolicy("") == nil || checkPasswordPolicy(strings.Repeat("x", 300)) == nil || checkPasswordPolicy("1") != nil || checkPasswordPolicy("short") != nil {
		t.Error("password policy")
	}
}

// RFC 6238 부록 B의 SHA1 벡터(8자리)를 6자리로 줄인 값. 비밀은 ASCII "12345678901234567890".
func TestTOTPVectors(t *testing.T) {
	secret := b32.EncodeToString([]byte("12345678901234567890"))
	for _, tc := range []struct {
		t    int64
		want string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}} {
		got, err := TOTPAt(secret, time.Unix(tc.t, 0))
		if err != nil || got != tc.want {
			t.Errorf("T=%d: %s %v, want %s", tc.t, got, err, tc.want)
		}
	}
	// HOTP 8자리도 RFC 값과 같다.
	if got := HOTP([]byte("12345678901234567890"), 1, 8); got != "94287082" {
		t.Errorf("8 digits: %s", got)
	}
	now := time.Unix(1111111111, 0)
	for _, d := range []time.Duration{-30 * time.Second, 0, 30 * time.Second} {
		code, _ := TOTPAt(secret, now.Add(d))
		step, ok := MatchTOTP(secret, code, now)
		if !ok || step != now.Add(d).Unix()/30 {
			t.Errorf("skew %v rejected", d)
		}
	}
	far, _ := TOTPAt(secret, now.Add(90*time.Second))
	if _, ok := MatchTOTP(secret, far, now); ok {
		t.Error("code 3 steps away accepted")
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef", "05047a"} {
		if _, ok := MatchTOTP(secret, bad, now); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	u := OTPAuthURL("aven", "a@example.com", secret)
	if !strings.HasPrefix(u, "otpauth://totp/aven:a@example.com?") || !strings.Contains(u, "secret="+secret) || !strings.Contains(u, "issuer=aven") {
		t.Errorf("otpauth url: %s", u)
	}
	codes := NewRecoveryCodes()
	if len(codes) != 10 || len(codes[0]) != 11 || codes[0][5] != '-' || normalizeRecovery(strings.ToUpper(strings.ReplaceAll(codes[0], "-", " "))) != codes[0] {
		t.Errorf("recovery codes: %v", codes)
	}
}

// ---- 모든 라우트의 권한 행렬 ----

type rbacFixture struct {
	s                         *Server
	h                         http.Handler
	st                        *store.Store
	p                         *store.Project
	prod                      *store.Environment
	owner, viewer, outsider   *store.User
	ownerC, viewerC, outsideC string
	fill                      func(pattern string) string
}

func newRBACFixture(t *testing.T) *rbacFixture {
	ctx := context.Background()
	st := testStore(t)
	s := &Server{Store: st, AdminPassword: "password123", SessionKey: make([]byte, 32), authzProbe: true, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f := &rbacFixture{s: s, h: s.Handler(), st: st}
	f.p = &store.Project{Name: "rbac"}
	var err error
	if f.prod, err = st.CreateProject(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: f.p.ID, Name: "web", SourceType: "image"}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(st.CreateService(ctx, svc))
	in := &store.Instance{ServiceID: svc.ID, EnvironmentID: f.prod.ID}
	must(st.CreateInstance(ctx, in))
	d := &store.Deployment{ServiceID: svc.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Trigger: "manual"}
	must(st.CreateDeployment(ctx, d))
	run := &store.CronRun{DeploymentID: d.ID, InstanceID: in.ID, Container: "c"}
	must(st.CreateCronRun(ctx, run))
	vol := &store.Volume{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, InstanceID: &in.ID, Name: "data", MountPath: "/data"}
	must(st.CreateVolume(ctx, vol))
	bak := &store.Backup{VolumeID: vol.ID, Status: "done"}
	must(st.CreateBackup(ctx, bak))
	dbb := &store.DBBackup{ServiceID: svc.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Kind: "postgres", Status: "done", Trigger: "manual"}
	must(st.CreateDBBackup(ctx, dbb))
	dest := &store.BackupDestination{Name: "local2", Type: store.DestLocal}
	must(st.CreateBackupDestination(ctx, dest))
	f.owner = testUser(t, st, "owner@example.com", store.RoleOwner)
	f.viewer = testUser(t, st, "viewer@example.com", store.RoleViewer)
	f.outsider = testUser(t, st, "outsider@example.com", store.RoleMember)
	victim := testUser(t, st, "victim@example.com", store.RoleMember)
	must(st.SetMember(ctx, f.p.ID, f.viewer.ID, store.RoleViewer))
	must(st.SetMember(ctx, f.p.ID, victim.ID, store.RoleMember))
	tok := &store.APIToken{UserID: victim.ID, Name: "t", Prefix: "keel_x", Scope: store.ScopeWorkspace, Role: store.RoleMember}
	must(st.CreateAPIToken(ctx, tok, hashSecret("keel_victim")))
	inv := &store.Invite{Email: "new@example.com", Role: store.RoleMember, ExpiresAt: time.Now().Add(time.Hour)}
	must(st.CreateInvite(ctx, inv, hashSecret("inv")))
	f.ownerC, f.viewerC, f.outsideC = testSessionCookie(t, s, f.owner), testSessionCookie(t, s, f.viewer), testSessionCookie(t, s, f.outsider)
	vals := map[string]string{"pid": f.p.ID, "eid": f.prod.ID, "sid": svc.ID, "did": d.ID, "rid": run.ID, "vid": vol.ID, "bid": bak.ID,
		"stid": "stk_x", "nid": "node_local", "uid": victim.ID, "iid": inv.ID, "tid": tok.ID, "sesid": "ses_x", "host": "a.example.com",
		"name": "t", "key": "postgres", "cid": "nch_x", "arid": "alr_x"}
	vals["gid"], vals["dbid"] = "grp_x", "dsh_x" // 캔버스 그룹, 관측 대시보드
	vals["wsid"] = store.DefaultWorkspaceID
	vals["scope"] = store.HoldProject // 서버 owner 정지(owner/holds)
	vals["appid"], vals["secretid"], vals["grantid"], vals["requestid"] = "oapp_x", "osec_x", "ogrt_x", "keel_req_x"
	sbx := &store.Sandbox{ID: "sbx_rbac", ProjectID: f.p.ID, EnvironmentID: f.prod.ID, NodeID: store.LocalNodeID,
		ContainerName: "keel-sbx-rbac", NetworkName: "keel-sbx-rbac", Image: "ubuntu:24.04", MemoryMB: 512, CPUCores: 1}
	must(st.CreateSandbox(ctx, sbx))
	scp := &store.SandboxCheckpoint{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, NodeID: store.LocalNodeID, Name: "base", Image: "keel/sandbox:scp-rbac"}
	_, _, err = st.SaveSandboxCheckpoint(ctx, scp)
	must(err)
	vals["sbid"], vals["cpid"] = sbx.ID, scp.ID
	f.fill = func(pattern string) string {
		_, path, _ := strings.Cut(pattern, " ")
		if strings.Contains(path, "/db-backups/{id}") {
			path = strings.ReplaceAll(path, "{id}", dbb.ID)
		}
		path = strings.ReplaceAll(path, "{id}", dest.ID)
		path = strings.ReplaceAll(path, "{key...}", "dir/obj.txt") // bucket 객체 key
		for k, v := range vals {
			path = strings.ReplaceAll(path, "{"+k+"}", v)
		}
		if strings.Contains(path, "{") {
			t.Fatalf("unfilled path variable in %s", pattern)
		}
		return path
	}
	return f
}

// selfService는 프로젝트와 무관하게 작업 공간 사용자가 쓸 수 있는 상태 변경 라우트다.
// 새 상태 변경 라우트는 여기 넣지 않는 한 viewer와 프로젝트 비멤버에게 거부되어야 한다.
// 값: viewer(작업 공간 viewer)와 outsider(작업 공간 member, 프로젝트 비멤버)가 권한 검사를 통과하는지.
var selfService = map[string][2]bool{
	"PATCH /api/auth/me":                     {true, true},
	"DELETE /api/auth/sessions/{sesid}":      {true, true},
	"POST /api/auth/2fa/setup":               {true, true},
	"POST /api/auth/2fa/enable":              {true, true},
	"POST /api/auth/2fa/disable":             {true, true},
	"POST /api/auth/2fa/recovery-codes":      {true, true},
	"POST /api/tokens":                       {true, true}, // 역할 상한은 핸들러가 검사한다(TestTokenScopes)
	"DELETE /api/tokens/{tid}":               {true, true}, // 남의 토큰은 핸들러가 404
	"POST /api/projects":                     {true, true}, // 대상 작업 공간 member 이상인지는 핸들러가 검사한다(TestWorkspaceIsolation)
	"POST /api/invites":                      {true, true}, // 비admin은 자기가 admin인 프로젝트로만(TestSessionLifecycleAndInviteFlow)
	"PUT /api/auth/notification-preferences": {true, true}, // 자기 구독 설정
	"POST /api/notifications/read":           {true, true}, // 자기 알림함
	"POST /api/ai/chat":                      {true, true}, // 대화 기록은 각 사용자 소유, 도구별 권한은 호출 때 검사한다
	"POST /api/ai/confirm":                   {true, true}, // 확인 뒤에도 도구가 리소스 권한을 다시 검사한다
	"DELETE /api/ai/threads/{id}":            {true, true}, // 자기 대화만 삭제한다
	"POST /api/templates":                    {true, true}, // 작업 공간 member 이상이 자기 템플릿을 올린다
	"PATCH /api/templates/{key}":             {true, true}, // 만든 사람이나 admin인지는 핸들러가 검사한다(TestTemplateVisibility)
	"DELETE /api/templates/{key}":            {true, true},
	"POST /api/templates/{key}/deploy":       {true, true}, // 대상 프로젝트 권한은 핸들러가 검사한다(TestTemplateVisibility)
	"POST /api/feature-flags/evaluate":       {true, true}, // 읽기 전용 평가. 프로젝트 토큰이 아니면 핸들러가 403(TestFeatureFlagAPIAndProjectTokenEvaluation)
	// GitHub 연결은 사용자별이다: 자기 연결 해제, 자기 토큰으로 웹훅 등록(TestGitHubOAuthConnectFlow, TestGitHubEnsureWebhook)
	"DELETE /api/github/connection": {true, true},
	"POST /api/github/webhooks":     {true, true},
	// 작업 공간: 누구나 팀 작업 공간을 만들고, 나머지는 핸들러가 작업 공간 멤버십을 검사한다(TestWorkspaceIsolation)
	"POST /api/workspaces":                        {true, true},
	"DELETE /api/workspaces/{wsid}/members/{uid}": {true, true},
	// 서비스 어드민 탭: 프로젝트를 볼 수 있으면 라우트를 통과하고, 서버 관리자인지는 핸들러가 검사한다(TestServiceAdminHoldsBlockDeploys)
	"POST /api/services/{sid}/admin/holds":           {true, false},
	"DELETE /api/services/{sid}/admin/holds/{scope}": {true, false},
}

func TestRouteAuthzMatrix(t *testing.T) {
	f := newRBACFixture(t)
	routes := f.s.Routes()
	if len(routes) < 110 {
		t.Fatalf("only %d routes registered", len(routes))
	}
	seen := map[string]bool{}
	for _, rt := range routes {
		method, _, _ := strings.Cut(rt.Pattern, " ")
		path := f.fill(rt.Pattern)
		seen[rt.Pattern] = true
		code := func(c string) int { return req(f.h, method, path, "", withCookie(c)).Code }
		if got := req(f.h, method, path, "", nil).Code; got != 401 {
			t.Errorf("%s without auth: %d", rt.Pattern, got)
		}
		if got := code(f.ownerC); got != probeStatus {
			t.Errorf("%s as owner: %d (resolver or fixture broken)", rt.Pattern, got)
		}
		viewer, outsider := code(f.viewerC), code(f.outsideC)
		denied := func(c int) bool { return c == 403 || c == 404 }
		if method != "GET" {
			if allow, ok := selfService[rt.Pattern]; ok {
				if (viewer == probeStatus) != allow[0] || (outsider == probeStatus) != allow[1] || (!allow[0] && !denied(viewer)) || (!allow[1] && !denied(outsider)) {
					t.Errorf("%s self-service: viewer=%d outsider=%d", rt.Pattern, viewer, outsider)
				}
				continue
			}
			if !denied(viewer) {
				t.Errorf("%s: viewer got %d, want 403/404", rt.Pattern, viewer)
			}
			if !denied(outsider) {
				t.Errorf("%s: non-member got %d, want 403/404", rt.Pattern, outsider)
			}
			continue
		}
		switch rt.Access.Kind {
		case accProject:
			if outsider != 404 {
				t.Errorf("%s: non-member got %d, want 404", rt.Pattern, outsider)
			}
			if want := rt.Access.Level <= lvViewer; (viewer == probeStatus) != want || (!want && viewer != 403) {
				t.Errorf("%s: viewer got %d", rt.Pattern, viewer)
			}
		case accInstance, accWorkspace:
			if viewer == probeStatus && rt.Access.Level > lvViewer {
				t.Errorf("%s: workspace viewer got %d", rt.Pattern, viewer)
			}
			if outsider == probeStatus && rt.Access.Level > lvMember {
				t.Errorf("%s: workspace member got %d", rt.Pattern, outsider)
			}
		}
	}
	for p := range selfService {
		if !seen[p] {
			t.Errorf("self-service entry %s is not a registered route", p)
		}
	}
	// 범위 있는 API 토큰은 계정/사용자/멤버 관리를 할 수 없다.
	ctx := context.Background()
	tok := &store.APIToken{UserID: f.owner.ID, Name: "ci", Prefix: "keel_ci", Scope: store.ScopeWorkspace, Role: store.RoleAdmin}
	if err := f.st.CreateAPIToken(ctx, tok, hashSecret("keel_ownertoken")); err != nil {
		t.Fatal(err)
	}
	for _, rt := range routes {
		method, _, _ := strings.Cut(rt.Pattern, " ")
		got := req(f.h, method, f.fill(rt.Pattern), "", withBearer("keel_ownertoken")).Code
		if rt.Access.NoToken && got != 403 {
			t.Errorf("%s with an API token: %d, want 403", rt.Pattern, got)
		}
		if !rt.Access.NoToken && rt.Access.Kind == accInstance {
			if got != 403 {
				t.Errorf("%s: workspace token reaches instance boundary: %d", rt.Pattern, got)
			}
		} else if !rt.Access.NoToken && got != probeStatus {
			t.Errorf("%s with an owner's workspace admin token: %d", rt.Pattern, got)
		}
	}
}

// ---- 동작 테스트(실제 핸들러) ----

func authServer(t *testing.T) (*ghFixture, http.Handler) {
	f := newGHFixture(t)
	f.srv.AdminPassword = "password123"
	f.srv.APIToken = "legacy-token"
	f.srv.SessionKey = []byte("0123456789abcdef0123456789abcdef")
	return f, f.srv.Handler()
}

func TestLegacyAdminPasswordAndAPIToken(t *testing.T) {
	f, h := authServer(t)
	w := req(h, "POST", "/api/auth/login", `{"password":"password123"}`, nil)
	if w.Code != 204 {
		t.Fatalf("password-only login: %d %s", w.Code, w.Body)
	}
	c := sessionFrom(t, w)
	if strings.Contains(c, ".") && len(c) < 40 {
		t.Errorf("cookie looks like the old HMAC format: %s", c)
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", withCookie(c)))
	if me["user"] != "admin" || me["role"] != "owner" || me["email"] != "admin@local" || me["builtin"] != true || me["auth"] != "session" {
		t.Errorf("me: %v", me)
	}
	if w := req(h, "POST", "/api/auth/login", `{"email":"ADMIN@local","password":"password123"}`, nil); w.Code != 204 {
		t.Errorf("admin@local login: %d", w.Code)
	}
	me = decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", withBearer("legacy-token")))
	if me["user"] != "admin" || me["role"] != "owner" || me["auth"] != "env_token" || me["workspace_admin"] != true {
		t.Errorf("me via KEEL_API_TOKEN: %v", me)
	}
	if w := req(h, "POST", "/api/projects", `{"name":"via-legacy"}`, withBearer("legacy-token")); w.Code != 201 {
		t.Errorf("legacy token create project: %d %s", w.Code, w.Body)
	}
	// 예전 HMAC 쿠키("exp.sig")는 더 이상 통하지 않는다.
	if w := req(h, "GET", "/api/auth/me", "", withCookie(fmt.Sprint(time.Now().Add(time.Hour).Unix())+".abc")); w.Code != 401 {
		t.Errorf("old cookie: %d", w.Code)
	}
	// 운영자가 KEEL_ADMIN_PASSWORD를 바꾸면 기본 계정 세션은 모두 끊긴다.
	f.srv.AdminPassword = "rotated-password"
	if w := req(h, "GET", "/api/auth/me", "", withCookie(c)); w.Code != 401 {
		t.Errorf("session after env password rotation: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/login", `{"password":"password123"}`, nil); w.Code != 401 {
		t.Errorf("old password after rotation: %d", w.Code)
	}
	// 기본 계정의 비밀번호는 API로 바꾸지 않는다.
	w = req(h, "POST", "/api/auth/login", `{"password":"rotated-password"}`, nil)
	c = sessionFrom(t, w)
	if w := req(h, "PATCH", "/api/auth/me", `{"current_password":"rotated-password","new_password":"another password"}`, withCookie(c)); w.Code != 400 {
		t.Errorf("builtin password change via API: %d", w.Code)
	}
}

func TestSessionLifecycleAndInviteFlow(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	// 프로젝트 viewer로 초대
	w := req(h, "POST", "/api/invites", fmt.Sprintf(`{"email":" Alice@Example.com ","role":"member","project_id":%q,"project_role":"viewer"}`, f.p.ID), legacy)
	if w.Code != 201 {
		t.Fatalf("invite: %d %s", w.Code, w.Body)
	}
	inv := decodeBody[struct {
		Token string `json:"token"`
		URL   string `json:"url"`
	}](t, w)
	if !strings.Contains(inv.URL, "/invite/#token="+inv.Token) {
		t.Errorf("invite url: %s", inv.URL)
	}
	if w := req(h, "POST", "/api/auth/accept-invite", `{"token":"nope","name":"Alice","password":"correct horse battery"}`, nil); w.Code != 400 {
		t.Errorf("bad invite token: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/accept-invite", `{"token":"`+inv.Token+`","name":"Alice","password":""}`, nil); w.Code != 400 {
		t.Errorf("empty password: %d", w.Code)
	}
	w = req(h, "POST", "/api/auth/accept-invite", `{"token":"`+inv.Token+`","name":"Alice","password":"correct horse battery"}`, nil)
	if w.Code != 201 {
		t.Fatalf("accept: %d %s", w.Code, w.Body)
	}
	c0 := sessionFrom(t, w)
	if w := req(h, "POST", "/api/auth/accept-invite", `{"token":"`+inv.Token+`","name":"Alice","password":"correct horse battery"}`, nil); w.Code != 400 {
		t.Errorf("invite reused: %d", w.Code)
	}
	me := decodeBody[struct {
		Email    string            `json:"email"`
		Role     string            `json:"role"`
		Projects map[string]string `json:"projects"`
	}](t, req(h, "GET", "/api/auth/me", "", withCookie(c0)))
	if me.Email != "alice@example.com" || me.Role != "member" || len(me.Projects) != 1 || me.Projects[f.p.ID] != "viewer" {
		t.Errorf("me after invite: %+v", me)
	}
	// 프로젝트 목록은 멤버인 프로젝트만(기본 프로젝트는 안 보인다)
	if ps := decodeBody[[]store.Project](t, req(h, "GET", "/api/projects", "", withCookie(c0))); len(ps) != 1 || ps[0].ID != f.p.ID {
		t.Errorf("visible projects: %+v", ps)
	}
	// 이메일 로그인
	if w := req(h, "POST", "/api/auth/login", `{"email":"alice@example.com","password":"wrong password!"}`, nil); w.Code != 401 {
		t.Errorf("wrong password: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/login", `{"email":"nobody@example.com","password":"correct horse battery"}`, nil); w.Code != 401 {
		t.Errorf("unknown email: %d", w.Code)
	}
	c1 := sessionFrom(t, req(h, "POST", "/api/auth/login", `{"email":"ALICE@example.com","password":"correct horse battery"}`, nil))
	c2 := sessionFrom(t, req(h, "POST", "/api/auth/login", `{"email":"alice@example.com","password":"correct horse battery"}`, nil))
	ses := decodeBody[[]struct {
		ID      string `json:"id"`
		Current bool   `json:"current"`
	}](t, req(h, "GET", "/api/auth/sessions", "", withCookie(c1)))
	if len(ses) != 3 {
		t.Fatalf("sessions: %+v", ses)
	}
	cur := 0
	for _, s := range ses {
		if s.Current {
			cur++
		}
	}
	if cur != 1 {
		t.Errorf("current marker: %+v", ses)
	}
	// 비밀번호를 바꾸면 다른 세션은 끊기고 지금 세션은 남는다.
	if w := req(h, "PATCH", "/api/auth/me", `{"current_password":"nope nope nope","new_password":"brand new password"}`, withCookie(c1)); w.Code != 401 {
		t.Errorf("wrong current password: %d", w.Code)
	}
	if w := req(h, "PATCH", "/api/auth/me", `{"name":"Alice A","current_password":"correct horse battery","new_password":"brand new password"}`, withCookie(c1)); w.Code != 200 {
		t.Fatalf("password change: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/auth/me", "", withCookie(c2)); w.Code != 401 {
		t.Errorf("other session after password change: %d", w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", withCookie(c0)); w.Code != 401 {
		t.Errorf("invite session after password change: %d", w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", withCookie(c1)); w.Code != 200 {
		t.Errorf("current session after password change: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/login", `{"email":"alice@example.com","password":"correct horse battery"}`, nil); w.Code != 401 {
		t.Errorf("old password still works: %d", w.Code)
	}
	// 로그아웃하면 세션이 서버에서 지워진다.
	if w := req(h, "POST", "/api/auth/logout", "", withCookie(c1)); w.Code != 204 {
		t.Errorf("logout: %d", w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", withCookie(c1)); w.Code != 401 {
		t.Errorf("session after logout: %d", w.Code)
	}
	// 비활성화하면 로그인할 수 없고 세션도 끊긴다.
	c3 := sessionFrom(t, req(h, "POST", "/api/auth/login", `{"email":"alice@example.com","password":"brand new password"}`, nil))
	alice, _ := f.st.GetUserByEmail(f.ctx, "alice@example.com")
	if w := req(h, "PATCH", "/api/users/"+alice.ID, `{"disabled":true}`, legacy); w.Code != 200 {
		t.Fatalf("disable: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/auth/me", "", withCookie(c3)); w.Code != 401 {
		t.Errorf("disabled user's session: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/login", `{"email":"alice@example.com","password":"brand new password"}`, nil); w.Code != 403 {
		t.Errorf("disabled login: %d", w.Code)
	}
	// 감사 기록
	aud := decodeBody[[]store.AuditEntry](t, req(h, "GET", "/api/audit", "", legacy))
	kinds := map[string]bool{}
	for _, a := range aud {
		kinds[a.Kind] = true
	}
	for _, k := range []string{"invite.created", "user.created", "auth.login", "user.updated"} {
		if !kinds[k] {
			t.Errorf("audit kind %s missing: %v", k, kinds)
		}
	}
	// 비admin 초대 제한: 프로젝트 admin이 아니면 초대할 수 없다.
	bob := testUser(t, f.st, "bob@example.com", store.RoleMember)
	bc := testSessionCookie(t, f.srv, bob)
	if w := req(h, "POST", "/api/invites", `{"email":"c@example.com"}`, withCookie(bc)); w.Code != 403 {
		t.Errorf("member invite without project: %d", w.Code)
	}
	if w := req(h, "POST", "/api/invites", fmt.Sprintf(`{"email":"c@example.com","project_id":%q}`, f.p.ID), withCookie(bc)); w.Code != 404 {
		t.Errorf("member invite to a project they cannot see: %d", w.Code)
	}
	f.st.SetMember(f.ctx, f.p.ID, bob.ID, store.RoleAdmin)
	if w := req(h, "POST", "/api/invites", fmt.Sprintf(`{"email":"c@example.com","role":"admin","project_id":%q}`, f.p.ID), withCookie(bc)); w.Code != 403 {
		t.Errorf("project admin inviting a workspace admin: %d", w.Code)
	}
	if w := req(h, "POST", "/api/invites", fmt.Sprintf(`{"email":"c@example.com","project_id":%q,"project_role":"admin"}`, f.p.ID), withCookie(bc)); w.Code != 201 {
		t.Errorf("project admin invite: %d %s", w.Code, w.Body)
	}
}

func TestLastOwnerProtection(t *testing.T) {
	f, h := authServer(t)
	admin := testUser(t, f.st, "adm@example.com", store.RoleAdmin)
	ac := testSessionCookie(t, f.srv, admin)
	owner2 := testUser(t, f.st, "own2@example.com", store.RoleOwner)
	if w := req(h, "PATCH", "/api/users/"+owner2.ID, `{"role":"member"}`, withCookie(ac)); w.Code != 403 {
		t.Errorf("admin demoting an owner: %d", w.Code)
	}
	if w := req(h, "PATCH", "/api/users/"+admin.ID, `{"role":"owner"}`, withCookie(ac)); w.Code != 403 {
		t.Errorf("admin promoting to owner: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/users/"+store.BuiltinUserID, "", withBearer("legacy-token")); w.Code != 400 {
		t.Errorf("delete builtin: %d", w.Code)
	}
	oc := testSessionCookie(t, f.srv, owner2)
	if w := req(h, "PATCH", "/api/users/"+store.BuiltinUserID, `{"disabled":true}`, withCookie(oc)); w.Code != 200 {
		t.Fatalf("disable builtin with another owner present: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PATCH", "/api/users/"+owner2.ID, `{"role":"admin"}`, withCookie(oc)); w.Code != 409 {
		t.Errorf("demote the last active owner: %d", w.Code)
	}
	// 기본 계정을 비활성화하면 KEEL_ADMIN_PASSWORD 로그인도 막힌다(KEEL_API_TOKEN은 운영자 자격이라 계속 동작).
	if w := req(h, "POST", "/api/auth/login", `{"password":"password123"}`, nil); w.Code != 403 {
		t.Errorf("disabled builtin login: %d", w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", withBearer("legacy-token")); w.Code != 200 {
		t.Errorf("KEEL_API_TOKEN after disabling builtin: %d", w.Code)
	}
}

func TestTOTPLoginFlow(t *testing.T) {
	f, h := authServer(t)
	u := testUser(t, f.st, "totp@example.com", store.RoleMember)
	c := testSessionCookie(t, f.srv, u)
	setup := decodeBody[struct {
		Secret string `json:"secret"`
		URL    string `json:"otpauth_url"`
	}](t, req(h, "POST", "/api/auth/2fa/setup", "", withCookie(c)))
	if setup.Secret == "" || !strings.HasPrefix(setup.URL, "otpauth://totp/") {
		t.Fatalf("setup: %+v", setup)
	}
	// 비밀은 암호화해서(박스가 있으면) 저장하고 응답 외에는 나가지 않는다.
	if w := req(h, "POST", "/api/auth/2fa/enable", `{"code":"000000"}`, withCookie(c)); w.Code != 401 {
		t.Errorf("enable with a wrong code: %d", w.Code)
	}
	now := time.Now()
	code, _ := TOTPAt(setup.Secret, now)
	w := req(h, "POST", "/api/auth/2fa/enable", `{"code":"`+code+`"}`, withCookie(c))
	if w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	rc := decodeBody[struct {
		Codes []string `json:"recovery_codes"`
	}](t, w).Codes
	if len(rc) != 10 {
		t.Fatalf("recovery codes: %v", rc)
	}
	login := func(code string) *httptest.ResponseRecorder {
		return req(h, "POST", "/api/auth/login", `{"email":"totp@example.com","password":"correct horse battery","totp_code":"`+code+`"}`, nil)
	}
	if w := login(""); w.Code != 401 || !strings.Contains(w.Body.String(), `"totp_required"`) {
		t.Errorf("no code: %d %s", w.Code, w.Body)
	}
	if w := login(code); w.Code != 401 || strings.Contains(w.Body.String(), "totp_required") {
		t.Errorf("replayed enable code: %d %s", w.Code, w.Body)
	}
	next, _ := TOTPAt(setup.Secret, now.Add(30*time.Second))
	if w := login(next); w.Code != 204 {
		t.Fatalf("next-step code: %d %s", w.Code, w.Body)
	}
	if w := login(next); w.Code != 401 {
		t.Errorf("same code twice: %d", w.Code)
	}
	if w := login(strings.ToUpper(rc[0])); w.Code != 204 {
		t.Errorf("recovery code: %d %s", w.Code, w.Body)
	}
	if w := login(rc[0]); w.Code != 401 {
		t.Errorf("recovery code reused: %d", w.Code)
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", withCookie(c)))
	if me["totp_enabled"] != true {
		t.Errorf("me: %v", me)
	}
	if w := req(h, "POST", "/api/auth/2fa/disable", `{"password":"wrong wrong wrong"}`, withCookie(c)); w.Code != 401 {
		t.Errorf("disable with wrong password: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/2fa/disable", `{"password":"correct horse battery"}`, withCookie(c)); w.Code != 204 {
		t.Errorf("disable: %d %s", w.Code, w.Body)
	}
	if w := login(""); w.Code != 204 {
		t.Errorf("login after disabling 2fa: %d", w.Code)
	}
}

func TestTOTPBruteForceLocks(t *testing.T) {
	f, h := authServer(t)
	u := testUser(t, f.st, "bf@example.com", store.RoleMember)
	secret := NewTOTPSecret()
	f.st.SetUserTOTP(f.ctx, u.ID, secret, true)
	locked := false
	for i := 0; i < 30; i++ {
		w := req(h, "POST", "/api/auth/login", `{"email":"bf@example.com","password":"correct horse battery","totp_code":"000000"}`, nil)
		if w.Code == 429 {
			locked = true
			break
		}
	}
	if !locked {
		t.Fatal("totp guesses never locked the account")
	}
	good, _ := TOTPAt(secret, time.Now())
	if w := req(h, "POST", "/api/auth/login", `{"email":"bf@example.com","password":"correct horse battery","totp_code":"`+good+`"}`, nil); w.Code != 429 {
		t.Errorf("correct code while locked: %d", w.Code)
	}
}

func TestTokenScopes(t *testing.T) {
	f, h := authServer(t)
	ctx := f.ctx
	staging := &store.Environment{ProjectID: f.p.ID, Name: "staging"}
	if err := f.st.CreateEnvironment(ctx, staging); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateInstance(ctx, &store.Instance{ServiceID: f.web.ID, EnvironmentID: staging.ID}); err != nil {
		t.Fatal(err)
	}
	owner := testUser(t, f.st, "tok-owner@example.com", store.RoleOwner)
	oc := withCookie(testSessionCookie(t, f.srv, owner))
	w := req(h, "POST", "/api/tokens", fmt.Sprintf(`{"name":"ci","environment_id":%q,"role":"member","expires_in_days":30}`, f.prod.ID), oc)
	if w.Code != 201 {
		t.Fatalf("create token: %d %s", w.Code, w.Body)
	}
	created := decodeBody[struct {
		ID     string `json:"id"`
		Token  string `json:"token"`
		Prefix string `json:"prefix"`
		Scope  string `json:"scope"`
	}](t, w)
	if !strings.HasPrefix(created.Token, "aven_") || !strings.HasPrefix(created.Token, created.Prefix) || created.Scope != "environment" {
		t.Fatalf("token: %+v", created)
	}
	// 목록에는 토큰 값이 없다.
	if strings.Contains(req(h, "GET", "/api/tokens", "", oc).Body.String(), created.Token) {
		t.Error("token value listed")
	}
	tk := withBearer(created.Token)
	prodInst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	stagingInst := "/api/services/" + f.web.ID + "/instances/" + staging.ID
	for _, tc := range []struct {
		method, path, body string
		want               int
	}{
		{"GET", prodInst, "", 200},
		{"PATCH", prodInst, `{"variables_patch":{"A":"1"}}`, 200},
		{"GET", stagingInst, "", 404},
		{"PATCH", stagingInst, `{"variables_patch":{"A":"1"}}`, 404},
		{"GET", "/api/environments/" + staging.ID + "/canvas", "", 404},
		{"GET", "/api/projects/" + f.p.ID, "", 200},
		{"PATCH", "/api/projects/" + f.p.ID, `{"name":"x"}`, 403},
		{"POST", "/api/projects/" + f.p.ID + "/services", `{"source_type":"image","image":"nginx"}`, 403},
		{"PATCH", "/api/services/" + f.web.ID, `{"name":"x"}`, 403},
		{"POST", "/api/projects", `{"name":"nope"}`, 404},
		{"GET", "/api/users", "", 403},
		{"POST", "/api/tokens", `{"name":"x"}`, 403},
		{"GET", "/api/auth/sessions", "", 403},
	} {
		if got := req(h, tc.method, tc.path, tc.body, tk).Code; got != tc.want {
			t.Errorf("env token %s %s: %d, want %d", tc.method, tc.path, got, tc.want)
		}
	}
	if ps := decodeBody[[]store.Project](t, req(h, "GET", "/api/projects", "", tk)); len(ps) != 1 || ps[0].ID != f.p.ID {
		t.Errorf("env token projects: %+v", ps)
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", tk))
	if me["auth"] != "token" || me["workspace_admin"] != false {
		t.Errorf("me via token: %v", me)
	}
	// 토큰 역할은 만든 사람의 권한을 넘지 못한다.
	viewer := testUser(t, f.st, "tok-viewer@example.com", store.RoleViewer)
	f.st.SetMember(ctx, f.p.ID, viewer.ID, store.RoleViewer)
	vc := withCookie(testSessionCookie(t, f.srv, viewer))
	if w := req(h, "POST", "/api/tokens", fmt.Sprintf(`{"name":"x","project_id":%q,"role":"member"}`, f.p.ID), vc); w.Code != 403 {
		t.Errorf("viewer creating a member token: %d", w.Code)
	}
	if w := req(h, "POST", "/api/tokens", `{"name":"x","role":"member"}`, vc); w.Code != 403 {
		t.Errorf("workspace viewer creating a member workspace token: %d", w.Code)
	}
	if w := req(h, "POST", "/api/tokens", `{"name":"x","project_id":"prj_nope","role":"viewer"}`, vc); w.Code != 404 {
		t.Errorf("token for a missing project: %d", w.Code)
	}
	w = req(h, "POST", "/api/tokens", fmt.Sprintf(`{"name":"ro","project_id":%q,"role":"viewer"}`, f.p.ID), vc)
	if w.Code != 201 {
		t.Fatalf("viewer token: %d %s", w.Code, w.Body)
	}
	ro := withBearer(decodeBody[struct {
		Token string `json:"token"`
	}](t, w).Token)
	if got := req(h, "PATCH", prodInst, `{"variables_patch":{"A":"2"}}`, ro).Code; got != 403 {
		t.Errorf("viewer token write: %d", got)
	}
	if got := req(h, "GET", stagingInst, "", ro).Code; got != 200 {
		t.Errorf("project token read other env: %d", got)
	}
	// Instance demotion does not revoke workspace membership; a workspace demotion does.
	f.st.UpdateUser(ctx, &store.User{ID: owner.ID, Name: owner.Name, Role: store.RoleViewer, PasswordHash: owner.PasswordHash})
	if got := req(h, "PATCH", prodInst, `{"variables_patch":{"A":"3"}}`, tk).Code; got != 200 {
		t.Errorf("instance role changed workspace access: %d", got)
	}
	if err := f.st.SetWorkspaceMember(ctx, store.DefaultWorkspaceID, owner.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if got := req(h, "PATCH", prodInst, `{"variables_patch":{"A":"3"}}`, tk).Code; got != 404 {
		t.Errorf("token after workspace access revoked: %d", got)
	}
	f.st.UpdateUser(ctx, &store.User{ID: owner.ID, Name: owner.Name, Role: store.RoleOwner, PasswordHash: owner.PasswordHash})

	// 폐기
	if w := req(h, "DELETE", "/api/tokens/"+created.ID, "", vc); w.Code != 404 {
		t.Errorf("someone else's token revoke: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/tokens/"+created.ID, "", oc); w.Code != 204 {
		t.Errorf("revoke: %d", w.Code)
	}
	if got := req(h, "GET", prodInst, "", tk).Code; got != 401 {
		t.Errorf("revoked token: %d", got)
	}
}

func TestViewerVariableMasking(t *testing.T) {
	f, h := authServer(t)
	ctx := f.ctx
	in, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	in.Variables = map[string]string{"SECRET": "hunter2", "REF": "${{ shared.REGION }}"}
	f.st.UpdateInstanceConfig(ctx, in)
	f.st.SetSharedVars(ctx, f.prod.ID, map[string]string{"REGION": "eu-1"})
	hook := "https://discord.example/api/webhooks/123/secret"
	f.p.NotifyWebhookURL = &hook
	f.st.UpdateProject(ctx, f.p)
	viewer := testUser(t, f.st, "v@example.com", store.RoleViewer)
	member := testUser(t, f.st, "m@example.com", store.RoleMember)
	f.st.SetMember(ctx, f.p.ID, viewer.ID, store.RoleViewer)
	f.st.SetMember(ctx, f.p.ID, member.ID, store.RoleMember)
	vc, mc := withCookie(testSessionCookie(t, f.srv, viewer)), withCookie(testSessionCookie(t, f.srv, member))
	inst := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID
	for _, tc := range []struct {
		path string
		auth func(*http.Request)
		has  []string
		not  []string
	}{
		{inst, vc, []string{`"SECRET":"•••"`, `"REF":"•••"`}, []string{"hunter2", "REGION"}},
		{inst, mc, []string{`"SECRET":"hunter2"`}, nil},
		{inst + "/variables/resolved", vc, []string{`"SECRET":{"value":"•••","source":"user"}`}, []string{"hunter2", "eu-1"}},
		{inst + "/variables/resolved", mc, []string{"hunter2", "eu-1"}, nil},
		{"/api/environments/" + f.prod.ID + "/shared-variables", vc, []string{`"REGION":"•••"`}, []string{"eu-1"}},
		{"/api/environments/" + f.prod.ID + "/canvas", vc, []string{`"SECRET":"•••"`}, []string{"hunter2", "discord.example"}},
		{"/api/projects/" + f.p.ID, vc, nil, []string{"discord.example"}},
		{"/api/projects", vc, nil, []string{"discord.example"}},
		{"/api/projects/" + f.p.ID, mc, []string{"discord.example"}, nil},
	} {
		w := req(h, "GET", tc.path, "", tc.auth)
		if w.Code != 200 {
			t.Errorf("%s: %d %s", tc.path, w.Code, w.Body)
			continue
		}
		body := w.Body.String()
		for _, s := range tc.has {
			if !strings.Contains(body, s) {
				t.Errorf("%s: missing %s in %s", tc.path, s, body)
			}
		}
		for _, s := range tc.not {
			if strings.Contains(body, s) {
				t.Errorf("%s: leaked %s", tc.path, s)
			}
		}
	}
	for _, p := range []string{inst + "/exec", inst + "/data/tables"} {
		if got := req(h, "GET", p, "", vc).Code; got != 403 {
			t.Errorf("viewer %s: %d", p, got)
		}
	}
	// 멤버 변경 이벤트에는 actor가 붙는다.
	if w := req(h, "PATCH", inst, `{"variables_patch":{"NEW":"1"}}`, mc); w.Code != 200 {
		t.Fatalf("member patch: %d %s", w.Code, w.Body)
	}
	evs, _ := f.st.ListEvents(ctx, f.p.ID, "", 20)
	found := false
	for _, e := range evs {
		if e.Kind == "variables.updated" && e.Actor != nil && *e.Actor == "m" {
			found = true
		}
	}
	if !found {
		t.Error("variables.updated without actor")
	}
}

func TestProjectMembersAPI(t *testing.T) {
	f, h := authServer(t)
	pa := testUser(t, f.st, "pa@example.com", store.RoleMember)
	other := testUser(t, f.st, "other@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, pa.ID, store.RoleAdmin)
	pc := withCookie(testSessionCookie(t, f.srv, pa))
	oc := withCookie(testSessionCookie(t, f.srv, other))
	path := "/api/projects/" + f.p.ID + "/members/" + other.ID
	if w := req(h, "GET", "/api/projects/"+f.p.ID, "", oc); w.Code != 404 {
		t.Errorf("non-member sees project: %d", w.Code)
	}
	if w := req(h, "PUT", path, `{"role":"owner"}`, pc); w.Code != 400 {
		t.Errorf("invalid project role: %d", w.Code)
	}
	if w := req(h, "PUT", path, `{"role":"member"}`, pc); w.Code != 200 {
		t.Fatalf("add member: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/projects/"+f.p.ID, "", oc); w.Code != 200 {
		t.Errorf("member sees project: %d", w.Code)
	}
	ms := decodeBody[[]store.Member](t, req(h, "GET", "/api/projects/"+f.p.ID+"/members", "", oc))
	if len(ms) != 2 {
		t.Errorf("members: %+v", ms)
	}
	if w := req(h, "DELETE", "/api/projects/"+f.p.ID+"/members/"+pa.ID, "", oc); w.Code != 403 {
		t.Errorf("member removing admin: %d", w.Code)
	}
	if w := req(h, "DELETE", path, "", pc); w.Code != 204 {
		t.Errorf("remove: %d", w.Code)
	}
	if w := req(h, "GET", "/api/projects/"+f.p.ID, "", oc); w.Code != 404 {
		t.Errorf("removed member still sees project: %d", w.Code)
	}
	// 작업 공간 member가 만든 프로젝트는 만든 사람이 admin이 된다.
	w := req(h, "POST", "/api/projects", `{"name":"mine"}`, oc)
	if w.Code != 201 {
		t.Fatalf("member create project: %d %s", w.Code, w.Body)
	}
	np := decodeBody[store.Project](t, w)
	if role, _ := f.st.MemberRole(f.ctx, np.ID, other.ID); role != store.RoleAdmin {
		t.Errorf("creator role: %q", role)
	}
}
