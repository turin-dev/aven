package deploy

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestDumpToolsNeverPutSecretsInScript(t *testing.T) {
	cases := []struct {
		kind string
		vars map[string]string
		ext  string
		want []string
	}{
		{"postgres", map[string]string{"POSTGRES_USER": "u", "POSTGRES_PASSWORD": "pw$ecret", "POSTGRES_DB": "d"}, "dump", []string{"pg_dump -Fc"}},
		{"mysql", map[string]string{"MYSQL_ROOT_PASSWORD": "pw$ecret"}, "sql.gz", []string{"--single-transaction", "--databases"}},
		{"mariadb", map[string]string{"MARIADB_ROOT_PASSWORD": "pw$ecret"}, "sql.gz", []string{"mariadb-dump"}},
		{"redis", map[string]string{"REDIS_PASSWORD": "pw$ecret"}, "rdb", []string{"redis-cli", "--rdb"}},
		{"mongo", map[string]string{"MONGOUSER": "m", "MONGOPASSWORD": "pw$ecret"}, "archive.gz", []string{"mongodump", "--archive", "--gzip"}},
	}
	for _, c := range cases {
		tool, err := dumpTool(c.kind, "svc.production.keel.internal", 0, c.vars)
		if err != nil {
			t.Fatalf("%s: %v", c.kind, err)
		}
		if tool.Ext != c.ext {
			t.Errorf("%s: ext %s", c.kind, tool.Ext)
		}
		for _, w := range c.want {
			if !strings.Contains(tool.Script, w) {
				t.Errorf("%s: script lacks %q:\n%s", c.kind, w, tool.Script)
			}
		}
		if strings.Contains(tool.Script, "pw$ecret") || strings.Contains(tool.Script, "svc.production") {
			t.Errorf("%s: secret or host baked into script", c.kind)
		}
		found := false
		for _, v := range tool.Env {
			if strings.Contains(v, "pw$ecret") || strings.Contains(v, "pw%24ecret") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: password not passed via env: %v", c.kind, tool.Env)
		}
		if tool.Env["DBHOST"] != "svc.production.keel.internal" || tool.Env["DBPORT"] != fmtPort(c.kind) {
			t.Errorf("%s: host/port env %v", c.kind, tool.Env)
		}
	}
	if _, err := dumpTool("sqlite", "h", 0, nil); err == nil {
		t.Error("unsupported kind must fail")
	}
}

func fmtPort(kind string) string {
	return map[string]string{"postgres": "5432", "mysql": "3306", "mariadb": "3306", "redis": "6379", "mongo": "27017"}[kind]
}

func TestRestoreTools(t *testing.T) {
	pg, _ := restoreTool("postgres", "h", 0, map[string]string{"POSTGRES_PASSWORD": "x"})
	if !strings.Contains(pg.Script, "pg_restore --clean --if-exists") {
		t.Error(pg.Script)
	}
	my, _ := restoreTool("mysql", "h", 0, map[string]string{"MYSQL_ROOT_PASSWORD": "x"})
	if !my.Gzip || strings.Contains(my.Script, "mysqldump") && !strings.Contains(my.Script, "DUMP=") {
		t.Error(my.Script)
	}
	rd, _ := restoreTool("redis", "h", 0, map[string]string{"REDIS_PASSWORD": "x"})
	if !rd.DataDir || !strings.Contains(rd.Script, "dump.rdb") || !strings.Contains(rd.Script, "appendonly yes") {
		t.Error(rd.Script)
	}
	mg, _ := restoreTool("mongo", "h", 0, map[string]string{"MONGOUSER": "u"})
	if mg.Gzip || !strings.Contains(mg.Script, "mongorestore") || !strings.Contains(mg.Script, "--drop") {
		t.Error(mg.Script)
	}
	if mg.Env["MONGO_URI"] == "" || strings.Contains(mg.Script, "mongodb://") {
		t.Error("mongo URI must come from env")
	}
	if !SameDBFamily("mysql", "mariadb") || SameDBFamily("postgres", "mysql") {
		t.Error("family check")
	}
}

func TestMongoURIEscapesPassword(t *testing.T) {
	u, err := mongoURI("h", 27017, map[string]string{"MONGOUSER": "a b", "MONGOPASSWORD": "p@ss/w:rd"})
	if err != nil || strings.Contains(u, "p@ss/w:rd") || !strings.Contains(u, "authSource=admin") {
		t.Errorf("%s %v", u, err)
	}
	if _, err := mongoURI("h", 1, nil); err == nil {
		t.Error("missing user must fail")
	}
}

func TestLocalStorage(t *testing.T) {
	l := &localStorage{root: t.TempDir()}
	ctx := context.Background()
	n, err := l.Put(ctx, "svc-prod/a.dump", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatal(n, err)
	}
	rc, err := l.Get(ctx, "svc-prod/a.dump")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(b, []byte("hello")) {
		t.Error("content")
	}
	if _, err := l.Put(ctx, "../../etc/evil", strings.NewReader("x")); err == nil {
		// path.Clean("/"+key) 이므로 root 안으로 정규화되어야 한다
		if _, err := l.Get(ctx, "etc/evil"); err != nil {
			t.Error("traversal key must be confined to root")
		}
	}
	// 실패한 Put은 파일을 남기지 않는다
	pr, pw := io.Pipe()
	go func() { pw.Write([]byte("par")); pw.CloseWithError(io.ErrClosedPipe) }()
	if _, err := l.Put(ctx, "svc-prod/b.dump", pr); err == nil {
		t.Fatal("want error")
	}
	if _, err := l.Get(ctx, "svc-prod/b.dump"); err == nil {
		t.Error("failed upload left a file")
	}
	if err := l.Delete(ctx, "svc-prod/a.dump"); err != nil {
		t.Error(err)
	}
	if err := l.Delete(ctx, "svc-prod/a.dump"); err != nil {
		t.Error("delete should be idempotent")
	}
}
