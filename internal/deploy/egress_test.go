package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// egressHub는 egress.status/egress.ensure만 흉내 내는 hub다. old에 든 노드는 옛 agent처럼 작업을 모른다고 답한다.
type egressHub struct {
	mu       sync.Mutex
	old      map[string]bool
	offline  map[string]bool
	ensures  map[string][][]string // node → 받은 blocked 목록들
	statuses int
}

func (h *egressHub) Online(n string) bool { return !h.offline[n] }
func (h *egressHub) Reply(context.Context, string, agentprotocol.Message) error {
	return nil
}
func (h *egressHub) CallStream(_ context.Context, nodeID string, op string, req, res any, _ func(json.RawMessage)) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.old[nodeID] || (op != agentprotocol.OpEgressStatus && op != agentprotocol.OpEgressEnsure) {
		return &agentprotocol.RemoteError{Msg: fmt.Sprintf("operation %q is not allowed", op)}
	}
	var out any
	switch op {
	case agentprotocol.OpEgressStatus:
		h.statuses++
		yes := true
		out = node.EgressStatus{IPv6: &yes, Enforce: true}
	case agentprotocol.OpEgressEnsure:
		r := req.(agentprotocol.EgressEnsureRequest)
		if h.ensures == nil {
			h.ensures = map[string][][]string{}
		}
		h.ensures[nodeID] = append(h.ensures[nodeID], r.Blocked)
		out = node.EgressEnsureResult{Supported: true, Rules: len(r.Blocked)}
	}
	b, _ := json.Marshal(out)
	return json.Unmarshal(b, res)
}

// egressLocal은 로컬 노드 드라이버 대역이다(egress 작업만 쓴다).
type egressLocal struct {
	Driver
	calls [][]string
}

func (l *egressLocal) EgressStatus(context.Context, string) (*node.EgressStatus, error) {
	return &node.EgressStatus{Enforce: false, EnforceReason: "no ip6tables"}, nil
}
func (l *egressLocal) EgressEnsure(_ context.Context, blocked []string) (*node.EgressEnsureResult, error) {
	l.calls = append(l.calls, blocked)
	return &node.EgressEnsureResult{Supported: true, Rules: len(blocked)}, nil
}

func TestEgressOldAgent(t *testing.T) {
	ctx := context.Background()
	h := &egressHub{old: map[string]bool{"node_remote": true}}
	m := &Manager{Agents: h}
	if _, err := m.EgressStatus(ctx, "node_remote", "env_abcd"); !errors.Is(err, ErrEgressAgentUnsupported) {
		t.Fatalf("status: %v", err)
	}
	if _, err := m.driverForNode("node_remote").(egressDriver).EgressEnsure(ctx, nil); !errors.Is(err, ErrEgressAgentUnsupported) {
		t.Fatalf("ensure: %v", err)
	}
	if !IsEgressUnsupported(ErrEgressAgentUnsupported) || !IsEgressUnsupported(&agentprotocol.RemoteError{Msg: node.ErrEgressUnsupported.Error() + ": x"}) ||
		IsEgressUnsupported(errors.New("boom")) {
		t.Fatal("IsEgressUnsupported")
	}
	// 새 agent: 결과를 30초 캐시한다
	st, err := m.EgressStatus(ctx, "node_us", "env_abcd")
	if err != nil || st.IPv6 == nil || !*st.IPv6 || !st.Enforce {
		t.Fatalf("%+v %v", st, err)
	}
	m.EgressStatus(ctx, "node_us", "env_abcd")
	if h.statuses != 1 {
		t.Fatalf("status not cached: %d calls", h.statuses)
	}
	h.offline = map[string]bool{"node_eu": true}
	if _, err := m.EgressStatus(ctx, "node_eu", "env_abcd"); !errors.Is(err, agentprotocol.ErrOffline) {
		t.Fatalf("offline: %v", err)
	}
	if _, err := (&Manager{}).EgressStatus(ctx, store.LocalNodeID, "env_abcd"); err == nil {
		t.Fatal("want error without docker")
	}
}

func TestSyncEgress(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mkNode := func(name string) *store.Node {
		st.CreateJoinToken(ctx, "h-"+name, name, time.Now().Add(time.Hour))
		n, err := st.ConsumeJoinToken(ctx, "h-"+name, "")
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	nNew, nOld := mkNode("us"), mkNode("remote")
	h := &egressHub{old: map[string]bool{nOld.ID: true}}
	local := &egressLocal{}
	m := &Manager{Store: st, Agents: h, localDrv: local}

	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	off := false
	place := func(name, nodeID string, outbound *bool) *store.Instance {
		svc := &store.Service{ProjectID: p.ID, Name: name, SourceType: "image"}
		if err := st.CreateService(ctx, svc); err != nil {
			t.Fatal(err)
		}
		in := &store.Instance{ServiceID: svc.ID, EnvironmentID: env.ID, NodeID: nodeID}
		in.OutboundIPv6 = outbound
		if err := st.CreateInstance(ctx, in); err != nil {
			t.Fatal(err)
		}
		return in
	}
	blockedUS := place("web", nNew.ID, &off)
	place("api", nNew.ID, nil)          // 기본값(켜짐)
	place("db", store.LocalNodeID, nil) // 로컬은 막을 것이 없다
	place("cache", nOld.ID, nil)        // 옛 agent 노드도 막을 것이 없다

	// 1. 막을 인스턴스는 그 노드에만 간다. 로컬은 빈 목록으로 한 번 확인하고, 옛 agent는 막을 것이 없으면 오류가 아니다.
	if err := m.SyncEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if got := h.ensures[nNew.ID]; len(got) != 1 || !reflect.DeepEqual(got[0], []string{blockedUS.ID}) {
		t.Fatalf("us ensures %v", got)
	}
	if len(local.calls) != 1 || len(local.calls[0]) != 0 {
		t.Fatalf("local calls %v", local.calls)
	}
	// 2. 다시 맞추면 로컬은 깨끗하다고 확인됐으므로 묻지 않고, 막을 것이 있는 노드는 매번 맞춘다(주소가 바뀌었을 수 있다).
	if err := m.SyncEgress(ctx); err != nil {
		t.Fatal(err)
	}
	if len(local.calls) != 1 || len(h.ensures[nNew.ID]) != 2 {
		t.Fatalf("second sync: local %v us %v", local.calls, h.ensures[nNew.ID])
	}
	// 3. agent가 다시 연결되면 캐시와 상관없이 맞춘다.
	if err := m.SyncEgressNode(ctx, store.LocalNodeID); err != nil || len(local.calls) != 2 {
		t.Fatalf("reconnect: %v %v", err, local.calls)
	}
	// 4. 옛 agent 노드에 막을 인스턴스가 생기면 오류로 알린다(토글은 API가 미리 거부한다).
	place("legacy", nOld.ID, &off)
	err = m.SyncEgress(ctx)
	if !IsEgressUnsupported(err) {
		t.Fatalf("want unsupported for old agent, got %v", err)
	}
	// 5. 토글을 다시 켜면 그 노드에는 빈 목록이 가서 규칙이 지워진다.
	blockedUS.OutboundIPv6 = nil
	if err := st.UpdateInstanceConfig(ctx, blockedUS); err != nil {
		t.Fatal(err)
	}
	m.SyncEgress(ctx)
	got := h.ensures[nNew.ID]
	if last := got[len(got)-1]; len(last) != 0 {
		t.Fatalf("us after re-enable: %v", got)
	}
	// 오프라인 노드는 건너뛴다.
	h.offline = map[string]bool{nNew.ID: true}
	n := len(h.ensures[nNew.ID])
	m.SyncEgress(ctx)
	if len(h.ensures[nNew.ID]) != n {
		t.Fatal("offline node was called")
	}
}
