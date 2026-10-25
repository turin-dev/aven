package agent

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

type portsDocker struct {
	Docker
	managed map[string]map[string]string
	pids    map[string]int
}

func (p *portsDocker) ListManaged(context.Context) (map[string]map[string]string, error) {
	return p.managed, nil
}
func (p *portsDocker) ContainerPIDs(_ context.Context, names []string) (map[string]int, error) {
	out := map[string]int{}
	for _, n := range names {
		if v := p.pids[n]; v > 0 {
			out[n] = v
		}
	}
	return out, nil
}

func TestPortsListeningOp(t *testing.T) {
	if binary.NativeEndian.Uint16([]byte{1, 0}) != 1 {
		t.Skip("fixture is little-endian")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "42", "net")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "tcp"), []byte("  sl  local_address rem_address   st\n"+
		"   0: 00000000:0BB8 00000000:0000 0A\n   1: 0100007F:1F90 00000000:0000 0A\n"), 0o644)
	old := procRoot
	procRoot = root
	t.Cleanup(func() { procRoot = old })

	h := &Handler{Docker: &portsDocker{
		managed: map[string]map[string]string{"keel-web-1": {node.LabelInstance: "ins_abcd", node.LabelRole: node.RoleApp}},
		pids:    map[string]int{"keel-web-1": 42},
	}}
	res, err := h.Handle(context.Background(), agentprotocol.OpPortsListening, []byte(`{"instance_id":"ins_abcd"}`), nil)
	if err != nil {
		t.Fatal(err)
	}
	lp := res.(*agentprotocol.PortsListeningResult)
	if !reflect.DeepEqual(lp.Ports, []int{3000}) || !reflect.DeepEqual(lp.Loopback, []int{8080}) || lp.Containers != 1 {
		t.Fatalf("%+v", lp)
	}
	// 잘못된 instance id, 모르는 필드는 거부한다.
	for _, p := range []string{`{"instance_id":"../etc"}`, `{"instance_id":"ins_abcd","pid":1}`} {
		if _, err := h.Handle(context.Background(), agentprotocol.OpPortsListening, []byte(p), nil); err == nil {
			t.Errorf("want error for %s", p)
		}
	}
	// PID를 알 수 없는 docker 드라이버는 오류다.
	h2 := &Handler{Docker: &fakeDocker{}}
	if _, err := h2.Handle(context.Background(), agentprotocol.OpPortsListening, []byte(`{"instance_id":"ins_abcd"}`), nil); err == nil ||
		!strings.Contains(err.Error(), "not supported") {
		t.Fatalf("%v", err)
	}
}
