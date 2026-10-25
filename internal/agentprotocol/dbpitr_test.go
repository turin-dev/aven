package agentprotocol

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPITRFramesRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	files := map[string]string{"000000010000000000000002": strings.Repeat("w", 1000), "00000002.history": "", "binlog.000003": "bin\nlog\n"}
	order := []string{"000000010000000000000002", "00000002.history", "binlog.000003"}
	for _, n := range order {
		if err := WritePITRFrame(&buf, n, int64(len(files[n])), strings.NewReader(files[n])); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	err := ReadPITRFrames(&buf, func(fr PITRFrame, body io.Reader) error {
		b, err := io.ReadAll(body)
		if string(b) != files[fr.Name] {
			t.Errorf("%s: %q", fr.Name, b)
		}
		got = append(got, fr.Name)
		return err
	})
	if err != nil || strings.Join(got, ",") != strings.Join(order, ",") {
		t.Fatalf("%v %v", got, err)
	}
	// 읽는 쪽이 body를 덜 읽어도 다음 프레임으로 넘어가지 않고 오류가 된다(파일을 빼먹지 않는다).
	buf.Reset()
	WritePITRFrame(&buf, "a", 3, strings.NewReader("abc"))
	if err := ReadPITRFrames(&buf, func(PITRFrame, io.Reader) error { return nil }); err == nil {
		t.Fatal("unconsumed frame accepted")
	}
	for _, bad := range []string{"KEELPITR ../x 1\n", "KEELPITR a -1\n", "NOPE a 1\n", "KEELPITR a 1", "KEELPITR .hidden 1\n"} {
		if _, err := ParsePITRFrameHeader(bad); err == nil {
			t.Errorf("header %q accepted", bad)
		}
	}
	// 잘린 스트림
	if err := ReadPITRFrames(strings.NewReader("KEELPITR a 10\nabc"), func(fr PITRFrame, b io.Reader) error { _, err := io.ReadAll(b); return err }); err == nil {
		t.Fatal("truncated frame accepted")
	}
	if err := WritePITRFrame(io.Discard, "a", 5, strings.NewReader("abc")); err == nil {
		t.Fatal("short body accepted")
	}
}

func TestParseBinlogPosition(t *testing.T) {
	for in, want := range map[string]string{
		"-- CHANGE REPLICATION SOURCE TO SOURCE_LOG_FILE='binlog.000002', SOURCE_LOG_POS=157;": "binlog.000002:157",
		"-- CHANGE MASTER TO MASTER_LOG_FILE='mysql-bin.000010', MASTER_LOG_POS=4;":            "mysql-bin.000010:4",
	} {
		f, p, ok := ParseBinlogPosition([]byte("-- dump\n" + in + "\n"))
		if !ok || f+":"+strconv.FormatUint(p, 10) != want {
			t.Errorf("%q → %s %d %v", in, f, p, ok)
		}
	}
	if _, _, ok := ParseBinlogPosition([]byte("CREATE TABLE x;")); ok {
		t.Error("no position accepted")
	}
}

func TestPITRPaths(t *testing.T) {
	if !PITRDataDirOK("/var/lib/postgresql/data", "/var/lib/postgresql/data/pgdata") {
		t.Fatal("catalog layout rejected")
	}
	for _, c := range [][2]string{
		{"/var/lib/postgresql/data", "/var/lib/postgresql/data"},
		{"/var/lib/postgresql/data", "/var/lib/postgresql/database"},
		{"/data", "/data/keel-pitr/x"},
		{"/data", "/data/a/../../etc"},
		{"/data", "/data/a b"},
		{"/da'ta", "/da'ta/x"},
	} {
		if PITRDataDirOK(c[0], c[1]) {
			t.Errorf("%v accepted", c)
		}
	}
}

func TestPITRToolsKeepSecretsOutOfScripts(t *testing.T) {
	c := DBCreds{User: "root", Password: "s3cr$t'"}
	at := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	tools := []func() (*DBTool, error){
		func() (*DBTool, error) { return DBPITRBaseTool("postgres", "10.0.0.2", 0, c) },
		func() (*DBTool, error) { return DBPITRBaseTool("mysql", "10.0.0.2", 0, c) },
		func() (*DBTool, error) { return DBPITRBinlogTool("10.0.0.2", 0, c, []string{"binlog.000001"}) },
		func() (*DBTool, error) {
			return DBPITRRestoreTool("mysql", "10.0.0.2", 0, c, PITRRestoreOpts{Target: at, BaseFile: "binlog.000001", BasePosition: 4})
		},
		func() (*DBTool, error) {
			return DBPITRRestoreTool("postgres", "", 0, c, PITRRestoreOpts{Target: at, Mount: "/var/lib/postgresql/data", DataDir: "/var/lib/postgresql/data/pgdata"})
		},
	}
	for i, f := range tools {
		tool, err := f()
		if err != nil {
			t.Fatalf("tool %d: %v", i, err)
		}
		if strings.Contains(tool.Script, c.Password) {
			t.Errorf("tool %d puts the password in the script", i)
		}
	}
	if _, err := DBPITRBaseTool("redis", "h", 0, c); err == nil {
		t.Error("redis base accepted")
	}
	if _, err := DBPITRBinlogTool("h", 0, c, []string{"a;rm"}); err == nil {
		t.Error("bad binlog name accepted")
	}
	if tool, _ := DBPITRBaseTool("mysql", "h", 0, c); !tool.Gzip || !strings.Contains(tool.Script, "--source-data=2") {
		t.Error("mysql base must be a gzip'd dump with the binlog position")
	}
}
