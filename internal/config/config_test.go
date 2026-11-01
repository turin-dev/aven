package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/store"
)

func TestJSON(t *testing.T) {
	f, err := Parse("railway.json", []byte(`{
		"$schema": "https://railway.com/railway.schema.json",
		"build": {"builder": "DOCKERFILE", "dockerfilePath": "docker/Dockerfile.prod", "watchPatterns": ["src/**"]},
		"deploy": {"startCommand": "node dist/main.js", "preDeployCommand": ["npm run migrate", "npm run seed"],
			"healthcheckPath": "/health", "healthcheckTimeout": 60, "restartPolicyType": "NEVER",
			"restartPolicyMaxRetries": 3, "numReplicas": 2, "cronSchedule": "*/5 * * * *"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	sp := store.Spec{}
	sp.Defaults()
	changed, err := f.Apply(&sp)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Builder != "dockerfile" || sp.DockerfilePath != "docker/Dockerfile.prod" || sp.WatchPaths[0] != "src/**" ||
		*sp.StartCommand != "node dist/main.js" || *sp.PreDeployCommand != "npm run migrate && npm run seed" ||
		sp.HealthcheckPath != "/health" || sp.HealthcheckTimeoutS != 60 || sp.RestartPolicy != "never" ||
		sp.RestartMaxRetries != 3 || sp.Replicas != 2 || *sp.CronSchedule != "*/5 * * * *" {
		t.Errorf("unexpected spec: %+v", sp)
	}
	if len(changed) != 11 {
		t.Errorf("changed = %v", changed)
	}
}

func TestTOMLAndFind(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "railway.toml"), []byte(`
[build]
builder = "RAILPACK"

[deploy]
startCommand = "python app.py"
preDeployCommand = "python migrate.py"
numReplicas = 3
restartPolicyType = "ON_FAILURE"
`), 0o644)
	name, f, err := Find(dir)
	if err != nil || name != "railway.toml" {
		t.Fatalf("find: %q %v", name, err)
	}
	sp := store.Spec{}
	sp.Defaults()
	if _, err := f.Apply(&sp); err != nil {
		t.Fatal(err)
	}
	if sp.Builder != "railpack" || *sp.StartCommand != "python app.py" || *sp.PreDeployCommand != "python migrate.py" || sp.Replicas != 3 || sp.RestartPolicy != "on_failure" {
		t.Errorf("unexpected spec: %+v", sp)
	}

	os.WriteFile(filepath.Join(dir, "aven.json"), []byte(`{"deploy":{"numReplicas":1}}`), 0o644)
	if name, _, _ := Find(dir); name != "aven.json" {
		t.Errorf("want aven.json first, got %s", name)
	}
}

func TestInvalid(t *testing.T) {
	for _, src := range []string{
		`{"deploy":{"numReplicas":0}}`,
		`{"deploy":{"restartPolicyType":"SOMETIMES"}}`,
		`{"build":{"builder":"MAGIC"}}`,
		`{"deploy":{"preDeployCommand":42}}`,
	} {
		f, err := Parse("aven.json", []byte(src))
		if err != nil {
			continue
		}
		sp := store.Spec{}
		if _, err := f.Apply(&sp); err == nil {
			t.Errorf("%s: want error", src)
		}
	}
	if _, err := Parse("railway.json", []byte(`{bad`)); err == nil {
		t.Error("want parse error")
	}
}

func TestUnknownKeysAndSchemaIgnored(t *testing.T) {
	for name, src := range map[string]string{
		"railway.json": `{"$schema":"https://railway.com/railway.schema.json","build":{"nixpacksPlan":{"x":1},"builder":"RAILPACK"},"deploy":{"healthcheckType":"x","runtime":"V2"},"somethingNew":true}`,
		"railway.toml": "\"$schema\" = \"https://railway.com/railway.schema.json\"\nfoo = 1\n[build]\nbuilder = \"RAILPACK\"\nnixpacksConfigPath = \"x\"\n[deploy]\nruntime = \"V2\"\n",
	} {
		f, err := Parse(name, []byte(src))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		sp := store.Spec{}
		sp.Defaults()
		if _, err := f.Apply(&sp); err != nil || sp.Builder != "railpack" {
			t.Errorf("%s: %v %s", name, err, sp.Builder)
		}
	}
}

func TestNewKeys(t *testing.T) {
	f, err := Parse("railway.json", []byte(`{
		"build": {"buildCommand": "npm run build:prod", "railpackVersion": "0.9.0"},
		"deploy": {"sleepApplication": true, "overlapSeconds": 20, "drainingSeconds": 45, "requiredMountPath": "/data/",
			"region": "eu-west"}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	sp := store.Spec{}
	sp.Defaults()
	changed, err := f.Apply(&sp)
	if err != nil {
		t.Fatal(err)
	}
	if sp.BuildCommand == nil || *sp.BuildCommand != "npm run build:prod" || !sp.SleepWhenIdle || sp.OverlapSeconds != 20 ||
		sp.DrainingSeconds == nil || *sp.DrainingSeconds != 45 || sp.RequiredMountPath != "/data" {
		t.Errorf("unexpected spec: %+v", sp)
	}
	if sp.DrainGrace(10) != 45 {
		t.Errorf("drain grace %d", sp.DrainGrace(10))
	}
	if len(changed) != 5 {
		t.Errorf("changed = %v", changed)
	}
	if r, w := f.Region(); r != "eu-west" || w != "" {
		t.Errorf("region %q %q", r, w)
	}
	if ws := f.Warnings(); len(ws) != 1 {
		t.Errorf("warnings: %v", ws)
	}
	// 범위 밖
	for _, src := range []string{
		`{"deploy":{"overlapSeconds":-1}}`, `{"deploy":{"overlapSeconds":601}}`,
		`{"deploy":{"drainingSeconds":301}}`, `{"deploy":{"requiredMountPath":"data"}}`,
	} {
		f, _ := Parse("aven.json", []byte(src))
		if _, err := f.Apply(&store.Spec{}); err == nil {
			t.Errorf("%s: want error", src)
		}
	}
	// 빈 buildCommand는 지운다
	f, _ = Parse("aven.json", []byte(`{"build":{"buildCommand":""}}`))
	sp2 := store.Spec{BuildCommand: sp.BuildCommand}
	f.Apply(&sp2)
	if sp2.BuildCommand != nil {
		t.Error("empty buildCommand should clear")
	}
}

func TestMultiRegionConfig(t *testing.T) {
	f, _ := Parse("railway.json", []byte(`{"deploy":{"multiRegionConfig":{"us-west2":{"numReplicas":3}}}}`))
	sp := store.Spec{}
	sp.Defaults()
	if _, err := f.Apply(&sp); err != nil || sp.Replicas != 3 {
		t.Fatalf("replicas %d %v", sp.Replicas, err)
	}
	if r, _ := f.Region(); r != "us-west2" {
		t.Errorf("region %q", r)
	}
	f, _ = Parse("railway.json", []byte(`{"deploy":{"multiRegionConfig":{"a":{"numReplicas":1},"b":{"numReplicas":2}}}}`))
	if r, w := f.Region(); r != "" || w != "" {
		t.Errorf("multi: %q %q", r, w)
	}
	// region이 여러 개면 Regions가 이름순 placement 후보를 준다(노드 배치는 deploy가 한다).
	rs, err := f.Regions()
	if err != nil || len(rs) != 2 || rs[0] != (RegionReplicas{"a", 1}) || rs[1] != (RegionReplicas{"b", 2}) {
		t.Errorf("regions: %+v %v", rs, err)
	}
	if len(f.Warnings()) != 0 {
		t.Errorf("multi-region must not warn anymore: %v", f.Warnings())
	}
	bad, _ := Parse("railway.json", []byte(`{"deploy":{"multiRegionConfig":{"a":{"numReplicas":6},"b":{"numReplicas":5}}}}`))
	if _, err := bad.Regions(); err == nil {
		t.Error("total > 10 accepted")
	}
	one, _ := Parse("railway.json", []byte(`{"deploy":{"region":"x","multiRegionConfig":{"a":{},"b":{}}}}`))
	if rs, _ := one.Regions(); rs != nil {
		t.Errorf("deploy.region wins: %+v", rs)
	}
	if rs, _ := (&File{}).Regions(); rs != nil {
		t.Error("empty")
	}
	sp = store.Spec{}
	sp.Defaults()
	f.Apply(&sp)
	if sp.Replicas != 1 {
		t.Errorf("multi-region replicas must not apply: %d", sp.Replicas)
	}
}

func TestEnvironmentOverrides(t *testing.T) {
	for _, tc := range []struct{ name, src string }{
		{"railway.json", `{
			"build": {"builder": "DOCKERFILE", "watchPatterns": ["src/**"]},
			"deploy": {"startCommand": "node main.js", "numReplicas": 2, "healthcheckPath": "/health"},
			"environments": {
				"staging": {"deploy": {"numReplicas": 1, "startCommand": "node main.js --staging"}},
				"pr": {"build": {"builder": "RAILPACK"}, "deploy": {"numReplicas": 1, "sleepApplication": true}},
				"pr-7": {"deploy": {"startCommand": "exact"}}
			}
		}`},
		{"railway.toml", `
[build]
builder = "DOCKERFILE"
watchPatterns = ["src/**"]

[deploy]
startCommand = "node main.js"
numReplicas = 2
healthcheckPath = "/health"

[environments.staging.deploy]
numReplicas = 1
startCommand = "node main.js --staging"

[environments.pr.build]
builder = "RAILPACK"

[environments.pr.deploy]
numReplicas = 1
sleepApplication = true

[environments.pr-7.deploy]
startCommand = "exact"
`},
	} {
		f, err := Parse(tc.name, []byte(tc.src))
		if err != nil {
			t.Fatal(err)
		}
		apply := func(env string, preview bool) (store.Spec, string) {
			m, block := f.ForEnvironment(env, preview)
			sp := store.Spec{}
			sp.Defaults()
			if _, err := m.Apply(&sp); err != nil {
				t.Fatal(err)
			}
			return sp, block
		}
		// 블록 없음: 최상위 값
		sp, block := apply("production", false)
		if block != "" || sp.Replicas != 2 || *sp.StartCommand != "node main.js" || sp.Builder != "dockerfile" {
			t.Errorf("%s production: %q %+v", tc.name, block, sp)
		}
		// 정확히 같은 이름: 덮어쓴 키만 바뀌고 나머지는 최상위 값
		sp, block = apply("staging", false)
		if block != "staging" || sp.Replicas != 1 || *sp.StartCommand != "node main.js --staging" || sp.HealthcheckPath != "/health" || sp.Builder != "dockerfile" || sp.WatchPaths[0] != "src/**" {
			t.Errorf("%s staging: %q %+v", tc.name, block, sp)
		}
		// 미리보기 환경: pr 블록
		sp, block = apply("pr-12", true)
		if block != "pr" || sp.Builder != "railpack" || sp.Replicas != 1 || !sp.SleepWhenIdle || *sp.StartCommand != "node main.js" {
			t.Errorf("%s pr-12: %q %+v", tc.name, block, sp)
		}
		// 정확한 이름이 pr보다 우선
		sp, block = apply("pr-7", true)
		if block != "pr-7" || *sp.StartCommand != "exact" || sp.Builder != "dockerfile" {
			t.Errorf("%s pr-7: %q %+v", tc.name, block, sp)
		}
		// standard 환경은 pr 블록을 쓰지 않는다
		if _, block := f.ForEnvironment("pr-99", false); block != "" {
			t.Errorf("%s: standard env used block %q", tc.name, block)
		}
		// 원본은 바뀌지 않는다
		if *f.Deploy.StartCommand != "node main.js" || f.Environments == nil {
			t.Errorf("%s: ForEnvironment mutated the file", tc.name)
		}
	}
}

// TestFindRejectsSymlinkOutsideRepo: a repository must not make the build server read its own files
// (master.key, /etc/...) as config and echo parts of them in parse errors.
func TestFindRejectsSymlinkOutsideRepo(t *testing.T) {
	outside := filepath.Join(t.TempDir(), "secret.toml")
	os.WriteFile(outside, []byte("root:x:0:0"), 0o600)
	dir := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "aven.toml")); err != nil {
		t.Skip("symlinks not supported:", err)
	}
	_, f, err := Find(dir)
	if err == nil || f != nil || !strings.Contains(err.Error(), "outside the repository") || strings.Contains(err.Error(), "root:x") {
		t.Errorf("err %v", err)
	}

	// a link inside the repository is fine
	dir = t.TempDir()
	os.MkdirAll(filepath.Join(dir, "config"), 0o755)
	os.WriteFile(filepath.Join(dir, "config", "railway.json"), []byte(`{"deploy":{"numReplicas":2}}`), 0o644)
	os.Symlink(filepath.Join("config", "railway.json"), filepath.Join(dir, "railway.json"))
	if name, f, err := Find(dir); err != nil || name != "railway.json" || *f.Deploy.NumReplicas != 2 {
		t.Errorf("inside link: %s %v", name, err)
	}

	// a dangling link counts as missing
	dir = t.TempDir()
	os.Symlink(filepath.Join(dir, "nope.json"), filepath.Join(dir, "aven.json"))
	if name, f, err := Find(dir); err != nil || name != "" || f != nil {
		t.Errorf("dangling: %q %v", name, err)
	}
}

func TestFindLimitsAndShadowing(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "aven.json"), []byte(`{"build":{"builder":"`+strings.Repeat("x", MaxFileSize)+`"}}`), 0o644)
	if _, _, err := Find(dir); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("huge file: %v", err)
	}
	os.WriteFile(filepath.Join(dir, "aven.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(dir, "railway.toml"), []byte(``), 0o644)
	_, f, err := Find(dir)
	if err != nil {
		t.Fatal(err)
	}
	ws := strings.Join(f.Warnings(), "\n")
	if !strings.Contains(ws, "railway.toml") {
		t.Errorf("shadowed file not reported: %q", ws)
	}
	// the warning survives ForEnvironment (it is what the deploy logs print)
	if merged, _ := f.ForEnvironment("production", false); !strings.Contains(strings.Join(merged.Warnings(), ""), "railway.toml") {
		t.Error("lost after ForEnvironment")
	}
	os.Remove(filepath.Join(dir, "aven.json"))
	os.Mkdir(filepath.Join(dir, "aven.json"), 0o755)
	if _, _, err := Find(dir); err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Errorf("directory: %v", err)
	}
}

func TestApplyFixes(t *testing.T) {
	for _, tc := range []struct {
		src   string
		check func(sp store.Spec) bool
	}{
		// an empty preDeployCommand list clears the command instead of setting ""
		{`{"deploy":{"preDeployCommand":[]}}`, func(sp store.Spec) bool { return sp.PreDeployCommand == nil }},
		{`{"deploy":{"preDeployCommand":["", " npm run migrate ", ""]}}`, func(sp store.Spec) bool {
			return sp.PreDeployCommand != nil && *sp.PreDeployCommand == "npm run migrate"
		}},
		{`{"deploy":{"healthcheckPath":"health"}}`, func(sp store.Spec) bool { return sp.HealthcheckPath == "/health" }},
		{`{"deploy":{"healthcheckPath":""}}`, func(sp store.Spec) bool { return sp.HealthcheckPath == "/" }},
		{`{"deploy":{"restartPolicyMaxRetries":0}}`, func(sp store.Spec) bool { return sp.RestartMaxRetries == 0 }},
	} {
		f, err := Parse("aven.json", []byte(tc.src))
		if err != nil {
			t.Fatal(err)
		}
		sp := store.Spec{}
		pre := "old"
		sp.PreDeployCommand = &pre
		if _, err := f.Apply(&sp); err != nil || !tc.check(sp) {
			t.Errorf("%s: %v %+v", tc.src, err, sp)
		}
	}
	for _, src := range []string{
		`{"deploy":{"restartPolicyMaxRetries":-1}}`,
		`{"deploy":{"healthcheckPath":"/a b"}}`,
		`{"deploy":{"healthcheckPath":"/a\nb"}}`,
	} {
		f, err := Parse("aven.json", []byte(src))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Apply(&store.Spec{}); err == nil {
			t.Errorf("%s: want error", src)
		}
	}
}
