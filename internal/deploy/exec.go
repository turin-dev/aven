package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/turin-dev/aven/internal/agentprotocol"
	"github.com/turin-dev/aven/internal/node"
)

// Exec는 노드의 컨테이너에 TTY exec 세션을 연다. 로컬은 Engine API, 원격은 agent의 exec.* 작업이다.
func (m *Manager) Exec(ctx context.Context, nodeID, container string, cmd []string, cols, rows int) (node.ExecSession, error) {
	if isLocal(nodeID) {
		if m.Docker == nil {
			return nil, errors.New("docker is not configured")
		}
		return m.Docker.Exec(ctx, container, cmd, cols, rows)
	}
	if m.Agents == nil || !m.Agents.Online(nodeID) {
		return nil, ErrNodeUnavailable
	}
	return startRemoteExec(ctx, m.Agents, nodeID, container, cmd, cols, rows)
}

// remoteExecQueue는 아직 읽지 않은 출력 chunk 수 상한이다(넘치면 세션을 끊는다: hub 읽기 goroutine을 막지 않기 위해).
const remoteExecQueue = 512

var errExecOverflow = errors.New("셸 출력이 너무 빨라 세션을 닫았습니다")

type remoteExec struct {
	hub    AgentCaller
	node   string
	id     string
	cancel context.CancelFunc

	out  chan []byte
	cur  []byte
	done chan struct{} // exec.start 호출이 끝나면 닫힌다
	code int
	err  error

	inMu    sync.Mutex
	inBuf   []byte
	inWake  chan struct{}
	inErr   error
	closeMu sync.Once
}

func startRemoteExec(ctx context.Context, hub AgentCaller, nodeID, container string, cmd []string, cols, rows int) (*remoteExec, error) {
	id, err := agentprotocol.Secret()
	if err != nil {
		return nil, err
	}
	cols, rows = node.ClampTermSize(cols, rows)
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 12*time.Hour)
	x := &remoteExec{hub: hub, node: nodeID, id: id[:24], cancel: cancel, out: make(chan []byte, remoteExecQueue),
		done: make(chan struct{}), inWake: make(chan struct{}, 1), code: -1}
	req := agentprotocol.ExecStartRequest{Session: x.id, Name: container, Cmd: cmd, Cols: cols, Rows: rows}
	var mu sync.Mutex // onChunk(hub 읽기 goroutine)와 out 닫기를 직렬화한다
	overflow, finished := false, false
	go func() {
		defer close(x.done)
		var res agentprotocol.ExecResult
		err := hub.CallStream(sctx, nodeID, agentprotocol.OpExecStart, req, &res, func(b json.RawMessage) {
			var c agentprotocol.DataChunk
			if json.Unmarshal(b, &c) != nil || len(c.Data) == 0 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if overflow || finished {
				return
			}
			select {
			case x.out <- c.Data:
			default:
				overflow = true
				cancel() // agent 쪽 세션도 취소된다
			}
		})
		mu.Lock()
		finished = true
		close(x.out)
		if overflow {
			err = errExecOverflow
		}
		mu.Unlock()
		x.code, x.err = res.ExitCode, err
		if err != nil {
			x.code = -1
		}
	}()
	go x.sendLoop(sctx)
	return x, nil
}

func (x *remoteExec) Read(p []byte) (int, error) {
	for len(x.cur) == 0 {
		b, ok := <-x.out
		if !ok {
			return 0, io.EOF
		}
		x.cur = b
	}
	n := copy(p, x.cur)
	x.cur = x.cur[n:]
	return n, nil
}

// Write는 입력을 모아 두고 sendLoop가 순서대로 exec.input으로 보낸다(한 번에 하나만 보내서 순서가 섞이지 않는다).
func (x *remoteExec) Write(p []byte) (int, error) {
	x.inMu.Lock()
	defer x.inMu.Unlock()
	if x.inErr != nil {
		return 0, x.inErr
	}
	if len(x.inBuf)+len(p) > 1<<20 {
		return 0, errors.New("셸 입력이 너무 많이 밀렸습니다")
	}
	x.inBuf = append(x.inBuf, p...)
	select {
	case x.inWake <- struct{}{}:
	default:
	}
	return len(p), nil
}

func (x *remoteExec) sendLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-x.done:
			return
		case <-x.inWake:
		}
		for {
			x.inMu.Lock()
			n := min(len(x.inBuf), agentprotocol.MaxExecInputBytes)
			chunk := append([]byte(nil), x.inBuf[:n]...)
			x.inBuf = x.inBuf[n:]
			x.inMu.Unlock()
			if n == 0 {
				break
			}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := x.hub.CallStream(cctx, x.node, agentprotocol.OpExecInput, agentprotocol.ExecInputRequest{Session: x.id, Data: chunk}, nil, nil)
			cancel()
			if err != nil {
				x.inMu.Lock()
				x.inErr = err
				x.inMu.Unlock()
				return
			}
		}
	}
}

func (x *remoteExec) Resize(ctx context.Context, cols, rows int) error {
	cols, rows = node.ClampTermSize(cols, rows)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return x.hub.CallStream(ctx, x.node, agentprotocol.OpExecResize, agentprotocol.ExecResizeRequest{Session: x.id, Cols: cols, Rows: rows}, nil, nil)
}

func (x *remoteExec) Close() error {
	x.closeMu.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		x.hub.CallStream(ctx, x.node, agentprotocol.OpExecClose, agentprotocol.ExecCloseRequest{Session: x.id}, nil, nil)
		cancel()
		// agent가 최종 응답을 보낼 틈을 조금 준 뒤 호출을 정리한다.
		go func() {
			select {
			case <-x.done:
			case <-time.After(5 * time.Second):
			}
			x.cancel()
		}()
	})
	return nil
}

func (x *remoteExec) Wait(ctx context.Context) (int, error) {
	select {
	case <-x.done:
		return x.code, x.err
	case <-ctx.Done():
		return -1, ctx.Err()
	}
}
