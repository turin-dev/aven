package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// plainOutput turns colors off for formatting tests, whatever the terminal is.
func plainOutput(t *testing.T) {
	t.Helper()
	initColors()
	so, se := stdoutColor, errColor
	stdoutColor, errColor = false, false
	t.Cleanup(func() { stdoutColor, errColor = so, se })
}

func TestFormatStatus(t *testing.T) {
	plainOutput(t)
	now := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	var st statusReport
	raw := `{"project":"shop","environment":"production","service":"web","service_id":"svc_1",
	  "instance":{"status":"running","private_domain":"web.production.keel.internal",
	    "domains":[{"host":"web.example.com","kind":"custom","status":"active"}],
	    "tcp_proxy":{"container_port":5432,"public_port":20001},"pending_changes":["variables"]},
	  "deployment":{"id":"dep_1","status":"live","trigger":"cli","commit_sha":"0123456789abcdef","commit_message":"fix bug\nmore",
	    "error":null,"created_at":"2026-01-02T11:55:00Z","replicas":[{"name":"web-1","status":"running","restarts":2}]}}`
	if err := json.Unmarshal([]byte(raw), &st); err != nil {
		t.Fatal(err)
	}
	got := formatStatus(st, now)
	want := "Project:      shop\n" +
		"Environment:  production\n" +
		"Service:      web\n" +
		"Status:       running\n" +
		"Deployment:   dep_1 (live, cli, 5m ago) 0123456 fix bug\n" +
		"Replica:      web-1 running (restarts 2)\n" +
		"Domain:       https://web.example.com (custom, active)\n" +
		"Private:      web.production.keel.internal\n" +
		"TCP proxy:    :20001 → 5432\n" +
		"Pending:      variables (apply with: aven redeploy or aven variables --deploy)\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	noSvc := formatStatus(statusReport{Project: "shop", Environment: "production"}, now)
	if !strings.Contains(noSvc, "Service:      (none; run: aven service use <name>)") {
		t.Errorf("no service:\n%s", noSvc)
	}
	// --json keeps the API field names
	b, _ := json.Marshal(st)
	if !strings.Contains(string(b), `"current_deployment_id":null`) || !strings.Contains(string(b), `"deployment":{"id":"dep_1"`) {
		t.Errorf("json: %s", b)
	}
}

func TestFormatVars(t *testing.T) {
	plainOutput(t)
	vars := map[string]string{"PORT": "8080", "API_KEY": "", "DATABASE_URL": "postgres://x"}
	sealed := []string{"API_KEY"}
	if got := formatVars(vars, sealed, true); got != "API_KEY       <sealed>\nDATABASE_URL  postgres://x\nPORT          8080\n" {
		t.Errorf("table:\n%q", got)
	}
	if got := formatVars(vars, sealed, false); got != "# API_KEY=<sealed>\nDATABASE_URL=postgres://x\nPORT=8080\n" {
		t.Errorf("kv:\n%q", got)
	}
	if got := formatVars(nil, nil, true); !strings.Contains(got, "no variables") {
		t.Errorf("empty: %q", got)
	}
	if got := formatVars(nil, nil, false); got != "" {
		t.Errorf("empty kv must stay empty (scripts): %q", got)
	}
	if o, err := parseVarsArgs([]string{"--json", "-s", "web"}); err != nil || !o.json || o.service != "web" {
		t.Errorf("--json: %v %+v", err, o)
	}
}

func TestChooseProjectAndService(t *testing.T) {
	ps := []project{{ID: "prj_1", Name: "shop"}, {ID: "prj_2", Name: "blog"}, {ID: "prj_3", Name: "blog"}}
	if p, err := chooseProject(ps, "shop", false); err != nil || p.ID != "prj_1" {
		t.Errorf("by name: %v %v", p, err)
	}
	if p, err := chooseProject(ps, "prj_3", false); err != nil || p.ID != "prj_3" {
		t.Errorf("by id: %v %v", p, err)
	}
	if _, err := chooseProject(ps, "blog", false); err == nil || !strings.Contains(err.Error(), "several workspaces") {
		t.Errorf("ambiguous name must not pick silently: %v", err)
	}
	if _, err := chooseProject(ps, "", false); err == nil || !strings.Contains(err.Error(), "-p") {
		t.Errorf("several projects, no terminal: %v", err)
	}
	if p, err := chooseProject(ps[:1], "", false); err != nil || p.ID != "prj_1" {
		t.Errorf("only project: %v %v", p, err)
	}
	if _, err := chooseProject(nil, "", false); err == nil || !strings.Contains(err.Error(), "aven init") {
		t.Errorf("no projects: %v", err)
	}
	if got := projectLabels(ps); strings.Join(got, "|") != "shop|blog (prj_2)|blog (prj_3)" {
		t.Errorf("labels: %v", got)
	}

	ss := []svcRef{{ID: "svc_1", Name: "web"}, {ID: "svc_2", Name: "db"}}
	if s, err := chooseService(ss, "db", false); err != nil || s.ID != "svc_2" {
		t.Errorf("by name: %v %v", s, err)
	}
	if _, err := chooseService(ss, "nope", false); err == nil || !strings.Contains(err.Error(), "web, db") {
		t.Errorf("unknown: %v", err)
	}
	if s, err := chooseService(ss, "", false); err != nil || s != nil {
		t.Errorf("several, no terminal → no service: %v %v", s, err)
	}
	if s, _ := chooseService(ss[:1], "", false); s == nil || s.ID != "svc_1" {
		t.Errorf("only service: %v", s)
	}
}

func TestPad(t *testing.T) {
	if got := pad("\x1b[1mab\x1b[0m", "ab", 4); got != "\x1b[1mab\x1b[0m  " {
		t.Errorf("%q", got)
	}
	if got := pad("abcdef", "abcdef", 3); got != "abcdef" {
		t.Errorf("%q", got)
	}
}
