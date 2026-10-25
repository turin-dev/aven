package api

import (
	"context"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/dbquery"
	"github.com/turin-dev/aven/internal/node"
)

func envOf(req node.ExecRunRequest, key string) string {
	for _, e := range req.Env {
		if v, ok := strings.CutPrefix(e, key+"="); ok {
			return v
		}
	}
	return ""
}

func TestDataRowEditPostgres(t *testing.T) {
	df := newDataFixture(t)
	cols := "name,type,pk\nid,integer,t\nname,text,f\n"
	df.run.reply = func(stdin string) *node.ExecOutput {
		switch {
		case strings.Contains(stdin, "pg_attribute"):
			return &node.ExecOutput{Stdout: []byte(cols)}
		case strings.Contains(stdin, "DELETE"):
			return &node.ExecOutput{Stdout: []byte("affected\n0\n")}
		}
		return &node.ExecOutput{Stdout: []byte("affected\n1\n")}
	}
	pv := pvFor(df, "postgres", map[string]string{"name": "users"})
	code, out := df.call(df.srv.dataRowUpdate, "PATCH", "/x?schema=app", `{"pk":{"id":7},"set":{"name":"O'Brien; DROP TABLE users"}}`, pv)
	if code != 200 || out["affected"] != float64(1) {
		t.Fatalf("update: %d %v", code, out)
	}
	want := `WITH r AS (UPDATE "app"."users" SET "name" = E'O''Brien; DROP TABLE users' WHERE "id" = E'7' RETURNING 1) SELECT count(*) AS affected FROM r;` + "\n"
	if got := string(df.run.last().Stdin); got != want {
		t.Errorf("update sql %q", got)
	}
	code, out = df.call(df.srv.dataRowInsert, "POST", "/x", `{"set":{"name":"x"}}`, pv)
	if code != 201 || !strings.Contains(string(df.run.last().Stdin), `INSERT INTO "public"."users" ("name") VALUES (E'x')`) {
		t.Errorf("insert: %d %v %q", code, out, df.run.last().Stdin)
	}
	if code, _ := df.call(df.srv.dataRowDelete, "DELETE", "/x", `{"pk":{"id":"7"}}`, pv); code != 404 {
		t.Errorf("delete missing row: %d", code)
	}
	for name, body := range map[string]string{
		"pk not primary key": `{"pk":{"name":"x"},"set":{"name":"y"}}`,
		"extra pk column":    `{"pk":{"id":1,"name":"x"},"set":{"name":"y"}}`,
		"unknown column":     `{"pk":{"id":1},"set":{"nope":"y"}}`,
		"null pk":            `{"pk":{"id":null},"set":{"name":"y"}}`,
		"empty set":          `{"pk":{"id":1}}`,
		"no pk":              `{"set":{"name":"y"}}`,
	} {
		if code, out := df.call(df.srv.dataRowUpdate, "PATCH", "/x", body, pv); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if code, _ := df.call(df.srv.dataRowInsert, "POST", "/x", `{"pk":{"id":1}}`, pv); code != 400 {
		t.Errorf("insert with pk: %d", code)
	}
	if code, _ := df.call(df.srv.dataRowUpdate, "PATCH", "/x", `{"pk":{"id":1},"set":{"name":"y"}}`, pvFor(df, "postgres", map[string]string{"name": "bad\nname"})); code != 400 {
		t.Errorf("bad table name: %d", code)
	}
	// 기본 키가 없는 표
	cols = "name,type,pk\nid,integer,f\n"
	if code, out := df.call(df.srv.dataRowUpdate, "PATCH", "/x", `{"pk":{"id":1},"set":{"id":2}}`, pv); code != 400 || !strings.Contains(out["error"].(string), "기본 키") {
		t.Errorf("no primary key: %d %v", code, out)
	}
	// 없는 표(mysql처럼 열이 0개)
	cols = "name,type,pk\n"
	if code, _ := df.call(df.srv.dataRowInsert, "POST", "/x", `{}`, pv); code != 404 {
		t.Errorf("missing table: %d", code)
	}
	// 감사: 열 이름과 키만, 값은 남기지 않는다
	evs, _ := df.st.ListEvents(df.ctx, df.p.ID, "", 50)
	n := 0
	for _, ev := range evs {
		if ev.Kind == "database.edit" {
			n++
			if strings.Contains(ev.Message, "O'Brien") || strings.Contains(ev.Message, "DROP") {
				t.Errorf("audit contains value: %s", ev.Message)
			}
		}
	}
	if n != 3 {
		t.Errorf("database.edit events: %d", n)
	}
	for _, kind := range []string{"redis", "mongo"} {
		if code, _ := df.call(df.srv.dataRowUpdate, "PATCH", "/x", `{"pk":{"id":1},"set":{"a":1}}`, pvFor(df, kind, map[string]string{"name": "t"})); code != 400 {
			t.Errorf("%s edit: %d", kind, code)
		}
	}
}

func TestDataRowEditMySQL(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(stdin string) *node.ExecOutput {
		if strings.Contains(stdin, "information_schema.COLUMNS") {
			return &node.ExecOutput{Stdout: []byte("name\ttype\tpk\na\tint\t1\nb\tvarchar(10)\t1\nc\ttext\t0\n")}
		}
		return &node.ExecOutput{Stdout: []byte("affected\n0\n")}
	}
	pv := pvFor(df, "mysql", map[string]string{"name": "t"})
	code, out := df.call(df.srv.dataRowUpdate, "PATCH", "/x?schema=shop", `{"pk":{"a":1,"b":"x"},"set":{"c":"\\'"}}`, pv)
	// mysql ROW_COUNT()는 값이 같으면 0이다: 404가 아니다
	if code != 200 || out["affected"] != float64(0) {
		t.Fatalf("%d %v", code, out)
	}
	req := df.run.last()
	tag := envOf(req, "KEEL_DATA_TAG")
	want := dbquery.MySQLSessionPrelude + "/* keel-data:" + tag + " */\nUPDATE `shop`.`t` SET `c` = '\\\\''' WHERE `a` = '1' AND `b` = 'x' LIMIT 1;\nSELECT ROW_COUNT() AS affected;\n"
	if tag == "" || string(req.Stdin) != want {
		t.Errorf("mysql sql %q", req.Stdin)
	}
	if code, _ := df.call(df.srv.dataRowUpdate, "PATCH", "/x", `{"pk":{"a":1},"set":{"c":"y"}}`, pv); code != 400 {
		t.Errorf("partial composite key: %d", code)
	}
	if code, _ := df.call(df.srv.dataRowDelete, "DELETE", "/x", `{"pk":{"a":1,"b":"x"}}`, pv); code != 404 {
		t.Errorf("delete 0 rows: %d", code)
	}
}

func TestDataTimeoutCleanupPostgres(t *testing.T) {
	df := newDataFixture(t)
	df.run.fail = func(req node.ExecRunRequest) error {
		if strings.Contains(string(req.Stdin), "pg_sleep") {
			return context.DeadlineExceeded
		}
		return nil
	}
	df.run.reply = func(string) *node.ExecOutput { return &node.ExecOutput{Stdout: []byte("cancelled\n1\n")} }
	code, _ := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"SELECT pg_sleep(100)"}`, pvFor(df, "postgres", nil))
	if code != 504 || len(df.run.reqs) != 2 {
		t.Fatalf("%d reqs=%d", code, len(df.run.reqs))
	}
	tag := envOf(df.run.reqs[0], "KEEL_DATA_TAG")
	if envOf(df.run.reqs[0], "PGAPPNAME") != "keel-data-"+tag {
		t.Errorf("appname %v", df.run.reqs[0].Env)
	}
	cl := df.run.reqs[1]
	if envOf(cl, "KILL_TAG") != tag || envOf(cl, "KEEL_DATA_TAG") != "" || !strings.Contains(cl.Cmd[2], "kill -TERM") ||
		!strings.Contains(string(cl.Stdin), "application_name = 'keel-data-"+tag+"'") || !strings.Contains(string(cl.Stdin), "pg_cancel_backend") {
		t.Errorf("cleanup %+v %q", cl.Env, cl.Stdin)
	}
	if df.run.nodes[1] != "nod_remote1" || df.run.conts[1] != "keel-postgres-1" {
		t.Errorf("cleanup target %v %v", df.run.nodes, df.run.conts)
	}
}

func TestDataTimeoutKillQueryMySQL(t *testing.T) {
	df := newDataFixture(t)
	df.run.fail = func(req node.ExecRunRequest) error {
		if strings.Contains(string(req.Stdin), "SLEEP(") {
			return context.DeadlineExceeded
		}
		return nil
	}
	df.run.reply = func(stdin string) *node.ExecOutput {
		if strings.Contains(stdin, "PROCESSLIST") {
			return &node.ExecOutput{Stdout: []byte("id\n55\n")}
		}
		return &node.ExecOutput{}
	}
	code, _ := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"SELECT SLEEP(100)"}`, pvFor(df, "mysql", nil))
	if code != 504 || len(df.run.reqs) != 3 {
		t.Fatalf("%d reqs=%d", code, len(df.run.reqs))
	}
	tag := envOf(df.run.reqs[0], "KEEL_DATA_TAG")
	if !strings.HasPrefix(string(df.run.reqs[0].Stdin), "/* keel-data:"+tag+" */\n") {
		t.Errorf("query not tagged %q", df.run.reqs[0].Stdin)
	}
	if !strings.Contains(string(df.run.reqs[1].Stdin), "LIKE '%keel-data:"+tag+"%'") || envOf(df.run.reqs[1], "KILL_TAG") != tag {
		t.Errorf("find %q", df.run.reqs[1].Stdin)
	}
	if string(df.run.reqs[2].Stdin) != "KILL QUERY 55;\n" {
		t.Errorf("kill %q", df.run.reqs[2].Stdin)
	}
}

func TestDataRedisRESP3Query(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(string) *node.ExecOutput {
		return &node.ExecOutput{Stdout: []byte("1# \"name\" => \"ann\"\n2# \"age\" => \"30\"\n")}
	}
	code, out := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"HGETALL user:1","resp3":true}`, pvFor(df, "redis", nil))
	if code != 200 || out["kind"] != "table" || out["row_count"] != float64(2) {
		t.Fatalf("%d %v", code, out)
	}
	if !strings.Contains(df.run.last().Cmd[2], "--no-raw -3 ") {
		t.Errorf("cmd %v", df.run.last().Cmd)
	}
	// 다른 DB에서는 resp3를 무시한다
	df.run.reply = func(string) *node.ExecOutput { return &node.ExecOutput{Stdout: []byte("n\n1\n")} }
	if code, _ := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"SELECT 1 AS n","resp3":true}`, pvFor(df, "postgres", nil)); code != 200 {
		t.Errorf("pg resp3: %d", code)
	}
}
