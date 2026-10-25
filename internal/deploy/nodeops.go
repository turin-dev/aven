package deploy

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/store"
)

// NodeInstances는 노드에 배치됐거나 현재 배포가 그 노드에서 도는 인스턴스들이다.
func (m *Manager) NodeInstances(ctx context.Context, nodeID string) ([]*store.Instance, error) {
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	var out []*store.Instance
	for _, in := range insts {
		if nodeKey(in.NodeID) == nodeID || m.homeNode(ctx, in) == nodeID {
			out = append(out, in)
		}
	}
	return out, nil
}

// ForceRemoveNode는 노드의 인스턴스를 offline으로 만들고 로컬 배치로 되돌린 뒤 노드 단위 상태를 정리한다.
// 노드 행 삭제와 인증서 폐기는 호출자가 한다. 원격 노드의 컨테이너/볼륨은 그 노드에 그대로 남는다(노드가 더는 aven에 속하지 않는다).
func (m *Manager) ForceRemoveNode(ctx context.Context, nodeID string) (int, error) {
	if isLocal(nodeID) {
		return 0, errors.New("local node cannot be removed")
	}
	m.StopNodeStreams(nodeID)
	insts, err := m.NodeInstances(ctx, nodeID)
	if err != nil {
		return 0, err
	}
	online := m.nodeOnline(nodeID)
	for _, in := range insts {
		m.cancelInstance(ctx, in.ID)
		if online {
			// 연결돼 있으면 그 노드의 컨테이너를 정리한다(꺼져 있으면 노드에 그대로 남는다).
			drv := m.driverForNode(nodeID)
			for _, c := range m.instanceContainers(ctx, nodeID, in.ID) {
				m.markDrain(c)
				drv.Remove(ctx, c, 10)
				m.undrain(c)
			}
		}
		if in.CurrentDeploymentID != nil {
			if d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID); err == nil && nodeKey(d.NodeID) == nodeID {
				if d.Status == store.DepLive || d.Status == store.DepCrashed {
					m.Store.SetDeploymentStatus(ctx, d.ID, store.DepRemoved)
				}
				m.Store.ClearInstanceCurrent(ctx, in.ID)
			}
		}
		if err := m.Store.SetInstanceStatus(ctx, in.ID, store.InstOffline, nil); err != nil {
			return 0, err
		}
		if nodeKey(in.NodeID) == nodeID {
			if err := m.Store.SetInstanceNode(ctx, in.ID, store.LocalNodeID); err != nil {
				return 0, err
			}
		}
		if t, err := m.Load(ctx, in.ID); err == nil {
			m.emit(ctx, t, "service.offline", fmt.Sprintf("%s (%s): 노드 %s가 제거되어 서비스가 offline이 되었습니다", t.Service.Name, t.Env.Name, nodeID))
		}
	}
	m.SyncProxy(ctx)
	return len(insts), nil
}

// DrainMove는 drain으로 다른 노드에 다시 배포하기 시작한 인스턴스다.
type DrainMove struct {
	InstanceID   string `json:"instance_id"`
	ServiceID    string `json:"service_id"`
	To           string `json:"to"`
	DeploymentID string `json:"deployment_id,omitempty"` // 배포 없이 배치만 바꾼 인스턴스는 비어 있다
}

type DrainSkip struct {
	InstanceID string `json:"instance_id"`
	ServiceID  string `json:"service_id"`
	Reason     string `json:"reason"`
}

// DrainTargets는 drain으로 옮길 수 있는 노드(스케줄 가능, 연결됨, 대상 노드가 아님)와 그 노드의 현재 인스턴스 수다.
func (m *Manager) drainTargets(ctx context.Context, from string) (map[string]int, error) {
	nodes, err := m.Store.ListNodes(ctx)
	if err != nil {
		return nil, err
	}
	load := map[string]int{}
	for _, n := range nodes {
		if n.ID == from || !n.Schedulable || n.Draining || !m.nodeOnline(n.ID) {
			continue
		}
		load[nodeKey(n.ID)] = 0
	}
	insts, err := m.Store.ListInstances(ctx)
	if err != nil {
		return nil, err
	}
	for _, in := range insts {
		if _, ok := load[m.homeNode(ctx, in)]; ok {
			load[m.homeNode(ctx, in)]++
		}
	}
	return load, nil
}

// DrainNode는 노드를 draining으로 표시하고(새 배포를 받지 않는다) 그 노드의 인스턴스를 다른 노드에 다시 배포한다.
// target이 비어 있으면 인스턴스마다 인스턴스 수가 가장 적은 다른 노드를 고른다. 새 노드에서 배포가 live가 되면 이전 노드의 컨테이너가 지워지고,
// 실패하면 이전 노드가 계속 서비스한다. 볼륨이 있으면 데이터는 move_volumes처럼 새 노드로 복사된다.
// CLI 업로드로 배포된 서비스는 소스가 남아 있지 않아 옮기지 못하고 skipped로 돌려준다.
func (m *Manager) DrainNode(ctx context.Context, nodeID, target string) ([]DrainMove, []DrainSkip, error) {
	if isLocal(nodeID) {
		return nil, nil, errors.New("local node cannot be drained")
	}
	if target != "" && target == nodeID {
		return nil, nil, errors.New("target must be a different node")
	}
	load, err := m.drainTargets(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	if target != "" {
		if _, ok := load[target]; !ok {
			return nil, nil, fmt.Errorf("target node %s is not available (it must exist, be schedulable, connected and not draining)", target)
		}
		load = map[string]int{target: 0}
	}
	if len(load) == 0 {
		return nil, nil, errors.New("no other schedulable online node to move the instances to")
	}
	if err := m.Store.SetNodeDraining(ctx, nodeID, true); err != nil {
		return nil, nil, err
	}
	insts, err := m.NodeInstances(ctx, nodeID)
	if err != nil {
		return nil, nil, err
	}
	sort.Slice(insts, func(i, j int) bool { return insts[i].ID < insts[j].ID })
	var moves []DrainMove
	var skips []DrainSkip
	pick := func() string {
		best := ""
		for n, c := range load {
			if best == "" || c < load[best] || (c == load[best] && n < best) {
				best = n
			}
		}
		load[best]++
		return best
	}
	for _, in := range insts {
		skip := func(reason string) {
			skips = append(skips, DrainSkip{InstanceID: in.ID, ServiceID: in.ServiceID, Reason: reason})
		}
		var cur *store.Deployment
		if in.CurrentDeploymentID != nil {
			cur, _ = m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
		}
		if cur != nil && strings.HasPrefix(cur.Source, "upload:") {
			skip("deployed from a CLI upload; the source is not kept, so redeploy it with the CLI after setting its node_id")
			continue
		}
		to := pick()
		if err := m.Store.SetInstanceNode(ctx, in.ID, to); err != nil {
			skip(err.Error())
			continue
		}
		in.NodeID = to
		mv := DrainMove{InstanceID: in.ID, ServiceID: in.ServiceID, To: to}
		// 중지된 서비스나 아직 배포한 적 없는 서비스는 배치만 바꾼다.
		if cur == nil || in.Status == store.InstOffline {
			moves = append(moves, mv)
			continue
		}
		d, err := m.Enqueue(ctx, in, EnqueueOpts{Trigger: "drain"})
		if err != nil {
			skip(err.Error())
			continue
		}
		mv.DeploymentID = d.ID
		moves = append(moves, mv)
	}
	return moves, skips, nil
}

// NodeStats는 연결된 원격 노드의 사용량이다.
func (m *Manager) NodeStats(ctx context.Context, nodeID string) (agentprotocol.NodeStats, error) {
	return (&remoteDriver{hub: m.Agents, node: nodeID}).nodeStats(ctx)
}

// OnlineRemoteNodeIDs는 연결된 원격 노드 ID들이다.
func (m *Manager) OnlineRemoteNodeIDs(ctx context.Context) []string {
	var out []string
	for _, n := range m.remoteNodes(ctx) {
		if m.nodeOnline(n.ID) {
			out = append(out, n.ID)
		}
	}
	return out
}
