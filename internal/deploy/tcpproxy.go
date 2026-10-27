package deploy

import (
	"context"
	"errors"
	"fmt"
	"github.com/turin-dev/aven/internal/compat"
	"net"
	"strings"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

func tcpName(instanceID string) string {
	return "aven-tcp-" + strings.TrimPrefix(instanceID, "ins_")
}

func legacyTCPName(instanceID string) string {
	return "keel-tcp-" + strings.TrimPrefix(instanceID, "ins_")
}

// syncTCPProxy는 인스턴스의 TCP 포워더 컨테이너를 원하는 상태로 맞춘다.
// 포워더는 인스턴스가 도는 노드에서 서버가 고른 공개 포트를 publish하고, 환경 네트워크의 서비스 alias로 넘긴다(배포가 바뀌어도 포트 유지).
// 원격 노드에서는 agent가 같은 포워더를 자기 노드에서 만든다(agent는 포트를 직접 받지 않고 검증된 필드로 스스로 만든다).
// 인스턴스가 다른 노드로 옮겨졌다면 이전 노드의 포워더는 지운다.
func (m *Manager) syncTCPProxy(ctx context.Context, t *Target) error {
	in, err := m.Store.GetInstance(ctx, t.Instance.ID)
	if err != nil {
		return err
	}
	home := m.homeNode(ctx, in)
	want := in.TCPProxy != nil && in.TCPProxy.PublicPort > 0 && in.CurrentDeploymentID != nil && liveStatus(in.Status) && in.CronSchedule == nil
	seen := map[string]bool{}
	var errs []error
	for _, n := range append([]string{home}, m.instanceNodes(ctx, in)...) {
		n = nodeKey(n)
		if seen[n] {
			continue
		}
		seen[n] = true
		if !m.nodeOnline(n) {
			if n == home && want {
				errs = append(errs, fmt.Errorf("node %s: %w", n, agentprotocol.ErrOffline))
			}
			continue
		}
		if err := m.syncTCPOn(ctx, t, in, n, want && n == home); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (m *Manager) syncTCPOn(ctx context.Context, t *Target, in *store.Instance, nodeID string, want bool) error {
	name := tcpName(in.ID)
	drv := m.driverForNode(nodeID)
	st, ierr := drv.Inspect(ctx, name)
	exists := ierr == nil
	if !exists {
		if prev, err := drv.Inspect(ctx, legacyTCPName(in.ID)); err == nil {
			name = legacyTCPName(in.ID)
			st, exists = prev, true
			compat.NoteLegacyResource("tcp-proxy", "container "+name, "container "+tcpName(in.ID))
		}
	}
	if !want {
		if exists {
			return drv.Remove(ctx, name, 1)
		}
		return nil
	}
	target := PrivateDomain(t.Service.Name, t.Env.Name)
	sig := fmt.Sprintf("%d:%d:%s", in.TCPProxy.PublicPort, in.TCPProxy.ContainerPort, target)
	if !isLocal(nodeID) {
		if err := m.ensureEnvNetwork(ctx, t.Env, nodeID); err != nil {
			return err
		}
		return (&remoteDriver{hub: m.Agents, node: nodeID}).tcpEnsure(ctx, agentprotocol.TCPProxyRequest{InstanceID: in.ID, ProjectID: t.Project.ID, EnvID: t.Env.ID,
			Network: EnvNetwork(t.Env.ID), Target: target, PublicPort: in.TCPProxy.PublicPort, ContainerPort: in.TCPProxy.ContainerPort})
	}
	got := ""
	if st != nil {
		got = st.Labels["aven.tcp"]
		if got == "" {
			got = st.Labels["keel.tcp"]
		}
	}
	if exists && got == sig && st.Running {
		return nil
	}
	if exists {
		m.Docker.Remove(ctx, name, 1)
	}
	if err := m.ensureEnvNetwork(ctx, t.Env, store.LocalNodeID); err != nil {
		return err
	}
	pub := fmt.Sprintf("%d:%d", in.TCPProxy.PublicPort, in.TCPProxy.ContainerPort)
	if m.Cfg.ProxyBind != "" {
		host := m.Cfg.ProxyBind
		if strings.Contains(host, ":") {
			host = "[" + host + "]"
		}
		pub = host + ":" + pub
	}
	return m.Docker.Run(ctx, node.RunSpec{
		Name: name, Image: node.TCPForwardImage, Network: EnvNetwork(t.Env.ID), Restart: "unless-stopped",
		Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleTCPProxy, node.LabelInstance: in.ID,
			node.LabelEnv: t.Env.ID, node.LabelProject: t.Project.ID, "aven.tcp": sig, "keel.tcp": sig},
		Publish: []string{pub},
		Cmd: []string{fmt.Sprintf("TCP-LISTEN:%d,fork,reuseaddr", in.TCPProxy.ContainerPort),
			fmt.Sprintf("TCP:%s:%d", target, in.TCPProxy.ContainerPort)},
	})
}

// AllocateTCPPort는 로컬 노드에서 범위 안에서 쓰지 않는 공개 포트를 고른다.
func (m *Manager) AllocateTCPPort(ctx context.Context) (int, error) {
	return m.AllocateTCPPortFor(ctx, store.LocalNodeID)
}

// AllocateTCPPortFor는 노드에서 쓸 공개 포트를 고른다. 이미 배정된 포트는 모든 노드에서 겹치지 않게 피하고,
// 실제로 bind가 되는지도 확인한다: 로컬 노드는 직접, 원격 노드는 agent의 port.free로(모르는 옛 agent면 확인 없이 배정하고,
// 충돌은 agent가 publish할 때 드러난다).
func (m *Manager) AllocateTCPPortFor(ctx context.Context, nodeID string) (int, error) {
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return 0, err
	}
	used := map[int]bool{}
	for _, in := range insts {
		if in.TCPProxy != nil {
			used[in.TCPProxy.PublicPort] = true
		}
	}
	bind := m.Cfg.ProxyBind
	probes := 0
	for p := m.Cfg.TCPPortMin; p <= m.Cfg.TCPPortMax; p++ {
		if used[p] {
			continue
		}
		if !isLocal(nodeID) {
			if probes >= maxRemotePortProbes {
				return 0, errors.New("no free TCP proxy port (remote node refused the first candidates)")
			}
			probes++
			free, err := m.remotePortFree(ctx, nodeID, p)
			switch {
			case errors.Is(err, errPortProbeUnsupported):
				return p, nil
			case err != nil:
				return 0, err
			case free:
				return p, nil
			}
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort(bind, fmt.Sprint(p)))
		if err != nil {
			continue
		}
		ln.Close()
		return p, nil
	}
	return 0, errors.New("no free TCP proxy port")
}

// maxRemotePortProbes는 원격 노드에서 port.free로 확인해 볼 후보 수의 상한이다(하나마다 agent 왕복 한 번).
const maxRemotePortProbes = 64

var errPortProbeUnsupported = errors.New("port.free is not supported by this agent")

// remotePortFree는 원격 노드 agent의 port.free다. 작업을 모르는 옛 agent면 errPortProbeUnsupported.
func (m *Manager) remotePortFree(ctx context.Context, nodeID string, port int) (bool, error) {
	if m.Agents == nil {
		return false, errPortProbeUnsupported
	}
	var res agentprotocol.PortFreeResult
	err := (&remoteDriver{hub: m.Agents, node: nodeID}).call(ctx, agentprotocol.OpPortFree, agentprotocol.PortFreeRequest{Port: port}, &res)
	if isUnknownOp(err, agentprotocol.OpPortFree) {
		return false, errPortProbeUnsupported
	}
	if err != nil {
		return false, fmt.Errorf("node %s: %w", nodeID, err)
	}
	return res.Free, nil
}

// TCPProxyEndpoint는 인스턴스의 TCP 프록시 접속 호스트와, 그 노드가 TCP 프록시를 열 수 있는지다.
//   - 로컬 노드: KEEL_TCP_PROXY_DOMAIN → 노드 tcp_hostname → 기존 방식(TCPProxyHost). 터널 모드면 열 수 없다.
//   - 원격 노드: 노드 tcp_hostname → 공인 주소(IPv6 우선). 터널 모드이거나 공인 주소가 없으면 열 수 없다.
func (m *Manager) TCPProxyEndpoint(ctx context.Context, in *store.Instance) (host string, available bool) {
	if in == nil {
		h := m.TCPProxyHost()
		return h, h != ""
	}
	return m.tcpEndpointOn(ctx, m.homeNode(ctx, in))
}

func (m *Manager) tcpEndpointOn(ctx context.Context, nodeID string) (string, bool) {
	if isLocal(nodeID) {
		var ing store.NodeIngress
		if m.Store != nil {
			if n, err := m.Store.LocalNode(ctx); err == nil {
				ing = n.NodeIngress
			}
		}
		if v := strings.TrimSpace(compat.Env("INGRESS_MODE")); v != "" {
			ing.IngressMode = v
		}
		if ing.Tunnel() {
			return "", false
		}
		h := m.TCPProxyHost()
		if m.Cfg.TCPProxyDomain == "" && strings.TrimSpace(ing.TCPHostname) != "" {
			h = strings.TrimSpace(ing.TCPHostname)
		}
		return h, h != ""
	}
	n, err := m.Store.GetNode(ctx, nodeID)
	if err != nil || n.Tunnel() {
		return "", false
	}
	var v6, v4 string
	if n.Address.IPv6 != nil {
		v6 = *n.Address.IPv6
	}
	if n.Address.IPv4 != nil {
		v4 = *n.Address.IPv4
	}
	if v6 == "" && v4 == "" {
		return "", false
	}
	if h := strings.TrimSpace(n.TCPHostname); h != "" {
		return h, true
	}
	if v6 != "" {
		return v6, true
	}
	return v4, true
}

// SyncTCP는 API에서 TCP proxy 설정이 바뀌었을 때 부른다.
func (m *Manager) SyncTCP(ctx context.Context, instanceID string) error {
	t, err := m.Load(ctx, instanceID)
	if err != nil {
		return err
	}
	return m.syncTCPProxy(ctx, t)
}
