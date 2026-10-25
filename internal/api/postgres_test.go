package api

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// fakePGOps는 컨테이너를 실제로 멈추거나 백업하지 않는 postgresOps다. Enqueue는 진짜 배포 관리자(가짜 dispatch)를 쓴다.
type fakePGOps struct {
	mgr        *deploy.Manager
	st         *store.Store
	mu         sync.Mutex
	stops      []string
	backups    []string
	restores   []string
	deleted    []string
	dbBackups  int
	backupErr  error
	restoreErr error
}

func (f *fakePGOps) Stop(ctx context.Context, in *store.Instance) error {
	f.mu.Lock()
	f.stops = append(f.stops, in.ID)
	f.mu.Unlock()
	return f.st.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil)
}

func (f *fakePGOps) Enqueue(ctx context.Context, in *store.Instance, o deploy.EnqueueOpts) (*store.Deployment, error) {
	return f.mgr.Enqueue(ctx, in, o)
}

func (f *fakePGOps) CreateBackupSync(ctx context.Context, v *store.Volume) (*store.Backup, error) {
	if f.backupErr != nil {
		return nil, f.backupErr
	}
	b := &store.Backup{VolumeID: v.ID, Status: "running"}
	if err := f.st.CreateBackup(ctx, b); err != nil {
		return nil, err
	}
	b.Status = "done"
	f.st.UpdateBackup(ctx, b)
	f.mu.Lock()
	f.backups = append(f.backups, v.MountPath)
	f.mu.Unlock()
	return b, nil
}

func (f *fakePGOps) RestoreBackupSync(_ context.Context, b *store.Backup) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restores = append(f.restores, b.ID)
	return f.restoreErr
}

func (f *fakePGOps) CreateDBBackup(_ context.Context, in *store.Instance, trigger, _ string) (*store.DBBackup, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.dbBackups++
	return &store.DBBackup{ID: "dbb_post", InstanceID: in.ID, Trigger: trigger}, nil
}

func (f *fakePGOps) DeleteService(ctx context.Context, svc *store.Service) error {
	f.mu.Lock()
	f.deleted = append(f.deleted, svc.Name)
	f.mu.Unlock()
	return f.st.DeleteService(ctx, svc.ID)
}

func (f *fakePGOps) SyncTCP(context.Context, string) error { return nil }

type pgFixture struct {
	*ghFixture
	ops   *fakePGOps
	run   *fakeRunner
	major int
	rt    func(major int) string // preflight CSV
}

func pgRuntimeCSV(major, prepared, tables int, ext string) string {
	return "version_num,reg_columns,aclitem_columns,prepared_xacts,logical_slots,md5_roles,event_triggers,extensions,size_bytes,user_tables\n" +
		fmt.Sprintf("%d0004,0,0,%d,1,0,0,%s,10485760,%d\n", major, prepared, ext, tables)
}

func newPGFixture(t *testing.T) *pgFixture {
	old := postgresPoll
	postgresPoll = 5 * time.Millisecond
	t.Cleanup(func() { postgresPoll = old })
	f := newGHFixture(t)
	// 카탈로그 postgres 서비스(볼륨 포함)를 만든다.
	kind := "postgres"
	f.db = &store.Service{ProjectID: f.p.ID, Name: "pg", SourceType: "database", DatabaseKind: &kind}
	if err := f.st.CreateService(f.ctx, f.db); err != nil {
		t.Fatal(err)
	}
	def, _ := catalog.DatabaseService(kind)
	pin := &store.Instance{ServiceID: f.db.ID, EnvironmentID: f.prod.ID}
	pin.Spec.Defaults()
	def.ApplyToInstance(pin)
	if err := f.st.CreateInstance(f.ctx, pin); err != nil {
		t.Fatal(err)
	}
	if err := f.st.CreateVolume(f.ctx, &store.Volume{ProjectID: f.p.ID, EnvironmentID: f.prod.ID, InstanceID: &pin.ID, Name: "pg-data", MountPath: pgLegacyMount}); err != nil {
		t.Fatal(err)
	}
	pf := &pgFixture{ghFixture: f, major: 17}
	pf.ops = &fakePGOps{mgr: f.mgr, st: f.st}
	f.srv.pgOps = pf.ops
	pf.run = &fakeRunner{}
	pf.rt = func(major int) string { return pgRuntimeCSV(major, 0, 3, "pg_trgm@1.6") }
	pf.run.reply = func(stdin string) *node.ExecOutput {
		f.mu.Lock()
		m, rt := pf.major, pf.rt
		f.mu.Unlock()
		return &node.ExecOutput{Stdout: []byte(rt(m))}
	}
	f.srv.dataExec = pf.run
	dbIn := pf.dbIn()
	pf.goLive(dbIn.ID, "")
	return pf
}

func (f *pgFixture) setMajor(m int) {
	f.mu.Lock()
	f.major = m
	f.mu.Unlock()
}

func (f *pgFixture) setRT(rt func(int) string) {
	f.mu.Lock()
	f.rt = rt
	f.mu.Unlock()
}

func (f *pgFixture) dbIn() *store.Instance {
	in, err := f.st.GetInstanceFor(f.ctx, f.db.ID, f.prod.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	return in
}

// goLive는 배포(없으면 새로 만든다)를 live로 만들고 인스턴스를 online으로 둔다.
func (f *pgFixture) goLive(instanceID, depID string) {
	f.t.Helper()
	ctx := f.ctx
	var d *store.Deployment
	if depID == "" {
		in, _ := f.st.GetInstance(ctx, instanceID)
		img := "postgres:17-alpine"
		d = &store.Deployment{ServiceID: in.ServiceID, EnvironmentID: in.EnvironmentID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
			NodeID: "node_local", Image: &img, Env: map[string]string{"PGUSER": "postgres", "PGPASSWORD": "pw"}}
		if err := f.st.CreateDeployment(ctx, d); err != nil {
			f.t.Fatal(err)
		}
	} else {
		var err error
		if d, err = f.st.GetDeployment(ctx, depID); err != nil {
			f.t.Fatal(err)
		}
		d.Status = store.DepLive
		if d.Image == nil {
			img := "built"
			d.Image = &img
		}
	}
	d.Containers = []string{"keel-" + d.ID}
	if err := f.st.UpdateDeployment(ctx, d); err != nil {
		f.t.Fatal(err)
	}
	f.st.SetInstanceStatus(ctx, instanceID, store.InstOnline, &d.ID)
}

func (f *pgFixture) fail(depID string) {
	d, _ := f.st.GetDeployment(f.ctx, depID)
	msg := "healthcheck failed"
	d.Status, d.Error = store.DepFailed, &msg
	f.st.UpdateDeployment(f.ctx, d)
}

func (f *pgFixture) pv() map[string]string {
	return map[string]string{"sid": f.db.ID, "eid": f.prod.ID}
}

func (f *pgFixture) state() *store.PostgresState {
	st, _ := postgresState(f.ctx, f.st, f.dbIn().ID)
	return st
}

// waitFor는 cond가 참이 될 때까지(최대 3초) 기다린다.
func (f *pgFixture) waitFor(what string, cond func(*store.PostgresState) bool) *store.PostgresState {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		st := f.state()
		if cond(st) {
			return st
		}
		if time.Now().After(deadline) {
			f.t.Fatalf("timed out waiting for %s: %+v", what, st)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *pgFixture) idle() {
	f.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for f.srv.postgresBusy(f.dbIn().ID) {
		if time.Now().After(deadline) {
			f.t.Fatal("postgres operation did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestPostgresImageMajor(t *testing.T) {
	for img, want := range map[string]int{
		"postgres:17-alpine": 17, "postgres:16.4-alpine": 16, "docker.io/library/postgres:15-alpine3.20": 15,
		"pgautoupgrade/pgautoupgrade:18-alpine": 18, "postgres:17-alpine@sha256:abc": 17,
		"postgres:17": 0, "postgres:latest": 0, "postgres:alpine": 0, "ghcr.io/x/postgres:17-alpine": 0, "postgres": 0, "postgres:19-alpine": 0,
	} {
		i := img
		got, ok := postgresImageMajor(&i)
		if (want == 0) == ok || ok && got != want {
			t.Errorf("%s: got %d %v, want %d", img, got, ok, want)
		}
	}
	if got := pgTargets(16); fmt.Sprint(got) != "[17 18]" {
		t.Errorf("targets %v", got)
	}
	if len(pgTargets(13)) != 0 || len(pgTargets(18)) != 0 {
		t.Error("unsupported source majors must have no targets")
	}
}

func TestPooledVarsRoundTrip(t *testing.T) {
	orig := map[string]string{"DATABASE_URL": "postgresql://custom@db/x", "DATABASE_PUBLIC_URL": "postgresql://pub/x", "PGHOST": "h"}
	v := map[string]string{}
	for k, x := range orig {
		v[k] = x
	}
	applyPooledVars(v, "db-pgbouncer", true)
	if v["DATABASE_URL"] != "postgresql://${{PGUSER}}:${{PGPASSWORD}}@${{db-pgbouncer.KEEL_PRIVATE_DOMAIN}}:5432/${{PGDATABASE}}" ||
		v["DATABASE_UNPOOLED_URL"] != orig["DATABASE_URL"] || v["DATABASE_PUBLIC_UNPOOLED_URL"] != orig["DATABASE_PUBLIC_URL"] ||
		!strings.Contains(v["DATABASE_PUBLIC_URL"], "${{db-pgbouncer.KEEL_TCP_PROXY_PORT}}") {
		t.Fatalf("pooled vars %v", v)
	}
	applyPooledVars(v, "db-pgbouncer", true) // 두 번 불러도 직접 연결 값을 잃지 않는다
	if v["DATABASE_UNPOOLED_URL"] != orig["DATABASE_URL"] {
		t.Fatalf("idempotent %v", v)
	}
	applyPooledVars(v, "db-pgbouncer", false) // PgBouncer 공개 접속이 없어지면 공개 URL은 직접 연결로
	if v["DATABASE_PUBLIC_URL"] != orig["DATABASE_PUBLIC_URL"] || v["DATABASE_PUBLIC_UNPOOLED_URL"] != "" {
		t.Fatalf("private only %v", v)
	}
	removePooledVars(v)
	if fmt.Sprint(v) != fmt.Sprint(orig) {
		t.Fatalf("round trip %v != %v", v, orig)
	}
	if !referencesPostgresURL(map[string]string{"X": "a ${{ db.DATABASE_URL }} b"}, "db") || referencesPostgresURL(map[string]string{"X": "${{db.PGHOST}}"}, "db") {
		t.Error("references")
	}
}

func TestParsePGRuntime(t *testing.T) {
	rt, err := parsePGRuntime([]byte(pgRuntimeCSV(16, 2, 5, "\"citext@1.6 postgis@3.4\"")))
	if err != nil {
		t.Fatal(err)
	}
	if rt.Major != 16 || rt.PreparedXacts != 2 || rt.LogicalSlots != 1 || rt.UserTables != 5 || *rt.SizeBytes != 10485760 ||
		fmt.Sprint(rt.Extensions) != "[citext@1.6 postgis@3.4]" {
		t.Fatalf("%+v", rt)
	}
	if _, err := parsePGRuntime([]byte("version_num\n90600\n")); err == nil {
		t.Error("accepted bogus version")
	}
}

func TestPostgresPoolingLifecycle(t *testing.T) {
	f := newPGFixture(t)
	ctx := f.ctx
	// 다른 서비스가 DB의 DATABASE_URL을 참조하고 실행 중이다
	webIn, _ := f.st.GetInstanceFor(ctx, f.web.ID, f.prod.ID)
	webIn.Variables = map[string]string{"DATABASE_URL": "${{pg.DATABASE_URL}}"}
	f.st.UpdateInstanceConfig(ctx, webIn)
	f.goLive(webIn.ID, "")
	dbIn := f.dbIn()
	dbIn.TCPProxy = &store.TCPProxy{ContainerPort: 5432, PublicPort: 15432}
	f.st.UpdateInstanceConfig(ctx, dbIn)
	directURL := dbIn.Variables["DATABASE_URL"]

	if code, out := f.call(f.srv.putPostgresPooling, "PUT", "/x", `{"pool_mode":"bogus"}`, f.pv()); code != 400 {
		t.Fatalf("bad mode: %d %v", code, out)
	}
	code, out := f.call(f.srv.putPostgresPooling, "PUT", "/x", `{"pool_mode":"transaction"}`, f.pv())
	if code != 202 || out["status"] != "deploying" || out["pooler_service_name"] != "pg-pgbouncer" {
		t.Fatalf("enable: %d %v", code, out)
	}
	if code, _ := f.call(f.srv.putPostgresPooling, "PUT", "/x", `{}`, f.pv()); code != 409 {
		t.Fatalf("concurrent change should be rejected, got %d", code)
	}
	st := f.state()
	pooler, err := f.st.GetService(ctx, st.PoolerServiceID)
	if err != nil || pooler.SourceType != "image" {
		t.Fatalf("pooler service %v %v", pooler, err)
	}
	poolIn, _ := f.st.GetInstanceFor(ctx, pooler.ID, f.prod.ID)
	if *poolIn.Spec.Image != pgBouncerImage || poolIn.Variables["DB_HOST"] != "${{pg.PGHOST}}" || poolIn.Variables["POOL_MODE"] != "transaction" ||
		poolIn.Variables["MAX_PREPARED_STATEMENTS"] != "" || poolIn.TCPProxy == nil {
		t.Fatalf("pooler instance %+v vars %v", poolIn.Spec, poolIn.Variables)
	}
	// live가 되기 전에는 DB 변수를 바꾸지 않는다
	if f.dbIn().Variables["DATABASE_URL"] != directURL {
		t.Fatal("DATABASE_URL switched before PgBouncer was live")
	}
	f.goLive(poolIn.ID, st.PoolerDeploymentID)
	f.waitFor("pooling enabled", func(s *store.PostgresState) bool { return s.PoolingStatus == "enabled" })
	f.idle()
	v := f.dbIn().Variables
	if !strings.Contains(v["DATABASE_URL"], "${{pg-pgbouncer.KEEL_PRIVATE_DOMAIN}}") || v["DATABASE_UNPOOLED_URL"] != directURL ||
		!strings.Contains(v["DATABASE_PUBLIC_URL"], "pg-pgbouncer.KEEL_TCP_PROXY_DOMAIN") || v["DATABASE_PUBLIC_UNPOOLED_URL"] == "" {
		t.Fatalf("db vars %v", v)
	}
	// 해석하면 PgBouncer 주소가 나온다
	res, err := f.mgr.Resolve(ctx, f.dbIn(), nil)
	if err != nil || !strings.Contains(res.Vars["DATABASE_URL"], "@pg-pgbouncer.production.keel.internal:5432/keel") {
		t.Fatalf("resolved %v %v", res.Vars["DATABASE_URL"], err)
	}
	deps, _ := f.st.ListDeployments(ctx, webIn.ID, 5)
	if len(deps) == 0 || deps[0].Trigger != "postgres-pooling" {
		t.Fatalf("consumer was not redeployed: %+v", deps)
	}

	// 설정 바꾸기: PgBouncer만 다시 배포한다
	code, out = f.call(f.srv.putPostgresPooling, "PUT", "/x", `{"pool_mode":"session","default_pool_size":30,"max_prepared_statements":0}`, f.pv())
	if code != 202 || out["status"] != "updating" || out["enabled"] != true {
		t.Fatalf("configure: %d %v", code, out)
	}
	poolIn, _ = f.st.GetInstanceFor(ctx, pooler.ID, f.prod.ID)
	if poolIn.Variables["POOL_MODE"] != "session" || poolIn.Variables["DEFAULT_POOL_SIZE"] != "30" || poolIn.Variables["MAX_PREPARED_STATEMENTS"] != "0" {
		t.Fatalf("pooler vars %v", poolIn.Variables)
	}
	f.fail(f.state().PoolerDeploymentID)
	st = f.waitFor("config failure recorded", func(s *store.PostgresState) bool { return s.PoolingError != "" })
	if st.PoolingStatus != "enabled" {
		t.Fatalf("failed reconfigure must keep pooling enabled: %+v", st)
	}
	f.idle()
	code, out = f.call(f.srv.getPostgresPooling, "GET", "/x", "", f.pv())
	if code != 200 || out["pool_mode"] != "session" || out["max_prepared_statements"] != float64(0) || len(out["variables"].([]any)) != 4 {
		t.Fatalf("get: %d %v", code, out)
	}

	// 제거
	code, out = f.call(f.srv.deletePostgresPooling, "DELETE", "/x", "", f.pv())
	if code != 200 || out["status"] != "off" {
		t.Fatalf("remove: %d %v", code, out)
	}
	v = f.dbIn().Variables
	if v["DATABASE_URL"] != directURL || v["DATABASE_UNPOOLED_URL"] != "" || v["DATABASE_PUBLIC_UNPOOLED_URL"] != "" || strings.Contains(v["DATABASE_PUBLIC_URL"], "pgbouncer") {
		t.Fatalf("restored vars %v", v)
	}
	if _, err := f.st.GetService(ctx, pooler.ID); !errors.Is(err, store.ErrNotFound) || len(f.ops.deleted) != 1 {
		t.Fatalf("pooler service not deleted: %v %v", err, f.ops.deleted)
	}
}

func TestPostgresPoolingRejectsOtherServices(t *testing.T) {
	f := newPGFixture(t)
	code, _ := f.call(f.srv.getPostgresPooling, "GET", "/x", "", map[string]string{"sid": f.web.ID, "eid": f.prod.ID})
	if code != 400 {
		t.Fatalf("non-postgres service: %d", code)
	}
}

func checkByKey(out map[string]any, key string) map[string]any {
	for _, c := range out["checks"].([]any) {
		if m := c.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}

func TestPostgresUpgradePreflight(t *testing.T) {
	f := newPGFixture(t)
	code, out := f.call(f.srv.postgresUpgradePreflight, "GET", "/x?to=18", "", f.pv())
	if code != 200 || out["ready"] != true || out["current_major"] != float64(17) || fmt.Sprint(out["available_targets"]) != "[18]" {
		t.Fatalf("preflight: %d %v", code, out)
	}
	if c := checkByKey(out, "logical-slots"); c == nil || c["passed"] != false || c["severity"] != "warning" {
		t.Errorf("warning check: %v", c)
	}
	if !strings.Contains(string(f.run.last().Stdin), "pg_prepared_xacts") {
		t.Error("preflight query did not run in the container")
	}
	// blocker: prepared 트랜잭션, 이미지에 없는 확장, 대상 버전
	f.setRT(func(m int) string { return pgRuntimeCSV(m, 1, 3, "\"postgis@3.4 pg_trgm@1.6\"") })
	_, out = f.call(f.srv.postgresUpgradePreflight, "GET", "/x?to=17", "", f.pv())
	for _, key := range []string{"prepared-xacts", "extensions", "target"} {
		if c := checkByKey(out, key); c == nil || c["passed"] != false {
			t.Errorf("%s should fail: %v", key, c)
		}
	}
	if out["ready"] != false || !strings.Contains(checkByKey(out, "extensions")["message"].(string), "postgis") {
		t.Fatalf("not ready expected: %v", out)
	}
	code, out = f.call(f.srv.startPostgresUpgrade, "POST", "/x", `{"to_major":18}`, f.pv())
	if code != 409 || out["preflight"] == nil {
		t.Fatalf("start with blockers: %d %v", code, out)
	}
	if len(f.ops.stops) != 0 {
		t.Fatal("database was stopped despite blockers")
	}
	// 지원하지 않는 이미지
	in := f.dbIn()
	img := "postgres:latest"
	in.Spec.Image = &img
	f.st.UpdateInstanceConfig(f.ctx, in)
	_, out = f.call(f.srv.postgresUpgradePreflight, "GET", "/x", "", f.pv())
	if c := checkByKey(out, "image"); c["passed"] != false || out["ready"] != false {
		t.Fatalf("latest tag must be rejected: %v", out)
	}
}

func TestPostgresUpgradeAndRevert(t *testing.T) {
	f := newPGFixture(t)
	ctx := f.ctx
	code, out := f.call(f.srv.startPostgresUpgrade, "POST", "/x", `{"to_major":18}`, f.pv())
	if code != 202 || out["status"] != "preparing" || out["from_major"] != float64(17) {
		t.Fatalf("start: %d %v", code, out)
	}
	st := f.waitFor("upgrade deployment", func(s *store.PostgresState) bool { return s.UpgradeStatus == "upgrading" })
	if st.UpgradeBackupID == "" || len(f.ops.stops) != 1 || fmt.Sprint(f.ops.backups) != "["+pgLegacyMount+"]" {
		t.Fatalf("backup before upgrade: %+v stops=%v backups=%v", st, f.ops.stops, f.ops.backups)
	}
	in := f.dbIn()
	vols, _ := f.st.ListVolumesByInstance(ctx, in.ID)
	if *in.Spec.Image != "pgautoupgrade/pgautoupgrade:18-alpine" || in.Variables["PGDATA"] != pgUpgradePGData || vols[0].MountPath != pgUpgradeMount {
		t.Fatalf("upgrade config: %s %s %s", *in.Spec.Image, in.Variables["PGDATA"], vols[0].MountPath)
	}
	dep, _ := f.st.GetDeployment(ctx, st.UpgradeDeploymentID)
	if dep.Spec.HealthcheckTimeoutS != pgUpgradeHealthcheck || dep.ReqSpec.HealthcheckTimeoutS == pgUpgradeHealthcheck {
		t.Errorf("healthcheck timeout: spec %d req %d", dep.Spec.HealthcheckTimeoutS, dep.ReqSpec.HealthcheckTimeoutS)
	}
	if code, _ := f.call(f.srv.revertPostgresUpgrade, "POST", "/x", "", f.pv()); code != 409 {
		t.Fatalf("revert during upgrade: %d", code)
	}
	f.setMajor(18)
	f.goLive(in.ID, st.UpgradeDeploymentID)
	st = f.waitFor("upgrade success", func(s *store.PostgresState) bool { return s.UpgradeStatus == "succeeded" })
	f.idle()
	if st.UpgradePostBackupID != "dbb_post" || st.UpgradeFinishedAt == nil {
		t.Fatalf("after upgrade: %+v", st)
	}
	code, out = f.call(f.srv.getPostgresUpgrade, "GET", "/x", "", f.pv())
	if code != 200 || out["can_revert"] != true || out["current_major"] != float64(18) || out["revert_deadline"] == nil {
		t.Fatalf("status: %d %v", code, out)
	}

	// 되돌리기
	code, out = f.call(f.srv.revertPostgresUpgrade, "POST", "/x", "", f.pv())
	if code != 202 || out["status"] != "reverting" {
		t.Fatalf("revert: %d %v", code, out)
	}
	st = f.waitFor("revert deployment", func(s *store.PostgresState) bool {
		return s.UpgradeDeploymentID != "" && s.UpgradeStatus == "reverting"
	})
	if fmt.Sprint(f.ops.restores) != "["+st.UpgradeBackupID+"]" {
		t.Fatalf("restores %v", f.ops.restores)
	}
	in = f.dbIn()
	vols, _ = f.st.ListVolumesByInstance(ctx, in.ID)
	if *in.Spec.Image != "postgres:17-alpine" || in.Variables["PGDATA"] != pgLegacyPGData || vols[0].MountPath != pgLegacyMount {
		t.Fatalf("reverted config: %s %s %s", *in.Spec.Image, in.Variables["PGDATA"], vols[0].MountPath)
	}
	f.setMajor(17)
	f.goLive(in.ID, st.UpgradeDeploymentID)
	st = f.waitFor("reverted", func(s *store.PostgresState) bool { return s.UpgradeStatus == "reverted" })
	if st.UpgradeError != "" {
		t.Errorf("manual revert error: %q", st.UpgradeError)
	}
	f.idle()
	if code, _ := f.call(f.srv.revertPostgresUpgrade, "POST", "/x", "", f.pv()); code != 409 {
		t.Fatalf("second revert: %d", code)
	}
}

func TestPostgresUpgradeFailureAutoReverts(t *testing.T) {
	f := newPGFixture(t)
	if code, out := f.call(f.srv.startPostgresUpgrade, "POST", "/x", `{"to_major":18}`, f.pv()); code != 202 {
		t.Fatalf("start: %d %v", code, out)
	}
	st := f.waitFor("upgrade deployment", func(s *store.PostgresState) bool { return s.UpgradeStatus == "upgrading" })
	f.fail(st.UpgradeDeploymentID)
	st = f.waitFor("auto revert deployment", func(s *store.PostgresState) bool {
		return s.UpgradeStatus == "reverting" && s.UpgradeDeploymentID != "" && s.UpgradeDeploymentID != st.UpgradeDeploymentID
	})
	if *f.dbIn().Spec.Image != "postgres:17-alpine" || len(f.ops.restores) != 1 {
		t.Fatalf("auto revert did not restore: %v", f.ops.restores)
	}
	f.goLive(f.dbIn().ID, st.UpgradeDeploymentID)
	st = f.waitFor("reverted", func(s *store.PostgresState) bool { return s.UpgradeStatus == "reverted" })
	if !strings.Contains(st.UpgradeError, "healthcheck failed") || !strings.Contains(st.UpgradeError, "자동 복구했습니다") {
		t.Fatalf("error %q", st.UpgradeError)
	}
	f.idle()
}

func TestPostgresUpgradeVerifiesNewMajor(t *testing.T) {
	f := newPGFixture(t)
	f.call(f.srv.startPostgresUpgrade, "POST", "/x", `{"to_major":18}`, f.pv())
	st := f.waitFor("upgrade deployment", func(s *store.PostgresState) bool { return s.UpgradeStatus == "upgrading" })
	// 배포는 live지만 데이터 디렉터리가 비어 있다(사용자 표 0개)
	f.setRT(func(m int) string { return pgRuntimeCSV(m, 0, 0, "") })
	f.setMajor(18)
	f.goLive(f.dbIn().ID, st.UpgradeDeploymentID)
	st = f.waitFor("auto revert", func(s *store.PostgresState) bool {
		return s.UpgradeStatus == "reverting" && s.UpgradeDeploymentID != ""
	})
	if !strings.Contains(st.UpgradeError, "사용자 표") {
		t.Fatalf("error %q", st.UpgradeError)
	}
	f.goLive(f.dbIn().ID, st.UpgradeDeploymentID)
	f.waitFor("reverted", func(s *store.PostgresState) bool { return s.UpgradeStatus == "reverted" })
	f.idle()
}

func TestPostgresUpgradeBackupFailureRestarts(t *testing.T) {
	f := newPGFixture(t)
	f.ops.backupErr = errors.New("disk full")
	f.call(f.srv.startPostgresUpgrade, "POST", "/x", `{"to_major":18}`, f.pv())
	st := f.waitFor("failed", func(s *store.PostgresState) bool { return s.UpgradeStatus == "failed" })
	f.idle()
	if !strings.Contains(st.UpgradeError, "disk full") || *f.dbIn().Spec.Image != "postgres:17-alpine" {
		t.Fatalf("%+v", st)
	}
	deps, _ := f.st.ListDeployments(f.ctx, f.dbIn().ID, 1)
	if len(deps) == 0 || deps[0].Trigger != "postgres-upgrade-recovery" {
		t.Fatalf("database was not restarted: %+v", deps)
	}
	if code, out := f.call(f.srv.getPostgresUpgrade, "GET", "/x", "", f.pv()); out["can_revert"] != false || code != 200 {
		t.Fatalf("no backup → no revert: %v", out)
	}
}
