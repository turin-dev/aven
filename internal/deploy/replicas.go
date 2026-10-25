package deploy

import (
	"context"

	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// FillReplicas는 진행 중이거나 live인 배포의 replica 상태를 docker에서 채운다.
func (m *Manager) FillReplicas(ctx context.Context, deps ...*store.Deployment) {
	// 노드별로 한 번씩 조회한다. 꺼진 원격 노드의 replica는 마지막 상태를 알 수 없어 exited로 보인다.
	byNode := map[string][]string{}
	for _, d := range deps {
		d.Replicas = []store.Replica{}
		if d.Status.Terminal() && d.Status != store.DepLive && d.Status != store.DepCrashed {
			continue
		}
		for _, g := range d.Groups() {
			byNode[nodeKey(g.NodeID)] = append(byNode[nodeKey(g.NodeID)], g.Containers...)
		}
	}
	if len(byNode) == 0 {
		return
	}
	states := map[string]*node.ContainerState{}
	for n, names := range byNode {
		if len(names) == 0 || !m.nodeOnline(n) {
			continue
		}
		st, err := m.driverForNode(n).InspectMany(ctx, names)
		if err != nil {
			continue
		}
		for k, v := range st {
			states[k] = v
		}
	}
	for _, d := range deps {
		if d.Status.Terminal() && d.Status != store.DepLive && d.Status != store.DepCrashed {
			continue
		}
		for _, c := range d.AllContainers() {
			st := states[c]
			r := store.Replica{Name: c, Status: "exited", NodeID: nodeKey(d.NodeOf(c))}
			if st != nil {
				r.Restarts = st.RestartCount
				switch {
				case st.Status == "restarting":
					r.Status = "restarting"
				case st.Running && (d.Status == store.DepDeploying || d.Status == store.DepHealthchecking):
					r.Status = "starting"
				case st.Running:
					r.Status = "running"
				case st.Status == "created":
					r.Status = "starting"
				}
			}
			d.Replicas = append(d.Replicas, r)
		}
	}
}

// InstanceRestarts는 인스턴스 현재 배포 replica들의 재시작 횟수 합이다(배포가 없거나 상태를 모르면 false). 알림 규칙 restarts용.
func (m *Manager) InstanceRestarts(ctx context.Context, in *store.Instance) (int, bool) {
	if in.CurrentDeploymentID == nil {
		return 0, false
	}
	d, err := m.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return 0, false
	}
	m.FillReplicas(ctx, d)
	if len(d.Replicas) == 0 {
		return 0, false
	}
	n := 0
	for _, r := range d.Replicas {
		n += r.Restarts
	}
	return n, true
}
