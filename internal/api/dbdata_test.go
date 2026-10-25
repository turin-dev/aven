package api

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/dbquery"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// fakeRunner는 일회성 exec 가짜다. reply(stdin)이 돌려줄 출력을 정한다.
type fakeRunner struct {
	mu    sync.Mutex
	reqs  []node.ExecRunRequest
	nodes []string
	conts []string
	reply func(stdin string) *node.ExecOutput
	fail  func(req node.ExecRunRequest) error // nil이 아니면 그 오류로 실패한다(시간 초과 흉내)
}

func (f *fakeRunner) ExecRun(_ context.Context, nodeID, container string, req node.ExecRunRequest, timeout time.Duration) (*node.ExecOutput, error) {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.nodes = append(f.nodes, nodeID)
	f.conts = append(f.conts, container)
	f.mu.Unlock()
	if (timeout != dbquery.TimeoutS*time.Second && timeout != dbquery.KillTimeoutS*time.Second) || req.MaxOutput != dbquery.MaxOutput {
		return nil, context.DeadlineExceeded
	}
	if f.fail != nil {
		if err := f.fail(req); err != nil {
			return nil, err
		}
	}
	return f.reply(string(req.Stdin)), nil
}

func (f *fakeRunner) last() node.ExecRunRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reqs[len(f.reqs)-1]
}

type dataFixture struct {
	*ghFixture
	run *fakeRunner
	svc map[string]*store.Service
}

func newDataFixture(t *testing.T) *dataFixture {
	f := newGHFixture(t)
	run := &fakeRunner{}
	f.srv.dataExec = run
	df := &dataFixture{ghFixture: f, run: run, svc: map[string]*store.Service{}}
	for _, kind := range []string{"postgres", "mysql", "redis", "mongo"} {
		k := kind
		sv := &store.Service{ProjectID: f.p.ID, Name: kind + "-db", SourceType: "database", DatabaseKind: &k}
		if err := f.st.CreateService(f.ctx, sv); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: sv.ID, EnvironmentID: f.prod.ID}
		f.st.CreateInstance(f.ctx, in)
		d := &store.Deployment{ServiceID: sv.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
			NodeID: "nod_remote1", Containers: []string{"keel-" + kind + "-1"},
			Env: map[string]string{"PGUSER": "app", "PGPASSWORD": "pg-secret", "MYSQLPASSWORD": "my-secret", "REDISPASSWORD": "r-secret",
				"MONGOUSER": "mongo", "MONGOPASSWORD": "m-secret"}}
		if err := f.st.CreateDeployment(f.ctx, d); err != nil {
			t.Fatal(err)
		}
		f.st.SetInstanceStatus(f.ctx, in.ID, "online", &d.ID)
		df.svc[kind] = sv
	}
	return df
}

func pvFor(df *dataFixture, kind string, extra map[string]string) map[string]string {
	pv := map[string]string{"sid": df.svc[kind].ID, "eid": df.prod.ID}
	for k, v := range extra {
		pv[k] = v
	}
	return pv
}

func TestDataTablesPostgres(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(string) *node.ExecOutput {
		return &node.ExecOutput{Stdout: []byte("schema,name,kind,rows\npublic,users,table,42\npublic,v_active,view,\x01NULL\x01\n")}
	}
	code, out := df.call(df.srv.dataTables, "GET", "/x", "", pvFor(df, "postgres", nil))
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	b, _ := json.Marshal(out["tables"])
	if string(b) != `[{"kind":"table","name":"users","rows":42,"schema":"public"},{"kind":"view","name":"v_active","rows":null,"schema":"public"}]` {
		t.Errorf("tables %s", b)
	}
	req := df.run.last()
	if strings.Contains(strings.Join(req.Cmd, " "), "pg-secret") || !strings.Contains(strings.Join(req.Env, " "), "PGPASSWORD=pg-secret") {
		t.Errorf("secret handling: cmd=%v", req.Cmd)
	}
	if df.run.nodes[0] != "nod_remote1" || df.run.conts[0] != "keel-postgres-1" {
		t.Errorf("target %v %v", df.run.nodes, df.run.conts)
	}
}

func TestDataTableRowsQuotingAndPaging(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(stdin string) *node.ExecOutput {
		if strings.Contains(stdin, "pg_attribute") {
			return &node.ExecOutput{Stdout: []byte("name,type,pk\nid,integer,t\nname,text,f\n")}
		}
		return &node.ExecOutput{Stdout: []byte("id,name\n1,a\n2,\x01NULL\x01\n3,c\n")}
	}
	pv := pvFor(df, "postgres", map[string]string{"name": `us"ers`})
	code, out := df.call(df.srv.dataTableRows, "GET", "/x?schema=app&limit=2&offset=4", "", pv)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if got := string(df.run.last().Stdin); got != "SELECT * FROM \"app\".\"us\"\"ers\" LIMIT 3 OFFSET 4;\n" {
		t.Errorf("query %q", got)
	}
	rows := out["rows"].([]any)
	if len(rows) != 2 || out["has_more"] != true || rows[1].([]any)[1] != nil {
		t.Errorf("rows %v has_more %v", rows, out["has_more"])
	}
	for _, q := range []string{"/x?limit=0", "/x?limit=501", "/x?offset=-1"} {
		if code, _ := df.call(df.srv.dataTableRows, "GET", q, "", pv); code != 400 {
			t.Errorf("%s: %d", q, code)
		}
	}
	if code, _ := df.call(df.srv.dataTableRows, "GET", "/x", "", pvFor(df, "postgres", map[string]string{"name": "bad\nname"})); code != 400 {
		t.Errorf("control char name: %d", code)
	}
	// mysql: backtick 인용
	if b, _ := json.Marshal([]any{out["column_types"], out["primary_key"], out["editable"]}); string(b) != `[["integer","text"],["id"],true]` {
		t.Errorf("column types %s", b)
	}
	df.run.reply = func(stdin string) *node.ExecOutput {
		if strings.Contains(stdin, "information_schema.COLUMNS") {
			return &node.ExecOutput{Stdout: []byte("name\ttype\tpk\nid\tbigint unsigned\t0\n")}
		}
		return &node.ExecOutput{Stdout: []byte("id\n1\n")}
	}
	code, out = df.call(df.srv.dataTableRows, "GET", "/x?schema=shop", "", pvFor(df, "mysql", map[string]string{"name": "or`ders"}))
	if code != 200 || !strings.HasSuffix(string(df.run.last().Stdin), " */\nSELECT * FROM `shop`.`or``ders` LIMIT 51 OFFSET 0;\n") {
		t.Errorf("mysql: %d %q", code, df.run.last().Stdin)
	}
	if b, _ := json.Marshal([]any{out["column_types"], out["primary_key"], out["editable"]}); string(b) != `[["bigint unsigned"],[],false]` {
		t.Errorf("mysql column types %s", b)
	}
	// mongo: JSON 문자열로 넣는다
	df.run.reply = func(string) *node.ExecOutput { return &node.ExecOutput{Stdout: []byte(`[{"_id":1,"a":"x"}]`)} }
	code, out = df.call(df.srv.dataTableRows, "GET", "/x?schema=app", "", pvFor(df, "mongo", map[string]string{"name": `c"); evil(); ("`}))
	if code != 200 || !strings.Contains(string(df.run.last().Stdin), `getCollection("c\"); evil(); (\"")`) {
		t.Errorf("mongo: %d %q %v", code, df.run.last().Stdin, out)
	}
	if code, _ := df.call(df.srv.dataTableRows, "GET", "/x", "", pvFor(df, "redis", map[string]string{"name": "db0"})); code != 400 {
		t.Errorf("redis rows: %d", code)
	}
}

func TestDataQueryAuditAndErrors(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(stdin string) *node.ExecOutput {
		if strings.Contains(stdin, "boom") {
			return &node.ExecOutput{Stderr: []byte("psql:<stdin>:1: ERROR:  syntax error at or near \"boom\""), ExitCode: 3}
		}
		return &node.ExecOutput{Stdout: []byte("n\n7\n")}
	}
	pv := pvFor(df, "postgres", nil)
	long := "SELECT 7 AS n -- " + strings.Repeat("x", 300)
	code, out := df.call(df.srv.dataQuery, "POST", "/x", `{"query":`+jsonStr(long)+`}`, pv)
	if code != 200 || out["kind"] != "table" || out["row_count"] != float64(1) {
		t.Fatalf("%d %v", code, out)
	}
	code, out = df.call(df.srv.dataQuery, "POST", "/x", `{"query":"boom"}`, pv)
	if code != 400 || !strings.Contains(out["error"].(string), "syntax error") {
		t.Errorf("error: %d %v", code, out)
	}
	if code, _ := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"  "}`, pv); code != 400 {
		t.Errorf("empty: %d", code)
	}
	evs, _ := df.st.ListEvents(df.ctx, df.p.ID, "", 20)
	n := 0
	for _, ev := range evs {
		if ev.Kind == "database.query" {
			n++
			if strings.Contains(ev.Message, strings.Repeat("x", 250)) || strings.Contains(ev.Message, "7\n") {
				t.Errorf("audit too long or contains results: %s", ev.Message)
			}
		}
	}
	if n != 2 {
		t.Errorf("database.query events: %d", n)
	}
	// 데이터베이스가 아닌 서비스, 실행 중이 아닌 DB
	if code, _ := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"x"}`, map[string]string{"sid": df.web.ID, "eid": df.prod.ID}); code != 400 {
		t.Errorf("non-db service: %d", code)
	}
	k := "postgres"
	sv := &store.Service{ProjectID: df.p.ID, Name: "idle-db", SourceType: "database", DatabaseKind: &k}
	df.st.CreateService(df.ctx, sv)
	df.st.CreateInstance(df.ctx, &store.Instance{ServiceID: sv.ID, EnvironmentID: df.prod.ID})
	if code, _ := df.call(df.srv.dataTables, "GET", "/x", "", map[string]string{"sid": sv.ID, "eid": df.prod.ID}); code != 409 {
		t.Errorf("not running: %d", code)
	}
}

func jsonStr(s string) string { b, _ := json.Marshal(s); return string(b) }

func TestDataRedisKeys(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(stdin string) *node.ExecOutput {
		switch {
		case strings.HasPrefix(stdin, `"SCAN"`):
			return &node.ExecOutput{Stdout: []byte("1) \"42\"\n2) 1) \"user:1\"\n   2) \"q\\\"k\"\n")}
		case strings.HasPrefix(stdin, `"TYPE" "user:1"`) && strings.Contains(stdin, `"TYPE" "q\"k"`):
			return &node.ExecOutput{Stdout: []byte("hash\n(integer) -1\nstring\n(integer) 300\n")}
		case strings.HasPrefix(stdin, `"TYPE" "user:1"`):
			return &node.ExecOutput{Stdout: []byte("hash\n(integer) -1\n")}
		case strings.HasPrefix(stdin, `"HLEN"`):
			return &node.ExecOutput{Stdout: []byte("(integer) 2\n1) \"0\"\n2) 1) \"name\"\n   2) \"ann\"\n   3) \"age\"\n   4) \"30\"\n")}
		case strings.HasPrefix(stdin, `"TYPE" "missing"`):
			return &node.ExecOutput{Stdout: []byte("none\n(integer) -2\n")}
		case strings.HasPrefix(stdin, `"INFO"`):
			return &node.ExecOutput{Stdout: []byte("# Keyspace\r\ndb0:keys=2,expires=1,avg_ttl=5\r\n")}
		}
		return &node.ExecOutput{Stdout: []byte("(error) ERR unknown command 'FOO'\n")}
	}
	pv := pvFor(df, "redis", nil)
	code, out := df.call(df.srv.dataKeys, "GET", "/x?pattern=user:*&cursor=0", "", pv)
	if code != 200 || out["cursor"] != "42" {
		t.Fatalf("%d %v", code, out)
	}
	b, _ := json.Marshal(out["keys"])
	if string(b) != `[{"key":"user:1","ttl":-1,"type":"hash"},{"key":"q\"k","ttl":300,"type":"string"}]` {
		t.Errorf("keys %s", b)
	}
	if !strings.Contains(strings.Join(df.run.reqs[0].Env, " "), "REDISCLI_AUTH=r-secret") {
		t.Errorf("auth env: %v", df.run.reqs[0].Env)
	}
	if code, _ := df.call(df.srv.dataKeys, "GET", "/x?cursor=abc", "", pv); code != 400 {
		t.Errorf("bad cursor: %d", code)
	}
	code, out = df.call(df.srv.dataKey, "GET", "/x", "", pvFor(df, "redis", map[string]string{"key": "user:1"}))
	if code != 200 || out["type"] != "hash" || out["length"] != float64(2) {
		t.Fatalf("key: %d %v", code, out)
	}
	b, _ = json.Marshal(out["value"])
	if string(b) != `[{"field":"name","value":"ann"},{"field":"age","value":"30"}]` {
		t.Errorf("value %s", b)
	}
	if code, _ := df.call(df.srv.dataKey, "GET", "/x", "", pvFor(df, "redis", map[string]string{"key": "missing"})); code != 404 {
		t.Errorf("missing key: %d", code)
	}
	code, out = df.call(df.srv.dataTables, "GET", "/x", "", pv)
	b, _ = json.Marshal(out["tables"])
	if code != 200 || string(b) != `[{"kind":"keyspace","name":"db0","rows":2,"schema":""}]` {
		t.Errorf("keyspace: %d %s", code, b)
	}
	// redis query는 글자 결과
	code, out = df.call(df.srv.dataQuery, "POST", "/x", `{"query":"FOO"}`, pv)
	if code != 200 || out["kind"] != "text" || !strings.Contains(out["text"].(string), "unknown command") {
		t.Errorf("redis query: %d %v", code, out)
	}
	if code, _ := df.call(df.srv.dataKeys, "GET", "/x", "", pvFor(df, "postgres", nil)); code != 400 {
		t.Errorf("keys on postgres: %d", code)
	}
}

func TestDataMongoTables(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(string) *node.ExecOutput {
		return &node.ExecOutput{Stdout: []byte(`[{"kind":"collection","name":"users","rows":3,"schema":"app"},{"kind":"view","name":"v","rows":null,"schema":"app"}]`)}
	}
	code, out := df.call(df.srv.dataTables, "GET", "/x", "", pvFor(df, "mongo", nil))
	b, _ := json.Marshal(out["tables"])
	if code != 200 || string(b) != `[{"kind":"collection","name":"users","rows":3,"schema":"app"},{"kind":"view","name":"v","rows":null,"schema":"app"}]` {
		t.Errorf("%d %s", code, b)
	}
	req := df.run.last()
	if strings.Contains(strings.Join(req.Cmd, " "), "m-secret") || !strings.Contains(strings.Join(req.Env, " "), "m-secret") {
		t.Errorf("mongo secret handling: %v", req.Cmd)
	}
}

// 실제 mongosh 2.x --json은 find()/aggregate() 커서를 직렬화하지 못하고 오류를 낸다: .toArray()로 한 번 더 실행한다.
func TestDataMongoCursorRetry(t *testing.T) {
	df := newDataFixture(t)
	circ := `{"message": "Converting circular structure to EJSON:\n    (root) -> _mongo -> __serviceProvider", "stack": "BSONError: ..."}`
	df.run.reply = func(stdin string) *node.ExecOutput {
		switch {
		case strings.HasSuffix(stdin, "\n).toArray()") && strings.Contains(stdin, ";"):
			return &node.ExecOutput{Stderr: []byte(`{"message": "Unexpected token (1:20)", "stack": "SyntaxError"}`), ExitCode: 1}
		case strings.HasSuffix(stdin, "\n).toArray()"):
			return &node.ExecOutput{Stdout: []byte(`[{"_id":1,"a":"x"}]`)}
		}
		return &node.ExecOutput{Stderr: []byte(circ), ExitCode: 1}
	}
	pv := pvFor(df, "mongo", nil)
	code, out := df.call(df.srv.dataQuery, "POST", "/x", `{"query":"db.c.find({a:1});"}`, pv)
	if code != 200 || out["kind"] != "table" || out["row_count"] != float64(1) {
		t.Fatalf("retry: %d %v", code, out)
	}
	if got := string(df.run.last().Stdin); got != "(db.c.find({a:1})\n).toArray()" {
		t.Errorf("retry eval %q", got)
	}
	// 여러 문장이면 다시 실행할 수 없다: 안내 오류
	code, out = df.call(df.srv.dataQuery, "POST", "/x", `{"query":"x = 1; db.c.find()"}`, pv)
	if code != 400 || !strings.Contains(out["error"].(string), ".toArray()") {
		t.Errorf("multi: %d %v", code, out)
	}
	// 오류 객체는 message만 보여 준다(stack 제외)
	df.run.reply = func(string) *node.ExecOutput {
		return &node.ExecOutput{Stderr: []byte(`{"message": "Unexpected token (1:15)", "code": "BABEL_PARSE_ERROR", "stack": "SyntaxError: at Parser.raise"}`), ExitCode: 1}
	}
	code, out = df.call(df.srv.dataQuery, "POST", "/x", `{"query":"db.c.find({ bad"}`, pv)
	if code != 400 || out["error"] != "Unexpected token (1:15)" {
		t.Errorf("mongo error: %d %v", code, out)
	}
}

// mysql --batch는 빈 결과에 헤더도 찍지 않는다: 빈 표 탐색이 502가 되면 안 된다.
func TestDataTableRowsEmptyMySQL(t *testing.T) {
	df := newDataFixture(t)
	df.run.reply = func(string) *node.ExecOutput { return &node.ExecOutput{} }
	code, out := df.call(df.srv.dataTableRows, "GET", "/x", "", pvFor(df, "mysql", map[string]string{"name": "empty"}))
	if code != 200 || len(out["rows"].([]any)) != 0 || out["has_more"] != false {
		t.Errorf("%d %v", code, out)
	}
}
