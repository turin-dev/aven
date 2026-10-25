package agent

import (
	"context"
	"errors"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/mesh"
)

// meshReady는 mesh gateway가 켜져 있고 노드 인증서가 있는지다(옛 Bearer 비밀키 agent는 mesh에 참여하지 못한다).
func (h *Handler) meshReady() bool {
	if h.Mesh == nil || h.MeshPort == 0 || h.Mesh.Cert == nil {
		return false
	}
	c, err := h.Mesh.Cert()
	return err == nil && c != nil
}

// meshEnsure는 mesh.ensure를 처리한다. 설정은 mesh.Validate로 엄격하게 검사한다:
// peer 주소는 사설 대역의 IP:포트만, 이름/ID/포트는 정해진 형식만 받는다. listen 주소와 forwarder 이미지·명령은 agent가 정한다.
// 이 작업이 publish하는 포트는 없다(gateway listener는 agent 설정의 사설 주소 한 곳뿐이고 forwarder는 포트를 publish하지 않는다).
func (h *Handler) meshEnsure(ctx context.Context, r agentprotocol.MeshEnsureRequest) (agentprotocol.MeshStatus, error) {
	if !h.meshReady() {
		return mesh.Status{Enabled: false, Peers: []mesh.PeerStatus{}, Reason: "mesh is disabled on this node"}, errors.New("mesh is disabled on this node (needs --private-address, a node certificate and --mesh=on)")
	}
	if err := mesh.Validate(r, h.NodeID); err != nil {
		return mesh.Status{}, err
	}
	// 일부 import가 실패해도(네트워크가 아직 없음 등) 나머지는 반영됐다. 오류는 상태의 error로 알린다.
	st, _ := mesh.Ensure(ctx, h.Mesh, h.Docker, r)
	return st, nil
}
