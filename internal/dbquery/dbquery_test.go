package dbquery

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseCSVNullsAndQuotes(t *testing.T) {
	out := "id,name,note\n1,\"a,b\",\x01NULL\x01\n2,\"\",\"line1\nline2 \"\"q\"\"\"\n3,plain,\r\n"
	cols, rows, err := ParseCSV([]byte(out), NullMarker, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cols, []string{"id", "name", "note"}) {
		t.Fatalf("cols %v", cols)
	}
	want := [][]any{{"1", "a,b", nil}, {"2", "", "line1\nline2 \"q\""}, {"3", "plain", ""}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows %#v", rows)
	}
	// 열 수가 다르면(결과 여러 개) 오류
	if _, _, err := ParseCSV([]byte("a,b\n1,2\nx\n1\n"), NullMarker, false); err == nil {
		t.Error("ragged csv accepted")
	}
	// 잘린 출력: 마지막 미완성 줄은 버린다
	_, rows, err = ParseCSV([]byte("a,b\n1,2\n3,\"unfinished"), NullMarker, true)
	if err != nil || len(rows) != 1 {
		t.Errorf("truncated: %v %v", rows, err)
	}
	if _, _, err := ParseCSV([]byte("a\n\"open"), NullMarker, false); err == nil {
		t.Error("unterminated quote accepted")
	}
}

func TestParseTSV(t *testing.T) {
	out := "id\tbody\tdeleted_at\n1\ttab\\there\\nnext \\\\ end\tNULL\n2\t\t2024-01-01\n"
	cols, rows, err := ParseTSV([]byte(out), false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cols) != 3 || cols[2] != "deleted_at" {
		t.Fatalf("cols %v", cols)
	}
	want := [][]any{{"1", "tab\there\nnext \\ end", nil}, {"2", "", "2024-01-01"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows %#v", rows)
	}
	_, rows, err = ParseTSV([]byte("a\n1\n2\n3partial"), true)
	if err != nil || len(rows) != 2 {
		t.Errorf("truncated: %v %v", rows, err)
	}
	if _, _, err := ParseTSV([]byte("a\tb\n1\n"), false); err == nil {
		t.Error("ragged tsv accepted")
	}
}

func TestSQLResultFallsBackToText(t *testing.T) {
	if r := SQLResult("postgres", []byte(""), false); r.Kind != "text" {
		t.Errorf("empty: %+v", r)
	}
	r := SQLResult("postgres", []byte("a\n1\nb,c\n2,3\n"), false)
	if r.Kind != "text" || !strings.Contains(r.Text, "b,c") {
		t.Errorf("multi result: %+v", r)
	}
	var b strings.Builder
	b.WriteString("n\n")
	for i := 0; i < MaxRows+5; i++ {
		b.WriteString("1\n")
	}
	r = SQLResult("mysql", []byte(b.String()), false)
	if r.Kind != "table" || len(r.Rows) != MaxRows || !r.Truncated {
		t.Errorf("row cap: %d %v", len(r.Rows), r.Truncated)
	}
}

func TestMongoResult(t *testing.T) {
	out := `[{"_id":{"$oid":"65f0"},"name":"a","n":12345678901234567890},{"_id":{"$oid":"65f1"},"tags":["x"]}]`
	r := MongoResult([]byte(out), false)
	if r.Kind != "table" || r.Columns[0] != "_id" || !reflect.DeepEqual(r.Columns, []string{"_id", "n", "name", "tags"}) {
		t.Fatalf("cols %+v", r)
	}
	if r.Rows[1][2] != nil || r.Rows[0][1].(json.Number).String() != "12345678901234567890" {
		t.Errorf("rows %#v", r.Rows)
	}
	if r := MongoResult([]byte(`{"ok":1}`), false); r.Kind != "text" || !strings.Contains(r.Text, `"ok": 1`) {
		t.Errorf("scalar: %+v", r)
	}
	if r := MongoResult([]byte("not json"), false); r.Kind != "text" || r.Text != "not json" {
		t.Errorf("garbage: %+v", r)
	}
}

func TestParseRedis(t *testing.T) {
	out := strings.Join([]string{
		`1) "17"`,
		`2)  1) "user:1"`,
		`    2) "a\"b\\c\n\xff"`,
		`    3) "k3"`,
		`    4) "k4"`,
		`    5) "k5"`,
		`    6) "k6"`,
		`    7) "k7"`,
		`    8) "k8"`,
		`    9) "k9"`,
		`   10) "k10"`,
		`string`,
		`(integer) -1`,
		`(nil)`,
		`(error) WRONGTYPE Operation against a key holding the wrong kind of value`,
		`(empty array)`,
		`1) 1) "1-0"`,
		`   2) 1) "f"`,
		`      2) "v"`,
		`2) 1) "2-0"`,
		`   2) (empty array)`,
		`1) "next"`,
		``,
	}, "\n")
	reps, err := ParseRedis(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(reps) != 8 {
		t.Fatalf("got %d replies: %+v", len(reps), reps)
	}
	scan := reps[0]
	if scan.Type != "array" || scan.Elems[0].Str != "17" || len(scan.Elems[1].Elems) != 10 {
		t.Fatalf("scan: %+v", scan)
	}
	if got := scan.Elems[1].Elems[1].Str; got != "a\"b\\c\n\xff" {
		t.Errorf("unescape: %q", got)
	}
	if scan.Elems[1].Elems[9].Str != "k10" {
		t.Errorf("wide index: %+v", scan.Elems[1].Elems[9])
	}
	if reps[1].Type != "status" || reps[1].Str != "string" || reps[2].Int != -1 || reps[3].Type != "nil" || reps[4].Type != "error" {
		t.Errorf("scalars: %+v", reps[1:5])
	}
	if reps[5].Type != "array" || len(reps[5].Elems) != 0 {
		t.Errorf("empty: %+v", reps[5])
	}
	st := reps[6]
	if len(st.Elems) != 2 || st.Elems[0].Elems[1].Elems[1].Str != "v" || len(st.Elems[1].Elems[1].Elems) != 0 {
		t.Errorf("stream: %+v", st)
	}
	if reps[7].Type != "array" || reps[7].Elems[0].Str != "next" {
		t.Errorf("trailing: %+v", reps[7])
	}
}

func TestQuoteRedisArgRoundTrip(t *testing.T) {
	for _, s := range []string{"plain", "a b", `q"uote`, "back\\slash", "nl\nret\r", "\x00\xff\x7f", "ünï"} {
		q := QuoteRedisArg(s)
		if strings.ContainsAny(q[1:len(q)-1], "\n\r\x00 ") && s != "a b" {
			t.Errorf("%q: unsafe quoting %q", s, q)
		}
		// redis-cli가 찍는 repr과 같은 이스케이프 규칙이므로 unrepr로 되돌릴 수 있다.
		back, err := unrepr(q[1 : len(q)-1])
		if err != nil || back != s {
			t.Errorf("%q → %q → %q (%v)", s, q, back, err)
		}
	}
	if got := RedisLine("GET", "a b"); got != "\"GET\" \"a b\"\n" {
		t.Errorf("line %q", got)
	}
}

func TestParseKeyspace(t *testing.T) {
	ks := ParseKeyspace("# Keyspace\r\ndb0:keys=12,expires=3,avg_ttl=0\r\ndb5:keys=1,expires=0,avg_ttl=0\r\n")
	if len(ks) != 2 || ks[0].Keys != 12 || ks[0].Expires != 3 || ks[1].DB != 5 {
		t.Errorf("keyspace %+v", ks)
	}
}

func TestQuoteIdentAndRowsQuery(t *testing.T) {
	cases := map[[2]string]string{
		{"postgres", `users`}:        `"users"`,
		{"postgres", `we"ird`}:       `"we""ird"`,
		{"mysql", "a`b"}:             "`a``b`",
		{"mariadb", `x"; DROP --`}:   "`x\"; DROP --`",
		{"postgres", `"; DROP x;--`}: `"""; DROP x;--"`,
	}
	for in, want := range cases {
		got, err := QuoteIdent(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("%v: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "a\x00b", "new\nline", strings.Repeat("x", 129)} {
		if _, err := QuoteIdent("postgres", bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if _, err := QuoteIdent("redis", "x"); err == nil {
		t.Error("redis ident accepted")
	}
	q, err := RowsQuery("postgres", "public", `o"rders`, 50, 100)
	if err != nil || q != "SELECT * FROM \"public\".\"o\"\"rders\" LIMIT 51 OFFSET 100;\n" {
		t.Errorf("rows query %q %v", q, err)
	}
	q, _ = RowsQuery("mysql", "", "t", 1, 0)
	if q != "SELECT * FROM `t` LIMIT 2 OFFSET 0;\n" {
		t.Errorf("mysql rows query %q", q)
	}
	for _, lo := range [][2]int{{0, 0}, {501, 0}, {10, -1}} {
		if _, err := RowsQuery("postgres", "", "t", lo[0], lo[1]); err == nil {
			t.Errorf("limit/offset %v accepted", lo)
		}
	}
}

func TestMongoFindEvalEscapes(t *testing.T) {
	e, err := MongoFindEval("app", `x"); db.dropDatabase(); ("`, 10, 5)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e, `getCollection("x\"); db.dropDatabase(); (\"")`) || !strings.Contains(e, "skip(5).limit(11)") {
		t.Errorf("eval %s", e)
	}
	if _, err := MongoFindEval("a.b", "c", 1, 0); err == nil {
		t.Error("bad db accepted")
	}
	for _, bad := range []string{"", "system.users", "a$b", "a\x00"} {
		if _, err := MongoFindEval("app", bad, 1, 0); err == nil {
			t.Errorf("collection %q accepted", bad)
		}
	}
}

func TestCommandsKeepSecretsOutOfArgv(t *testing.T) {
	const pw = "s3cr3t-pass"
	targets := []Target{
		{Kind: "postgres", Vars: map[string]string{"PGUSER": "app", "PGPASSWORD": pw, "PGDATABASE": "aven"}},
		{Kind: "mysql", Vars: map[string]string{"MYSQLUSER": "root", "MYSQLPASSWORD": pw}},
		{Kind: "mongo", Vars: map[string]string{"MONGOUSER": "mongo", "MONGOPASSWORD": pw}},
		{Kind: "redis", Vars: map[string]string{"REDISPASSWORD": pw}},
	}
	for _, tg := range targets {
		var c Command
		var err error
		switch tg.Kind {
		case "mongo":
			c, err = tg.Mongo("db.x.find()")
		case "redis":
			c, err = tg.Redis(0, "PING")
		default:
			c, err = tg.SQL("SELECT 1")
		}
		if err != nil {
			t.Fatal(tg.Kind, err)
		}
		if strings.Contains(strings.Join(c.Cmd, " "), pw) {
			t.Errorf("%s: password in argv", tg.Kind)
		}
		if !strings.Contains(strings.Join(c.Env, "\n"), pw) {
			t.Errorf("%s: password not in env: %v", tg.Kind, c.Env)
		}
		if len(c.Stdin) == 0 {
			t.Errorf("%s: query not on stdin", tg.Kind)
		}
	}
	if _, err := (Target{Kind: "postgres"}).SQL(" "); err == nil {
		t.Error("empty query accepted")
	}
	if _, err := (Target{Kind: "postgres"}).SQL(strings.Repeat("x", MaxQuery+1)); err == nil {
		t.Error("huge query accepted")
	}
	c, _ := (Target{Kind: "postgres", Port: 6543}).SQL("SELECT 1")
	if !strings.Contains(strings.Join(c.Env, " "), "PGPORT=6543") || !strings.Contains(strings.Join(c.Env, " "), "PGUSER=postgres") {
		t.Errorf("pg env %v", c.Env)
	}
}

// 실제 psql/mysql은 문장마다 헤더를 찍는다: "SELECT 1 AS x; SELECT 2 AS x"가 [1, x, 2] 한 표로 읽히면 안 된다.
func TestSQLResultMultipleResultSets(t *testing.T) {
	if r := SQLResult("postgres", []byte("x\n1\nx\n2\n"), false); r.Kind != "text" || !strings.Contains(r.Text, "2") {
		t.Errorf("pg multi: %+v", r)
	}
	if r := SQLResult("mysql", []byte("x\n1\nx\n2\n"), false); r.Kind != "text" {
		t.Errorf("mysql multi: %+v", r)
	}
	// 헤더와 다른 행은 그대로 표다(따옴표 여부까지 같아야 헤더다)
	if r := SQLResult("postgres", []byte("x\n\"x \"\n1\n"), false); r.Kind != "table" || len(r.Rows) != 2 {
		t.Errorf("pg single: %+v", r)
	}
}

func TestMongoCursorEval(t *testing.T) {
	// 줄 끝 주석이 닫는 괄호를 먹지 않게 줄을 바꾼다
	if e, ok := MongoCursorEval("  db.c.find({a: 1}) // 주석\n"); !ok || e != "(db.c.find({a: 1}) // 주석\n).toArray()" {
		t.Errorf("%q %v", e, ok)
	}
	if e, ok := MongoCursorEval("db.c.aggregate([])  ;; "); !ok || e != "(db.c.aggregate([])\n).toArray()" {
		t.Errorf("%q %v", e, ok)
	}
	if _, ok := MongoCursorEval(" ; "); ok {
		t.Error("empty must not be wrapped")
	}
	if !IsMongoCursorErr(`{"message": "Converting circular structure to EJSON:\n (root) -> _mongo"}`) {
		t.Error("cursor error not detected")
	}
}

func TestMongoErrorMessage(t *testing.T) {
	full := `{"message": "Unexpected token (1:15)", "code": "BABEL_PARSE_ERROR", "stack": "SyntaxError: ..."}`
	if m := MongoErrorMessage(full); m != "Unexpected token (1:15)" {
		t.Errorf("full: %q", m)
	}
	if m := MongoErrorMessage(`{"message": "bad auth", "codeName": "AuthenticationFailed"}`); m != "bad auth (AuthenticationFailed)" {
		t.Errorf("codeName: %q", m)
	}
	cut := "{\n  \"message\": \"Converting circular structure\",\n  \"stack\": \"BSONError: very long…"
	if m := MongoErrorMessage(cut); m != "Converting circular structure" {
		t.Errorf("truncated: %q", m)
	}
	if m := MongoErrorMessage("MongoServerError: plain text"); m != "MongoServerError: plain text" {
		t.Errorf("plain: %q", m)
	}
}
