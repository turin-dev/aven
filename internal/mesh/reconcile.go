package mesh

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/node"
)

// Docker는 forwarder 컨테이너와 컨테이너 주소 조회에 쓰는 docker 기능이다(node.Docker, agent Docker).
type Docker interface {
	NetworkInfo(ctx context.Context, name string) (*node.NetworkInfo, error)
	ListManaged(ctx context.Context) (map[string]map[string]string, error)
	InspectMany(ctx context.Context, names []string) (map[string]*node.ContainerState, error)
	Run(ctx context.Context, s node.RunSpec) error
	Remove(ctx context.Context, name string, grace int) error
}

const (
	labelMesh        = "keel.mesh"         // forwarder 서명(포트 → 로컬 포트 @ gateway IP)
	labelMeshService = "keel.mesh.service" // forwarder가 대신하는 서비스 이름
	labelMeshAliases = "keel.mesh.aliases" // forwarder에 더 붙인 이름(Import.Aliases, 쉼표로 이음). 없으면 라벨도 없다.
)

// Ensure는 설정을 gateway에 반영하고 forwarder 컨테이너를 맞춘다. 결과는 현재 mesh 상태다.
// forwarder는 이 함수만 만들고 지운다(서버의 일반 컨테이너 작업 대상이 아니다).
func Ensure(ctx context.Context, g *Gateway, dk Docker, c Config) (Status, error) {
	c.Normalize()
	managed, err := dk.ListManaged(ctx)
	if err != nil {
		return g.Status(), err
	}
	existing := map[string]map[string]string{}
	for name, labels := range managed {
		if labels[node.LabelRole] == node.RoleMeshProxy {
			existing[name] = labels
		}
	}
	nets := map[string]EnvNet{}
	var errs []error
	for _, im := range c.Imports {
		if _, ok := nets[im.EnvID]; ok {
			continue
		}
		info, err := dk.NetworkInfo(ctx, EnvNetwork(im.EnvID))
		if err != nil {
			errs = append(errs, fmt.Errorf("network %s: %w", EnvNetwork(im.EnvID), err))
			continue
		}
		nets[im.EnvID] = envNetOf(info)
	}
	prefer := map[string]int{}
	for _, im := range c.Imports {
		labels := existing[ContainerName(im.EnvID, im.Service)]
		if labels == nil {
			continue
		}
		if local, _, ok := parseSignature(labels[labelMesh]); ok {
			for p, l := range local {
				prefer[fmt.Sprintf("%s/%s/%d", im.EnvID, im.Service, p)] = l
			}
		}
		if local, _, ok := parseSignature6(labels[labelMesh]); ok {
			for p, l := range local {
				prefer[fmt.Sprintf("%s/%s/%d/6", im.EnvID, im.Service, p)] = l
			}
		}
	}
	bindings, aerr := g.Apply(c, nets, prefer)
	if aerr != nil {
		errs = append(errs, aerr)
	}
	byImport := map[string]map[int]netip.AddrPort{}
	byImport6 := map[string]map[int]netip.AddrPort{}
	for _, b := range bindings {
		k := b.EnvID + "/" + b.Service
		if byImport[k] == nil {
			byImport[k], byImport6[k] = map[int]netip.AddrPort{}, map[int]netip.AddrPort{}
		}
		byImport[k][b.Port] = b.Local
		if b.Local6.IsValid() {
			byImport6[k][b.Port] = b.Local6
		}
	}
	want := map[string]bool{}
	for _, im := range c.Imports {
		bs := byImport[im.key()]
		if len(bs) != len(im.Ports) {
			continue // 네트워크가 없거나 로컬 포트를 열지 못했다(위에서 오류로 알린다)
		}
		spec := ForwarderSpecDual(im, bs, byImport6[im.key()])
		want[spec.Name] = true
		if labels := existing[spec.Name]; labels != nil && labels[labelMesh] == spec.Labels[labelMesh] &&
			labels[labelMeshAliases] == spec.Labels[labelMeshAliases] {
			if st, err := dk.InspectMany(ctx, []string{spec.Name}); err == nil && st[spec.Name] != nil && st[spec.Name].Running {
				continue
			}
		}
		if err := dk.Remove(ctx, spec.Name, 1); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := dk.Run(ctx, spec); err != nil {
			errs = append(errs, fmt.Errorf("forwarder %s: %w", spec.Name, err))
		}
	}
	for name := range existing {
		if !want[name] {
			if err := dk.Remove(ctx, name, 1); err != nil {
				errs = append(errs, err)
			}
		}
	}
	st := g.Status()
	if err := errors.Join(errs...); err != nil {
		st.Error = strings.TrimSpace(strings.Join([]string{st.Error, err.Error()}, " "))
		return st, err
	}
	return st, nil
}

// envNetOf는 docker 네트워크 정보에서 gateway 주소를 고른다: 첫 gateway(IPv4 우선), 그리고 IPv4 네트워크에 IPv6도 있으면 그 IPv6 gateway.
func envNetOf(info *node.NetworkInfo) EnvNet {
	n := EnvNet{Subnets: info.Subnets, Gateway: info.Gateways[0]}
	if n.Gateway.Is4() {
		for _, a := range info.Gateways[1:] {
			if a.Is6() && !a.Is4In6() {
				n.Gateway6 = a
				break
			}
		}
	}
	return n
}

// ForwarderSpec은 import 하나의 forwarder 컨테이너 설정이다(IPv4/단일 주소). ForwarderSpecDual을 보라.
func ForwarderSpec(im Import, local map[int]netip.AddrPort) node.RunSpec {
	return ForwarderSpecDual(im, local, nil)
}

// ForwarderSpecDual은 import 하나의 forwarder 컨테이너 설정이다. 이미지와 명령은 고정이고, 포트 publish·볼륨·특권이 없다.
// 환경 네트워크에 서비스 alias로 붙어 서비스 포트에서 listen하고, 그 네트워크의 gateway IP(호스트)의 로컬 포트로 넘긴다.
// local6이 있으면(네트워크에 IPv6가 있으면) 같은 포트의 IPv6 소켓도 열어 IPv6 gateway의 로컬 포트로 넘긴다.
func ForwarderSpecDual(im Import, local, local6 map[int]netip.AddrPort) node.RunSpec {
	ports := append([]int(nil), im.Ports...)
	lp, lp6 := map[int]int{}, map[int]int{}
	var gw, gw6 netip.Addr
	var cmds []string
	for _, p := range ports {
		ap := local[p]
		gw = ap.Addr()
		lp[p] = int(ap.Port())
		if ap.Addr().Is6() {
			cmds = append(cmds, fmt.Sprintf("socat TCP6-LISTEN:%d,fork,reuseaddr,ipv6only=1 TCP6:[%s]:%d &", p, ap.Addr(), ap.Port()))
		} else {
			cmds = append(cmds, fmt.Sprintf("socat TCP-LISTEN:%d,fork,reuseaddr TCP4:%s:%d &", p, ap.Addr(), ap.Port()))
		}
		if a6, ok := local6[p]; ok && a6.IsValid() && a6.Addr().Is6() && !ap.Addr().Is6() {
			gw6 = a6.Addr()
			lp6[p] = int(a6.Port())
			cmds = append(cmds, fmt.Sprintf("socat TCP6-LISTEN:%d,fork,reuseaddr,ipv6only=1 TCP6:[%s]:%d &", p, a6.Addr(), a6.Port()))
		}
	}
	sh := "/bin/sh"
	aliases := im.AllAliases()
	labels := map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleMeshProxy, node.LabelEnv: im.EnvID,
		labelMesh: signatureDual(ports, lp, gw, lp6, gw6), labelMeshService: im.Service}
	if extra := aliases[2:]; len(extra) > 0 {
		// 별칭이 바뀌면 forwarder를 다시 만든다(별칭이 없는 forwarder는 라벨도 없어 예전 것과 그대로 같다).
		labels[labelMeshAliases] = strings.Join(extra, ",")
	}
	return node.RunSpec{
		Name: ContainerName(im.EnvID, im.Service), Image: node.TCPForwardImage, Network: EnvNetwork(im.EnvID),
		Aliases: aliases, Restart: "unless-stopped", Labels: labels,
		Entrypoint: &sh, Cmd: []string{"-c", strings.Join(cmds, " ") + " wait"},
	}
}

// DockerResolver는 docker 라벨과 inspect로 컨테이너 IP를 찾는다(5초 캐시).
type DockerResolver struct {
	Docker interface {
		ListManaged(ctx context.Context) (map[string]map[string]string, error)
		InspectMany(ctx context.Context, names []string) (map[string]*node.ContainerState, error)
	}
	TTL time.Duration

	mu    sync.Mutex
	cache map[string]resolved
}

type resolved struct {
	at    time.Time
	addrs []netip.Addr
}

func (r *DockerResolver) Upstreams(ctx context.Context, envID, serviceID string) ([]netip.Addr, error) {
	ttl := r.TTL
	if ttl <= 0 {
		ttl = 5 * time.Second
	}
	k := envID + "/" + serviceID
	r.mu.Lock()
	if c, ok := r.cache[k]; ok && time.Since(c.at) < ttl && len(c.addrs) > 0 {
		r.mu.Unlock()
		return append([]netip.Addr(nil), c.addrs...), nil
	}
	r.mu.Unlock()
	managed, err := r.Docker.ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	var names []string
	for name, l := range managed {
		if l[node.LabelEnv] == envID && l[node.LabelService] == serviceID && l[node.LabelRole] == node.RoleApp {
			names = append(names, name)
		}
	}
	st, err := r.Docker.InspectMany(ctx, names)
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, c := range st {
		if !c.Running {
			continue
		}
		// 라벨을 다시 확인한다(목록과 inspect 사이에 바뀌었을 수 있다).
		if c.Labels[node.LabelEnv] != envID || c.Labels[node.LabelService] != serviceID || c.Labels[node.LabelRole] != node.RoleApp {
			continue
		}
		var v4, v6 []netip.Addr
		for _, ip := range c.IP(EnvNetwork(envID)) {
			if a, err := netip.ParseAddr(ip); err == nil {
				if a.Unmap().Is4() {
					v4 = append(v4, a.Unmap())
				} else {
					v6 = append(v6, a)
				}
			}
		}
		// 컨테이너 하나에 주소 하나(IPv4 우선: 앱은 0.0.0.0에서만 listen하는 경우가 많다).
		if len(v4) > 0 {
			out = append(out, v4[0])
		} else if len(v6) > 0 {
			out = append(out, v6[0])
		}
	}
	r.mu.Lock()
	if r.cache == nil {
		r.cache = map[string]resolved{}
	}
	r.cache[k] = resolved{at: time.Now(), addrs: out}
	r.mu.Unlock()
	return append([]netip.Addr(nil), out...), nil
}
