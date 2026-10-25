package deploy

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/events"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/tarx"
)

func (m *Manager) backupDir() string     { return filepath.Join(m.Cfg.DataDir, "backups") }
func (m *Manager) cronLogDir() string    { return filepath.Join(m.Cfg.DataDir, "logs", "cron") }
func (m *Manager) runtimeLogDir() string { return filepath.Join(m.Cfg.DataDir, "logs", "runtime") }
func (m *Manager) uploadDir() string     { return filepath.Join(m.Cfg.DataDir, "uploads") }

// UploadPath는 CLI 업로드 소스를 둘 경로다.
func (m *Manager) UploadPath() string {
	return filepath.Join(m.uploadDir(), store.NewID("upl")+".tar.gz")
}

// ensureEnvNetwork는 노드에 환경 네트워크를 만들고 그 노드의 keel-proxy를 붙인다(원격 노드는 agent가 붙인다).
func (m *Manager) ensureEnvNetwork(ctx context.Context, env *store.Environment, nodeID string) error {
	name := EnvNetwork(env.ID)
	if !isLocal(nodeID) || m.localDrv != nil {
		if err := m.driverForNode(nodeID).EnsureNamedNetwork(ctx, name, map[string]string{node.LabelProject: env.ProjectID, node.LabelEnv: env.ID}); err != nil {
			return fmt.Errorf("network %s on node %s: %w", name, nodeID, err)
		}
		return nil
	}
	m.networkMu.Lock()
	defer m.networkMu.Unlock()
	if err := m.Docker.EnsureNamedNetwork(ctx, name, map[string]string{node.LabelProject: env.ProjectID, node.LabelEnv: env.ID}); err != nil {
		return fmt.Errorf("network %s: %w", name, err)
	}
	return m.Proxy.Connect(ctx, name)
}

// liveInstance는 현재 배포 컨테이너가 트래픽을 받아야 하는 상태인지 알려준다.
func liveStatus(st string) bool {
	return st == store.InstOnline || st == store.InstDeploying || st == store.InstCrashed
}

// SyncProxy는 DB 기준으로 모든 노드의 라우트를 다시 만들어 적용한다. 로컬 keel-proxy의 오류만 돌려준다
// (원격 노드 오류는 로그로 남기고, 노드가 다시 연결되면 ReconcileNode가 다시 보낸다).
func (m *Manager) SyncProxy(ctx context.Context) error {
	m.syncDomainIPv6Async() // 도메인 추가·삭제 뒤(API가 SyncProxy를 부른다) 도메인별 IPv6도 맞춘다
	errs := m.syncProxy(ctx)
	for n, err := range errs {
		if n != store.LocalNodeID && err != nil && !errors.Is(err, agentprotocol.ErrOffline) {
			m.Log.Warn("proxy sync on remote node", "node", n, "err", err)
		}
	}
	return errs[store.LocalNodeID]
}

// syncProxy는 노드별 라우트를 만들어 각 노드의 keel-proxy에 넣고 노드별 오류를 돌려준다.
// 인스턴스의 라우트는 현재 배포가 도는 노드의 proxy에만 들어간다.
func (m *Manager) syncProxy(ctx context.Context) map[string]error {
	m.proxyMu.Lock()
	defer m.proxyMu.Unlock()
	errs := map[string]error{}
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		errs[store.LocalNodeID] = err
		return errs
	}
	m.edgePurgeOnDeploy(ctx, insts)
	routes := map[string][]proxy.Route{}
	hosts := map[string]string{}
	kinds := map[string]string{} // 호스트 → 도메인 종류(도메인 확인 토큰·ACME 보류용)
	// 같은 환경의 서비스 이름 → 인스턴스(경로 규칙이 다른 서비스를 가리킬 수 있다)
	svcName := map[string]string{}
	byEnvSvc := map[string]*store.Instance{}
	for _, in := range insts {
		if sv, err := m.Store.GetService(ctx, in.ServiceID); err == nil {
			svcName[in.ID] = sv.Name
			byEnvSvc[in.EnvironmentID+"/"+sv.Name] = in
		}
	}
	for _, in := range insts {
		var doms []store.Domain
		for _, dm := range in.Domains {
			hosts[dm.Host] = in.ID
			kinds[dm.Host] = dm.Kind
			if dm.Upstream != "" {
				// aven 밖의 upstream: 배포 상태와 무관하게 인스턴스의 노드 proxy에 넣는다(그 노드가 허용한 대상만).
				nk := nodeKey(in.NodeID)
				if m.externalAllowed(ctx, nk, dm.Upstream) {
					routes[nk] = append(routes[nk], m.upstreamRoutes(ctx, in, dm)...)
				} else if m.Log != nil {
					m.Log.Warn("external upstream not allowed on node; domain not routed", "host", dm.Host, "upstream", dm.Upstream, "node", nk)
				}
				continue
			}
			if dm.Redirect != nil {
				// 리다이렉트 도메인은 upstream이 필요 없다. 인스턴스가 도는 노드마다 넣는다(DNS가 어느 노드로 보내도 같게).
				rt := m.hostRoute(ctx, in, dm)
				for _, nk := range m.servingNodes(ctx, in) {
					routes[nk] = append(routes[nk], rt)
				}
				continue
			}
			doms = append(doms, dm)
		}
		if len(doms) == 0 {
			continue
		}
		if in.Status == store.InstSleeping && in.CurrentDeploymentID != nil {
			// 잠든 서비스의 라우트는 그 서비스가 도는 노드의 wake handler를 가리킨다(로컬은 aven-server, 원격은 그 노드의 agent).
			nk, wake := store.LocalNodeID, m.Cfg.WakeAddr
			if d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil {
				nk = nodeKey(d.NodeID)
			}
			if nk != store.LocalNodeID {
				wake = proxy.WakeDial
			}
			if wake != "" {
				for _, dm := range doms {
					rt := m.hostRoute(ctx, in, dm)
					rt.Upstreams = []string{wake}
					routes[nk] = append(routes[nk], rt)
				}
				continue
			}
		}
		if !liveStatus(in.Status) || in.CurrentDeploymentID == nil {
			continue
		}
		d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err != nil {
			continue
		}
		// 여러 노드에 replica가 있으면 노드마다 자기 노드의 replica로만 보낸다(DNS round-robin이 노드를 고른다).
		for _, g := range d.Groups() {
			if len(g.Containers) == 0 {
				continue
			}
			nk := nodeKey(g.NodeID)
			for _, dm := range doms {
				rt := m.hostRoute(ctx, in, dm)
				rt.Upstreams = upstreams(g.Containers, routePort(d, in.Port, dm.TargetPort, targetIfPositive))
				routes[nk] = append(routes[nk], rt)
				for _, pr := range dm.Paths {
					if r, ok := m.pathRoute(ctx, in, dm, pr, nk, byEnvSvc, svcName); ok {
						routes[nk] = append(routes[nk], r)
					}
				}
			}
		}
	}
	if m.Cfg.SelfHost != "" && m.Cfg.SelfUpstream != "" {
		if _, taken := hosts[m.Cfg.SelfHost]; !taken {
			routes[store.LocalNodeID] = append(routes[store.LocalNodeID], proxy.Route{Hosts: []string{m.Cfg.SelfHost},
				Upstreams: []string{m.Cfg.SelfUpstream}, SetHeaders: m.Cfg.SelfHeaders})
		} else if m.Log != nil {
			m.Log.Warn("a service domain uses aven's own public host; aven-server is not routed", "host", m.Cfg.SelfHost)
		}
	}
	m.decorateDomainRoutes(ctx, routes, kinds)
	m.HTTPLogs.SetHosts(hosts)
	if m.localProxy != nil {
		errs[store.LocalNodeID] = m.localProxy(ctx, routes[store.LocalNodeID])
	} else {
		errs[store.LocalNodeID] = m.Proxy.Apply(ctx, routes[store.LocalNodeID])
	}
	for _, n := range m.remoteNodes(ctx) {
		if !m.nodeOnline(n.ID) {
			errs[n.ID] = fmt.Errorf("node %s: %w", n.ID, agentprotocol.ErrOffline)
			continue
		}
		rctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		errs[n.ID] = m.applyRemoteProxy(rctx, n.ID, routes[n.ID])
		cancel()
	}
	return errs
}

// externalAllowed는 노드 nk의 keel-proxy가 upstream으로 보낼 수 있는지다(노드 설정의 허용 목록).
func (m *Manager) externalAllowed(ctx context.Context, nk, upstream string) bool {
	if nk == store.LocalNodeID {
		return slices.Contains(m.Cfg.ExternalUpstreams, upstream)
	}
	n, err := m.Store.GetNode(ctx, nk)
	return err == nil && slices.Contains(n.ExternalUpstreams, upstream)
}

// upstreamRoutes는 aven 밖의 upstream으로 가는 라우트다. upstream_paths가 있으면 그 접두어만 보내고 나머지는 404다.
func (m *Manager) upstreamRoutes(ctx context.Context, in *store.Instance, dm store.Domain) []proxy.Route {
	rt := m.hostRoute(ctx, in, dm)
	rt.Upstreams = []string{dm.Upstream}
	if len(dm.UpstreamPaths) == 0 {
		return []proxy.Route{rt}
	}
	out := make([]proxy.Route, 0, len(dm.UpstreamPaths))
	for _, p := range dm.UpstreamPaths {
		r := rt
		r.Path = strings.TrimRight(p, "/") + "/*"
		r.PathsOnly = true
		out = append(out, r)
	}
	return out
}

func (m *Manager) cancelInstance(ctx context.Context, instanceID string) {
	deps, _ := m.Store.ListDeployments(ctx, instanceID, 1000)
	for _, d := range deps {
		if !d.Status.Terminal() {
			m.Cancel(ctx, d.ID)
		}
	}
}

// instanceContainers는 노드에서 인스턴스에 속한 aven 컨테이너(app/tcpproxy/cron) 이름이다.
func (m *Manager) instanceContainers(ctx context.Context, nodeID, instanceID string, roles ...string) []string {
	cs, err := m.driverForNode(nodeID).ListManaged(ctx)
	if err != nil {
		return nil
	}
	var out []string
	for name, labels := range cs {
		if labels[node.LabelInstance] != instanceID {
			continue
		}
		if len(roles) == 0 {
			out = append(out, name)
			continue
		}
		for _, r := range roles {
			if labels[node.LabelRole] == r {
				out = append(out, name)
			}
		}
	}
	return out
}

// Stop은 인스턴스의 컨테이너를 멈추고 라우트를 뺀다(status offline).
func (m *Manager) Stop(ctx context.Context, in *store.Instance) error {
	if m.IsSelfService(in.ServiceID) {
		return ErrSelfService
	}
	m.cancelInstance(ctx, in.ID)
	if err := m.Store.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil); err != nil {
		return err
	}
	if err := m.SyncProxy(ctx); err != nil {
		m.Log.Warn("proxy sync", "err", err)
	}
	var cur *store.Deployment
	if in.CurrentDeploymentID != nil {
		cur, _ = m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	}
	if cur != nil {
		m.saveRuntimeLogs(ctx, cur, cur.AllContainers())
	}
	grace := in.Spec.DrainGrace(10)
	if cur != nil {
		grace = cur.Spec.DrainGrace(10)
	}
	// 원격 노드가 꺼져 있으면 다시 연결될 때 reconcile이 고아 컨테이너로 지운다.
	for _, n := range m.instanceNodes(ctx, in) {
		if !m.nodeOnline(n) {
			continue
		}
		drv := m.driverForNode(n)
		for _, c := range m.instanceContainers(ctx, n, in.ID) {
			m.markDrain(c)
			drv.Remove(ctx, c, grace)
			m.undrain(c)
		}
	}
	return nil
}

// Restart는 현재 배포의 컨테이너를 재시작한다.
func (m *Manager) Restart(ctx context.Context, in *store.Instance) error {
	if m.IsSelfService(in.ServiceID) {
		return ErrSelfService
	}
	if in.CurrentDeploymentID == nil {
		return errors.New("no current deployment")
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return err
	}
	if in.Status == store.InstOffline {
		return errors.New("service is stopped; deploy it to start again")
	}
	for _, g := range d.Groups() {
		if !m.nodeOnline(g.NodeID) {
			m.Log.Warn("restart: node offline", "node", g.NodeID)
			continue
		}
		drv := m.driverForNode(g.NodeID)
		for _, c := range g.Containers {
			m.markDrain(c)
			err := drv.Restart(ctx, c, d.Spec.DrainGrace(10))
			m.undrain(c)
			if errors.Is(err, context.Canceled) {
				return err
			}
			if err != nil {
				// 컨테이너가 없으면 reconcile이 다시 만든다.
				m.Log.Warn("restart", "container", c, "err", err)
			}
		}
	}
	if in.Status == store.InstSleeping {
		m.markAwake(in.ID)
		m.Store.SetInstanceStatus(ctx, in.ID, store.InstOnline, nil)
	}
	if in.Status == store.InstCrashed {
		m.Store.SetInstanceStatus(ctx, in.ID, store.InstOnline, nil)
		if d.Status == store.DepCrashed {
			m.Store.SetDeploymentStatus(ctx, d.ID, store.DepLive)
		}
	}
	return m.SyncProxy(ctx)
}

// deleteInstanceResources는 인스턴스의 컨테이너, 볼륨, 로그를 지운다(DB 행은 호출자가 지운다).
func (m *Manager) deleteInstanceResources(ctx context.Context, in *store.Instance) {
	m.cancelInstance(ctx, in.ID)
	m.Store.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil)
	nodes := m.instanceNodes(ctx, in)
	for _, n := range nodes {
		if !m.nodeOnline(n) {
			continue
		}
		drv := m.driverForNode(n)
		for _, c := range m.instanceContainers(ctx, n, in.ID) {
			m.markDrain(c)
			drv.Remove(ctx, c, 5)
			m.undrain(c)
		}
	}
	vols, _ := m.Store.ListVolumesByInstance(ctx, in.ID)
	for _, v := range vols {
		m.deleteVolumeDataOn(ctx, v, nodes)
		m.Store.DeleteVolume(ctx, v.ID)
	}
	deps, _ := m.Store.ListDeployments(ctx, in.ID, 10000)
	for _, d := range deps {
		m.Logs.Remove(d.ID)
		os.Remove(m.runtimeLogPath(d.ID))
		runs, _ := m.Store.ListCronRuns(ctx, d.ID, 10000)
		for _, r := range runs {
			os.Remove(m.cronLogPath(r.ID))
		}
	}
}

func (m *Manager) deleteVolumeData(ctx context.Context, v *store.Volume) {
	m.deleteVolumeDataOn(ctx, v, []string{m.volumeNode(ctx, v)})
}

// deleteVolumeDataOn은 볼륨을 주어진 노드들에서 지우고 백업 파일도 지운다. 꺼진 원격 노드의 볼륨은 남는다(경고).
func (m *Manager) deleteVolumeDataOn(ctx context.Context, v *store.Volume, nodes []string) {
	for _, n := range nodes {
		if !m.nodeOnline(n) {
			m.Log.Warn("remove volume: node offline, volume left in place", "volume", v.DockerName, "node", n)
			continue
		}
		if err := m.driverForNode(n).RemoveVolume(ctx, v.DockerName); err != nil {
			m.Log.Warn("remove volume", "volume", v.DockerName, "node", n, "err", err)
		}
	}
	bs, _ := m.Store.ListBackups(ctx, v.ID)
	for _, b := range bs {
		os.Remove(b.Path)
	}
}

// DeleteService는 모든 환경의 인스턴스를 정리하고 서비스를 지운다.
func (m *Manager) DeleteService(ctx context.Context, svc *store.Service) error {
	if m.IsSelfService(svc.ID) {
		return ErrSelfInUse
	}
	insts, err := m.Store.ListInstancesByService(ctx, svc.ID)
	if err != nil {
		return err
	}
	for _, in := range insts {
		m.deleteInstanceResources(ctx, in)
	}
	if err := m.Store.DeleteService(ctx, svc.ID); err != nil {
		return err
	}
	if err := m.SyncProxy(ctx); err != nil {
		m.Log.Warn("proxy sync", "err", err)
	}
	for _, drv := range m.onlineDrivers(ctx) {
		for _, img := range drv.ImagesByRepo(ctx, m.imageRepo(svc.ID)) {
			drv.RemoveImage(ctx, img)
		}
	}
	return nil
}

// DeleteEnvironment는 환경의 모든 리소스(컨테이너, 볼륨, 네트워크)를 지운다.
func (m *Manager) DeleteEnvironment(ctx context.Context, env *store.Environment) error {
	insts, err := m.Store.ListInstancesByEnv(ctx, env.ID)
	if err != nil {
		return err
	}
	for _, in := range insts {
		if m.IsSelfService(in.ServiceID) {
			return ErrSelfInUse
		}
	}
	for _, in := range insts {
		m.deleteInstanceResources(ctx, in)
	}
	vols, _ := m.Store.ListVolumesByEnv(ctx, env.ID)
	for _, v := range vols {
		m.deleteVolumeData(ctx, v)
	}
	if err := m.Store.DeleteEnvironment(ctx, env.ID); err != nil {
		return err
	}
	if err := m.SyncProxy(ctx); err != nil {
		m.Log.Warn("proxy sync", "err", err)
	}
	m.removeEnvNetwork(ctx, env.ID)
	return nil
}

func (m *Manager) DeleteProject(ctx context.Context, p *store.Project) error {
	svcs, err := m.Store.ListServices(ctx, p.ID)
	if err != nil {
		return err
	}
	for _, s := range svcs {
		if m.IsSelfService(s.ID) {
			return ErrSelfInUse
		}
	}
	for _, s := range svcs {
		if err := m.DeleteService(ctx, s); err != nil {
			return err
		}
	}
	envs, _ := m.Store.ListEnvironments(ctx, p.ID)
	vols, _ := m.Store.ListVolumesByProject(ctx, p.ID)
	for _, v := range vols {
		m.deleteVolumeData(ctx, v)
	}
	if err := m.Store.DeleteProject(ctx, p.ID); err != nil {
		return err
	}
	for _, e := range envs {
		m.removeEnvNetwork(ctx, e.ID)
	}
	return m.SyncProxy(ctx)
}

// EnsureEnvironment는 새 환경의 네트워크를 로컬 노드에 미리 만든다(원격 노드는 배포 때 만든다).
func (m *Manager) EnsureEnvironment(ctx context.Context, env *store.Environment) error {
	return m.ensureEnvNetwork(ctx, env, store.LocalNodeID)
}

// ---------------- reconcile ----------------

// Reconcile은 실제 상태를 DB(desired state)에 맞춘다. 노드마다:
//   - 환경 네트워크가 없으면 만들고 proxy를 붙인다.
//   - live 인스턴스의 현재 컨테이너가 없으면 같은 이미지/환경변수로 다시 띄운다.
//   - 멈춘 컨테이너: restart 정책이 always면 다시 시작, 아니면 crashed로 표시한다.
//   - 어떤 인스턴스에도 속하지 않는 aven 컨테이너(고아)를 지운다.
//   - TCP 포워더(로컬 노드만)와 프록시 설정을 다시 맞춘다.
//
// 꺼진 원격 노드는 건너뛴다(그 노드의 컨테이너는 restart 정책대로 계속 돈다). 다시 연결되면 ReconcileNode가 이어서 맞춘다.
func (m *Manager) Reconcile(ctx context.Context) error {
	if err := m.Docker.EnsureNetwork(ctx); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	err := m.reconcileNode(ctx, store.LocalNodeID)
	for _, n := range m.remoteNodes(ctx) {
		if !m.nodeOnline(n.ID) {
			continue
		}
		if rerr := m.reconcileNode(ctx, n.ID); rerr != nil && ctx.Err() == nil {
			m.Log.Warn("reconcile: remote node", "node", n.ID, "err", rerr)
		}
	}
	if err != nil {
		return err
	}
	if merr := m.SyncMesh(ctx); merr != nil && ctx.Err() == nil {
		m.Log.Warn("reconcile: mesh", "err", merr)
	}
	return m.SyncProxy(ctx)
}

// ReconcileNode는 원격 노드가 (다시) 연결됐을 때 그 노드만 맞추고 그 노드의 라우트를 다시 보낸다.
// 노드가 연결되면 다른 노드의 mesh 설정(peer, import)도 바뀌므로 mesh는 전체를 다시 맞춘다.
func (m *Manager) ReconcileNode(ctx context.Context, nodeID string) error {
	if err := m.reconcileNode(ctx, nodeID); err != nil {
		return err
	}
	if merr := m.SyncMesh(ctx); merr != nil && ctx.Err() == nil {
		m.Log.Warn("reconcile: mesh", "node", nodeID, "err", merr)
	}
	return m.syncProxy(ctx)[nodeKey(nodeID)]
}

func (m *Manager) reconcileNode(ctx context.Context, nodeID string) error {
	nodeID = nodeKey(nodeID)
	local := nodeID == store.LocalNodeID
	drv := m.driverForNode(nodeID)
	envs, err := m.Store.ListEnvironments(ctx, "")
	if err != nil {
		return err
	}
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return err
	}
	// 이 노드에서 돌아야 하는 인스턴스: 현재 배포(없으면 지정 노드)가 이 노드인 것
	var mine []*store.Instance
	envOnNode := map[string]bool{}
	for _, in := range insts {
		for _, n := range m.servingNodes(ctx, in) {
			if n == nodeID {
				mine = append(mine, in)
				envOnNode[in.EnvironmentID] = true
				break
			}
		}
	}
	for _, e := range envs {
		if local || envOnNode[e.ID] {
			if err := m.ensureEnvNetwork(ctx, e, nodeID); err != nil {
				m.Log.Warn("reconcile: env network", "env", e.ID, "node", nodeID, "err", err)
			}
		}
	}
	managed, err := drv.ListManaged(ctx)
	if err != nil {
		return err
	}
	want := map[string]bool{}
	paused := map[string]bool{}
	for _, in := range insts {
		m.mu.Lock()
		paused[in.ID] = m.paused[in.ID]
		m.mu.Unlock()
		// 중지된 인스턴스의 컨테이너는 원하지 않는다(노드가 꺼져 있어 Stop이 못 지운 것을 여기서 지운다).
		if in.CurrentDeploymentID != nil && in.Status != store.InstOffline {
			if d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil {
				for _, c := range d.AllContainers() {
					want[c] = true
				}
			}
		}
		if paused[in.ID] {
			for _, c := range m.instanceContainers(ctx, nodeID, in.ID) {
				want[c] = true
			}
		}
	}
	for _, in := range mine {
		if paused[in.ID] || in.CurrentDeploymentID == nil || !liveStatus(in.Status) || in.CronSchedule != nil {
			continue
		}
		if t, err := m.Load(ctx, in.ID); err == nil {
			m.reconcileInstance(ctx, t, nodeID)
		}
	}
	for _, in := range insts {
		if in.TCPProxy == nil || m.homeNode(ctx, in) != nodeID {
			continue
		}
		want[tcpName(in.ID)] = true
		if paused[in.ID] {
			continue
		}
		if t, err := m.Load(ctx, in.ID); err == nil {
			if err := m.syncTCPProxy(ctx, t); err != nil {
				m.Log.Warn("reconcile: tcp proxy", "instance", in.ID, "node", nodeID, "err", err)
			}
		}
	}
	for name, labels := range managed {
		role := labels[node.LabelRole]
		if role == "proxy" || want[name] || m.draining(name) {
			continue
		}
		switch role {
		case node.RoleApp, node.RoleTCPProxy, "":
		default:
			continue // cron/predeploy/helper는 실행기가 정리한다
		}
		if role == "" && labels[node.LabelLegacyApp] == "" {
			continue
		}
		if !m.isOrphan(ctx, labels, nodeID) {
			continue
		}
		m.Log.Info("reconcile: removing orphan container", "container", name, "node", nodeID)
		drv.Remove(ctx, name, 10)
	}
	return nil
}

// reconcileInstance는 인스턴스 현재 배포의 replica 중 노드 nodeID에 있어야 하는 것을 맞춘다.
func (m *Manager) reconcileInstance(ctx context.Context, t *Target, nodeID string) {
	in := t.Instance
	if in.CurrentDeploymentID == nil { // 목록을 읽은 뒤 삭제·중지되었을 수 있다
		return
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil || d.Image == nil {
		return
	}
	var names []string
	for _, g := range d.Groups() {
		if nodeKey(g.NodeID) == nodeKey(nodeID) {
			names = g.Containers
		}
	}
	if len(names) == 0 {
		return
	}
	drv := m.driverForNode(nodeID)
	states, err := drv.InspectMany(ctx, names)
	if err != nil {
		return
	}
	for _, c := range names {
		if m.draining(c) {
			continue
		}
		st := states[c]
		switch {
		case st == nil:
			m.Log.Warn("reconcile: container missing, recreating", "service", t.Service.Name, "container", c, "node", nodeKey(nodeID))
			m.ensureEnvNetwork(ctx, t.Env, nodeID)
			if err := drv.Run(ctx, m.runSpecNamed(t, d, c, replicaIndex(c))); err != nil {
				m.Log.Error("reconcile: recreate failed", "service", t.Service.Name, "err", err)
			}
		case !st.Running && st.Status != "restarting" && in.Status != store.InstCrashed:
			if d.Spec.RestartPolicy == "always" {
				m.Log.Warn("reconcile: container stopped, starting", "container", c, "status", st.Status)
				drv.Start(ctx, c)
			} else {
				m.markCrashed(ctx, t, d, c, st.ExitCode)
			}
		}
	}
}

func (m *Manager) markCrashed(ctx context.Context, t *Target, d *store.Deployment, container string, exit int) {
	cur, err := m.Store.GetInstance(ctx, t.Instance.ID)
	if err != nil || cur.Status != store.InstOnline || cur.CurrentDeploymentID == nil || *cur.CurrentDeploymentID != d.ID {
		return
	}
	m.Store.SetInstanceStatus(ctx, cur.ID, store.InstCrashed, nil)
	m.Store.SetDeploymentStatus(ctx, d.ID, store.DepCrashed)
	m.emit(events.WithDeployment(ctx, d.ID), t, "deploy.crashed", fmt.Sprintf("%s (%s) 크래시: 컨테이너 %s가 종료되었습니다 (exit %d)", t.Service.Name, t.Env.Name, container, exit))
}

// isOrphan은 최신 DB 상태를 다시 읽어 판단한다. 진행 중인 배포나 방금 live가 된 컨테이너를 지우지 않기 위해서다.
func (m *Manager) isOrphan(ctx context.Context, labels map[string]string, nodeID string) bool {
	instID := labels[node.LabelInstance]
	if instID == "" {
		// v0 컨테이너: 같은 deployment ID가 현재 배포로 옮겨졌으면 유지
		depID := labels[node.LabelDeploy]
		d, err := m.Store.GetDeployment(ctx, depID)
		if errors.Is(err, store.ErrNotFound) {
			return true
		}
		return err == nil && !m.isCurrent(ctx, d)
	}
	in, err := m.Store.GetInstance(ctx, instID)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	if err != nil {
		return false
	}
	if labels[node.LabelRole] == node.RoleTCPProxy {
		// 포워더는 인스턴스가 도는 노드에만 있어야 한다.
		return in.TCPProxy == nil || m.homeNode(ctx, in) != nodeKey(nodeID)
	}
	depID := labels[node.LabelDeploy]
	if in.CurrentDeploymentID != nil && *in.CurrentDeploymentID == depID {
		// 중지된(offline) 인스턴스의 현재 컨테이너는 Stop이 지운다. 노드가 꺼져 있어 못 지웠으면 여기서 지운다.
		return in.Status == store.InstOffline
	}
	d, err := m.Store.GetDeployment(ctx, depID)
	if errors.Is(err, store.ErrNotFound) {
		return true
	}
	return err == nil && d.Status.Terminal()
}

func (m *Manager) ReconcileLoop(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		if err := m.Reconcile(ctx); err != nil && ctx.Err() == nil {
			m.Log.Error("reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// WatchCrashes는 로컬 docker events로 live 컨테이너의 종료를 감시한다(원격 노드는 agent가 보내는 events.follow, nodestreams.go).
// 재시작 정책으로 다시 뜨면 service.restarted 이벤트만 남기고, 끝내 멈추면 crashed로 표시한다.
func (m *Manager) WatchCrashes(ctx context.Context) {
	for ctx.Err() == nil {
		m.Docker.Events(ctx, func(ev node.ContainerEvent) { m.onContainerEvent(ctx, store.LocalNodeID, ev) })
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

// onContainerEvent는 노드(로컬 또는 원격)에서 온 컨테이너 이벤트를 처리한다. 의미는 노드와 상관없이 같다.
func (m *Manager) onContainerEvent(ctx context.Context, nodeID string, ev node.ContainerEvent) {
	if ev.Action != "die" || ev.Labels[node.LabelRole] != node.RoleApp || m.draining(ev.Name) {
		return
	}
	go m.handleDie(ctx, nodeID, ev)
}

func (m *Manager) handleDie(ctx context.Context, nodeID string, ev node.ContainerEvent) {
	time.Sleep(3 * time.Second)
	if m.draining(ev.Name) {
		return
	}
	t, err := m.Load(ctx, ev.Labels[node.LabelInstance])
	if err != nil || t.Instance.CurrentDeploymentID == nil || *t.Instance.CurrentDeploymentID != ev.Labels[node.LabelDeploy] {
		return
	}
	if t.Instance.Status != store.InstOnline {
		return
	}
	d, err := m.Store.GetDeployment(ctx, *t.Instance.CurrentDeploymentID)
	if err != nil || nodeKey(d.NodeOf(ev.Name)) != nodeKey(nodeID) {
		return
	}
	st, err := m.driverForNode(nodeID).Inspect(ctx, ev.Name)
	if err != nil {
		return // 지워졌으면 reconcile이 다시 만든다
	}
	if st.Running || st.Status == "restarting" {
		m.emit(ctx, t, "service.restarted", fmt.Sprintf("%s (%s): 컨테이너 %s가 종료되어 (exit %s) 다시 시작했습니다", t.Service.Name, t.Env.Name, ev.Name, ev.Exit))
		return
	}
	m.markCrashed(ctx, t, d, ev.Name, st.ExitCode)
}

// ---------------- upload extraction ----------------

// extractTarGz는 tar.gz를 dst에 푼다. 경로 탈출과 링크는 막는다.
func extractTarGz(path, dst string) error { return tarx.ExtractFile(path, dst) }
