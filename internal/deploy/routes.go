package deploy

import (
	"context"
	"fmt"
	"net"

	"strings"

	"github.com/turin-dev/aven/internal/logbook"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// hostRoute는 도메인의 호스트 단위 설정(TLS 모드, 리다이렉트, basic auth)을 담은 Route다. upstream은 호출자가 채운다.
func (m *Manager) hostRoute(ctx context.Context, in *store.Instance, dm store.Domain) proxy.Route {
	rt := proxy.Route{Hosts: []string{dm.Host}, TLS: proxy.TLSMode(dm.TLS), TrustProto: dm.TrustForwardedProto}
	rt.Trace, rt.TraceAttributes = m.traceRoute(ctx, in, in.CurrentDeploymentID)
	if dm.Redirect != nil && dm.Redirect.To != "" {
		rt.Redirect = &proxy.Redirect{To: dm.Redirect.To, Permanent: dm.Redirect.Permanent}
	}
	if dm.BasicAuth != nil && dm.BasicAuth.Username != "" {
		if hash, err := m.Store.GetSetting(ctx, store.DomainAuthKey(in.ID, dm.Host)); err == nil && hash != "" {
			rt.Auth = &proxy.BasicAuth{User: dm.BasicAuth.Username, Hash: hash}
		}
	}
	m.attachEdge(ctx, in, &rt)
	return rt
}

// pathRoute는 도메인의 경로 규칙 하나를 Route로 바꾼다. 대상 서비스가 같은 환경에 있고, 이 라우트가 들어갈 노드(nk)에서
// 돌고 있어야 한다(아니면 ok=false, 그 경로는 이 호스트의 기본 서비스로 간다).
func (m *Manager) pathRoute(ctx context.Context, in *store.Instance, dm store.Domain, pr store.PathRule, nk string,
	byEnvSvc map[string]*store.Instance, svcName map[string]string) (proxy.Route, bool) {
	target := in
	if pr.Service != "" && pr.Service != svcName[in.ID] {
		target = byEnvSvc[in.EnvironmentID+"/"+pr.Service]
	}
	if target == nil || !liveStatus(target.Status) || target.CurrentDeploymentID == nil {
		return proxy.Route{}, false
	}
	d, err := m.Store.GetDeployment(ctx, *target.CurrentDeploymentID)
	if err != nil {
		return proxy.Route{}, false
	}
	var local []string
	for _, g := range d.Groups() {
		if nodeKey(g.NodeID) == nk {
			local = g.Containers
		}
	}
	if len(local) == 0 {
		return proxy.Route{}, false
	}
	port := routePort(d, target.Port, pr.TargetPort, targetIfPositive)
	rt := proxy.Route{Hosts: []string{dm.Host}, Path: pr.Path, StripPrefix: pr.StripPrefix, Upstreams: upstreams(local, port)}
	rt.Trace, rt.TraceAttributes = m.traceRoute(ctx, target, &d.ID)
	return rt, true
}

// targetOverride는 도메인·경로 규칙의 target_port가 기본 포트를 언제 덮는지다. 호출하는 곳마다 예전 동작이 달라서 그대로 둔다.
type targetOverride int

const (
	// targetIfPositive: target_port가 0보다 클 때만 덮는다(syncProxy, pathRoute).
	targetIfPositive targetOverride = iota
	// targetIfSet: target_port가 nil이 아니면 값과 상관없이 덮는다(잠든 서비스를 깨울 때의 targetPort).
	targetIfSet
)

// routePort는 라우트가 보낼 컨테이너 포트다. 배포 스냅샷의 EffectivePort(port → PORT 변수 → 8080)에서 시작해,
// 인스턴스의 port가 nil이 아니면 그 값으로(0이어도), 그다음 target_port가 mode에 맞으면 그 값으로 바꾼다.
func routePort(d *store.Deployment, instPort, target *int, mode targetOverride) int {
	port := d.Spec.EffectivePort(d.Env)
	if instPort != nil {
		port = *instPort
	}
	if target != nil && (mode == targetIfSet || *target > 0) {
		port = *target
	}
	return port
}

// DomainPort는 도메인 라우트가 보낼 컨테이너 포트다(syncProxy와 같은 규칙: routePort, target_port는 0보다 클 때만).
// 현재 배포가 없으면 인스턴스 스펙과 변수로 같은 순서를 따른다.
func (m *Manager) DomainPort(ctx context.Context, in *store.Instance, dm store.Domain) int {
	if in.CurrentDeploymentID != nil {
		if d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil {
			return routePort(d, in.Port, dm.TargetPort, targetIfPositive)
		}
	}
	return routePort(&store.Deployment{Spec: in.Spec, Env: in.Variables}, in.Port, dm.TargetPort, targetIfPositive)
}

// upstreams는 컨테이너 이름과 포트로 proxy upstream 주소를 만든다.
func upstreams(names []string, port int) []string {
	out := make([]string, 0, len(names))
	for _, c := range names {
		out = append(out, net.JoinHostPort(c, fmt.Sprint(port)))
	}
	return out
}

// servingNodes는 인스턴스의 트래픽을 받는 노드들이다: 현재 배포의 replica가 있는 노드(주 노드 먼저), 배포가 없으면 지정 노드.
func (m *Manager) servingNodes(ctx context.Context, in *store.Instance) []string {
	if in.CurrentDeploymentID != nil {
		if d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil {
			var out []string
			for _, g := range d.Groups() {
				out = append(out, nodeKey(g.NodeID))
			}
			return out
		}
	}
	if len(in.Placement) > 0 {
		var out []string
		for _, p := range in.Placement {
			out = append(out, nodeKey(p.NodeID))
		}
		return out
	}
	return []string{nodeKey(in.NodeID)}
}

// ServingNodes는 인스턴스의 트래픽을 받는 노드 ID들이다(도메인 DNS 레코드용). 연결이 끊긴 원격 노드는 뺀다
// (모든 노드가 끊겼으면 레코드가 비지 않도록 그대로 둔다).
func (m *Manager) ServingNodes(ctx context.Context, in *store.Instance) []string {
	all := m.servingNodes(ctx, in)
	var up []string
	for _, n := range all {
		if !m.knownOffline(n) {
			up = append(up, n)
		}
	}
	if len(up) == 0 {
		return all
	}
	return up
}

// cloneRepo는 repo를 clone한다. SSH URL이고 서비스에 deploy key가 있으면 그 키로(호스트 키 고정, TOFU 기록), 아니면 토큰으로 clone한다.
func (m *Manager) cloneRepo(ctx context.Context, serviceID, repo, branch, sha, dir string, b *logbook.Book) (*node.Commit, error) {
	if key, kh := m.sshKeyFor(ctx, serviceID, repo, b); key != "" {
		c, newKH, err := node.CloneSSH(ctx, repo, branch, sha, dir, key, kh, b.Line)
		m.saveKnownHosts(ctx, newKH)
		return c, err
	}
	return node.Clone(ctx, repo, branch, sha, dir, m.githubTokenFor(ctx, serviceID, repo), b.Line)
}

// sshKeyFor는 SSH repo URL에 쓸 deploy key 개인키와 기록된 known_hosts를 돌려준다(없으면 "").
func (m *Manager) sshKeyFor(ctx context.Context, serviceID, repo string, b *logbook.Book) (key, knownHosts string) {
	if !node.IsSSHRepo(repo) {
		return "", ""
	}
	k, err := m.Store.GetDeployKey(ctx, serviceID)
	if err != nil {
		return "", ""
	}
	pem, err := m.Store.DeployKeyPrivate(k)
	if err != nil {
		b.Printf("WARN: deploy key를 복호화할 수 없습니다: %v", err)
		return "", ""
	}
	b.Printf("SSH deploy key %s 사용", k.Fingerprint)
	return pem, m.Store.KnownHosts(ctx)
}

// saveKnownHosts는 clone 뒤의 known_hosts에서 새로 기록된 키(내장 github 키 제외)를 저장한다.
func (m *Manager) saveKnownHosts(ctx context.Context, all string) {
	if all == "" {
		return
	}
	builtin := map[string]bool{}
	for _, l := range strings.Split(node.BuiltinKnownHosts, "\n") {
		builtin[strings.TrimSpace(l)] = true
	}
	var extra []string
	for _, l := range strings.Split(all, "\n") {
		if l = strings.TrimSpace(l); l != "" && !builtin[l] {
			extra = append(extra, l)
		}
	}
	merged := node.MergeKnownHosts("", strings.Join(extra, "\n"))
	if strings.TrimSpace(merged) != strings.TrimSpace(m.Store.KnownHosts(ctx)) && len(extra) > 0 {
		m.Store.SetKnownHosts(ctx, merged)
	}
}
