package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/deploy"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// GET /api/services/{sid}/instances/{eid}/networking: 설정 > 네트워킹 화면이 한 번에 쓰는 정보다
// (감지된 포트, 실제 라우팅 포트, 도메인, TCP 프록시, 사설 네트워크, 아웃바운드). 읽기 전용이고 viewer 권한이다.

// networkingTimeout은 노드에 묻는 작업(포트 감지, EXPOSE, inspect) 전체의 상한이다. agent가 느려도 화면이 기다리지 않게 짧게 둔다.
const networkingTimeout = 3 * time.Second

// networkingBackend는 핸들러가 노드에 묻는 작업이다. 기본은 deploy.Manager, 테스트는 netImpl로 바꾼다.
type networkingBackend interface {
	ListeningPorts(ctx context.Context, nodeID, instanceID string) (*node.ListeningPorts, error)
	ImageExposedPortsOn(ctx context.Context, nodeID, image string) []int
	InspectOn(ctx context.Context, nodeID, name string) (*node.ContainerState, error)
}

func (s *Server) netBackend() networkingBackend {
	if s.netImpl != nil {
		return s.netImpl
	}
	return s.Deploy
}

// 감지된 포트의 출처.
const (
	portSrcListening = "listening" // 컨테이너가 실제로 listen 중(프록시가 닿는 주소)
	portSrcExpose    = "expose"    // 이미지 EXPOSE
	portSrcEnv       = "env"       // 사용자가 정한 PORT 변수
	portSrcLoopback  = "loopback"  // 127.0.0.1/::1에서만 listen(프록시가 닿지 못한다, UI 경고)
)

type detectedPort struct {
	Port   int    `json:"port"`
	Source string `json:"source"`
}

// portDetection은 listen 포트 감지가 됐는지다. source는 "proc"(읽음) 또는 "unavailable"(옛 agent, 오프라인, 실행 중인 컨테이너 없음 등).
type portDetection struct {
	Source     string `json:"source"`
	Reason     string `json:"reason,omitempty"`
	Node       string `json:"node,omitempty"`
	Containers int    `json:"containers"`
}

type tcpProxyView struct {
	Enabled       bool   `json:"enabled"`
	Host          string `json:"host"`
	PublicPort    *int   `json:"public_port"`
	ContainerPort *int   `json:"container_port"`
	Available     bool   `json:"available"`
	Reason        string `json:"reason,omitempty"`
}

type privateView struct {
	DNSName   string   `json:"dns_name"`
	FQDN      string   `json:"fqdn"`
	Aliases   []string `json:"aliases"`
	IPv4      []string `json:"ipv4"`
	IPv6      []string `json:"ipv6"`
	MeshPorts []int    `json:"mesh_ports"`
}

type staticIPs struct {
	IPv4 *string `json:"ipv4"`
	IPv6 *string `json:"ipv6"`
}

type outboundView struct {
	// IPv6Enabled는 이 서비스의 설정(outbound_ipv6, 기본 켜짐)이다.
	IPv6Enabled bool `json:"ipv6_enabled"`
	// NodeIPv6Egress는 이 노드의 환경 네트워크 컨테이너가 IPv6로 밖에 나갈 수 있는지다(null: 확인하지 못함, EgressReason에 이유).
	NodeIPv6Egress *bool  `json:"node_ipv6_egress"`
	EgressReason   string `json:"egress_reason,omitempty"`
	// EnforceSupported는 이 노드에서 서비스별로 IPv6 egress를 막을 수 있는지다(null: 옛 agent, 오프라인 등으로 모름).
	EnforceSupported *bool     `json:"enforce_supported"`
	EnforceReason    string    `json:"enforce_reason,omitempty"`
	Node             string    `json:"node"`
	StaticIPs        staticIPs `json:"static_ips"`
}

// egressBackend는 아웃바운드 IPv6 상태와 차단 규칙 작업이다. 기본은 deploy.Manager, 테스트는 netImpl이 구현하면 그것을 쓴다.
type egressBackend interface {
	EgressStatus(ctx context.Context, nodeID, envID string) (*node.EgressStatus, error)
	EgressNodes(ctx context.Context, in *store.Instance) []string
	SyncEgress(ctx context.Context) error
}

// noEgress는 netImpl이 egress를 모르는 테스트에서 쓴다(실제 노드에 묻지 않는다).
type noEgress struct{}

func (noEgress) EgressStatus(context.Context, string, string) (*node.EgressStatus, error) {
	return nil, deploy.ErrEgressAgentUnsupported
}
func (noEgress) EgressNodes(context.Context, *store.Instance) []string { return nil }
func (noEgress) SyncEgress(context.Context) error                      { return nil }

func (s *Server) egressBackend() egressBackend {
	if s.netImpl != nil {
		if eb, ok := s.netImpl.(egressBackend); ok {
			return eb
		}
		return noEgress{}
	}
	return s.Deploy
}

// egressTimeout은 networking 화면에서 egress 상태를 기다리는 상한이다(노드 쪽은 30초 캐시).
const egressTimeout = 2 * time.Second

// outboundInfo는 아웃바운드 IPv6 설정과 노드(nodeID)의 egress 상태, 고정 외부 IP다.
func (s *Server) outboundInfo(ctx context.Context, in *store.Instance, envID, nodeID string) outboundView {
	v := outboundView{IPv6Enabled: in.OutboundIPv6 == nil || *in.OutboundIPv6, Node: nodeID}
	ctx, cancel := context.WithTimeout(ctx, egressTimeout)
	defer cancel()
	st, err := s.egressBackend().EgressStatus(ctx, nodeID, envID)
	switch {
	case err != nil:
		v.EgressReason = egressErrReason(err)
		v.EnforceReason = v.EgressReason
	case st != nil:
		v.NodeIPv6Egress, v.EgressReason = st.IPv6, st.Reason
		enf := st.Enforce
		v.EnforceSupported, v.EnforceReason = &enf, st.EnforceReason
	}
	return v
}

// egressToggleTimeout은 토글 때 노드에 차단 지원 여부를 묻고 규칙을 맞추는 상한이다.
const egressToggleTimeout = 15 * time.Second

// patchOutboundIPv6은 PATCH instance의 outbound_ipv6(true|false|null, null은 기본값 켜짐)을 in에 반영한다.
// 바뀌었으면 changed=true다. 끄려는데 서비스가 있을 수 있는 노드 중 하나라도 차단을 지원하지 않으면(ip6tables나 Docker의
// DOCKER-USER가 없음, 옛 agent, 오프라인) 409로 거부하고 ok=false다. 켜는 것은 항상 된다.
func (s *Server) patchOutboundIPv6(w http.ResponseWriter, r *http.Request, in *store.Instance, envID string, patch map[string]json.RawMessage) (changed, ok bool) {
	raw, given := patch["outbound_ipv6"]
	if !given {
		return false, true
	}
	var v *bool
	if err := json.Unmarshal(raw, &v); err != nil {
		writeErr(w, 400, "outbound_ipv6 must be a boolean or null")
		return false, false
	}
	on := v == nil || *v
	if was := in.OutboundIPv6 == nil || *in.OutboundIPv6; on == was {
		return false, true
	}
	if on {
		in.OutboundIPv6 = nil // 기본값(켜짐)은 저장하지 않는다
		return true, true
	}
	if msg := s.egressBlockUnsupported(r.Context(), in, envID); msg != "" {
		writeErr(w, 409, msg)
		return false, false
	}
	off := false
	in.OutboundIPv6 = &off
	return true, true
}

// egressBlockUnsupported는 인스턴스가 있을 수 있는 노드 중 IPv6 차단을 못 하는 노드가 있으면 그 이유(한국어)다. 모두 되면 "".
func (s *Server) egressBlockUnsupported(ctx context.Context, in *store.Instance, envID string) string {
	eb := s.egressBackend()
	nodes := eb.EgressNodes(ctx, in)
	if len(nodes) == 0 {
		nodes = []string{store.NormNode(in.NodeID)}
	}
	ctx, cancel := context.WithTimeout(ctx, egressToggleTimeout)
	defer cancel()
	for _, n := range nodes {
		st, err := eb.EgressStatus(ctx, n, envID)
		reason := ""
		switch {
		case err != nil:
			reason = egressErrReason(err)
		case st == nil:
			reason = "노드의 IPv6 상태를 확인하지 못했습니다"
		case !st.Enforce:
			reason = st.EnforceReason
			if reason == "" {
				reason = "ip6tables DOCKER-USER 체인을 쓸 수 없습니다"
			}
		}
		if reason != "" {
			return fmt.Sprintf("%s 노드에서는 아웃바운드 IPv6를 끌 수 없습니다: %s", n, reason)
		}
	}
	return ""
}

// applyOutboundIPv6은 저장된 outbound_ipv6을 노드의 차단 규칙에 바로 반영하고 이벤트를 남긴다.
// 규칙 반영이 실패해도 설정은 저장되어 있고 EgressLoop(30초)가 다시 맞춘다.
func (s *Server) applyOutboundIPv6(ctx context.Context, c *instCtx, in *store.Instance) {
	state := "켰습니다"
	if in.OutboundIPv6 != nil && !*in.OutboundIPv6 {
		state = "껐습니다"
	}
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "service.updated", fmt.Sprintf("%s (%s): 아웃바운드 IPv6를 %s", c.svc.Name, c.env.Name, state))
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), egressToggleTimeout)
	defer cancel()
	if err := s.egressBackend().SyncEgress(sctx); err != nil {
		s.Log.Warn("egress6 sync", "instance", in.ID, "err", err)
	}
}

// egressErrReason은 egress 상태를 못 읽은 이유를 화면용 문장으로 바꾼다.
func egressErrReason(err error) string {
	switch {
	case errors.Is(err, deploy.ErrEgressAgentUnsupported):
		return deploy.ErrEgressAgentUnsupported.Error()
	case errors.Is(err, agentprotocol.ErrOffline):
		return "노드가 연결되어 있지 않습니다"
	case errors.Is(err, context.DeadlineExceeded):
		return "노드 응답이 늦어 IPv6 상태를 확인하지 못했습니다"
	default:
		return "노드의 IPv6 상태를 확인하지 못했습니다"
	}
}

type networkingView struct {
	DetectedPorts []detectedPort `json:"detected_ports"`
	PortDetection portDetection  `json:"port_detection"`
	EffectivePort int            `json:"effective_port"`
	Domains       []domainView   `json:"domains"`
	TCPProxy      tcpProxyView   `json:"tcp_proxy"`
	Private       privateView    `json:"private"`
	Outbound      outboundView   `json:"outbound"`
}

// tcpNoPublicIPReason은 공인 IP가 없는 노드(Cloudflare 터널 노드)에서 TCP 프록시를 쓸 수 없는 이유다.
const tcpNoPublicIPReason = "이 노드는 공개 IP가 없어 TCP 프록시를 열 수 없습니다"

func (s *Server) getNetworking(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	in := c.in
	var d *store.Deployment
	if in.CurrentDeploymentID != nil {
		d, _ = s.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	}
	home := s.Deploy.HomeNode(ctx, in)

	// 노드에 묻는 작업은 짧은 시간 안에 동시에 한다. egress는 컨테이너가 도는 노드(없으면 주 노드)에 묻는다.
	egressNode := home
	if d != nil {
		for _, g := range d.Groups() {
			if len(g.Containers) > 0 {
				egressNode = store.NormNode(g.NodeID)
				break
			}
		}
	}
	outc := make(chan outboundView, 1)
	go func() { outc <- s.outboundInfo(ctx, in, c.env.ID, egressNode) }()
	probe := s.probeNetworking(ctx, in, c.env, d)

	v := networkingView{
		DetectedPorts: mergeDetectedPorts(probe.listening, probe.expose, envPorts(in, d)),
		PortDetection: probe.detection,
		EffectivePort: effectivePort(in, d),
		Domains:       []domainView{},
		TCPProxy:      s.tcpProxyInfo(ctx, in),
		Private:       privateInfo(c.svc, c.env, in, d, probe.state),
	}
	for _, dm := range in.Domains {
		v.Domains = append(v.Domains, s.domainView(ctx, in, dm))
	}
	v6, v4 := s.nodeAddr(ctx, home)
	v.Outbound = <-outc
	v.Outbound.StaticIPs = staticIPs{IPv4: strPtrOrNil(v4), IPv6: strPtrOrNil(v6)}
	writeJSON(w, 200, v)
}

// netProbe는 노드에서 읽은 결과다.
type netProbe struct {
	listening *node.ListeningPorts
	detection portDetection
	expose    []int
	state     *node.ContainerState // 첫 replica(사설 IP용), 없으면 nil
}

// probeNetworking은 현재 배포의 첫 노드(주 노드)에서 listen 포트(replica 합집합), 이미지 EXPOSE, 첫 replica의 inspect를 동시에 읽는다.
func (s *Server) probeNetworking(ctx context.Context, in *store.Instance, env *store.Environment, d *store.Deployment) netProbe {
	p := netProbe{detection: portDetection{Source: "unavailable"}}
	if d == nil {
		p.detection.Reason = "배포된 컨테이너가 없습니다"
		return p
	}
	nodeID, first := "", ""
	for _, g := range d.Groups() {
		if len(g.Containers) > 0 {
			nodeID, first = store.NormNode(g.NodeID), g.Containers[0]
			break
		}
	}
	if first == "" {
		p.detection.Reason = "배포된 컨테이너가 없습니다"
		return p
	}
	p.detection.Node = nodeID
	be := s.netBackend()
	ctx, cancel := context.WithTimeout(ctx, networkingTimeout)
	defer cancel()
	var wg sync.WaitGroup
	var lerr error
	wg.Add(3)
	go func() {
		defer wg.Done()
		p.listening, lerr = be.ListeningPorts(ctx, nodeID, in.ID)
	}()
	go func() {
		defer wg.Done()
		if d.Image != nil {
			p.expose = be.ImageExposedPortsOn(ctx, nodeID, *d.Image)
		}
	}()
	go func() {
		defer wg.Done()
		if st, err := be.InspectOn(ctx, nodeID, first); err == nil && st != nil && st.Running {
			p.state = st
		}
	}()
	wg.Wait()
	// 배포 때 port가 없어서 EXPOSE 포트를 골랐다면(manager.go) 스냅샷에도 남아 있다. 이미지를 못 읽었을 때를 위해 더한다.
	if d.Spec.Port != nil && *d.Spec.Port > 0 && d.ReqSpec.Port == nil {
		if _, ok := d.VarsRaw["PORT"]; !ok {
			p.expose = append(p.expose, *d.Spec.Port)
		}
	}
	switch {
	case lerr != nil:
		p.listening = nil
		p.detection.Reason = portDetectionReason(lerr)
		if !errors.Is(lerr, deploy.ErrPortDetectionUnsupported) && !errors.Is(lerr, agentprotocol.ErrOffline) {
			s.Log.Debug("port detection", "instance", in.ID, "node", nodeID, "err", lerr)
		}
	case p.listening == nil || p.listening.Containers == 0:
		p.listening = nil
		p.detection.Reason = "실행 중인 컨테이너가 없습니다"
	default:
		p.detection.Source = "proc"
		p.detection.Containers = p.listening.Containers
	}
	return p
}

// portDetectionReason은 감지 실패를 화면용 문장으로 바꾼다.
func portDetectionReason(err error) string {
	switch {
	case errors.Is(err, deploy.ErrPortDetectionUnsupported):
		return deploy.ErrPortDetectionUnsupported.Error()
	case errors.Is(err, agentprotocol.ErrOffline):
		return "노드가 연결되어 있지 않습니다"
	case errors.Is(err, context.DeadlineExceeded):
		return "노드 응답이 늦어 포트를 확인하지 못했습니다"
	default:
		return "포트를 확인하지 못했습니다"
	}
}

// envPorts는 사용자가 정한 PORT 변수 값이다(aven이 넣는 기본 PORT=8080은 빼고). 배포가 있으면 그 배포가 실제로 넣은 값이다.
func envPorts(in *store.Instance, d *store.Deployment) []int {
	raw, ok := in.Variables["PORT"]
	if d != nil {
		if _, set := d.VarsRaw["PORT"]; set {
			raw, ok = d.Env["PORT"], true
		}
	}
	if !ok {
		return nil
	}
	if p, err := strconv.Atoi(raw); err == nil && p > 0 && p < 65536 {
		return []int{p}
	}
	return nil
}

// effectivePort는 도메인에 target_port가 없을 때 프록시가 보내는 포트다(syncProxy의 routePort와 같다).
// 아직 배포가 없으면 인스턴스 설정으로 같은 규칙(port → PORT 변수 → 8080)을 쓴다.
func effectivePort(in *store.Instance, d *store.Deployment) int {
	if d != nil {
		return deploy.RoutePort(d, in.Port)
	}
	return in.Spec.EffectivePort(in.Variables)
}

// portRank는 같은 포트가 여러 출처에 있을 때 무엇으로 보일지다(작을수록 우선). 실제 소켓(listening, loopback)이 설정보다 앞선다.
var portRank = map[string]int{portSrcListening: 0, portSrcLoopback: 1, portSrcExpose: 2, portSrcEnv: 3}

// portOrder는 응답 순서다: listening, expose, env, 그리고 경고용 loopback을 마지막에.
var portOrder = map[string]int{portSrcListening: 0, portSrcExpose: 1, portSrcEnv: 2, portSrcLoopback: 3}

// mergeDetectedPorts는 listen 포트, EXPOSE, PORT 변수를 합쳐 포트마다 하나씩 남기고 정렬한다.
func mergeDetectedPorts(lp *node.ListeningPorts, expose, env []int) []detectedPort {
	best := map[int]string{}
	add := func(src string, ports []int) {
		for _, p := range ports {
			if p <= 0 || p > 65535 {
				continue
			}
			if cur, ok := best[p]; !ok || portRank[src] < portRank[cur] {
				best[p] = src
			}
		}
	}
	if lp != nil {
		add(portSrcListening, lp.Ports)
		add(portSrcLoopback, lp.Loopback)
	}
	add(portSrcExpose, expose)
	add(portSrcEnv, env)
	out := make([]detectedPort, 0, len(best))
	for p, src := range best {
		out = append(out, detectedPort{Port: p, Source: src})
	}
	sort.Slice(out, func(i, j int) bool {
		if a, b := portOrder[out[i].Source], portOrder[out[j].Source]; a != b {
			return a < b
		}
		return out[i].Port < out[j].Port
	})
	return out
}

// tcpProxyInfo는 TCP 프록시 설정과 이 노드에서 쓸 수 있는지다. 접속 호스트가 없으면(공인 IP가 없는 터널 노드) 쓸 수 없다.
func (s *Server) tcpProxyInfo(ctx context.Context, in *store.Instance) tcpProxyView {
	v := tcpProxyView{}
	v.Host, v.Available = s.Deploy.TCPProxyEndpoint(ctx, in)
	if !v.Available {
		v.Reason = tcpNoPublicIPReason
	}
	if t := in.TCPProxy; t != nil {
		v.Enabled = true
		pub, cp := t.PublicPort, t.ContainerPort
		v.PublicPort, v.ContainerPort = &pub, &cp
	}
	return v
}

// privateInfo는 같은 환경 안에서 이 서비스에 닿는 이름과 주소다. 이름과 별칭은 privateDNS(privatenet.go, 설정 값 기준)를 따른다.
// st는 첫 replica의 inspect 결과(없으면 IP가 비어 있다).
func privateInfo(svc *store.Service, env *store.Environment, in *store.Instance, d *store.Deployment, st *node.ContainerState) privateView {
	name, fqdn, aliases := privateDNS(in, svc, env)
	v := privateView{DNSName: name, FQDN: fqdn, Aliases: aliases, IPv4: []string{}, IPv6: []string{},
		MeshPorts: deploy.MeshPorts(in, d)}
	if st != nil {
		for _, ip := range st.IP(deploy.EnvNetwork(env.ID)) {
			a, err := netip.ParseAddr(ip)
			switch {
			case err != nil:
			case a.Is4():
				v.IPv4 = append(v.IPv4, a.String())
			default:
				v.IPv6 = append(v.IPv6, a.String())
			}
		}
	}
	if v.MeshPorts == nil {
		v.MeshPorts = []int{}
	}
	return v
}
