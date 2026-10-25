package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func auditKinds(t *testing.T, st *store.Store, kind string) []*store.AuditEntry {
	t.Helper()
	entries, err := st.ListAudit(context.Background(), 200)
	if err != nil {
		t.Fatal(err)
	}
	var out []*store.AuditEntry
	for _, a := range entries {
		if a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

func TestOwnerAssignsSomeoneElsesGitHubConnection(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	_, bossC := freshAccount(t, f, "assign-boss@example.com", store.RoleOwner)
	alice, _ := freshAccount(t, f, "assign-alice@example.com", store.RoleViewer)
	carol, _ := freshAccount(t, f, "assign-carol@example.com", store.RoleViewer)
	padmin, padminC := freshAccount(t, f, "assign-padmin@example.com", store.RoleAdmin)
	proj, _ := f.st.GetProject(ctx, f.p.ID)
	f.st.SetWorkspaceMember(ctx, proj.WorkspaceID, padmin.ID, store.RoleMember)
	f.st.SetMember(ctx, f.p.ID, padmin.ID, store.RoleAdmin)
	if err := f.st.SetGitHubConnection(ctx, alice.ID, "gho_alice", "alice-gh", "repo"); err != nil {
		t.Fatal(err)
	}
	path := "/api/projects/" + f.p.ID + "/github"
	body := fmt.Sprintf(`{"user_id":%q}`, alice.ID)
	// 프로젝트 admin(서버 admin이라도)은 다른 사람의 연결을 지정할 수 없다.
	if w := req(h, "PUT", path, body, padminC); w.Code != 403 {
		t.Fatalf("project admin: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PUT", path, fmt.Sprintf(`{"user_id":%q}`, carol.ID), bossC); w.Code != 409 {
		t.Fatalf("unconnected user: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PUT", path, body, bossC); w.Code != 200 {
		t.Fatalf("owner assign: %d %s", w.Code, w.Body)
	}
	if uid, _ := f.st.ProjectGitHubUser(ctx, f.p.ID); uid != alice.ID {
		t.Fatalf("github user: %s", uid)
	}
	if len(auditKinds(t, f.st, "owner.github_assigned")) != 1 {
		t.Fatal("assignment not audited")
	}
	list := decodeBody[[]store.GitHubConnectionOwner](t, req(h, "GET", "/api/owner/github-connections", "", bossC))
	if len(list) != 1 || list[0].UserID != alice.ID || list[0].Login != "alice-gh" {
		t.Fatalf("connections: %+v", list)
	}
	if w := req(h, "GET", "/api/owner/github-connections", "", padminC); w.Code != 403 {
		t.Fatalf("admin lists connections: %d", w.Code)
	}
}

func TestOwnerForcesWorkspaceOwner(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	_, bossC := freshAccount(t, f, "force-boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "force-admin@example.com", store.RoleAdmin)
	alice, _ := freshAccount(t, f, "force-alice@example.com", store.RoleMember)
	bob, _ := freshAccount(t, f, "force-bob@example.com", store.RoleMember)
	team, err := f.st.CreateWorkspace(ctx, "Leaver team", alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/owner/workspaces/" + team.ID + "/owner"
	body := fmt.Sprintf(`{"user_id":%q,"previous":"remove"}`, bob.ID)
	if w := req(h, "PUT", path, body, adminC); w.Code != 403 {
		t.Fatalf("instance admin: %d", w.Code)
	}
	if w := req(h, "PUT", path, fmt.Sprintf(`{"user_id":%q,"previous":"nope"}`, bob.ID), bossC); w.Code != 400 {
		t.Fatalf("bad previous: %d", w.Code)
	}
	// 마지막 owner를 내보내는 것도 owner는 할 수 있다.
	if w := req(h, "PUT", path, body, bossC); w.Code != 200 {
		t.Fatalf("force team owner: %d %s", w.Code, w.Body)
	}
	if r, _ := f.st.WorkspaceRole(ctx, team.ID, bob); r != store.RoleOwner {
		t.Fatalf("bob role: %q", r)
	}
	if r, _ := f.st.WorkspaceRole(ctx, team.ID, alice); r != "" {
		t.Fatalf("alice still member: %q", r)
	}
	// 개인 작업 공간은 팀 작업 공간이 되고, 기존 주인은 admin으로 낮아진다.
	personal, err := f.st.EnsurePersonalWorkspace(ctx, alice)
	if err != nil {
		t.Fatal(err)
	}
	w := req(h, "PUT", "/api/owner/workspaces/"+personal.ID+"/owner", fmt.Sprintf(`{"user_id":%q,"previous":"demote"}`, bob.ID), bossC)
	if w.Code != 200 || decodeBody[map[string]any](t, w)["converted"] != true {
		t.Fatalf("force personal owner: %d %s", w.Code, w.Body)
	}
	got, _ := f.st.GetWorkspace(ctx, personal.ID)
	if got.Personal || got.OwnerID != nil {
		t.Fatalf("still personal: %+v", got)
	}
	if r, _ := f.st.WorkspaceRole(ctx, personal.ID, alice); r != store.RoleAdmin {
		t.Fatalf("alice role: %q", r)
	}
	// 기존 주인은 새 개인 작업 공간을 받는다.
	fresh, err := f.st.EnsurePersonalWorkspace(ctx, alice)
	if err != nil || fresh.ID == personal.ID {
		t.Fatalf("new personal workspace: %v %+v", err, fresh)
	}
	if len(auditKinds(t, f.st, "owner.workspace_owner_changed")) != 2 {
		t.Fatal("not audited")
	}
}

func TestOwnerHoldBlocksDeploysUntilReleased(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	boss, bossC := freshAccount(t, f, "hold-boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "hold-admin@example.com", store.RoleAdmin)
	deployPath := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID + "/deploy"
	proj, _ := f.st.GetProject(ctx, f.p.ID)
	for _, tc := range []struct{ scope, id string }{{store.HoldProject, f.p.ID}, {store.HoldWorkspace, proj.WorkspaceID}} {
		body := fmt.Sprintf(`{"scope":%q,"target_id":%q,"reason":"abuse report"}`, tc.scope, tc.id)
		if w := req(h, "POST", "/api/owner/holds", body, adminC); w.Code != 403 {
			t.Fatalf("%s: instance admin hold: %d", tc.scope, w.Code)
		}
		if w := req(h, "POST", "/api/owner/holds", body, bossC); w.Code != 200 {
			t.Fatalf("%s: hold: %d %s", tc.scope, w.Code, w.Body)
		}
		if w := req(h, "POST", "/api/owner/holds", body, bossC); w.Code != 409 {
			t.Fatalf("%s: double hold: %d", tc.scope, w.Code)
		}
		// owner 자신도 정지 중에는 배포할 수 없다.
		if w := req(h, "POST", deployPath, "", bossC); w.Code != 409 {
			t.Fatalf("%s: deploy while held: %d %s", tc.scope, w.Code, w.Body)
		}
		detail := decodeBody[map[string]any](t, req(h, "GET", "/api/projects/"+f.p.ID, "", bossC))
		if hold, _ := detail["owner_hold"].(map[string]any); hold == nil || hold["reason"] != "abuse report" || hold["scope"] != tc.scope {
			t.Fatalf("%s: owner_hold: %v", tc.scope, detail["owner_hold"])
		}
		if w := req(h, "DELETE", "/api/owner/holds/"+tc.scope+"/"+tc.id, "", bossC); w.Code != 200 {
			t.Fatalf("%s: release: %d %s", tc.scope, w.Code, w.Body)
		}
		if w := req(h, "POST", deployPath, "", bossC); w.Code != 202 {
			t.Fatalf("%s: deploy after release: %d %s", tc.scope, w.Code, w.Body)
		}
	}
	if len(auditKinds(t, f.st, "owner.hold_created")) != 2 || len(auditKinds(t, f.st, "owner.hold_released")) != 2 {
		t.Fatal("holds not audited")
	}
	// 대상이 지워지면 정지 기록도 지워진다.
	ws, err := f.st.CreateWorkspace(ctx, "Doomed", boss.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w := req(h, "POST", "/api/owner/holds", fmt.Sprintf(`{"scope":"workspace","target_id":%q}`, ws.ID), bossC); w.Code != 200 {
		t.Fatalf("hold empty ws: %d %s", w.Code, w.Body)
	}
	if err := f.st.DeleteWorkspace(ctx, ws.ID); err != nil {
		t.Fatal(err)
	}
	if hs, _ := f.st.ListOwnerHolds(ctx); len(hs) != 0 {
		t.Fatalf("orphan holds: %+v", hs)
	}
}

func TestOwnerViewAsUserIsReadOnlyAndExpires(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	boss, bossC := freshAccount(t, f, "view-boss@example.com", store.RoleOwner)
	_, otherOwnerC := freshAccount(t, f, "view-boss2@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "view-admin@example.com", store.RoleAdmin)
	alice, _ := freshAccount(t, f, "view-alice@example.com", store.RoleMember)
	start := fmt.Sprintf(`{"user_id":%q}`, alice.ID)
	if w := req(h, "POST", "/api/owner/view-as", start, adminC); w.Code != 403 {
		t.Fatalf("admin view-as: %d", w.Code)
	}
	if w := req(h, "POST", "/api/owner/view-as", fmt.Sprintf(`{"user_id":%q}`, boss.ID), otherOwnerC); w.Code != 400 {
		t.Fatalf("view another owner: %d", w.Code)
	}
	if w := req(h, "POST", "/api/owner/view-as", start, bossC); w.Code != 200 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", bossC))
	if me["id"] != alice.ID || me["view_as"] == nil || me["instance_owner"] == true {
		t.Fatalf("me while viewing: %v", me)
	}
	// 읽기 전용: 쓰기, 웹소켓, owner 전용 라우트는 막힌다.
	if w := req(h, "POST", "/api/projects", `{"name":"x"}`, bossC); w.Code != 403 {
		t.Fatalf("write while viewing: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/services/"+f.web.ID+"/instances/"+f.prod.ID+"/exec", "", func(r *http.Request) {
		bossC(r)
		r.Header.Set("Upgrade", "websocket")
	}); w.Code != 403 {
		t.Fatalf("websocket while viewing: %d", w.Code)
	}
	if w := req(h, "GET", "/api/owner", "", bossC); w.Code != 403 {
		t.Fatalf("owner route while viewing: %d", w.Code)
	}
	if w := req(h, "POST", "/api/auth/view-as/stop", "", bossC); w.Code != 204 {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	if me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", bossC)); me["id"] != boss.ID || me["view_as"] != nil {
		t.Fatalf("me after stop: %v", me)
	}
	if len(auditKinds(t, f.st, "owner.view_as_started")) != 1 || len(auditKinds(t, f.st, "owner.view_as_ended")) != 1 {
		t.Fatal("view-as not audited")
	}
	// 기한이 지나면 자동으로 끝난다.
	if w := req(h, "POST", "/api/owner/view-as", start, bossC); w.Code != 200 {
		t.Fatalf("restart: %d", w.Code)
	}
	ses, err := f.st.ListSessions(ctx, boss.ID)
	if err != nil || len(ses) == 0 {
		t.Fatalf("sessions: %v", err)
	}
	for _, se := range ses {
		f.st.SetSessionViewAs(ctx, se.ID, alice.ID, time.Now().Add(-time.Minute))
	}
	if me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", bossC)); me["id"] != boss.ID {
		t.Fatalf("expired view-as still active: %v", me["id"])
	}
}
