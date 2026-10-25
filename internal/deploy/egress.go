package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// 아웃바운드 IPv6(P8): 노드의 IPv6 egress 상태(egress.status)와 서비스별 차단(egress.ensure).
// 차단 규칙은 노드마다 "그 노드에 컨테이너가 있을 수 있는 outbound_ipv6=false 인스턴스" 전체를 보내 맞춘다(idempotent).
// 맞추는 때: 토글 변경(API), 배포 완료와 replica 변경(syncEgressAsync), agent 재연결(SyncEgressNode), 그리고 EgressLoop
// (컨테이너 재시작으로 주소가 바뀐 경우). 차단할 것이 없고 aven 규칙도 없다고 확인된 노드는 루프에서 다시 묻지 않는다.

// ErrEgressAgentUnsupported는 원격 노드의 agent가 egress.*를 모를 때다(옛 agent).
var ErrEgressAgentUnsupported = errors.New("이 노드의 aven-agent가 아웃바운드 IPv6 제어(egress.status/egress.ensure)를 지원하지 않습니다. agent를 업그레이드하세요")

// egressDriver는 노드의 egress 작업이다. node.Docker(로컬)와 remoteDriver가 구현한다.
type egressDriver interface {
	EgressStatus(ctx context.Context, envID string) (*node.EgressStatus, error)
	EgressEnsure(ctx context.Context, blocked []string) (*node.EgressEnsureResult, error)
}

var _ egressDriver = (*node.Docker)(nil)
var _ egressDriver = (*remoteDriver)(nil)

// EgressStatus는 agent의 egress.status다. 옛 agent는 ErrEgressAgentUnsupported다.
func (r *remoteDriver) EgressStatus(ctx context.Context, envID string) (*node.EgressStatus, error) {
	var res agentprotocol.EgressStatusResult
	if err := r.call(ctx, agentprotocol.OpEgressStatus, agentprotocol.EgressStatusRequest{EnvID: envID}, &res); err != nil {
		if isUnknownOp(err, agentprotocol.OpEgressStatus) {
			return nil, ErrEgressAgentUnsupported
		}
		return nil, err
	}
	return &res, nil
}

// EgressEnsure는 agent의 egress.ensure다. 옛 agent는 ErrEgressAgentUnsupported다.
func (r *remoteDriver) EgressEnsure(ctx context.Context, blocked []string) (*node.EgressEnsureResult, error) {
	var res agentprotocol.EgressEnsureResult
	if blocked == nil {
		blocked = []string{}
	}
	if err := r.call(ctx, agentprotocol.OpEgressEnsure, agentprotocol.EgressEnsureRequest{Blocked: blocked}, &res); err != nil {
		if isUnknownOp(err, agentprotocol.OpEgressEnsure) {
			return nil, ErrEgressAgentUnsupported
		}
		return nil, err
	}
	return &res, nil
}

// IsEgressUnsupported는 노드가 차단 규칙을 쓸 수 없다는 오류인지다(로컬은 node.ErrEgressUnsupported, 원격은 그 문장을 담은 오류).
func IsEgressUnsupported(err error) bool {
	return err != nil && (errors.Is(err, node.ErrEgressUnsupported) || errors.Is(err, ErrEgressAgentUnsupported) ||
		strings.Contains(err.Error(), node.ErrEgressUnsupported.Error()))
}

const (
	egressStatusTTL     = 30 * time.Second
	egressOldAgentRetry = 10 * time.Minute
	egressCallTimeout   = 20 * time.Second
)

type egressCached struct {
	st  *node.EgressStatus
	err error
	at  time.Time
}

type egressNode struct {
	clean      bool      // 차단할 것이 없고 aven 규칙도 없다고 확인됨(루프가 다시 묻지 않는다)
	oldAgentAt time.Time // egress.*를 모르는 agent라고 확인한 시각
	lastErr    string    // 같은 오류를 반복해서 기록하지 않는다
}

// egressState는 Manager의 egress 상태다(manager.go의 egressSt).
type egressState struct {
	syncMu sync.Mutex // ensure는 한 번에 하나(ip6tables 경합으로 규칙이 겹치지 않게)
	mu     sync.Mutex
	nodes  map[string]*egressNode
	status map[string]egressCached // nodeID|envID
}

func (s *egressState) node(id string) *egressNode {
	if s.nodes == nil {
		s.nodes = map[string]*egressNode{}
	}
	n := s.nodes[id]
	if n == nil {
		n = &egressNode{}
		s.nodes[id] = n
	}
	return n
}

// egressDriverFor는 노드의 egress 드라이버다. 로컬 docker가 없거나 드라이버가 egress를 모르면 ErrEgressAgentUnsupported다.
func (m *Manager) egressDriverFor(nodeID string) (egressDriver, error) {
	if isLocal(nodeID) && m.localDrv == nil && m.Docker == nil {
		return nil, errors.New("docker is not configured")
	}
	if !m.nodeOnline(nodeID) {
		return nil, fmt.Errorf("node %s: %w", nodeID, agentprotocol.ErrOffline)
	}
	ed, ok := m.driverForNode(nodeID).(egressDriver)
	if !ok {
		return nil, ErrEgressAgentUnsupported
	}
	return ed, nil
}

// EgressStatus는 노드(nodeID)에서 환경(envID) 네트워크의 IPv6 egress 상태와 차단 가능 여부다(읽기 전용, 30초 캐시).
func (m *Manager) EgressStatus(ctx context.Context, nodeID, envID string) (*node.EgressStatus, error) {
	nodeID = nodeKey(nodeID)
	key := nodeID + "|" + envID
	m.egressSt.mu.Lock()
	c, ok := m.egressSt.status[key]
	m.egressSt.mu.Unlock()
	if ok && time.Since(c.at) < egressStatusTTL {
		return c.st, c.err
	}
	ed, err := m.egressDriverFor(nodeID)
	if err != nil {
		return nil, err
	}
	st, err := ed.EgressStatus(ctx, envID)
	if err == nil || errors.Is(err, ErrEgressAgentUnsupported) {
		m.egressSt.mu.Lock()
		if m.egressSt.status == nil {
			m.egressSt.status = map[string]egressCached{}
		}
		m.egressSt.status[key] = egressCached{st: st, err: err, at: time.Now()}
		m.egressSt.mu.Unlock()
	}
	return st, err
}

// EgressNodes는 인스턴스의 컨테이너가 있을 수 있는 노드들이다(차단 토글 전에 이 노드들이 모두 차단을 지원하는지 본다).
func (m *Manager) EgressNodes(ctx context.Context, in *store.Instance) []string {
	return m.instanceNodes(ctx, in)
}

// blockedEgress는 노드별로 outbound_ipv6=false인 인스턴스 ID다(그 노드에 컨테이너가 있을 수 있는 것만).
func (m *Manager) blockedEgress(ctx context.Context) (map[string][]string, error) {
	ins, err := m.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, in := range ins {
		if in.OutboundIPv6 == nil || *in.OutboundIPv6 {
			continue
		}
		for _, n := range m.instanceNodes(ctx, in) {
			out[n] = append(out[n], in.ID)
		}
	}
	for n := range out {
		sort.Strings(out[n])
	}
	return out, nil
}

// SyncEgress는 연결된 모든 노드(로컬 포함)의 차단 규칙을 맞춘다. 차단을 지원하지 않는 노드에 차단할 인스턴스가 있으면 오류에 담는다.
func (m *Manager) SyncEgress(ctx context.Context) error {
	return m.syncEgress(ctx, "", false)
}

// SyncEgressNode는 노드 하나를 캐시와 상관없이 다시 맞춘다(agent가 다시 연결됐을 때: agent가 바뀌었거나 노드가 재부팅됐을 수 있다).
func (m *Manager) SyncEgressNode(ctx context.Context, nodeID string) error {
	nodeID = nodeKey(nodeID)
	m.egressSt.mu.Lock()
	delete(m.egressSt.nodes, nodeID)
	for k := range m.egressSt.status {
		if strings.HasPrefix(k, nodeID+"|") {
			delete(m.egressSt.status, k)
		}
	}
	m.egressSt.mu.Unlock()
	return m.syncEgress(ctx, nodeID, true)
}

func (m *Manager) syncEgress(ctx context.Context, only string, force bool) error {
	if m.Store == nil {
		return nil
	}
	m.egressSt.syncMu.Lock()
	defer m.egressSt.syncMu.Unlock()
	blocked, err := m.blockedEgress(ctx)
	if err != nil {
		return err
	}
	var nodes []string
	if only != "" {
		nodes = []string{only}
	} else {
		if m.localDrv != nil || m.Docker != nil {
			nodes = append(nodes, store.LocalNodeID)
		}
		for _, n := range m.remoteNodes(ctx) {
			nodes = append(nodes, n.ID)
		}
	}
	var errs []error
	for _, id := range nodes {
		if err := m.syncEgressNode(ctx, id, blocked[id], force); err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// syncEgressNode는 노드 하나에 egress.ensure를 보낸다. 오프라인이면 건너뛴다.
func (m *Manager) syncEgressNode(ctx context.Context, nodeID string, blocked []string, force bool) error {
	if !m.nodeOnline(nodeID) {
		return nil
	}
	m.egressSt.mu.Lock()
	st := m.egressSt.node(nodeID)
	skip := !force && len(blocked) == 0 && st.clean
	oldAgent := !force && !st.oldAgentAt.IsZero() && time.Since(st.oldAgentAt) < egressOldAgentRetry
	m.egressSt.mu.Unlock()
	if skip {
		return nil
	}
	if oldAgent {
		if len(blocked) > 0 {
			return ErrEgressAgentUnsupported
		}
		return nil
	}
	ed, err := m.egressDriverFor(nodeID)
	if err != nil {
		if errors.Is(err, ErrEgressAgentUnsupported) {
			m.noteEgress(nodeID, func(n *egressNode) { n.oldAgentAt = time.Now() })
			if len(blocked) == 0 {
				return nil
			}
		}
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, egressCallTimeout)
	defer cancel()
	res, err := ed.EgressEnsure(cctx, blocked)
	switch {
	case errors.Is(err, ErrEgressAgentUnsupported):
		m.noteEgress(nodeID, func(n *egressNode) { n.oldAgentAt = time.Now(); n.clean = false })
		if len(blocked) == 0 {
			return nil
		}
		m.logEgressErr(nodeID, err)
		return err
	case err != nil:
		m.noteEgress(nodeID, func(n *egressNode) { n.clean = false; n.oldAgentAt = time.Time{} })
		m.logEgressErr(nodeID, err)
		return err
	}
	m.noteEgress(nodeID, func(n *egressNode) {
		n.oldAgentAt = time.Time{}
		n.lastErr = ""
		n.clean = len(blocked) == 0 && res != nil && res.Rules == 0
	})
	if res != nil && (res.Added > 0 || res.Removed > 0) && m.Log != nil {
		m.Log.Info("egress6 rules", "node", nodeID, "added", res.Added, "removed", res.Removed, "rules", res.Rules)
	}
	return nil
}

func (m *Manager) noteEgress(nodeID string, f func(*egressNode)) {
	m.egressSt.mu.Lock()
	f(m.egressSt.node(nodeID))
	m.egressSt.mu.Unlock()
}

// logEgressErr는 노드의 egress 오류를 바뀔 때만 기록한다(루프가 30초마다 같은 오류를 남기지 않게).
func (m *Manager) logEgressErr(nodeID string, err error) {
	m.egressSt.mu.Lock()
	n := m.egressSt.node(nodeID)
	same := n.lastErr == err.Error()
	n.lastErr = err.Error()
	m.egressSt.mu.Unlock()
	if !same && m.Log != nil {
		m.Log.Warn("egress6 sync", "node", nodeID, "err", err)
	}
}

// syncEgressAsync는 배포 완료나 replica 변경 뒤 차단 규칙을 맞춘다(새 컨테이너는 새 주소를 받는다). 배포 경로를 막지 않는다.
func (m *Manager) syncEgressAsync() {
	if m.Store == nil {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_ = m.SyncEgress(ctx) // 오류는 syncEgressNode가 기록한다
	}()
}

// EgressLoop는 every마다 차단 규칙을 맞춘다(컨테이너 재시작이나 재부팅으로 주소가 바뀐 경우를 잡는다).
func (m *Manager) EgressLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_ = m.SyncEgress(rctx)
		cancel()
	}
}
