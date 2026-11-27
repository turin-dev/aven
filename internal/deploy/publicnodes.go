package deploy

import (
	"context"
	"net/netip"
	"strings"

	"github.com/turin-dev/aven/internal/compat"
	"github.com/turin-dev/aven/internal/store"
)

// publicNode는 공개 트래픽을 받을 수 있는 노드 하나다(ACME relay 고리와 Cloudflare 연동이 쓴다).
type publicNode struct {
	ID, Name   string
	IPv4, IPv6 string // 검사한 공인 주소("" = 없음)
	Up         bool   // 로컬 노드는 항상, 원격 노드는 agent 연결 여부
	Tunnel     bool   // Cloudflare 터널로만 받는 노드(공인 주소로 보내면 안 된다)
	Labels     map[string]string
}

// Public은 공인 주소로 트래픽을 보낼 수 있는지다.
func (n publicNode) Public() bool { return !n.Tunnel && (n.IPv4 != "" || n.IPv6 != "") }

// publicNodes는 로컬 노드와 원격 노드의 공개 주소 정보다(로컬 노드가 먼저).
func (m *Manager) publicNodes(ctx context.Context) []publicNode {
	var ing store.NodeIngress
	local := publicNode{ID: store.LocalNodeID, Name: "local", Up: true, Labels: map[string]string{},
		IPv4: cleanAddr(m.Cfg.PublicIPv4, true), IPv6: cleanAddr(m.Cfg.PublicIPv6, false)}
	if m.Store != nil {
		if n, err := m.Store.LocalNode(ctx); err == nil {
			ing, local.Name, local.Labels = n.NodeIngress, n.Name, n.Labels
		}
	}
	if v := strings.TrimSpace(compat.Env("INGRESS_MODE")); v != "" {
		ing.IngressMode = v
	}
	local.Tunnel = ing.Tunnel()
	out := []publicNode{local}
	for _, n := range m.remoteNodes(ctx) {
		pn := publicNode{ID: n.ID, Name: n.Name, Up: m.nodeOnline(n.ID), Tunnel: n.NodeIngress.Tunnel(), Labels: n.Labels}
		if n.Address.IPv4 != nil {
			pn.IPv4 = cleanAddr(*n.Address.IPv4, true)
		}
		if n.Address.IPv6 != nil {
			pn.IPv6 = cleanAddr(*n.Address.IPv6, false)
		}
		out = append(out, pn)
	}
	return out
}

// cleanAddr는 공인 주소 문자열을 검사한다(형식이 틀리거나 주소 체계가 다르면 "").
func cleanAddr(s string, v4 bool) string {
	a, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil || a.Zone() != "" || (v4 && !a.Is4()) || (!v4 && (!a.Is6() || a.Is4In6())) {
		return ""
	}
	return a.String()
}
