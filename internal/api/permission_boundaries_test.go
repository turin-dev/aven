package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func freshAccount(t *testing.T, f *ghFixture, email, role string) (*store.User, func(*http.Request)) {
	t.Helper()
	u := &store.User{Email: email, Name: email, Role: role}
	if err := f.st.CreateUser(context.Background(), u); err != nil {
		t.Fatal(err)
	}
	return u, withCookie(testSessionCookie(t, f.srv, u))
}

func TestInstanceAndWorkspaceBoundaries(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	admin, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	owner, ownerC := freshAccount(t, f, "tenant@example.com", store.RoleViewer)
	ws, err := f.st.CreateWorkspace(ctx, "Tenant", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	prj := &store.Project{Name: "private", WorkspaceID: ws.ID}
	if _, err := f.st.CreateProject(ctx, prj); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		auth func(*http.Request)
		want int
	}{
		{"/api/users", adminC, 200},
		{"/api/users", ownerC, 403},
		{"/api/projects/" + prj.ID, adminC, 404},
		{"/api/projects/" + prj.ID, ownerC, 200},
		{"/api/workspaces/" + store.DefaultWorkspaceID + "/members", adminC, 404},
		{"/api/workspaces/" + ws.ID + "/members", adminC, 404},
	} {
		if w := req(h, "GET", tc.path, "", tc.auth); w.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.path, w.Code, w.Body, tc.want)
		}
	}
	for _, w := range decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", adminC)) {
		if !w.Personal {
			t.Errorf("new instance administrator inherited a workspace: %+v", w)
		}
	}
	if role, err := f.st.WorkspaceRole(ctx, store.DefaultWorkspaceID, admin); err != nil || role != "" {
		t.Fatalf("implicit default membership: %q %v", role, err)
	}
	// The default workspace uses the same API, independently of the instance role.
	if err := f.st.SetWorkspaceMember(ctx, store.DefaultWorkspaceID, admin.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "PATCH", "/api/workspaces/"+store.DefaultWorkspaceID, `{"name":"Changed"}`, adminC); w.Code != 403 {
		t.Fatalf("instance admin renamed default workspace: %d", w.Code)
	}
	if w := req(h, "POST", "/api/projects", fmt.Sprintf(`{"name":"owned","workspace_id":%q}`, ws.ID), ownerC); w.Code != 201 {
		t.Fatalf("instance viewer creates in own workspace: %d %s", w.Code, w.Body)
	}
	me := decodeBody[map[string]any](t, req(h, "GET", "/api/auth/me", "", ownerC))
	if me["instance_admin"] != false || me["can_create_projects"] != true {
		t.Fatalf("capabilities: %+v", me)
	}
}

func TestTokensHaveOneAuthorizationBoundary(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	u, cookie := freshAccount(t, f, "tokens@example.com", store.RoleOwner)
	a, _ := f.st.CreateWorkspace(ctx, "A", u.ID)
	b, _ := f.st.CreateWorkspace(ctx, "B", u.ID)
	pa := &store.Project{Name: "api", WorkspaceID: a.ID}
	pb := &store.Project{Name: "other-api", WorkspaceID: b.ID}
	if _, err := f.st.CreateProject(ctx, pa); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.CreateProject(ctx, pb); err != nil {
		t.Fatal(err)
	}
	mint := func(body string) func(*http.Request) {
		t.Helper()
		w := req(h, "POST", "/api/tokens", body, cookie)
		if w.Code != 201 {
			t.Fatalf("mint: %d %s", w.Code, w.Body)
		}
		return withBearer(decodeBody[struct {
			Token string `json:"token"`
		}](t, w).Token)
	}
	wsToken := mint(fmt.Sprintf(`{"name":"workspace A","workspace_id":%q,"role":"admin"}`, a.ID))
	instanceToken := mint(`{"name":"operator","scope":"instance","role":"admin"}`)
	projectToken := mint(fmt.Sprintf(`{"name":"project A","project_id":%q,"role":"admin"}`, pa.ID))
	for _, tc := range []struct {
		path string
		auth func(*http.Request)
		want int
	}{
		{"/api/projects/" + pa.ID, wsToken, 200}, {"/api/projects/" + pb.ID, wsToken, 404},
		{"/api/workspaces/" + b.ID, wsToken, 404}, {"/api/users", wsToken, 403},
		{"/api/users", instanceToken, 200}, {"/api/projects/" + pa.ID, instanceToken, 404},
	} {
		if w := req(h, "GET", tc.path, "", tc.auth); w.Code != tc.want {
			t.Errorf("%s: %d %s, want %d", tc.path, w.Code, w.Body, tc.want)
		}
	}
	visible := decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", wsToken))
	if len(visible) != 1 || visible[0].ID != a.ID {
		t.Fatalf("token workspace list: %+v", visible)
	}
	if visible := decodeBody[[]wsView](t, req(h, "GET", "/api/workspaces", "", instanceToken)); len(visible) != 0 {
		t.Fatalf("instance token lists tenants: %+v", visible)
	}
	w := req(h, "POST", "/api/projects", `{"name":"implicit"}`, wsToken)
	if w.Code != 201 || decodeBody[store.Project](t, w).WorkspaceID != a.ID {
		t.Fatalf("token implicit destination: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PUT", "/api/projects/"+pa.ID+"/workspace", fmt.Sprintf(`{"workspace_id":%q}`, b.ID), cookie); w.Code != 200 {
		t.Fatalf("move: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/projects/"+pa.ID, "", projectToken); w.Code != 401 {
		t.Fatalf("old project token survives move: %d", w.Code)
	}
	if w := req(h, "GET", "/api/projects/"+pa.ID, "", wsToken); w.Code != 404 {
		t.Fatalf("workspace token follows a moved project: %d", w.Code)
	}
}

func TestWorkspaceGroupsAndMembershipRevocation(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	owner, ownerC := freshAccount(t, f, "group-owner@example.com", store.RoleViewer)
	member, memberC := freshAccount(t, f, "group-member@example.com", store.RoleMember)
	a, _ := f.st.CreateWorkspace(ctx, "A", owner.ID)
	b, _ := f.st.CreateWorkspace(ctx, "B", owner.ID)
	if err := f.st.SetWorkspaceMember(ctx, a.ID, member.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Name: "grouped", WorkspaceID: a.ID}
	if _, err := f.st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	path := "/api/workspaces/" + a.ID + "/access-groups"
	w := req(h, "POST", path, fmt.Sprintf(`{"name":"Developers","role":"member","user_ids":[%q],"project_ids":[%q]}`, member.ID, p.ID), ownerC)
	if w.Code != 201 {
		t.Fatalf("create group: %d %s", w.Code, w.Body)
	}
	g := decodeBody[store.AccessGroup](t, w)
	if w := req(h, "PUT", "/api/workspaces/"+b.ID+"/access-groups/"+g.ID, `{"name":"hijack","role":"admin"}`, ownerC); w.Code != 404 {
		t.Fatalf("cross-workspace group write: %d", w.Code)
	}
	if w := req(h, "POST", "/api/workspaces/"+b.ID+"/access-groups", fmt.Sprintf(`{"name":"Invalid","role":"admin","project_ids":[%q]}`, p.ID), ownerC); w.Code != 400 {
		t.Fatalf("cross-workspace group project: %d", w.Code)
	}
	if w := req(h, "POST", "/api/workspaces/"+b.ID+"/access-groups", `{"name":"Developers","role":"member"}`, ownerC); w.Code != 201 {
		t.Fatalf("same group name in another workspace: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PATCH", "/api/workspaces/"+a.ID+"/access-group-settings", `{"access_groups_enabled":true}`, ownerC); w.Code != 200 {
		t.Fatalf("enable groups: %d %s", w.Code, w.Body)
	}
	if w := req(h, "GET", "/api/projects/"+p.ID, "", memberC); w.Code != 200 {
		t.Fatalf("group access: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/workspaces/"+a.ID+"/members/"+member.ID, "", ownerC); w.Code != 204 {
		t.Fatalf("remove member: %d", w.Code)
	}
	if err := f.st.SetWorkspaceMember(ctx, a.ID, member.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "GET", "/api/projects/"+p.ID, "", memberC); w.Code != 404 {
		t.Fatalf("rejoining restored revoked group grants: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/workspaces/"+a.ID+"/members/"+owner.ID, "", ownerC); w.Code != 400 {
		t.Fatalf("last owner removed: %d", w.Code)
	}
}

func TestInstanceInviteWithoutWorkspace(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	operator, operatorC := freshAccount(t, f, "invite-operator@example.com", store.RoleAdmin)
	member, memberC := freshAccount(t, f, "team-owner@example.com", store.RoleViewer)
	team, err := f.st.CreateWorkspace(ctx, "Team", member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if role, err := f.st.WorkspaceRole(ctx, team.ID, operator); err != nil || role != "" {
		t.Fatalf("operator already belongs to the team: %q %v", role, err)
	}
	if w := req(h, "POST", "/api/invites", `{"email":"denied@example.com","role":"member"}`, memberC); w.Code != 403 {
		t.Fatalf("workspace owner created account-only invitation: %d %s", w.Code, w.Body)
	}
	w := req(h, "POST", "/api/invites", `{"email":"account-only@example.com","role":"member","send_email":false}`, operatorC)
	if w.Code != 201 {
		t.Fatalf("operator without a workspace cannot invite: %d %s", w.Code, w.Body)
	}
	inv := decodeBody[struct {
		Token string `json:"token"`
	}](t, w)
	w = req(h, "POST", "/api/auth/accept-invite", fmt.Sprintf(`{"token":%q,"name":"New User","password":"correct horse battery"}`, inv.Token), nil)
	if w.Code != 201 {
		t.Fatalf("accept account invitation: %d %s", w.Code, w.Body)
	}
	u, err := f.st.GetUserByEmail(ctx, "account-only@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != store.RoleMember {
		t.Fatalf("invited instance role: %s", u.Role)
	}
	for _, wsid := range []string{store.DefaultWorkspaceID, team.ID} {
		if role, err := f.st.WorkspaceRole(ctx, wsid, u); err != nil || role != "" {
			t.Fatalf("account invitation granted workspace %s: %q %v", wsid, role, err)
		}
	}
}

func TestWorkspaceInviteCannotGrantInstanceRole(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	u, cookie := freshAccount(t, f, "invite-owner@example.com", store.RoleViewer)
	ws, _ := f.st.CreateWorkspace(ctx, "Team", u.ID)
	w := req(h, "POST", "/api/workspaces/"+ws.ID+"/invites", `{"email":"invitee@example.com","role":"owner"}`, cookie)
	if w.Code != 201 {
		t.Fatalf("invite: %d %s", w.Code, w.Body)
	}
	inv := decodeBody[struct {
		Token string `json:"token"`
	}](t, w)
	newUser := &store.User{Name: "Invitee"}
	if _, err := f.st.AcceptInvite(ctx, hashSecret(inv.Token), newUser); err != nil {
		t.Fatal(err)
	}
	if newUser.Role != store.RoleMember {
		t.Fatalf("workspace invite granted instance role %s", newUser.Role)
	}
	if role, _ := f.st.WorkspaceRole(ctx, ws.ID, newUser); role != store.RoleOwner {
		t.Fatalf("invited workspace role: %s", role)
	}
	if role, _ := f.st.WorkspaceRole(ctx, store.DefaultWorkspaceID, newUser); role != "" {
		t.Fatalf("invite granted unrelated default membership: %s", role)
	}
}

func TestWorkspaceInvitesRecheckAuthority(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	owner, ownerC := freshAccount(t, f, "inv-owner@example.com", store.RoleViewer)
	admin, adminC := freshAccount(t, f, "inv-admin@example.com", store.RoleMember)
	ws, err := f.st.CreateWorkspace(ctx, "Invitations", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetWorkspaceMember(ctx, ws.ID, admin.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	mint := func(email string) string {
		t.Helper()
		w := req(h, "POST", "/api/workspaces/"+ws.ID+"/invites", fmt.Sprintf(`{"email":%q,"role":"member"}`, email), adminC)
		if w.Code != 201 {
			t.Fatalf("invite: %d %s", w.Code, w.Body)
		}
		return decodeBody[struct{ Token string }](t, w).Token
	}
	token := mint("demoted-invite@example.com")
	if err := f.st.SetWorkspaceMember(ctx, ws.ID, admin.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AcceptInvite(ctx, hashSecret(token), &store.User{Name: "Rejected"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("demoted inviter: %v", err)
	}
	if _, err := f.st.GetUserByEmail(ctx, "demoted-invite@example.com"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("failed invitation created an account: %v", err)
	}
	if err := f.st.SetWorkspaceMember(ctx, ws.ID, admin.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	token = mint("removed-invite@example.com")
	if w := req(h, "DELETE", "/api/workspaces/"+ws.ID+"/members/"+admin.ID, "", ownerC); w.Code != 204 {
		t.Fatalf("remove inviter: %d %s", w.Code, w.Body)
	}
	if err := f.st.SetWorkspaceMember(ctx, ws.ID, admin.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.AcceptInvite(ctx, hashSecret(token), &store.User{Name: "Rejected"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("rejoining restored a revoked invitation: %v", err)
	}
}

func TestTemplatesRespectWorkspaceMembership(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	owner, ownerC := freshAccount(t, f, "template-owner@example.com", store.RoleViewer)
	outsider, outsiderC := freshAccount(t, f, "template-operator@example.com", store.RoleAdmin)
	ws, err := f.st.CreateWorkspace(ctx, "Templates", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"key":"tenant-stack","workspace_id":%q,"visibility":"workspace","definition":%s}`, ws.ID, userTemplateDef)
	if w := req(h, "POST", "/api/templates", body, ownerC); w.Code != 201 {
		t.Fatalf("create template: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/templates/tenant-stack", "/api/templates/tenant-stack/export"} {
		if w := req(h, "GET", path, "", outsiderC); w.Code != 404 {
			t.Errorf("instance admin reads workspace template: %s %d", path, w.Code)
		}
	}
	if strings.Contains(req(h, "GET", "/api/templates", "", outsiderC).Body.String(), "tenant-stack") {
		t.Fatal("workspace template leaked into catalog")
	}
	if err := f.st.SetWorkspaceMember(ctx, ws.ID, outsider.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if w := req(h, "GET", "/api/templates/tenant-stack", "", outsiderC); w.Code != 200 {
		t.Fatalf("workspace viewer reads template: %d", w.Code)
	}
	if w := req(h, "PATCH", "/api/templates/tenant-stack", `{"name":"unauthorized"}`, outsiderC); w.Code != 403 {
		t.Fatalf("instance admin writes workspace template: %d", w.Code)
	}
	// 인스턴스 owner는 막히는 것이 없다.
	_, bossC := freshAccount(t, f, "template-boss@example.com", store.RoleOwner)
	if w := req(h, "GET", "/api/templates/tenant-stack", "", bossC); w.Code != 200 {
		t.Fatalf("instance owner reads workspace template: %d", w.Code)
	}
}

func TestGraphQLUsesWorkspaceBoundaries(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	u, cookie := freshAccount(t, f, "graphql-tenant@example.com", store.RoleViewer)
	a, err := f.st.CreateWorkspace(ctx, "GraphQL A", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.st.CreateWorkspace(ctx, "GraphQL B", u.ID)
	if err != nil {
		t.Fatal(err)
	}
	var projectA string
	for _, ws := range []*store.Workspace{a, b} {
		data := mustGQL(t, h, cookie, `mutation($workspace: String!) { projectCreate(input: {name: "same-name", workspaceId: $workspace}) { id } }`, map[string]any{"workspace": ws.ID})
		id := dig(data, "projectCreate.id").(string)
		p, err := f.st.GetProject(ctx, id)
		if err != nil || p.WorkspaceID != ws.ID {
			t.Fatalf("GraphQL created in wrong workspace: %+v %v", p, err)
		}
		if ws.ID == a.ID {
			projectA = id
		}
	}
	data := mustGQL(t, h, cookie, `query($workspace: String!) { projects(workspaceId: $workspace) { edges { node { id } } } }`, map[string]any{"workspace": a.ID})
	edges := dig(data, "projects.edges").([]any)
	if len(edges) != 1 || dig(data, "projects.edges.0.node.id") != projectA {
		t.Fatalf("GraphQL workspace filter: %+v", data)
	}
}
