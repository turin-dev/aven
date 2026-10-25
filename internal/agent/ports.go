package agent

import (
	"context"
	"errors"
	"fmt"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// procRoot는 컨테이너 PID의 /proc를 읽을 위치다(테스트에서 바꾼다). aven-agent는 root로 돌기 때문에 다른 namespace의
// /proc/<pid>/net/tcp도 읽을 수 있다.
var procRoot = "/proc"

// pidLister는 컨테이너의 init PID를 알려 주는 docker 기능이다. node.Docker가 구현한다(agent의 Docker 인터페이스에는 없다).
type pidLister interface {
	ContainerPIDs(ctx context.Context, names []string) (map[string]int, error)
}

// portsListening은 ports.listening을 처리한다: 이 노드에서 instance의 app 컨테이너가 listen 중인 TCP 포트다.
// 서버가 컨테이너 이름이나 PID를 정하지 못하게 instance id만 받고, 대상은 keel.managed 라벨이 있는 app 컨테이너로 agent가 고른다.
func (h *Handler) portsListening(ctx context.Context, r agentprotocol.PortsListeningRequest) (*agentprotocol.PortsListeningResult, error) {
	if !instanceRe.MatchString(r.InstanceID) {
		return nil, fmt.Errorf("instance %q is not allowed", r.InstanceID)
	}
	pl, ok := h.Docker.(pidLister)
	if !ok {
		return nil, errors.New("port detection is not supported by this docker driver")
	}
	return node.InstanceListeningPorts(ctx, struct {
		Docker
		pidLister
	}{h.Docker, pl}, procRoot, r.InstanceID)
}

// handlePortsListening은 ports.listening 요청을 엄격하게 읽어 처리한다(agent.go의 dispatcher에서 부른다).
func (h *Handler) handlePortsListening(ctx context.Context, payload []byte) (any, error) {
	var r agentprotocol.PortsListeningRequest
	if err := decodeStrict(payload, &r); err != nil {
		return nil, err
	}
	return h.portsListening(ctx, r)
}
