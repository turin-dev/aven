package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParsePostgresArgs(t *testing.T) {
	o, err := parsePostgresArgs([]string{"pgbouncer", "add", "-s", "db", "--pool-mode", "session"})
	if err != nil || o.area != "pooling" || o.action != "enable" || o.svc != "db" || o.poolMode != "session" {
		t.Fatalf("%+v %v", o, err)
	}
	b, _ := json.Marshal(o.poolingBody())
	if string(b) != `{"enabled":true,"pool_mode":"session"}` {
		t.Errorf("body %s", b)
	}
	o, _ = parsePostgresArgs([]string{"pooling", "enable"})
	if o.poolingBody()["pool_mode"] != "transaction" {
		t.Errorf("enable defaults to transaction: %v", o.poolingBody())
	}
	o, err = parsePostgresArgs([]string{"pooling", "configure", "--default-pool-size", "30", "--max-prepared-statements", "0"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ = json.Marshal(o.poolingBody())
	if string(b) != `{"default_pool_size":30,"enabled":true,"max_prepared_statements":0}` {
		t.Errorf("configure body %s", b)
	}
	if o, err := parsePostgresArgs([]string{"pooling"}); err != nil || o.action != "status" {
		t.Fatalf("status default: %+v %v", o, err)
	}
	o, err = parsePostgresArgs([]string{"upgrade", "start", "--to", "18", "-y", "-e", "staging"})
	if err != nil || o.action != "start" || o.to != 18 || !o.yes || o.env != "staging" {
		t.Fatalf("upgrade: %+v %v", o, err)
	}
	for _, bad := range [][]string{
		nil, {"replicas"}, {"pooling", "explode"},
		{"pooling", "configure"},                        // 바꿀 값 없음
		{"pooling", "status", "--pool-mode", "session"}, // 플래그가 맞지 않는 명령
		{"pooling", "enable", "--pool-mode", "bogus"},
		{"upgrade", "revert", "--to", "18"},
		{"upgrade", "start", "extra"},
	} {
		if _, err := parsePostgresArgs(bad); err == nil {
			t.Errorf("accepted %v", bad)
		}
	}
}

func TestFormatPostgres(t *testing.T) {
	var p pgPooling
	json.Unmarshal([]byte(`{"status":"enabled","pool_mode":"transaction","default_pool_size":20,"max_client_conn":1000,"max_prepared_statements":null,
		"pooler_service_name":"db-pgbouncer","variables":["DATABASE_URL","DATABASE_UNPOOLED_URL"]}`), &p)
	out := formatPooling("db", "production", &p)
	for _, want := range []string{"PgBouncer enabled (db-pgbouncer)", "pool mode: transaction", "max prepared statements: default", "DATABASE_UNPOOLED_URL"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
	if !strings.Contains(formatPooling("db", "p", &pgPooling{Status: "off"}), "PgBouncer off") {
		t.Error("off")
	}
	pf := &pgPreflight{Ready: false, CurrentMajor: 16, TargetMajor: 18, Checks: []pgCheck{
		{Label: "확장", Severity: "blocker", Message: "postgis 없음"}, {Label: "슬롯", Severity: "warning", Message: "slot"},
		{Label: "이미지", Severity: "blocker", Passed: true, Message: "hidden"}}}
	out = formatPreflight(pf)
	if !strings.Contains(out, "[FAIL] 확장: postgis 없음") || !strings.Contains(out, "[warn] 슬롯") || strings.Contains(out, "hidden") || !strings.Contains(out, "not ready") {
		t.Errorf("preflight:\n%s", out)
	}
	u := &pgUpgrade{Status: "succeeded", CurrentMajor: 18, FromMajor: 16, TargetMajor: 18, Image: "pgautoupgrade/pgautoupgrade:18-alpine", BackupID: "bak_1", CanRevert: true}
	if out := formatUpgrade("db", "production", u); !strings.Contains(out, "16 → 18 succeeded") || !strings.Contains(out, "revert available") {
		t.Errorf("upgrade:\n%s", out)
	}
}
