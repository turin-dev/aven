package dbquery

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseRedisRESP3(t *testing.T) {
	out := strings.Join([]string{
		`1# "server" => "redis"`,
		`2# "proto" => (integer) 3`,
		`3# "modules" => (empty array)`,
		`4# "nested" => 1) "a"`,
		`   2) "b"`, // redis-cli: 중첩 응답의 이어지는 줄은 번호 칸 폭+2만큼 들여 쓴다("=> " 뒤에 맞추지 않는다)
		`5# "score" => (double) 1.5`,
		`6# "ok" => (true)`,
		`7# "big" => (big number) 3492890328409238509324850943850943825024385`,
		`8# "none" => (nil)`,
		`9# "m" => 1# "x" => (integer) 1`,
		`   2# "y" => "q\"z"`,
		`1~ "s1"`,
		`2~ "s2"`,
		`(empty hash)`,
		`(empty set)`,
		`(double) inf`,
		`(false)`,
	}, "\n") + "\n"
	reps, err := ParseRedis(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 6 {
		t.Fatalf("replies: %d %+v", len(reps), reps)
	}
	m := reps[0]
	if m.Type != "map" || len(m.Elems) != 18 {
		t.Fatalf("map: %+v", m)
	}
	want := map[string]string{"server": "string", "proto": "integer", "modules": "array", "nested": "array", "score": "double",
		"ok": "bool", "big": "bignum", "none": "nil", "m": "map"}
	for i := 0; i < len(m.Elems); i += 2 {
		k := m.Elems[i].Str
		if want[k] != m.Elems[i+1].Type {
			t.Errorf("%s: %s", k, m.Elems[i+1].Type)
		}
	}
	if n := m.Elems[7]; len(n.Elems) != 2 || n.Elems[1].Str != "b" {
		t.Errorf("nested %+v", n)
	}
	if mm := m.Elems[17]; mm.Text() != `x: 1, y: q"z` {
		t.Errorf("nested map %q", mm.Text())
	}
	if m.Elems[13].Str != "3492890328409238509324850943850943825024385" {
		t.Errorf("bignum %+v", m.Elems[13])
	}
	if s := reps[1]; s.Type != "set" || len(s.Elems) != 2 || s.Elems[1].Str != "s2" {
		t.Errorf("set %+v", s)
	}
	if reps[2].Type != "map" || reps[3].Type != "set" || reps[4].Type != "double" || reps[5].Str != "false" {
		t.Errorf("scalars %+v", reps[2:])
	}
}

// 10개 이상이면 번호를 오른쪽 정렬한다(" 1# "), map은 원소 쌍 수로 폭을 정한다.
func TestParseRedisRESP3Wide(t *testing.T) {
	var lines []string
	for i := 1; i <= 10; i++ {
		pre := " "
		if i == 10 {
			pre = ""
		}
		lines = append(lines, pre+itoa(i)+`# "k`+itoa(i)+`" => "v"`)
	}
	reps, err := ParseRedis(strings.Join(lines, "\n"))
	if err != nil || len(reps) != 1 || len(reps[0].Elems) != 20 || reps[0].Elems[18].Str != "k10" {
		t.Fatalf("%v %+v", err, reps)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

// verbatim 문자열은 들여쓰기 없이 그대로 찍힌다: 중첩 응답 안에서 다음 번호까지 이어 붙인다.
func TestParseRedisVerbatimInAggregate(t *testing.T) {
	out := "1) 1) txt line one\nline two\n   2) \"after\"\n2) \"x\"\n"
	reps, err := ParseRedis(out)
	if err != nil || len(reps) != 1 {
		t.Fatalf("%v %+v", err, reps)
	}
	inner := reps[0].Elems[0]
	if inner.Elems[0].Type != "verbatim" || inner.Elems[0].Str != "txt line one\nline two" || inner.Elems[1].Str != "after" || reps[0].Elems[1].Str != "x" {
		t.Errorf("%+v", reps[0])
	}
}

func TestRedisResultMapTable(t *testing.T) {
	res := RedisResult("1# \"name\" => \"ann\"\n2# \"age\" => (integer) 30\n3# \"gone\" => (nil)\n", false)
	if res.Kind != "table" || !reflect.DeepEqual(res.Columns, []string{"key", "value"}) || len(res.Rows) != 3 || res.Rows[1][1] != "30" || res.Rows[2][1] != nil {
		t.Errorf("%+v", res)
	}
	if res := RedisResult("1) \"a\"\n", false); res.Kind != "text" {
		t.Errorf("array should stay text: %+v", res)
	}
}

func TestCleanupCommands(t *testing.T) {
	tag := "0123456789abcdef01234567"
	pg := Target{Kind: "postgres", Vars: map[string]string{"PGPASSWORD": "pw"}, Tag: "ffffffffffffffffffffffff"}
	c, err := pg.Cleanup(tag)
	if err != nil {
		t.Fatal(err)
	}
	script := c.Cmd[len(c.Cmd)-1]
	env := strings.Join(c.Env, " ")
	if !strings.Contains(script, `grep -qx "AVEN_DATA_TAG=$KILL_TAG"`) || !strings.Contains(script, `grep -qx "KEEL_DATA_TAG=$KILL_TAG"`) || !strings.Contains(script, "kill -TERM") || !strings.Contains(script, "exec $T psql") {
		t.Errorf("script %s", script)
	}
	// 정리 프로세스 자신은 kill grep에 걸리면 안 된다.
	if !strings.Contains(env, "KILL_TAG="+tag) || strings.Contains(env, "KEEL_DATA_TAG=") || strings.Contains(env, "AVEN_DATA_TAG=") || !strings.Contains(env, "PGAPPNAME=aven-data") {
		t.Errorf("env %s", env)
	}
	if !strings.Contains(string(c.Stdin), "pg_cancel_backend(pid)") || !strings.Contains(string(c.Stdin), "application_name IN ('aven-data-"+tag+"', 'keel-data-"+tag+"')") ||
		!strings.Contains(string(c.Stdin), "pid <> pg_backend_pid()") {
		t.Errorf("pg stdin %s", c.Stdin)
	}
	my := Target{Kind: "mysql", Tag: tag}
	c, _ = my.Cleanup(tag)
	if !strings.Contains(string(c.Stdin), "aven-data:"+tag) || !strings.Contains(string(c.Stdin), "keel-data:"+tag) || !strings.Contains(string(c.Stdin), "CONNECTION_ID()") {
		t.Errorf("mysql find %q", c.Stdin)
	}
	if got := MySQLKillQuery(ParseIDs(SQLResult("mysql", []byte("id\n17\n42\n"), false))); got != "KILL QUERY 17;\nKILL QUERY 42;\n" {
		t.Errorf("kill %q", got)
	}
	if MySQLKillQuery(nil) != "" || MySQLKillQuery([]int64{0, -1}) != "" {
		t.Error("kill of nothing")
	}
	for _, bad := range []string{"", "x'; DROP", "ABCDEF0123456789", "0123%"} {
		if _, err := pg.Cleanup(bad); err == nil {
			t.Errorf("tag %q accepted", bad)
		}
		if _, err := MySQLFindQuery(bad); err == nil {
			t.Errorf("find %q accepted", bad)
		}
	}
	if c, err := (Target{Kind: "redis"}).Cleanup(tag); err != nil || !strings.Contains(c.Cmd[2], "AVEN_DATA_TAG") || !strings.Contains(c.Cmd[2], "KEEL_DATA_TAG") || len(c.Stdin) != 0 {
		t.Errorf("redis cleanup %v %v", c, err)
	}
}

func TestTaggedCommands(t *testing.T) {
	tag := "abcdef012345abcdef012345"
	c, _ := Target{Kind: "postgres", Tag: tag}.SQL("SELECT 1")
	env := strings.Join(c.Env, " ")
	if !strings.Contains(env, "PGAPPNAME=aven-data-"+tag) || !strings.Contains(env, "AVEN_DATA_TAG="+tag) || !strings.Contains(env, "KEEL_DATA_TAG="+tag) || string(c.Stdin) != "SELECT 1" {
		t.Errorf("pg %s %q", env, c.Stdin)
	}
	c, _ = Target{Kind: "mysql", Tag: tag}.SQL("SELECT SLEEP(100)")
	if string(c.Stdin) != "/* aven-data:"+tag+" */\nSELECT SLEEP(100)" || !strings.Contains(c.Cmd[2], "--comments") {
		t.Errorf("mysql %q %s", c.Stdin, c.Cmd[2])
	}
	c, _ = Target{Kind: "mysql", Tag: tag}.SQL(MySQLSessionPrelude + "UPDATE t SET a = 1;")
	if string(c.Stdin) != MySQLSessionPrelude+"/* aven-data:"+tag+" */\nUPDATE t SET a = 1;" {
		t.Errorf("mysql edit %q", c.Stdin)
	}
	c, _ = Target{Kind: "mysql", Tag: tag}.SQL("DELIMITER //\nCREATE PROCEDURE p() BEGIN END//")
	if strings.Contains(string(c.Stdin), "aven-data") || strings.Contains(string(c.Stdin), "keel-data") {
		t.Errorf("delimiter script tagged: %q", c.Stdin)
	}
	c, _ = Target{Kind: "redis", RESP3: true}.Redis(0, "PING")
	if !strings.Contains(c.Cmd[2], "redis-cli --no-raw -3 ") {
		t.Errorf("resp3 %s", c.Cmd[2])
	}
	e, _ := MongoFindEval("app", "users", 10, 0)
	if !strings.Contains(e, ".limit(11).maxTimeMS(25000).toArray()") {
		t.Errorf("mongo %s", e)
	}
	if len(NewTag()) != 24 || !tagRe.MatchString(NewTag()) || NewTag() == NewTag() {
		t.Error("NewTag")
	}
}

func TestQuoteLiteralInjection(t *testing.T) {
	for _, tc := range []struct{ kind, in, want string }{
		{"postgres", "it's", `E'it''s'`},
		{"postgres", `a\'; DROP TABLE x; --`, `E'a\\''; DROP TABLE x; --'`},
		{"postgres", "line\nbreak", "E'line\nbreak'"},
		{"mysql", "it's", `'it''s'`},
		{"mysql", `\'); DROP TABLE x; -- `, `'\\''); DROP TABLE x; -- '`},
		{"mariadb", `\`, `'\\'`},
	} {
		got, err := QuoteLiteral(tc.kind, tc.in)
		if err != nil || got != tc.want {
			t.Errorf("%s %q: %q %v", tc.kind, tc.in, got, err)
		}
	}
	// 인용한 글자 안에서 '가 홀로 나오지 않는다(따옴표가 짝수 개로 이어진다): 문자열 밖으로 나갈 수 없다
	for _, in := range []string{`'`, `\'`, `\\'`, `'''`, `\`, "x'\\\\'y"} {
		for _, kind := range []string{"postgres", "mysql"} {
			q, _ := QuoteLiteral(kind, in)
			body := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(q, "E"), "'"), "'")
			if lone(body) {
				t.Errorf("%s %q → %q", kind, in, q)
			}
		}
	}
	if _, err := QuoteLiteral("postgres", "a\x00b"); err == nil {
		t.Error("NUL accepted")
	}
	if _, err := QuoteLiteral("postgres", "\xff"); err == nil {
		t.Error("invalid UTF-8 accepted")
	}
	if _, err := QuoteLiteral("redis", "x"); err == nil {
		t.Error("redis literal")
	}
}

// lone은 이스케이프를 풀면서 읽을 때 문자열을 닫는 '가 있는지 본다(백슬래시 이스케이프를 쓰는 해석과 쓰지 않는 해석 모두).
func lone(body string) bool {
	for _, backslash := range []bool{true, false} {
		for i := 0; i < len(body); i++ {
			switch {
			case backslash && body[i] == '\\':
				i++
			case body[i] == '\'':
				if i+1 < len(body) && body[i+1] == '\'' {
					i++
				} else {
					return true
				}
			}
		}
	}
	return false
}

func num(s string) json.Number { return json.Number(s) }

func TestRowEditSQL(t *testing.T) {
	pg := RowEdit{Kind: "postgres", Schema: "app", Table: `us"ers`, PK: map[string]any{"id": num("7")},
		Set: map[string]any{"name": "O'Brien", "active": true, "note": nil, "meta": map[string]any{"a": num("1")}}}
	q, err := pg.UpdateSQL()
	want := `WITH r AS (UPDATE "app"."us""ers" SET "active" = TRUE, "meta" = E'{"a":1}', "name" = E'O''Brien', "note" = NULL WHERE "id" = E'7' RETURNING 1) SELECT count(*) AS affected FROM r;` + "\n"
	if err != nil || q != want {
		t.Errorf("pg update\n%s\n%s %v", q, want, err)
	}
	q, _ = pg.DeleteSQL()
	if q != `WITH r AS (DELETE FROM "app"."us""ers" WHERE "id" = E'7' RETURNING 1) SELECT count(*) AS affected FROM r;`+"\n" {
		t.Errorf("pg delete %s", q)
	}
	ins := RowEdit{Kind: "postgres", Schema: "public", Table: "t", Set: map[string]any{"b": "x", "a": num("-1.5e3")}}
	q, _ = ins.InsertSQL()
	if q != `WITH r AS (INSERT INTO "public"."t" ("a", "b") VALUES (E'-1.5e3', E'x') RETURNING 1) SELECT count(*) AS affected FROM r;`+"\n" {
		t.Errorf("pg insert %s", q)
	}
	q, _ = RowEdit{Kind: "postgres", Table: "t"}.InsertSQL()
	if !strings.Contains(q, `INSERT INTO "t" DEFAULT VALUES RETURNING 1`) {
		t.Errorf("pg default insert %s", q)
	}
	my := RowEdit{Kind: "mysql", Schema: "shop", Table: "or`ders", PK: map[string]any{"a": num("1"), "b": "x"}, Set: map[string]any{"c`ol": `\'`}}
	q, _ = my.UpdateSQL()
	if q != MySQLSessionPrelude+"UPDATE `shop`.`or``ders` SET `c``ol` = '\\\\''' WHERE `a` = '1' AND `b` = 'x' LIMIT 1;\nSELECT ROW_COUNT() AS affected;\n" {
		t.Errorf("mysql update %q", q)
	}
	q, _ = my.DeleteSQL()
	if q != MySQLSessionPrelude+"DELETE FROM `shop`.`or``ders` WHERE `a` = '1' AND `b` = 'x' LIMIT 1;\nSELECT ROW_COUNT() AS affected;\n" {
		t.Errorf("mysql delete %q", q)
	}
	q, _ = RowEdit{Kind: "mariadb", Table: "t"}.InsertSQL()
	if q != MySQLSessionPrelude+"INSERT INTO `t` () VALUES ();\nSELECT ROW_COUNT() AS affected;\n" {
		t.Errorf("mysql default insert %q", q)
	}
	// 거부: null 기본 키, 빈 pk/set, 잘못된 숫자, 제어 문자 열 이름, SQL이 아닌 DB
	for name, e := range map[string]RowEdit{
		"null pk":   {Kind: "postgres", Table: "t", PK: map[string]any{"id": nil}, Set: map[string]any{"a": "x"}},
		"no pk":     {Kind: "postgres", Table: "t", Set: map[string]any{"a": "x"}},
		"no set":    {Kind: "postgres", Table: "t", PK: map[string]any{"id": num("1")}},
		"bad num":   {Kind: "postgres", Table: "t", PK: map[string]any{"id": num("1; DROP")}, Set: map[string]any{"a": "x"}},
		"bad col":   {Kind: "mysql", Table: "t", PK: map[string]any{"id": num("1")}, Set: map[string]any{"a\nb": "x"}},
		"bad table": {Kind: "mysql", Table: "", PK: map[string]any{"id": num("1")}, Set: map[string]any{"a": "x"}},
		"mongo":     {Kind: "mongo", Table: "t", PK: map[string]any{"id": num("1")}, Set: map[string]any{"a": "x"}},
	} {
		if q, err := e.UpdateSQL(); err == nil {
			t.Errorf("%s accepted: %s", name, q)
		}
	}
	if n, err := ParseAffected(SQLResult("postgres", []byte("affected\n1\n"), false)); err != nil || n != 1 {
		t.Errorf("affected %d %v", n, err)
	}
}

func TestColumnsQuery(t *testing.T) {
	q, err := ColumnsQuery("postgres", "app", `us"er's`)
	if err != nil || !strings.Contains(q, `a.attrelid = E'"app"."us""er''s"'::regclass`) || !strings.Contains(q, "i.indisprimary") {
		t.Errorf("pg %s %v", q, err)
	}
	q, _ = ColumnsQuery("mysql", "", "or'ders")
	if !strings.Contains(q, "TABLE_SCHEMA = DATABASE() AND TABLE_NAME = 'or''ders'") || !strings.HasPrefix(q, MySQLSessionPrelude) {
		t.Errorf("mysql %s", q)
	}
	q, _ = ColumnsQuery("mysql", "shop", "t")
	if !strings.Contains(q, "TABLE_SCHEMA = 'shop'") {
		t.Errorf("mysql schema %s", q)
	}
	cols, err := ParseColumns(SQLResult("postgres", []byte("name,type,pk\nid,integer,t\nname,character varying(40),f\n"), false))
	if err != nil || len(cols) != 2 || !cols[0].PK || cols[1].PK || cols[1].Type != "character varying(40)" {
		t.Errorf("parse %+v %v", cols, err)
	}
	if pk := PrimaryKey(cols); !reflect.DeepEqual(pk, []string{"id"}) {
		t.Errorf("pk %v", pk)
	}
	cols, _ = ParseColumns(SQLResult("mysql", []byte("name\ttype\tpk\nid\tint\t1\n"), false))
	if len(cols) != 1 || !cols[0].PK || cols[0].Type != "int" {
		t.Errorf("mysql parse %+v", cols)
	}
	if cols, err := ParseColumns(SQLResult("mysql", nil, false)); err != nil || len(cols) != 0 {
		t.Errorf("empty %v %v", cols, err)
	}
}
