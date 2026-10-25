package api

import (
	"context"
	"net/http"
	"testing"

	"github.com/turin-dev/aven/internal/secret"
	"github.com/turin-dev/aven/internal/store"
)

type wsView struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Personal bool   `json:"personal"`
	Default  bool   `json:"default"`
	Role     string `json:"role"`
}

func personalOf(t *testing.T, ws []wsView) wsView {
	t.Helper()
	for _, w := range ws {
		if w.Personal {
			return w
		}
	}
	t.Fatalf("no personal workspace in %+v", ws)
	return wsView{}
}

// 사람마다 개인 작업 공간이 있고, 다른 사용자는 남의 개인 작업 공간 프로젝트를 볼 수 없다(인스턴스 owner는 예외).
func TestWorkspaceIsolation(t *testing.T) {
	f, h := authServer(t)
	st := f.st
	ctx := context.Background()
	owner := testUser(t, st, "owner2@example.com", store.RoleOwner) // 인스턴스 owner
	alice := testUser(t, st, "alice@example.com", store.RoleViewer) // 기본 작업 공간에서는 viewer
	bob := testUser(t, st, "bob@example.com", store.RoleMember)
	ownerC := withCookie(testSessionCookie(t, f.srv, owner))
	aliceC := withCookie(testSessionCookie(t, f.srv, alice))
	bobC := withCookie(testSessionCookie(t, f.srv, bob))

	// 목록: 기본 + 자기 개인 작업 공간(처음 볼 때 만든다)
	w := req(h, "GET", "/api/workspaces", "", aliceC)
	aws := decodeBody[[]wsView](t, w)
	if len(aws) != 2 || !aws[0].Default || aws[0].Role != store.RoleViewer {
		t.Fatalf("alice workspaces: %s", w.Body.String())
	}
	ap := personalOf(t, aws)
	if ap.Role != store.RoleOwner {
		t.Fatalf("alice personal role: %+v", ap)
	}
	bp := personalOf(t, decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", bobC)))
	if bp.ID == ap.ID {
		t.Fatal("each person needs their own workspace")
	}

	// 기본 작업 공간 viewer여도 개인 작업 공간에는 프로젝트를 만든다. 기본 작업 공간에는 못 만든다.
	if w := req(h, "POST", "/api/projects", `{"name":"api"}`, aliceC); w.Code != 403 {
		t.Fatalf("viewer in default workspace: %d %s", w.Code, w.Body.String())
	}
	w = req(h, "POST", "/api/projects", `{"name":"api","workspace_id":"`+ap.ID+`"}`, aliceC)
	if w.Code != 201 {
		t.Fatalf("alice create: %d %s", w.Code, w.Body.String())
	}
	alicePrj := decodeBody[store.Project](t, w)
	if alicePrj.WorkspaceID != ap.ID {
		t.Fatalf("workspace_id: %+v", alicePrj)
	}
	// 같은 이름도 작업 공간이 다르면 된다. 같은 작업 공간에서는 409.
	if w := req(h, "POST", "/api/projects", `{"name":"api","workspace_id":"`+bp.ID+`"}`, bobC); w.Code != 201 {
		t.Fatalf("bob same name: %d %s", w.Code, w.Body.String())
	}
	if w := req(h, "POST", "/api/projects", `{"name":"api","workspace_id":"`+ap.ID+`"}`, aliceC); w.Code != 409 {
		t.Fatalf("duplicate in one workspace: %d", w.Code)
	}
	// 남의 작업 공간에는 만들 수 없고, 있는지도 알 수 없다.
	if w := req(h, "POST", "/api/projects", `{"name":"x","workspace_id":"`+ap.ID+`"}`, bobC); w.Code != 404 {
		t.Fatalf("bob into alice workspace: %d", w.Code)
	}

	// 다른 사용자는 alice 프로젝트를 볼 수 없다. 인스턴스 owner는 프로젝트를 열람할 수 있지만 작업 공간 멤버는 아니다.
	if w := req(h, "GET", "/api/projects/"+alicePrj.ID, "", bobC); w.Code != 404 {
		t.Errorf("bob reads alice's project: %d", w.Code)
	}
	for _, p := range decodeBody[[]store.Project](t, req(h, "GET", "/api/projects", "", bobC)) {
		if p.ID == alicePrj.ID {
			t.Errorf("bob lists alice's project")
		}
	}
	if w := req(h, "GET", "/api/projects/"+alicePrj.ID, "", ownerC); w.Code != 200 {
		t.Errorf("instance owner reads alice's project: %d", w.Code)
	}
	listed := false
	for _, p := range decodeBody[[]store.Project](t, req(h, "GET", "/api/projects", "", ownerC)) {
		listed = listed || p.ID == alicePrj.ID
	}
	if !listed {
		t.Errorf("instance owner does not list alice's project")
	}
	if w := req(h, "GET", "/api/workspaces/"+ap.ID+"/members", "", bobC); w.Code != 404 {
		t.Errorf("bob reads alice's members: %d", w.Code)
	}
	if w := req(h, "GET", "/api/workspaces/"+ap.ID+"/members", "", ownerC); w.Code != 200 {
		t.Errorf("instance owner reads alice's members: %d", w.Code)
	}
	if w := req(h, "GET", "/api/projects/"+alicePrj.ID, "", aliceC); w.Code != 200 {
		t.Fatalf("alice reads own project: %d", w.Code)
	}
	if got := decodeBody[[]store.Project](t, req(h, "GET", "/api/projects?workspace="+ap.ID, "", aliceC)); len(got) != 1 || got[0].ID != alicePrj.ID {
		t.Fatalf("workspace filter: %+v", got)
	}
	// KEEL_API_TOKEN(운영 자동화)은 전부 본다.
	if w := req(h, "GET", "/api/projects/"+alicePrj.ID, "", withBearer("legacy-token")); w.Code != 200 {
		t.Fatalf("env token: %d", w.Code)
	}

	// alice가 bob을 개인 작업 공간에 member로 초대: 작업 공간 member는 프로젝트 멤버로 받은 프로젝트만 본다(기본 작업 공간과 같은 규칙).
	if w := req(h, "POST", "/api/workspaces/"+ap.ID+"/members", `{"email":"bob@example.com","role":"owner"}`, bobC); w.Code != 404 {
		t.Fatalf("non-member invites: %d", w.Code)
	}
	if w := req(h, "POST", "/api/workspaces/"+ap.ID+"/members", `{"email":"bob@example.com","role":"member"}`, aliceC); w.Code != 201 {
		t.Fatalf("invite bob: %d %s", w.Code, w.Body.String())
	}
	if w := req(h, "GET", "/api/projects/"+alicePrj.ID, "", bobC); w.Code != 404 {
		t.Fatalf("workspace member without project membership: %d", w.Code)
	}
	if w := req(h, "POST", "/api/projects", `{"name":"bobs","workspace_id":"`+ap.ID+`"}`, bobC); w.Code != 201 {
		t.Fatalf("workspace member creates: %d %s", w.Code, w.Body.String())
	}
	// admin으로 올리면 그 공간의 모든 프로젝트가 보인다.
	if w := req(h, "PATCH", "/api/workspaces/"+ap.ID+"/members/"+bob.ID, `{"role":"admin"}`, aliceC); w.Code != 200 {
		t.Fatalf("promote: %d %s", w.Code, w.Body.String())
	}
	if w := req(h, "GET", "/api/projects/"+alicePrj.ID, "", bobC); w.Code != 200 {
		t.Fatalf("workspace admin: %d", w.Code)
	}
	// admin은 owner를 바꾸거나 뺄 수 없다. 개인 작업 공간 주인은 뺄 수 없다.
	if w := req(h, "DELETE", "/api/workspaces/"+ap.ID+"/members/"+alice.ID, "", bobC); w.Code != 403 {
		t.Fatalf("admin removes owner: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/workspaces/"+ap.ID+"/members/"+alice.ID, "", aliceC); w.Code != 400 {
		t.Fatalf("personal owner leaves: %d", w.Code)
	}
	// bob이 나가면 다시 안 보인다(직접 받은 프로젝트 멤버십도 정리된다).
	if w := req(h, "DELETE", "/api/workspaces/"+ap.ID+"/members/"+bob.ID, "", bobC); w.Code != 204 {
		t.Fatalf("bob leaves: %d", w.Code)
	}
	for _, p := range decodeBody[[]store.Project](t, req(h, "GET", "/api/projects", "", bobC)) {
		if p.WorkspaceID == ap.ID {
			t.Fatalf("bob still sees %s after leaving", p.Name)
		}
	}

	// 기본 작업 공간 프로젝트는 예전 그대로: 인스턴스 owner/admin은 전부 본다.
	if w := req(h, "GET", "/api/projects/"+f.p.ID, "", ownerC); w.Code != 200 {
		t.Fatalf("default workspace project for instance owner: %d", w.Code)
	}

	// 이동: 프로젝트 admin + 대상 작업 공간 member 이상(인스턴스 owner는 어디든 된다).
	if w := req(h, "PUT", "/api/projects/"+f.p.ID+"/workspace", `{"workspace_id":"`+ap.ID+`"}`, bobC); w.Code != 404 {
		t.Fatalf("move by a non-member: %d", w.Code)
	}
	op := personalOf(t, decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", ownerC)))
	if w := req(h, "PUT", "/api/projects/"+f.p.ID+"/workspace", `{"workspace_id":"`+op.ID+`"}`, ownerC); w.Code != 200 {
		t.Fatalf("move: %d %s", w.Code, w.Body.String())
	}
	admin := testUser(t, st, "admin3@example.com", store.RoleAdmin)
	if w := req(h, "GET", "/api/projects/"+f.p.ID, "", withCookie(testSessionCookie(t, f.srv, admin))); w.Code != 404 {
		t.Fatalf("instance admin sees a project moved to a personal workspace: %d", w.Code)
	}

	// 팀 작업 공간: 누구나 만들고, 빈 공간만 지운다. 기본·개인 작업 공간은 지울 수 없다.
	w = req(h, "POST", "/api/workspaces", `{"name":"Side project"}`, aliceC)
	if w.Code != 201 {
		t.Fatalf("create team: %d", w.Code)
	}
	team := decodeBody[wsView](t, w)
	if w := req(h, "DELETE", "/api/workspaces/"+ap.ID, "", aliceC); w.Code != 400 {
		t.Fatalf("delete personal: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/workspaces/"+store.DefaultWorkspaceID, "", ownerC); w.Code != 400 {
		t.Fatalf("delete default: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/workspaces/"+team.ID, "", aliceC); w.Code != 204 {
		t.Fatalf("delete team: %d", w.Code)
	}

	// 개인 작업 공간에 프로젝트가 남은 사용자는 지울 수 없다.
	if w := req(h, "DELETE", "/api/users/"+alice.ID, "", ownerC); w.Code != 409 {
		t.Fatalf("delete user with projects: %d %s", w.Code, w.Body.String())
	}
	_ = ctx
}

// 프로젝트는 만든 사람의 GitHub 연결을 쓰고, 프로젝트 admin은 자기 연결로만 바꿀 수 있다.
func TestProjectGitHubAccount(t *testing.T) {
	f, h := authServer(t)
	st := f.st
	box, err := secret.LoadOrCreate(t.TempDir() + "/master.key")
	if err != nil {
		t.Fatal(err)
	}
	st.SetSecretBox(box)
	ctx := context.Background()
	alice := testUser(t, st, "alice@example.com", store.RoleMember)
	bob := testUser(t, st, "bob@example.com", store.RoleMember)
	aliceC := withCookie(testSessionCookie(t, f.srv, alice))
	bobC := withCookie(testSessionCookie(t, f.srv, bob))
	if err := st.SetGitHubConnection(ctx, alice.ID, "gho_alice", "alice-gh", "repo"); err != nil {
		t.Fatal(err)
	}
	w := req(h, "POST", "/api/projects", `{"name":"web"}`, aliceC)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	p := decodeBody[store.Project](t, w)
	if p.GitHubUserID == nil || *p.GitHubUserID != alice.ID || f.mgr.ProjectGitHubToken(ctx, p.ID) != "gho_alice" {
		t.Fatalf("creator's connection: %+v", p)
	}
	detail := decodeBody[map[string]any](t, req(h, "GET", "/api/projects/"+p.ID, "", aliceC))
	if acc, _ := detail["github_account"].(map[string]any); acc["login"] != "alice-gh" || acc["connected"] != true {
		t.Fatalf("github_account: %v", detail["github_account"])
	}
	// bob을 프로젝트 admin으로 넣어도 alice의 연결을 다른 사람에게 줄 수는 없고, bob은 연결이 있어야 자기 것으로 바꾼다.
	if err := st.SetMember(ctx, p.ID, bob.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "PUT", "/api/projects/"+p.ID+"/github", `{"user_id":"`+alice.ID+`"}`, bobC); w.Code != 403 {
		t.Fatalf("assign someone else's connection: %d", w.Code)
	}
	if w := req(h, "PUT", "/api/projects/"+p.ID+"/github", `{"user_id":"`+bob.ID+`"}`, bobC); w.Code != 409 {
		t.Fatalf("assign without a connection: %d", w.Code)
	}
	st.SetGitHubConnection(ctx, bob.ID, "gho_bob", "bob-gh", "repo")
	if w := req(h, "PUT", "/api/projects/"+p.ID+"/github", `{"user_id":"`+bob.ID+`"}`, bobC); w.Code != 200 {
		t.Fatalf("assign own: %d %s", w.Code, w.Body.String())
	}
	if got := f.mgr.ProjectGitHubToken(ctx, p.ID); got != "gho_bob" {
		t.Fatalf("after switch: %q", got)
	}
	if w := req(h, "PUT", "/api/projects/"+p.ID+"/github", `{"user_id":null}`, bobC); w.Code != 200 {
		t.Fatalf("clear: %d", w.Code)
	}
	if got := f.mgr.ProjectGitHubToken(ctx, p.ID); got != "" {
		t.Fatalf("after clear: %q", got)
	}
}

// KEEL_SUPERADMINS grants instance owner authority, and an instance owner is never blocked:
// owner of every workspace and admin of every project. Plain instance admins are not.
func TestSuperAdminRespectsWorkspaceMembership(t *testing.T) {
	f, h := authServer(t)
	f.srv.SuperAdmins = map[string]bool{"me@example.com": true}
	st := f.st
	boss := testUser(t, st, "Me@example.com", store.RoleMember)
	alice := testUser(t, st, "alice@example.com", store.RoleMember)
	admin := testUser(t, st, "admin9@example.com", store.RoleAdmin)
	bossC := withCookie(testSessionCookie(t, f.srv, boss))
	aliceC := withCookie(testSessionCookie(t, f.srv, alice))
	adminC := withCookie(testSessionCookie(t, f.srv, admin))
	ap := personalOf(t, decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", aliceC)))
	w := req(h, "POST", "/api/projects", `{"name":"secret","workspace_id":"`+ap.ID+`"}`, aliceC)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	prj := decodeBody[store.Project](t, w)

	listed := ""
	for _, ws := range decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", bossC)) {
		if ws.ID == ap.ID {
			listed = ws.Role
		}
	}
	if listed != store.RoleOwner {
		t.Fatalf("instance owner lists another workspace as owner: %q", listed)
	}
	if w := req(h, "GET", "/api/projects/"+prj.ID, "", bossC); w.Code != 200 {
		t.Fatalf("instance owner reads project: %d", w.Code)
	}
	if role := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", bossC))["projects"].(map[string]any)[prj.ID]; role != store.RoleAdmin {
		t.Fatalf("instance owner project role: %v", role)
	}
	if w := req(h, "PATCH", "/api/projects/"+prj.ID, `{"description":"edited by owner"}`, bossC); w.Code != 200 {
		t.Fatalf("instance owner edits: %d %s", w.Code, w.Body.String())
	}
	if w := req(h, "POST", "/api/projects", `{"name":"boss-made","workspace_id":"`+ap.ID+`"}`, bossC); w.Code != 201 {
		t.Fatalf("instance owner creates in any workspace: %d %s", w.Code, w.Body.String())
	}
	w = req(h, "POST", "/api/projects", `{"name":"doomed","workspace_id":"`+ap.ID+`"}`, aliceC)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	doomed := decodeBody[store.Project](t, w)
	// 삭제 자체는 deploy 매니저가 필요하므로 권한 검사만 본다(통과하면 299).
	f.srv.authzProbe = true
	ph := f.srv.Handler()
	if w := req(ph, "DELETE", "/api/projects/"+doomed.ID, "", adminC); w.Code != 404 {
		t.Fatalf("plain instance admin deletes: %d", w.Code)
	}
	if w := req(ph, "DELETE", "/api/projects/"+doomed.ID, "", aliceC); w.Code != probeStatus {
		t.Fatalf("project admin deletes: %d", w.Code)
	}
	if w := req(ph, "DELETE", "/api/projects/"+doomed.ID, "", bossC); w.Code != probeStatus {
		t.Fatalf("instance owner deletes: %d %s", w.Code, w.Body.String())
	}
	f.srv.authzProbe = false
	if w := req(h, "GET", "/api/workspaces/"+ap.ID+"/members", "", bossC); w.Code != 200 {
		t.Fatalf("instance owner reads members: %d", w.Code)
	}
	if w := req(h, "GET", "/api/users", "", bossC); w.Code != 200 {
		t.Fatalf("superadmin instance operations: %d", w.Code)
	}

	if w := req(h, "GET", "/api/projects/"+prj.ID, "", adminC); w.Code != 404 {
		t.Fatalf("plain instance admin: %d", w.Code)
	}
	// 비활성화되면 권한이 사라진다.
	boss.Disabled = true
	st.UpdateUser(context.Background(), boss)
	if w := req(h, "GET", "/api/projects/"+prj.ID, "", bossC); w.Code == 200 {
		t.Fatalf("disabled superadmin: %d", w.Code)
	}
}

// 서버 owner는 멤버가 아닌 프로젝트에서도 배포할 수 있지만, 다른 사람의 GitHub 연결을 쓰게 되면 경고 플래그와 감사 기록이 남는다.
func TestOwnerBorrowedGitHubIsFlaggedAndAudited(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	boss, bossC := freshAccount(t, f, "gh-boss@example.com", store.RoleOwner)
	alice, aliceC := freshAccount(t, f, "gh-alice@example.com", store.RoleViewer)
	proj, err := f.st.GetProject(ctx, f.p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetWorkspaceMember(ctx, proj.WorkspaceID, alice.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(ctx, f.p.ID, alice.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetGitHubConnection(ctx, alice.ID, "gho_alice", "alice-gh", "repo"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetProjectGitHubUser(ctx, f.p.ID, &alice.ID); err != nil {
		t.Fatal(err)
	}
	borrowed := func(c func(*http.Request)) bool {
		return decodeBody[map[string]any](t, req(h, "GET", "/api/projects/"+f.p.ID, "", c))["github_borrowed"] == true
	}
	if !borrowed(bossC) || borrowed(aliceC) {
		t.Fatalf("github_borrowed: boss=%v alice=%v", borrowed(bossC), borrowed(aliceC))
	}
	count := func() int {
		entries, err := f.st.ListAudit(ctx, 100)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, a := range entries {
			if a.Kind == "project.github_borrowed" && a.ActorID == boss.ID && a.Target == f.p.ID {
				n++
			}
		}
		return n
	}
	deployPath := "/api/services/" + f.web.ID + "/instances/" + f.prod.ID + "/deploy"
	if w := req(h, "POST", deployPath, "", bossC); w.Code != 202 {
		t.Fatalf("owner deploy is allowed: %d %s", w.Code, w.Body)
	}
	if count() != 1 {
		t.Fatalf("owner deploy with alice's GitHub connection not audited")
	}
	if w := req(h, "POST", deployPath, "", aliceC); w.Code != 202 || count() != 1 {
		t.Fatalf("connection owner's deploy: %d, audits %d", w.Code, count())
	}
	// 실제 멤버가 되면 팀이 함께 쓰는 연결이므로 경고하지 않는다.
	if err := f.st.SetWorkspaceMember(ctx, proj.WorkspaceID, boss.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetMember(ctx, f.p.ID, boss.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	if borrowed(bossC) {
		t.Fatal("member owner flagged")
	}
	if w := req(h, "POST", deployPath, "", bossC); w.Code != 202 || count() != 1 {
		t.Fatalf("member owner deploy: %d, audits %d", w.Code, count())
	}
}
