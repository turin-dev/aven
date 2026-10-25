package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// 일회성(non-TTY) exec: 명령을 한 번 실행하고 stdout/stderr를 상한까지 모은다(DB 데이터 브라우저 등).
// 비밀번호 같은 값은 Env로 넘긴다(Cmd에 넣지 않는다).

const (
	// MaxExecRunOutput은 stdout 상한의 최댓값이다.
	MaxExecRunOutput = 2 << 20
	// MaxExecRunStderr는 stderr 상한이다.
	MaxExecRunStderr = 64 << 10
	// MaxExecRunStdin은 stdin으로 넣을 수 있는 최대 크기다.
	MaxExecRunStdin = 256 << 10
	maxExecEnv      = 32
	maxExecEnvBytes = 64 << 10
)

// ExecRunRequest는 TTY 없이 한 번 실행할 명령이다.
type ExecRunRequest struct {
	Cmd   []string
	Env   []string // KEY=VALUE, 컨테이너 환경 위에 덮는다
	Stdin []byte
	// MaxOutput은 stdout 상한(바이트, 0이면 1MiB)이다. 넘치면 거기서 끊고 Truncated=true.
	MaxOutput int
}

// ExecOutput은 일회성 exec의 결과다. ExitCode는 알 수 없으면 -1(출력이 잘려 기다리지 않은 경우 포함).
type ExecOutput struct {
	Stdout    []byte `json:"stdout"`
	Stderr    []byte `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Truncated bool   `json:"truncated"`
}

// ExecRunner는 컨테이너에서 일회성 명령을 실행한다.
type ExecRunner interface {
	ExecRun(ctx context.Context, container string, req ExecRunRequest) (*ExecOutput, error)
}

var execEnvKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,127}$`)

// ValidateExecEnv는 exec 환경변수를 검사한다(KEY=VALUE 형식, 개수/크기, NUL 금지).
func ValidateExecEnv(env []string) error {
	if len(env) > maxExecEnv {
		return fmt.Errorf("exec env must have at most %d entries", maxExecEnv)
	}
	total := 0
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok || !execEnvKeyRe.MatchString(k) || strings.ContainsRune(kv, 0) {
			return errors.New("exec env must be KEY=VALUE")
		}
		total += len(kv)
	}
	if total > maxExecEnvBytes {
		return errors.New("exec env is too large")
	}
	return nil
}

// ClampExecOutput은 stdout 상한을 1..MaxExecRunOutput으로 맞춘다(0이면 1MiB).
func ClampExecOutput(n int) int {
	if n <= 0 {
		return 1 << 20
	}
	return min(n, MaxExecRunOutput)
}

// DemuxExec는 docker의 multiplexed stream(8바이트 헤더 + payload)을 stdout/stderr로 나눈다.
// stdout이 maxOut을 넘으면 거기서 멈추고 truncated=true를 돌려준다(나머지는 읽지 않는다). stderr는 maxErr까지만 남긴다.
func DemuxExec(r io.Reader, maxOut, maxErr int) (stdout, stderr []byte, truncated bool, err error) {
	var out, errb bytes.Buffer
	hdr := make([]byte, 8)
	for {
		if _, err := io.ReadFull(r, hdr); err != nil {
			if err == io.EOF {
				return out.Bytes(), errb.Bytes(), false, nil
			}
			return out.Bytes(), errb.Bytes(), false, err
		}
		n := int64(binary.BigEndian.Uint32(hdr[4:]))
		switch hdr[0] {
		case 1:
			room := int64(maxOut - out.Len())
			if n > room {
				io.CopyN(&out, r, room)
				return out.Bytes(), errb.Bytes(), true, nil
			}
			if _, err := io.CopyN(&out, r, n); err != nil {
				return out.Bytes(), errb.Bytes(), false, err
			}
		case 2:
			room := max(int64(maxErr-errb.Len()), 0)
			keep := min(n, room)
			if _, err := io.CopyN(&errb, r, keep); err != nil {
				return out.Bytes(), errb.Bytes(), false, err
			}
			if _, err := io.CopyN(io.Discard, r, n-keep); err != nil {
				return out.Bytes(), errb.Bytes(), false, err
			}
		default:
			if _, err := io.CopyN(io.Discard, r, n); err != nil {
				return out.Bytes(), errb.Bytes(), false, err
			}
		}
	}
}

// ExecRun은 Engine API로 non-TTY exec를 실행하고 출력을 모은다. Exec와 같은 대상 검사를 한다.
// ctx의 deadline이 실행 시간 제한이다.
func (d *Docker) ExecRun(ctx context.Context, container string, req ExecRunRequest) (*ExecOutput, error) {
	if err := ValidateExecEnv(req.Env); err != nil {
		return nil, err
	}
	if len(req.Stdin) > MaxExecRunStdin {
		return nil, errors.New("exec stdin is too large")
	}
	e, err := newEngine()
	if err != nil {
		return nil, err
	}
	var info struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"Config"`
		State struct {
			Running bool `json:"Running"`
		} `json:"State"`
	}
	if err := e.do(ctx, "GET", "/containers/"+url.PathEscape(container)+"/json", nil, &info); err != nil {
		return nil, err
	}
	if err := CheckExecTarget(container, info.Config.Labels); err != nil {
		return nil, err
	}
	if !info.State.Running {
		return nil, fmt.Errorf("container %s is not running", container)
	}
	env := req.Env
	if env == nil {
		env = []string{}
	}
	var created struct {
		ID string `json:"Id"`
	}
	err = e.do(ctx, "POST", "/containers/"+url.PathEscape(container)+"/exec", map[string]any{
		"AttachStdin": len(req.Stdin) > 0, "AttachStdout": true, "AttachStderr": true, "Tty": false,
		"Cmd": req.Cmd, "Env": env,
	}, &created)
	if err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("docker exec: no exec id")
	}
	conn, br, err := e.hijack(ctx, created.ID)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if len(req.Stdin) > 0 {
		go func() {
			conn.Write(req.Stdin)
			if cw, ok := conn.(interface{ CloseWrite() error }); ok {
				cw.CloseWrite()
			}
		}()
	} else if cw, ok := conn.(interface{ CloseWrite() error }); ok {
		cw.CloseWrite()
	}
	stdout, stderr, truncated, rerr := DemuxExec(br, ClampExecOutput(req.MaxOutput), MaxExecRunStderr)
	if execTimedOut(ctx, rerr) {
		return nil, fmt.Errorf("exec timed out: %w", context.DeadlineExceeded)
	}
	if rerr != nil && !truncated {
		return nil, rerr
	}
	res := &ExecOutput{Stdout: stdout, Stderr: stderr, ExitCode: -1, Truncated: truncated}
	if !truncated {
		res.ExitCode = e.execExitCode(ctx, created.ID)
	}
	return res, nil
}

// execTimedOut은 실행 시간 제한에 걸렸는지 알려준다. 연결에도 같은 deadline을 걸어 두므로(hijack) 읽기가
// ctx보다 먼저 "i/o timeout"으로 끝날 수 있다. 그때 ctx.Err()는 아직 nil일 수 있다.
func execTimedOut(ctx context.Context, rerr error) bool {
	if ctx.Err() != nil || errors.Is(rerr, os.ErrDeadlineExceeded) {
		return true
	}
	var ne net.Error
	return errors.As(rerr, &ne) && ne.Timeout()
}

var _ ExecRunner = (*Docker)(nil)

// hijack은 exec/start를 non-TTY로 열고 연결을 넘긴다.
func (e *engine) hijack(ctx context.Context, id string) (net.Conn, *bufio.Reader, error) {
	conn, err := e.dial(ctx)
	if err != nil {
		return nil, nil, err
	}
	body, _ := json.Marshal(map[string]any{"Detach": false, "Tty": false})
	req, _ := http.NewRequest("POST", "http://docker/exec/"+url.PathEscape(id)+"/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		conn.Close()
		return nil, nil, fmt.Errorf("docker exec start: %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return conn, br, nil
}

// execExitCode는 끝난 exec의 종료 코드다(잠깐 기다려도 끝나지 않으면 -1).
func (e *engine) execExitCode(ctx context.Context, id string) int {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for i := 0; i < 50; i++ {
		var st struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if err := e.do(ctx, "GET", "/exec/"+url.PathEscape(id)+"/json", nil, &st); err != nil {
			return -1
		}
		if !st.Running {
			return st.ExitCode
		}
		select {
		case <-ctx.Done():
			return -1
		case <-time.After(100 * time.Millisecond):
		}
	}
	return -1
}
