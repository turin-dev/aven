package iac

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func needJS(t *testing.T) {
	t.Helper()
	if _, _, err := Runner("x.ts"); err != nil {
		t.Skip(err)
	}
	if _, err := exec.LookPath("bun"); err != nil && os.Getenv("KEEL_IAC_RUNNER") == "" {
		// node needs type stripping (22.6+) for aven.ts
		out, err := exec.Command("node", "--no-warnings", "--experimental-strip-types", "-e", "1").CombinedOutput()
		if err != nil {
			t.Skipf("node without --experimental-strip-types: %s", out)
		}
	}
}

func normalize(t *testing.T, d *Definition) any {
	t.Helper()
	b, err := MarshalJSON(d)
	if err != nil {
		t.Fatal(err)
	}
	var v any
	json.Unmarshal(b, &v)
	return v
}

func sample() *Definition {
	vars := map[string]Variable{"API_KEY": {Preserve: true}, "DB": {Value: "${{db.DATABASE_URL}}"}, "weird-key": {Value: "a\"b\nc"}}
	dbVars := map[string]Variable{"POSTGRES_PASSWORD": {Preserve: true}}
	empty := map[string]Variable{}
	return &Definition{Version: 1, ProjectName: "acme", Partial: "api", Services: []Service{
		{Name: "web", SourceType: "repo", Settings: map[string]json.RawMessage{
			"repo_url": json.RawMessage(`"https://github.com/acme/app"`), "branch": json.RawMessage(`"dev"`),
			"start_command": json.RawMessage(`"npm start"`), "replicas": json.RawMessage(`2`),
			"domains":   json.RawMessage(`[{"host":"app.acme.dev","kind":"custom","target_port":null},{"host":"api.acme.dev","kind":"custom","target_port":3000}]`),
			"autoscale": json.RawMessage(`{"enabled":true,"max_replicas":4}`),
		}, Variables: &vars},
		{Name: "worker-2", SourceType: "repo", Settings: map[string]json.RawMessage{
			"repo_url": json.RawMessage(`"git@gitlab.com:acme/w.git"`), "root_dir": json.RawMessage(`"/apps/w"`),
			"domains": json.RawMessage(`[{"host":"x.acme.dev","kind":"custom","target_port":null,"tls":"internal"}]`),
		}, Variables: &empty},
		{Name: "proxy", SourceType: "image", Settings: map[string]json.RawMessage{"image": json.RawMessage(`"nginx:alpine"`)}},
		{Name: "db", SourceType: "database", DatabaseKind: "postgres", Settings: map[string]json.RawMessage{"image": json.RawMessage(`"postgres:17-alpine"`), "port": json.RawMessage(`5432`)}, Variables: &dbVars},
		{Name: "fn", SourceType: "function"},
	}}
}

func TestRenderTSRoundTrip(t *testing.T) {
	needJS(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, Dir)
	if err := WriteSupportFiles(cfg); err != nil {
		t.Fatal(err)
	}
	want := sample()
	src := RenderTS(want)
	file := filepath.Join(cfg, "aven.ts")
	os.WriteFile(file, src, 0o644)
	got, err := Load(file, Context{Command: "plan", Environment: "production"})
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	// 기본 도메인 kind/target_port null은 DSL이 생략한다(서버가 정규화한다).
	w := want.Services[0]
	w.Settings["domains"] = json.RawMessage(`[{"host":"app.acme.dev"},{"host":"api.acme.dev","target_port":3000}]`)
	if g, w := normalize(t, got), normalize(t, want); !reflect.DeepEqual(g, w) {
		gb, _ := json.MarshalIndent(g, "", " ")
		wb, _ := json.MarshalIndent(w, "", " ")
		t.Fatalf("round trip differs\n--- got\n%s\n--- want\n%s\n--- source\n%s", gb, wb, src)
	}
	for _, s := range []string{`import { defineAven, functionService, git, github, image, postgres, preserve, project, service } from "./keel-iac.mjs";`,
		`export const partial = "api";`, `source: github("acme/app", { branch: "dev" }),`, `const worker2 = service("worker-2"`, `"weird-key": "a\"b\nc",`} {
		if !strings.Contains(string(src), s) {
			t.Errorf("source lacks %s:\n%s", s, src)
		}
	}
}

func TestEvaluateDSL(t *testing.T) {
	needJS(t)
	dir := t.TempDir()
	cfg := filepath.Join(dir, Dir)
	WriteSupportFiles(cfg)
	os.WriteFile(filepath.Join(cfg, "aven.ts"), []byte(`
import { defineKeel, github, group, postgres, preserve, project, service, type KeelContext } from "./keel-iac.mjs";

export const partial = "backend";

export default defineKeel((ctx: KeelContext) => {
  console.log("evaluating", ctx.command);
  const prod: boolean = ctx.isEnvironment("production");
  const db = postgres("db");
  const api = service("api", {
    source: github("acme/api", { rootDirectory: "apps/api" }),
    replicas: prod ? 3 : 1,
    healthcheck: "/health",
    domains: prod ? ["api.acme.dev", { domain: "admin.acme.dev", port: 8081 }] : [],
    env: { DATABASE_URL: db.env.DATABASE_URL, SENTRY: ctx.shared.SENTRY_DSN, SECRET: preserve(), PORT: 8080 },
    settings: { wait_for_ci: true },
  });
  return project(ctx.projectName, { resources: [group("Backend", [api, db]), api] });
});
`), 0o644)
	d, err := Load(filepath.Join(cfg, "aven.ts"), Context{Command: "plan", ProjectName: "acme", Environment: "production"})
	if err != nil {
		t.Fatal(err)
	}
	if d.Partial != "backend" || d.ProjectName != "acme" || len(d.Services) != 2 {
		t.Fatalf("definition: %+v", d)
	}
	api := d.Services[0]
	if api.Name != "api" || api.SourceType != "repo" || string(api.Settings["replicas"]) != "3" || string(api.Settings["root_dir"]) != `"apps/api"` ||
		string(api.Settings["healthcheck_path"]) != `"/health"` || string(api.Settings["wait_for_ci"]) != "true" {
		t.Errorf("api: %+v", api.Settings)
	}
	if got := string(api.Settings["domains"]); got != `[{"host":"api.acme.dev"},{"host":"admin.acme.dev","target_port":8081}]` {
		t.Errorf("domains: %s", got)
	}
	v := *api.Variables
	if v["DATABASE_URL"].Value != "${{db.DATABASE_URL}}" || v["SENTRY"].Value != "${{shared.SENTRY_DSN}}" || !v["SECRET"].Preserve || v["PORT"].Value != "8080" {
		t.Errorf("variables: %+v", v)
	}
	if d.Services[1].DatabaseKind != "postgres" || d.Services[1].SourceType != "database" {
		t.Errorf("db: %+v", d.Services[1])
	}

	os.WriteFile(filepath.Join(cfg, "aven.ts"), []byte(`import { defineKeel, project, service } from "./keel-iac.mjs";
export default defineKeel(() => project("x", { resources: [service("a", { strat: "typo" })] }));`), 0o644)
	if _, err := Load(filepath.Join(cfg, "aven.ts"), Context{}); err == nil || !strings.Contains(err.Error(), `unknown option "strat"`) {
		t.Errorf("unknown option: %v", err)
	}
}

func TestFindFileAndFormats(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "apps", "web")
	os.MkdirAll(nested, 0o755)
	if _, err := FindFile(nested); err == nil {
		t.Error("found a file in an empty tree")
	}
	os.MkdirAll(filepath.Join(root, Dir), 0o755)
	yml := filepath.Join(root, Dir, "aven.yaml")
	os.WriteFile(yml, []byte("version: 1\nservices:\n  - name: web\n    variables:\n      A: x\n"), 0o644)
	got, err := FindFile(nested)
	if err != nil || got != yml {
		t.Fatalf("FindFile: %s %v", got, err)
	}
	d, err := Load(got, Context{})
	if err != nil || (*d.Services[0].Variables)["A"].Value != "x" {
		t.Fatalf("yaml: %+v %v", d, err)
	}
	os.WriteFile(filepath.Join(root, Dir, "aven.json"), []byte(`{"version":1,"services":[]}`), 0o644)
	if _, err := FindFile(nested); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Errorf("two files: %v", err)
	}
	y, err := MarshalYAML(sample())
	if err != nil {
		t.Fatal(err)
	}
	back, err := Decode(y, "yaml")
	if err != nil {
		t.Fatalf("%v\n%s", err, y)
	}
	if !reflect.DeepEqual(normalize(t, back), normalize(t, sample())) {
		t.Errorf("yaml round trip:\n%s", y)
	}
}

func TestJSIdent(t *testing.T) {
	used := map[string]bool{}
	for in, want := range map[string]string{"web": "web", "my-api": "myApi", "2fa": "svc2fa", "service": "serviceService", "class": "classService"} {
		if got := jsIdent(in, used); got != want {
			t.Errorf("%s → %s, want %s", in, got, want)
		}
	}
	if got := jsIdent("my_api", used); got != "myApi2" {
		t.Errorf("dedupe: %s", got)
	}
}
