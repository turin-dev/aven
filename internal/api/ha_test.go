package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/catalog"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// haExec는 HA 명령용 가짜 exec다. reply(container, stdin)이 출력이나 오류를 정한다.
type haExec struct {
	mu    sync.Mutex
	calls []haCall
	reply func(container, stdin string) (string, error)
}

type haCall struct{ container, stdin string }

func (f *haExec) ExecRun(_ context.Context, _, container string, req node.ExecRunRequest, _ time.Duration) (*node.ExecOutput, error) {
	f.mu.Lock()
	f.calls = append(f.calls, haCall{container, string(req.Stdin)})
	reply := f.reply
	f.mu.Unlock()
	out, err := reply(container, string(req.Stdin))
	if err != nil {
		return nil, err
	}
	return &node.ExecOutput{Stdout: []byte(out)}, nil
}

func (f *haExec) sent(container, substr string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.container == container && strings.Contains(c.stdin, substr) {
			return true
		}
	}
	return false
}

type haFixture struct {
	*ghFixture
	exec    *haExec
	staging *store.Environment
}

func newHAFixture(t *testing.T) *haFixture {
	f := newGHFixture(t)
	f.mgr.Cfg.TCPPortMin, f.mgr.Cfg.TCPPortMax = 24300, 24400
	ex := &haExec{reply: func(string, string) (string, error) { return "", errors.New("no reply") }}
	f.srv.dataExec = ex
	oldGo, oldBackup, oldSleep, oldDelete := haGo, haBackup, haSleep, haDeleteService
	haGo = func(fn func()) { fn() }
	haDeleteService = func(s *Server, ctx context.Context, svc *store.Service) error {
		return s.Store.DeleteService(ctx, svc.ID)
	}
	haBackup = func(*Server, context.Context, *store.Volume) error { return nil }
	haSleep = func(context.Context, time.Duration) {}
	t.Cleanup(func() { haGo, haBackup, haSleep, haDeleteService = oldGo, oldBackup, oldSleep, oldDelete })
	staging := &store.Environment{ProjectID: f.p.ID, Name: "staging"}
	if err := f.st.CreateEnvironment(f.ctx, staging); err != nil {
		t.Fatal(err)
	}
	return &haFixture{ghFixture: f, exec: ex, staging: staging}
}

// dbService는 kind DB 서비스를 production에 만들고(볼륨, live 배포) 서비스를 돌려준다.
func (f *haFixture) dbService(name, kind string) *store.Service {
	f.t.Helper()
	k := kind
	sv := &store.Service{ProjectID: f.p.ID, Name: name, SourceType: "database", DatabaseKind: &k}
	if err := f.st.CreateService(f.ctx, sv); err != nil {
		f.t.Fatal(err)
	}
	def, _ := catalog.DatabaseService(kind)
	for _, env := range []*store.Environment{f.prod, f.staging} {
		in := &store.Instance{ServiceID: sv.ID, EnvironmentID: env.ID}
		in.Spec.Defaults()
		def.ApplyToInstance(in)
		if err := f.st.CreateInstance(f.ctx, in); err != nil {
			f.t.Fatal(err)
		}
		if err := f.st.CreateVolume(f.ctx, &store.Volume{ProjectID: f.p.ID, EnvironmentID: env.ID, InstanceID: &in.ID, Name: name + "-data", MountPath: def.Volumes[0]}); err != nil {
			f.t.Fatal(err)
		}
	}
	f.live(sv.ID)
	return sv
}

// live는 서비스의 production 인스턴스에 실행 중인 배포를 만든다(컨테이너 이름 = 서비스 이름).
func (f *haFixture) live(serviceID string) {
	f.t.Helper()
	in, err := f.st.GetInstanceFor(f.ctx, serviceID, f.prod.ID)
	if err != nil {
		f.t.Fatal(err)
	}
	svc, _ := f.st.GetService(f.ctx, serviceID)
	env := map[string]string{"PGUSER": "postgres", "PGPASSWORD": "pg-secret", "REDISPASSWORD": "r-secret", "MONGOUSER": "mongo", "MONGOPASSWORD": "m-secret"}
	d := &store.Deployment{ServiceID: serviceID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
		NodeID: store.LocalNodeID, Containers: []string{svc.Name}, Env: env}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		f.t.Fatal(err)
	}
	f.st.SetInstanceStatus(f.ctx, in.ID, store.InstOnline, &d.ID)
}

func (f *haFixture) inst(serviceID string) *store.Instance {
	f.t.Helper()
	in, err := f.st.GetInstanceFor(f.ctx, serviceID, f.prod.ID)
	if err != nil {
		f.t.Fatalf("instance %s: %v", serviceID, err)
	}
	return in
}

func (f *haFixture) svcNamed(name string) *store.Service {
	f.t.Helper()
	svcs, _ := f.st.ListServices(f.ctx, f.p.ID)
	for _, s := range svcs {
		if s.Name == name {
			return s
		}
	}
	f.t.Fatalf("service %s not found", name)
	return nil
}

func (f *haFixture) haCall(method, path, body string, sv *store.Service) (int, map[string]any) {
	f.t.Helper()
	pv := map[string]string{"sid": sv.ID, "eid": f.prod.ID}
	var code int
	var out map[string]any
	switch method + " " + path {
	case "GET ha":
		code, out = f.call(f.srv.getHA, "GET", "/", body, pv)
	case "POST ha":
		code, out = f.call(f.srv.convertHA, "POST", "/", body, pv)
	case "PATCH ha":
		code, out = f.call(f.srv.patchHA, "PATCH", "/", body, pv)
	case "DELETE ha":
		code, out = f.call(f.srv.revertHA, "DELETE", "/", body, pv)
	case "POST switchover":
		code, out = f.call(f.srv.switchoverHA, "POST", "/", body, pv)
	default:
		f.t.Fatalf("unknown call %s %s", method, path)
	}
	return code, out
}

func (f *haFixture) cluster(rootID string) *store.HACluster {
	f.t.Helper()
	cl, err := f.st.GetHAClusterForService(f.ctx, rootID, f.prod.ID)
	if err != nil {
		f.t.Fatalf("cluster: %v", err)
	}
	return cl
}

func (f *haFixture) dispatchedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.dispatched)
}

func TestHAEligibility(t *testing.T) {
	f := newHAFixture(t)
	pg := f.dbService("main", "postgres")
	code, out := f.haCall("GET", "ha", "", pg)
	if code != 200 || out["eligible"] != true || out["cluster"] != nil {
		t.Fatalf("eligible postgres: %d %v", code, out)
	}
	comp := out["companion"].(map[string]any)
	sels := comp["selectors"].([]any)
	if comp["key"] != "postgres-ha" || comp["version"] != "17" || len(sels) != 2 || sels[0].(map[string]any)["role"] != "replica" ||
		sels[0].(map[string]any)["default"] != float64(2) {
		t.Errorf("companion: %v", comp)
	}

	in := f.inst(pg.ID)
	img, cmd := "postgres:latest", "postgres -c max_connections=500"
	in.Image, in.StartCommand = &img, &cmd
	f.st.UpdateInstanceConfig(f.ctx, in)
	code, out = f.haCall("GET", "ha", "", pg)
	probs := out["problems"].([]any)
	if out["eligible"] != false || len(probs) != 2 || !strings.Contains(probs[0].(string), ":latest") || !strings.Contains(probs[1].(string), "시작 명령") {
		t.Errorf("problems: %d %v", code, out)
	}
	if code, out := f.haCall("POST", "ha", `{}`, pg); code != 409 || len(out["problems"].([]any)) != 2 {
		t.Errorf("convert ineligible: %d %v", code, out)
	}
	// mysql은 아직 지원하지 않는다
	my := f.dbService("my", "mysql")
	if _, out := f.haCall("GET", "ha", "", my); out["eligible"] != false || !strings.Contains(out["problems"].([]any)[0].(string), "mysql") {
		t.Errorf("mysql: %v", out)
	}
}

func TestHAPostgresConvertFailoverRevert(t *testing.T) {
	f := newHAFixture(t)
	pg := f.dbService("pg", "postgres")
	// 공개 접속을 켜 둔 DB와, 그 DB를 참조하는 앱
	root := f.inst(pg.ID)
	root.TCPProxy = &store.TCPProxy{ContainerPort: 5432, PublicPort: 24399}
	f.st.UpdateInstanceConfig(f.ctx, root)
	web := f.inst(f.web.ID)
	web.Variables = map[string]string{"DATABASE_URL": "${{pg.DATABASE_URL}}", "PGPASSWORD": "${{pg.POSTGRES_PASSWORD}}"}
	f.st.UpdateInstanceConfig(f.ctx, web)

	before := f.dispatchedCount()
	code, out := f.haCall("POST", "ha", `{"replicas": 2, "edge": 1}`, pg)
	if code != 202 {
		t.Fatalf("convert: %d %v", code, out)
	}
	if mig := out["migrated_services"].([]any); len(mig) != 1 || mig[0] != "web" {
		t.Errorf("migrated: %v", mig)
	}
	if got := f.dispatchedCount() - before; got != 4 {
		t.Errorf("deployments enqueued: %d, want 4 (root, 2 replicas, edge)", got)
	}
	r1, r2, edge := f.svcNamed("pg-replica-1"), f.svcNamed("pg-replica-2"), f.svcNamed("pg-ha")
	if _, err := f.st.GetInstanceFor(f.ctx, r1.ID, f.staging.ID); err == nil {
		t.Error("replica must exist only in the converted environment")
	}
	root, ri, ei := f.inst(pg.ID), f.inst(r1.ID), f.inst(edge.ID)
	if r1.DatabaseKind == nil || *r1.DatabaseKind != "postgres" || *r1.Template != "postgres-ha" || *ri.Image != "postgres:17-alpine" {
		t.Errorf("replica service: %+v %v", r1, ri.Image)
	}
	if ri.Variables["KEEL_HA_ROLE"] != "replica" || ri.Variables["KEEL_HA_PRIMARY"] != "${{pg.KEEL_PRIVATE_DOMAIN}}" ||
		ri.Variables["POSTGRES_PASSWORD"] != "${{pg.POSTGRES_PASSWORD}}" || ri.HealthcheckTimeoutS != 3600 {
		t.Errorf("replica vars: %v", ri.Variables)
	}
	if vols, _ := f.st.ListVolumesByInstance(f.ctx, ri.ID); len(vols) != 1 || vols[0].MountPath != "/var/lib/postgresql/data" {
		t.Errorf("replica volume: %v", vols)
	}
	if root.StartCommand == nil || !strings.Contains(*root.StartCommand, "pg_basebackup") || root.Variables["KEEL_HA_ROLE"] != "primary" ||
		root.Variables["POSTGRES_PASSWORD"] == "" || root.TCPProxy != nil {
		t.Errorf("root after convert: start=%v vars=%v tcp=%v", root.StartCommand, root.Variables, root.TCPProxy)
	}
	if *ei.Image != "haproxy:3.0-alpine" || ei.Variables["KEEL_HA_NODES"] != "${{pg.KEEL_PRIVATE_DOMAIN}}:5432" || ei.TCPProxy == nil ||
		ei.Variables["DATABASE_URL"] == "" || !strings.Contains(ei.Variables["PGPASSWORD"], "${{pg.") {
		t.Errorf("edge: %v tcp=%v", ei.Variables, ei.TCPProxy)
	}
	if web := f.inst(f.web.ID); web.Variables["DATABASE_URL"] != "${{pg-ha.DATABASE_URL}}" || web.Variables["PGPASSWORD"] != "${{pg.POSTGRES_PASSWORD}}" {
		t.Errorf("web refs: %v", web.Variables)
	}
	cl := f.cluster(pg.ID)
	if cl.Status != store.HAProvisioning || cl.PrimaryServiceID != pg.ID || !cl.AutoFailover || cl.Saved.Counts["replica"] != 2 {
		t.Errorf("cluster: %+v", cl)
	}
	// 같은 클러스터를 replica에서 봐도 된다
	if _, out := f.haCall("GET", "ha", "", r1); out["cluster"] == nil {
		t.Error("GET ha from a member")
	}
	if code, _ := f.haCall("POST", "ha", `{}`, pg); code != 409 {
		t.Errorf("double convert: %d", code)
	}

	// 배포가 끝났다고 치고 controller를 돌린다.
	for _, id := range []string{pg.ID, r1.ID, r2.ID, edge.ID} {
		f.live(id)
	}
	var mu sync.Mutex
	rootDown := false
	f.exec.reply = func(c, stdin string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case c == "pg" && rootDown:
			return "", errors.New("node is not available")
		case c == "pg":
			return "f|0/500|2|", nil
		case c == "pg-replica-1":
			return "t|0/400|0|streaming", nil
		case c == "pg-replica-2":
			return "t|0/480|0|streaming", nil
		}
		return "", errors.New("unexpected " + c)
	}
	f.srv.HAOnce(f.ctx)
	if cl := f.cluster(pg.ID); cl.Status != store.HAActive {
		t.Fatalf("cluster not active: %+v", cl)
	}
	_, out = f.haCall("GET", "ha", "", pg)
	members := out["cluster"].(map[string]any)["members"].([]any)
	if len(members) != 4 || members[0].(map[string]any)["db_role"] != "primary" || members[2].(map[string]any)["lag_bytes"] != float64(0x80) {
		t.Errorf("members: %v", members)
	}

	// 주 노드가 30초(3번) 응답하지 않으면 가장 앞선 replica(pg-replica-2)를 승격한다.
	mu.Lock()
	rootDown = true
	mu.Unlock()
	before = f.dispatchedCount()
	for range haFailAfter {
		f.srv.HAOnce(f.ctx)
	}
	cl = f.cluster(pg.ID)
	if cl.PrimaryServiceID != r2.ID || cl.Status != store.HAActive || !strings.Contains(cl.Message, "pg-replica-2") {
		t.Fatalf("failover: %+v", cl)
	}
	if got := f.dispatchedCount() - before; got != 4 {
		t.Errorf("failover deployments: %d, want 4 (new primary, edge, other replica, old primary)", got)
	}
	root, ri2, ei := f.inst(pg.ID), f.inst(r2.ID), f.inst(edge.ID)
	if root.Variables["KEEL_HA_ROLE"] != "replica" || root.Variables["KEEL_HA_REJOIN"] != "reclone" || root.Variables["KEEL_HA_PRIMARY"] != "${{pg-replica-2.KEEL_PRIVATE_DOMAIN}}" {
		t.Errorf("old primary vars: %v", root.Variables)
	}
	if ri2.Variables["KEEL_HA_ROLE"] != "primary" || ei.Variables["KEEL_HA_NODES"] != "${{pg-replica-2.KEEL_PRIVATE_DOMAIN}}:5432" {
		t.Errorf("new primary %v / edge %v", ri2.Variables, ei.Variables)
	}
	// 이전 주 노드가 예전 환경으로 다시 주 노드로 뜨면 다시 배포해서 막는다.
	f.exec.reply = func(c, stdin string) (string, error) {
		switch c {
		case "pg", "pg-replica-2":
			return "f|0/600|1|", nil
		}
		return "t|0/600|0|streaming", nil
	}
	f.live(r2.ID)
	before = f.dispatchedCount()
	f.srv.HAOnce(f.ctx)
	if got := f.dispatchedCount() - before; got != 1 || !strings.Contains(f.cluster(pg.ID).Message, "pg가 주 노드로") {
		t.Errorf("fencing: %d deployments, message %q", got, f.cluster(pg.ID).Message)
	}

	// 원래 서비스가 주 노드가 아니면 되돌릴 수 없다.
	if code, out := f.haCall("DELETE", "ha", "", pg); code != 409 || !strings.Contains(out["error"].(string), "주 노드가 아닙니다") {
		t.Errorf("revert while replica is primary: %d %v", code, out)
	}
	// switchover로 db를 다시 주 노드로: 이전 주 노드(pg-replica-2)를 먼저 멈추고 db를 승격한다.
	f.exec.reply = func(c, stdin string) (string, error) {
		if c == "pg-replica-2" {
			return "", errors.New("stopped")
		}
		return "t|0/600|0|streaming", nil
	}
	f.srv.HAOnce(f.ctx)
	code, out = f.haCall("POST", "switchover", `{"to": "pg"}`, pg)
	if code != 202 {
		t.Fatalf("switchover: %d %v", code, out)
	}
	cl = f.cluster(pg.ID)
	if cl.PrimaryServiceID != pg.ID || cl.Status != store.HAActive {
		t.Fatalf("after switchover: %+v", cl)
	}
	if v := f.inst(r2.ID).Variables; v["KEEL_HA_REJOIN"] != "standby" || v["KEEL_HA_ROLE"] != "replica" {
		t.Errorf("demoted primary vars: %v", v)
	}

	// 되돌리기: 구성원을 지우고(볼륨은 남김) 참조, TCP proxy, root 설정을 되돌린다.
	f.exec.reply = func(c, stdin string) (string, error) { return "f|0/700|2|", nil }
	f.live(pg.ID)
	code, out = f.haCall("DELETE", "ha", "", pg)
	if code != 204 {
		t.Fatalf("revert: %d %v", code, out)
	}
	if _, err := f.st.GetHAClusterForService(f.ctx, pg.ID, f.prod.ID); err == nil {
		t.Error("cluster still exists")
	}
	for _, id := range []string{r1.ID, r2.ID, edge.ID} {
		if _, err := f.st.GetService(f.ctx, id); err == nil {
			t.Errorf("member %s not deleted", id)
		}
	}
	vols, _ := f.st.ListVolumesByEnv(f.ctx, f.prod.ID)
	detached := 0
	for _, v := range vols {
		if v.InstanceID == nil {
			detached++
		}
	}
	if detached != 2 {
		t.Errorf("replica volumes kept: %d", detached)
	}
	root = f.inst(pg.ID)
	if root.StartCommand != nil || root.TCPProxy == nil || root.Variables["KEEL_HA_ROLE"] != "" || root.Variables["POSTGRES_PASSWORD"] == "" {
		t.Errorf("root after revert: start=%v tcp=%v vars=%v", root.StartCommand, root.TCPProxy, root.Variables)
	}
	if _, ok := root.Variables["KEEL_HA_PRIMARY"]; ok {
		t.Error("wiring variable left on root")
	}
	if web := f.inst(f.web.ID); web.Variables["DATABASE_URL"] != "${{pg.DATABASE_URL}}" {
		t.Errorf("web refs after revert: %v", web.Variables)
	}
	// 다시 전환하면 남은 볼륨과 이름이 겹치지 않는다.
	if code, out := f.haCall("POST", "ha", `{"replicas": 1, "edge": 1}`, pg); code != 202 {
		t.Fatalf("convert again: %d %v", code, out)
	}
	r1b := f.svcNamed("pg-replica-1")
	if vols, _ := f.st.ListVolumesByInstance(f.ctx, f.inst(r1b.ID).ID); len(vols) != 1 || vols[0].Name != "pg-replica-1-data-2" {
		t.Errorf("new replica volume: %+v", vols)
	}
}

func TestHARedisConvertAndSwitchover(t *testing.T) {
	f := newHAFixture(t)
	rd := f.dbService("cache", "redis")
	code, out := f.haCall("POST", "ha", `{"replicas": 2}`, rd)
	if code != 202 {
		t.Fatalf("convert: %d %v", code, out)
	}
	root, r1, r2 := f.inst(rd.ID), f.svcNamed("cache-replica-1"), f.svcNamed("cache-replica-2")
	edge := f.svcNamed("cache-ha")
	ei := f.inst(edge.ID)
	if !strings.Contains(*root.StartCommand, "redis-sentinel") || root.Variables["KEEL_HA_QUORUM"] != "2" ||
		strings.Count(root.Variables["KEEL_HA_PEERS"], "KEEL_PRIVATE_DOMAIN") != 3 {
		t.Errorf("root: %v", root.Variables)
	}
	if ei.Replicas != 2 || strings.Count(ei.Variables["KEEL_HA_NODES"], ":6379") != 3 || len(root.PrivatePorts) != 2 {
		t.Errorf("edge replicas=%d vars=%v root ports=%v", ei.Replicas, ei.Variables, root.PrivatePorts)
	}
	for _, id := range []string{rd.ID, r1.ID, r2.ID} {
		f.live(id)
	}
	primary := "cache"
	var mu sync.Mutex
	f.exec.reply = func(c, stdin string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(stdin, "SENTINEL FAILOVER") {
			primary = c
			return "OK", nil
		}
		if strings.Contains(stdin, "CONFIG SET") {
			return "OK", nil
		}
		if c == primary {
			return "# Replication\r\nrole:master\r\nconnected_slaves:2\r\nmaster_repl_offset:100\r\n", nil
		}
		return "role:slave\r\nmaster_host:x\r\nmaster_link_status:up\r\nslave_repl_offset:100\r\n", nil
	}
	f.srv.HAOnce(f.ctx)
	if cl := f.cluster(rd.ID); cl.Status != store.HAActive {
		t.Fatalf("not active: %+v", cl)
	}
	code, out = f.haCall("POST", "switchover", `{"to": "`+r2.ID+`"}`, rd)
	if code != 202 {
		t.Fatalf("switchover: %d %v", code, out)
	}
	if cl := f.cluster(rd.ID); cl.PrimaryServiceID != r2.ID {
		t.Errorf("primary after switchover: %+v", cl)
	}
	if !f.exec.sent("cache-replica-2", "CONFIG SET replica-priority 1") || !f.exec.sent("cache", "CONFIG SET replica-priority 100") ||
		!f.exec.sent("cache-replica-2", "SENTINEL FAILOVER keel") {
		t.Errorf("sentinel commands: %+v", f.exec.calls)
	}
	// Sentinel이 스스로 승격하면 기록만 바꾼다.
	mu.Lock()
	primary = "cache-replica-1"
	mu.Unlock()
	f.srv.HAOnce(f.ctx)
	if cl := f.cluster(rd.ID); cl.PrimaryServiceID != r1.ID || !strings.Contains(cl.Message, "Sentinel") {
		t.Errorf("observed failover: %+v", cl)
	}
	// replica 수는 선택지(2, 4, 6, 8)만
	if code, _ := f.haCall("PATCH", "ha", `{"replicas": 3}`, rd); code != 400 {
		t.Errorf("scale to 3: %d", code)
	}
	code, out = f.haCall("PATCH", "ha", `{"replicas": 4, "auto_failover": false}`, rd)
	if code != 200 {
		t.Fatalf("scale: %d %v", code, out)
	}
	cl := f.cluster(rd.ID)
	if cl.AutoFailover || cl.Saved.Counts["replica"] != 4 {
		t.Errorf("after patch: %+v", cl)
	}
	if v := f.inst(f.svcNamed("cache-replica-4").ID).Variables; v["KEEL_HA_QUORUM"] != "3" || !strings.Contains(v["KEEL_HA_PRIMARY"], "cache-replica-1") {
		t.Errorf("new replica wiring: %v", v)
	}
	if v := f.inst(edge.ID).Variables; strings.Count(v["KEEL_HA_NODES"], ":6379") != 5 {
		t.Errorf("edge after scale: %v", v)
	}
}

func TestHAMongoReplicaSet(t *testing.T) {
	f := newHAFixture(t)
	mg := f.dbService("docs", "mongo")
	code, out := f.haCall("POST", "ha", `{}`, mg)
	if code != 202 {
		t.Fatalf("convert: %d %v", code, out)
	}
	root := f.inst(mg.ID)
	if root.Variables["MONGO_URL"] != "mongodb://${{MONGOUSER}}:${{MONGOPASSWORD}}@${{docs.KEEL_PRIVATE_DOMAIN}}:27017,${{docs-replica-1.KEEL_PRIVATE_DOMAIN}}:27017,${{docs-replica-2.KEEL_PRIVATE_DOMAIN}}:27017/?replicaSet=${{KEEL_HA_REPLSET}}&authSource=admin" ||
		len(root.Variables["KEEL_HA_KEYFILE"]) != 64 {
		t.Errorf("root vars: %v", root.Variables)
	}
	r1 := f.inst(f.svcNamed("docs-replica-1").ID)
	if r1.Variables["KEEL_HA_KEYFILE"] != "${{docs.KEEL_HA_KEYFILE}}" || r1.Variables["MONGO_INITDB_ROOT_USERNAME"] != "" {
		t.Errorf("replica vars: %v", r1.Variables)
	}
	f.live(mg.ID)
	f.exec.reply = func(c, stdin string) (string, error) {
		if strings.Contains(stdin, "rs.initiate") {
			return `{"action":"initiated"}`, nil
		}
		return `{"isWritablePrimary":true}`, nil
	}
	f.srv.HAOnce(f.ctx)
	if !f.exec.sent("docs", `"docs.production.keel.internal:27017","docs-replica-1.production.keel.internal:27017"`) {
		t.Errorf("reconcile hosts: %+v", f.exec.calls)
	}
	// revert는 주 노드인 root만 남기도록 replica set을 줄인다.
	f.exec.reply = func(c, stdin string) (string, error) {
		return `{"isWritablePrimary":true,"setName":"rs0","hosts":["a","b","c"]}`, nil
	}
	if code, out := f.haCall("DELETE", "ha", "", mg); code != 204 {
		t.Fatalf("revert: %d %v", code, out)
	}
	if !f.exec.sent("docs", "force: true") {
		t.Error("standalone reconfig not sent")
	}
	if root := f.inst(mg.ID); !strings.HasPrefix(root.Variables["MONGO_URL"], "mongodb://${{MONGOUSER}}:${{MONGOPASSWORD}}@${{MONGOHOST}}") {
		t.Errorf("MONGO_URL not restored: %v", root.Variables["MONGO_URL"])
	}
}

func TestHACompanionTemplatesHidden(t *testing.T) {
	f := newHAFixture(t)
	w := httptest.NewRecorder()
	f.srv.listTemplates(w, httptest.NewRequest("GET", "/api/templates", nil))
	var list []templateView
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list) == 0 {
		t.Fatalf("list: %v %s", err, w.Body)
	}
	for _, v := range list {
		if strings.HasSuffix(v.Key, "-ha") {
			t.Errorf("companion %s listed", v.Key)
		}
	}
	code, out := f.call(f.srv.deployTemplate, "POST", "/", `{}`, map[string]string{"pid": f.p.ID, "key": "postgres-ha"})
	if code != 400 || !strings.Contains(out["error"].(string), "동반 템플릿") {
		t.Errorf("deploy companion: %d %v", code, out)
	}
}
