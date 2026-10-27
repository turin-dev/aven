package deploy

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agent"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// dbAgentDocker는 원격 노드의 docker를 흉내 낸다(agent.Handler 뒤에서 loopHub로 부른다).
type dbAgentDocker struct {
	agent.Docker
	states map[string]*node.ContainerState
	out    []byte
	stdin  []byte
	spec   node.RunSpec
	sizes  map[string]int64
}

func (d *dbAgentDocker) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		if s := d.states[n]; s != nil {
			out[n] = s
		}
	}
	return out, nil
}

func (d *dbAgentDocker) RunStream(_ context.Context, s node.RunSpec, stdin io.Reader, stdout io.Writer, _ func(string)) (int, error) {
	d.spec = s
	if stdin != nil {
		d.stdin, _ = io.ReadAll(stdin)
	}
	if stdout != nil {
		stdout.Write(d.out)
	}
	return 0, nil
}

func (d *dbAgentDocker) VolumeSizes(context.Context) (map[string]int64, error) { return d.sizes, nil }

func remoteDBFixture(t *testing.T, kind string) (*dbAgentDocker, *Manager, *dbTarget) {
	d := &dbAgentDocker{states: map[string]*node.ContainerState{
		"keel-db-abcdefgh-1": {Name: "keel-db-abcdefgh-1", Running: true, IPs: map[string][]string{"keel-env_abcd1234": {"172.20.0.5"}},
			Volumes: []string{"keel-vol-abcdefgh"},
			Labels:  map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp, node.LabelProject: "prj_abcd", node.LabelEnv: "env_abcd1234", node.LabelService: "svc_abcd"}},
	}}
	hub := &loopHub{h: &agent.Handler{Docker: d, WorkDir: t.TempDir()}}
	m := &Manager{Agents: hub}
	dt := &dbTarget{
		t:    &Target{Project: &store.Project{ID: "prj_abcd"}, Env: &store.Environment{ID: "env_abcd1234", Name: "production"}, Service: &store.Service{ID: "svc_abcd", Name: "db"}},
		kind: kind, image: "postgres:17-alpine", port: DBPort(kind, 0), node: "nod_remote", drv: m.driverForNode("nod_remote"),
		vars: map[string]string{"POSTGRES_PASSWORD": "pw$ecret", "POSTGRES_DB": "app", "REDIS_PASSWORD": "r"},
		host: "db.production.keel.internal",
		cur:  &store.Deployment{NodeID: "nod_remote", Containers: []string{"keel-db-abcdefgh-1"}},
	}
	return d, m, dt
}

func TestRemoteDBBackupRoundTripThroughAgent(t *testing.T) {
	d, m, dt := remoteDBFixture(t, "postgres")
	d.out = bytes.Repeat([]byte("pgdump"), agentprotocol.MaxChunkBytes/3) // 여러 chunk
	tool, err := dumpTool(dt.kind, dt.host, dt.port, dt.vars)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if _, err := m.streamOut(context.Background(), dt, "abcdefgh234", tool, &buf, nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes(), d.out) {
		t.Fatalf("got %d bytes, want %d", buf.Len(), len(d.out))
	}
	// 원격 helper는 agent가 같은 스크립트 표로 만든다: 서버가 정한 host 대신 컨테이너 주소를 쓴다.
	if d.spec.Name != "aven-dbb-abcdefgh234" || d.spec.Env["DBHOST"] != "172.20.0.5" || d.spec.Env["PGPASSWORD"] != "pw$ecret" ||
		d.spec.Env["DBNAME"] != "app" || d.spec.Cmd[1] != tool.Script || d.spec.Image != "postgres:17-alpine" {
		t.Fatalf("spec: %+v", d.spec)
	}
}

func TestRemoteDBRestoreRoundTripThroughAgent(t *testing.T) {
	d, m, dt := remoteDBFixture(t, "postgres")
	tool, _ := restoreTool(dt.kind, dt.host, dt.port, dt.vars)
	data := bytes.Repeat([]byte("x"), agentprotocol.MaxChunkBytes+3)
	if _, err := m.streamIn(context.Background(), dt, "abcdefgh234", tool, nil, bytes.NewReader(data), nil); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(d.stdin, data) || d.spec.Name != "aven-dbr-abcdefgh234" || !strings.Contains(d.spec.Cmd[1], "pg_restore") {
		t.Fatalf("stdin=%d spec=%+v", len(d.stdin), d.spec)
	}
	// redis: 멈춘 DB의 볼륨에 놓는다
	_, m2, rt := remoteDBFixture(t, "redis")
	rd := rt.drv.(*remoteDriver).hub.(*loopHub)
	rdock := rd.h.Docker.(*dbAgentDocker)
	rdock.states["keel-db-abcdefgh-1"].Running = false
	rtool, _ := restoreTool("redis", rt.host, rt.port, rt.vars)
	vol := &store.Volume{DockerName: "keel-vol-abcdefgh", MountPath: "/data"}
	if _, err := m2.streamIn(context.Background(), rt, "abcdefgh234", rtool, vol, strings.NewReader("REDIS0011"), nil); err != nil {
		t.Fatal(err)
	}
	if len(rdock.spec.Mounts) != 1 || rdock.spec.Env["DATADIR"] != "/data" || string(rdock.stdin) != "REDIS0011" {
		t.Fatalf("redis spec: %+v", rdock.spec)
	}
}

func TestRemoteDBNeedsContainerOnNode(t *testing.T) {
	_, m, dt := remoteDBFixture(t, "postgres")
	dt.cur = &store.Deployment{NodeID: "nod_other", Containers: []string{"keel-db-abcdefgh-1"}}
	tool, _ := dumpTool(dt.kind, dt.host, dt.port, dt.vars)
	if _, err := m.streamOut(context.Background(), dt, "abcdefgh234", tool, io.Discard, nil); err == nil || !strings.Contains(err.Error(), "no container") {
		t.Fatalf("err = %v", err)
	}
}

// oldDBHub는 db.backup / db.restore를 모르는 옛 agent처럼 답한다.
type oldDBHub struct{ fakeHub }

func (o *oldDBHub) CallStream(ctx context.Context, n, op string, req, res any, ch func(json.RawMessage)) error {
	switch op {
	case agentprotocol.OpDBBackup:
		return &agentprotocol.RemoteError{Msg: `operation "db.backup" is not allowed`}
	case agentprotocol.OpUploadBegin:
		return &agentprotocol.RemoteError{Msg: `invalid payload: json: unknown field "db"`}
	}
	return o.fakeHub.CallStream(ctx, n, op, req, res, ch)
}

func TestRemoteDBOldAgentError(t *testing.T) {
	r := &remoteDriver{hub: &oldDBHub{}, node: "nod_remote"}
	if err := r.dbBackup(context.Background(), agentprotocol.DBToolRequest{}, io.Discard); !errors.Is(err, ErrAgentNoDBBackup) {
		t.Errorf("backup: %v", err)
	}
	if err := r.dbRestore(context.Background(), agentprotocol.DBToolRequest{}, strings.NewReader("x")); !errors.Is(err, ErrAgentNoDBBackup) {
		t.Errorf("restore: %v", err)
	}
}

func TestRemoteBackupGzipStaysOnServer(t *testing.T) {
	// mysql 덤프는 agent가 원본을 보내고 서버가 gzip으로 압축해 저장한다.
	d, m, dt := remoteDBFixture(t, "mysql")
	d.out = []byte("CREATE TABLE t (id int);")
	dt.vars = map[string]string{"MYSQL_ROOT_PASSWORD": "pw"}
	st := &localStorage{root: t.TempDir()}
	tool, _ := dumpTool("mysql", dt.host, dt.port, dt.vars)
	b := &store.DBBackup{ID: "dbb_abcdefgh234", ObjectKey: "db-production/x.sql.gz"}
	size, err := m.runDump(context.Background(), dt, tool, st, b)
	if err != nil || size == 0 {
		t.Fatal(size, err)
	}
	rc, _ := st.Get(context.Background(), b.ObjectKey)
	defer rc.Close()
	gz, err := gzip.NewReader(rc)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(gz)
	if string(got) != string(d.out) || d.spec.Env["MYSQL_PWD"] != "pw" {
		t.Fatalf("got %q env %v", got, d.spec.Env)
	}
}

func TestRemoteVolumeSizes(t *testing.T) {
	d, m, _ := remoteDBFixture(t, "postgres")
	d.sizes = map[string]int64{"keel-vol-abcdefgh": 1234}
	sizer, ok := m.driverForNode("nod_remote").(interface {
		VolumeSizes(context.Context) (map[string]int64, error)
	})
	if !ok {
		t.Fatal("remote driver does not report volume sizes")
	}
	got, err := sizer.VolumeSizes(context.Background())
	if err != nil || got["keel-vol-abcdefgh"] != 1234 {
		t.Fatalf("%v %v", got, err)
	}
}
