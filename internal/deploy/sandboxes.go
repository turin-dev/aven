package deploy

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// ErrSandboxNodeOffline은 샌드박스가 있는 원격 노드가 연결되어 있지 않다는 뜻이다.
var ErrSandboxNodeOffline = errors.New("the node running this sandbox is offline")

// StartSandbox는 샌드박스의 네트워크를 준비하고 컨테이너를 띄운다.
// isolated는 proxy도 다른 서비스도 붙지 않은 전용 bridge, private은 환경 네트워크에 붙는다.
func (m *Manager) StartSandbox(ctx context.Context, env *store.Environment, b *store.Sandbox, spec node.RunSpec) error {
	if b.NetworkMode == store.SandboxPrivate {
		if err := m.ensureEnvNetwork(ctx, env, b.NodeID); err != nil {
			return err
		}
	} else if err := m.driverForNode(b.NodeID).EnsureNamedNetwork(ctx, b.NetworkName, map[string]string{
		node.LabelProject: b.ProjectID,
		node.LabelEnv:     b.EnvironmentID,
		node.LabelSandbox: b.ID,
	}); err != nil {
		return err
	}
	return m.driverForNode(b.NodeID).Run(ctx, spec)
}

func (m *Manager) InspectSandbox(ctx context.Context, b *store.Sandbox) (*node.ContainerState, error) {
	return m.driverForNode(b.NodeID).Inspect(ctx, b.ContainerName)
}

// RemoveSandbox는 컨테이너와(isolated면) 전용 네트워크를 지운다. 이미 없으면 성공이다.
func (m *Manager) RemoveSandbox(ctx context.Context, b *store.Sandbox) error {
	if !isLocal(b.NodeID) && !m.nodeOnline(b.NodeID) {
		return ErrSandboxNodeOffline
	}
	drv := m.driverForNode(b.NodeID)
	if err := drv.Remove(ctx, b.ContainerName, 0); err != nil && !errors.Is(err, node.ErrNoContainer) {
		return err
	}
	if b.NetworkMode == store.SandboxPrivate {
		return nil
	}
	return drv.RemoveNetwork(ctx, b.NetworkName)
}

// CommitSandbox는 실행 중인 샌드박스의 파일시스템을 노드 로컬 이미지(keel/sandbox:*)로 남긴다.
func (m *Manager) CommitSandbox(ctx context.Context, nodeID, container, image string) error {
	if isLocal(nodeID) {
		if m.Docker == nil {
			return errors.New("docker is not configured")
		}
		return m.Docker.CommitSandbox(ctx, container, image)
	}
	if m.Agents == nil || !m.Agents.Online(nodeID) {
		return ErrSandboxNodeOffline
	}
	return m.Agents.CallStream(ctx, nodeID, agentprotocol.OpSandboxCommit,
		agentprotocol.SandboxCommitRequest{Name: container, Image: image}, nil, func(json.RawMessage) {})
}

func (m *Manager) RemoveSandboxImage(ctx context.Context, nodeID, image string) error {
	if image == "" {
		return nil
	}
	if !isLocal(nodeID) && !m.nodeOnline(nodeID) {
		return ErrSandboxNodeOffline
	}
	m.driverForNode(nodeID).RemoveImage(ctx, image)
	return nil
}

// SandboxContainers는 노드에 있는 샌드박스 컨테이너 이름 → 샌드박스 id다(고아 정리용).
func (m *Manager) SandboxContainers(ctx context.Context, nodeID string) (map[string]string, error) {
	if !isLocal(nodeID) && !m.nodeOnline(nodeID) {
		return nil, ErrSandboxNodeOffline
	}
	cs, err := m.driverForNode(nodeID).ListManaged(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for name, labels := range cs {
		if labels[node.LabelRole] == node.RoleSandbox {
			out[name] = labels[node.LabelSandbox]
		}
	}
	return out, nil
}
