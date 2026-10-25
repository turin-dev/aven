package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/store"
)

// fakeEnvOps는 docker 없이 환경 리소스 작업을 기록한다(삭제는 DB 행만 지운다).
type fakeEnvOps struct {
	st      *store.Store
	mu      sync.Mutex
	deleted []string
}

func (f *fakeEnvOps) EnsureEnvironment(context.Context, *store.Environment) error { return nil }
func (f *fakeEnvOps) DeleteEnvironment(ctx context.Context, env *store.Environment) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, env.ID)
	f.mu.Unlock()
	return f.st.DeleteEnvironment(ctx, env.ID)
}

type ghFixture struct {
	t          *testing.T
	ctx        context.Context
	srv        *Server
	st         *store.Store
	mgr        *deploy.Manager
	ops        *fakeEnvOps
	p          *store.Project
	prod       *store.Environment
	web, db    *store.Service
	other      *store.Service
	mu         sync.Mutex
	dispatched []string
	ghAPI      *httptest.Server
	changed    []string
}

func (f *ghFixture) queued() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.dispatched...)
}

const ghSecret = "webhook-s3cret"

func newGHFixture(t *testing.T) *ghFixture {
	t.Helper()
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	f := &ghFixture{t: t, ctx: ctx, st: st, changed: []string{"main.go"}}
	f.mgr = &deploy.Manager{Store: st, Docker: testDocker(), Cfg: deploy.Config{DataDir: t.TempDir(), BaseDomain: "example.com"},
		Dispatch: func(_, dep string) { f.mu.Lock(); f.dispatched = append(f.dispatched, dep); f.mu.Unlock() }}
	f.ops = &fakeEnvOps{st: st}
	f.srv = &Server{Store: st, Deploy: f.mgr, Events: &events.Bus{Store: st}, GitHubSecret: ghSecret,
		Log: slog.New(slog.NewTextHandler(io.Discard, nil)), envOps: f.ops}
	f.ghAPI = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		start := (page - 1) * 100
		if start < 0 || start >= len(f.changed) {
			json.NewEncoder(w).Encode([]map[string]string{})
			return
		}
		end := start + 100
		if end > len(f.changed) {
			end = len(f.changed)
		}
		files := make([]map[string]string, 0, end-start)
		for _, name := range f.changed[start:end] {
			files = append(files, map[string]string{"filename": name})
		}
		json.NewEncoder(w).Encode(files)
	}))
	t.Cleanup(f.ghAPI.Close)
	f.srv.GitHubAPIBase = f.ghAPI.URL

	f.p = &store.Project{Name: "acme"}
	if f.prod, err = st.CreateProject(ctx, f.p); err != nil {
		t.Fatal(err)
	}
	mk := func(name, source string, set func(in *store.Instance)) *store.Service {
		sv := &store.Service{ProjectID: f.p.ID, Name: name, SourceType: source}
		if err := st.CreateService(ctx, sv); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: sv.ID, EnvironmentID: f.prod.ID}
		set(in)
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		return sv
	}
	repo, other := "https://github.com/acme/app.git", "https://github.com/acme/other"
	f.web = mk("web", "repo", func(in *store.Instance) {
		in.RepoURL, in.Branch, in.AutoDeploy = &repo, "main", true
		in.Domains = []store.Domain{{Host: "app.acme.dev", Kind: "custom"}}
	})
	kind := "postgres"
	f.db = mk("db", "database", func(in *store.Instance) {
		def, _ := catalog.DatabaseService(kind)
		def.ApplyToInstance(in)
	})
	dbIn, _ := st.GetInstanceFor(ctx, f.db.ID, f.prod.ID)
	if err := st.CreateVolume(ctx, &store.Volume{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, InstanceID: &dbIn.ID, Name: "db-data", MountPath: "/var/lib/postgresql/data"}); err != nil {
		t.Fatal(err)
	}
	f.other = mk("other", "repo", func(in *store.Instance) { in.RepoURL, in.Branch, in.AutoDeploy = &other, "main", true })
	return f
}

func (f *ghFixture) hook(event string, payload any) (int, map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(payload)
	mac := hmac.New(sha256.New, []byte(ghSecret))
	mac.Write(body)
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/github", bytes.NewReader(body))
	r.Header.Set("X-GitHub-Event", event)
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	f.srv.githubWebhook(w, r)
	var out map[string]any
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (f *ghFixture) patchProject(body string) {
	f.t.Helper()
	r := httptest.NewRequest(http.MethodPatch, "/api/projects/"+f.p.ID, strings.NewReader(body))
	r.SetPathValue("pid", f.p.ID)
	w := httptest.NewRecorder()
	f.srv.updateProject(w, r)
	if w.Code != 200 {
		f.t.Fatalf("PATCH project: %d %s", w.Code, w.Body.String())
	}
}

func ghRepoJSON(full string) map[string]any {
	return map[string]any{"full_name": full, "clone_url": "https://github.com/" + full + ".git", "html_url": "https://github.com/" + full}
}

func prPayload(action string, number int, headRef, sha, headRepo, baseRepo string) map[string]any {
	return map[string]any{
		"action": action, "number": number,
		"pull_request": map[string]any{
			"number": number, "html_url": "https://github.com/" + baseRepo + "/pull/" + itoa(number), "title": "Add feature\nbody",
			"user": map[string]any{"login": "octocat"},
			"head": map[string]any{"ref": headRef, "sha": sha, "repo": ghRepoJSON(headRepo)},
			"base": map[string]any{"ref": "main", "repo": ghRepoJSON(baseRepo)},
		},
		"repository": ghRepoJSON(baseRepo),
	}
}

func pushPayload(branch, sha, repo string) map[string]any {
	return map[string]any{"ref": "refs/heads/" + branch, "after": sha, "repository": ghRepoJSON(repo),
		"head_commit": map[string]any{"id": sha, "message": "msg", "author": map[string]any{"name": "a"}},
		"commits":     []any{map[string]any{"modified": []string{"main.go"}}}}
}

func sha(c byte) string { return strings.Repeat(string(c), 40) }

func (f *ghFixture) envByName(name string) *store.Environment {
	envs, _ := f.st.ListEnvironments(f.ctx, f.p.ID)
	for _, e := range envs {
		if e.Name == name {
			return e
		}
	}
	return nil
}

func TestPRPreviewLifecycle(t *testing.T) {
	f := newGHFixture(t)
	ctx := f.ctx
	// 꺼져 있으면 아무것도 만들지 않는다
	if code, _ := f.hook("pull_request", prPayload("opened", 7, "feature/x", sha('a'), "acme/app", "acme/app")); code != 202 || f.envByName("pr-7") != nil {
		t.Fatalf("disabled project got a preview env (code %d)", code)
	}
	f.patchProject(`{"pr_environments":{"enabled":true}}`)
	p, _ := f.st.GetProject(ctx, f.p.ID)
	if !p.PREnvironments.Enabled || !p.PREnvironments.AutoDelete || !p.PREnvironments.Domain || p.PREnvironments.Focused || p.PRFromForks || p.PRFromBots || p.PREnvironments.BaseEnvironmentID != nil {
		t.Fatalf("pr settings defaults: %+v fork=%v bot=%v", p.PREnvironments, p.PRFromForks, p.PRFromBots)
	}

	code, out := f.hook("pull_request", prPayload("opened", 7, "feature/x", sha('a'), "acme/app", "acme/app"))
	if code != 202 {
		t.Fatalf("opened: %d %v", code, out)
	}
	env := f.envByName("pr-7")
	if env == nil || env.Kind != store.EnvPreview || env.PRNumber == nil || *env.PRNumber != 7 || env.PRURL == nil || *env.PRBranch != "feature/x" {
		t.Fatalf("preview env: %+v", env)
	}
	if len(f.queued()) != 3 {
		t.Fatalf("expected web+db+other deployments, got %v", f.queued())
	}
	web, _ := f.st.GetInstanceFor(ctx, f.web.ID, env.ID)
	if web.Branch != "feature/x" {
		t.Errorf("web branch = %s", web.Branch)
	}
	if len(web.Domains) != 1 || web.Domains[0].Host != "web-pr-7.example.com" || web.Domains[0].Kind != "generated" {
		t.Errorf("preview domains: %+v", web.Domains)
	}
	d, _ := f.st.LatestDeployment(ctx, web.ID)
	if d.CommitSHA == nil || *d.CommitSHA != sha('a') || d.Trigger != "github" || d.Status != store.DepQueued || *d.Branch != "feature/x" {
		t.Errorf("web deployment: %+v", d)
	}
	other, _ := f.st.GetInstanceFor(ctx, f.other.ID, env.ID)
	if other.Branch != "main" {
		t.Errorf("other repo branch changed: %s", other.Branch)
	}
	dbIn, _ := f.st.GetInstanceFor(ctx, f.db.ID, env.ID)
	vols, _ := f.st.ListVolumesByInstance(ctx, dbIn.ID)
	prodDB, _ := f.st.GetInstanceFor(ctx, f.db.ID, f.prod.ID)
	prodVols, _ := f.st.ListVolumesByInstance(ctx, prodDB.ID)
	if len(vols) != 1 || vols[0].DockerName == prodVols[0].DockerName {
		t.Errorf("db volume must be a fresh volume: %+v", vols)
	}
	// 같은 opened가 다시 와도 환경을 또 만들지 않는다
	f.hook("pull_request", prPayload("opened", 7, "feature/x", sha('a'), "acme/app", "acme/app"))
	if envs, _ := f.st.ListEnvironments(ctx, f.p.ID); len(envs) != 2 {
		t.Fatalf("duplicate preview env: %d", len(envs))
	}

	// PR head 브랜치 push는 미리보기 환경을 배포하지 않는다(synchronize가 담당)
	before := len(f.queued())
	_, out = f.hook("push", pushPayload("feature/x", sha('b'), "acme/app"))
	if len(f.queued()) != before {
		t.Fatalf("push double-deployed the preview env: %v", out)
	}
	// main push는 production만
	f.hook("push", pushPayload("main", sha('c'), "acme/app"))
	if q := f.queued(); len(q) != before+1 {
		t.Fatalf("main push: %v", q)
	}
	prodWeb, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	if d, _ := f.st.LatestDeployment(ctx, prodWeb.ID); *d.CommitSHA != sha('c') {
		t.Error("production web not deployed")
	}

	// synchronize: 새 head SHA를 web(preview)에만
	before = len(f.queued())
	f.hook("pull_request", prPayload("synchronize", 7, "feature/x", sha('d'), "acme/app", "acme/app"))
	if q := f.queued(); len(q) != before+1 {
		t.Fatalf("synchronize deployments: %v", q)
	}
	if d, _ := f.st.LatestDeployment(ctx, web.ID); *d.CommitSHA != sha('d') {
		t.Errorf("synchronize deployed %v", *d.CommitSHA)
	}

	// closed: auto_delete → 환경 삭제
	code, out = f.hook("pull_request", prPayload("closed", 7, "feature/x", sha('d'), "acme/app", "acme/app"))
	if code != 202 || f.envByName("pr-7") != nil || len(f.ops.deleted) != 1 || f.ops.deleted[0] != env.ID {
		t.Fatalf("closed: %d %v deleted=%v", code, out, f.ops.deleted)
	}
	evs, _ := f.st.ListEvents(ctx, f.p.ID, "", 100)
	kinds := map[string]bool{}
	for _, e := range evs {
		kinds[e.Kind] = true
	}
	if !kinds["environment.preview_created"] || !kinds["environment.preview_deleted"] {
		t.Errorf("events: %v", kinds)
	}
}

func TestPRPreviewForkGatingAndOptions(t *testing.T) {
	f := newGHFixture(t)
	f.patchProject(`{"pr_environments":{"enabled":true,"auto_delete":false,"domain":false}}`)
	_, out := f.hook("pull_request", prPayload("opened", 9, "patch-1", sha('e'), "stranger/app", "acme/app"))
	if f.envByName("pr-9") != nil {
		t.Fatal("fork PR created an env with pr_from_forks off")
	}
	if envs, _ := out["environments"].([]any); len(envs) != 1 || envs[0].(map[string]any)["action"] != "skipped" {
		t.Errorf("fork result: %v", out)
	}
	f.patchProject(`{"pr_from_forks":true}`)
	f.hook("pull_request", prPayload("reopened", 9, "patch-1", sha('e'), "stranger/app", "acme/app"))
	env := f.envByName("pr-9")
	if env == nil {
		t.Fatal("fork PR env not created with pr_from_forks on")
	}
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, env.ID)
	// fork의 head 브랜치는 base 저장소에 없으므로 base 브랜치를 clone하고 head SHA를 받는다
	if web.Branch != "main" || len(web.Domains) != 0 {
		t.Errorf("fork preview web: branch=%s domains=%v", web.Branch, web.Domains)
	}
	if d, _ := f.st.LatestDeployment(f.ctx, web.ID); d == nil || *d.CommitSHA != sha('e') {
		t.Errorf("fork deploy: %+v", d)
	}
	// auto_delete=false면 닫혀도 남는다
	f.hook("pull_request", prPayload("closed", 9, "patch-1", sha('e'), "stranger/app", "acme/app"))
	if f.envByName("pr-9") == nil || len(f.ops.deleted) != 0 {
		t.Error("env deleted although auto_delete=false")
	}
	// 다른 저장소의 PR은 무시
	f.hook("pull_request", prPayload("opened", 10, "x", sha('f'), "acme/unrelated", "acme/unrelated"))
	if f.envByName("pr-10") != nil {
		t.Error("unrelated repo created env")
	}
	// 잘못된 payload
	if code, _ := f.hook("pull_request", prPayload("opened", 11, "-bad", "nothex", "acme/app", "acme/app")); code != 400 {
		t.Errorf("invalid payload accepted: %d", code)
	}
	// base 환경은 같은 프로젝트의 standard 환경이어야 한다
	r := httptest.NewRequest(http.MethodPatch, "/", strings.NewReader(`{"pr_environments":{"base_environment_id":"`+env.ID+`"}}`))
	r.SetPathValue("pid", f.p.ID)
	w := httptest.NewRecorder()
	f.srv.updateProject(w, r)
	if w.Code != 400 {
		t.Errorf("preview env accepted as base: %d", w.Code)
	}
}

func TestFocusedPRPreviewUsesChangedFilesDependenciesAndBaseValues(t *testing.T) {
	f := newGHFixture(t)
	f.patchProject(`{"pr_environments":{"enabled":true,"focused":true}}`)
	f.changed = []string{"apps/web/src/page.tsx"}
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	web.RootDir = "/apps/web"
	web.WatchPaths = []string{"apps/web/**"}
	web.Variables["DATABASE_URL"] = "${{db.DATABASE_URL}}"
	if err := f.st.UpdateInstanceConfig(f.ctx, web); err != nil {
		t.Fatal(err)
	}
	other, _ := f.st.GetInstanceFor(f.ctx, f.other.ID, f.prod.ID)
	other.Variables["BASE_MARKER"] = "from-base"
	other.Variables["REGION"] = "${{shared.REGION}}"
	if err := f.st.UpdateInstanceConfig(f.ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetSharedVars(f.ctx, f.prod.ID, map[string]string{"REGION": "base-region"}); err != nil {
		t.Fatal(err)
	}

	code, out := f.hook("pull_request", prPayload("opened", 21, "feature/web", sha('b'), "acme/app", "acme/app"))
	if code != 202 {
		t.Fatalf("opened: %d %v", code, out)
	}
	env := f.envByName("pr-21")
	if env == nil || env.BaseEnvironmentID == nil || *env.BaseEnvironmentID != f.prod.ID {
		t.Fatalf("preview base environment: %+v", env)
	}
	if got := len(f.queued()); got != 2 {
		t.Fatalf("expected web and its database dependency to deploy, got %v", f.queued())
	}
	previewWeb, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, env.ID)
	previewDB, _ := f.st.GetInstanceFor(f.ctx, f.db.ID, env.ID)
	previewOther, _ := f.st.GetInstanceFor(f.ctx, f.other.ID, env.ID)
	if d, _ := f.st.LatestDeployment(f.ctx, previewWeb.ID); d.Status != store.DepQueued || *d.CommitSHA != sha('b') {
		t.Errorf("web deployment: %+v", d)
	}
	if d, _ := f.st.LatestDeployment(f.ctx, previewDB.ID); d.Status != store.DepQueued {
		t.Errorf("database dependency deployment: %+v", d)
	}
	if d, _ := f.st.LatestDeployment(f.ctx, previewOther.ID); d.Status != store.DepSkipped {
		t.Errorf("unaffected service should be skipped: %+v", d)
	}
	// References inside the skipped service resolve in the base environment, not the preview.
	if err := f.st.SetSharedVars(f.ctx, env.ID, map[string]string{"REGION": "preview-region"}); err != nil {
		t.Fatal(err)
	}
	resolved, err := f.mgr.Resolve(f.ctx, previewOther, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Vars["BASE_MARKER"] != "from-base" || resolved.Vars["REGION"] != "base-region" || resolved.Vars["KEEL_ENVIRONMENT_NAME"] != "production" {
		t.Errorf("skipped service did not resolve against its base environment: %v", resolved.Vars)
	}
}

func TestPRPreviewBotOptIn(t *testing.T) {
	f := newGHFixture(t)
	f.patchProject(`{"pr_environments":{"enabled":true}}`)
	payload := prPayload("opened", 22, "dependabot/update", sha('c'), "acme/app", "acme/app")
	pull := payload["pull_request"].(map[string]any)
	user := pull["user"].(map[string]any)
	user["login"], user["type"] = "dependabot[bot]", "Bot"
	code, out := f.hook("pull_request", payload)
	if code != 202 || f.envByName("pr-22") != nil {
		t.Fatalf("bot PR should be off by default: code=%d out=%v", code, out)
	}
	f.patchProject(`{"pr_from_bots":true}`)
	payload["action"] = "reopened"
	code, out = f.hook("pull_request", payload)
	if code != 202 || f.envByName("pr-22") == nil {
		t.Fatalf("bot PR was not allowed: code=%d out=%v", code, out)
	}
}

func TestWaitForCIReleaseFailSupersede(t *testing.T) {
	f := newGHFixture(t)
	ctx := f.ctx
	web, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	web.WaitForCI = true
	f.st.UpdateInstanceConfig(ctx, web)

	f.hook("push", pushPayload("main", sha('1'), "acme/app"))
	d1, _ := f.st.LatestDeployment(ctx, web.ID)
	if d1.Status != store.DepWaiting || len(f.queued()) != 0 {
		t.Fatalf("expected waiting, got %s queued=%v", d1.Status, f.queued())
	}
	// 재시작해도 waiting은 실패 처리되지 않는다(진행 중이던 queued 배포만 실패 처리)
	other, _ := f.st.GetInstanceFor(ctx, f.other.ID, f.prod.ID)
	stale := &store.Deployment{ServiceID: f.other.ID, EnvironmentID: f.prod.ID, InstanceID: other.ID, Trigger: "manual"}
	f.st.CreateDeployment(ctx, stale)
	if err := f.mgr.Init(ctx); err != nil {
		t.Fatal(err)
	}
	if d, _ := f.st.GetDeployment(ctx, d1.ID); d.Status != store.DepWaiting {
		t.Fatalf("waiting deployment changed on restart: %s", d.Status)
	}
	if d, _ := f.st.GetDeployment(ctx, stale.ID); d.Status != store.DepFailed {
		t.Fatalf("stale queued deployment: %s", d.Status)
	}

	// 새 push가 이전 waiting을 대체한다
	f.hook("push", pushPayload("main", sha('2'), "acme/app"))
	if d, _ := f.st.GetDeployment(ctx, d1.ID); d.Status != store.DepRemoved {
		t.Fatalf("old waiting not superseded: %s", d.Status)
	}
	d2, _ := f.st.LatestDeployment(ctx, web.ID)

	ci := func(event, s, conclusion, repo string) map[string]any {
		key := "check_suite"
		if event == "workflow_run" {
			key = "workflow_run"
		}
		return map[string]any{"action": "completed", key: map[string]any{"head_sha": s, "conclusion": conclusion}, "repository": ghRepoJSON(repo)}
	}
	// 대체된 SHA의 성공은 아무것도 풀지 않는다
	f.hook("check_suite", ci("check_suite", sha('1'), "success", "acme/app"))
	if len(f.queued()) != 0 {
		t.Fatal("superseded deployment released")
	}
	// 다른 저장소의 같은 SHA는 무시
	f.hook("workflow_run", ci("workflow_run", sha('2'), "success", "acme/other"))
	if len(f.queued()) != 0 {
		t.Fatal("released by another repo's CI")
	}
	// 판단할 수 없는 결론(stale)은 무시
	f.hook("check_suite", ci("check_suite", sha('2'), "stale", "acme/app"))
	_, out := f.hook("workflow_run", ci("workflow_run", sha('2'), "success", "acme/app"))
	if q := f.queued(); len(q) != 1 || q[0] != d2.ID {
		t.Fatalf("release: %v %v", q, out)
	}
	if d, _ := f.st.GetDeployment(ctx, d2.ID); d.Status != store.DepQueued {
		t.Fatalf("released status: %s", d.Status)
	}
	// 중복 전달은 두 번 풀지 않는다
	f.hook("check_suite", ci("check_suite", sha('2'), "success", "acme/app"))
	if len(f.queued()) != 1 {
		t.Fatal("released twice")
	}

	// CI 실패 → skipped("CI 실패")
	f.hook("push", pushPayload("main", sha('3'), "acme/app"))
	d3, _ := f.st.LatestDeployment(ctx, web.ID)
	f.hook("check_suite", ci("check_suite", sha('3'), "failure", "acme/app"))
	d3, _ = f.st.GetDeployment(ctx, d3.ID)
	if d3.Status != store.DepSkipped || d3.Error == nil || *d3.Error != "CI 실패" || d3.FinishedAt == nil || len(f.queued()) != 1 {
		t.Fatalf("ci failure: %+v", d3)
	}
	// waiting 배포도 취소할 수 있다
	f.hook("push", pushPayload("main", sha('4'), "acme/app"))
	d4, _ := f.st.LatestDeployment(ctx, web.ID)
	f.mgr.Cancel(ctx, d4.ID)
	if d, _ := f.st.GetDeployment(ctx, d4.ID); d.Status != store.DepCancelled {
		t.Fatalf("cancel waiting: %s", d.Status)
	}
}

func TestWaitForCIInPreview(t *testing.T) {
	f := newGHFixture(t)
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	web.WaitForCI = true
	f.st.UpdateInstanceConfig(f.ctx, web)
	f.patchProject(`{"pr_environments":{"enabled":true}}`)
	f.hook("pull_request", prPayload("opened", 3, "feat", sha('a'), "acme/app", "acme/app"))
	env := f.envByName("pr-3")
	pw, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, env.ID)
	d, _ := f.st.LatestDeployment(f.ctx, pw.ID)
	if d.Status != store.DepWaiting || len(f.queued()) != 2 { // db, other만 바로
		t.Fatalf("preview wait_for_ci: %s %v", d.Status, f.queued())
	}
	f.hook("check_suite", map[string]any{"action": "completed", "check_suite": map[string]any{"head_sha": sha('a'), "conclusion": "success"}, "repository": ghRepoJSON("acme/app")})
	if len(f.queued()) != 3 {
		t.Fatalf("preview not released: %v", f.queued())
	}
}

func TestFocusedPRPreviewSynchronizeDeploysAffectedServicesOnly(t *testing.T) {
	f := newGHFixture(t)
	f.patchProject(`{"pr_environments":{"enabled":true,"focused":true}}`)
	repo := "https://github.com/acme/app.git"
	apiSvc := &store.Service{ProjectID: f.p.ID, Name: "api", SourceType: "repo"}
	if err := f.st.CreateService(f.ctx, apiSvc); err != nil {
		t.Fatal(err)
	}
	apiIn := &store.Instance{ServiceID: apiSvc.ID, EnvironmentID: f.prod.ID}
	apiIn.RepoURL, apiIn.Branch, apiIn.RootDir = &repo, "main", "/apps/api"
	if err := f.st.CreateInstance(f.ctx, apiIn); err != nil {
		t.Fatal(err)
	}
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	web.RootDir = "/apps/web"
	web.Variables["DATABASE_URL"] = "${{db.DATABASE_URL}}"
	if err := f.st.UpdateInstanceConfig(f.ctx, web); err != nil {
		t.Fatal(err)
	}

	f.changed = []string{"apps/web/page.tsx"}
	if code, out := f.hook("pull_request", prPayload("opened", 31, "feature/both", sha('d'), "acme/app", "acme/app")); code != 202 {
		t.Fatalf("opened: %d %v", code, out)
	}
	env := f.envByName("pr-31")
	previewAPI, _ := f.st.GetInstanceFor(f.ctx, apiSvc.ID, env.ID)
	if d, _ := f.st.LatestDeployment(f.ctx, previewAPI.ID); d.Status != store.DepSkipped {
		t.Fatalf("api should be skipped on open: %+v", d)
	}
	if previewAPI.Branch != "feature/both" {
		t.Errorf("skipped PR repo service should track the PR branch, got %q", previewAPI.Branch)
	}
	// Simulate the opened deploys finishing so the web/db instances have a current deployment.
	for _, svc := range []*store.Service{f.web, f.db} {
		in, _ := f.st.GetInstanceFor(f.ctx, svc.ID, env.ID)
		d, _ := f.st.LatestDeployment(f.ctx, in.ID)
		if err := f.st.SetInstanceStatus(f.ctx, in.ID, store.InstOnline, &d.ID); err != nil {
			t.Fatal(err)
		}
	}
	before := len(f.queued())

	f.changed = []string{"apps/api/main.go", "apps/web/page.tsx"}
	if code, out := f.hook("pull_request", prPayload("synchronize", 31, "feature/both", sha('e'), "acme/app", "acme/app")); code != 202 {
		t.Fatalf("synchronize: %d %v", code, out)
	}
	// web and api changed; db (web's dependency) already runs in the preview and is not redeployed.
	if got := len(f.queued()) - before; got != 2 {
		t.Fatalf("synchronize should deploy web and api only, got %d new deployments", got)
	}
	if d, _ := f.st.LatestDeployment(f.ctx, previewAPI.ID); d.Status != store.DepQueued || *d.CommitSHA != sha('e') {
		t.Errorf("api deployment after synchronize: %+v", d)
	}
}
