package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/turin-dev/aven/internal/selfhost"
	"github.com/turin-dev/aven/internal/store"
)

func TestSelfServiceOwnerFlow(t *testing.T) {
	ctx := context.Background()
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	member, memberC := freshAccount(t, f, "dev@example.com", store.RoleMember)
	// 기본 작업 공간의 admin이어도 Aven 프로젝트는 보이지 않아야 한다.
	if err := f.st.SetWorkspaceMember(ctx, "ws_default", member.ID, store.RoleAdmin); err != nil {
		t.Fatal(err)
	}

	if w := req(h, "POST", "/api/owner/self-service", `{}`, adminC); w.Code != 403 {
		t.Fatalf("instance admin create: %d", w.Code)
	}
	v := decodeBody[selfServiceView](t, req(h, "GET", "/api/owner/self-service", "", ownerC))
	if v.Enabled || v.Server == nil || v.Server.Version == "" && v.Server.Mode == "" {
		t.Fatalf("initial view: %+v", v)
	}
	// 기본 작업 공간에 자원 한도가 0이어도 Aven은 전용 작업 공간에 만든다.
	// (fixture 서비스가 한도를 넘지 않게 먼저 지운다)
	ps, _ := f.st.ListProjects(ctx)
	for _, p := range ps {
		f.st.DeleteProject(ctx, p.ID)
	}
	zero := 0.0
	zi := int64(0)
	if err := f.st.SetWorkspaceResourceLimits(ctx, "ws_default", store.ResourceLimits{CPU: &zero, MemoryMB: &zi, VolumeMB: &zi}); err != nil {
		t.Fatal(err)
	}
	w := req(h, "POST", "/api/owner/self-service", `{"repo_url":"https://github.com/acme/aven-fork","branch":"stable"}`, ownerC)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	v = decodeBody[selfServiceView](t, w)
	if !v.Enabled || v.ServiceName != "aven-server" || v.RepoURL != "https://github.com/acme/aven-fork" || v.Branch != "stable" || v.ProjectID == "" || v.EnvironmentID == "" {
		t.Fatalf("created view: %+v", v)
	}
	if !f.mgr.IsSelfService(v.ServiceID) {
		t.Fatal("manager does not know the self service")
	}
	in, err := f.st.GetInstanceFor(ctx, v.ServiceID, v.EnvironmentID)
	if err != nil || in.Builder != "dockerfile" || in.DockerfilePath != "Dockerfile" || in.AutoDeploy || in.NodeID != store.LocalNodeID || in.Replicas != 1 || len(in.Domains) != 0 {
		t.Fatalf("instance: %+v %v", in, err)
	}
	if w := req(h, "POST", "/api/owner/self-service", `{}`, ownerC); w.Code != 409 {
		t.Fatalf("second create: %d", w.Code)
	}
	if p, err := f.st.GetProject(ctx, v.ProjectID); err != nil || p.WorkspaceID == "ws_default" {
		t.Fatalf("aven project workspace: %+v %v", p, err)
	} else if ws, err := f.st.GetWorkspace(ctx, p.WorkspaceID); err != nil || ws.Name != "Aven" {
		t.Fatalf("aven workspace: %+v %v", ws, err)
	}

	// 서버 owner는 캔버스에서 보고, 인스턴스에 self 정보가 붙는다. 다른 사람에게는 프로젝트가 없다.
	inst := decodeBody[map[string]any](t, req(h, "GET", fmt.Sprintf("/api/services/%s/instances/%s", v.ServiceID, v.EnvironmentID), "", ownerC))
	if self, ok := inst["self"].(map[string]any); !ok || self["mode"] == "" {
		t.Fatalf("instance self info: %v", inst["self"])
	}
	if w := req(h, "GET", "/api/projects/"+v.ProjectID, "", memberC); w.Code != 404 {
		t.Fatalf("workspace admin sees the aven project: %d", w.Code)
	}
	for _, p := range decodeBody[[]store.Project](t, req(h, "GET", "/api/projects", "", memberC)) {
		if p.ID == v.ProjectID {
			t.Fatal("aven project listed for a non-owner")
		}
	}
	if w := req(h, "POST", fmt.Sprintf("/api/services/%s/instances/%s/deploy", v.ServiceID, v.EnvironmentID), `{}`, memberC); w.Code != 404 {
		t.Fatalf("non-owner deploy: %d", w.Code)
	}

	// 지정된 동안은 서비스·프로젝트를 지울 수 없고, 해제하면 평범한 서비스로 남는다.
	if w := req(h, "DELETE", "/api/services/"+v.ServiceID, "", ownerC); w.Code != 409 {
		t.Fatalf("delete self service: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", "/api/projects/"+v.ProjectID, "", ownerC); w.Code != 409 {
		t.Fatalf("delete self project: %d %s", w.Code, w.Body)
	}
	if w := req(h, "DELETE", "/api/owner/self-service", "", ownerC); w.Code != 200 {
		t.Fatalf("unlink: %d %s", w.Code, w.Body)
	}
	if f.mgr.IsSelfService(v.ServiceID) {
		t.Fatal("still self after unlink")
	}
	if _, err := f.st.GetService(ctx, v.ServiceID); err != nil {
		t.Fatalf("service removed on unlink: %v", err)
	}
}

func TestSelfProjectOnFirstRun(t *testing.T) {
	ctx := context.Background()
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	// 새로 설치한 서버처럼 fixture의 프로젝트를 지운다.
	ps, _ := f.st.ListProjects(ctx)
	for _, p := range ps {
		if err := f.st.DeleteProject(ctx, p.ID); err != nil {
			t.Fatal(err)
		}
	}
	if insts, _ := f.st.ListInstances(ctx); len(insts) != 0 {
		t.Fatalf("fixture has instances: %d", len(insts))
	}
	if err := f.srv.EnsureSelfProjectOnFirstRun(ctx); err != nil {
		t.Fatal(err)
	}
	v := decodeBody[selfServiceView](t, req(h, "GET", "/api/owner/self-service", "", ownerC))
	if !v.Enabled || v.RepoURL != defaultSelfRepo || v.Branch != "main" {
		t.Fatalf("first run: %+v", v)
	}
	// 첫 프로젝트 항목은 Aven 프로젝트로 끝나지 않고, Aven 항목은 끝난다.
	setup := decodeBody[struct {
		Items []setupItem `json:"items"`
	}](t, req(h, "GET", "/api/owner/setup", "", ownerC))
	for _, it := range setup.Items {
		if (it.ID == "first_project" && it.Done) || (it.ID == "self_service" && !it.Done) {
			t.Errorf("setup item %+v", it)
		}
	}
	// 해제한 뒤 재시작해도 다시 만들지 않는다.
	req(h, "DELETE", "/api/owner/self-service", "", ownerC)
	if err := f.srv.EnsureSelfProjectOnFirstRun(ctx); err != nil {
		t.Fatal(err)
	}
	if f.mgr.SelfServiceID() != "" {
		t.Fatal("recreated after the owner unlinked it")
	}
}

func TestSelfProjectSkippedOnExistingInstall(t *testing.T) {
	ctx := context.Background()
	f, _ := authServer(t) // fixture에 서비스가 이미 있다
	if err := f.srv.EnsureSelfProjectOnFirstRun(ctx); err != nil {
		t.Fatal(err)
	}
	if f.mgr.SelfServiceID() != "" {
		t.Fatal("existing install got an aven project")
	}
}

func TestServerConfig(t *testing.T) {
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	_, adminC := freshAccount(t, f, "operator@example.com", store.RoleAdmin)
	envFile := filepath.Join(t.TempDir(), "aven.env")
	os.WriteFile(envFile, []byte("# aven\nKEEL_PUBLIC_URL=https://a.example\nKEEL_SMTP_PASSWORD=hunter22\n"), 0o600)
	f.mgr.Cfg.SelfEnvFile = envFile

	if w := req(h, "GET", "/api/owner/server-config", "", adminC); w.Code != 403 {
		t.Fatalf("instance admin: %d", w.Code)
	}
	v := decodeBody[serverConfigView](t, req(h, "GET", "/api/owner/server-config", "", ownerC))
	if !v.Writable || v.File != envFile {
		t.Fatalf("view: %+v", v)
	}
	for _, e := range v.Entries {
		if e.Name == "KEEL_SMTP_PASSWORD" && (e.Value != "" || !e.Set) {
			t.Fatalf("secret leaked: %+v", e)
		}
	}
	if w := req(h, "PUT", "/api/owner/server-config", `{"set":{"KEEL_DATA":"/tmp"}}`, ownerC); w.Code != 400 {
		t.Fatalf("fixed key: %d", w.Code)
	}
	w := req(h, "PUT", "/api/owner/server-config", `{"set":{"KEEL_BASE_DOMAIN":"apps.example"},"unset":["KEEL_PUBLIC_URL"]}`, ownerC)
	if w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body)
	}
	b, _ := os.ReadFile(envFile)
	if string(b) != "# aven\nKEEL_SMTP_PASSWORD=hunter22\nAVEN_BASE_DOMAIN=apps.example\n" {
		t.Fatalf("file: %q", b)
	}
	v = decodeBody[serverConfigView](t, w)
	if v.Pending == 0 {
		t.Fatal("changes must be pending until the next deploy")
	}
	// 처음 고치기 전 파일이 되돌릴 기준으로 남는다.
	if !selfhost.HasGoodEnv(f.mgr.Cfg.DataDir) {
		t.Fatal("no snapshot of the settings that worked")
	}
}

func TestSelfUpdateCheck(t *testing.T) {
	ctx := context.Background()
	f, h := authServer(t)
	_, ownerC := freshAccount(t, f, "boss@example.com", store.RoleOwner)
	old, latest := "1111111111111111111111111111111111111111", "3333333333333333333333333333333333333333"
	gh := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/aven/commits/main":
			fmt.Fprintf(w, `{"sha":%q,"commit":{"message":"Third\n\nbody","author":{"name":"Ann","date":"2026-10-25T10:00:00Z"}}}`, latest)
		case "/repos/acme/aven/compare/" + old + "..." + latest:
			fmt.Fprintf(w, `{"status":"ahead","ahead_by":2,"commits":[
				{"sha":"2222222222222222222222222222222222222222","commit":{"message":"Second","author":{"name":"Bo","date":"2026-10-25T09:00:00Z"}}},
				{"sha":%q,"commit":{"message":"Third","author":{"name":"Ann","date":"2026-10-25T10:00:00Z"}}}]}`, latest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer gh.Close()
	f.srv.GitHubAPIBase = gh.URL

	v := decodeBody[selfServiceView](t, req(h, "POST", "/api/owner/self-service", `{"repo_url":"https://github.com/acme/aven.git"}`, ownerC))
	// 지금 배포: old 커밋
	d := &store.Deployment{ServiceID: v.ServiceID, EnvironmentID: v.EnvironmentID, Trigger: "manual", Status: store.DepLive, CommitSHA: &old}
	in, _ := f.st.GetInstanceFor(ctx, v.ServiceID, v.EnvironmentID)
	d.InstanceID = in.ID
	if err := f.st.CreateDeployment(ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d.ID)

	w := req(h, "POST", "/api/owner/self-service/check", "", ownerC)
	if w.Code != 200 {
		t.Fatalf("check: %d %s", w.Code, w.Body)
	}
	v = decodeBody[selfServiceView](t, w)
	u := v.Update
	if u == nil || !u.Available || u.Behind != 2 || u.Latest == nil || u.Latest.SHA != latest || u.Latest.Message != "Third" || len(u.Commits) != 2 || u.Commits[0].SHA != latest || u.Error != "" {
		t.Fatalf("update: %+v", u)
	}
	if !v.Server.UpdateAvailable || v.Server.UpdateBehind != 2 {
		t.Fatalf("server info: %+v", v.Server)
	}
	inst := decodeBody[map[string]any](t, req(h, "GET", fmt.Sprintf("/api/services/%s/instances/%s", v.ServiceID, v.EnvironmentID), "", ownerC))
	if self := inst["self"].(map[string]any); self["update_available"] != true {
		t.Fatalf("instance self: %v", self)
	}
	// 활동 기록에는 새 버전마다 한 번만 남긴다.
	req(h, "POST", "/api/owner/self-service/check", "", ownerC)
	evs, _ := f.st.ListEvents(ctx, v.ProjectID, "", 50)
	n := 0
	for _, e := range evs {
		if e.Kind == "aven.update_available" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("update events: %d", n)
	}
	// 최신 커밋을 배포하면 바로 최신으로 보인다.
	d2 := &store.Deployment{ServiceID: v.ServiceID, EnvironmentID: v.EnvironmentID, InstanceID: in.ID, Trigger: "manual", Status: store.DepLive, CommitSHA: &latest}
	f.st.CreateDeployment(ctx, d2)
	f.st.SetInstanceStatus(ctx, in.ID, store.InstOnline, &d2.ID)
	if v = decodeBody[selfServiceView](t, req(h, "GET", "/api/owner/self-service", "", ownerC)); v.Update.Available {
		t.Fatalf("still available after updating: %+v", v.Update)
	}
}
