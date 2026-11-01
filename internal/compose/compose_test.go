package compose

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, file string, in Input) *Plan {
	t.Helper()
	b, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	in.YAML = string(b)
	if in.Stack == "" {
		in.Stack = "t"
	}
	p, err := Parse(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	p.AssignNames(nil, nil)
	return p
}

func codes(p *Plan) map[string]bool {
	m := map[string]bool{}
	for _, e := range p.Errors {
		m[e.Code] = true
	}
	return m
}

func TestRealFixtures(t *testing.T) {
	cases := []struct {
		file    string
		ok      bool
		errCode string
	}{
		{"n8n.yml", true, ""},
		{"registry.yml", true, ""},
		{"downloads.yml", false, "bind_mount"},
		{"monitoring.yml", false, "network_mode"},
		{"node-exporter.yml", false, "privileged"},
		{"mailu.yml", false, "shared_volume"},
	}
	for _, c := range cases {
		p := load(t, c.file, Input{Vars: map[string]string{"MAILU_VERSION": "2024.06"}})
		if p.OK() != c.ok {
			t.Errorf("%s: ok=%v want %v errors=%v", c.file, p.OK(), c.ok, p.Errors)
		}
		if c.errCode != "" && !codes(p)[c.errCode] {
			t.Errorf("%s: missing error %s: %v", c.file, c.errCode, p.Errors)
		}
	}
}

func TestBindMountAllowed(t *testing.T) {
	p := load(t, "downloads.yml", Input{AllowBindMounts: true})
	if !p.OK() {
		t.Fatal(p.Errors)
	}
	if len(p.Services[0].BindMounts) != 1 || !p.Services[0].BindMounts[0].ReadOnly {
		t.Fatalf("%+v", p.Services[0].BindMounts)
	}
}

func TestN8nMapping(t *testing.T) {
	p := load(t, "n8n.yml", Input{Vars: map[string]string{"N8N_HOST": "n.example.com", "N8N_PORT": "5678", "GENERIC_TIMEZONE": "UTC"}})
	s := p.Service("n8n")
	if s.Variables["WEBHOOK_URL"] != "https://n.example.com/" {
		t.Errorf("interpolation: %q", s.Variables["WEBHOOK_URL"])
	}
	if s.Port == nil || *s.Port != 5678 || s.RestartPolicy != "always" {
		t.Errorf("port/restart: %v %s", s.Port, s.RestartPolicy)
	}
	if len(s.Volumes) != 1 || s.Volumes[0].MountPath != "/home/node/.n8n" {
		t.Errorf("volumes %+v", s.Volumes)
	}
	if len(s.DomainCandidates) != 1 || s.DomainCandidates[0].Port != 5678 {
		t.Errorf("domain candidates %+v", s.DomainCandidates)
	}
}

const stackYAML = `
name: shop
services:
  web:
    build: {context: ., dockerfile: Dockerfile, target: prod, args: {A: "1"}}
    ports: ["8080:3000"]
    command: ["node", "server.js"]
    depends_on:
      db: {condition: service_healthy}
      cache: {condition: service_started}
    environment: {DB_URL: "postgres://db:5432/${DB_NAME:-app}"}
    deploy: {replicas: 2}
  db:
    image: postgres:17
    healthcheck: {test: ["CMD-SHELL", "pg_isready -U app"], interval: 5s, retries: 5}
    volumes: [pgdata:/var/lib/postgresql/data]
    networks: {default: {aliases: [database]}}
  cache:
    image: redis:7
volumes: {pgdata: {}}
`

func TestStackOrderingAndMapping(t *testing.T) {
	dir := t.TempDir()
	p, err := Parse(context.Background(), Input{YAML: stackYAML, Stack: "shop", RepoURL: "https://x/y.git", Branch: "main", RepoDir: dir, ComposeDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	p.AssignNames(nil, nil)
	if !p.OK() {
		t.Fatal(p.Errors)
	}
	if len(p.Levels) != 2 || p.Levels[1][0] != "web" {
		t.Fatalf("levels %v", p.Levels)
	}
	w := p.Service("web")
	if w.Build == nil || w.Build.Target != "prod" || w.Spec.Replicas != 2 || w.Variables["DB_URL"] != "postgres://db:5432/app" {
		t.Errorf("web %+v %v", w.Build, w.Variables)
	}
	if *w.Port != 3000 {
		t.Errorf("port %d", *w.Port)
	}
	db := p.Service("db")
	if db.HealthcheckType != "tcp" || *db.Port != 5432 {
		t.Errorf("db hc %s %v", db.HealthcheckType, db.Port)
	}
	if strings.Join(db.Aliases, ",") != "database" && !strings.Contains(strings.Join(db.Aliases, ","), "database") {
		t.Errorf("aliases %v", db.Aliases)
	}
	if w.DependsOn[0].Condition != "healthy" && w.DependsOn[1].Condition != "healthy" {
		t.Errorf("deps %+v", w.DependsOn)
	}
}

func TestBuildNeedsRepo(t *testing.T) {
	p, _ := Parse(context.Background(), Input{YAML: stackYAML, Stack: "shop"})
	if !codes(p)["build_needs_repo"] {
		t.Fatal(p.Errors)
	}
}

func TestCycleAndSecrets(t *testing.T) {
	p, err := Parse(context.Background(), Input{Stack: "c", YAML: `
services:
  a: {image: x, depends_on: [b]}
  b: {image: y, depends_on: [a]}
`})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatal(err, p)
	}
	p, err = Parse(context.Background(), Input{Stack: "c", YAML: `
services:
  a: {image: x, secrets: [s]}
secrets: {s: {file: ./s.txt}}
`})
	if err != nil || !codes(p)["secrets"] {
		t.Fatal(err, p)
	}
}

func TestNameCollision(t *testing.T) {
	p, _ := Parse(context.Background(), Input{Stack: "s", YAML: "services:\n  db: {image: postgres}\n"})
	p.AssignNames(nil, map[string]bool{"db": true})
	if p.Services[0].Name != "s-db" || p.Services[0].Aliases[0] != "db" {
		t.Fatalf("%s %v", p.Services[0].Name, p.Services[0].Aliases)
	}
}

func TestHealthParse(t *testing.T) {
	k, port, path := parseHealthTest([]string{"CMD", "curl", "-f", "http://localhost:8080/health"})
	if k != "http" || port != 8080 || path != "/health" {
		t.Fatal(k, port, path)
	}
}

// env_file이 저장소 밖을 가리키는 심볼릭 링크면 읽지 않는다(서버 파일이 서비스 변수로 들어가지 않게).
func TestEnvFileSymlinkOutsideRepo(t *testing.T) {
	outside := t.TempDir()
	secret := outside + "/aven.env"
	os.WriteFile(secret, []byte("KEEL_ADMIN_PASSWORD=hunter2\n"), 0o600)
	repo := t.TempDir()
	os.WriteFile(repo+"/app.env", []byte("GOOD=1\n"), 0o644)
	if err := os.Symlink(secret, repo+"/leak.env"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("app.env", repo+"/alias.env"); err != nil {
		t.Fatal(err)
	}
	big := strings.Repeat("A=1\n", maxEnvFileSize/4+1)
	os.WriteFile(repo+"/big.env", []byte(big), 0o644)
	yml := `services:
  web:
    image: nginx:1
    env_file: [app.env, alias.env, leak.env, big.env]
`
	p, err := Parse(context.Background(), Input{YAML: yml, Stack: "t", RepoURL: "https://example.com/r.git", RepoDir: repo, ComposeDir: repo})
	if err != nil {
		t.Fatal(err)
	}
	p.AssignNames(nil, nil)
	vars := p.Service("web").Variables
	if vars["GOOD"] != "1" {
		t.Fatalf("regular env_file not read: %v", vars)
	}
	if _, ok := vars["KEEL_ADMIN_PASSWORD"]; ok {
		t.Fatal("env_file symlink escaped the repository")
	}
	if _, ok := vars["A"]; ok {
		t.Fatal("oversized env_file was read")
	}
	missing := 0
	for _, w := range p.Warnings {
		if w.Code == "env_file_missing" {
			missing++
		}
	}
	if missing != 2 {
		t.Fatalf("want 2 env_file_missing warnings (leak, big), got %d: %v", missing, p.Warnings)
	}
}

func TestExposePorts(t *testing.T) {
	cases := []struct {
		in   string
		want []int
		udp  bool
	}{
		{"8080", []int{8080}, false},
		{"8080/tcp", []int{8080}, false},
		{"8080/TCP", []int{8080}, false},
		{"53/udp", nil, true},
		{"9000-9002", []int{9000, 9001, 9002}, false},
		{"9000-9002/tcp", []int{9000, 9001, 9002}, false},
		{"1-100", []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}, false},
		{"9002-9000", nil, false},
		{"abc", nil, false},
		{"70000", nil, false},
		{"80/sctp", nil, false},
	}
	for _, c := range cases {
		got, udp := exposePorts(c.in)
		if fmt.Sprint(got) != fmt.Sprint(c.want) || udp != c.udp {
			t.Errorf("exposePorts(%q) = %v %v, want %v %v", c.in, got, udp, c.want, c.udp)
		}
	}
	// expose의 프로토콜 표기도 서비스 포트가 된다.
	p, err := Parse(context.Background(), Input{YAML: "services:\n  web:\n    image: nginx:1\n    expose: [\"8080/tcp\"]\n", Stack: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Service("web"); s.Port == nil || *s.Port != 8080 || len(s.DomainCandidates) != 1 {
		t.Fatalf("port %v candidates %+v", s.Port, s.DomainCandidates)
	}
}
