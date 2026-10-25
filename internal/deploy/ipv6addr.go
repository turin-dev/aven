package deploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// 도메인별 공개 IPv6(§7).
//
// 노드에 IPv6 풀(nodes.ipv6_pool, 로컬은 KEEL_IPV6_POOL이 우선)이 있으면 그 노드가 서비스하는 공개 도메인마다 풀에서 /128을 하나
// 배정하고(store.ReconcileDomainIPv6), 노드 인터페이스에 붙인다(로컬: node.IPv6Ensure, 원격: agent ipv6.ensure).
// 서비스 노드는 인스턴스의 home 노드(현재 배포의 노드, 없으면 node_id)다. replica가 여러 노드에 있어도 home 노드 풀에서만 배정한다.
//
// 맞추는 때: 서버 시작, 도메인 추가·삭제·이동(API와 배포 완료가 syncDomainIPv6Async로 깨운다), agent 재연결, 그리고 60초마다
// (systemd-networkd가 다시 설정하면서 aven이 붙인 주소를 지울 수 있다). 풀 밖의 주소는 절대 건드리지 않는다.

// ErrIPv6AgentUnsupported는 원격 노드의 agent가 ipv6.*를 모를 때다(옛 agent).
var ErrIPv6AgentUnsupported = errors.New("이 노드의 aven-agent가 도메인별 IPv6(ipv6.addrs/ipv6.ensure)를 지원하지 않습니다. agent를 업그레이드하세요")

// IPv6NodeStatus는 노드 하나의 도메인별 IPv6 반영 상태다(GET /api/nodes의 ipv6_status).
type IPv6NodeStatus struct {
	Pool string `json:"pool"`
	// Supported: true(반영함) | false(이 노드에서 쓸 수 없음, Reason) | nil(아직 확인 전이거나 노드가 오프라인).
	Supported *bool      `json:"supported"`
	Reason    string     `json:"reason,omitempty"`
	Iface     string     `json:"iface,omitempty"`
	Desired   int        `json:"desired"`
	Present   int        `json:"present"`
	Foreign   []string   `json:"foreign,omitempty"`
	LastError string     `json:"last_error,omitempty"`
	CheckedAt *time.Time `json:"checked_at,omitempty"`
}

type ipv6State struct {
	syncMu sync.Mutex // 반영은 한 번에 하나
	mu     sync.Mutex
	nodes  map[string]*IPv6NodeStatus
	once   sync.Once
	kick   chan struct{}
}

func (s *ipv6State) init() {
	s.once.Do(func() { s.kick = make(chan struct{}, 1) })
}

// ipv6Driver는 원격 노드의 ipv6 작업이다(remoteDriver가 구현한다). 로컬 노드는 Manager.IPRunner로 직접 한다.
type ipv6Driver interface {
	IPv6Addrs(ctx context.Context) ([]node.HostIPv6Addr, error)
	IPv6Ensure(ctx context.Context, req agentprotocol.IPv6EnsureRequest) (*node.IPv6EnsureResult, error)
}

var _ ipv6Driver = (*remoteDriver)(nil)

// IPv6Addrs는 agent의 ipv6.addrs다. 옛 agent는 ErrIPv6AgentUnsupported다.
func (r *remoteDriver) IPv6Addrs(ctx context.Context) ([]node.HostIPv6Addr, error) {
	var res agentprotocol.IPv6AddrsResult
	if err := r.call(ctx, agentprotocol.OpIPv6Addrs, struct{}{}, &res); err != nil {
		if isUnknownOp(err, agentprotocol.OpIPv6Addrs) {
			return nil, ErrIPv6AgentUnsupported
		}
		return nil, err
	}
	return res.Addrs, nil
}

// IPv6Ensure는 agent의 ipv6.ensure다. 옛 agent는 ErrIPv6AgentUnsupported다.
func (r *remoteDriver) IPv6Ensure(ctx context.Context, req agentprotocol.IPv6EnsureRequest) (*node.IPv6EnsureResult, error) {
	if req.Addresses == nil {
		req.Addresses = []string{}
	}
	var res agentprotocol.IPv6EnsureResult
	if err := r.call(ctx, agentprotocol.OpIPv6Ensure, req, &res); err != nil {
		if isUnknownOp(err, agentprotocol.OpIPv6Ensure) {
			return nil, ErrIPv6AgentUnsupported
		}
		return nil, err
	}
	return &res, nil
}

// LocalIPv6PoolFromEnv는 로컬 노드의 풀이 KEEL_IPV6_POOL로 정해졌는지다(그러면 PATCH 값보다 우선한다).
func (m *Manager) LocalIPv6PoolFromEnv() bool { return strings.TrimSpace(m.Cfg.IPv6Pool) != "" }

// NodeIPv6Pool은 노드의 IPv6 풀이다(없거나 형식이 틀리면 ok=false). 로컬 노드는 KEEL_IPV6_POOL이 우선한다.
func (m *Manager) NodeIPv6Pool(ctx context.Context, nodeID string) (netip.Prefix, bool) {
	raw := ""
	if isLocal(nodeID) && m.LocalIPv6PoolFromEnv() {
		raw = m.Cfg.IPv6Pool
	} else if m.Store != nil {
		if n, err := m.Store.GetNode(ctx, nodeKey(nodeID)); err == nil {
			raw = n.IPv6Pool
		}
	}
	if strings.TrimSpace(raw) == "" {
		return netip.Prefix{}, false
	}
	p, err := node.ParseIPv6Pool(raw)
	if err != nil {
		return netip.Prefix{}, false
	}
	return p, true
}

// nodeMainIPv6는 노드의 기본 공인 IPv6다(풀이 담으면 안 되는 주소).
func (m *Manager) nodeMainIPv6(ctx context.Context, nodeID string) []netip.Addr {
	raw := ""
	if isLocal(nodeID) {
		raw = m.Cfg.PublicIPv6
	} else if n, err := m.Store.GetNode(ctx, nodeID); err == nil && n.Address.IPv6 != nil {
		raw = *n.Address.IPv6
	}
	if a, err := netip.ParseAddr(strings.TrimSpace(raw)); err == nil {
		return []netip.Addr{a}
	}
	return nil
}

// domainIPv6Want는 호스트마다 배정할 풀과 그 호스트를 서비스하는(home) 노드다.
func (m *Manager) domainIPv6Want(ctx context.Context) (map[string]netip.Prefix, map[string]string, error) {
	ins, err := m.Store.ListInstances(ctx)
	if err != nil {
		return nil, nil, err
	}
	pools := map[string]*netip.Prefix{}
	poolOf := func(id string) (netip.Prefix, bool) {
		if p, ok := pools[id]; ok {
			if p == nil {
				return netip.Prefix{}, false
			}
			return *p, true
		}
		p, ok := m.NodeIPv6Pool(ctx, id)
		if !ok {
			pools[id] = nil
			return p, false
		}
		pools[id] = &p
		return p, true
	}
	want := map[string]netip.Prefix{}
	home := map[string]string{}
	for _, in := range ins {
		if len(in.Domains) == 0 {
			continue
		}
		h := m.homeNode(ctx, in)
		p, ok := poolOf(h)
		for _, d := range in.Domains {
			host := strings.ToLower(strings.TrimSpace(d.Host))
			if host == "" {
				continue
			}
			home[host] = h
			if ok {
				want[host] = p
			}
		}
	}
	return want, home, nil
}

// AssignDomainIPv6은 도메인 주소 배정만 한다(저장소 트랜잭션 하나, 노드에는 반영하지 않는다). 도메인 추가 직후 응답에
// 주소를 보여 주려고 API가 바로 부른다. 반영은 syncDomainIPv6Async나 루프가 한다.
func (m *Manager) AssignDomainIPv6(ctx context.Context) error {
	if m.Store == nil {
		return nil
	}
	want, _, err := m.domainIPv6Want(ctx)
	if err != nil {
		return err
	}
	ch, err := m.Store.ReconcileDomainIPv6(ctx, want)
	if err != nil {
		return err
	}
	for _, c := range ch {
		if m.Log != nil {
			m.Log.Info("domain ipv6", "host", c.Host, "old", c.Old, "new", c.New)
		}
	}
	return nil
}

// SyncDomainIPv6은 주소를 배정하고, 풀이 있는 연결된 모든 노드(로컬 포함)에 반영한다.
func (m *Manager) SyncDomainIPv6(ctx context.Context) error {
	if m.Store == nil {
		return nil
	}
	m.ipv6St.syncMu.Lock()
	defer m.ipv6St.syncMu.Unlock()
	if err := m.AssignDomainIPv6(ctx); err != nil {
		return err
	}
	_, home, err := m.domainIPv6Want(ctx)
	if err != nil {
		return err
	}
	states, err := m.Store.ListDomainStates(ctx)
	if err != nil {
		return err
	}
	byNode := map[string][]netip.Addr{}
	for _, st := range states {
		if st.IPv6 == "" {
			continue
		}
		a, err := netip.ParseAddr(st.IPv6)
		if err != nil {
			continue
		}
		if n, ok := home[st.Host]; ok {
			byNode[n] = append(byNode[n], a)
		}
	}
	nodes := []string{store.LocalNodeID}
	for _, n := range m.remoteNodes(ctx) {
		nodes = append(nodes, n.ID)
	}
	var errs []error
	for _, id := range nodes {
		pool, ok := m.NodeIPv6Pool(ctx, id)
		if !ok {
			m.ipv6St.mu.Lock()
			delete(m.ipv6St.nodes, id)
			m.ipv6St.mu.Unlock()
			continue
		}
		var want []netip.Addr
		for _, a := range byNode[id] {
			if pool.Contains(a) {
				want = append(want, a)
			}
		}
		if err := m.ensureNodeIPv6(ctx, id, pool, want); err != nil {
			errs = append(errs, fmt.Errorf("node %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// ensureNodeIPv6는 노드 하나의 pool 안 주소를 want에 맞추고 상태를 기록한다. 오프라인 노드는 건너뛴다.
// 옛 agent와 ip를 쓸 수 없는 노드는 supported=false로 기록하고 오류로 치지 않는다.
func (m *Manager) ensureNodeIPv6(ctx context.Context, nodeID string, pool netip.Prefix, want []netip.Addr) error {
	sort.Slice(want, func(i, j int) bool { return want[i].Less(want[j]) })
	st := &IPv6NodeStatus{Pool: pool.String(), Desired: len(want)}
	now := time.Now().UTC()
	st.CheckedAt = &now
	if !m.nodeOnline(nodeID) {
		st.CheckedAt = nil
		st.Reason = "노드가 오프라인입니다"
		m.setIPv6Status(nodeID, st)
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var res *node.IPv6EnsureResult
	var err error
	if isLocal(nodeID) {
		res, err = m.localIPv6Ensure(cctx, pool, want)
	} else {
		addrs := make([]string, 0, len(want))
		for _, a := range want {
			addrs = append(addrs, a.String())
		}
		res, err = (&remoteDriver{hub: m.Agents, node: nodeID}).IPv6Ensure(cctx, agentprotocol.IPv6EnsureRequest{Pool: pool.String(), Addresses: addrs})
	}
	if res != nil {
		st.Iface, st.Present, st.Foreign, st.Reason = res.Iface, res.Present, res.Foreign, res.Reason
		sup := res.Supported
		st.Supported = &sup
	}
	switch {
	case errors.Is(err, ErrIPv6AgentUnsupported) || errors.Is(err, node.ErrIPv6Unsupported):
		f := false
		st.Supported = &f
		if st.Reason == "" {
			st.Reason = err.Error()
		}
		m.setIPv6Status(nodeID, st)
		return nil
	case err != nil:
		st.LastError = err.Error()
		prev := m.ipv6StatusRaw(nodeID)
		m.setIPv6Status(nodeID, st)
		if m.Log != nil && (prev == nil || prev.LastError != st.LastError) { // 같은 오류를 60초마다 남기지 않는다
			m.Log.Warn("domain ipv6 sync", "node", nodeID, "err", err)
		}
		return err
	}
	m.setIPv6Status(nodeID, st)
	if res != nil && (res.Added > 0 || res.Removed > 0) && m.Log != nil {
		m.Log.Info("domain ipv6 addresses", "node", nodeID, "iface", res.Iface, "pool", pool, "added", res.Added, "removed", res.Removed, "present", res.Present)
	}
	return nil
}

// localIPv6Ensure는 로컬 노드에 주소를 반영한다. IPRunner가 없으면(root가 아님, 테스트) 붙일 것이 있을 때 node.ErrIPv6Unsupported다.
func (m *Manager) localIPv6Ensure(ctx context.Context, pool netip.Prefix, want []netip.Addr) (*node.IPv6EnsureResult, error) {
	r := m.IPRunner
	iface := strings.TrimSpace(m.Cfg.IPv6Iface)
	if iface == "" && r != nil {
		var err error
		if iface, err = node.DefaultIPv6Iface(ctx, r, m.Cfg.PublicIPv6); err != nil {
			return &node.IPv6EnsureResult{Pool: pool.String(), Desired: len(want)}, err
		}
	}
	return node.IPv6Ensure(ctx, r, iface, pool, want, m.nodeMainIPv6(ctx, store.LocalNodeID))
}

func (m *Manager) setIPv6Status(nodeID string, st *IPv6NodeStatus) {
	m.ipv6St.mu.Lock()
	defer m.ipv6St.mu.Unlock()
	if m.ipv6St.nodes == nil {
		m.ipv6St.nodes = map[string]*IPv6NodeStatus{}
	}
	m.ipv6St.nodes[nodeKey(nodeID)] = st
}

func (m *Manager) ipv6StatusRaw(nodeID string) *IPv6NodeStatus {
	m.ipv6St.mu.Lock()
	defer m.ipv6St.mu.Unlock()
	return m.ipv6St.nodes[nodeKey(nodeID)]
}

// IPv6Status는 노드의 마지막 반영 상태다. 풀이 없으면 nil이다.
func (m *Manager) IPv6Status(ctx context.Context, nodeID string) *IPv6NodeStatus {
	pool, ok := m.NodeIPv6Pool(ctx, nodeID)
	if !ok {
		return nil
	}
	m.ipv6St.mu.Lock()
	st := m.ipv6St.nodes[nodeKey(nodeID)]
	m.ipv6St.mu.Unlock()
	if st == nil || st.Pool != pool.String() {
		return &IPv6NodeStatus{Pool: pool.String()}
	}
	c := *st
	return &c
}

// ReleaseIPv6Pool은 노드의 예전 풀(old)에서 aven 주소를 거둔다(풀을 바꾸거나 지운 뒤). 새 풀과 겹치는 주소 가운데 지금 배정된
// 것은 남긴다. 오프라인이거나 지원하지 않는 노드는 건너뛴다(그 노드에는 주소가 남는다).
func (m *Manager) ReleaseIPv6Pool(ctx context.Context, nodeID string, old netip.Prefix) error {
	nodeID = nodeKey(nodeID)
	if !old.IsValid() || !m.nodeOnline(nodeID) {
		return nil
	}
	m.ipv6St.syncMu.Lock()
	defer m.ipv6St.syncMu.Unlock()
	managed, err := m.Store.ManagedIPv6s(ctx)
	if err != nil {
		return err
	}
	var keep []netip.Addr
	if cur, ok := m.NodeIPv6Pool(ctx, nodeID); ok {
		for a := range managed {
			if old.Contains(a) && cur.Contains(a) {
				keep = append(keep, a)
			}
		}
	}
	sort.Slice(keep, func(i, j int) bool { return keep[i].Less(keep[j]) })
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var res *node.IPv6EnsureResult
	if isLocal(nodeID) {
		res, err = m.localIPv6Ensure(cctx, old, keep)
	} else {
		addrs := []string{}
		for _, a := range keep {
			addrs = append(addrs, a.String())
		}
		res, err = (&remoteDriver{hub: m.Agents, node: nodeID}).IPv6Ensure(cctx, agentprotocol.IPv6EnsureRequest{Pool: old.String(), Addresses: addrs})
	}
	if errors.Is(err, ErrIPv6AgentUnsupported) || errors.Is(err, node.ErrIPv6Unsupported) {
		return nil
	}
	if err == nil && res != nil && res.Removed > 0 && m.Log != nil {
		m.Log.Info("domain ipv6 old pool released", "node", nodeID, "pool", old, "removed", res.Removed)
	}
	return err
}

// HostIPv6Addrs는 노드 인터페이스의 전역 IPv6 주소다(풀 검증용). 로컬은 IPRunner(없으면 Go net 패키지로 읽는다), 원격은 ipv6.addrs.
func (m *Manager) HostIPv6Addrs(ctx context.Context, nodeID string) ([]node.HostIPv6Addr, error) {
	if isLocal(nodeID) {
		if m.IPRunner != nil {
			return node.ListHostIPv6(ctx, m.IPRunner, "")
		}
		return goHostIPv6()
	}
	if !m.nodeOnline(nodeID) {
		return nil, fmt.Errorf("node %s: %w", nodeID, agentprotocol.ErrOffline)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	return (&remoteDriver{hub: m.Agents, node: nodeID}).IPv6Addrs(cctx)
}

// goHostIPv6는 root 없이 읽은 호스트 주소다(deprecated 여부는 알 수 없다).
func goHostIPv6() ([]node.HostIPv6Addr, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []node.HostIPv6Addr
	for _, ifc := range ifs {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip, ok := netip.AddrFromSlice(ipn.IP)
			if !ok || !ip.Is6() || ip.Is4In6() || ip.IsLinkLocalUnicast() || ip.IsLoopback() {
				continue
			}
			bits, _ := ipn.Mask.Size()
			out = append(out, node.HostIPv6Addr{Iface: ifc.Name, Addr: ip.String(), Bits: bits, Scope: "global"})
		}
	}
	return out, nil
}

// CheckIPv6Pool은 노드에 pool을 써도 되는지 본다: 노드의 기본 IPv6를 담지 않고, 노드에 이미 있는 주소(aven이 배정한 주소 제외)를
// 담지 않으며, /64보다 작은 노드 서브넷과 겹치지 않아야 한다. 노드에 물어볼 수 없으면(오프라인, 옛 agent) 오류다.
func (m *Manager) CheckIPv6Pool(ctx context.Context, nodeID string, pool netip.Prefix) error {
	nodeID = nodeKey(nodeID)
	protect := m.nodeMainIPv6(ctx, nodeID)
	for _, p := range protect {
		if pool.Contains(p) {
			return fmt.Errorf("IPv6 풀 %s에 노드의 기본 IPv6 주소 %s가 들어 있습니다", pool, p)
		}
	}
	host, err := m.HostIPv6Addrs(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("노드의 IPv6 주소를 확인하지 못했습니다: %w", err)
	}
	managed := map[netip.Addr]bool{}
	if all, err := m.Store.ManagedIPv6s(ctx); err == nil {
		for a := range all {
			managed[a] = true
		}
	}
	return node.CheckIPv6PoolOnHost(pool, host, managed, protect)
}

// syncDomainIPv6Async는 도메인 추가·삭제·이동 뒤 루프를 바로 깨운다(요청과 배포 경로를 막지 않는다).
func (m *Manager) syncDomainIPv6Async() {
	m.ipv6St.init()
	select {
	case m.ipv6St.kick <- struct{}{}:
	default:
	}
}

// SyncDomainIPv6Async는 syncDomainIPv6Async의 공개판이다(API가 도메인을 바꾼 뒤 부른다).
func (m *Manager) SyncDomainIPv6Async() { m.syncDomainIPv6Async() }

// DomainIPv6Loop는 시작할 때 한 번, 그 뒤 every마다, 그리고 깨울 때마다 도메인 IPv6를 맞춘다.
func (m *Manager) DomainIPv6Loop(ctx context.Context, every time.Duration) {
	m.ipv6St.init()
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		_ = m.SyncDomainIPv6(rctx) // 오류는 ensureNodeIPv6가 기록한다
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.ipv6St.kick:
		}
	}
}
