package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenAppliesPragmas(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, c := range []struct {
		pragma string
		want   string
	}{
		{"journal_mode", "wal"},
		{"foreign_keys", "1"},
		{"busy_timeout", "5000"},
		{"synchronous", "1"}, // NORMAL
	} {
		var got string
		if err := s.db.QueryRowContext(ctx, `PRAGMA `+c.pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if strings.ToLower(got) != c.want {
			t.Errorf("PRAGMA %s = %q, want %q", c.pragma, got, c.want)
		}
	}
}

// 경로에 '?'나 '#'이 있어도 그 파일을 연다(예전에는 '?' 뒤가 쿼리로 잘려 다른 파일을 열었다).
func TestOpenPathWithURIChars(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a?b#c%d")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "k.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("db not created at %s: %v", path, err)
	}
}

func TestMigrationsIdempotentOnReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "k.db")
	for i := 0; i < 3; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var v int
		if err := s.db.QueryRowContext(ctx, `SELECT version FROM schema_version`).Scan(&v); err != nil {
			t.Fatal(err)
		}
		var rows int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM schema_version`).Scan(&rows)
		s.Close()
		if v != len(migrations) || rows != 1 {
			t.Fatalf("open %d: version %d (rows %d), want %d", i, v, rows, len(migrations))
		}
	}
}

// 다른 프로세스가 먼저 버전을 올렸으면 같은 마이그레이션을 다시 적용하지 않고 되돌린다.
func TestBumpSchemaVersionDetectsConcurrentMigration(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	err := s.Tx(ctx, func(tx *Store) error {
		if _, err := tx.q.ExecContext(ctx, `CREATE TABLE should_roll_back (x INTEGER)`); err != nil {
			return err
		}
		return tx.bumpSchemaVersion(ctx, len(migrations)-1) // 실제 버전은 이미 len(migrations)
	})
	if err == nil {
		t.Fatal("expected concurrent migration error")
	}
	if s.tableExists(ctx, "should_roll_back") {
		t.Error("migration changes were committed")
	}
}

// 모든 외래 키 자식 열에는 그 열로 시작하는 인덱스(또는 기본 키)가 있어야 한다. 없으면 부모를 지울 때 자식 표 전체를 훑는다.
func TestForeignKeysIndexed(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	rows, err := s.db.QueryContext(ctx, `SELECT m.name, f."from", f."table" FROM sqlite_master m, pragma_foreign_key_list(m.name) f WHERE m.type='table'`)
	if err != nil {
		t.Fatal(err)
	}
	type fk struct{ table, col, parent string }
	var fks []fk
	for rows.Next() {
		var f fk
		if err := rows.Scan(&f.table, &f.col, &f.parent); err != nil {
			t.Fatal(err)
		}
		fks = append(fks, f)
	}
	rows.Close()
	if len(fks) < 20 {
		t.Fatalf("only %d foreign keys found", len(fks))
	}
	for _, f := range fks {
		var idx, pk int
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_index_list(?) il, pragma_index_info(il.name) ii WHERE ii.seqno=0 AND ii.name=?`, f.table, f.col).Scan(&idx)
		s.db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info(?) WHERE name=? AND pk=1`, f.table, f.col).Scan(&pk)
		if idx == 0 && pk == 0 {
			t.Errorf("foreign key %s(%s) -> %s has no index", f.table, f.col, f.parent)
		}
	}
}

// 자주 도는 조회가 인덱스를 쓰는지 쿼리 계획으로 확인한다.
func TestHotQueriesUseIndexes(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	for _, q := range []string{
		`SELECT id FROM instances WHERE environment_id='x' ORDER BY id`,
		`SELECT id FROM volumes WHERE instance_id='x' ORDER BY created_at`,
		`SELECT id FROM volumes WHERE environment_id='x' ORDER BY created_at`,
		`SELECT id FROM backups WHERE volume_id='x' ORDER BY created_at DESC`,
		`SELECT id FROM cron_runs WHERE instance_id='x' ORDER BY started_at DESC LIMIT 5`,
		`SELECT id FROM cron_runs WHERE status='running'`,
		`DELETE FROM events WHERE created_at < 'x'`,
		`DELETE FROM sessions WHERE expires_at <= 'x'`,
		`SELECT * FROM usage_hourly WHERE hour>=1 AND hour<2 ORDER BY hour`,
	} {
		rows, err := s.db.QueryContext(ctx, `EXPLAIN QUERY PLAN `+q)
		if err != nil {
			t.Fatal(err)
		}
		var plan []string
		for rows.Next() {
			var id, parent, notused int
			var detail string
			rows.Scan(&id, &parent, &notused, &detail)
			plan = append(plan, detail)
		}
		rows.Close()
		p := strings.Join(plan, "; ")
		if !strings.Contains(p, "INDEX") {
			t.Errorf("%s: no index used: %s", q, p)
		}
	}
}

// f가 panic해도 트랜잭션이 남지 않는다(커넥션이 하나라 남으면 이후 모든 쿼리가 막힌다).
func TestTxRollsBackOnPanic(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	func() {
		defer func() { recover() }()
		s.Tx(ctx, func(tx *Store) error {
			if err := tx.SetSetting(ctx, "panic_key", "v"); err != nil {
				return err
			}
			panic("boom")
		})
	}()
	done := make(chan error, 1)
	go func() {
		_, err := s.GetSetting(ctx, "panic_key")
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("setting from panicked tx: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("store blocked after panic inside Tx")
	}
}

// 여러 고루틴이 동시에 써도 SQLITE_BUSY 없이 모두 들어간다.
func TestConcurrentWrites(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	p := &Project{Name: "conc"}
	if _, err := s.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 200)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				if err := s.CreateEvent(ctx, &Event{ProjectID: p.ID, Kind: "k", Message: "m"}); err != nil {
					errs <- err
				}
				if _, err := s.ListEvents(ctx, p.ID, "", 5); err != nil {
					errs <- err
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	evs, _ := s.ListEvents(ctx, p.ID, "", 1000)
	if len(evs) != 200 {
		t.Fatalf("events: %d", len(evs))
	}
}

func TestRandomSecretAlphabet(t *testing.T) {
	const alphabet = "abcdefghijkmnopqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	counts := map[rune]int{}
	for i := 0; i < 2000; i++ {
		v := RandomSecret(32)
		if len(v) != 32 {
			t.Fatalf("len %d", len(v))
		}
		for _, r := range v {
			if !strings.ContainsRune(alphabet, r) {
				t.Fatalf("unexpected rune %q", r)
			}
			counts[r]++
		}
	}
	// 나머지 편향(256 % 57 = 28)이 있으면 앞쪽 28글자가 나머지보다 1.25배 자주 나온다. 평균끼리 비교해 우연한 흔들림은 작다.
	var head, tail float64
	for i, r := range alphabet {
		if i < 256%len(alphabet) {
			head += float64(counts[r])
		} else {
			tail += float64(counts[r])
		}
	}
	head /= float64(256 % len(alphabet))
	tail /= float64(len(alphabet) - 256%len(alphabet))
	if head/tail > 1.1 {
		t.Errorf("modulo bias: head avg %.0f tail avg %.0f", head, tail)
	}
}

func TestPruneMetricsByIndex(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ps, _ := s.ListProjects(ctx)
	envs, _ := s.ListEnvironments(ctx, ps[0].ID)
	svc := &Service{ProjectID: ps[0].ID, Name: "web", SourceType: "image"}
	if err := s.CreateService(ctx, svc); err != nil {
		t.Fatal(err)
	}
	in := &Instance{ServiceID: svc.ID, EnvironmentID: envs[0].ID}
	if err := s.CreateInstance(ctx, in); err != nil {
		t.Fatal(err)
	}
	pts := []MetricRow{{T: 100, Resolution: 10}, {T: 200, Resolution: 10}, {T: 300, Resolution: 10}}
	s.InsertMetrics(ctx, in.ID, pts)
	s.InsertMetrics(ctx, "ins_deleted", pts)
	ids, err := s.MetricInstances(ctx)
	if err != nil || len(ids) != 2 {
		t.Fatalf("metric instances: %v %v", ids, err)
	}
	if err := s.PruneMetrics(ctx, 250); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.QueryMetrics(ctx, in.ID, 0); len(got) != 1 || got[0].T != 300 {
		t.Fatalf("kept: %+v", got)
	}
	if got, _ := s.QueryMetrics(ctx, "ins_deleted", 0); len(got) != 0 {
		t.Fatalf("orphan points kept: %+v", got)
	}
	if ids, _ := s.MetricInstances(ctx); len(ids) != 1 || ids[0] != in.ID {
		t.Fatalf("after prune: %v", ids)
	}

	np := []NodeMetricRow{{T: 100}, {T: 300}}
	s.InsertNodeMetrics(ctx, LocalNodeID, np)
	s.InsertNodeMetrics(ctx, "node_gone", np)
	if err := s.PruneNodeMetrics(ctx, 250); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.QueryNodeMetrics(ctx, LocalNodeID, 0); len(got) != 1 {
		t.Fatalf("node points kept: %+v", got)
	}
	if ids, _ := s.NodeMetricNodes(ctx); len(ids) != 1 || ids[0] != LocalNodeID {
		t.Fatalf("node ids after prune: %v", ids)
	}

	pid := ps[0].ID
	s.UpsertUsage(ctx, []UsageRow{
		{ProjectID: pid, EnvironmentID: "e", ServiceID: "a", Hour: 3600},
		{ProjectID: pid, EnvironmentID: "e", ServiceID: "a", Hour: 7200},
		{ProjectID: "prj_gone", EnvironmentID: "e", ServiceID: "b", Hour: 7200},
	})
	if err := s.PruneUsage(ctx, 5000); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.QueryUsage(ctx, "", 0, 1<<40); len(u) != 1 || u[0].Hour != 7200 || u[0].ProjectID != pid {
		t.Fatalf("usage after prune: %+v", u)
	}
}

// 서비스를 지우면(인스턴스는 CASCADE) 그 인스턴스의 도메인 basic auth 해시도 지워진다. 다른 인스턴스 것은 남는다.
func TestDomainAuthSettingsRemovedWithInstance(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)
	ps, _ := s.ListProjects(ctx)
	envs, _ := s.ListEnvironments(ctx, ps[0].ID)
	mk := func(name string) *Instance {
		svc := &Service{ProjectID: ps[0].ID, Name: name, SourceType: "image"}
		if err := s.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		in := &Instance{ServiceID: svc.ID, EnvironmentID: envs[0].ID}
		if err := s.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	a, b := mk("a"), mk("b")
	s.SetSetting(ctx, DomainAuthKey(a.ID, "a.example.com"), "hash-a")
	s.SetSetting(ctx, DomainAuthKey(b.ID, "b.example.com"), "hash-b")
	s.SetSetting(ctx, "unrelated", "x")
	if err := s.DeleteService(ctx, a.ServiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSetting(ctx, DomainAuthKey(a.ID, "a.example.com")); !errors.Is(err, ErrNotFound) {
		t.Errorf("hash of deleted instance kept: %v", err)
	}
	for _, k := range []string{DomainAuthKey(b.ID, "b.example.com"), "unrelated"} {
		if _, err := s.GetSetting(ctx, k); err != nil {
			t.Errorf("%s removed: %v", k, err)
		}
	}
	// 마이그레이션은 이미 남아 있던 고아 해시를 정리한다.
	s.SetSetting(ctx, DomainAuthKey("ins_gone", "x.example.com"), "old")
	if err := s.Tx(ctx, func(tx *Store) error { return migrateOrphanSettings(ctx, tx) }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetSetting(ctx, DomainAuthKey("ins_gone", "x.example.com")); !errors.Is(err, ErrNotFound) {
		t.Errorf("orphan hash kept: %v", err)
	}
	if _, err := s.GetSetting(ctx, DomainAuthKey(b.ID, "b.example.com")); err != nil {
		t.Errorf("live hash removed by cleanup: %v", err)
	}
}
