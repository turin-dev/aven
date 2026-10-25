package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// ip6tables는 egress.* 작업이 쓰는 ip6tables다(테스트에서 가짜로 바꾼다). nil을 돌려주면 설치되어 있지 않은 노드다.
var ip6tables = func() node.IP6Tables { return node.SystemIP6Tables() }

// egressDocker는 egress.*에 필요한 docker 기능이다(agent의 Docker 인터페이스 + PID 조회).
func (h *Handler) egressDocker() (interface {
	Docker
	pidLister
}, error) {
	pl, ok := h.Docker.(pidLister)
	if !ok {
		return nil, errors.New("egress is not supported by this docker driver")
	}
	return struct {
		Docker
		pidLister
	}{h.Docker, pl}, nil
}

// handleEgress는 egress.status와 egress.ensure를 처리한다(agent.go의 dispatcher에서 부른다).
// 서버는 환경 ID와 instance ID만 보낸다: 대상 컨테이너, 주소, 규칙 모양은 agent가 정한다.
func (h *Handler) handleEgress(ctx context.Context, op string, payload []byte) (any, error) {
	d, err := h.egressDocker()
	if err != nil {
		return nil, err
	}
	switch op {
	case agentprotocol.OpEgressStatus:
		var r agentprotocol.EgressStatusRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		if !node.ValidEgressEnvID(r.EnvID) {
			return nil, fmt.Errorf("environment %q is not allowed", r.EnvID)
		}
		return node.EgressProbe(ctx, d, ip6tables(), procRoot, r.EnvID)
	case agentprotocol.OpEgressEnsure:
		var r agentprotocol.EgressEnsureRequest
		if err := decodeStrict(payload, &r); err != nil {
			return nil, err
		}
		res, err := node.EgressEnsure(ctx, d, ip6tables(), r.Blocked)
		if err != nil {
			return nil, err
		}
		return res, nil
	}
	return nil, fmt.Errorf("operation %q is not allowed", op)
}
