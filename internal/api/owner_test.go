package api

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestOwnerRoutesAreOwnerOnly(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	ws, _ := freshAccount(t, f, "tenant@example.com", store.RoleMember)
	if _, err := f.st.CreateWorkspace(context.Background(), "Tenant", ws.ID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/owner", "/api/owner/sessions", "/api/owner/tokens", "/api/owner/backups"} {
		if w := req(h, "GET", path, "", ownerC); w.Code != 200 {
			t.Errorf("owner %s: %d %s", path, w.Code, w.Body)
		}
		if w := req(h, "GET", path, "", adminC); w.Code != 403 {
			t.Errorf("instance admin %s: %d", path, w.Code)
		}
	}
	// owner가 만든 instance 토큰도 owner 전용 기능은 쓸 수 없다(세션만).
	tok := decodeBody[map[string]any](t, req(h, "POST", "/api/tokens", `{"name":"ops","scope":"instance","role":"admin"}`, ownerC))
	if w := req(h, "GET", "/api/owner", "", withBearer(tok["token"].(string))); w.Code != 403 {
		t.Errorf("owner token: %d", w.Code)
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", ownerC))
	if me["instance_owner"] != true {
		t.Errorf("owner me: %v", me["instance_owner"])
	}
	me = decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", adminC))
	if me["instance_owner"] != false || len(me["admin_caps"].([]any)) != len(adminCaps) {
		t.Errorf("admin me: %v %v", me["instance_owner"], me["admin_caps"])
	}
}

func TestOwnerRestrictsAdminCapabilities(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	if w := req(h, "PUT", "/api/owner/admin-caps", `{"disabled":["nope"]}`, ownerC); w.Code != 400 {
		t.Fatalf("unknown cap: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/owner/admin-caps", `{"disabled":["users","audit"]}`, ownerC); w.Code != 200 {
		t.Fatalf("set caps: %d %s", w.Code, w.Body)
	}
	for _, tc := range []struct {
		path string
		want int
	}{
		{"/api/users", 403}, {"/api/invites", 403}, {"/api/audit", 403},
		{"/api/instance/overview", 200}, {"/api/instance/resource-limits", 200},
	} {
		if w := req(h, "GET", tc.path, "", adminC); w.Code != tc.want {
			t.Errorf("admin %s: %d, want %d", tc.path, w.Code, tc.want)
		}
		if w := req(h, "GET", tc.path, "", ownerC); w.Code != 200 {
			t.Errorf("owner %s: %d", tc.path, w.Code)
		}
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", adminC))
	caps := me["admin_caps"].([]any)
	if slices.Contains(caps, any("users")) || !slices.Contains(caps, any("nodes")) {
		t.Errorf("admin caps: %v", caps)
	}
}

func TestEveryInstanceRouteHasKnownCap(t *testing.T) {
	f, _ := authServer(t)
	for _, rt := range f.srv.Routes() {
		if rt.Access.Kind == accInstance && rt.Access.Cap != "" && !validAdminCap(rt.Access.Cap) {
			t.Errorf("%s: unknown cap %q", rt.Pattern, rt.Access.Cap)
		}
	}
	if adminCapFor("GET /api/users/{uid}") != "users" || adminCapFor("GET /api/usersx") != "" || adminCapFor("GET /api/instance/overview") != "" {
		t.Error("adminCapFor prefix matching")
	}
}

func TestMaintenanceMode(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	member, memberC := freshAccount(t, f, "dev@example.com", store.RoleMember)
	ws, err := f.st.CreateWorkspace(context.Background(), "Dev", member.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"name":"p1","workspace_id":%q}`, ws.ID)
	if w := req(h, "PUT", "/api/owner/maintenance", `{"enabled":true,"message":"DB 이전 중","block_logins":true}`, ownerC); w.Code != 200 {
		t.Fatalf("enable: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", "/api/projects", body, memberC); w.Code != 503 {
		t.Fatalf("member write during maintenance: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/projects", "", memberC); w.Code != 200 {
		t.Fatalf("member read during maintenance: %d", w.Code)
	}
	if w := req(h, "POST", "/api/notifications/read", `{}`, memberC); w.Code == 503 {
		t.Fatalf("exempt route blocked")
	}
	if w := req(h, "POST", "/api/projects", `{"name":"owner-p"}`, ownerC); w.Code != 201 {
		t.Fatalf("owner write during maintenance: %d %s", w.Code, w.Body)
	}
	if w := req(h, "POST", "/api/projects", `{"name":"legacy-p"}`, withBearer("legacy-token")); w.Code != 201 {
		t.Fatalf("KEEL_API_TOKEN during maintenance: %d %s", w.Code, w.Body)
	}
	n := decodeBody[map[string]any](t, req(h, "GET", "/api/instance/notice", "", memberC))
	if m := n["maintenance"].(map[string]any); m["enabled"] != true || m["message"] != "DB 이전 중" || n["exempt"] != false {
		t.Fatalf("notice: %v", n)
	}
	// 로그인 차단: owner가 아닌 계정은 새 세션을 만들 수 없다(기본 owner 계정은 된다).
	if err := f.srv.startSession(context.Background(), nil, nil, member); err != errMaintenanceLogin {
		t.Fatalf("member login: %v", err)
	}
	if w := req(h, "POST", "/api/auth/login", `{"password":"password123"}`, nil); w.Code != 204 {
		t.Fatalf("builtin owner login during maintenance: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PUT", "/api/owner/maintenance", `{"enabled":false}`, ownerC); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := req(h, "POST", "/api/projects", body, memberC); w.Code != 201 {
		t.Fatalf("member write after maintenance: %d %s", w.Code, w.Body)
	}
}

func TestAnnouncement(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, memberC := freshAccount(t, f, "dev@example.com", store.RoleMember)
	if w := req(h, "PUT", "/api/owner/announcement", `{"enabled":true,"message":"","level":"info"}`, ownerC); w.Code != 400 {
		t.Fatalf("empty: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/owner/announcement", `{"enabled":true,"message":"x","level":"loud"}`, ownerC); w.Code != 400 {
		t.Fatalf("level: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/owner/announcement", `{"enabled":true,"message":"오늘 밤 점검","level":"warning"}`, ownerC); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	n := decodeBody[map[string]any](t, req(h, "GET", "/api/instance/notice", "", memberC))
	if a := n["announcement"].(map[string]any); a["message"] != "오늘 밤 점검" || a["level"] != "warning" {
		t.Fatalf("notice: %v", n)
	}
}

func TestOwnerRevokesSessionsAndTokens(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	owner, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	admin, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	_, memberC := freshAccount(t, f, "dev@example.com", store.RoleMember)
	tok := decodeBody[map[string]any](t, req(h, "POST", "/api/tokens", `{"name":"ops","scope":"instance","role":"admin"}`, adminC))
	ownerTok := decodeBody[map[string]any](t, req(h, "POST", "/api/tokens", `{"name":"mine","scope":"instance","role":"admin"}`, ownerC))

	all := decodeBody[[]map[string]any](t, req(h, "GET", "/api/owner/tokens", "", ownerC))
	if len(all) != 2 {
		t.Fatalf("tokens: %v", all)
	}
	ses := decodeBody[[]map[string]any](t, req(h, "GET", "/api/owner/sessions", "", ownerC))
	if len(ses) != 3 {
		t.Fatalf("sessions: %d", len(ses))
	}
	if w := req(h, "POST", "/api/owner/tokens/revoke", `{"scope":"non_owners"}`, ownerC); w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/users", "", withBearer(tok["token"].(string))); w.Code != 401 {
		t.Errorf("revoked admin token still works: %d", w.Code)
	}
	if w := req(h, "GET", "/api/users", "", withBearer(ownerTok["token"].(string))); w.Code != 200 {
		t.Errorf("owner token revoked: %d", w.Code)
	}
	if w := req(h, "POST", "/api/owner/sessions/revoke", `{"user_id":"`+admin.ID+`"}`, ownerC); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", adminC); w.Code != 401 {
		t.Errorf("admin session survived: %d", w.Code)
	}
	if w := req(h, "GET", "/api/auth/me", "", memberC); w.Code != 200 {
		t.Errorf("member session revoked: %d", w.Code)
	}
	// 지금 쓰는 자기 세션은 개별 삭제로 끊지 않는다.
	ses = decodeBody[[]map[string]any](t, req(h, "GET", "/api/owner/sessions", "", ownerC))
	for _, se := range ses {
		if se["current"] == true {
			if w := req(h, "DELETE", "/api/owner/sessions/"+se["id"].(string), "", ownerC); w.Code != 400 {
				t.Errorf("delete own current session: %d", w.Code)
			}
		}
	}
	if n, _ := f.st.ListSessions(ctx, owner.ID); len(n) != 1 {
		t.Errorf("owner sessions: %d", len(n))
	}
}

func TestLockdown(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	_, memberC := freshAccount(t, f, "dev@example.com", store.RoleMember)
	if w := req(h, "POST", "/api/owner/lockdown", `{}`, adminC); w.Code != 403 {
		t.Fatalf("admin lockdown: %d", w.Code)
	}
	res := decodeBody[map[string]any](t, req(h, "POST", "/api/owner/lockdown", `{"message":"침해 대응"}`, ownerC))
	if res["sessions_revoked"].(float64) != 2 {
		t.Fatalf("lockdown: %v", res)
	}
	for _, c := range []func(*http.Request){adminC, memberC} {
		if w := req(h, "GET", "/api/auth/me", "", c); w.Code != 401 {
			t.Errorf("session survived lockdown: %d", w.Code)
		}
	}
	m := f.srv.maintenance(context.Background())
	if !m.Enabled || !m.BlockLogins || !m.PauseWebhooks || m.Message != "침해 대응" {
		t.Fatalf("maintenance after lockdown: %+v", m)
	}
	if w := req(h, "GET", "/api/owner", "", ownerC); w.Code != 200 {
		t.Fatalf("owner after lockdown: %d", w.Code)
	}
}

func TestTransferOwnership(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	owner, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	heir, heirC := freshAccount(t, f, "heir@example.com", store.RoleMember)
	off, _ := freshAccount(t, f, "off@example.com", store.RoleMember)
	off.Disabled = true
	if err := f.st.UpdateUser(ctx, off); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "POST", "/api/owner/transfer", `{"user_id":"`+owner.ID+`"}`, ownerC); w.Code != 400 {
		t.Errorf("self transfer: %d", w.Code)
	}
	if w := req(h, "POST", "/api/owner/transfer", `{"user_id":"`+off.ID+`"}`, ownerC); w.Code != 409 {
		t.Errorf("disabled transfer: %d", w.Code)
	}
	res := decodeBody[map[string]any](t, req(h, "POST", "/api/owner/transfer", `{"user_id":"`+heir.ID+`"}`, ownerC))
	if res["still_owner"] != false {
		t.Fatalf("transfer: %v", res)
	}
	if u, _ := f.st.GetUser(ctx, owner.ID); u.Role != store.RoleAdmin {
		t.Errorf("old owner role: %s", u.Role)
	}
	if u, _ := f.st.GetUser(ctx, heir.ID); u.Role != store.RoleOwner {
		t.Errorf("heir role: %s", u.Role)
	}
	if w := req(h, "GET", "/api/owner", "", ownerC); w.Code != 403 {
		t.Errorf("old owner still owner: %d", w.Code)
	}
	if w := req(h, "GET", "/api/owner", "", heirC); w.Code != 200 {
		t.Errorf("new owner: %d", w.Code)
	}
	// 남는 owner로 함께 운영하기.
	res = decodeBody[map[string]any](t, req(h, "POST", "/api/owner/transfer", `{"user_id":"`+owner.ID+`","stay_owner":true}`, heirC))
	if res["still_owner"] != true {
		t.Fatalf("co-owner: %v", res)
	}
	if n, _ := f.st.CountActiveOwners(ctx); n != 3 { // 기본 계정 + boss + heir
		t.Errorf("owners: %d", n)
	}
}

func TestKeelBackupsAndRestart(t *testing.T) {
	f, h := authServer(t)
	f.srv.Deploy.Cfg.DataDir = t.TempDir()
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	w := req(h, "POST", "/api/owner/backups", "", ownerC)
	if w.Code != 201 {
		t.Fatalf("backup: %d %s", w.Code, w.Body)
	}
	b := decodeBody[keelBackup](t, w)
	if b.Bytes == 0 {
		t.Fatalf("empty backup: %+v", b)
	}
	list := decodeBody[[]keelBackup](t, req(h, "GET", "/api/owner/backups", "", ownerC))
	if len(list) != 1 || list[0].Name != b.Name {
		t.Fatalf("list: %+v", list)
	}
	if w := req(h, "GET", "/api/owner/backups/"+b.Name, "", ownerC); w.Code != 200 || w.Body.Len() != int(b.Bytes) {
		t.Fatalf("download: %d %d", w.Code, w.Body.Len())
	}
	if w := req(h, "GET", "/api/owner/backups/..%2Fkeel.db", "", ownerC); w.Code != 400 && w.Code != 404 {
		t.Fatalf("traversal: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/owner/backups/"+b.Name, "", ownerC); w.Code != 204 {
		t.Fatalf("delete: %d", w.Code)
	}

	restarted := make(chan struct{})
	f.srv.restartFn = func() { close(restarted) }
	if w := req(h, "POST", "/api/owner/restart", "", ownerC); w.Code != 202 {
		t.Fatalf("restart: %d %s", w.Code, w.Body)
	}
	select {
	case <-restarted:
	case <-time.After(3 * time.Second):
		t.Fatal("restart not triggered")
	}
}
