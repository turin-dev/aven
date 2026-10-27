package deploy

import (
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentpki"
	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/store"
	"github.com/turin-dev/aven/internal/vars"
)

// 둘 이상의 mesh 노드에 걸친 환경마다, 각 노드는 자기 노드의 그 환경 서비스를 다른 노드들에 export하고
// 다른 노드에 있는 그 환경 서비스를 import한다(forwarder 컨테이너 + 로컬 포트).
// reconcile(30초), 노드 연결, 배포가 live가 될 때 다시 맞춘다.

// MeshNode는 mesh를 쓸 수 있는 노드다.
type MeshNode struct {
	ID      string
	Addr    string   // 사설 주소:mesh 포트
	Serials []string // 유효한 인증서 serial
	// Offline은 agent가 연결돼 있지 않은 노드다. DesiredMesh는 이 노드를 peer·import 대상에서 뺀다
	// (다른 노드의 forwarder가 닿지 않는 노드로 연결을 보내지 않게; failover 후보가 있으면 그 노드로 간다).
	Offline bool
}

// MeshPlacement는 환경의 서비스 하나가 도는 노드와 내보낼 포트다.
type MeshPlacement struct {
	ProjectID          string
	EnvID, EnvName     string
	ServiceID, Service string
	Node               string
	Ports              []int
	// DNSName은 live 배포의 private_dns_name이다(비우면 서비스 이름만). import forwarder에 별칭으로 붙는다.
	DNSName string
}

// DesiredMesh는 노드별 mesh 설정을 계산한다. mesh 노드가 아닌 곳에 있는 서비스는 빠진다(그 노드와는 닿지 않는다).
// 모든 mesh 노드가 서로 peer다(상태 확인). export는 같은 환경의 다른 노드만 쓸 수 있다.
// 꺼진 노드(Offline)는 처음부터 없는 것으로 본다.
func DesiredMesh(all []MeshNode, ps []MeshPlacement) map[string]mesh.Config {
	var nodes []MeshNode
	for _, n := range all {
		if !n.Offline {
			nodes = append(nodes, n)
		}
	}
	byID := map[string]MeshNode{}
	for _, n := range nodes {
		byID[n.ID] = n
	}
	out := map[string]mesh.Config{}
	for _, n := range nodes {
		c := mesh.Config{}
		for _, o := range nodes {
			if o.ID != n.ID {
				c.Peers = append(c.Peers, mesh.Peer{NodeID: o.ID, Address: o.Addr, Serials: append([]string(nil), o.Serials...)})
			}
		}
		out[n.ID] = c
	}
	byEnv := map[string][]MeshPlacement{}
	for _, p := range ps {
		if _, ok := byID[p.Node]; ok && len(p.Ports) > 0 {
			byEnv[p.EnvID] = append(byEnv[p.EnvID], p)
		}
	}
	for _, list := range byEnv {
		envNodes := map[string]bool{}
		for _, p := range list {
			envNodes[p.Node] = true
		}
		if len(envNodes) < 2 {
			continue
		}
		for nid := range envNodes {
			c := out[nid]
			var others []string
			for o := range envNodes {
				if o != nid {
					others = append(others, o)
				}
			}
			sort.Strings(others)
			// 같은 서비스의 replica가 이 노드에도 있으면 가져오지 않는다(로컬 replica가 같은 private domain alias로 답한다).
			// 여러 다른 노드에 있으면 처음 나온 노드(주 노드)를 Node로, 모두를 failover 후보(Nodes)로 둔다.
			local := map[string]bool{}
			for _, p := range list {
				if p.Node == nid {
					local[p.ServiceID] = true
				}
			}
			imported := map[string]int{} // service ID → c.Imports 위치
			for _, p := range list {
				switch {
				case p.Node == nid:
					c.Exports = append(c.Exports, mesh.Export{EnvID: p.EnvID, ServiceID: p.ServiceID, Service: p.Service, Ports: append([]int(nil), p.Ports...), Peers: others})
				case local[p.ServiceID]:
				default:
					if i, ok := imported[p.ServiceID]; ok {
						im := &c.Imports[i]
						if len(im.Nodes) == 0 {
							im.Nodes = []string{im.Node}
						}
						if !slices.Contains(im.Nodes, p.Node) {
							im.Nodes = append(im.Nodes, p.Node)
						}
						continue
					}
					imported[p.ServiceID] = len(c.Imports)
					var aliases []string
					// 0=service, 1=keel DNS, 2=aven DNS. 그 뒤가 사용자가 붙인 이름이다.
					if extra := PrivateAliases(p.Service, p.EnvName, p.DNSName)[3:]; len(extra) > 0 {
						aliases = extra
					}
					c.Imports = append(c.Imports, mesh.Import{EnvID: p.EnvID, EnvName: p.EnvName, Service: p.Service, Ports: append([]int(nil), p.Ports...), Node: p.Node, Aliases: aliases})
				}
			}
			for i := range c.Imports {
				if len(c.Imports[i].Nodes) == 1 {
					c.Imports[i].Nodes = nil
				}
			}
			out[nid] = c
		}
	}
	for id, c := range out {
		c.Normalize()
		out[id] = c
	}
	return out
}

// meshState는 Manager의 mesh 상태다.
type meshState struct {
	mu        sync.Mutex // SyncMesh 직렬화
	statusMu  sync.Mutex
	status    map[string]mesh.Status // 원격 노드: 마지막 mesh.ensure 결과
	noAliases map[string]time.Time   // 원격 노드: Import.aliases를 모른다고 거부한 시각(옛 agent)
	online    map[string]bool        // 원격 노드: 마지막으로 본 연결 상태(node.unavailable 전이 감지)
	localCert *tls.Certificate
	localSer  string
}

// StartLocalMesh는 로컬 노드(aven-server)의 mesh gateway를 띄운다. KEEL_MESH=off거나 사설 IP가 없으면(auto) 끈 채로 둔다.
func (m *Manager) StartLocalMesh(ctx context.Context) error {
	mode := strings.ToLower(strings.TrimSpace(m.Cfg.Mesh))
	if mode == "off" {
		return nil
	}
	if mode != "" && mode != "auto" && mode != "on" {
		return fmt.Errorf("KEEL_MESH must be on, off or auto (got %q)", m.Cfg.Mesh)
	}
	ip, ok := mesh.ParsePrivateIP(m.Cfg.PrivateAddress)
	if !ok {
		if mode == "on" {
			return errors.New("KEEL_MESH=on needs KEEL_PRIVATE_ADDRESS to be a private IP (RFC1918, ULA or 100.64.0.0/10)")
		}
		return nil
	}
	if m.MeshCA == nil {
		return errors.New("mesh needs the agent CA")
	}
	port := m.Cfg.MeshPort
	if port == 0 {
		port = mesh.DefaultPort
	}
	if port < 1024 || port > 65535 {
		return errors.New("KEEL_MESH_PORT must be 1024..65535")
	}
	if err := m.issueLocalMeshCert(); err != nil {
		return err
	}
	m.LocalMesh = &mesh.Gateway{NodeID: store.LocalNodeID, Listen: netip.AddrPortFrom(ip, uint16(port)).String(), Roots: m.MeshCA.Pool(),
		Cert: m.localMeshCert, Resolve: &mesh.DockerResolver{Docker: m.Docker}, Log: m.Log}
	go m.LocalMesh.Run(ctx)
	return nil
}

// issueLocalMeshCert는 로컬 노드의 mesh 인증서를 새로 발급한다(키는 메모리에만 있고 프로세스를 떠나지 않는다).
func (m *Manager) issueLocalMeshCert() error {
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	is, err := m.MeshCA.Issue(store.LocalNodeID, pub)
	if err != nil {
		return err
	}
	c, err := agentpki.TLSCertificate(is.CertPEM, priv)
	if err != nil {
		return err
	}
	m.meshSt.statusMu.Lock()
	m.meshSt.localCert, m.meshSt.localSer = c, is.Serial
	m.meshSt.statusMu.Unlock()
	return nil
}

func (m *Manager) localMeshCert() (*tls.Certificate, error) {
	m.meshSt.statusMu.Lock()
	defer m.meshSt.statusMu.Unlock()
	if m.meshSt.localCert == nil {
		return nil, errors.New("no local mesh certificate")
	}
	return m.meshSt.localCert, nil
}

// meshNodeReason은 노드가 mesh를 쓸 수 있으면 (주소, "")를, 아니면 ("", 이유)를 돌려준다.
func (m *Manager) meshNodeReason(n *store.Node, serials []string) (string, string) {
	if isLocal(n.ID) {
		if m.LocalMesh == nil {
			if strings.EqualFold(m.Cfg.Mesh, "off") {
				return "", "KEEL_MESH=off"
			}
			return "", "KEEL_PRIVATE_ADDRESS가 사설 IP가 아니어서 mesh를 쓸 수 없습니다"
		}
		return m.LocalMesh.Listen, ""
	}
	if n.Address.Private == nil || *n.Address.Private == "" {
		return "", "노드에 사설 주소(--private-address)가 없어 mesh를 쓸 수 없습니다"
	}
	ip, ok := mesh.ParsePrivateIP(*n.Address.Private)
	if !ok {
		return "", "노드의 사설 주소가 사설 IP가 아니어서 mesh를 쓸 수 없습니다"
	}
	if n.MeshPort < 1024 || n.MeshPort > 65535 {
		return "", "agent가 mesh를 지원하지 않거나 꺼져 있습니다(--mesh, 인증서 기반 연결 필요)"
	}
	if len(serials) == 0 {
		return "", "노드 인증서가 없어 mesh를 쓸 수 없습니다"
	}
	return netip.AddrPortFrom(ip, uint16(n.MeshPort)).String(), ""
}

// meshNodes는 mesh를 쓸 수 있는 노드 목록이다(꺼진 노드도 주소를 알면 포함: 상태 확인과 안정된 설정을 위해).
func (m *Manager) meshNodes(ctx context.Context) ([]MeshNode, error) {
	nodes, err := m.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	serials, err := m.Store.ActiveNodeCertSerials(ctx)
	if err != nil {
		return nil, err
	}
	var out []MeshNode
	for _, n := range nodes {
		s := serials[n.ID]
		if isLocal(n.ID) {
			m.meshSt.statusMu.Lock()
			if m.meshSt.localSer != "" {
				s = []string{m.meshSt.localSer}
			}
			m.meshSt.statusMu.Unlock()
		}
		if addr, reason := m.meshNodeReason(n, s); reason == "" {
			out = append(out, MeshNode{ID: nodeKey(n.ID), Addr: addr, Serials: s})
		}
	}
	return out, nil
}

// MeshPorts는 인스턴스가 mesh로 내보내는 포트다: private_ports, 없으면 [PORT](live 배포가 실제로 쓰는 포트).
func MeshPorts(in *store.Instance, d *store.Deployment) []int {
	if len(in.PrivatePorts) > 0 {
		return append([]int(nil), in.PrivatePorts...)
	}
	if in.Port != nil && *in.Port > 0 {
		return []int{*in.Port}
	}
	if d != nil {
		return []int{d.Spec.EffectivePort(d.Env)}
	}
	return []int{8080}
}

// meshPlacements는 배포된(중지되지 않은) 서비스 인스턴스의 배치다. cron 서비스는 listen하지 않으므로 뺀다.
func (m *Manager) meshPlacements(ctx context.Context) ([]MeshPlacement, error) {
	envs, err := m.Store.ListEnvironments(ctx, "")
	if err != nil {
		return nil, err
	}
	envByID := map[string]*store.Environment{}
	for _, e := range envs {
		envByID[e.ID] = e
	}
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	svcs := map[string]*store.Service{}
	loaded := map[string]bool{}
	var out []MeshPlacement
	for _, in := range insts {
		if in.CurrentDeploymentID == nil || in.Status == store.InstOffline || in.CronSchedule != nil {
			continue
		}
		env := envByID[in.EnvironmentID]
		if env == nil {
			continue
		}
		if !loaded[env.ProjectID] {
			loaded[env.ProjectID] = true
			list, err := m.Store.ListServices(ctx, env.ProjectID)
			if err != nil {
				return nil, err
			}
			for _, s := range list {
				svcs[s.ID] = s
			}
		}
		svc := svcs[in.ServiceID]
		if svc == nil || !mesh.ValidNames(env.ID, env.Name, svc.ID, svc.Name) {
			continue
		}
		d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		if err != nil {
			continue
		}
		// placement로 여러 노드에 replica가 있으면 노드마다 하나씩 넣는다(주 노드 먼저). replica가 없는 노드는 뺀다.
		added := false
		for _, g := range d.Groups() {
			if len(g.Containers) == 0 {
				continue
			}
			added = true
			out = append(out, MeshPlacement{ProjectID: env.ProjectID, EnvID: env.ID, EnvName: env.Name, ServiceID: svc.ID, Service: svc.Name, Node: nodeKey(g.NodeID), Ports: MeshPorts(in, d), DNSName: d.Spec.PrivateDNSName})
		}
		if !added { // 잠든 서비스 등: 배포 노드에 있는 것으로 본다
			out = append(out, MeshPlacement{ProjectID: env.ProjectID, EnvID: env.ID, EnvName: env.Name, ServiceID: svc.ID, Service: svc.Name, Node: nodeKey(d.NodeID), Ports: MeshPorts(in, d), DNSName: d.Spec.PrivateDNSName})
		}
	}
	return out, nil
}

// SyncMesh는 모든 mesh 노드에 원하는 상태를 보낸다(로컬은 직접 반영). 꺼진 원격 노드는 건너뛴다.
func (m *Manager) SyncMesh(ctx context.Context) error {
	if m.Store == nil {
		return nil
	}
	m.meshSt.mu.Lock()
	defer m.meshSt.mu.Unlock()
	if m.LocalMesh != nil {
		if c, err := m.localMeshCert(); err == nil && c.Leaf != nil && agentpki.NeedsRenewal(c.Leaf.NotAfter, time.Now()) {
			if err := m.issueLocalMeshCert(); err != nil {
				m.Log.Warn("mesh: local certificate renewal", "err", err)
			}
		}
	}
	nodes, err := m.meshNodes(ctx)
	if err != nil {
		return err
	}
	if len(nodes) == 0 && len(m.remoteNodes(ctx)) == 0 {
		return nil
	}
	ps, err := m.meshPlacements(ctx)
	if err != nil {
		return err
	}
	m.noteNodeAvailability(ctx, ps)
	if len(nodes) == 0 {
		return nil
	}
	for i := range nodes {
		nodes[i].Offline = !m.nodeOnline(nodes[i].ID)
	}
	cfgs := DesiredMesh(nodes, ps)
	var errs []error
	for _, n := range nodes {
		if n.Offline {
			m.forgetNoAliases(n.ID)
			continue
		}
		cfg := cfgs[n.ID]
		if isLocal(n.ID) {
			if m.LocalMesh == nil || m.Docker == nil {
				continue
			}
			if err := mesh.Validate(cfg, store.LocalNodeID); err != nil {
				errs = append(errs, fmt.Errorf("local mesh config: %w", err))
				continue
			}
			if _, err := mesh.Ensure(ctx, m.LocalMesh, m.Docker, cfg); err != nil {
				m.Log.Warn("mesh: local", "err", err)
			}
			continue
		}
		if !m.nodeOnline(n.ID) {
			continue
		}
		var st mesh.Status
		cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := m.sendMeshConfig(cctx, n.ID, cfg, &st)
		cancel()
		if err != nil {
			st = mesh.Status{Enabled: true, Peers: []mesh.PeerStatus{}, Error: err.Error()}
			errs = append(errs, fmt.Errorf("node %s: %w", n.ID, err))
		}
		m.meshSt.statusMu.Lock()
		if m.meshSt.status == nil {
			m.meshSt.status = map[string]mesh.Status{}
		}
		m.meshSt.status[n.ID] = st
		m.meshSt.statusMu.Unlock()
	}
	return errors.Join(errs...)
}

// syncMeshAsync는 배포가 live가 된 뒤 mesh를 다시 맞춘다(배포 경로를 막지 않는다).
func (m *Manager) syncMeshAsync() {
	if m.Store == nil || (m.LocalMesh == nil && m.Agents == nil) {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := m.SyncMesh(ctx); err != nil && m.Log != nil {
			m.Log.Warn("mesh sync", "err", err)
		}
	}()
}

// MeshStatus는 노드의 mesh 상태다(GET /api/nodes). 원격 노드는 마지막 mesh.ensure 결과(최대 30초 전)다.
func (m *Manager) MeshStatus(ctx context.Context, n *store.Node) mesh.Status {
	serials := []string{"-"}
	if !isLocal(n.ID) {
		all, _ := m.Store.ActiveNodeCertSerials(ctx)
		serials = all[n.ID]
	}
	addr, reason := m.meshNodeReason(n, serials)
	if reason != "" {
		return mesh.Status{Enabled: false, Peers: []mesh.PeerStatus{}, Reason: reason}
	}
	if isLocal(n.ID) {
		return m.LocalMesh.Status()
	}
	m.meshSt.statusMu.Lock()
	st, ok := m.meshSt.status[n.ID]
	m.meshSt.statusMu.Unlock()
	if !ok {
		return mesh.Status{Enabled: true, Listen: &addr, Peers: []mesh.PeerStatus{}}
	}
	if st.Listen == nil {
		st.Listen = &addr
	}
	return st
}

// CrossRef는 변수 하나가 참조하는 다른 노드의 서비스다. Mesh는 그 노드들과 mesh로 닿는지다.
type CrossRef struct {
	Services []string
	Mesh     bool
}

// CrossNodeRefs는 환경 envID에서 서비스 self(노드 home)의 사용자 변수 user가 다른 노드의 서비스를 참조하는 것을 찾는다.
// 직접 참조(${{svc.X}})와 공유 변수를 거친 참조(${{shared.X}} 안의 ${{svc.X}})를 본다.
func (m *Manager) CrossNodeRefs(ctx context.Context, envID, self, home string, user map[string]string) (map[string]CrossRef, error) {
	insts, err := m.Store.ListInstancesByEnv(ctx, envID)
	if err != nil {
		return nil, err
	}
	env, err := m.Store.GetEnvironment(ctx, envID)
	if err != nil {
		return nil, err
	}
	svcs, err := m.Store.ListServices(ctx, env.ProjectID)
	if err != nil {
		return nil, err
	}
	names := map[string]string{}
	for _, s := range svcs {
		names[s.ID] = s.Name
	}
	nodeOf := map[string]string{}
	for _, in := range insts {
		if n := names[in.ServiceID]; n != "" {
			nodeOf[n] = m.homeNode(ctx, in)
		}
	}
	shared, _ := m.Store.GetSharedVars(ctx, envID)
	home = nodeKey(home)
	var walk func(raw string, seen map[string]bool, out map[string]bool)
	walk = func(raw string, seen map[string]bool, out map[string]bool) {
		for _, ref := range vars.References(raw) {
			scope, name, ok := strings.Cut(ref, ".")
			if !ok {
				continue
			}
			if scope == "shared" {
				if !seen[name] {
					seen[name] = true
					walk(shared[name], seen, out)
				}
				continue
			}
			if scope != self {
				if n, ok := nodeOf[scope]; ok && n != home {
					out[scope] = true
				}
			}
		}
	}
	out := map[string]CrossRef{}
	capable := map[string]bool{}
	if nodes, err := m.meshNodes(ctx); err == nil {
		for _, n := range nodes {
			capable[n.ID] = true
		}
	}
	for k, raw := range user {
		hit := map[string]bool{}
		walk(raw, map[string]bool{}, hit)
		if len(hit) == 0 {
			continue
		}
		cr := CrossRef{Mesh: capable[home]}
		for s := range hit {
			cr.Services = append(cr.Services, s)
			cr.Mesh = cr.Mesh && capable[nodeOf[s]]
		}
		sort.Strings(cr.Services)
		out[k] = cr
	}
	return out, nil
}

// logCrossNode는 다른 노드의 서비스를 참조하는 변수를 배포 로그에 남긴다. mesh로 닿지 않으면 경고한다
// (그 private domain은 이 노드에서 풀리지 않는다; 배포는 계속한다).
func (m *Manager) logCrossNode(ctx context.Context, t *Target, d *store.Deployment, b *logbook.Book) {
	refs, err := m.CrossNodeRefs(ctx, t.Env.ID, t.Service.Name, d.NodeID, d.VarsRaw)
	if err != nil || len(refs) == 0 {
		return
	}
	viaMesh, unreachable := map[string]bool{}, map[string]bool{}
	for _, r := range refs {
		for _, s := range r.Services {
			if r.Mesh {
				viaMesh[s] = true
			} else {
				unreachable[s] = true
			}
		}
	}
	keys := func(set map[string]bool) string {
		var out []string
		for k := range set {
			out = append(out, k)
		}
		sort.Strings(out)
		return strings.Join(out, ", ")
	}
	if len(viaMesh) > 0 {
		b.Printf("다른 노드의 서비스를 노드 간 사설 네트워크(mesh)로 연결합니다: %s", keys(viaMesh))
	}
	if len(unreachable) > 0 {
		b.Printf("WARN: 다른 노드에 있는 서비스를 참조하지만 mesh를 쓸 수 없어 private domain이 이 노드에서 풀리지 않습니다: %s (두 노드 모두 사설 주소와 mesh가 필요합니다. 공개 TCP proxy 주소를 쓰거나 같은 노드로 옮기세요)", keys(unreachable))
	}
}
