package deploy

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

var pitrT0 = time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)

func min_(m int) time.Time { return pitrT0.Add(time.Duration(m) * time.Minute) }

func pgObjs() []*store.DBPITRObject {
	return []*store.DBPITRObject{
		{ID: "w0", ObjectType: "wal", FileName: "000000010000000000000001", SourceTime: min_(-5)}, // 기준 백업 전
		{ID: "b1", ObjectType: "base", FileName: "b1.tar.gz", StartedAt: min_(0), SourceTime: min_(2)},
		{ID: "w1", ObjectType: "wal", FileName: "000000010000000000000002", SourceTime: min_(3)},
		{ID: "w2", ObjectType: "wal", FileName: "000000010000000000000003", SourceTime: min_(10)},
		{ID: "w3", ObjectType: "wal", FileName: "000000010000000000000004", SourceTime: min_(20)},
		{ID: "w4", ObjectType: "wal", FileName: "000000010000000000000005", SourceTime: min_(30)},
		{ID: "b2", ObjectType: "base", FileName: "b2.tar.gz", StartedAt: min_(25), SourceTime: min_(26)},
	}
}

func ids(os []*store.DBPITRObject) string {
	var s []string
	for _, o := range os {
		s = append(s, o.ID)
	}
	return strings.Join(s, ",")
}

func TestPITRWindow(t *testing.T) {
	w := pitrWindow(pgObjs())
	if w.Earliest == nil || !w.Earliest.Equal(min_(2)) || w.Latest == nil || !w.Latest.Equal(min_(30)) || w.Bases != 2 || w.Logs != 5 {
		t.Fatalf("window: %+v", w)
	}
	// 기준 백업만 있고 그 뒤 로그가 없으면 아직 복원할 수 없다.
	w = pitrWindow([]*store.DBPITRObject{{ObjectType: "base", SourceTime: min_(0)}, {ObjectType: "wal", SourceTime: min_(-1)}})
	if w.Earliest != nil || w.Latest != nil {
		t.Fatalf("base only: %+v", w)
	}
}

func TestPlanPITRPostgres(t *testing.T) {
	objs := pgObjs()
	p, err := planPITR("postgres", objs, min_(9))
	if err != nil {
		t.Fatal(err)
	}
	// b1(목표 이전의 가장 새 기준 백업), 기준 백업 시작 뒤 WAL부터 목표+2분까지, 그리고 그 뒤에 보관된 첫 세그먼트 하나.
	if p.Base.ID != "b1" || ids(p.Logs) != "w1,w2,w3" {
		t.Fatalf("plan at +9m: base %s logs %s", p.Base.ID, ids(p.Logs))
	}
	p, err = planPITR("postgres", objs, min_(5))
	if err != nil || ids(p.Logs) != "w1,w2" {
		t.Fatalf("plan at +5m: %v %v", p, err)
	}
	p, err = planPITR("postgres", objs, min_(27))
	if err != nil || p.Base.ID != "b2" || ids(p.Logs) != "w4" {
		t.Fatalf("plan at +27m: %+v %v", p, err)
	}
	for _, at := range []time.Time{min_(1), min_(31)} {
		if _, err := planPITR("postgres", objs, at); err == nil {
			t.Errorf("plan at %s should fail", at)
		}
	}
}

func TestPlanPITRMySQL(t *testing.T) {
	objs := []*store.DBPITRObject{
		{ID: "b1", ObjectType: "base", FileName: "b1.sql.gz", StartedAt: min_(0), SourceTime: min_(1), BaseFile: "binlog.000009", BasePosition: 157},
		{ID: "l8", ObjectType: "binlog", FileName: "binlog.000008", SourceTime: min_(-1)},
		{ID: "l9", ObjectType: "binlog", FileName: "binlog.000009", SourceTime: min_(2)},
		{ID: "l10", ObjectType: "binlog", FileName: "binlog.000010", SourceTime: min_(3)},
		{ID: "l11", ObjectType: "binlog", FileName: "binlog.000011", SourceTime: min_(4)},
	}
	p, err := planPITR("mysql", objs, min_(2).Add(30*time.Second))
	if err != nil || p.Base.ID != "b1" || ids(p.Logs) != "l9,l10" {
		t.Fatalf("mysql plan: %+v %v", p, err)
	}
	// binlog 번호는 숫자로 비교한다.
	if !binlogLess("binlog.000999", "binlog.1000000") || binlogLess("binlog.000010", "binlog.000009") {
		t.Fatal("binlogLess")
	}
	// 빠진 binlog는 복원을 거부한다.
	gap := []*store.DBPITRObject{objs[0], objs[2], objs[4]}
	if _, err := planPITR("mysql", gap, min_(4)); err == nil || !strings.Contains(err.Error(), "빠져") {
		t.Fatalf("gap: %v", err)
	}
	// 기준 백업의 binlog가 없으면 거부한다.
	if _, err := planPITR("mysql", []*store.DBPITRObject{objs[0], objs[3]}, min_(2)); err == nil {
		t.Fatal("missing base binlog should fail")
	}
}

func TestPlanPITRIgnoresOtherEpochs(t *testing.T) {
	objs := []*store.DBPITRObject{
		{ID: "b0", ObjectType: "base", Epoch: 0, StartedAt: min_(0), SourceTime: min_(1)},
		{ID: "w0", ObjectType: "wal", Epoch: 0, FileName: "000000010000000000000002", SourceTime: min_(5)},
		{ID: "b1", ObjectType: "base", Epoch: 1, StartedAt: min_(10), SourceTime: min_(11)},
		{ID: "w1", ObjectType: "wal", Epoch: 1, FileName: "000000020000000000000003", SourceTime: min_(15)},
	}
	if p, err := planPITR("postgres", objs, min_(3)); err != nil || ids(p.Logs) != "w0" {
		t.Fatalf("epoch 0: %+v %v", p, err)
	}
	// epoch 0의 로그가 끝난 뒤, epoch 1 기준 백업 전 시각은 덮지 못한다.
	if _, err := planPITR("postgres", objs, min_(8)); err == nil {
		t.Fatal("gap between epochs should fail")
	}
	if p, err := planPITR("postgres", objs, min_(12)); err != nil || p.Base.ID != "b1" || ids(p.Logs) != "w1" {
		t.Fatalf("epoch 1: %+v %v", p, err)
	}
}

func TestPlanPITRPrune(t *testing.T) {
	objs := pgObjs()
	// cutoff +27m: b2(+26m)부터 남긴다 → b1과 b2 시작(+25m) 전 WAL을 지운다.
	if got := ids(planPITRPrune("postgres", objs, min_(27))); got != "w0,b1,w1,w2,w3" {
		t.Fatalf("prune: %s", got)
	}
	// cutoff가 첫 기준 백업 전이면 아무것도 지우지 않는다.
	if got := planPITRPrune("postgres", objs, min_(1)); len(got) != 0 {
		t.Fatalf("prune before first base: %s", ids(got))
	}
	my := []*store.DBPITRObject{
		{ID: "b1", ObjectType: "base", SourceTime: min_(1), BaseFile: "binlog.000002"},
		{ID: "l1", ObjectType: "binlog", FileName: "binlog.000001", SourceTime: min_(0)},
		{ID: "l2", ObjectType: "binlog", FileName: "binlog.000002", SourceTime: min_(2)},
		{ID: "old", ObjectType: "binlog", Epoch: -1, FileName: "binlog.000005", SourceTime: min_(0)},
	}
	if got := ids(planPITRPrune("mysql", my, min_(3))); got != "l1,old" {
		t.Fatalf("mysql prune: %s", got)
	}
}

func TestParseBinlogList(t *testing.T) {
	got := parseBinlogList("binlog.000001\t180\tNo\nbinlog.000002\t4567\tNo\n\ngarbage\n")
	if len(got) != 2 || got[1].Name != "binlog.000002" || got[1].Size != 4567 {
		t.Fatalf("%+v", got)
	}
}

func gz(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	w.Write([]byte(s))
	w.Close()
	return b.Bytes()
}

func fakeOpener(data map[string][]byte) pitrOpener {
	return func(_ context.Context, o *store.DBPITRObject) (io.ReadCloser, error) {
		b, ok := data[o.ID]
		if !ok {
			return nil, fmt.Errorf("missing %s", o.ID)
		}
		return io.NopCloser(bytes.NewReader(b)), nil
	}
}

func TestWritePGRestoreTar(t *testing.T) {
	data := map[string][]byte{"b": []byte("BASE"), "w1": gz("wal-1"), "w2": gz("wal-2")}
	plan := &pitrPlan{
		Base: &store.DBPITRObject{ID: "b", FileName: "b.tar.gz", SizeBytes: 4},
		Logs: []*store.DBPITRObject{
			{ID: "w1", FileName: "000000010000000000000002", SizeBytes: int64(len(data["w1"]))},
			{ID: "w2", FileName: "000000010000000000000003", SizeBytes: int64(len(data["w2"]))},
		},
	}
	var buf bytes.Buffer
	if err := writePGRestoreTar(context.Background(), &buf, plan, fakeOpener(data)); err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(&buf)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
		if h.Name == "base.tar.gz" {
			b, _ := io.ReadAll(tr)
			if string(b) != "BASE" {
				t.Fatalf("base content %q", b)
			}
		}
	}
	if strings.Join(names, " ") != "base.tar.gz wal/ wal/000000010000000000000002.gz wal/000000010000000000000003.gz" {
		t.Fatalf("tar entries: %v", names)
	}
	// 기록된 크기와 저장된 파일이 다르면 실패한다(잘린 보관 파일).
	plan.Logs[0].SizeBytes++
	if err := writePGRestoreTar(context.Background(), io.Discard, plan, fakeOpener(data)); err == nil {
		t.Fatal("size mismatch should fail")
	}
}

func TestWriteMySQLRestoreFrames(t *testing.T) {
	data := map[string][]byte{"b": gz("CREATE DATABASE x;"), "l": gz("binlog-bytes")}
	plan := &pitrPlan{
		Base: &store.DBPITRObject{ID: "b", SizeBytes: int64(len(data["b"]))},
		Logs: []*store.DBPITRObject{{ID: "l", FileName: "binlog.000003", SizeBytes: int64(len(data["l"]))}},
	}
	var buf bytes.Buffer
	if err := writeMySQLRestoreFrames(context.Background(), &buf, plan, fakeOpener(data)); err != nil {
		t.Fatal(err)
	}
	var got []string
	err := agentprotocol.ReadPITRFrames(&buf, func(fr agentprotocol.PITRFrame, body io.Reader) error {
		b, err := io.ReadAll(body)
		if int64(len(b)) != fr.Size {
			return fmt.Errorf("short frame %s", fr.Name)
		}
		got = append(got, fr.Name)
		return err
	})
	if err != nil || strings.Join(got, " ") != "base.sql.gz binlog.000003.gz" {
		t.Fatalf("frames %v %v", got, err)
	}
}

func TestHeadSniffer(t *testing.T) {
	var out bytes.Buffer
	h := &headSniffer{w: &out}
	io.WriteString(h, "-- MySQL dump\n-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000004', SOURCE_LOG_POS=1234;\n")
	f, pos, ok := agentprotocol.ParseBinlogPosition(h.head)
	if !ok || f != "binlog.000004" || pos != 1234 || out.Len() == 0 {
		t.Fatalf("%q %d %v", f, pos, ok)
	}
}
