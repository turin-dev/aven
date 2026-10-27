package main

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/turin-dev/aven/internal/compat"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestParseVarsArgs(t *testing.T) {
	o, err := parseVarsArgs([]string{"--set", "A=1", "--set=B=x=y", "--unset", "C", "--kv", "--deploy", "-s", "web", "--environment=staging"})
	if err != nil {
		t.Fatal(err)
	}
	if *o.patch["A"] != "1" || *o.patch["B"] != "x=y" || o.patch["C"] != nil || !o.kv || !o.deploy || o.service != "web" || o.env != "staging" {
		t.Errorf("parsed: %+v", o)
	}
	if _, ok := o.patch["C"]; !ok {
		t.Error("unset missing")
	}
	// 옛 형식
	o, err = parseVarsArgs([]string{"set", "K=V", "L=W", "unset", "M", "--deploy"})
	if err != nil || *o.patch["K"] != "V" || *o.patch["L"] != "W" || o.patch["M"] != nil || !o.deploy {
		t.Errorf("legacy: %v %+v", err, o)
	}
	for _, bad := range [][]string{{"--set", "NOEQ"}, {"--set"}, {"--bogus"}, {"stray"}, {"--set", "=v"}} {
		if _, err := parseVarsArgs(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
	if o, err := parseVarsArgs(nil); err != nil || len(o.patch) != 0 || o.deploy {
		t.Errorf("empty: %v %+v", err, o)
	}
}

func TestParseAddArgs(t *testing.T) {
	cases := map[string]map[string]any{
		"--database pg":                              {"source_type": "database", "database_kind": "postgres"},
		"--database mongodb --name db":               {"source_type": "database", "database_kind": "mongo", "name": "db"},
		"--database s3 --name files":                 {"source_type": "database", "database_kind": "bucket", "name": "files"},
		"--image nginx:1.27":                         {"source_type": "image", "image": "nginx:1.27"},
		"--repo https://github.com/a/b --branch dev": {"source_type": "repo", "repo_url": "https://github.com/a/b", "branch": "dev"},
	}
	for in, want := range cases {
		got, err := parseAddArgs(strings.Fields(in))
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v %v", in, err, got)
		}
	}
	for _, bad := range []string{"", "--database oracle", "--image a --repo b", "--image a --branch dev", "--image a extra"} {
		if _, err := parseAddArgs(strings.Fields(bad)); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestReorderFlags(t *testing.T) {
	got := reorderFlags([]string{"staging", "--copy-from", "production", "-y"})
	if strings.Join(got, " ") != "--copy-from production -y staging" {
		t.Errorf("reorder: %v", got)
	}
	got = reorderFlags([]string{"a.example.com", "--port=8080", "--", "-x"})
	if strings.Join(got, " ") != "--port=8080 a.example.com -x" {
		t.Errorf("reorder with --: %v", got)
	}
}

func TestRunEnv(t *testing.T) {
	l := &link{ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1", Service: "web", Environment: "production", Project: "acme"}
	base := []string{"PATH=/bin", "DATABASE_URL=local", "HOME=/home/u", "Port=1"}
	vars := map[string]string{"DATABASE_URL": "postgres://remote", "PORT": "8080", "KEEL_SERVICE_NAME": "web-server"}
	env := runEnv(base, vars, l, false)
	m := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := m[k]; dup {
			t.Errorf("duplicate %s", k)
		}
		m[k] = v
	}
	if m["DATABASE_URL"] != "postgres://remote" || m["PATH"] != "/bin" || m["PORT"] != "8080" || m["Port"] != "1" {
		t.Errorf("env: %v", m)
	}
	// 서버 값이 연결 정보보다 우선, 없는 KEEL_*는 연결 정보로 채운다
	if m["KEEL_SERVICE_NAME"] != "web-server" || m["KEEL_PROJECT_ID"] != "prj_1" || m["KEEL_ENVIRONMENT_NAME"] != "production" {
		t.Errorf("aven vars: %v", m)
	}
	// Windows: 대소문자 무시로 덮는다
	env = runEnv(base, vars, l, true)
	for _, kv := range env {
		if kv == "Port=1" {
			t.Error("case-insensitive override failed")
		}
	}
}

// TestHelperProcess는 runLocal이 실행하는 자식이다(KEEL_HELPER=1일 때만).
func TestHelperProcess(t *testing.T) {
	if compat.Env("HELPER") != "1" {
		return
	}
	if os.Getenv("DATABASE_URL") != "postgres://remote" || compat.Env("SERVICE_ID") != "svc_1" {
		os.Exit(9)
	}
	os.Exit(42)
}

func TestRunUsesResolvedVarsAndExitCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/services/svc_1/instances/env_1/variables/resolved" || r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(404)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{
			"DATABASE_URL":    map[string]string{"value": "postgres://remote", "source": "reference"},
			"KEEL_SERVICE_ID": map[string]string{"value": "svc_1", "source": "injected"},
		})
	}))
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "tok")
	c := &config{URL: srv.URL, Links: map[string]*link{}}
	l := &link{ProjectID: "prj_1", EnvironmentID: "env_1", ServiceID: "svc_1"}
	vars := c.resolvedVars(l)
	env := append(runEnv(os.Environ(), vars, l, false), "KEEL_HELPER=1")
	exe, _ := os.Executable()
	if code := runLocal(env, exe, "-test.run=TestHelperProcess"); code != 42 {
		t.Errorf("exit code %d, want 42 (9 means variables were not injected)", code)
	}
	if code := runLocal(env, "keel-definitely-missing-binary"); code != 127 {
		t.Errorf("missing binary: %d", code)
	}
}

func TestShellJoinAndExecURL(t *testing.T) {
	if got := shellJoin([]string{"ls", "-la", "/app dir", "it's"}); got != `ls -la '/app dir' 'it'\''s'` {
		t.Errorf("shellJoin: %s", got)
	}
	l := &link{ServiceID: "svc_1", EnvironmentID: "env_1"}
	u, err := execURL("https://aven.example.com/", l, "keel-web-1", []string{"echo", "a b"}, 120, 40)
	if err != nil || u != "wss://aven.example.com/api/services/svc_1/instances/env_1/exec?cmd=echo+%27a+b%27&cols=120&replica=keel-web-1&rows=40" {
		t.Errorf("execURL: %s %v", u, err)
	}
	if u, _ := execURL("http://localhost:8080", l, "", nil, 0, 0); u != "ws://localhost:8080/api/services/svc_1/instances/env_1/exec" {
		t.Errorf("execURL plain: %s", u)
	}
	if _, err := execURL("ftp://x", l, "", nil, 0, 0); err == nil {
		t.Error("ftp accepted")
	}
}

func TestDBClientKeepsPasswordOutOfArgv(t *testing.T) {
	cases := map[string]string{
		"postgres": "postgresql://user:s3cret@proxy.example.com:20001/aven",
		"mysql":    "mysql://root:s3cret@proxy.example.com:20002/app",
		"redis":    "redis://default:s3cret@proxy.example.com:20003",
		"mongo":    "mongodb://mongo:s3cret@proxy.example.com:20004",
	}
	for kind, u := range cases {
		bin, args, env, err := dbClient(kind, u)
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if bin != dbDefaults[kind].bin {
			t.Errorf("%s: bin %s", kind, bin)
		}
		if strings.Contains(strings.Join(args, " "), "s3cret") {
			t.Errorf("%s: password in argv: %v", kind, args)
		}
		if !strings.Contains(strings.Join(env, " "), "s3cret") {
			t.Errorf("%s: password not passed via env", kind)
		}
	}
	if _, args, _, _ := dbClient("mysql", cases["mysql"]); strings.Join(args, " ") != "--protocol=TCP -h proxy.example.com -P 20002 -u root app" {
		t.Errorf("mysql args: %v", args)
	}
	if _, _, _, err := dbClient("postgres", "postgresql://u:p@:/db"); err == nil {
		t.Error("URL without proxy host accepted")
	}
	if _, _, _, err := dbClient("oracle", cases["mysql"]); err == nil {
		t.Error("unknown kind accepted")
	}
}

func TestBuildFilter(t *testing.T) {
	f := buildFilter(`error -"npm warn" Step`)
	for line, want := range map[string]bool{"Step 3: ERROR x": true, "step 1 error npm warn": false, "step ok": false} {
		if f(line) != want {
			t.Errorf("%q: %v", line, !want)
		}
	}
	if !buildFilter("")("anything") {
		t.Error("empty filter")
	}
}

func TestDashboardURL(t *testing.T) {
	got := dashboardURL("https://k.example/", &link{ProjectID: "prj_1", Environment: "production", ServiceID: "svc_1"})
	if got != "https://k.example/?env=production&project=prj_1&svc=svc_1" {
		t.Error(got)
	}
}

// TestSSHPumpFraming은 CLI 쪽 exec 프로토콜(binary=바이트, text=resize/exit/error)을 가짜 서버로 확인한다.
func TestSSHPumpFraming(t *testing.T) {
	got := make(chan string, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		ctx := r.Context()
		conn.Write(ctx, websocket.MessageBinary, []byte("\x1b[32mhello\x1b[0m\r\n"))
		for i := 0; i < 2; i++ {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			got <- map[websocket.MessageType]string{websocket.MessageBinary: "bin:", websocket.MessageText: "text:"}[typ] + string(data)
		}
		conn.Write(ctx, websocket.MessageText, []byte(`{"type":"error","message":"warn"}`))
		conn.Write(ctx, websocket.MessageText, []byte(`{"type":"exit","code":5}`))
		conn.Close(websocket.StatusNormalClosure, "exited")
	}))
	defer srv.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	resize := make(chan [2]int, 1)
	resize <- [2]int{100, 30}
	in := strings.NewReader("ls\r")
	var out, errOut bytes.Buffer
	// resize와 입력의 순서는 정해지지 않는다
	code := sshPump(ctx, conn, in, &out, &errOut, resize)
	if code != 5 {
		t.Errorf("code %d", code)
	}
	if !strings.Contains(out.String(), "hello") {
		t.Errorf("out: %q", out.String())
	}
	if !strings.Contains(errOut.String(), "warn") {
		t.Errorf("errOut: %q", errOut.String())
	}
	a, b := <-got, <-got
	all := a + "|" + b
	if !strings.Contains(all, "bin:ls\r") || !strings.Contains(all, `text:{"cols":100,"rows":30,"type":"resize"}`) {
		t.Errorf("frames: %s", all)
	}
}

func TestFormatSyncPlan(t *testing.T) {
	var p syncPlan
	raw := `{"from":{"id":"env_a","name":"production"},"to":{"id":"env_b","name":"staging"},
	"services":[{"service_id":"svc_1","service_name":"web","action":"update",
	  "settings":[{"field":"replicas","from":1,"to":2},{"field":"start_command","from":null,"to":"npm start"}],
	  "variables":[{"key":"API_KEY","action":"added","from":null,"to":"•••"},{"key":"OLD","action":"removed","from":"•••","to":null},
	    {"key":"DB","action":"changed","from":"•••","to":"${{db.DATABASE_URL}}"}],
	  "volumes":[{"mount_path":"/data","size_limit_mb":null}],"protected_variables":[]},
	 {"service_id":"svc_2","service_name":"db","action":"create","settings":[],"variables":[],"volumes":[],"protected_variables":["PGPASSWORD"]}],
	"shared_variables":[{"key":"REGION","action":"added","from":null,"to":"•••"}]}`
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	want := "web\n" +
		"  ~ replicas: 1 → 2\n" +
		"  ~ start_command: null → \"npm start\"\n" +
		"  + API_KEY = •••\n" +
		"  - OLD\n" +
		"  ~ DB: ••• → ${{db.DATABASE_URL}}\n" +
		"  + volume /data (empty)\n" +
		"db (new)\n" +
		"  (not synced: database credentials PGPASSWORD)\n" +
		"shared variables\n" +
		"  + REGION = •••\n"
	if got := formatSyncPlan(&p); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if formatSyncPlan(&syncPlan{}) != "" {
		t.Error("empty plan should format to nothing")
	}
	if got := reorderFlags([]string{"staging", "--from", "production", "--deploy"}); strings.Join(got, " ") != "--from production --deploy staging" {
		t.Errorf("reorder: %v", got)
	}
}
