package agent

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// ipRunner는 ipv6.* 작업이 쓰는 ip 명령이다(테스트에서 가짜로 바꾼다). nil이면 쓸 수 없는 노드다(root가 아님 등).
var ipRunner = func() node.IPRunner { return node.SystemIPRunner() }

// handleIPv6는 ipv6.addrs와 ipv6.ensure를 처리한다(agent.go의 dispatcher에서 부른다).
// 서버는 풀과 주소 목록만 보낸다. agent는 풀 형식, 모든 주소가 풀 안인지, 풀이 이 노드의 공인 IPv6를 담지 않는지 다시 검사한다.
// 인터페이스는 agent 설정(KEEL_IPV6_IFACE)이 서버 값보다 우선한다.
func (h *Handler) handleIPv6(ctx context.Context, op string, payload []byte) (any, error) {
	switch op {
	case agentprotocol.OpIPv6Addrs:
		if err := decodeStrict(payload, &struct{}{}); err != nil {
			return nil, err
		}
		r := ipRunner()
		if r == nil {
			return nil, fmt.Errorf("%w: ip 명령을 쓸 수 없습니다", node.ErrIPv6Unsupported)
		}
		addrs, err := node.ListHostIPv6(ctx, r, "")
		if err != nil {
			return nil, err
		}
		if addrs == nil {
			addrs = []node.HostIPv6Addr{}
		}
		return agentprotocol.IPv6AddrsResult{Addrs: addrs}, nil
	case agentprotocol.OpIPv6Ensure:
		var req agentprotocol.IPv6EnsureRequest
		if err := decodeStrict(payload, &req); err != nil {
			return nil, err
		}
		pool, addrs, err := node.ParseIPv6PoolAddresses(req.Pool, req.Addresses)
		if err != nil {
			return nil, err
		}
		if req.Iface != "" && !node.ValidIPv6Iface(req.Iface) {
			return nil, fmt.Errorf("interface %q is not allowed", req.Iface)
		}
		var protect []netip.Addr
		if a, err := netip.ParseAddr(strings.TrimSpace(h.IPv6)); err == nil {
			protect = append(protect, a)
		}
		r := ipRunner()
		iface := h.ipv6Iface(req.Iface)
		if iface == "" && r != nil {
			iface, err = node.DefaultIPv6Iface(ctx, r, h.IPv6)
			if err != nil {
				return nil, err
			}
		}
		return node.IPv6Ensure(ctx, r, iface, pool, addrs, protect)
	}
	return nil, fmt.Errorf("operation %q is not allowed", op)
}

// ipv6Iface는 주소를 붙일 인터페이스다: agent 설정(Handler.IPv6Iface, --ipv6-iface/KEEL_IPV6_IFACE) → 서버가 보낸 값 → ""(자동).
func (h *Handler) ipv6Iface(requested string) string {
	if v := strings.TrimSpace(h.IPv6Iface); v != "" {
		return v
	}
	return requested
}
