package deploy

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/mesh"
	"github.com/turin-dev/aven/internal/store"
)

// WithoutFailover는 import의 failover 후보(nodes)를 뺀 설정이다(그 필드를 모르는 옛 agent용; 주 노드 node만 남는다).
func WithoutFailover(c mesh.Config) mesh.Config {
	out := c
	out.Imports = make([]mesh.Import, len(c.Imports))
	for i, im := range c.Imports {
		im.Nodes = nil
		out.Imports[i] = im
	}
	return out
}

// WithoutAliases는 import의 사설 DNS 별칭(aliases)을 뺀 설정이다(그 필드를 모르는 옛 agent용; 서비스 이름 alias만 남는다).
func WithoutAliases(c mesh.Config) mesh.Config {
	out := c
	out.Imports = make([]mesh.Import, len(c.Imports))
	for i, im := range c.Imports {
		im.Aliases = nil
		out.Imports[i] = im
	}
	return out
}

// noAliasesRetry는 aliases를 거부한 agent에 다시 aliases를 보내 보기까지의 시간이다(그 사이 agent가 업그레이드될 수 있다).
const noAliasesRetry = 10 * time.Minute

// sendMeshConfig는 원격 노드에 mesh 설정을 보낸다. 옛 agent가 모르는 필드("aliases", "nodes")로 거부하면 그 필드를 빼고
// 다시 보낸다. aliases를 거부한 노드는 기억해 두고(noAliasesRetry 동안, 노드가 끊기면 잊는다) 처음부터 빼고 보낸다.
func (m *Manager) sendMeshConfig(ctx context.Context, nodeID string, cfg mesh.Config, st *mesh.Status) error {
	m.meshSt.statusMu.Lock()
	at, skip := m.meshSt.noAliases[nodeID]
	if skip && time.Since(at) >= noAliasesRetry {
		delete(m.meshSt.noAliases, nodeID)
		skip = false
	}
	m.meshSt.statusMu.Unlock()
	if skip {
		cfg = WithoutAliases(cfg)
	}
	err := m.Agents.CallStream(ctx, nodeID, agentprotocol.OpMeshEnsure, cfg, st, nil)
	// 옛 agent는 모르는 필드를 하나씩만 알린다: 최대 두 번(aliases, nodes) 빼고 다시 보낸다.
	for range 2 {
		switch {
		case err != nil && strings.Contains(err.Error(), `unknown field "aliases"`):
			m.meshSt.statusMu.Lock()
			if m.meshSt.noAliases == nil {
				m.meshSt.noAliases = map[string]time.Time{}
			}
			m.meshSt.noAliases[nodeID] = time.Now()
			m.meshSt.statusMu.Unlock()
			cfg = WithoutAliases(cfg)
		case err != nil && strings.Contains(err.Error(), `unknown field "nodes"`):
			// failover 후보(nodes)를 모르는 옛 agent: 주 노드만으로 다시 보낸다.
			cfg = WithoutFailover(cfg)
		default:
			return err
		}
		err = m.Agents.CallStream(ctx, nodeID, agentprotocol.OpMeshEnsure, cfg, st, nil)
	}
	return err
}

// forgetNoAliases는 끊긴 노드의 "aliases 거부" 기억을 지운다(다시 연결되면 업그레이드됐을 수 있다).
func (m *Manager) forgetNoAliases(nodeID string) {
	m.meshSt.statusMu.Lock()
	delete(m.meshSt.noAliases, nodeID)
	m.meshSt.statusMu.Unlock()
}

// noteNodeAvailability는 연결돼 있던 원격 노드가 끊긴 것을 감지하면 그 노드에서 도는 서비스마다 node.unavailable을 낸다.
// 한 번 끊길 때 한 번만 낸다(다시 연결되면 다시 감시한다). 서버가 막 시작해 아직 연결된 적 없는 노드는 알리지 않는다.
func (m *Manager) noteNodeAvailability(ctx context.Context, ps []MeshPlacement) {
	if m.Agents == nil {
		return
	}
	nodes := m.remoteNodes(ctx)
	var down []*store.Node
	m.meshSt.statusMu.Lock()
	if m.meshSt.online == nil {
		m.meshSt.online = map[string]bool{}
	}
	seen := map[string]bool{}
	for _, n := range nodes {
		seen[n.ID] = true
		on := m.nodeOnline(n.ID)
		if was, ok := m.meshSt.online[n.ID]; ok && was && !on {
			down = append(down, n)
		}
		m.meshSt.online[n.ID] = on
	}
	for id := range m.meshSt.online {
		if !seen[id] {
			delete(m.meshSt.online, id) // 지워진 노드
		}
	}
	m.meshSt.statusMu.Unlock()
	if len(down) == 0 || m.Events == nil {
		return
	}
	for _, n := range down {
		done := map[string]bool{}
		for _, p := range ps {
			if p.Node != nodeKey(n.ID) || done[p.EnvID+"/"+p.ServiceID] || p.ProjectID == "" {
				continue
			}
			done[p.EnvID+"/"+p.ServiceID] = true
			env, svc := p.EnvID, p.ServiceID
			m.Events.Emit(ctx, p.ProjectID, &env, &svc, "node.unavailable",
				fmt.Sprintf("노드 %s에 연결할 수 없습니다. %s의 이 노드 replica를 도메인 DNS 레코드와 노드 간 사설 네트워크(mesh)에서 뺐습니다", n.Name, p.Service))
		}
	}
}

// knownOffline은 원격 노드가 agent hub 기준으로 연결돼 있지 않은지다(hub가 없으면 알 수 없으므로 false).
func (m *Manager) knownOffline(nodeID string) bool {
	return !isLocal(nodeID) && m.Agents != nil && !m.Agents.Online(nodeID)
}
