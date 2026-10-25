package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

func TestCredentialKeys(t *testing.T) {
	pg := catalog.CredentialKeys("postgres")
	if !pg["POSTGRES_PASSWORD"] || !pg["PGPASSWORD"] || pg["DATABASE_URL"] || pg["POSTGRES_USER"] {
		t.Errorf("postgres: %v", pg)
	}
	if my := catalog.CredentialKeys("mysql"); !my["MYSQL_ROOT_PASSWORD"] || !my["MYSQLPASSWORD"] {
		t.Errorf("mysql: %v", my)
	}
	if r := catalog.CredentialKeys("redis"); !r["REDIS_PASSWORD"] || !r["REDISPASSWORD"] {
		t.Errorf("redis: %v", r)
	}
	if m := catalog.CredentialKeys("mongo"); !m["MONGO_INITDB_ROOT_PASSWORD"] || !m["MONGOPASSWORD"] || m["MONGOUSER"] {
		t.Errorf("mongo: %v", m)
	}
	if catalog.CredentialKeys("nope") != nil {
		t.Error("unknown kind")
	}
}

func TestDiffVarsMasking(t *testing.T) {
	src := map[string]string{"A": "secret", "B": "${{db.DATABASE_URL}}", "SAME": "x", "PW": "new"}
	dst := map[string]string{"A": "old", "C": "gone", "SAME": "x", "PW": "keep"}
	ch, next := DiffVars(src, dst, map[string]bool{"PW": true})
	got := []string{}
	for _, c := range ch {
		s := c.Key + ":" + c.Action
		if c.From != nil {
			s += ":" + *c.From
		}
		if c.To != nil {
			s += ">" + *c.To
		}
		got = append(got, s)
	}
	want := "A:changed:•••>•••,B:added>${{db.DATABASE_URL}},C:removed:•••"
	if strings.Join(got, ",") != want {
		t.Errorf("changes %s", strings.Join(got, ","))
	}
	if next["A"] != "secret" || next["B"] == "" || next["PW"] != "keep" || next["SAME"] != "x" {
		t.Errorf("next %v", next)
	}
	if _, ok := next["C"]; ok {
		t.Error("removed key kept")
	}
}

type syncFixture struct {
	*ghFixture
	stg *store.Environment
}

func newSyncFixture(t *testing.T) *syncFixture {
	f := newGHFixture(t)
	// database_kind가 있는 DB 서비스(새 환경은 catalog로 새 비밀번호를 만든다)
	kind := "postgres"
	pg := &store.Service{ProjectID: f.p.ID, Name: "pg", SourceType: "database", DatabaseKind: &kind}
	if err := f.st.CreateService(f.ctx, pg); err != nil {
		t.Fatal(err)
	}
	pgIn := &store.Instance{ServiceID: pg.ID, EnvironmentID: f.prod.ID}
	def, _ := catalog.DatabaseService(kind)
	def.ApplyToInstance(pgIn)
	pgIn.Variables["POSTGRES_DB"] = "prod_db"
	if err := f.st.CreateInstance(f.ctx, pgIn); err != nil {
		t.Fatal(err)
	}
	code, out := f.call(f.srv.createEnvironment, "POST", "/x", `{"name":"staging"}`, map[string]string{"pid": f.p.ID})
	if code != 201 {
		t.Fatalf("create env: %d %v", code, out)
	}
	stg, err := f.st.GetEnvironment(f.ctx, out["id"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// 원본(prod) 변경: web 설정/변수/볼륨, 공유 변수
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	cmd := "npm start"
	web.StartCommand, web.Replicas = &cmd, 2
	web.Variables = map[string]string{"API_KEY": "sk-live-123", "DB": "${{db.DATABASE_URL}}"}
	if err := f.st.UpdateInstanceConfig(f.ctx, web); err != nil {
		t.Fatal(err)
	}
	limit := 512
	if err := f.st.CreateVolume(f.ctx, &store.Volume{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, InstanceID: &web.ID, Name: "web-data", MountPath: "/data", SizeLimitMB: &limit}); err != nil {
		t.Fatal(err)
	}
	f.st.SetSharedVars(f.ctx, f.prod.ID, map[string]string{"REGION": "eu"})
	f.st.SetSharedVars(f.ctx, stg.ID, map[string]string{"OLD": "1"})
	return &syncFixture{f, stg}
}

func findService(t *testing.T, f *ghFixture, name string) *store.Service {
	t.Helper()
	svcs, _ := f.st.ListServices(f.ctx, f.p.ID)
	for _, s := range svcs {
		if s.Name == name {
			return s
		}
	}
	t.Fatalf("service %s not found", name)
	return nil
}

func findSvc(plan map[string]any, name string) map[string]any {
	for _, x := range plan["services"].([]any) {
		m := x.(map[string]any)
		if m["service_name"] == name {
			return m
		}
	}
	return nil
}

func TestSyncPreview(t *testing.T) {
	f := newSyncFixture(t)
	pv := map[string]string{"eid": f.stg.ID}
	code, plan := f.call(f.srv.syncPreview, "GET", "/x?from="+f.prod.ID, "", pv)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, plan)
	}
	web := findSvc(plan, "web")
	if web == nil {
		t.Fatalf("web missing: %v", plan)
	}
	b, _ := json.Marshal(web)
	s := string(b)
	for _, want := range []string{`"field":"start_command"`, `"to":"npm start"`, `"field":"replicas"`, `"action":"added","from":null,"key":"API_KEY","to":"•••"`,
		`"to":"${{db.DATABASE_URL}}"`, `"mount_path":"/data","size_limit_mb":512`} {
		if !strings.Contains(s, want) {
			t.Errorf("web diff missing %s: %s", want, s)
		}
	}
	if strings.Contains(s, "sk-live-123") || strings.Contains(s, "domains") || strings.Contains(s, "app.acme.dev") {
		t.Errorf("secret or domain leaked: %s", s)
	}
	// 대상 web은 소스를 기본 환경에서 받아 왔지만 변수·배포가 없으므로 update다(pristine 아님: repo_url이 있다).
	if web["action"] != "update" {
		t.Errorf("web action %v", web["action"])
	}
	// DB 비밀번호는 옮기지 않는다(새 환경은 자기 비밀번호를 만들었다).
	pg := findSvc(plan, "pg")
	if pg == nil {
		t.Fatalf("pg missing (POSTGRES_DB differs): %v", plan)
	}
	b, _ = json.Marshal(pg)
	if strings.Contains(string(b), `"key":"POSTGRES_PASSWORD"`) || strings.Contains(string(b), `"key":"PGPASSWORD"`) {
		t.Errorf("db credential in diff: %s", b)
	}
	if !strings.Contains(string(b), `"protected_variables":["PGPASSWORD","POSTGRES_PASSWORD"]`) || !strings.Contains(string(b), `"key":"POSTGRES_DB"`) {
		t.Errorf("protected list / db diff: %s", b)
	}
	sh, _ := json.Marshal(plan["shared_variables"])
	if !strings.Contains(string(sh), `"action":"removed","from":"•••","key":"OLD"`) || !strings.Contains(string(sh), `"action":"added","from":null,"key":"REGION"`) {
		t.Errorf("shared diff: %s", sh)
	}
	// 거부: 자기 자신, 다른 프로젝트, from 없음
	if code, _ := f.call(f.srv.syncPreview, "GET", "/x?from="+f.stg.ID, "", pv); code != 400 {
		t.Errorf("self: %d", code)
	}
	if code, _ := f.call(f.srv.syncPreview, "GET", "/x", "", pv); code != 400 {
		t.Errorf("no from: %d", code)
	}
	other := &store.Project{Name: "other"}
	oenv, _ := f.st.CreateProject(f.ctx, other)
	if code, _ := f.call(f.srv.syncPreview, "GET", "/x?from="+oenv.ID, "", pv); code != 400 {
		t.Errorf("cross project: %d", code)
	}
}

func TestSyncApply(t *testing.T) {
	f := newSyncFixture(t)
	pv := map[string]string{"eid": f.stg.ID}
	stgWeb, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.stg.ID)
	pgSvc := findService(t, f.ghFixture, "pg")
	stgDB, _ := f.st.GetInstanceFor(f.ctx, pgSvc.ID, f.stg.ID)
	prodDB, _ := f.st.GetInstanceFor(f.ctx, pgSvc.ID, f.prod.ID)
	pwBefore := stgDB.Variables["POSTGRES_PASSWORD"]
	if pwBefore == "" || pwBefore == prodDB.Variables["POSTGRES_PASSWORD"] {
		t.Fatal("fixture: staging db should have its own password")
	}
	// 대상 web에 현재 배포를 둔다(동기화 변경이 pending_changes로 보여야 한다).
	d := &store.Deployment{ServiceID: f.web.ID, EnvironmentID: f.stg.ID, InstanceID: stgWeb.ID, Status: store.DepLive, Trigger: "manual",
		ReqSpec: stgWeb.Spec, VarsRaw: stgWeb.Variables}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	f.st.SetInstanceStatus(f.ctx, stgWeb.ID, "online", &d.ID)

	// 서비스 목록 검사
	if code, _ := f.call(f.srv.syncEnvironment, "POST", "/x", `{"from":"`+f.prod.ID+`","services":["svc_nope"]}`, pv); code != 400 {
		t.Errorf("unknown service: %d", code)
	}
	if code, _ := f.call(f.srv.syncEnvironment, "POST", "/x", `{"from":"`+f.stg.ID+`"}`, pv); code != 400 {
		t.Errorf("self sync: %d", code)
	}

	body := `{"from":"` + f.prod.ID + `","services":["` + f.web.ID + `","` + pgSvc.ID + `"],"include":{"settings":true,"variables":true,"volumes":false,"shared_variables":false}}`
	code, out := f.call(f.srv.syncEnvironment, "POST", "/x", body, pv)
	if code != 200 {
		t.Fatalf("sync: %d %v", code, out)
	}
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.stg.ID)
	if web.StartCommand == nil || *web.StartCommand != "npm start" || web.Replicas != 2 || web.Variables["API_KEY"] != "sk-live-123" {
		t.Errorf("web not synced: %+v %v", web.Spec, web.Variables)
	}
	if len(web.Domains) != 0 {
		t.Errorf("domains copied: %v", web.Domains)
	}
	pc := strings.Join(PendingChanges(web, d), ",")
	if !strings.Contains(pc, "start_command") || !strings.Contains(pc, "variables.API_KEY") {
		t.Errorf("pending changes: %s", pc)
	}
	vols, _ := f.st.ListVolumesByInstance(f.ctx, web.ID)
	if len(vols) != 0 {
		t.Errorf("volumes created though excluded: %v", vols)
	}
	db, _ := f.st.GetInstanceFor(f.ctx, pgSvc.ID, f.stg.ID)
	if db.Variables["POSTGRES_PASSWORD"] != pwBefore || db.Variables["PGPASSWORD"] != "${{POSTGRES_PASSWORD}}" {
		t.Error("db password overwritten")
	}
	if db.Variables["POSTGRES_DB"] != "prod_db" {
		t.Errorf("non-credential db variable not synced: %v", db.Variables["POSTGRES_DB"])
	}
	if sh, _ := f.st.GetSharedVars(f.ctx, f.stg.ID); sh["OLD"] != "1" || sh["REGION"] != "" {
		t.Errorf("shared vars touched: %v", sh)
	}
	if len(f.queued()) != 0 {
		t.Errorf("deployed without deploy=true: %v", f.queued())
	}
	// 'other'는 services에 없으므로 그대로다
	oth, _ := f.st.GetInstanceFor(f.ctx, f.other.ID, f.stg.ID)
	if oth.AutoDeploy {
		t.Error("unselected service synced")
	}

	// 나머지(볼륨, 공유 변수) + 배포
	code, out = f.call(f.srv.syncEnvironment, "POST", "/x", `{"from":"`+f.prod.ID+`","deploy":true}`, pv)
	if code != 200 {
		t.Fatalf("sync 2: %d %v", code, out)
	}
	vols, _ = f.st.ListVolumesByInstance(f.ctx, web.ID)
	if len(vols) != 1 || vols[0].MountPath != "/data" || vols[0].SizeLimitMB == nil || *vols[0].SizeLimitMB != 512 {
		t.Errorf("volume: %+v", vols)
	}
	if sh, _ := f.st.GetSharedVars(f.ctx, f.stg.ID); sh["REGION"] != "eu" || sh["OLD"] != "" {
		t.Errorf("shared vars: %v", sh)
	}
	if len(f.queued()) == 0 {
		t.Error("deploy=true enqueued nothing")
	}
	b, _ := json.Marshal(out)
	if !strings.Contains(string(b), `"deployment_id":"dep_`) {
		t.Errorf("response: %s", b)
	}
	// 이벤트
	evs, _ := f.st.ListEvents(f.ctx, f.p.ID, "", 50)
	n := 0
	for _, ev := range evs {
		if ev.Kind == "environment.synced" {
			n++
			if strings.Contains(ev.Message, "sk-live") {
				t.Errorf("secret in event: %s", ev.Message)
			}
		}
	}
	if n != 2 {
		t.Errorf("environment.synced events: %d", n)
	}
	// 다시 미리보기하면 web 변경이 없다
	_, plan := f.call(f.srv.syncPreview, "GET", "/x?from="+f.prod.ID, "", pv)
	if findSvc(plan, "web") != nil {
		t.Errorf("web still differs: %v", findSvc(plan, "web"))
	}
}

func TestSyncPristineIsCreate(t *testing.T) {
	f := newSyncFixture(t)
	// 새 서비스: prod에만 설정이 있고 staging 인스턴스는 빈 기본값
	sv := &store.Service{ProjectID: f.p.ID, Name: "worker", SourceType: "image"}
	f.st.CreateService(f.ctx, sv)
	img := "ghcr.io/acme/worker:1"
	f.st.CreateInstance(f.ctx, &store.Instance{ServiceID: sv.ID, EnvironmentID: f.prod.ID, Spec: store.Spec{Image: &img}})
	f.st.CreateInstance(f.ctx, &store.Instance{ServiceID: sv.ID, EnvironmentID: f.stg.ID})
	r := httptest.NewRequest("GET", "/x?from="+f.prod.ID, nil)
	r.SetPathValue("eid", f.stg.ID)
	w := httptest.NewRecorder()
	f.srv.syncPreview(w, r)
	var plan map[string]any
	json.Unmarshal(w.Body.Bytes(), &plan)
	wk := findSvc(plan, "worker")
	if wk == nil || wk["action"] != "create" {
		t.Errorf("worker: %v", wk)
	}
}
