package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/netlog"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/proxy"
	"github.com/turin-dev/aven/internal/store"
)

// 원격 노드가 연결돼 있는 동안 agent가 이 노드의 keel-proxy access log와 docker 이벤트를 서버로 흘려보낸다.
// 서버는 로컬 노드와 같은 허브/핸들러(HTTPLogHub, onContainerEvent)에 넣는다. 그래서 HTTP 로그 조회와
// sleeping 유휴 판정, crash 감지가 노드와 상관없이 같은 방식으로 동작한다.

// StartNodeStreams는 노드가 (다시) 연결됐을 때 그 노드의 스트림을 새로 시작한다(이전 것은 취소).
func (m *Manager) StartNodeStreams(nodeID string) {
	if isLocal(nodeID) || m.streamCtx == nil {
		return
	}
	m.mu.Lock()
	if cancel := m.streams[nodeID]; cancel != nil {
		cancel()
	}
	ctx, cancel := context.WithCancel(m.streamCtx)
	m.streams[nodeID] = cancel
	m.mu.Unlock()
	rd := &remoteDriver{hub: m.Agents, node: nodeID}
	go m.followLoop(ctx, nodeID, "access log", func(ctx context.Context) error {
		return rd.stream(ctx, agentprotocol.OpAccessLogs, struct{}{}, nil, func(b json.RawMessage) {
			var l proxy.AccessLog
			if json.Unmarshal(b, &l) == nil {
				m.HTTPLogs.Add(l)
			}
		})
	})
	go m.followLoop(ctx, nodeID, "events", func(ctx context.Context) error {
		return rd.stream(ctx, agentprotocol.OpEvents, struct{}{}, nil, func(b json.RawMessage) {
			var c agentprotocol.EventChunk
			if json.Unmarshal(b, &c) == nil {
				m.onContainerEvent(ctx, nodeID, node.ContainerEvent{Action: c.Action, Name: c.Name, Labels: c.Labels, Exit: c.Exit})
			}
		})
	})
	go m.followLoop(ctx, nodeID, "network logs", func(ctx context.Context) error {
		err := rd.stream(ctx, agentprotocol.OpNetLogFollow, struct{}{}, nil, func(b json.RawMessage) {
			var e netlog.Event
			if json.Unmarshal(b, &e) == nil {
				m.NetworkLogs.Add(nodeID, e)
			}
		})
		if err != nil && strings.Contains(err.Error(), "is not allowed") {
			// 이 기능 이전 버전의 agent: 다시 시도하지 않고 연결을 기다린다(재연결하면 StartNodeStreams가 다시 부른다).
			m.NetworkLogs.Add(nodeID, netlog.Event{Kind: "status", Status: &netlog.Status{Node: nodeID, Error: "agent does not support network logs; upgrade aven-agent"}})
			<-ctx.Done()
			return ctx.Err()
		}
		return err
	})
}

// StopNodeStreams는 노드의 스트림을 멈춘다(노드 삭제 등).
func (m *Manager) StopNodeStreams(nodeID string) {
	m.mu.Lock()
	if cancel := m.streams[nodeID]; cancel != nil {
		cancel()
		delete(m.streams, nodeID)
	}
	m.mu.Unlock()
}

// followLoop는 장기 스트림 호출을 연결이 끊기거나 ctx가 끝날 때까지 다시 건다.
func (m *Manager) followLoop(ctx context.Context, nodeID, what string, call func(context.Context) error) {
	for ctx.Err() == nil {
		cctx, cancel := withLongDeadline(ctx, 24*time.Hour)
		err := call(cctx)
		cancel()
		if ctx.Err() != nil || errors.Is(err, agentprotocol.ErrOffline) {
			return
		}
		if err != nil && m.Log != nil {
			m.Log.Warn("node stream ended; retrying", "node", nodeID, "stream", what, "err", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
}

// ---------------- wake (agent → server) ----------------

// HandleAgentRequest는 agent가 보낸 요청(wake)을 처리하고 답한다(hub.OnRequest).
func (m *Manager) HandleAgentRequest(nodeID string, msg agentprotocol.Message) {
	ctx, cancel := context.WithTimeout(context.Background(), wakeTimeout+15*time.Second)
	defer cancel()
	reply := agentprotocol.Message{ID: msg.ID, Operation: msg.Operation}
	switch msg.Operation {
	case agentprotocol.OpWake:
		var req agentprotocol.WakeRequest
		if err := json.Unmarshal(msg.Payload, &req); err != nil {
			reply.Error = "invalid payload"
			break
		}
		res, err := m.WakeNode(ctx, nodeID, req.Host)
		if err != nil {
			reply.Error = err.Error()
			break
		}
		reply.Payload, _ = json.Marshal(res)
	default:
		reply.Error = fmt.Sprintf("operation %q is not allowed", msg.Operation)
	}
	if err := m.Agents.Reply(ctx, nodeID, reply); err != nil && m.Log != nil {
		m.Log.Warn("reply to agent", "node", nodeID, "op", msg.Operation, "err", err)
	}
}

// WakeNode는 노드의 agent가 요청한 host의 서비스를 깨우고 요청을 넘길 컨테이너를 알려준다.
// 그 host가 요청한 노드에서 도는 서비스일 때만 허용한다(다른 노드의 서비스를 깨울 수 없다).
func (m *Manager) WakeNode(ctx context.Context, nodeID, host string) (*agentprotocol.WakeResult, error) {
	id, ok := m.HTTPLogs.InstanceForHost(host)
	if !ok {
		return nil, errors.New("unknown host")
	}
	in, err := m.Store.GetInstance(ctx, id)
	if err != nil {
		return nil, err
	}
	if m.homeNode(ctx, in) != nodeKey(nodeID) {
		return nil, errors.New("host is not served by this node")
	}
	m.HTTPLogs.Touch(id)
	if err := m.Wake(ctx, id); err != nil {
		return nil, err
	}
	t, err := m.Load(ctx, id)
	if err != nil {
		return nil, err
	}
	if t.Instance.CurrentDeploymentID == nil {
		return nil, errors.New("no running deployment")
	}
	d, err := m.Store.GetDeployment(ctx, *t.Instance.CurrentDeploymentID)
	if err != nil || len(d.Containers) == 0 {
		return nil, errors.New("no running deployment")
	}
	states, err := m.driverFor(d).InspectMany(ctx, d.Containers)
	if err != nil {
		return nil, err
	}
	for _, c := range d.Containers {
		if st := states[c]; st != nil && st.Running {
			return &agentprotocol.WakeResult{Network: EnvNetwork(t.Env.ID), Container: c, Port: m.targetPort(t, d, host)}, nil
		}
	}
	return nil, errors.New("no running replica")
}

// targetPort는 host로 들어온 요청이 가야 할 컨테이너 포트다.
func (m *Manager) targetPort(t *Target, d *store.Deployment, host string) int {
	// 정확히 같은 호스트가 와일드카드(*.example.com)보다 우선한다.
	for _, want := range []string{host, proxy.WildcardFor(host)} {
		for _, dm := range t.Instance.Domains {
			if want != "" && dm.Host == want {
				return routePort(d, t.Instance.Port, dm.TargetPort, targetIfSet)
			}
		}
	}
	return routePort(d, t.Instance.Port, nil, targetIfSet)
}
