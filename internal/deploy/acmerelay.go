package deploy

import (
	"context"
	"net"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/proxy"
)

// decorateACMERelay는 여러 노드가 서비스하는 호스트의 라우트에 ACME relay(다음 노드)를 채운다(proxy/acmerelay.go).
// 고리는 그 호스트를 서비스하고, 연결돼 있고, 공인 주소가 있는 노드를 ID 순서로 놓은 것이다. 노드가 하나뿐이면 relay가 없다.
func (m *Manager) decorateACMERelay(ctx context.Context, routes map[string][]proxy.Route) {
	byID := map[string]publicNode{}
	for _, n := range m.publicNodes(ctx) {
		if n.Up && n.Public() {
			byID[n.ID] = n
		}
	}
	serving := map[string]map[string]bool{} // 호스트 → 노드
	for nk, rs := range routes {
		if _, ok := byID[nk]; !ok {
			continue
		}
		for _, r := range rs {
			for _, h := range r.Hosts {
				h = strings.ToLower(h)
				if serving[h] == nil {
					serving[h] = map[string]bool{}
				}
				serving[h][nk] = true
			}
		}
	}
	rings := map[string][]string{}
	for h, set := range serving {
		if len(set) < 2 {
			continue
		}
		ring := make([]string, 0, len(set))
		for id := range set {
			ring = append(ring, id)
		}
		sort.Strings(ring)
		rings[h] = ring
	}
	if len(rings) == 0 {
		return
	}
	for nk, rs := range routes {
		self, ok := byID[nk]
		if !ok {
			continue
		}
		for i := range rs {
			r := &rs[i]
			if len(r.Hosts) == 0 {
				continue
			}
			ring := rings[strings.ToLower(r.Hosts[0])]
			if len(ring) < 2 {
				continue
			}
			pos := sort.SearchStrings(ring, nk)
			next := byID[ring[(pos+1)%len(ring)]]
			if addrs := relayAddrs(self, next); len(addrs) > 0 {
				r.ACMERelay = &proxy.ACMERelay{Self: nk, Next: next.ID, NextAddrs: addrs}
			}
		}
		routes[nk] = rs
	}
}

// relayAddrs는 self가 next에 닿을 주소:80이다. self에 있는 주소 체계를 먼저 쓴다.
func relayAddrs(self, next publicNode) []string {
	var v4, v6 []string
	if next.IPv4 != "" {
		v4 = []string{net.JoinHostPort(next.IPv4, "80")}
	}
	if next.IPv6 != "" {
		v6 = []string{net.JoinHostPort(next.IPv6, "80")}
	}
	var out []string
	if self.IPv4 == "" && self.IPv6 != "" {
		out = append(v6, v4...)
	} else {
		out = append(v4, v6...)
	}
	r := &proxy.ACMERelay{Self: "node_a", Next: "node_b", NextAddrs: out}
	if !proxy.ValidACMERelay(r) {
		return nil // 사설 주소 등: 넘길 수 없다
	}
	return out
}
