package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// portsHub는 ports.listening만 흉내 내는 hub다. old면 옛 agent처럼 작업을 모른다고 답한다.
type portsHub struct {
	old    bool
	online bool
	req    agentprotocol.PortsListeningRequest
}

func (h *portsHub) Online(string) bool { return h.online }
func (h *portsHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *portsHub) CallStream(_ context.Context, _ string, op string, req, res any, _ func(json.RawMessage)) error {
	if h.old || op != agentprotocol.OpPortsListening {
		// agent.Handle의 모르는 작업 응답과 같은 문장이다.
		return &agentprotocol.RemoteError{Msg: fmt.Sprintf("operation %q is not allowed", op)}
	}
	h.req = req.(agentprotocol.PortsListeningRequest)
	b, _ := json.Marshal(node.ListeningPorts{Ports: []int{3000}, Loopback: []int{9229}, Containers: 2})
	return json.Unmarshal(b, res)
}

func TestListeningPortsRemote(t *testing.T) {
	h := &portsHub{online: true}
	m := &Manager{Agents: h}
	lp, err := m.ListeningPorts(context.Background(), "node_us", "ins_abcd")
	if err != nil {
		t.Fatal(err)
	}
	if h.req.InstanceID != "ins_abcd" || !reflect.DeepEqual(lp.Ports, []int{3000}) || !reflect.DeepEqual(lp.Loopback, []int{9229}) || lp.Containers != 2 {
		t.Fatalf("%+v %+v", h.req, lp)
	}
}

func TestListeningPortsOldAgent(t *testing.T) {
	m := &Manager{Agents: &portsHub{online: true, old: true}}
	_, err := m.ListeningPorts(context.Background(), "node_remote", "ins_abcd")
	if !errors.Is(err, ErrPortDetectionUnsupported) {
		t.Fatalf("want ErrPortDetectionUnsupported, got %v", err)
	}
	// 다른 오류는 그대로 둔다.
	if isUnknownOp(&agentprotocol.RemoteError{Msg: "boom"}, agentprotocol.OpPortsListening) {
		t.Fatal("boom is not unknown op")
	}
}

func TestListeningPortsOffline(t *testing.T) {
	m := &Manager{Agents: &portsHub{online: false}}
	if _, err := m.ListeningPorts(context.Background(), "node_us", "ins_abcd"); !errors.Is(err, agentprotocol.ErrOffline) {
		t.Fatalf("got %v", err)
	}
	// 로컬 docker가 없으면 패닉 없이 오류다.
	if _, err := (&Manager{}).ListeningPorts(context.Background(), store.LocalNodeID, "ins_abcd"); err == nil {
		t.Fatal("want error without docker")
	}
	if got := (&Manager{Agents: &portsHub{}}).ImageExposedPortsOn(context.Background(), "node_us", "img"); got != nil {
		t.Fatalf("offline expose: %v", got)
	}
}

func TestRoutePortMatchesProxy(t *testing.T) {
	p := 3000
	d := &store.Deployment{Env: map[string]string{"PORT": "5000"}}
	if got := RoutePort(d, nil); got != 5000 {
		t.Fatalf("env PORT: %d", got)
	}
	if got := RoutePort(d, &p); got != 3000 {
		t.Fatalf("instance port: %d", got)
	}
	d.Spec.Port = &p
	if got := RoutePort(&store.Deployment{Spec: d.Spec}, nil); got != 3000 {
		t.Fatalf("spec port: %d", got)
	}
}
