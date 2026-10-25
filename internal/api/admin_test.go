package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

func TestInstanceOverviewAuthorityAndInventory(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	operator, operatorC := freshAccount(t, f, "inventory-operator@example.com", store.RoleAdmin)
	owner, ownerC := freshAccount(t, f, "inventory-owner@example.com", store.RoleViewer)
	ws, err := f.st.CreateWorkspace(ctx, "Private workspace", owner.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Name: "private-project-name", WorkspaceID: ws.ID}
	env, err := f.st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	svc := &store.Service{ProjectID: p.ID, Name: "private-service-name", SourceType: "image"}
	if err := f.st.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	for _, status := range []string{store.InstOnline, store.InstFailed, store.InstCrashed, store.InstOffline} {
		e := env
		if status != store.InstOnline {
			e = &store.Environment{ProjectID: p.ID, Name: status}
			if err := f.st.CreateEnvironment(ctx, e); err != nil {
				t.Fatal(err)
			}
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: e.ID, Status: status, Variables: map[string]string{"PASSWORD": "private-app-password"}}
		if err := f.st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	before := decodeBody[instanceOverview](t, req(h, "GET", "/api/instance/overview", "", operatorC))
	disabled, _ := freshAccount(t, f, "inventory-disabled@example.com", store.RoleMember)
	disabled.Disabled = true
	if err := f.st.UpdateUser(ctx, disabled); err != nil {
		t.Fatal(err)
	}
	for i, ttl := range []time.Duration{time.Hour, -time.Hour} {
		invite := &store.Invite{Email: fmt.Sprintf("pending-%d@example.com", i), Role: store.RoleMember, CreatedBy: operator.ID, ExpiresAt: time.Now().Add(ttl)}
		if err := f.st.CreateInvite(ctx, invite, fmt.Sprintf("test-hash-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	// Database status alone cannot make a disconnected agent online.
	if err := f.st.CreateJoinToken(ctx, "inventory-node-hash", "remote-inventory", time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	remote, err := f.st.ConsumeJoinToken(ctx, "inventory-node-hash", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetNodeStatus(ctx, remote.ID, "online"); err != nil {
		t.Fatal(err)
	}
	w := req(h, "GET", "/api/instance/overview", "", operatorC)
	if w.Code != 200 {
		t.Fatalf("overview: %d %s", w.Code, w.Body)
	}
	got := decodeBody[instanceOverview](t, w)
	if got.Users.Total != before.Users.Total+1 || got.Users.Disabled != before.Users.Disabled+1 || got.Users.Active != before.Users.Active || got.Users.Admins != before.Users.Admins || got.PendingInvites != before.PendingInvites+1 {
		t.Fatalf("account/expiry counts: before=%+v, got=%+v", before.InstanceInventory, got.InstanceInventory)
	}
	var found bool
	for _, item := range got.Workspaces {
		if item.ID == ws.ID {
			found = true
			if item.Members != 1 || item.Projects != 1 || item.Services != 1 || item.Instances != 4 || item.UnhealthyInstances != 2 {
				t.Fatalf("workspace counts duplicated or stopped instances counted as unhealthy: %+v", item)
			}
		}
	}
	if !found || got.GeneratedAt.IsZero() {
		t.Fatalf("incomplete inventory: %+v", got)
	}
	for _, n := range got.Nodes {
		if n.ID == remote.ID && n.Status != "offline" {
			t.Fatalf("disconnected node: %+v", n)
		}
		if n.Local && n.Status != "online" {
			t.Fatalf("local node: %+v", n)
		}
	}
	for _, private := range []string{p.Name, svc.Name, "private-app-password", "password_hash", "token_hash", "totp_secret"} {
		if strings.Contains(w.Body.String(), private) {
			t.Fatalf("private project/account data in overview: %s", private)
		}
	}
	for _, tc := range []struct {
		path string
		auth func(*http.Request)
		code int
	}{
		{"/api/instance/overview", nil, 401},
		{"/api/instance/overview", ownerC, 403},
		{"/api/workspaces/" + ws.ID, operatorC, 404},
		{"/api/projects/" + p.ID, operatorC, 404},
	} {
		if w := req(h, "GET", tc.path, "", tc.auth); w.Code != tc.code {
			t.Fatalf("guard %s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	operations, err := f.st.CreateWorkspace(ctx, "Operations", operator.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"name":"overview-read","scope":"instance","role":"admin"}`, 200},
		{`{"name":"overview-low","scope":"instance","role":"viewer"}`, 403},
		{fmt.Sprintf(`{"name":"overview-workspace","scope":"workspace","workspace_id":%q,"role":"admin"}`, operations.ID), 403},
	} {
		w := req(h, "POST", "/api/tokens", tc.body, operatorC)
		if w.Code != 201 {
			t.Fatalf("create token: %d %s", w.Code, w.Body)
		}
		token := decodeBody[struct {
			Token string `json:"token"`
		}](t, w).Token
		if w := req(h, "GET", "/api/instance/overview", "", withBearer(token)); w.Code != tc.code {
			t.Fatalf("token guard: %d %s, want %d", w.Code, w.Body, tc.code)
		}
	}
}

func TestInstanceAdminDeletesWorkspace(t *testing.T) {
	f, h := authServer(t)
	ctx := context.Background()
	// 프로젝트를 지우면 proxy를 동기화한다: 가짜 Caddy admin API로 받는다.
	caddy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(caddy.Close)
	port, _ := strconv.Atoi(caddy.URL[strings.LastIndex(caddy.URL, ":")+1:])
	f.mgr.Proxy, f.mgr.HTTPLogs = proxy.New(proxy.Config{AdminPort: port}), deploy.NewHTTPLogHub(10)
	operator, operatorC := freshAccount(t, f, "ws-delete-operator@example.com", store.RoleAdmin)
	_, bossC := freshAccount(t, f, "ws-delete-boss@example.com", store.RoleOwner)
	tenant, tenantC := freshAccount(t, f, "ws-delete-tenant@example.com", store.RoleViewer)
	ws, err := f.st.CreateWorkspace(ctx, "Tenant team", tenant.ID)
	if err != nil {
		t.Fatal(err)
	}
	p := &store.Project{Name: "tenant-project", WorkspaceID: ws.ID}
	if _, err := f.st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	path := "/api/instance/workspaces/" + ws.ID
	confirm := `{"confirm":"Tenant team"}`
	// 작업 공간 owner라도 서버 관리자가 아니면 이 라우트를 쓸 수 없다.
	if w := req(h, "DELETE", path, confirm, tenantC); w.Code != 403 {
		t.Fatalf("workspace owner without instance role: %d %s", w.Code, w.Body)
	}
	// 서버 관리자가 만든 instance 토큰도 쓸 수 없다(세션만).
	tok := decodeBody[map[string]any](t, req(h, "POST", "/api/tokens", `{"name":"ops","scope":"instance","role":"admin"}`, operatorC))
	if w := req(h, "DELETE", path, confirm, withBearer(tok["token"].(string))); w.Code != 403 {
		t.Fatalf("instance token: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", path, `{"confirm":"tenant team"}`, operatorC); w.Code != 400 {
		t.Fatalf("wrong confirmation: %d %s", w.Code, w.Body)
	}
	ws0, err := f.st.GetWorkspace(ctx, store.DefaultWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if w := req(h, "DELETE", "/api/instance/workspaces/"+ws0.ID, fmt.Sprintf(`{"confirm":%q}`, ws0.Name), operatorC); w.Code != 400 {
		t.Fatalf("default workspace: %d %s", w.Code, w.Body)
	}
	// owner가 이 기능을 막으면 서버 관리자는 403을 받는다.
	if w := req(h, "PUT", "/api/owner/admin-caps", `{"disabled":["workspaces"]}`, bossC); w.Code != 200 {
		t.Fatalf("set caps: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", path, confirm, operatorC); w.Code != 403 {
		t.Fatalf("withheld capability: %d %s", w.Code, w.Body)
	}
	if w := req(h, "PUT", "/api/owner/admin-caps", `{"disabled":[]}`, bossC); w.Code != 200 {
		t.Fatalf("reset caps: %d %s", w.Code, w.Body)
	}
	// 멤버십이 없는 서버 관리자도 프로젝트가 남은 작업 공간을 지울 수 있다.
	if w := req(h, "DELETE", path, confirm, operatorC); w.Code != 204 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	if _, err := f.st.GetWorkspace(ctx, ws.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("workspace still exists: %v", err)
	}
	if _, err := f.st.GetProject(ctx, p.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("project still exists: %v", err)
	}
	if w := req(h, "DELETE", path, confirm, operatorC); w.Code != 404 {
		t.Fatalf("second delete: %d", w.Code)
	}
	entries, err := f.st.ListAudit(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(entries, func(a *store.AuditEntry) bool {
		return a.Kind == "workspace.deleted" && a.Target == ws.ID && a.ActorID == operator.ID
	}) {
		t.Fatalf("no audit entry: %+v", entries)
	}
}
