// 커널 WireGuard 없이, 노드마다 aven 프로세스(원격은 aven-agent, 로컬은 aven-server) 안에서 "mesh gateway"가 돈다:
//
//   - gateway는 노드의 사설 주소(Node.address.private, Tailscale/WireGuard)의 한 포트(KEEL_MESH_PORT, 기본 7443)에서만
//     TLS 1.3 + 상호 인증(mTLS)으로 listen한다. 인증서는 aven CA가 노드에 발급한 노드 인증서다(agent 연결에 쓰는 것과 같다).
//     상대 인증서는 aven CA 서명, node ID 바인딩, 그리고 서버가 밀어 준 "유효한 serial 목록"으로 확인한다(폐기된 노드는 거부).
//   - mTLS 뒤 첫 줄은 작은 JSON 헤더({env, service, port})다. gateway는 그 서비스가 이 노드에서 export되는지,
//     요청한 노드가 그 환경의 노드인지, 포트가 private_ports에 있는지 확인한 뒤 컨테이너 IP로 직접 연결한다
//     (aven 프로세스는 호스트에서 돌므로 docker bridge의 컨테이너 IP에 닿는다).
//   - 다른 노드의 서비스를 쓰는 환경에는 서비스마다 forwarder 컨테이너(role=meshproxy, 포트 publish 없음)를 환경 네트워크에
//     `<svc>`, `<svc>.<env>.keel.internal` alias로 붙인다. forwarder는 그 환경 네트워크의 bridge gateway IP(호스트)에서
//     gateway가 연 로컬 포트로 넘기고, gateway는 연결의 출발 주소가 그 환경 네트워크의 서브넷인지 확인한 뒤 mTLS로 상대 노드에 넘긴다.
//     로컬 포트 하나는 (환경, 서비스, 포트) 하나에만 묶이므로 다른 환경의 컨테이너가 이 포트로 와도 거부된다.
package mesh

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	DefaultPort = 7443
	// ALPN은 mesh 연결의 TLS ALPN 값이다(다른 TLS 서비스와 섞이지 않게).
	ALPN = "keel-mesh/1"
	// MaxHeaderBytes는 mTLS 뒤 요청 헤더(JSON 한 줄)의 상한이다.
	MaxHeaderBytes = 1024

	maxPeers   = 256
	maxExports = 2048
	maxImports = 2048
	maxPorts   = 16
	maxSerials = 16
	maxAliases = 8
)

var (
	nodeIDRe  = regexp.MustCompile(`^(node_local|nod_[a-z0-9]{4,40})$`)
	envIDRe   = regexp.MustCompile(`^env_[a-zA-Z0-9]{4,40}$`)
	svcIDRe   = regexp.MustCompile(`^svc_[a-zA-Z0-9]{4,40}$`)
	labelRe   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)
	envNameRe = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9_-]{0,62}[a-zA-Z0-9])?$`)
	serialRe  = regexp.MustCompile(`^[0-9a-f]{1,40}$`)
	// aliasLabelRe는 import 별칭의 이름 부분이다(DNS label, 소문자, 63자 이하).
	aliasLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// Config는 서버가 노드 하나에 내려 주는 mesh 원하는 상태다(agent op mesh.ensure의 payload).
type Config struct {
	// Peers는 mesh에 참여하는 다른 노드다(상태 확인 대상이자 mTLS로 받아들이는 노드).
	Peers []Peer `json:"peers"`
	// Exports는 이 노드에서 도는 서비스 중 다른 노드에 내보내는 것이다.
	Exports []Export `json:"exports"`
	// Imports는 이 노드의 환경 네트워크에서 쓸 수 있게 할 다른 노드의 서비스다.
	Imports []Import `json:"imports"`
}

type Peer struct {
	NodeID  string   `json:"node_id"`
	Address string   `json:"address"` // ip:port (사설 주소의 mesh gateway)
	Serials []string `json:"serials"` // 이 노드의 유효한(폐기되지 않은) 인증서 serial(hex)
}

type Export struct {
	EnvID     string   `json:"env_id"`
	ServiceID string   `json:"service_id"`
	Service   string   `json:"service"` // 서비스 이름(헤더의 service)
	Ports     []int    `json:"ports"`
	Peers     []string `json:"peers"` // 이 서비스에 접속할 수 있는 노드(같은 환경의 다른 노드)
}

type Import struct {
	EnvID   string `json:"env_id"`
	EnvName string `json:"env_name"`
	Service string `json:"service"`
	Ports   []int  `json:"ports"`
	Node    string `json:"node"` // 서비스가 도는 노드(주 노드). Nodes가 있으면 그 중 하나다.
	// Nodes는 서비스를 내보내는 모든 노드다(failover 후보, 비우면 [Node]). gateway는 연결마다 RTT가 낮은 노드부터 시도하고
	// 닿지 않는 노드는 건너뛴다. 이 필드를 모르는 옛 agent에는 서버가 빼고 보낸다.
	Nodes []string `json:"nodes,omitempty"`
	// Aliases는 forwarder에 서비스 이름 alias(<svc>, <svc>.<env>.keel.internal) 말고 더 붙일 이름이다
	// (사설 DNS 이름: <dns>, <dns>.<env>.keel.internal). 이 필드를 모르는 옛 agent에는 서버가 빼고 보낸다.
	Aliases []string `json:"aliases,omitempty"`
}

// AllAliases는 forwarder 컨테이너에 붙일 이름이다: 서비스 이름 alias 두 개와 Aliases(중복 제외).
func (i Import) AllAliases() []string {
	out := []string{i.Service, PrivateDomain(i.Service, i.EnvName)}
	for _, a := range i.Aliases {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

// validAlias는 import 별칭이 <label> 또는 <label>.<env>.keel.internal 꼴인지다.
func validAlias(a, envName string) bool {
	if aliasLabelRe.MatchString(a) {
		return true
	}
	label, ok := strings.CutSuffix(a, "."+envName+".keel.internal")
	return ok && aliasLabelRe.MatchString(label)
}

// Candidates는 import의 failover 후보 노드다(Nodes, 없으면 Node 하나).
func (i Import) Candidates() []string {
	if len(i.Nodes) > 0 {
		return append([]string(nil), i.Nodes...)
	}
	return []string{i.Node}
}

func (e Export) key() string { return e.EnvID + "/" + e.Service }
func (i Import) key() string { return i.EnvID + "/" + i.Service }

// PrivateDomain은 deploy.PrivateDomain과 같다(mesh가 deploy를 import하지 않으려고 따로 둔다).
func PrivateDomain(service, env string) string { return service + "." + env + ".keel.internal" }

// EnvNetwork는 deploy.EnvNetwork와 같다.
func EnvNetwork(envID string) string { return "keel-" + envID }

// ContainerName은 (환경, 서비스) forwarder 컨테이너 이름이다.
func ContainerName(envID, service string) string {
	return "keel-mesh-" + strings.ToLower(strings.TrimPrefix(envID, "env_")) + "-" + service
}

var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// PrivateAddr는 mesh에 쓸 수 있는 사설 주소인지다: RFC1918, ULA(fc00::/7), CGNAT(100.64/10, Tailscale).
// 공인 주소나 루프백·링크 로컬은 받지 않는다(서버가 agent를 임의 주소로 보내지 못하게).
func PrivateAddr(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsValid() && !a.IsLoopback() && !a.IsUnspecified() && !a.IsLinkLocalUnicast() && (a.IsPrivate() || cgnat.Contains(a))
}

// ParsePrivateIP는 노드의 사설 주소 문자열을 mesh listen 주소로 읽는다(IP만, 사설 대역만).
func ParsePrivateIP(s string) (netip.Addr, bool) {
	a, err := netip.ParseAddr(strings.Trim(strings.TrimSpace(s), "[]"))
	if err != nil || !PrivateAddr(a) {
		return netip.Addr{}, false
	}
	return a.Unmap(), true
}

func checkPorts(ports []int) error {
	if len(ports) == 0 || len(ports) > maxPorts {
		return fmt.Errorf("ports must have 1..%d entries", maxPorts)
	}
	seen := map[int]bool{}
	for _, p := range ports {
		if p < 1 || p > 65535 || seen[p] {
			return fmt.Errorf("port %d is not allowed", p)
		}
		seen[p] = true
	}
	return nil
}

// Validate는 서버가 보낸 mesh 설정을 엄격하게 검사한다. self는 이 노드 ID다.
func Validate(c Config, self string) error {
	if !nodeIDRe.MatchString(self) {
		return errors.New("invalid self node id")
	}
	if len(c.Peers) > maxPeers || len(c.Exports) > maxExports || len(c.Imports) > maxImports {
		return errors.New("mesh config is too large")
	}
	peers := map[string]bool{}
	for _, p := range c.Peers {
		if !nodeIDRe.MatchString(p.NodeID) || p.NodeID == self || peers[p.NodeID] {
			return fmt.Errorf("peer %q is not allowed", p.NodeID)
		}
		peers[p.NodeID] = true
		ap, err := netip.ParseAddrPort(p.Address)
		if err != nil || !PrivateAddr(ap.Addr()) || ap.Port() < 1024 {
			return fmt.Errorf("peer %s address %q is not allowed (private address, port >= 1024)", p.NodeID, p.Address)
		}
		if len(p.Serials) == 0 || len(p.Serials) > maxSerials {
			return fmt.Errorf("peer %s: 1..%d certificate serials required", p.NodeID, maxSerials)
		}
		for _, s := range p.Serials {
			if !serialRe.MatchString(s) {
				return fmt.Errorf("peer %s: invalid serial", p.NodeID)
			}
		}
	}
	seen := map[string]bool{}
	for _, e := range c.Exports {
		if !envIDRe.MatchString(e.EnvID) || !svcIDRe.MatchString(e.ServiceID) || !labelRe.MatchString(e.Service) {
			return fmt.Errorf("export %s/%s is not allowed", e.EnvID, e.Service)
		}
		if seen[e.key()] {
			return fmt.Errorf("duplicate export %s", e.key())
		}
		seen[e.key()] = true
		if err := checkPorts(e.Ports); err != nil {
			return fmt.Errorf("export %s: %w", e.key(), err)
		}
		if len(e.Peers) == 0 {
			return fmt.Errorf("export %s: no peers", e.key())
		}
		for _, p := range e.Peers {
			if !peers[p] {
				return fmt.Errorf("export %s: unknown peer %q", e.key(), p)
			}
		}
	}
	seen = map[string]bool{}
	for _, i := range c.Imports {
		if !envIDRe.MatchString(i.EnvID) || !envNameRe.MatchString(i.EnvName) || !labelRe.MatchString(i.Service) {
			return fmt.Errorf("import %s/%s is not allowed", i.EnvID, i.Service)
		}
		if seen[i.key()] {
			return fmt.Errorf("duplicate import %s", i.key())
		}
		seen[i.key()] = true
		if err := checkPorts(i.Ports); err != nil {
			return fmt.Errorf("import %s: %w", i.key(), err)
		}
		if !peers[i.Node] {
			return fmt.Errorf("import %s: unknown peer %q", i.key(), i.Node)
		}
		if len(i.Nodes) > 0 {
			if len(i.Nodes) > maxPeers {
				return fmt.Errorf("import %s: too many nodes", i.key())
			}
			dup, hasPrimary := map[string]bool{}, false
			for _, n := range i.Nodes {
				if !peers[n] || dup[n] {
					return fmt.Errorf("import %s: node %q is not allowed", i.key(), n)
				}
				dup[n] = true
				hasPrimary = hasPrimary || n == i.Node
			}
			if !hasPrimary {
				return fmt.Errorf("import %s: node %q must be one of nodes", i.key(), i.Node)
			}
		}
		if len(i.Aliases) > maxAliases {
			return fmt.Errorf("import %s: too many aliases", i.key())
		}
		for _, a := range i.Aliases {
			if !validAlias(a, i.EnvName) {
				return fmt.Errorf("import %s: alias %q is not allowed (<name> or <name>.%s.keel.internal)", i.key(), a, i.EnvName)
			}
		}
		if n := ContainerName(i.EnvID, i.Service); len(n) > 120 {
			return fmt.Errorf("import %s: name too long", i.key())
		}
	}
	return nil
}

// ValidNames는 서비스가 mesh로 내보낼 수 있는 이름/ID 형식인지다(아니면 서버가 그 서비스를 mesh에서 뺀다).
func ValidNames(envID, envName, serviceID, service string) bool {
	return envIDRe.MatchString(envID) && envNameRe.MatchString(envName) && svcIDRe.MatchString(serviceID) && labelRe.MatchString(service) &&
		len(ContainerName(envID, service)) <= 120
}

// Normalize는 비교와 출력이 안정적이도록 정렬한다.
func (c *Config) Normalize() {
	sort.Slice(c.Peers, func(a, b int) bool { return c.Peers[a].NodeID < c.Peers[b].NodeID })
	for i := range c.Peers {
		sort.Strings(c.Peers[i].Serials)
	}
	sort.Slice(c.Exports, func(a, b int) bool { return c.Exports[a].key() < c.Exports[b].key() })
	for i := range c.Exports {
		sort.Ints(c.Exports[i].Ports)
		sort.Strings(c.Exports[i].Peers)
	}
	sort.Slice(c.Imports, func(a, b int) bool { return c.Imports[a].key() < c.Imports[b].key() })
	for i := range c.Imports {
		sort.Ints(c.Imports[i].Ports)
		sort.Strings(c.Imports[i].Aliases)
	}
	if c.Peers == nil {
		c.Peers = []Peer{}
	}
	if c.Exports == nil {
		c.Exports = []Export{}
	}
	if c.Imports == nil {
		c.Imports = []Import{}
	}
}

// Header는 mTLS 연결의 첫 줄이다.
type Header struct {
	V       int    `json:"v"`
	Op      string `json:"op"` // dial | ping
	Env     string `json:"env,omitempty"`
	Service string `json:"service,omitempty"`
	Port    int    `json:"port,omitempty"`
}

// Reply는 헤더에 대한 gateway의 답(JSON 한 줄)이다. ok면 그 뒤로 바이트가 그대로 오간다.
type Reply struct {
	OK    bool   `json:"ok"`
	Node  string `json:"node,omitempty"`
	Error string `json:"error,omitempty"`
}

// ValidateHeader는 헤더 형식을 검사한다(권한 검사는 gateway가 한다).
func ValidateHeader(h Header) error {
	if h.V != 1 {
		return errors.New("unsupported mesh protocol version")
	}
	switch h.Op {
	case "ping":
		if h.Env != "" || h.Service != "" || h.Port != 0 {
			return errors.New("ping carries no target")
		}
		return nil
	case "dial":
		if !envIDRe.MatchString(h.Env) || !labelRe.MatchString(h.Service) || h.Port < 1 || h.Port > 65535 {
			return errors.New("invalid dial target")
		}
		return nil
	}
	return errors.New("unknown op")
}

// signature는 forwarder 컨테이너 라벨(keel.mesh)이다: "<svcport>=<localport>+...@<gateway ip>".
func signature(ports []int, local map[int]int, gw netip.Addr) string {
	parts := make([]string, 0, len(ports))
	for _, p := range ports {
		parts = append(parts, strconv.Itoa(p)+"="+strconv.Itoa(local[p]))
	}
	return strings.Join(parts, "+") + "@" + gw.String()
}

// signatureDual은 IPv6 로컬 포트도 있을 때의 서명이다: "<IPv4 서명>/<IPv6 서명>"(IPv6 주소에는 '/'가 없다).
func signatureDual(ports []int, local map[int]int, gw netip.Addr, local6 map[int]int, gw6 netip.Addr) string {
	s := signature(ports, local, gw)
	var p6 []int
	for _, p := range ports {
		if _, ok := local6[p]; ok {
			p6 = append(p6, p)
		}
	}
	if gw6.IsValid() && len(p6) > 0 {
		s += "/" + signature(p6, local6, gw6)
	}
	return s
}

// parseSignature6는 서명의 IPv6 부분이다(없으면 ok=false).
func parseSignature6(s string) (map[int]int, netip.Addr, bool) {
	_, v6, ok := strings.Cut(s, "/")
	if !ok {
		return nil, netip.Addr{}, false
	}
	return parseSignature(v6)
}

// parseSignature는 signature의 반대다(이전 실행에서 쓰던 로컬 포트를 다시 쓰려고 읽는다). IPv6 부분은 무시한다.
func parseSignature(s string) (map[int]int, netip.Addr, bool) {
	s, _, _ = strings.Cut(s, "/")
	ports, gw, ok := strings.Cut(s, "@")
	if !ok {
		return nil, netip.Addr{}, false
	}
	a, err := netip.ParseAddr(gw)
	if err != nil {
		return nil, netip.Addr{}, false
	}
	out := map[int]int{}
	for _, kv := range strings.Split(ports, "+") {
		k, v, ok := strings.Cut(kv, "=")
		p, e1 := strconv.Atoi(k)
		l, e2 := strconv.Atoi(v)
		if !ok || e1 != nil || e2 != nil || p < 1 || p > 65535 || l < 1 || l > 65535 {
			return nil, netip.Addr{}, false
		}
		out[p] = l
	}
	return out, a, true
}
