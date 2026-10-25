package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/store"
)

const userTemplateDef = `{
  "version": 1,
  "name": "Acme stack",
  "description": "web + db",
  "category": "starter",
  "tags": ["node", "postgres"],
  "readme": "# Acme\nhello",
  "inputs": [
    {"key": "ADMIN_EMAIL", "label": "Admin email", "required": true, "pattern": "[^@\\s]+@[^@\\s]+"},
    {"key": "REGION", "default": "eu"}
  ],
  "services": [
    {"name": "db", "source": {"database": "postgres"}, "volumes": [{"mount_path": "/var/lib/postgresql/data", "size_limit_mb": 512}]},
    {"name": "api", "icon": "node", "source": {"image": "ghcr.io/acme/api:1"}, "port": 3000, "replicas": 2,
     "healthcheck_path": "/health", "cron_schedule": "",
     "variables": {
       "DATABASE_URL": "${{db.DATABASE_URL}}",
       "ADMIN_EMAIL": "${{ inputs.ADMIN_EMAIL }}",
       "REGION": "${{ inputs.REGION }}",
       "SESSION_SECRET": "${{ secret(40) }}",
       "PIN": "${{ randomInt(1000, 9999) }}"
     },
     "volumes": [{"mount_path": "/data", "size_limit_mb": 100}],
     "domains": [{"generate": true, "target_port": 3000}],
     "tcp_proxy": {"container_port": 9229},
     "canvas": {"x": 120, "y": 40}
    }
  ]
}`

func TestUserTemplateLifecycleAndDeploy(t *testing.T) {
	f, h := authServer(t)
	f.mgr.Cfg.TCPPortMin, f.mgr.Cfg.TCPPortMax = 24100, 24200
	alice := testUser(t, f.st, "alice@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, alice.ID, store.RoleMember)
	ac := withCookie(testSessionCookie(t, f.srv, alice))

	body, _ := json.Marshal(map[string]any{"key": "acme-stack", "visibility": "private", "definition": json.RawMessage(userTemplateDef)})
	w := req(h, "POST", "/api/templates", string(body), ac)
	if w.Code != 201 {
		t.Fatalf("create: %d %s", w.Code, w.Body)
	}
	created := decodeBody[templateDetail](t, w)
	if created.Key != "acme-stack" || created.Name != "Acme stack" || created.Visibility != "private" || !created.CanEdit || created.Readme != "# Acme\nhello" ||
		len(created.Services) != 2 || len(created.Inputs) != 2 || *created.OwnerUserID != alice.ID {
		t.Fatalf("created: %+v", created)
	}
	// 정의 JSON에는 메타데이터가 들어가고 생성 함수는 계산되지 않은 채 남는다.
	if !strings.Contains(string(created.Definition), `"acme-stack"`) || !strings.Contains(string(created.Definition), "secret(40)") {
		t.Errorf("stored definition: %s", created.Definition)
	}
	if w := req(h, "POST", "/api/templates", string(body), ac); w.Code != 409 {
		t.Errorf("duplicate key: %d", w.Code)
	}
	if w := req(h, "POST", "/api/templates", `{"key":"postgres","definition":`+userTemplateDef+`}`, ac); w.Code != 409 {
		t.Errorf("built-in key: %d", w.Code)
	}
	if w := req(h, "POST", "/api/templates", `{"definition":{"version":1,"services":[{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{b.X}}"}}]}}`, ac); w.Code != 400 ||
		!strings.Contains(w.Body.String(), "problems") {
		t.Errorf("invalid definition: %d %s", w.Code, w.Body)
	}

	deploy := func(b string) *httpResult {
		w := req(h, "POST", "/api/projects/"+f.p.ID+"/templates/acme-stack", b, ac)
		return &httpResult{w.Code, w.Body.String()}
	}
	if r := deploy(`{}`); r.code != 400 || !strings.Contains(r.body, `"missing":["ADMIN_EMAIL"]`) {
		t.Errorf("missing input: %d %s", r.code, r.body)
	}
	if r := deploy(`{"inputs":{"ADMIN_EMAIL":"nope"}}`); r.code != 400 || !strings.Contains(r.body, `"invalid":["ADMIN_EMAIL"]`) {
		t.Errorf("pattern mismatch: %d %s", r.code, r.body)
	}
	if r := deploy(`{"inputs":{"ADMIN_EMAIL":"a@b.co","EXTRA":"1"}}`); r.code != 400 || !strings.Contains(r.body, `"unknown":["EXTRA"]`) {
		t.Errorf("unknown input: %d %s", r.code, r.body)
	}
	before := len(f.queued())
	r := deploy(fmt.Sprintf(`{"environment_id":%q,"inputs":{"ADMIN_EMAIL":"admin@acme.dev"}}`, f.prod.ID))
	if r.code != 201 {
		t.Fatalf("deploy: %d %s", r.code, r.body)
	}
	var svcs []store.Service
	json.Unmarshal([]byte(r.body), &svcs)
	// 대표 서비스(도메인이 있는 api)가 맨 앞, 이미 있는 db와 겹친 이름은 db-2로 바뀐다.
	if len(svcs) != 2 || svcs[0].Name != "api" || svcs[1].Name != "db-2" || *svcs[0].Template != "acme-stack" {
		t.Fatalf("services: %+v", svcs)
	}
	if got := len(f.queued()) - before; got != 2 {
		t.Errorf("enqueued %d deployments", got)
	}
	api, _ := f.st.GetInstanceFor(f.ctx, svcs[0].ID, f.prod.ID)
	v := api.Variables
	if v["DATABASE_URL"] != "${{db-2.DATABASE_URL}}" || v["ADMIN_EMAIL"] != "admin@acme.dev" || v["REGION"] != "eu" || len(v["SESSION_SECRET"]) != 40 || len(v["PIN"]) != 4 {
		t.Errorf("variables: %v", v)
	}
	if api.Replicas != 2 || *api.Port != 3000 || api.HealthcheckPath != "/health" || api.Canvas == nil || api.Canvas.X != 120 || *api.Image != "ghcr.io/acme/api:1" {
		t.Errorf("settings: %+v", api.Spec)
	}
	if len(api.Domains) != 1 || api.Domains[0].Kind != "generated" || api.Domains[0].Host != "api.example.com" || *api.Domains[0].TargetPort != 3000 {
		t.Errorf("domains: %+v", api.Domains)
	}
	if api.TCPProxy == nil || api.TCPProxy.ContainerPort != 9229 || api.TCPProxy.PublicPort < 24100 {
		t.Errorf("tcp proxy: %+v", api.TCPProxy)
	}
	vols, _ := f.st.ListVolumesByInstance(f.ctx, api.ID)
	if len(vols) != 1 || vols[0].MountPath != "/data" || vols[0].SizeLimitMB == nil || *vols[0].SizeLimitMB != 100 {
		t.Errorf("api volumes: %+v", vols)
	}
	db, _ := f.st.GetInstanceFor(f.ctx, svcs[1].ID, f.prod.ID)
	if len(db.Variables["POSTGRES_PASSWORD"]) != 32 || db.Variables["PGPASSWORD"] != "${{POSTGRES_PASSWORD}}" || *db.Image != "postgres:17-alpine" {
		t.Errorf("db: %v", db.Variables)
	}
	dbVols, _ := f.st.ListVolumesByInstance(f.ctx, db.ID)
	if len(dbVols) != 1 || *dbVols[0].SizeLimitMB != 512 {
		t.Errorf("db volumes: %+v", dbVols)
	}
	// 템플릿 정의에는 배포한 값이 남지 않는다.
	ut, _ := f.st.GetTemplateByKey(f.ctx, "acme-stack")
	if strings.Contains(string(ut.Definition), v["SESSION_SECRET"]) || strings.Contains(string(ut.Definition), "admin@acme.dev") || ut.DeployCount != 1 {
		t.Errorf("template after deploy: count=%d", ut.DeployCount)
	}

	// 공개 범위: private 템플릿은 다른 사용자에게 보이지 않는다.
	bob := testUser(t, f.st, "bob@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, bob.ID, store.RoleMember)
	bc := withCookie(testSessionCookie(t, f.srv, bob))
	if w := req(h, "GET", "/api/templates/acme-stack", "", bc); w.Code != 404 {
		t.Errorf("bob sees a private template: %d", w.Code)
	}
	if strings.Contains(req(h, "GET", "/api/templates", "", bc).Body.String(), "acme-stack") {
		t.Error("private template listed for bob")
	}
	if w := req(h, "POST", "/api/projects/"+f.p.ID+"/templates/acme-stack", `{"inputs":{"ADMIN_EMAIL":"a@b.co"}}`, bc); w.Code != 404 {
		t.Errorf("bob deploys a private template: %d", w.Code)
	}
	if w := req(h, "POST", "/api/templates/acme-stack/deploy", fmt.Sprintf(`{"project_id":%q,"inputs":{"ADMIN_EMAIL":"a@b.co"}}`, f.p.ID), bc); w.Code != 404 {
		t.Errorf("bob deploys a private template by key: %d", w.Code)
	}
	if w := req(h, "GET", "/api/templates/acme-stack/export", "", bc); w.Code != 404 {
		t.Errorf("bob exports a private template: %d", w.Code)
	}
	if w := req(h, "PATCH", "/api/templates/acme-stack", `{"name":"x"}`, bc); w.Code != 404 {
		t.Errorf("bob patches a private template: %d", w.Code)
	}
	// workspace로 바꾸면 보이지만 고칠 수는 없다.
	if w := req(h, "PATCH", "/api/templates/acme-stack", `{"visibility":"workspace","description":"shared now"}`, ac); w.Code != 200 {
		t.Fatalf("patch visibility: %d %s", w.Code, w.Body)
	}
	got := decodeBody[templateDetail](t, req(h, "GET", "/api/templates/acme-stack", "", bc))
	if got.Description != "shared now" || got.CanEdit || got.Visibility != "workspace" {
		t.Errorf("bob view: %+v", got.templateView)
	}
	if w := req(h, "PATCH", "/api/templates/acme-stack", `{"name":"x"}`, bc); w.Code != 403 {
		t.Errorf("bob patches a workspace template: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/templates/acme-stack", "", bc); w.Code != 403 {
		t.Errorf("bob deletes: %d", w.Code)
	}
	// 대상 프로젝트 권한: bob이 viewer인 프로젝트에는 배포할 수 없다.
	f.st.SetMember(f.ctx, f.p.ID, bob.ID, store.RoleViewer)
	if w := req(h, "POST", "/api/templates/acme-stack/deploy", fmt.Sprintf(`{"project_id":%q,"inputs":{"ADMIN_EMAIL":"a@b.co"}}`, f.p.ID), bc); w.Code != 403 {
		t.Errorf("viewer deploy: %d %s", w.Code, w.Body)
	}
	f.st.SetMember(f.ctx, f.p.ID, bob.ID, store.RoleMember)
	if w := req(h, "POST", "/api/templates/acme-stack/deploy", fmt.Sprintf(`{"project_id":%q,"inputs":{"ADMIN_EMAIL":"b@b.co"}}`, f.p.ID), bc); w.Code != 201 {
		t.Errorf("member deploy by key: %d %s", w.Code, w.Body)
	}
	// 내보내기는 정의 JSON 그대로
	w = req(h, "GET", "/api/templates/acme-stack/export", "", bc)
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "acme-stack.json") {
		t.Errorf("export: %d %v", w.Code, w.Header())
	}
	if _, err := catalog.ParseDefinition(w.Body.Bytes()); err != nil {
		t.Errorf("exported definition does not parse: %v", err)
	}
	// 목록 필터/정렬
	list := decodeBody[[]templateView](t, req(h, "GET", "/api/templates?sort=popular", "", bc))
	if len(list) == 0 || list[0].Key != "acme-stack" || list[0].DeployCount != 2 {
		t.Errorf("popular first: %+v", list[0])
	}
	if l := decodeBody[[]templateView](t, req(h, "GET", "/api/templates?category=databases", "", bc)); len(l) != 4 {
		t.Errorf("category filter: %d", len(l))
	}
	if l := decodeBody[[]templateView](t, req(h, "GET", "/api/templates?q=acme&tag=node", "", bc)); len(l) != 1 || l[0].Key != "acme-stack" {
		t.Errorf("q+tag filter: %+v", l)
	}
	if l := decodeBody[[]templateView](t, req(h, "GET", "/api/templates?sort=new", "", bc)); l[0].Key != "acme-stack" || !l[len(l)-1].Builtin {
		t.Errorf("new sort: %s", l[0].Key)
	}
	// 작업 공간 admin은 남의 private 템플릿도 지울 수 있다.
	req(h, "PATCH", "/api/templates/acme-stack", `{"visibility":"private"}`, ac)
	admin := testUser(t, f.st, "adm@example.com", store.RoleAdmin)
	adc := withCookie(testSessionCookie(t, f.srv, admin))
	if w := req(h, "GET", "/api/templates/acme-stack", "", adc); w.Code != 200 {
		t.Errorf("admin view: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/templates/acme-stack", "", adc); w.Code != 204 {
		t.Errorf("admin delete: %d", w.Code)
	}
	if w := req(h, "DELETE", "/api/templates/postgres", "", adc); w.Code != 400 {
		t.Errorf("delete built-in: %d", w.Code)
	}
}

type httpResult struct {
	code int
	body string
}

// 내장 템플릿도 새 엔진으로 배포된다(대표 서비스가 앞, DB 볼륨, generated domain, 참조).
func TestBuiltinTemplateDeploy(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	w := req(h, "POST", "/api/projects/"+f.p.ID+"/templates/umami", `{}`, legacy)
	if w.Code != 201 {
		t.Fatalf("deploy umami: %d %s", w.Code, w.Body)
	}
	svcs := decodeBody[[]store.Service](t, w)
	if len(svcs) != 2 || svcs[0].Name != "umami" || svcs[1].Name != "postgres" {
		t.Fatalf("services: %+v", svcs)
	}
	um, _ := f.st.GetInstanceFor(f.ctx, svcs[0].ID, f.prod.ID)
	if um.Variables["DATABASE_URL"] != "${{postgres.DATABASE_URL}}" || len(um.Variables["APP_SECRET"]) != 48 || um.HealthcheckTimeoutS != 300 ||
		len(um.Domains) != 1 || um.Domains[0].Host != "umami.example.com" || um.RestartPolicy != "always" {
		t.Errorf("umami: %+v %v", um.Spec, um.Variables)
	}
	pg, _ := f.st.GetInstanceFor(f.ctx, svcs[1].ID, f.prod.ID)
	if vols, _ := f.st.ListVolumesByInstance(f.ctx, pg.ID); len(vols) != 1 || vols[0].MountPath != "/var/lib/postgresql/data" {
		t.Errorf("pg volumes: %+v", vols)
	}
	// 두 번째 환경은 DB 비밀번호가 다르다.
	staging := &store.Environment{ProjectID: f.p.ID, Name: "staging"}
	f.st.CreateEnvironment(f.ctx, staging)
	w = req(h, "POST", "/api/projects/"+f.p.ID+"/templates/redis", `{}`, legacy)
	if w.Code != 201 {
		t.Fatalf("deploy redis: %d %s", w.Code, w.Body)
	}
	rs := decodeBody[[]store.Service](t, w)
	a, _ := f.st.GetInstanceFor(f.ctx, rs[0].ID, f.prod.ID)
	b, _ := f.st.GetInstanceFor(f.ctx, rs[0].ID, staging.ID)
	if a.Variables["REDIS_PASSWORD"] == b.Variables["REDIS_PASSWORD"] || b.Variables["REDIS_PASSWORD"] == "" || *b.StartCommand == "" {
		t.Errorf("per-env password: %q %q", a.Variables["REDIS_PASSWORD"], b.Variables["REDIS_PASSWORD"])
	}
	detail := decodeBody[templateDetail](t, req(h, "GET", "/api/templates/umami", "", legacy))
	if !detail.Builtin || detail.DeployCount != 1 || detail.Readme == "" || len(detail.Definition) == 0 || detail.Category != "analytics" {
		t.Errorf("builtin detail: %+v", detail.templateView)
	}
	if w := req(h, "GET", "/api/templates/nope", "", legacy); w.Code != 404 {
		t.Errorf("unknown template: %d", w.Code)
	}
}

func TestExportEnvironmentTemplateRedaction(t *testing.T) {
	f, h := authServer(t)
	legacy := withBearer("legacy-token")
	w0 := req(h, "POST", "/api/projects/"+f.p.ID+"/services", `{"name":"pg","source_type":"database","database_kind":"postgres"}`, legacy)
	if w0.Code != 201 {
		t.Fatalf("create pg: %d %s", w0.Code, w0.Body)
	}
	pgSvc := decodeBody[store.Service](t, w0)
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	tp := 8080
	web.Domains = append(web.Domains, store.Domain{Host: "web.example.com", Kind: "generated", TargetPort: &tp})
	web.Variables = map[string]string{
		"API_TOKEN":       "tok-literal-123",
		"SECRET_KEY_BASE": "skb-literal-456",
		"DATABASE_URL":    "${{pg.DATABASE_URL}}",
		"DB_PASSWORD":     "${{pg.POSTGRES_PASSWORD}}",
		"GREETING":        "hello",
		"LEGACY_DSN":      "postgres://u:dsn-pass-789@h:5432/x",
		"OUTSIDE":         "${{ghost.X}}",
	}
	r := 3
	web.Replicas = r
	f.st.UpdateInstanceConfig(f.ctx, web)
	db, _ := f.st.GetInstanceFor(f.ctx, pgSvc.ID, f.prod.ID)
	db.TCPProxy = &store.TCPProxy{ContainerPort: 5432, PublicPort: 21987}
	f.st.UpdateInstanceConfig(f.ctx, db)
	pw := db.Variables["POSTGRES_PASSWORD"]
	// 픽스처의 db는 database_kind 없는 서비스라 이미지 서비스로 나간다(비밀번호는 그래도 숨긴다).
	fixturePW, _ := f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	pw2 := fixturePW.Variables["POSTGRES_PASSWORD"]

	export := func(q string) (string, map[string]any) {
		w := req(h, "POST", "/api/projects/"+f.p.ID+"/environments/"+f.prod.ID+"/export-template"+q, "", legacy)
		if w.Code != 200 {
			t.Fatalf("export%s: %d %s", q, w.Code, w.Body)
		}
		var out map[string]any
		json.Unmarshal(w.Body.Bytes(), &out)
		return w.Body.String(), out
	}
	body, out := export("")
	for _, leak := range []string{"tok-literal-123", "skb-literal-456", "dsn-pass-789", pw, pw2, "app.acme.dev", "21987", "web.example.com"} {
		if strings.Contains(body, leak) {
			t.Errorf("export leaks %q: %s", leak, body)
		}
	}
	def, _ := json.Marshal(out["definition"])
	d, err := catalog.ParseDefinition(def)
	if err != nil {
		t.Fatalf("exported definition invalid: %v\n%s", err, def)
	}
	byName := map[string]catalog.ServiceSpec{}
	for _, sv := range d.Services {
		byName[sv.Name] = sv
	}
	w := byName["web"]
	if w.Source.Repo == nil || w.Source.Repo.URL != "https://github.com/acme/app.git" || *w.Replicas != 3 {
		t.Errorf("web source/settings: %+v", w)
	}
	if w.Variables["API_TOKEN"] != "${{ secret(32) }}" || w.Variables["LEGACY_DSN"] != "${{ secret(32) }}" || w.Variables["DATABASE_URL"] != "${{pg.DATABASE_URL}}" ||
		w.Variables["DB_PASSWORD"] != "${{pg.POSTGRES_PASSWORD}}" || w.Variables["GREETING"] != "hello" {
		t.Errorf("web variables: %v", w.Variables)
	}
	if _, ok := w.Variables["OUTSIDE"]; ok {
		t.Error("reference outside the template kept")
	}
	if len(w.Domains) != 1 || !w.Domains[0].Generate || *w.Domains[0].TargetPort != 8080 {
		t.Errorf("web domains (custom must be dropped): %+v", w.Domains)
	}
	dbs := byName["pg"]
	if dbs.Source.Database != "postgres" || len(dbs.Variables) != 0 || dbs.TCPProxy == nil || dbs.TCPProxy.ContainerPort != 5432 || len(dbs.Volumes) != 0 ||
		dbs.Port != nil || dbs.HealthcheckType != nil {
		t.Errorf("db export: %+v", dbs)
	}
	if v := byName["db"].Variables["POSTGRES_PASSWORD"]; v != "${{ secret(32) }}" {
		t.Errorf("image service password: %q", v)
	}
	if d.Primary != "web" || len(out["warnings"].([]any)) == 0 {
		t.Errorf("primary %s warnings %v", d.Primary, out["warnings"])
	}

	// secrets=inputs: 비밀은 필수 입력이 된다(값은 기본값에도 남지 않는다).
	body, out = export("?secrets=inputs")
	if strings.Contains(body, "tok-literal-123") {
		t.Error("inputs mode leaks the value")
	}
	def, _ = json.Marshal(out["definition"])
	d, err = catalog.ParseDefinition(def)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]catalog.Input{}
	for _, in := range d.Inputs {
		keys[in.Key] = in
	}
	if in, ok := keys["WEB_API_TOKEN"]; !ok || !in.Required || !in.Secret || in.Default != "" {
		t.Errorf("inputs: %+v", d.Inputs)
	}
	if web := d.Services[0]; web.Name == "web" && web.Variables["API_TOKEN"] != "${{ inputs.WEB_API_TOKEN }}" {
		t.Errorf("input ref: %v", web.Variables)
	}
	if w := req(h, "POST", "/api/projects/"+f.p.ID+"/environments/"+f.prod.ID+"/export-template?secrets=plain", "", legacy); w.Code != 400 {
		t.Errorf("bad secrets mode: %d", w.Code)
	}
	// 검토한 정의를 그대로 저장하고 다시 배포할 수 있다.
	save, _ := json.Marshal(map[string]any{"key": "acme-env", "visibility": "workspace", "definition": json.RawMessage(def)})
	if w := req(h, "POST", "/api/templates", string(save), legacy); w.Code != 201 {
		t.Fatalf("save exported: %d %s", w.Code, w.Body)
	}
	inputs := map[string]string{}
	for k := range keys {
		inputs[k] = "v-" + strings.ToLower(k)
	}
	dep, _ := json.Marshal(map[string]any{"inputs": inputs})
	if w := req(h, "POST", "/api/projects/"+f.p.ID+"/templates/acme-env", string(dep), legacy); w.Code != 201 {
		t.Errorf("redeploy exported: %d %s", w.Code, w.Body)
	}
	// viewer는 내보낼 수 없다(변수 값을 읽는 작업).
	viewer := testUser(t, f.st, "v@example.com", store.RoleMember)
	f.st.SetMember(f.ctx, f.p.ID, viewer.ID, store.RoleViewer)
	if w := req(h, "POST", "/api/projects/"+f.p.ID+"/environments/"+f.prod.ID+"/export-template", "", withCookie(testSessionCookie(t, f.srv, viewer))); w.Code != http.StatusForbidden {
		t.Errorf("viewer export: %d", w.Code)
	}
}
