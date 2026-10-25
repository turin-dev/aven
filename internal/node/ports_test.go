package node

import (
	"context"
	"encoding/binary"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// x86(little-endian) 노드에서 찍은 /proc/<pid>/net/tcp, tcp6 그대로다.
const fixtureTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 12877 1 0000000000000000 100 0 0 10 0
   1: 0100007F:18EB 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 52614 1 0000000000000000 100 0 0 10 0
   2: 0300120A:0BB8 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 38259 1 0000000000000000 100 0 0 10 0
   3: 0300120A:1F90 0400120A:C350 01 00000000:00000000 00:00000000 00000000     0        0 1041330 1 0000000000000000 20 4 30 10 -1
   4: 0100007F:0CEA 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1041331 1 0000000000000000 100 0 0 10 0
`

const fixtureTCP6 = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000000000000:10DE 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 26986601 1 0000000000000000 100 0 0 10 0
   1: 00000000000000000000000001000000:0CEA 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 14705101 1 0000000000000000 100 0 0 10 0
   2: B80D012043196B000000000002000000:0019 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 14699043 1 0000000000000000 100 0 0 10 0
   3: 0000000000000000FFFF00000100007F:2382 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 1 1 0000000000000000 100 0 0 10 0
   4: B80D012043196B000000000002000000:01BB B80D012043196B000000000003000000:D431 01 00000000:00000000 00:00000000 00000000     0        0 2 1 0000000000000000 100 0 0 10 0
`

func TestParseProcNetTCP(t *testing.T) {
	ls, err := ParseProcNetTCP(strings.NewReader(fixtureTCP), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want := []Listener{
		{netip.MustParseAddr("0.0.0.0"), 8080},
		{netip.MustParseAddr("127.0.0.1"), 6379},
		{netip.MustParseAddr("10.18.0.3"), 3000},
		{netip.MustParseAddr("127.0.0.1"), 3306},
	}
	if !reflect.DeepEqual(ls, want) {
		t.Fatalf("tcp: %+v", ls)
	}
	ls6, err := ParseProcNetTCP(strings.NewReader(fixtureTCP6), binary.LittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	want6 := []Listener{
		{netip.MustParseAddr("::"), 4318},
		{netip.MustParseAddr("::1"), 3306},
		{netip.MustParseAddr("2001:db8:6b:1943::2"), 25},
		{netip.MustParseAddr("::ffff:127.0.0.1"), 9090},
	}
	if !reflect.DeepEqual(ls6, want6) {
		t.Fatalf("tcp6: %+v", ls6)
	}
	got := SummarizeListeners(append(ls, ls6...))
	if !reflect.DeepEqual(got.Ports, []int{25, 3000, 4318, 8080}) || !reflect.DeepEqual(got.Loopback, []int{3306, 6379, 9090}) {
		t.Fatalf("summary: %+v", got)
	}
}

func TestParseProcNetTCPBadInput(t *testing.T) {
	for _, in := range []string{
		"   0: 0000000:1F90 00000000:0000 0A x\n",
		"   0: 00000000:XYZ 00000000:0000 0A x\n",
		"   0: 00000000 00000000:0000 0A x\n",
	} {
		if _, err := ParseProcNetTCP(strings.NewReader(in), binary.LittleEndian); err == nil {
			t.Errorf("want error for %q", in)
		}
	}
	// LISTEN이 아닌 줄과 짧은 줄은 건너뛴다.
	ls, err := ParseProcNetTCP(strings.NewReader("garbage\n   0: zz 00000000:0000 01\n"), binary.LittleEndian)
	if err != nil || len(ls) != 0 {
		t.Fatalf("%v %v", ls, err)
	}
}

func TestSummarizeLoopbackAndReachableSamePort(t *testing.T) {
	got := SummarizeListeners([]Listener{
		{netip.MustParseAddr("127.0.0.1"), 8080},
		{netip.MustParseAddr("::"), 8080},
	})
	if !reflect.DeepEqual(got.Ports, []int{8080}) || len(got.Loopback) != 0 {
		t.Fatalf("%+v", got)
	}
}

type fakeLister struct {
	managed map[string]map[string]string
	pids    map[string]int
}

func (f *fakeLister) ListManaged(context.Context) (map[string]map[string]string, error) {
	return f.managed, nil
}
func (f *fakeLister) ContainerPIDs(_ context.Context, names []string) (map[string]int, error) {
	out := map[string]int{}
	for _, n := range names {
		if p := f.pids[n]; p > 0 {
			out[n] = p
		}
	}
	return out, nil
}

func writeProc(t *testing.T, root string, pid, tcp, tcp6 string) {
	dir := filepath.Join(root, pid, "net")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "tcp"), []byte(tcp), 0o644)
	if tcp6 != "" {
		os.WriteFile(filepath.Join(dir, "tcp6"), []byte(tcp6), 0o644)
	}
}

func TestInstanceListeningPortsUnion(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("fixtures are little-endian")
	}
	root := t.TempDir()
	hdr := "  sl  local_address rem_address   st\n"
	writeProc(t, root, "100", hdr+"   0: 00000000:1F90 00000000:0000 0A\n", "")
	writeProc(t, root, "200", hdr+"   0: 00000000:0BB8 00000000:0000 0A\n   1: 0100007F:18EB 00000000:0000 0A\n", "")
	f := &fakeLister{
		managed: map[string]map[string]string{
			"keel-web-a":   {LabelInstance: "ins_x", LabelRole: RoleApp},
			"keel-web-b":   {LabelInstance: "ins_x", LabelRole: RoleApp},
			"keel-web-old": {LabelInstance: "ins_x", LabelRole: RoleApp}, // 멈춤(PID 0)
			"keel-tcp-x":   {LabelInstance: "ins_x", LabelRole: RoleTCPProxy},
			"keel-other":   {LabelInstance: "ins_y", LabelRole: RoleApp},
		},
		pids: map[string]int{"keel-web-a": 100, "keel-web-b": 200, "keel-tcp-x": 300, "keel-other": 400},
	}
	got, err := InstanceListeningPorts(context.Background(), f, root, "ins_x")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Ports, []int{3000, 8080}) || !reflect.DeepEqual(got.Loopback, []int{6379}) || got.Containers != 2 {
		t.Fatalf("%+v", got)
	}
	none, err := InstanceListeningPorts(context.Background(), f, root, "ins_none")
	if err != nil || none.Containers != 0 || len(none.Ports) != 0 {
		t.Fatalf("%+v %v", none, err)
	}
}

// 실제 /proc/self로 읽어 본다(어떤 소켓이 있는지는 환경마다 다르니 오류만 본다).
func TestReadListenersSelf(t *testing.T) {
	if _, err := os.Stat("/proc/self/net/tcp"); err != nil {
		t.Skip("no /proc")
	}
	if _, err := ReadListeners("/proc", os.Getpid()); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadListeners("/proc", 0); err == nil {
		t.Fatal("pid 0 must fail")
	}
}
