package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
)

func TestPITRWALMountsDataVolumeReadOnly(t *testing.T) {
	f := dbDocker()
	f.out = []byte("KEELPITR 000000010000000000000002 0\n")
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	r := pgReq()
	r.Action, r.Volume, r.MountPath = agentprotocol.PITRWAL, "keel-vol-abcdefgh", "/var/lib/postgresql/data"
	if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(r), func(any) {}); err != nil {
		t.Fatal(err)
	}
	s := f.gotSpec
	if len(s.Mounts) != 1 || !s.Mounts[0].ReadOnly || s.Mounts[0].Volume != "keel-vol-abcdefgh" ||
		s.Env["PITR_DIR"] != "/var/lib/postgresql/data/aven-pitr/wal" || !strings.Contains(s.Cmd[1], "AVENPITR") || !strings.Contains(s.Cmd[1], "keel-pitr/wal") {
		t.Fatalf("spec: %+v", s)
	}
}

func TestPITRBaseAndBinlogUseFixedScripts(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	r := pgReq()
	r.Action = agentprotocol.PITRBase
	if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(r), func(any) {}); err != nil {
		t.Fatal(err)
	}
	if s := f.gotSpec; !strings.Contains(s.Cmd[1], "pg_basebackup") || s.Env["DBHOST"] != "172.20.0.5" || strings.Contains(s.Cmd[1], "pw$ecret") || len(s.Mounts) != 0 {
		t.Fatalf("base spec: %+v", s)
	}
	m := pgReq()
	m.Kind, m.Port, m.User, m.Action, m.Files = "mysql", 3306, "root", agentprotocol.PITRBinlog, []string{"binlog.000003", "binlog.000004"}
	if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(m), func(any) {}); err != nil {
		t.Fatal(err)
	}
	if s := f.gotSpec; s.Env["PITR_FILES"] != "binlog.000003 binlog.000004" || !strings.Contains(s.Cmd[1], "--read-from-remote-server") || s.Env["MYSQL_PWD"] != "pw$ecret" {
		t.Fatalf("binlog spec: %+v", s)
	}
}

func TestPITRRejects(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}}
	bad := map[string]func(*agentprotocol.DBToolRequest){
		"unknown action":     func(r *agentprotocol.DBToolRequest) { r.Action = "pitr-shell" },
		"redis base":         func(r *agentprotocol.DBToolRequest) { r.Kind, r.Action = "redis", agentprotocol.PITRBase },
		"mariadb base":       func(r *agentprotocol.DBToolRequest) { r.Kind, r.Action = "mariadb", agentprotocol.PITRBase },
		"wal without volume": func(r *agentprotocol.DBToolRequest) { r.Action = agentprotocol.PITRWAL },
		"wal foreign volume": func(r *agentprotocol.DBToolRequest) {
			r.Action, r.Volume, r.MountPath = agentprotocol.PITRWAL, "keel-vol-zzzzzzzz", "/data"
		},
		"wal dotdot": func(r *agentprotocol.DBToolRequest) {
			r.Action, r.Volume, r.MountPath = agentprotocol.PITRWAL, "keel-vol-abcdefgh", "/data/../etc"
		},
		"wal quote in path": func(r *agentprotocol.DBToolRequest) {
			r.Action, r.Volume, r.MountPath = agentprotocol.PITRWAL, "keel-vol-abcdefgh", "/data'x"
		},
		"binlog on postgres": func(r *agentprotocol.DBToolRequest) {
			r.Action, r.Files = agentprotocol.PITRBinlog, []string{"binlog.000001"}
		},
		"binlog bad name": func(r *agentprotocol.DBToolRequest) {
			r.Kind, r.Action, r.Files = "mysql", agentprotocol.PITRBinlog, []string{"../x"}
		},
		"binlog no files": func(r *agentprotocol.DBToolRequest) { r.Kind, r.Action = "mysql", agentprotocol.PITRBinlog },
		"restore as backup": func(r *agentprotocol.DBToolRequest) {
			r.Action, r.TargetTime = agentprotocol.PITRRestore, "2026-10-18T10:00:00Z"
		},
		"pitr fields no action": func(r *agentprotocol.DBToolRequest) { r.TargetTime = "2026-10-18T10:00:00Z" },
		"files on base":         func(r *agentprotocol.DBToolRequest) { r.Action, r.Files = agentprotocol.PITRBase, []string{"x"} },
	}
	for name, mod := range bad {
		r := pgReq()
		mod(&r)
		if _, err := h.Handle(context.Background(), agentprotocol.OpDBBackup, mustJSON(r), func(any) {}); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	if f.runs != 0 {
		t.Fatalf("helper ran %d times for rejected requests", f.runs)
	}
}

func TestPITRRestorePostgresNeedsStoppedVolume(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	req := pgReq()
	req.Action, req.TargetTime = agentprotocol.PITRRestore, "2026-10-18T10:00:00Z"
	req.Volume, req.MountPath, req.DataDir = "keel-vol-abcdefgh", "/var/lib/postgresql/data", "/var/lib/postgresql/data/pgdata"
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, []byte("tar")); err == nil {
		t.Fatal("restore into a running postgres accepted")
	}
	f.states["keel-db-abc-1"].Running = false
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, []byte("tar")); err != nil {
		t.Fatal(err)
	}
	s := f.gotSpec
	if s.Name != "aven-dbr-abc123def" || len(s.Mounts) != 1 || s.Mounts[0].ReadOnly || s.Env["PITR_TARGET"] != "2026-10-18 10:00:00+00" ||
		s.Env["PITR_ARCHIVE"] != "off" || !strings.Contains(s.Cmd[1], "recovery.signal") || string(f.stdin) != "tar" {
		t.Fatalf("spec: %+v", s)
	}
	// PGDATA는 볼륨 하위여야 한다(볼륨 루트 = PGDATA면 archive 디렉터리를 둘 곳이 없다).
	bad := req
	bad.DataDir = req.MountPath
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &bad}); err == nil {
		t.Error("data_dir = mount accepted")
	}
	bad.DataDir = "/etc/pgdata"
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &bad}); err == nil {
		t.Error("data_dir outside the volume accepted")
	}
}

func TestPITRRestoreMySQLRunsAgainstLiveDB(t *testing.T) {
	f := dbDocker()
	h := &Handler{Docker: f, Proxy: &fakeProxy{}, WorkDir: t.TempDir()}
	req := pgReq()
	req.Kind, req.Port, req.User = "mysql", 3306, "root"
	req.Action, req.TargetTime, req.BaseFile, req.BasePosition = agentprotocol.PITRRestore, "2026-10-18T10:00:00Z", "binlog.000002", 157
	if err := uploadAll(t, h, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}, []byte("frames")); err != nil {
		t.Fatal(err)
	}
	s := f.gotSpec
	if len(s.Mounts) != 0 || s.Env["DBHOST"] != "172.20.0.5" || s.Env["PITR_TARGET"] != "2026-10-18 10:00:00" || s.Env["PITR_BASE_POSITION"] != "157" ||
		!strings.Contains(s.Cmd[1], "--stop-datetime") {
		t.Fatalf("spec: %+v", s)
	}
	req.BasePosition = 0
	if _, err := handle(t, h, agentprotocol.OpUploadBegin, agentprotocol.UploadBeginRequest{Kind: agentprotocol.UploadDBTool, DB: &req}); err == nil {
		t.Error("restore without base position accepted")
	}
}
