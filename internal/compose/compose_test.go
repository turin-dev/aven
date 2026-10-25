package compose

import (
	"context"
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
