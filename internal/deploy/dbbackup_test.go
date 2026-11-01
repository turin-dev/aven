package deploy

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// 덤프가 0이 아닌 코드로 끝나면 백업은 실패이고 저장소에 반쪽 파일이 남지 않는다.
func TestRunDumpFailsOnExitCodeAndLeavesNoFile(t *testing.T) {
	for _, kind := range []string{"postgres", "mysql"} {
		d, m, dt := remoteDBFixture(t, kind)
		dt.vars["MYSQL_ROOT_PASSWORD"] = "pw"
		d.out = []byte("partial dump")
		d.code = 2
		root := t.TempDir()
		st := &localStorage{root: root}
		tool, err := dumpTool(kind, dt.host, dt.port, dt.vars)
		if err != nil {
			t.Fatal(err)
		}
		b := &store.DBBackup{ID: "dbb_abcdefgh234", ObjectKey: "db-production/x." + tool.Ext}
		if _, err := m.runDump(context.Background(), dt, tool, st, b); err == nil {
			t.Fatalf("%s: exit code 2 must fail the backup", kind)
		}
		var files []string
		filepath.Walk(root, func(p string, fi os.FileInfo, _ error) error {
			if fi != nil && !fi.IsDir() {
				files = append(files, p)
			}
			return nil
		})
		if len(files) != 0 {
			t.Errorf("%s: failed dump left files %v", kind, files)
		}
	}
}

// shortStorage는 스트림을 끝까지 읽지 않고 성공을 돌려주는 잘못된 저장소다.
type shortStorage struct{ localStorage }

func (s *shortStorage) Put(_ context.Context, _ string, r io.Reader) (int64, error) {
	buf := make([]byte, 4)
	n, err := io.ReadFull(r, buf)
	return int64(n), err
}

func TestRunDumpDetectsUnreadStream(t *testing.T) {
	d, m, dt := remoteDBFixture(t, "postgres")
	d.out = []byte(strings.Repeat("pgdump", 1000))
	tool, _ := dumpTool(dt.kind, dt.host, dt.port, dt.vars)
	b := &store.DBBackup{ID: "dbb_abcdefgh234", ObjectKey: "db-production/x.dump"}
	done := make(chan error, 1)
	go func() {
		_, err := m.runDump(context.Background(), dt, tool, &shortStorage{}, b)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a storage that stopped reading early must not count as a complete backup")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runDump hung when storage stopped reading")
	}
}

func TestRunDumpEmpty(t *testing.T) {
	_, m, dt := remoteDBFixture(t, "postgres")
	tool, _ := dumpTool(dt.kind, dt.host, dt.port, dt.vars)
	b := &store.DBBackup{ID: "dbb_abcdefgh234", ObjectKey: "db-production/x.dump"}
	if _, err := m.runDump(context.Background(), dt, tool, &localStorage{root: t.TempDir()}, b); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("err = %v", err)
	}
}

func TestPruneDBBackupPlan(t *testing.T) {
	mk := func(id, status string) *store.DBBackup { return &store.DBBackup{ID: id, Status: status} }
	// 최신순
	bs := []*store.DBBackup{mk("r", "running"), mk("d1", "done"), mk("f1", "failed"), mk("d2", "done"), mk("d3", "done"), mk("d4", "done")}
	for i := 2; i <= 12; i++ {
		bs = append(bs, mk("f"+string(rune('a'+i)), "failed"))
	}
	ids := func(out []*store.DBBackup) string {
		var s []string
		for _, b := range out {
			s = append(s, b.ID)
		}
		return strings.Join(s, ",")
	}
	if got := ids(pruneDBBackupPlan(bs, 2)); got != "d3,d4,fl,fm" {
		t.Errorf("retention 2: %s", got)
	}
	if got := ids(pruneDBBackupPlan(bs, 0)); got != "fl,fm" {
		t.Errorf("retention 0 keeps every successful backup: %s", got)
	}
	// 복원에 쓰는 중인 백업은 보관 개수를 넘어도 지우지 않는다
	dbJobs.Lock()
	dbJobs.restores["d4"] = &RestoreStatus{Status: "running"}
	dbJobs.Unlock()
	defer func() {
		dbJobs.Lock()
		delete(dbJobs.restores, "d4")
		dbJobs.Unlock()
	}()
	if got := ids(pruneDBBackupPlan(bs, 2)); got != "d3,fl,fm" {
		t.Errorf("in-use backup pruned: %s", got)
	}
	if err := (&Manager{}).DeleteDBBackup(context.Background(), mk("d4", "done")); !errors.Is(err, ErrBackupInUse) {
		t.Errorf("delete of in-use backup: %v", err)
	}
	if err := (&Manager{}).DeleteDBBackup(context.Background(), mk("r", "running")); !errors.Is(err, ErrDBBusy) {
		t.Errorf("delete of running backup: %v", err)
	}
}

func TestCheckRestoreProject(t *testing.T) {
	if checkRestoreProject("prj_a", "prj_a") != nil {
		t.Error("same project must be allowed")
	}
	for _, c := range [][2]string{{"prj_a", "prj_b"}, {"", ""}, {"", "prj_b"}} {
		if !errors.Is(checkRestoreProject(c[0], c[1]), ErrRestoreOtherProject) {
			t.Errorf("%v must be refused", c)
		}
	}
}

// stopDriver는 Stop/Start 호출을 기록하고 failStop 컨테이너의 Stop을 실패시킨다.
type stopDriver struct {
	Driver
	failStop string
	stopped  []string
	started  []string
}

func (d *stopDriver) Stop(_ context.Context, name string, _ int) error {
	if name == d.failStop {
		return errors.New("daemon timeout")
	}
	if name == "gone" {
		return errors.New("Error response from daemon: No such container: gone")
	}
	d.stopped = append(d.stopped, name)
	return nil
}

func (d *stopDriver) Start(_ context.Context, name string) error {
	d.started = append(d.started, name)
	return nil
}

// 복원은 DB 컨테이너를 모두 멈춘 뒤에만 데이터 볼륨에 쓴다: 하나라도 못 멈추면 멈춘 것을 되살리고 멈춘다.
func TestStopForRestore(t *testing.T) {
	m := &Manager{drain: map[string]bool{}}
	d := &stopDriver{failStop: "b"}
	if err := m.stopForRestore(context.Background(), d, []string{"a", "b", "c"}); err == nil {
		t.Fatal("a container that did not stop must abort the restore")
	}
	if strings.Join(d.stopped, ",") != "a" || strings.Join(d.started, ",") != "a,b" {
		t.Errorf("stopped=%v started=%v", d.stopped, d.started)
	}
	if len(m.drain) != 0 {
		t.Errorf("drain marks left: %v", m.drain)
	}
	d = &stopDriver{}
	if err := m.stopForRestore(context.Background(), d, []string{"a", "gone"}); err != nil {
		t.Fatalf("missing container is already stopped: %v", err)
	}
	m.startAfterRestore(context.Background(), d, []string{"a", "gone"})
	if len(m.drain) != 0 || len(d.started) != 2 {
		t.Errorf("drain=%v started=%v", m.drain, d.started)
	}
}

func TestCheckPITRRestoreTarget(t *testing.T) {
	src := &store.Instance{ID: "ins_src", EnvironmentID: "env_a"}
	fork := &store.Instance{ID: "ins_fork", EnvironmentID: "env_a"}
	other := &store.Instance{ID: "ins_other", EnvironmentID: "env_b"}
	cases := []struct {
		name    string
		target  *store.Instance
		inPlace bool
		ok      bool
	}{
		{"fork", fork, false, true},
		{"in place", src, true, true},
		{"source without in_place", src, false, false},
		{"in place to another service", fork, true, false},
		{"other environment", other, false, false},
		{"nil target", nil, false, false},
	}
	for _, c := range cases {
		if err := checkPITRRestoreTarget(src, c.target, c.inPlace); (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

func TestPITRLockWait(t *testing.T) {
	if !pitrLock("ins_lw", "sync") {
		t.Fatal("lock")
	}
	go func() {
		time.Sleep(200 * time.Millisecond)
		pitrUnlock("ins_lw", "sync")
	}()
	if err := pitrLockWait(context.Background(), "ins_lw", "sync", 5*time.Second); err != nil {
		t.Fatalf("wait: %v", err)
	}
	if err := pitrLockWait(context.Background(), "ins_lw", "sync", 100*time.Millisecond); !errors.Is(err, ErrPITRBusy) {
		t.Errorf("held lock: %v", err)
	}
	pitrUnlock("ins_lw", "sync")
}

func TestExactReader(t *testing.T) {
	cases := []struct {
		data string
		want int64
		ok   bool
	}{
		{"hello", 5, true},
		{"hell", 5, false},
		{"hello!", 5, false},
		{"", 1, false},
	}
	for _, c := range cases {
		_, err := io.ReadAll(&exactReader{r: strings.NewReader(c.data), want: c.want})
		if (err == nil) != c.ok {
			t.Errorf("%q/%d: err=%v", c.data, c.want, err)
		}
	}
}

func TestCheckBackupStream(t *testing.T) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(bytes.Repeat([]byte("INSERT INTO t VALUES (1);\n"), 2000))
	zw.Close()
	good := buf.Bytes()
	n := int64(len(good))
	cases := []struct {
		name string
		data []byte
		size int64
		gz   bool
		ok   bool
	}{
		{"gzip ok", good, n, true, true},
		{"gzip unknown size", good, 0, true, true},
		{"gzip truncated", good[:len(good)-10], n - 10, true, false},
		{"gzip size mismatch", good, n + 1, true, false},
		{"gzip corrupted", append(append([]byte{}, good[:40]...), append([]byte{0xff, 0xff}, good[42:]...)...), n, true, false},
		{"raw ok", []byte("REDIS0011"), 9, false, true},
		{"raw truncated", []byte("REDIS"), 9, false, false},
		{"not gzip", []byte("plain"), 5, true, false},
	}
	for _, c := range cases {
		if err := checkBackupStream(bytes.NewReader(c.data), c.size, c.gz); (err == nil) != c.ok {
			t.Errorf("%s: err=%v", c.name, err)
		}
	}
}

// 도구 출력이 담긴 오류(백업 기록, 이벤트, 로그에 남는다)에서 비밀번호를 가린다.
func TestScrubRedactsPasswords(t *testing.T) {
	pw := "p@ss/w:rd 1"
	dt := &dbTarget{kind: "mongo", vars: map[string]string{"MONGOUSER": "root", "MONGOPASSWORD": pw}}
	uri, _ := mongoURI("db", 27017, dt.vars)
	base := errors.New("mongodump exited with code 1: error connecting to " + uri + " | auth failed for " + pw)
	err := dt.scrub(base)
	if strings.Contains(err.Error(), "w:rd") || !strings.Contains(err.Error(), "[redacted]") {
		t.Errorf("not redacted: %s", err)
	}
	if !errors.Is(err, base) {
		t.Error("scrub must keep the error chain")
	}
	pg := &dbTarget{kind: "postgres", vars: map[string]string{"POSTGRES_PASSWORD": "hunter22"}}
	plain := errors.New("pg_dump: connection refused")
	if pg.scrub(plain) != plain {
		t.Error("errors without secrets are returned unchanged")
	}
	if got := pg.scrub(errors.New("bad password hunter22")).Error(); got != "bad password [redacted]" {
		t.Errorf("got %q", got)
	}
	if (&dbTarget{kind: "postgres", vars: map[string]string{"POSTGRES_PASSWORD": "x"}}).scrub(plain) != plain || pg.scrub(nil) != nil {
		t.Error("short passwords and nil errors are left alone")
	}
}
