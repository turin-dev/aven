package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/iac"
	"github.com/turin-dev/aven/internal/store"
)

// stubIACRuntime은 컨테이너·프록시 작업을 store 삭제로 바꾼다(테스트 fixture에는 docker/caddy가 없다).
func stubIACRuntime(t *testing.T) {
	del, sync := iacDeleteService, iacSyncProxy
	iacDeleteService = func(s *Server, ctx context.Context, svc *store.Service) error {
		return s.Store.DeleteService(ctx, svc.ID)
	}
	iacSyncProxy = func(*Server, context.Context) error { return nil }
	t.Cleanup(func() { iacDeleteService, iacSyncProxy = del, sync })
}

func (f *ghFixture) iacPV() map[string]string {
	return map[string]string{"pid": f.p.ID, "eid": f.prod.ID}
}

func (f *ghFixture) iacExport(include bool) *iac.Definition {
	f.t.Helper()
	q := "/x"
	if include {
		q += "?include_variables=1"
	}
	code, out := f.call(f.srv.exportIACConfig, "GET", q, "", f.iacPV())
	if code != 200 {
		f.t.Fatalf("export: %d %v", code, out)
	}
	b, _ := json.Marshal(out)
	var d iac.Definition
	if err := json.Unmarshal(b, &d); err != nil {
		f.t.Fatal(err)
	}
	return &d
}

func (f *ghFixture) iacPlan(d *iac.Definition) *iacPlan {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"definition": d})
	code, out := f.call(f.srv.planIACConfig, "POST", "/x", string(body), f.iacPV())
	if code != 200 {
		f.t.Fatalf("plan: %d %v", code, out)
	}
	b, _ := json.Marshal(out)
	var p iacPlan
	json.Unmarshal(b, &p)
	return &p
}

func (f *ghFixture) iacApply(d *iac.Definition, hash string, destructive bool) (int, map[string]any) {
	f.t.Helper()
	body, _ := json.Marshal(map[string]any{"definition": d, "plan_hash": hash, "confirm_destructive": destructive})
	return f.call(f.srv.applyIACConfig, "POST", "/x", string(body), f.iacPV())
}

func iacSvc(d *iac.Definition, name string) *iac.Service {
	for i := range d.Services {
		if d.Services[i].Name == name {
			return &d.Services[i]
		}
	}
	return nil
}

func changeKeys(p *iacPlan) string {
	out := []string{}
	for _, c := range p.Changes {
		k := c.Action + " " + c.Resource
		if c.Field != "" {
			k += "." + c.Field
		}
		out = append(out, k)
	}
	return strings.Join(out, ",")
}

func TestIACExportOmitsDefaultsAndRoundTripsClean(t *testing.T) {
	f := newGHFixture(t)
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	web.Variables = map[string]string{"API_KEY": "sk-live", "DB": "${{db.DATABASE_URL}}"}
	web.Domains = append([]store.Domain{{Host: "web-production.example.com", Kind: "generated"}}, web.Domains...)
	f.st.UpdateInstanceConfig(f.ctx, web)

	d := f.iacExport(false)
	if d.ProjectName != "acme" || len(d.Services) != 3 {
		t.Fatalf("definition: %+v", d)
	}
	w := iacSvc(d, "web")
	if w.SourceType != "repo" || string(w.Settings["repo_url"]) != `"https://github.com/acme/app.git"` {
		t.Errorf("web settings: %v", w.Settings)
	}
	for _, k := range []string{"branch", "root_dir", "builder", "healthcheck_timeout_s", "tcp_proxy", "compose"} {
		if _, ok := w.Settings[k]; ok {
			t.Errorf("default/unsupported setting %s exported", k)
		}
	}
	if doms := string(w.Settings["domains"]); !strings.Contains(doms, "app.acme.dev") || strings.Contains(doms, "web-production") {
		t.Errorf("domains: %s", doms)
	}
	vs := *w.Variables
	if !vs["API_KEY"].Preserve || vs["DB"].Value != "${{db.DATABASE_URL}}" {
		t.Errorf("variables: %+v", vs)
	}
	if db := iacSvc(d, "db"); db.SourceType != "database" || !(*db.Variables)["POSTGRES_PASSWORD"].Preserve {
		t.Errorf("db: %+v", db)
	}
	if v := *iacSvc(f.iacExport(true), "web").Variables; v["API_KEY"].Value != "sk-live" {
		t.Errorf("include_variables: %+v", v)
	}

	p := f.iacPlan(d)
	if len(p.Errors) != 0 || len(p.Changes) != 0 || p.PlanHash == "" || p.ConfigETag == "" {
		t.Fatalf("clean import plan: %+v", p)
	}
}

func TestIACPlanApplyStagesChanges(t *testing.T) {
	f := newGHFixture(t)
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	web.Variables = map[string]string{"KEEP": "1", "DROP": "x", "SECRET": "s3"}
	f.st.UpdateInstanceConfig(f.ctx, web)
	if err := f.st.SealVariables(f.ctx, web.ID, web.Variables, []string{"SECRET"}); err != nil {
		t.Fatal(err)
	}

	d := f.iacExport(false)
	w := iacSvc(d, "web")
	w.Settings["start_command"] = json.RawMessage(`"npm start"`)
	w.Settings["replicas"] = json.RawMessage(`2`)
	vs := *w.Variables
	delete(vs, "DROP")
	vs["NEW"] = iac.Variable{Value: "hello"}

	p := f.iacPlan(d)
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	want := "change service.web.replicas,change service.web.start_command,change service.web.variables.DROP,change service.web.variables.NEW"
	if got := changeKeys(p); got != want {
		t.Fatalf("changes:\n got %s\nwant %s", got, want)
	}
	if !p.Destructive || p.Summary.Change != 1 {
		t.Errorf("removing a variable is destructive: %+v", p)
	}
	for _, c := range p.Changes {
		if c.Field == "variables.NEW" && string(c.To) != `"«hidden»"` {
			t.Errorf("variable value not redacted: %s", c.To)
		}
	}

	if code, _ := f.iacApply(d, "", true); code != 409 {
		t.Errorf("apply without plan hash: %d", code)
	}
	if code, _ := f.iacApply(d, p.PlanHash, false); code != 409 {
		t.Errorf("destructive apply without confirm: %d", code)
	}
	code, out := f.iacApply(d, p.PlanHash, true)
	if code != 200 || out["staged"] != true {
		t.Fatalf("apply: %d %v", code, out)
	}
	got, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	if got.StartCommand == nil || *got.StartCommand != "npm start" || got.Replicas != 2 {
		t.Errorf("spec not saved: %+v", got.Spec)
	}
	if got.Variables["NEW"] != "hello" || got.Variables["SECRET"] != "s3" || got.Variables["KEEP"] != "1" {
		t.Errorf("variables: %v", got.Variables)
	}
	if _, ok := got.Variables["DROP"]; ok {
		t.Error("DROP kept")
	}
	if len(f.queued()) != 0 {
		t.Errorf("apply must not deploy without deploy=true: %v", f.queued())
	}
	// staged: 현재 배포와 비교하면 pending_changes에 보인다.
	pc := strings.Join(PendingChanges(got, &store.Deployment{ReqSpec: web.Spec, VarsRaw: web.Variables}), ",")
	if !strings.Contains(pc, "start_command") || !strings.Contains(pc, "variables.NEW") {
		t.Errorf("pending changes: %s", pc)
	}
	if p2 := f.iacPlan(d); len(p2.Changes) != 0 {
		t.Errorf("re-plan after apply: %s", changeKeys(p2))
	}

	// 봉인된 변수는 값을 바꿀 수 없다.
	(*w.Variables)["SECRET"] = iac.Variable{Value: "other"}
	if p := f.iacPlan(d); len(p.Errors) != 1 || !strings.Contains(p.Errors[0], "sealed") {
		t.Errorf("sealed: %v", p.Errors)
	}
}

func TestIACStalePlanRejected(t *testing.T) {
	f := newGHFixture(t)
	d := f.iacExport(false)
	iacSvc(d, "web").Settings["start_command"] = json.RawMessage(`"a"`)
	p := f.iacPlan(d)
	web, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	cmd := "dashboard edit"
	web.PreDeployCommand = &cmd
	f.st.UpdateInstanceConfig(f.ctx, web)
	if code, out := f.iacApply(d, p.PlanHash, false); code != 409 {
		t.Errorf("stale plan: %d %v", code, out)
	}
}

func TestIACCreateAndDestroyServices(t *testing.T) {
	stubIACRuntime(t)
	f := newGHFixture(t)
	d := f.iacExport(false)
	// other를 빼고 image 서비스와 redis를 추가한다.
	kept := []iac.Service{}
	for _, s := range d.Services {
		if s.Name != "other" {
			kept = append(kept, s)
		}
	}
	vars := map[string]iac.Variable{"MODE": {Value: "worker"}}
	d.Services = append(kept,
		iac.Service{Name: "worker", SourceType: "image", Settings: map[string]json.RawMessage{"image": json.RawMessage(`"nginx:alpine"`), "replicas": json.RawMessage(`3`)}, Variables: &vars},
		iac.Service{Name: "cache", SourceType: "database", DatabaseKind: "redis"},
	)
	p := f.iacPlan(d)
	if len(p.Errors) != 0 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if got := changeKeys(p); got != "create service.worker,create service.cache,destroy service.other" {
		t.Fatalf("changes: %s", got)
	}
	if p.Summary.Create != 2 || p.Summary.Destroy != 1 || !p.Destructive {
		t.Errorf("summary: %+v", p.Summary)
	}
	code, out := f.iacApply(d, p.PlanHash, true)
	if code != 200 {
		t.Fatalf("apply: %d %v", code, out)
	}
	worker := findService(t, f, "worker")
	in, err := f.st.GetInstanceFor(f.ctx, worker.ID, f.prod.ID)
	if err != nil {
		t.Fatal(err)
	}
	if in.Image == nil || *in.Image != "nginx:alpine" || in.Replicas != 3 || in.Variables["MODE"] != "worker" {
		t.Errorf("worker: %+v %v", in.Spec, in.Variables)
	}
	cache := findService(t, f, "cache")
	cin, _ := f.st.GetInstanceFor(f.ctx, cache.ID, f.prod.ID)
	if cache.DatabaseKind == nil || *cache.DatabaseKind != "redis" || cin.Variables["REDIS_PASSWORD"] == "" {
		t.Errorf("cache: %+v %v", cache, cin.Variables)
	}
	svcs, _ := f.st.ListServices(f.ctx, f.p.ID)
	for _, s := range svcs {
		if s.Name == "other" {
			t.Error("other not deleted")
		}
	}
	if p2 := f.iacPlan(d); len(p2.Changes) != 0 || len(p2.Errors) != 0 {
		t.Errorf("re-plan: %s %v", changeKeys(p2), p2.Errors)
	}
}

func TestIACPartialOwnership(t *testing.T) {
	stubIACRuntime(t)
	f := newGHFixture(t)
	full := f.iacExport(false)
	api := &iac.Definition{Version: 1, Partial: "api", Services: []iac.Service{*iacSvc(full, "web"), *iacSvc(full, "db")}}
	p := f.iacPlan(api)
	if len(p.Errors) != 0 || len(p.Changes) != 0 || strings.Join(p.Claims, ",") != "service.db,service.web" {
		t.Fatalf("partial plan: %+v", p)
	}
	if code, out := f.iacApply(api, p.PlanHash, false); code != 200 {
		t.Fatalf("claim apply: %d %v", code, out)
	}
	code, out := f.call(f.srv.listIACPartials, "GET", "/x", "", f.iacPV())
	if code != 200 || out["whole_project_available"] != false || out["iac_partials"].(map[string]any)["service.web"] != "api" {
		t.Fatalf("partials: %v", out)
	}

	// 전체 프로젝트 정의는 named partial이 있으면 막힌다.
	if p := f.iacPlan(full); len(p.Errors) == 0 || !strings.Contains(p.Errors[0], "named IaC partials") {
		t.Errorf("whole project with partials: %v", p.Errors)
	}
	// 다른 partial은 api 소유 서비스를 선언할 수 없고, 소유하지 않은 서비스는 지우지 않는다.
	ops := &iac.Definition{Version: 1, Partial: "ops", Services: []iac.Service{*iacSvc(full, "web")}}
	if p := f.iacPlan(ops); len(p.Errors) != 1 || !strings.Contains(p.Errors[0], `already managed by partial "api"`) {
		t.Errorf("foreign partial: %v", p.Errors)
	}
	ops.Services = []iac.Service{*iacSvc(full, "other")}
	if p := f.iacPlan(ops); len(p.Errors) != 0 || len(p.Changes) != 0 {
		t.Errorf("ops partial must not destroy api resources: %s %v", changeKeys(p), p.Errors)
	}
	// api가 db를 빼면 db만 삭제 대상이다.
	api.Services = api.Services[:1]
	if got := changeKeys(f.iacPlan(api)); got != "destroy service.db" {
		t.Errorf("api omitting db: %s", got)
	}

	// transfer dry run → 실제 transfer → release.
	etag := out["config_etag"].(string)
	code, out = f.call(f.srv.transferIACPartial, "POST", "/x", `{"partial":"api","to":"core","resources":["service.db"],"dry_run":true,"base_config_etag":"`+etag+`"}`, f.iacPV())
	if code != 200 || out["dry_run"] != true || out["iac_partials"].(map[string]any)["service.db"] != "core" {
		t.Fatalf("transfer dry run: %d %v", code, out)
	}
	if o, _ := f.st.ListIACOwnership(f.ctx, f.prod.ID); o["service.db"] != "api" {
		t.Error("dry run changed ownership")
	}
	if code, _ := f.call(f.srv.transferIACPartial, "POST", "/x", `{"partial":"api","to":"core","base_config_etag":"stale"}`, f.iacPV()); code != 409 {
		t.Errorf("stale etag: %d", code)
	}
	if code, out := f.call(f.srv.transferIACPartial, "POST", "/x", `{"partial":"api","to":"core","resources":["service.other"]}`, f.iacPV()); code != 400 {
		t.Errorf("transfer of unowned resource: %d %v", code, out)
	}
	if code, out := f.call(f.srv.transferIACPartial, "POST", "/x", `{"partial":"api","to":"core","resources":["service.db"]}`, f.iacPV()); code != 200 {
		t.Fatalf("transfer: %d %v", code, out)
	}
	for _, partial := range []string{"api", "core"} {
		code, out := f.call(f.srv.releaseIACPartial, "POST", "/x", `{"partial":"`+partial+`"}`, f.iacPV())
		if code != 200 {
			t.Fatalf("release %s: %d %v", partial, code, out)
		}
		if partial == "core" && out["whole_project_available"] != true {
			t.Errorf("after releasing everything: %v", out)
		}
	}
	if p := f.iacPlan(full); len(p.Errors) != 0 {
		t.Errorf("whole project after release: %v", p.Errors)
	}
}

func TestIACDecodeYAMLAndValidation(t *testing.T) {
	d, err := iac.Decode([]byte("version: 1\npartial: web\nservices:\n  - name: web\n    settings:\n      replicas: 2\n      domains: [app.example.com]\n    variables:\n      A: x\n      B: {preserve: true}\n"), "yaml")
	if err != nil {
		t.Fatal(err)
	}
	if string(d.Services[0].Settings["replicas"]) != "2" || !(*d.Services[0].Variables)["B"].Preserve {
		t.Errorf("decoded: %+v", d.Services[0])
	}
	doms, err := parseIACDomains(d.Services[0].Settings["domains"])
	if err != nil || len(doms) != 1 || doms[0].Host != "app.example.com" {
		t.Errorf("domains: %v %v", doms, err)
	}
	for _, bad := range []string{
		`{"version":2,"services":[]}`,
		`{"version":1,"partial":"bad name","services":[]}`,
		`{"version":1,"services":[{"name":"a"},{"name":"a"}]}`,
		`{"version":1,"services":[{"name":"a","variables":{"X":1}}]}`,
		`{"version":1,"services":[],"extra":true}`,
	} {
		if _, err := iac.Decode([]byte(bad), "json"); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestIACRejectsUnsupportedSettings(t *testing.T) {
	f := newGHFixture(t)
	d := f.iacExport(false)
	iacSvc(d, "web").Settings["tcp_proxy"] = json.RawMessage(`{"container_port":5432}`)
	iacSvc(d, "other").Settings["replicas"] = json.RawMessage(`99`)
	iacSvc(d, "db").SourceType = "image"
	p := f.iacPlan(d)
	if len(p.Errors) != 3 {
		t.Fatalf("errors: %v", p.Errors)
	}
	if code, _ := f.iacApply(d, p.PlanHash, true); code != 422 {
		t.Errorf("apply with errors: %d", code)
	}
}

func TestIACApplyDeploy(t *testing.T) {
	f := newGHFixture(t)
	d := f.iacExport(false)
	iacSvc(d, "web").Settings["start_command"] = json.RawMessage(`"npm start"`)
	p := f.iacPlan(d)
	body, _ := json.Marshal(map[string]any{"definition": d, "plan_hash": p.PlanHash, "deploy": true})
	code, out := f.call(f.srv.applyIACConfig, "POST", "/x", string(body), f.iacPV())
	if code != 200 || out["staged"] != false {
		t.Fatalf("apply: %d %v", code, out)
	}
	svcs := out["services"].([]any)
	if len(svcs) != 1 || svcs[0].(map[string]any)["deployment_id"] == nil || len(f.queued()) != 1 {
		t.Errorf("deploy: %v queued=%v", svcs, f.queued())
	}
}
