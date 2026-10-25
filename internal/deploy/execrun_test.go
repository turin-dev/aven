package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// ExecRun은 execDocker를 node.ExecRunner로 만든다: 받은 요청을 기록하고 stdin을 stdout으로 돌려준다.
func (d *execDocker) ExecRun(_ context.Context, container string, req node.ExecRunRequest) (*node.ExecOutput, error) {
	d.mu.Lock()
	d.cmd = append([]string{container}, append(req.Cmd, req.Env...)...)
	d.mu.Unlock()
	return &node.ExecOutput{Stdout: append([]byte("out:"), req.Stdin...), Stderr: []byte("e"), ExitCode: 2}, nil
}

func TestRemoteExecRun(t *testing.T) {
	d, hub := newExecLoop()
	m := &Manager{Agents: hub}
	ctx := context.Background()
	out, err := m.ExecRun(ctx, "nod_remote", "keel-web-a", node.ExecRunRequest{Cmd: []string{"psql", "-f", "-"}, Env: []string{"PGPASSWORD=pw"}, Stdin: []byte("SELECT 1")}, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.Stdout) != "out:SELECT 1" || string(out.Stderr) != "e" || out.ExitCode != 2 {
		t.Errorf("result: %+v", out)
	}
	d.mu.Lock()
	got := strings.Join(d.cmd, " ")
	d.mu.Unlock()
	if got != "keel-web-a psql -f - PGPASSWORD=pw" {
		t.Errorf("request: %s", got)
	}
	// 워크로드가 아닌 컨테이너는 거부
	for _, name := range []string{"keel-tcp-a", "keel-other-a", "keel-missing"} {
		if _, err := m.ExecRun(ctx, "nod_remote", name, node.ExecRunRequest{Cmd: []string{"sh"}}, time.Second); err == nil {
			t.Errorf("%s: allowed", name)
		}
	}
	// 엄격한 검사: 잘못된 env, 범위 밖 값, 모르는 필드
	bad := []string{
		`{"name":"keel-web-a","cmd":["sh"],"env":["NOEQ"]}`,
		`{"name":"keel-web-a","cmd":["sh"],"timeout_s":999}`,
		`{"name":"keel-web-a","cmd":["sh"],"max_output":99999999}`,
		`{"name":"keel-web-a","cmd":[]}`,
		`{"name":"keel-web-a","cmd":["sh"],"privileged":true}`,
	}
	for _, b := range bad {
		if _, err := hub.h.Handle(ctx, agentprotocol.OpExecRun, []byte(b), func(any) {}); err == nil {
			t.Errorf("accepted %s", b)
		}
	}
	ok, _ := json.Marshal(agentprotocol.ExecRunRequest{Name: "keel-web-a", Cmd: []string{"sh"}, TimeoutS: 10})
	if _, err := hub.h.Handle(ctx, agentprotocol.OpExecRun, ok, func(any) {}); err != nil {
		t.Errorf("valid request rejected: %v", err)
	}
}

// oldAgentHub는 exec.run을 모르는 옛 agent처럼 거부한다.
type oldAgentHub struct{}

func (oldAgentHub) Online(string) bool                                         { return true }
func (oldAgentHub) Reply(context.Context, string, agentprotocol.Message) error { return nil }
func (oldAgentHub) CallStream(_ context.Context, _ string, op string, _, _ any, _ func(json.RawMessage)) error {
	return &agentprotocol.RemoteError{Msg: `operation "` + op + `" is not allowed`}
}

func TestRemoteExecRunOldAgent(t *testing.T) {
	m := &Manager{Agents: oldAgentHub{}}
	_, err := m.ExecRun(context.Background(), "nod_remote", "keel-web-a", node.ExecRunRequest{Cmd: []string{"sh"}}, time.Second)
	if !errors.Is(err, ErrAgentTooOld) {
		t.Errorf("err = %v", err)
	}
}
