package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

type streamDocker struct {
	extraDocker
	out     []byte
	exit    int
	stdin   []byte
	gotSpec node.RunSpec
	runs    int
	sizes   map[string]int64
}

func (f *streamDocker) RunStream(_ context.Context, s node.RunSpec, stdin io.Reader, stdout io.Writer, stderr func(string)) (int, error) {
	f.gotSpec = s
	f.runs++
	if stdin != nil {
		f.stdin, _ = io.ReadAll(stdin)
	}
	if stdout != nil {
		stdout.Write(f.out)
	}
	if f.exit != 0 {
		stderr("pg_dump: boom")
	}
	return f.exit, nil
}

func (f *streamDocker) VolumeSizes(context.Context) (map[string]int64, error) {
	if f.sizes == nil {
		return nil, errors.New("df failed")
	}
	return f.sizes, nil
}

const dbNet = "keel-env_abcd1234"

// dbDocker는 이 노드에 postgres 컨테이너(keel-db-abc-1)와 다른 종류의 컨테이너가 있는 가짜 docker다.
func dbDocker() *streamDocker {
	f := &streamDocker{}
	f.states = map[string]*node.ContainerState{
		"keel-db-abc-1": {Name: "keel-db-abc-1", Running: true, IPs: map[string][]string{dbNet: {"fd00::5", "172.20.0.5"}}, Volumes: []string{"keel-vol-abcdefgh"},
			Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp, node.LabelProject: "prj_abcd", node.LabelEnv: "env_abcd1234", node.LabelService: "svc_abcd"}},
		"keel-tcp-abc": {Name: "keel-tcp-abc", Running: true, IPs: map[string][]string{dbNet: {"172.20.0.6"}},
			Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleTCPProxy, node.LabelProject: "prj_abcd", node.LabelEnv: "env_abcd1234", node.LabelService: "svc_abcd"}},
		"keel-other-1": {Name: "keel-other-1", Running: true, IPs: map[string][]string{"keel-env_zzzz9999": {"172.30.0.2"}},
			Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp, node.LabelProject: "prj_abcd", node.LabelEnv: "env_zzzz9999", node.LabelService: "svc_other"}},
	}
	return f
}

func pgReq() agentprotocol.DBToolRequest {
	return agentprotocol.DBToolRequest{Kind: "postgres", Container: "keel-db-abc-1", Network: dbNet, Port: 5432, Image: "postgres:17-alpine",
		Job: "abc123def", User: "postgres", Database: "app", Password: "pw$ecret"}
}

func TestDBBackupStreamsWithFixedScript(t *testing.T) {
	data := bytes.Repeat([]byte("d"), agentprotocol.MaxChunkBytes+10)
	f := dbDocker()
	f.out = data
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	var chunks []agentprotocol.DataChunk
	res, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(pgReq()),
		func(v any) { chunks = append(chunks, v.(agentprotocol.DataChunk)) })
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for _, c := range chunks {
		got = append(got, c.Data...)
	}
	if !bytes.Equal(got, data) || res.(agentprotocol.DBBackupResult).Bytes != int64(len(data)) {
		t.Fatalf("dump: %d bytes in %d chunks", len(got), len(chunks))
	}
	s := f.gotSpec
	// helper는 agent가 정한 이름·역할·라벨·스크립트로, DB 컨테이너의 IPv4 주소에 붙는다. 비밀번호는 환경변수로만.
	if s.Name != "aven-dbb-abc123def" || s.Labels[node.LabelRole] != node.RoleHelper || s.Labels[node.LabelService] != "svc_abcd" ||
		s.Network != dbNet || len(s.Mounts) != 0 || len(s.Publish) != 0 {
		t.Fatalf("spec: %+v", s)
	}
	if s.Env["DBHOST"] != "172.20.0.5" || s.Env["PGPASSWORD"] != "pw$ecret" || !strings.Contains(s.Cmd[1], "pg_dump -Fc") || strings.Contains(s.Cmd[1], "pw$ecret") {
		t.Fatalf("env/script: %v %v", s.Env, s.Cmd)
	}
	// 실패한 덤프는 오류 메시지에 stderr 꼬리를 담는다
	f.exit = 3
	if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(pgReq()), func(any) {}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("want stderr in error, got %v", err)
	}
}

func TestDBBackupRejects(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	bad := map[string]func(*agentprotocol.DBToolRequest){
		"unknown kind":       func(r *agentprotocol.DBToolRequest) { r.Kind = "sqlite" },
		"missing container":  func(r *agentprotocol.DBToolRequest) { r.Container = "keel-nope-1" },
		"foreign container":  func(r *agentprotocol.DBToolRequest) { r.Container = "dokploy-postgres" },
		"tcp proxy target":   func(r *agentprotocol.DBToolRequest) { r.Container = "keel-tcp-abc" },
		"other env network":  func(r *agentprotocol.DBToolRequest) { r.Container = "keel-other-1" },
		"foreign network":    func(r *agentprotocol.DBToolRequest) { r.Network = "bridge" },
		"bad port":           func(r *agentprotocol.DBToolRequest) { r.Port = 0 },
		"bad image":          func(r *agentprotocol.DBToolRequest) { r.Image = "-v /:/host" },
		"bad job":            func(r *agentprotocol.DBToolRequest) { r.Job = "../x" },
		"NUL password":       func(r *agentprotocol.DBToolRequest) { r.Password = "a\x00b" },
		"volume on dump":     func(r *agentprotocol.DBToolRequest) { r.Volume, r.MountPath = "keel-vol-abcdefgh", "/data" },
		"mongo without user": func(r *agentprotocol.DBToolRequest) { r.Kind, r.User = "mongo", "" },
	}
	for name, mod := range bad {
		r := pgReq()
		mod(&r)
		if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(r), func(any) {}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// 정지된 DB는 덤프하지 않는다
	f.states["keel-db-abc-1"].Running = false
	if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(pgReq()), func(any) {}); err == nil {
		t.Error("stopped database dumped")
	}
	// 서버가 스크립트/스펙을 보내는 필드는 없다(strict decode)
	if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, []byte(`{"kind":"postgres","container":"keel-db-abc-1","network":"`+dbNet+`","port":5432,"image":"postgres:17","job":"abcdef12","script":"rm -rf /"}`), func(any) {}); err == nil {
		t.Error("unknown field accepted")
	}
	if f.runs != 0 {
		t.Fatalf("helper ran %d times for rejected requests", f.runs)
	}
}

func uploadAll(t *testing.T, h *Handler, begin agentprotocol.UploadBeginRequest, data []byte) error {
	t.Helper()
	r, err := handle(t, h, agentprotocol.OpUploadBegin, begin)
	if err != nil {
		return err
	}
	id := r.(*agentprotocol.UploadBeginResult).UploadID
	for seq, off := 0, 0; off < len(data); seq++ {
		n := min(agentprotocol.MaxChunkBytes, len(data)-off)
		if _, err := handle(t, h, agentprotocol.OpUploadChunk, agentprotocol.UploadChunkRequest{UploadID: id, Seq: seq, Data: data[off : off+n]}); err != nil {
			return err
		}
		off += n
	}
	_, err = handle(t, h, agentprotocol.OpUploadFinish, agentprotocol.UploadFinishRequest{UploadID: id, Bytes: int64(len(data))})
	return err
}

func TestDBRestoreUploadFeedsStdin(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	data := bytes.Repeat([]byte("r"), agentprotocol.MaxChunkBytes+7)
	req := pgReq()
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, data); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f.stdin, data) || f.gotSpec.Name != "aven-dbr-abc123def" || !strings.Contains(f.gotSpec.Cmd[1], "pg_restore --clean") {
		t.Fatalf("stdin %d bytes, spec %q %v", len(f.stdin), f.gotSpec.Name, f.gotSpec.Cmd)
	}
	// 대상이 없거나 잘못되면 업로드를 시작하지 못한다
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool}); err == nil {
		t.Error("db.restore without target accepted")
	}
	bad := pgReq()
	bad.Job = "x"
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &bad}); err == nil {
		t.Error("db.restore with bad job accepted")
	}
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadSource, DB: &req}); err == nil {
		t.Error("source upload with db target accepted")
	}
	// 실패 종료 코드는 finish 오류가 된다
	f.exit = 2
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, []byte("x")); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("want restore failure, got %v", err)
	}
}

func TestDBRestoreRedisNeedsStoppedVolume(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	req := agentprotocol.DBToolRequest{Kind: "redis", Container: "keel-db-abc-1", Network: dbNet, Port: 6379, Image: "redis:7",
		Job: "abc123def", Password: "x", Volume: "keel-vol-abcdefgh", MountPath: "/data"}
	// 아직 돌고 있으면 거부
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, []byte("rdb")); err == nil {
		t.Fatal("rdb restore into a running redis accepted")
	}
	f.states["keel-db-abc-1"].Running = false
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, []byte("rdb")); err != nil {
		t.Fatal(err)
	}
	s := f.gotSpec
	if len(s.Mounts) != 1 || s.Mounts[0].Volume != "keel-vol-abcdefgh" || s.Env["DATADIR"] != "/data" || s.Env["DBHOST"] != "" || !strings.Contains(s.Cmd[1], "dump.rdb") {
		t.Fatalf("spec: %+v", s)
	}
	// DB 컨테이너가 마운트하지 않은 볼륨은 쓰지 않는다
	other := req
	other.Volume = "keel-vol-zzzzzzzz"
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &other}, []byte("rdb")); err == nil {
		t.Error("foreign volume accepted")
	}
	other = req
	other.MountPath = "/"
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &other}); err == nil {
		t.Error("root mount accepted")
	}
}

func TestVolumeSizesOnlyKeelVolumes(t *testing.T) {
	f := dbDocker()
	f.sizes = map[string]int64{"keel-vol-abcdefgh": 42, "other-volume": 7}
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	res, err := handle(t, h, agentprotocol.OpVolumeSizes, struct{}{})
	if err != nil {
		t.Fatal(err)
	}
	got := res.(agentprotocol.VolumeSizesResult).Sizes
	if len(got) != 1 || got["keel-vol-abcdefgh"] != 42 {
		t.Fatalf("sizes: %v", got)
	}
	if _, err := h.Handle(context.Background(), agentprotocol.OpVolumeSizes, []byte(`{"all":true}`), func(any) {}); err == nil {
		t.Error("unknown field accepted")
	}
	f.sizes = nil
	if _, err := handle(t, h, agentprotocol.OpVolumeSizes, struct{}{}); err == nil {
		t.Error("df failure not reported")
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
