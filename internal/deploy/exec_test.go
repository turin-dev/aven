package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/agent"
	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// loopHub는 hub 대신 agent.Handler를 바로 부른다(서버 ↔ agent exec 프로토콜 전체를 메모리에서 돌린다).
type loopHub struct{ h *agent.Handler }

func (l *loopHub) Online(string) bool                                         { return true }
func (l *loopHub) Reply(context.Context, string, agentprotocol.Message) error { return nil }
func (l *loopHub) CallStream(ctx context.Context, _ string, op string, req, res any, onChunk func(json.RawMessage)) error {
	b, _ := json.Marshal(req)
	out, err := l.h.Handle(ctx, op, b, func(v any) {
		if onChunk != nil {
			c, _ := json.Marshal(v)
			onChunk(c)
		}
	})
	if err != nil {
		return &agentprotocol.RemoteError{Msg: err.Error()}
	}
	if res != nil && out != nil {
		return setResult(res, out)
	}
	return nil
}

type echoSess struct {
	pr      *io.PipeReader
	pw      *io.PipeWriter
	mu      sync.Mutex
	resizes []int
}

func (e *echoSess) Read(p []byte) (int, error) { return e.pr.Read(p) }
func (e *echoSess) Write(p []byte) (int, error) {
	if bytes.Equal(p, []byte("exit\r")) {
		e.pw.Close()
		return len(p), nil
	}
	return e.pw.Write(p)
}
func (e *echoSess) Close() error { return e.pw.Close() }
func (e *echoSess) Resize(_ context.Context, c, r int) error {
	e.mu.Lock()
	e.resizes = append(e.resizes, c, r)
	e.mu.Unlock()
	return nil
}
func (e *echoSess) Wait(context.Context) (int, error) { return 7, nil }

type execDocker struct {
	agent.Docker
	states map[string]*node.ContainerState
	mu     sync.Mutex
	last   *echoSess
	cmd    []string
}

func (d *execDocker) InspectMany(_ context.Context, names []string) (map[string]*node.ContainerState, error) {
	out := map[string]*node.ContainerState{}
	for _, n := range names {
		if s := d.states[n]; s != nil {
			out[n] = s
		}
	}
	return out, nil
}

func (d *execDocker) Exec(_ context.Context, _ string, cmd []string, _, _ int) (node.ExecSession, error) {
	pr, pw := io.Pipe()
	s := &echoSess{pr: pr, pw: pw}
	d.mu.Lock()
	d.last, d.cmd = s, cmd
	d.mu.Unlock()
	return s, nil
}

func newExecLoop() (*execDocker, *loopHub) {
	d := &execDocker{states: map[string]*node.ContainerState{
		"keel-web-a":   {Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleApp}},
		"keel-tcp-a":   {Labels: map[string]string{node.LabelManaged: "true", node.LabelRole: node.RoleTCPProxy}},
		"keel-other-a": {Labels: map[string]string{node.LabelRole: node.RoleApp}},
	}}
	return d, &loopHub{h: &agent.Handler{Docker: d}}
}

func readUntil(t *testing.T, r io.Reader, want string) {
	t.Helper()
	got := ""
	buf := make([]byte, 64)
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(got, want) {
		if time.Now().After(deadline) {
			t.Fatalf("read %q, want %q", got, want)
		}
		n, err := r.Read(buf)
		got += string(buf[:n])
		if err != nil && !strings.Contains(got, want) {
			t.Fatalf("read %q: %v", got, err)
		}
	}
}

func TestRemoteExecRoundTrip(t *testing.T) {
	d, hub := newExecLoop()
	m := &Manager{Agents: hub}
	ctx := context.Background()
	sess, err := m.Exec(ctx, "nod_remote", "keel-web-a", []string{"sh", "-lc", "exec sh"}, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	sess.Write([]byte("ls\r"))
	sess.Write([]byte("pwd\r")) // 순서대로 도착해야 한다
	readUntil(t, sess, "ls\rpwd\r")
	if err := sess.Resize(ctx, 132, 43); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	es, cmd := d.last, d.cmd
	d.mu.Unlock()
	es.mu.Lock()
	if len(es.resizes) != 2 || es.resizes[0] != 132 || es.resizes[1] != 43 {
		t.Errorf("resize: %v", es.resizes)
	}
	es.mu.Unlock()
	if strings.Join(cmd, " ") != "sh -lc exec sh" {
		t.Errorf("cmd: %v", cmd)
	}
	sess.Write([]byte("exit\r"))
	if _, err := io.ReadAll(sess); err != nil {
		t.Fatal(err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if code, err := sess.Wait(wctx); err != nil || code != 7 {
		t.Errorf("wait: %d %v", code, err)
	}
	sess.Close()
}

func TestRemoteExecRejectsNonWorkloads(t *testing.T) {
	_, hub := newExecLoop()
	m := &Manager{Agents: hub}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, name := range []string{"keel-tcp-a", "keel-other-a", "keel-missing", "dokploy-traefik"} {
		sess, err := m.Exec(ctx, "nod_remote", name, []string{"sh"}, 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		if code, err := sess.Wait(ctx); err == nil || code != -1 {
			t.Errorf("%s: exec allowed (%d %v)", name, code, err)
		}
	}
	// 명령 검사
	b, _ := json.Marshal(agentprotocol.ExecStartRequest{Session: strings.Repeat("a", 24), Name: "keel-web-a", Cmd: []string{"a\x00"}})
	if _, err := hub.h.Handle(ctx, agentprotocol.OpExecStart, b, func(any) {}); err == nil {
		t.Error("NUL in cmd accepted")
	}
	b, _ = json.Marshal(agentprotocol.ExecStartRequest{Session: "bad", Name: "keel-web-a", Cmd: []string{"sh"}})
	if _, err := hub.h.Handle(ctx, agentprotocol.OpExecStart, b, func(any) {}); err == nil {
		t.Error("bad session id accepted")
	}
	b, _ = json.Marshal(agentprotocol.ExecInputRequest{Session: strings.Repeat("b", 24), Data: []byte("x")})
	if _, err := hub.h.Handle(ctx, agentprotocol.OpExecInput, b, func(any) {}); err == nil {
		t.Error("input to unknown session accepted")
	}
	// 알 수 없는 필드 거부(strict)
	if _, err := hub.h.Handle(ctx, agentprotocol.OpExecStart, []byte(`{"session":"`+strings.Repeat("a", 24)+`","name":"keel-web-a","cmd":["sh"],"privileged":true}`), func(any) {}); err == nil {
		t.Error("unknown field accepted")
	}
}

func TestRemoteExecSessionLimit(t *testing.T) {
	_, hub := newExecLoop()
	m := &Manager{Agents: hub}
	ctx := context.Background()
	var open []node.ExecSession
	for i := 0; i < agent.MaxExecSessions; i++ {
		s, err := m.Exec(ctx, "nod_remote", "keel-web-a", []string{"sh"}, 80, 24)
		if err != nil {
			t.Fatal(err)
		}
		s.Write([]byte("ping\r"))
		readUntil(t, s, "ping") // 세션이 실제로 열렸는지 확인
		open = append(open, s)
	}
	extra, _ := m.Exec(ctx, "nod_remote", "keel-web-a", []string{"sh"}, 80, 24)
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if _, err := extra.Wait(wctx); err == nil || !strings.Contains(err.Error(), "too many") {
		t.Errorf("limit: %v", err)
	}
	for _, s := range open {
		s.Close()
	}
}
