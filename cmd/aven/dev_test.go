package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseDevArgs(t *testing.T) {
	o, err := parseDevArgs([]string{"--environment", "staging", "--dry-run", "-v", "--no-tui"})
	if err != nil || o.action != "up" || o.environment != "staging" || !o.dryRun || !o.verbose {
		t.Fatalf("options=%+v err=%v", o, err)
	}
	o, err = parseDevArgs([]string{"start", "-e=env_1"})
	if err != nil || o.action != "up" || o.environment != "env_1" {
		t.Fatalf("start alias: options=%+v err=%v", o, err)
	}
	for in, want := range map[string]string{"down": "down", "stop": "down", "clean": "clean", "reset": "clean", "config": "configure"} {
		o, err := parseDevArgs([]string{in})
		if err != nil || o.action != want {
			t.Errorf("%s: options=%+v err=%v", in, o, err)
		}
	}
	o, err = parseDevArgs([]string{"configure", "--service", "api", "--command", "npm run dev", "--port", "3000"})
	if err != nil || o.service != "api" || o.command == nil || *o.command != "npm run dev" || o.port == nil || *o.port != 3000 || o.dir != nil {
		t.Fatalf("configure: options=%+v err=%v", o, err)
	}
	o, err = parseDevArgs([]string{"configure", "--remove", "api"})
	if err != nil || !o.remove || o.service != "api" {
		t.Fatalf("configure --remove: options=%+v err=%v", o, err)
	}
	for _, args := range [][]string{{"unexpected"}, {"--bad"}, {"up", "extra"}, {"down", "--dry-run"}, {"configure", "--port", "70000"}, {"configure", "--remove", "--command", "x"}} {
		if _, err := parseDevArgs(args); err == nil {
			t.Errorf("accepted %v", args)
		}
	}
}

func testTargets() map[string]devHostTarget {
	return map[string]devHostTarget{
		"postgres.development.keel.internal": {host: "localhost", fromPort: 5432, toPort: 55432},
		"api.development.keel.internal":      {host: "localhost", fromPort: 8080, toPort: 18080},
	}
}

func TestRewriteDevHosts(t *testing.T) {
	targets := testTargets()
	cases := map[string]string{
		"postgresql://u:p@postgres.development.keel.internal:5432/app?host=postgres.development.keel.internal": "postgresql://u:p@localhost:55432/app?host=localhost",
		"api.development.keel.internal":                                    "localhost",
		"API.Development.keel.internal:8080":                               "localhost:18080",
		"http://api.development.keel.internal:9090/metrics":                "http://localhost:9090/metrics",
		"api.development.keel.internal,api.development.keel.internal:8080": "localhost,localhost:18080",
		// 다른 호스트의 일부는 바꾸지 않는다.
		"https://remote.example.com/postgres.development.keel.internal.example": "https://remote.example.com/postgres.development.keel.internal.example",
		"xapi.development.keel.internal":                                        "xapi.development.keel.internal",
	}
	for in, want := range cases {
		if got := rewriteDevHosts(in, targets); got != want {
			t.Errorf("rewrite %q:\n got %q\nwant %q", in, got, want)
		}
	}
}

func TestRewriteDevHostsKeepsURLStructure(t *testing.T) {
	input := "redis://default:p%40ss@redis.production.keel.internal:6379/0"
	got := rewriteDevHosts(input, map[string]devHostTarget{"redis.production.keel.internal": {host: "localhost", fromPort: 6379, toPort: 16379}})
	parsed, err := url.Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if got != "redis://default:p%40ss@localhost:16379/0" || parsed.Hostname() != "localhost" || parsed.Port() != "16379" || parsed.User.Username() != "default" {
		t.Fatalf("rewritten URL %q", got)
	}
}

func TestDevServicePort(t *testing.T) {
	port := 4321
	db := "postgres"
	cases := []struct {
		name string
		item devService
		want int
	}{
		{"configured port", devService{instance: devInstance{Port: &port}, vars: map[string]string{"PORT": "1234"}}, 4321},
		{"variable port", devService{vars: map[string]string{"PORT": "1234"}}, 1234},
		{"database default", devService{service: svcRef{DatabaseKind: &db}}, 5432},
		{"generic default", devService{}, 8080},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := devServicePort(&tc.item); got != tc.want {
				t.Fatalf("port=%d, want %d", got, tc.want)
			}
		})
	}
}

func TestReserveDevPortsFallsBackWhenBusy(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	busyPort := busy.Addr().(*net.TCPAddr).Port
	svc := &devService{service: svcRef{Name: "api"}, containerPort: busyPort}
	ls, err := reserveDevPorts([]*devService{svc})
	if err != nil {
		t.Fatal(err)
	}
	closeDevPorts(ls)
	if svc.localPort == 0 || svc.localPort == busyPort {
		t.Fatalf("localPort=%d busy=%d", svc.localPort, busyPort)
	}
}

func TestDevProcessPlanKeepsSecretsOutOfDockerArgs(t *testing.T) {
	image := "postgres:17-alpine"
	start := "postgres -c log_statement=all"
	item := &devService{
		service:       svcRef{ID: "svc_db", Name: "postgres", SourceType: "database"},
		instance:      devInstance{Image: &image, StartCommand: &start},
		vars:          map[string]string{"POSTGRES_PASSWORD": "local-secret", "PORT": "5432"},
		containerVars: map[string]string{"POSTGRES_PASSWORD": "local-secret", "PORT": "5432", "DOCKER_HOST_HINT": "x"},
		containerPort: 5432,
		localPort:     15432,
		volumes:       []devVolume{{ID: "vol_data", MountPath: "/var/lib/postgresql/data"}},
	}
	l := &link{ProjectID: "prj_1", Project: "sample", EnvironmentID: "env_1", Environment: "development"}
	plan, err := devProcessPlan(t.TempDir(), l, []*devService{item}, "/usr/bin/docker", "keel-dev-development-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 {
		t.Fatalf("plan length %d", len(plan))
	}
	args := strings.Join(plan[0].args, " ")
	for _, want := range []string{
		"-e POSTGRES_PASSWORD ", "127.0.0.1:15432:5432/tcp", "source=keel-dev-vol-data,target=/var/lib/postgresql/data",
		"--network keel-dev-development-1", "--network-alias postgres.development.keel.internal", "--network-alias postgres ",
		"--label keel.dev.project=prj_1", "--label keel.dev.environment=env_1", "--label keel.dev.service=svc_db",
		"--entrypoint /bin/sh", "postgres:17-alpine -c postgres -c log_statement=all", "-e DOCKER_HOST_HINT=x",
	} {
		if !strings.Contains(args, want) {
			t.Errorf("docker args missing %q: %s", want, args)
		}
	}
	if strings.Contains(args, "local-secret") {
		t.Fatalf("secret in args: %s", args)
	}
	env := map[string]string{}
	for _, entry := range plan[0].env {
		if key, value, ok := strings.Cut(entry, "="); ok {
			env[key] = value
		}
	}
	if env["POSTGRES_PASSWORD"] != "local-secret" {
		t.Fatalf("docker environment is missing the variable")
	}
	if _, ok := env["DOCKER_HOST_HINT"]; ok && os.Getenv("DOCKER_HOST_HINT") == "" {
		t.Fatalf("DOCKER_* variable leaked into the docker CLI environment")
	}
	if got := plan[0].volumes; len(got) != 1 || got[0] != "keel-dev-vol-data" {
		t.Fatalf("volumes=%v", got)
	}
	var out strings.Builder
	printDevPlan(&out, plan)
	if strings.Contains(out.String(), "local-secret") || !strings.Contains(out.String(), "DOCKER_HOST_HINT=***") {
		t.Fatalf("dry-run output: %s", out.String())
	}
}

func TestDevProcessPlanRepoService(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	item := &devService{
		service:  svcRef{ID: "svc_api", Name: "api", SourceType: "repo"},
		instance: devInstance{RootDir: "/api"},
		command:  "npm run dev", dir: "/api",
		vars:      map[string]string{"PORT": "18080"},
		localPort: 18080, containerPort: 8080,
	}
	l := &link{ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "development"}
	plan, err := devProcessPlan(base, l, []*devService{item}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if plan[0].container || plan[0].dir != filepath.Join(base, "api") || plan[0].args[len(plan[0].args)-1] != "npm run dev" {
		t.Fatalf("plan=%+v", plan[0])
	}
	if !strings.Contains(strings.Join(plan[0].env, "\n"), "KEEL_SERVICE_NAME=api") {
		t.Fatal("KEEL_* link variables missing")
	}
}

func TestRewriteDevVariablesForLocalAndDockerServices(t *testing.T) {
	dbURL := "postgresql://postgres:secret@postgres.development.keel.internal:5432/app"
	apiURL := "http://api.development.keel.internal:8080"
	services := []*devService{
		{
			service:   svcRef{ID: "svc_api", Name: "api", SourceType: "repo"},
			instance:  devInstance{PrivateDomain: "api.development.keel.internal"},
			vars:      map[string]string{"DATABASE_URL": dbURL, "PGHOST": "postgres.development.keel.internal"},
			localPort: 18080, containerPort: 8080,
		},
		{
			service:       svcRef{ID: "svc_pg", Name: "postgres", SourceType: "database"},
			instance:      devInstance{PrivateDomain: "postgres.development.keel.internal"},
			vars:          map[string]string{"DATABASE_URL": dbURL, "API_URL": apiURL},
			containerVars: map[string]string{"DATABASE_URL": dbURL, "API_URL": apiURL},
			localPort:     15432, containerPort: 5432,
		},
	}
	services[0].containerVars = cloneStringMap(services[0].vars)
	before := cloneStringMap(services[0].vars)
	rewriteDevVariables(services, "development")
	if got, want := services[0].vars["DATABASE_URL"], "postgresql://postgres:secret@localhost:15432/app"; got != want {
		t.Errorf("local code URL=%q want %q", got, want)
	}
	if got := services[0].vars["PGHOST"]; got != "localhost" {
		t.Errorf("PGHOST=%q", got)
	}
	if got := services[0].vars["PORT"]; got != "18080" {
		t.Errorf("repo PORT=%q", got)
	}
	if got, want := services[1].containerVars["API_URL"], "http://host.docker.internal:18080"; got != want {
		t.Errorf("Docker to local code URL=%q want %q", got, want)
	}
	if got := services[1].containerVars["DATABASE_URL"]; got != dbURL {
		t.Errorf("Docker network URL=%q want %q", got, dbURL)
	}
	if got := services[1].containerVars["PORT"]; got != "5432" {
		t.Errorf("container PORT=%q", got)
	}
	if keys := devChangedKeys(before, services[0].vars); strings.Join(keys, ",") != "DATABASE_URL,PGHOST" {
		t.Errorf("changed keys=%v", keys)
	}
}

func TestDevWorkingDir(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "api")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := devWorkingDir(root, "/api/", false); err != nil || got != sub {
		t.Fatalf("dir=%s err=%v", got, err)
	}
	if got, err := devWorkingDir(root, "", false); err != nil || got != root {
		t.Fatalf("empty dir=%s err=%v", got, err)
	}
	if _, err := devWorkingDir(root, "../outside", false); err == nil {
		t.Fatal("accepted server root directory outside the linked directory")
	}
	other := t.TempDir()
	if got, err := devWorkingDir(root, other, true); err != nil || got != other {
		t.Fatalf("local absolute dir=%s err=%v", got, err)
	}
	if _, err := devWorkingDir(root, "missing", true); err == nil {
		t.Fatal("accepted missing directory")
	}
}

func TestDevPrefixWriter(t *testing.T) {
	var output strings.Builder
	plan := []devProcessSpec{{service: "api"}, {service: "postgres"}}
	mux := newDevLogMux(&output, plan, false)
	w := &devPrefixWriter{mux: mux, service: "api"}
	if _, err := w.Write([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(" line\r\nsecond")); err != nil {
		t.Fatal(err)
	}
	if err := w.flush(); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), "api      | first line\napi      | second\n"; got != want {
		t.Fatalf("output=%q want %q", got, want)
	}
	output.Reset()
	colored := newDevLogMux(&output, plan, true)
	_ = colored.line("postgres", "ok")
	if got := output.String(); got != "\x1b[33mpostgres |\x1b[0m ok\n" {
		t.Fatalf("colored=%q", got)
	}
}

func TestDevPortReady(t *testing.T) {
	// 연결을 받고 데이터를 보내지 않는 서버(postgres, redis, HTTP)는 준비된 것이다.
	quiet, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer quiet.Close()
	go func() {
		for {
			c, err := quiet.Accept()
			if err != nil {
				return
			}
			go func() { time.Sleep(time.Second); c.Close() }()
		}
	}()
	if !devPortReady(quiet.Addr().(*net.TCPAddr).Port) {
		t.Error("quiet server is not ready")
	}
	// docker 포트 프록시처럼 받자마자 끊는 것은 아직 준비되지 않은 것이다.
	closing, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer closing.Close()
	go func() {
		for {
			c, err := closing.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	if devPortReady(closing.Addr().(*net.TCPAddr).Port) {
		t.Error("closing proxy counted as ready")
	}
	port := closing.Addr().(*net.TCPAddr).Port
	closing.Close()
	if devPortReady(port) {
		t.Error("closed port counted as ready")
	}
}

func TestDevConfigOverrides(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	cfg, err := loadDevConfig()
	if err != nil || len(cfg.Projects) != 0 {
		t.Fatalf("cfg=%+v err=%v", cfg, err)
	}
	cmd, port := "go run .", 3000
	ov := &devServiceOverride{Directory: "api"}
	applyDevOverride(ov, &devOptions{command: &cmd, port: &port})
	if ov.Command != "go run ." || ov.Directory != "api" || ov.Port != 3000 {
		t.Fatalf("override=%+v", ov)
	}
	cfg.Projects["prj_1"] = devProjectConfig{"svc_api": ov}
	if err := cfg.save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadDevConfig()
	if err != nil || loaded.Projects["prj_1"]["svc_api"].Command != "go run ." {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	empty, zero := "", 0
	applyDevOverride(ov, &devOptions{command: &empty, dir: &empty, port: &zero})
	if !ov.empty() {
		t.Fatalf("override not cleared: %+v", ov)
	}
}

func TestLoadDevServices(t *testing.T) {
	pg := "postgres"
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/environments/env_1/volumes", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"vol_1","service_instance_id":"in_pg","name":"pg-data","mount_path":"/var/lib/postgresql/data"},{"id":"vol_2","service_instance_id":null,"mount_path":"/x"}]`)
	})
	mux.HandleFunc("GET /api/services/svc_api/instances/env_1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"in_api","private_domain":"api.dev.keel.internal","root_dir":"/","start_command":null,"port":null}`)
	})
	mux.HandleFunc("GET /api/services/svc_web/instances/env_1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"in_web","private_domain":"web.dev.keel.internal","root_dir":"/web","start_command":"npm start","port":3000}`)
	})
	mux.HandleFunc("GET /api/services/svc_pg/instances/env_1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"in_pg","private_domain":"postgres.dev.keel.internal","image":"postgres:17-alpine","root_dir":"","start_command":null,"port":null}`)
	})
	for _, id := range []string{"svc_api", "svc_web", "svc_pg"} {
		mux.HandleFunc("GET /api/services/"+id+"/instances/env_1/variables/resolved", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, `{"DATABASE_URL":{"value":"postgresql://u:p@postgres.dev.keel.internal:5432/db"},"TOKEN":{"value":"","sealed":true}}`)
		})
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	t.Setenv("KEEL_TOKEN", "test")
	c := &config{URL: srv.URL}
	l := &link{ProjectID: "prj_1", EnvironmentID: "env_1", Environment: "dev"}
	p := &project{ID: "prj_1", Services: []svcRef{
		{ID: "svc_api", Name: "api", SourceType: "repo"},
		{ID: "svc_web", Name: "web", SourceType: "repo"},
		{ID: "svc_pg", Name: "postgres", SourceType: "database", DatabaseKind: &pg},
		{ID: "svc_fn", Name: "fn", SourceType: "function"},
	}}
	// api는 서버에 start command가 없어 건너뛰고, web은 로컬 설정이 명령과 포트를 덮는다.
	local := devProjectConfig{"svc_web": {Command: "npm run dev", Port: 5173}}
	services, skipped, err := loadDevServices(c, l, p, local)
	if err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 2 || skipped[0].name != "api" || skipped[1].name != "fn" {
		t.Fatalf("skipped=%+v", skipped)
	}
	if len(services) != 2 {
		t.Fatalf("services=%d", len(services))
	}
	web, db := services[0], services[1]
	if web.command != "npm run dev" || web.dir != "/web" || web.containerPort != 5173 {
		t.Errorf("web=%+v", web)
	}
	if db.containerPort != 5432 || len(db.volumes) != 1 || db.volumes[0].ID != "vol_1" || !db.container() {
		t.Errorf("db=%+v", db)
	}
	if len(web.sealed) != 1 || web.sealed[0] != "TOKEN" {
		t.Errorf("sealed=%v", web.sealed)
	}
	mux.HandleFunc("GET /api/services/svc_x/instances/env_1", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"id":"in_x","image":"redis"}`)
	})
	mux.HandleFunc("GET /api/services/svc_x/instances/env_1/variables/resolved", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"SECRET":{"value":"•••"}}`)
	})
	p.Services = []svcRef{{ID: "svc_x", Name: "x", SourceType: "image"}}
	if _, _, err := loadDevServices(c, l, p, nil); err == nil || !strings.Contains(err.Error(), "masked") {
		t.Fatalf("masked variables: err=%v", err)
	}
}
