package agent

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// MaxExecSessions는 agent 하나에서 동시에 열 수 있는 exec 세션 수다(MaxConcurrent 슬롯을 다 쓰지 않게).
const MaxExecSessions = 8

var execSessionRe = regexp.MustCompile(`^[a-f0-9]{24}$`)

type execSessions struct {
	mu sync.Mutex
	m  map[string]*execEntry
}

// execEntry는 세션 자리다. ready가 닫히면 sess가 정해진다(열기에 실패하면 nil).
// 서버는 exec.start 응답을 기다리지 않고 input을 보내므로, 세션이 열리는 중이면 잠깐 기다린다.
type execEntry struct {
	ready chan struct{}
	sess  node.ExecSession
}

func (e *execSessions) get(ctx context.Context, id string) node.ExecSession {
	// exec.start와 exec.input은 agent에서 서로 다른 goroutine으로 처리되므로 자리가 아직 없을 수도 있다.
	var ent *execEntry
	for i := 0; ent == nil; i++ {
		e.mu.Lock()
		ent = e.m[id]
		e.mu.Unlock()
		if ent != nil {
			break
		}
		if i >= 100 || ctx.Err() != nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case <-ent.ready:
		return ent.sess
	case <-ctx.Done():
		return nil
	case <-time.After(15 * time.Second):
		return nil
	}
}

// ValidateExecCmd는 exec 명령을 검사한다(인자 수/길이, NUL 금지).
func ValidateExecCmd(cmd []string) error {
	if len(cmd) == 0 || len(cmd) > 64 {
		return errors.New("exec command must have 1..64 arguments")
	}
	total := 0
	for _, c := range cmd {
		if strings.ContainsRune(c, 0) {
			return errors.New("exec command is not allowed")
		}
		total += len(c)
	}
	if total > 8192 {
		return errors.New("exec command is too long")
	}
	return nil
}

func (h *Handler) execer() (node.Execer, error) {
	if x, ok := h.Docker.(node.Execer); ok {
		return x, nil
	}
	return nil, errors.New("exec is not supported by this agent")
}

// execStart는 세션을 열고, 출력이 끝날 때까지 DataChunk로 보낸 뒤 종료 코드를 돌려준다.
// ctx가 끝나면(서버 취소/연결 끊김/exec.close) 세션을 닫는다.
func (h *Handler) execStart(ctx context.Context, r agentprotocol.ExecStartRequest, stream func(any)) (any, error) {
	if !execSessionRe.MatchString(r.Session) {
		return nil, errors.New("invalid exec session")
	}
	if err := ValidateExecCmd(r.Cmd); err != nil {
		return nil, err
	}
	if !containerRe.MatchString(r.Name) {
		return nil, fmt.Errorf("container name %q is not allowed", r.Name)
	}
	st, err := h.Docker.InspectMany(ctx, []string{r.Name})
	if err != nil {
		return nil, err
	}
	c := st[r.Name]
	if c == nil {
		return nil, node.ErrNoContainer
	}
	if err := node.CheckExecTarget(r.Name, c.Labels); err != nil {
		return nil, err
	}
	x, err := h.execer()
	if err != nil {
		return nil, err
	}
	ent := &execEntry{ready: make(chan struct{})}
	h.execs.mu.Lock()
	if h.execs.m == nil {
		h.execs.m = map[string]*execEntry{}
	}
	if _, dup := h.execs.m[r.Session]; dup {
		h.execs.mu.Unlock()
		return nil, errors.New("exec session already exists")
	}
	if len(h.execs.m) >= MaxExecSessions {
		h.execs.mu.Unlock()
		return nil, errors.New("too many exec sessions on this node")
	}
	h.execs.m[r.Session] = ent // 자리 예약
	h.execs.mu.Unlock()
	defer func() {
		h.execs.mu.Lock()
		delete(h.execs.m, r.Session)
		h.execs.mu.Unlock()
	}()
	cols, rows := node.ClampTermSize(r.Cols, r.Rows)
	sess, err := x.Exec(ctx, r.Name, r.Cmd, cols, rows)
	if err != nil {
		close(ent.ready)
		return nil, err
	}
	ent.sess = sess
	close(ent.ready)
	stop := context.AfterFunc(ctx, func() { sess.Close() })
	defer stop()
	defer sess.Close()
	w := &chunkWriter{ctx: ctx, stream: stream}
	buf := make([]byte, 32<<10)
	for {
		n, rerr := sess.Read(buf)
		if n > 0 {
			if _, err := w.Write(buf[:n]); err != nil {
				return nil, err
			}
		}
		if rerr != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			break // EOF 또는 exec.close로 닫힘
		}
	}
	code, err := sess.Wait(context.WithoutCancel(ctx))
	if err != nil {
		code = -1
	}
	return agentprotocol.ExecResult{ExitCode: code}, nil
}

// execRun은 TTY 없는 일회성 exec를 실행하고 출력을 모아 돌려준다(stdout 상한, 실행 시간 제한).
func (h *Handler) execRun(ctx context.Context, r agentprotocol.ExecRunRequest) (any, error) {
	if err := ValidateExecCmd(r.Cmd); err != nil {
		return nil, err
	}
	if err := node.ValidateExecEnv(r.Env); err != nil {
		return nil, err
	}
	if len(r.Stdin) > node.MaxExecRunStdin {
		return nil, errors.New("exec stdin is too large")
	}
	if r.MaxOutput < 0 || r.MaxOutput > node.MaxExecRunOutput {
		return nil, errors.New("max_output is out of range")
	}
	if r.TimeoutS < 0 || r.TimeoutS > agentprotocol.MaxExecRunTimeoutS {
		return nil, errors.New("timeout_s is out of range")
	}
	if !containerRe.MatchString(r.Name) {
		return nil, fmt.Errorf("container name %q is not allowed", r.Name)
	}
	st, err := h.Docker.InspectMany(ctx, []string{r.Name})
	if err != nil {
		return nil, err
	}
	c := st[r.Name]
	if c == nil {
		return nil, node.ErrNoContainer
	}
	if err := node.CheckExecTarget(r.Name, c.Labels); err != nil {
		return nil, err
	}
	x, ok := h.Docker.(node.ExecRunner)
	if !ok {
		return nil, errors.New("exec is not supported by this agent")
	}
	timeout := time.Duration(r.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	out, err := x.ExecRun(ctx, r.Name, node.ExecRunRequest{Cmd: r.Cmd, Env: r.Env, Stdin: r.Stdin, MaxOutput: r.MaxOutput})
	if err != nil {
		return nil, err
	}
	return *out, nil
}

func (h *Handler) execInput(ctx context.Context, r agentprotocol.ExecInputRequest) error {
	if !execSessionRe.MatchString(r.Session) {
		return errors.New("invalid exec session")
	}
	if len(r.Data) > agentprotocol.MaxExecInputBytes {
		return errors.New("exec input is too large")
	}
	s := h.execs.get(ctx, r.Session)
	if s == nil {
		return errors.New("no such exec session")
	}
	_, err := s.Write(r.Data)
	return err
}

func (h *Handler) execResize(ctx context.Context, r agentprotocol.ExecResizeRequest) error {
	if !execSessionRe.MatchString(r.Session) {
		return errors.New("invalid exec session")
	}
	s := h.execs.get(ctx, r.Session)
	if s == nil {
		return errors.New("no such exec session")
	}
	return s.Resize(ctx, r.Cols, r.Rows)
}

func (h *Handler) execClose(ctx context.Context, r agentprotocol.ExecCloseRequest) error {
	if !execSessionRe.MatchString(r.Session) {
		return errors.New("invalid exec session")
	}
	if s := h.execs.get(ctx, r.Session); s != nil {
		s.Close() // execStart의 읽기 루프가 끝나며 최종 응답을 보낸다
	}
	return nil
}
