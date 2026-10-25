package deploy

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// ErrAgentTooOld는 원격 agent가 exec.run을 모를 때다.
var ErrAgentTooOld = errors.New("이 노드의 aven-agent가 일회성 exec(exec.run)를 지원하지 않습니다. agent를 업그레이드하세요")

// ExecRun은 노드의 컨테이너에서 TTY 없이 명령을 한 번 실행하고 출력을 모은다(stdout 상한 req.MaxOutput, 실행 시간 timeout).
// 로컬은 Engine API, 원격은 agent의 exec.run이다.
func (m *Manager) ExecRun(ctx context.Context, nodeID, container string, req node.ExecRunRequest, timeout time.Duration) (*node.ExecOutput, error) {
	if timeout <= 0 || timeout > agentprotocol.MaxExecRunTimeoutS*time.Second {
		timeout = 30 * time.Second
	}
	req.MaxOutput = node.ClampExecOutput(req.MaxOutput)
	if isLocal(nodeID) {
		if m.Docker == nil {
			return nil, errors.New("docker is not configured")
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return m.Docker.ExecRun(ctx, container, req)
	}
	if m.Agents == nil || !m.Agents.Online(nodeID) {
		return nil, ErrNodeUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, timeout+10*time.Second)
	defer cancel()
	var res agentprotocol.ExecRunResult
	err := m.Agents.CallStream(ctx, nodeID, agentprotocol.OpExecRun, agentprotocol.ExecRunRequest{
		Name: container, Cmd: req.Cmd, Env: req.Env, Stdin: req.Stdin, MaxOutput: req.MaxOutput, TimeoutS: int(timeout / time.Second),
	}, &res, nil)
	if err != nil {
		if strings.Contains(err.Error(), `operation "exec.run" is not allowed`) {
			return nil, ErrAgentTooOld
		}
		return nil, err
	}
	return &res, nil
}
