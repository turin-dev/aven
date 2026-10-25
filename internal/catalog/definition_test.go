package catalog

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// 내장 템플릿을 JSON 엔진으로 옮긴 뒤에도 서비스/변수/설정 모양이 예전 Go 정의와 같아야 한다.
func TestBuiltinTemplatesGolden(t *testing.T) {
	b, err := os.ReadFile("testdata/builtin_golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var want map[string]goldenTemplate
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	got := goldenShapes(func(key string) []ServiceDef { return Templates[key].Services() })
	if len(got) != len(want) {
		t.Fatalf("templates: got %d, want %d", len(got), len(want))
	}
	for k, w := range want {
		g, ok := got[k]
		if !ok {
			t.Errorf("missing template %s", k)
			continue
		}
		// spec은 JSON 값으로 비교한다(키 순서 무관).
		norm := func(gt goldenTemplate) goldenTemplate {
			for i := range gt.Services {
				var v any
				json.Unmarshal(gt.Services[i].Spec, &v)
				gt.Services[i].Spec, _ = json.Marshal(v)
			}
			return gt
		}
		g, w = norm(g), norm(w)
		if !reflect.DeepEqual(g, w) {
			gb, _ := json.MarshalIndent(g, "", " ")
			wb, _ := json.MarshalIndent(w, "", " ")
			t.Errorf("template %s changed:\ngot  %s\nwant %s", k, gb, wb)
		}
		if p := Templates[k].Definition.PrimaryService(); p != w.Primary {
			t.Errorf("template %s primary %s, want %s", k, p, w.Primary)
		}
	}
}

func TestBuiltinDBPasswordsDifferPerEnvironment(t *testing.T) {
	defs := Templates["umami"].Services()
	pg := defs[0]
	other := pg.ForAnotherEnvironment()
	if pg.Variables["POSTGRES_PASSWORD"] == other.Variables["POSTGRES_PASSWORD"] || other.Variables["POSTGRES_PASSWORD"] == "" {
		t.Error("password not regenerated")
	}
	if other.Variables["POSTGRES_USER"] != "postgres" || other.Variables["PGPASSWORD"] != "${{POSTGRES_PASSWORD}}" {
		t.Errorf("other vars changed: %v", other.Variables)
	}
	// 두 번 배포하면 다른 비밀이 나온다.
	if Templates["umami"].Services()[1].Variables["APP_SECRET"] == defs[1].Variables["APP_SECRET"] {
		t.Error("APP_SECRET reused across deploys")
	}
}

const goodDef = `{
  "version": 1,
  "key": "app-stack",
  "name": "App stack",
  "primary": "web",
  "inputs": [
    {"key": "ADMIN_EMAIL", "label": "Admin email", "required": true, "pattern": "[^@\\s]+@[^@\\s]+"},
    {"key": "REGION", "default": "eu"},
    {"key": "TOKEN", "secret": true, "default": "${{ secret(12) }}"}
  ],
  "services": [
    {"name": "db", "source": {"database": "postgres"}, "volumes": [{"mount_path": "/var/lib/postgresql/data", "size_limit_mb": 2048}]},
    {"name": "web", "icon": "node", "source": {"repo": {"url": "https://github.com/acme/web", "branch": "prod", "root_dir": "app"}},
     "port": 3000, "replicas": 2, "healthcheck_path": "/health", "cron_schedule": "", "start_command": "node server.js --region ${{ inputs.REGION }}",
     "variables": {
       "DATABASE_URL": "${{db.DATABASE_URL}}",
       "ADMIN_EMAIL": "${{ inputs.ADMIN_EMAIL }}",
       "SESSION_SECRET": "${{ secret(40) }}",
       "PIN": "${{ randomInt(1000, 9999) }}",
       "INSTANCE_ID": "${{ uuid() }}",
       "HEX": "${{ secret(16, \"abcdef0123456789\") }}",
       "SELF": "${{KEEL_PUBLIC_DOMAIN}}",
       "TOKEN": "${{ inputs.TOKEN }}",
       "SHARED": "${{ shared.SENTRY_DSN }}",
       "URL": "https://${{SELF}}/"
     },
     "domains": [{"generate": true, "target_port": 3000}],
     "tcp_proxy": {"container_port": 9229},
     "canvas": {"x": 100, "y": -40}
    },
    {"name": "worker", "source": {"image": "ghcr.io/acme/worker:1"}, "cron_schedule": "*/5 * * * *", "restart_policy": "never",
     "variables": {"DB": "${{db.PGHOST}}", "WEB": "${{web.SESSION_SECRET}}"}}
  ]
}`

func TestParseDefinitionGood(t *testing.T) {
	d, err := ParseDefinition([]byte(goodDef))
	if err != nil {
		t.Fatal(err)
	}
	defs, primary, err := d.Instantiate(map[string]string{"ADMIN_EMAIL": "a@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if primary != 1 || defs[1].Name != "web" {
		t.Errorf("primary %d", primary)
	}
	web := defs[1]
	v := web.Variables
	if v["DATABASE_URL"] != "${{db.DATABASE_URL}}" || v["ADMIN_EMAIL"] != "a@example.com" || v["SHARED"] != "${{ shared.SENTRY_DSN }}" {
		t.Errorf("refs/inputs: %v", v)
	}
	if len(v["SESSION_SECRET"]) != 40 || len(v["TOKEN"]) != 12 || strings.Contains(v["TOKEN"], "${{") {
		t.Errorf("secrets: %q %q", v["SESSION_SECRET"], v["TOKEN"])
	}
	if !regexp.MustCompile(`^[0-9a-f]{16}$`).MatchString(v["HEX"]) {
		t.Errorf("hex secret: %q", v["HEX"])
	}
	if *web.Settings.StartCommand != "node server.js --region eu" || *web.Settings.Replicas != 2 || web.RepoURL != "https://github.com/acme/web" || web.Branch != "prod" {
		t.Errorf("settings: %+v", web)
	}
	if web.TCPProxyPort != 9229 || len(web.Domains) != 1 || *web.Domains[0].TargetPort != 3000 || web.Canvas.X != 100 {
		t.Errorf("extras: %+v", web)
	}
	if defs[0].VolumeLimits["/var/lib/postgresql/data"] != 2048 || len(defs[0].Volumes) != 1 || defs[0].Variables["POSTGRES_PASSWORD"] == "" {
		t.Errorf("db: %+v", defs[0])
	}
	if strings.Contains(string(mustJSON(d)), "a@example.com") {
		t.Error("input value leaked into the definition")
	}
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func TestParseDefinitionBad(t *testing.T) {
	base := func(services string) string {
		return `{"version":1,"inputs":[{"key":"X"}],"services":[` + services + `]}`
	}
	cases := map[string]string{
		"unknown top field":  `{"version":1,"servics":[],"services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"unknown svc field":  base(`{"name":"a","source":{"image":"nginx"},"replica":2}`),
		"wrong version":      `{"version":2,"services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"no services":        `{"version":1,"services":[]}`,
		"bad name":           base(`{"name":"A_b","source":{"image":"nginx"}}`),
		"reserved name":      base(`{"name":"shared","source":{"image":"nginx"}}`),
		"dup name":           base(`{"name":"a","source":{"image":"nginx"}},{"name":"a","source":{"image":"nginx"}}`),
		"two sources":        base(`{"name":"a","source":{"image":"nginx","database":"postgres"}}`),
		"no source":          base(`{"name":"a","source":{}}`),
		"bad db":             base(`{"name":"a","source":{"database":"oracle"}}`),
		"file repo":          base(`{"name":"a","source":{"repo":{"url":"file:///etc"}}}`),
		"unknown svc ref":    base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{b.X}}"}}`),
		"unknown var ref":    base(`{"name":"a","source":{"image":"nginx"}},{"name":"b","source":{"image":"nginx"},"variables":{"A":"${{a.NOPE}}"}}`),
		"unknown local ref":  base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{NOPE}}"}}`),
		"cycle":              base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{b.B}}"}},{"name":"b","source":{"image":"nginx"},"variables":{"B":"${{a.A}}"}}`),
		"self cycle":         base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{A}}"}}`),
		"unknown input":      base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ inputs.Y }}"}}`),
		"unknown function":   base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ random(3) }}"}}`),
		"secret too long":    base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ secret(9999) }}"}}`),
		"secret dup charset": base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ secret(8, \"aa\") }}"}}`),
		"randomInt reversed": base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ randomInt(9, 1) }}"}}`),
		"uuid args":          base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ uuid(1) }}"}}`),
		"garbage expression": base(`{"name":"a","source":{"image":"nginx"},"variables":{"A":"${{ a b }}"}}`),
		"ref in command":     base(`{"name":"a","source":{"image":"nginx"},"start_command":"run ${{KEEL_PORT}}"}`),
		"custom domain":      base(`{"name":"a","source":{"image":"nginx"},"domains":[{"generate":false}]}`),
		"bad port":           base(`{"name":"a","source":{"image":"nginx"},"port":70000}`),
		"bad replicas":       base(`{"name":"a","source":{"image":"nginx"},"replicas":0}`),
		"bad cron":           base(`{"name":"a","source":{"image":"nginx"},"cron_schedule":"every day"}`),
		"bad restart":        base(`{"name":"a","source":{"image":"nginx"},"restart_policy":"sometimes"}`),
		"relative volume":    base(`{"name":"a","source":{"image":"nginx"},"volumes":[{"mount_path":"data"}]}`),
		"volume limit":       base(`{"name":"a","source":{"image":"nginx"},"volumes":[{"mount_path":"/data","size_limit_mb":0}]}`),
		"bad primary":        `{"version":1,"primary":"zzz","services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"bad input pattern":  `{"version":1,"inputs":[{"key":"X","pattern":"("}],"services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"default vs pattern": `{"version":1,"inputs":[{"key":"X","pattern":"\\d+","default":"abc"}],"services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"dup input":          `{"version":1,"inputs":[{"key":"X"},{"key":"X"}],"services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"trailing data":      `{"version":1,"services":[{"name":"a","source":{"image":"nginx"}}]} {}`,
		"bad key":            `{"version":1,"key":"Bad Key","services":[{"name":"a","source":{"image":"nginx"}}]}`,
		"string for int":     base(`{"name":"a","source":{"image":"nginx"},"port":"80"}`),
		"bad image":          base(`{"name":"a","source":{"image":"-rm -rf"}}`),
	}
	for name, src := range cases {
		_, err := ParseDefinition([]byte(src))
		var ve *ValidationError
		if err == nil || !errors.As(err, &ve) {
			t.Errorf("%s: accepted (%v)", name, err)
		}
	}
	big := `{"version":1,"readme":"` + strings.Repeat("x", MaxDefinitionBytes) + `","services":[{"name":"a","source":{"image":"nginx"}}]}`
	if _, err := ParseDefinition([]byte(big)); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("size cap: %v", err)
	}
}

func TestFunctions(t *testing.T) {
	for i := 0; i < 50; i++ {
		s, err := evalFunction(`secret(32)`, false)
		if err != nil || len(s) != 32 || strings.Trim(s, DefaultSecretCharset) != "" {
			t.Fatalf("secret: %q %v", s, err)
		}
		s, _ = evalFunction(`secret(64, "ab")`, false)
		if len(s) != 64 || strings.Trim(s, "ab") != "" {
			t.Fatalf("charset: %q", s)
		}
		n, _ := evalFunction(` randomInt( 1000 , 1002 ) `, false)
		v, err := strconv.Atoi(n)
		if err != nil || v < 1000 || v > 1002 {
			t.Fatalf("randomInt: %q", n)
		}
		neg, _ := evalFunction(`randomInt(-5,-5)`, false)
		if neg != "-5" {
			t.Fatalf("randomInt equal bounds: %q", neg)
		}
		u, _ := evalFunction(`uuid()`, false)
		if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) {
			t.Fatalf("uuid: %q", u)
		}
	}
	// 모든 글자가 고르게 나온다(2글자 집합에서 한쪽만 나오지 않는다).
	s, _ := evalFunction(`secret(512, "xy")`, false)
	if c := strings.Count(s, "x"); c < 150 || c > 362 {
		t.Errorf("biased secret: %d x of 512", c)
	}
	out, err := expand(`a=${{ secret(4) }} b=${{ ref.X }} c=${{ inputs.K }}`, map[string]string{"K": "${{ secret(3) }}"})
	if err != nil || !strings.Contains(out, "b=${{ ref.X }}") || !strings.HasSuffix(out, "c=${{ secret(3) }}") || strings.Contains(out, "secret(4)") {
		t.Errorf("expand: %q %v", out, err)
	}
}

func TestResolveInputs(t *testing.T) {
	d, err := ParseDefinition([]byte(goodDef))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.Instantiate(map[string]string{"ADMIN_EMAIL": "not-an-email", "BOGUS": "1"})
	var ie *InputError
	if !errors.As(err, &ie) || len(ie.Invalid) != 1 || ie.Invalid[0] != "ADMIN_EMAIL" || len(ie.Unknown) != 1 {
		t.Errorf("invalid: %+v", err)
	}
	_, _, err = d.Instantiate(nil)
	if !errors.As(err, &ie) || len(ie.Missing) != 1 || ie.Missing[0] != "ADMIN_EMAIL" {
		t.Errorf("missing: %+v", err)
	}
	in, err := d.ResolveInputs(map[string]string{"ADMIN_EMAIL": "a@b.c", "REGION": "us"})
	if err != nil || in["REGION"] != "us" || len(in["TOKEN"]) != 12 {
		t.Errorf("resolved: %v %v", in, err)
	}
}
