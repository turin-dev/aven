package node

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// ExecSession은 컨테이너 안에서 TTY로 실행 중인 명령 하나다.
// Read는 TTY 출력(stdout+stderr), Write는 stdin이다. Close는 연결을 끊는다(프로세스는 TTY 입력이 끊겨 끝난다).
type ExecSession interface {
	io.ReadWriteCloser
	Resize(ctx context.Context, cols, rows int) error
	// Wait는 출력이 끝난 뒤 종료 코드를 돌려준다(알 수 없으면 -1).
	Wait(ctx context.Context) (int, error)
}

// Execer는 컨테이너에 TTY exec 세션을 연다.
type Execer interface {
	Exec(ctx context.Context, container string, cmd []string, cols, rows int) (ExecSession, error)
}

// ExecRoles는 exec를 허용하는 컨테이너 역할이다(워크로드만; proxy/tcpproxy/helper 제외).
var ExecRoles = map[string]bool{RoleApp: true, RoleCron: true, RoleSandbox: true}

// CheckExecTarget은 exec 대상 컨테이너의 라벨을 검사한다.
func CheckExecTarget(name string, labels map[string]string) error {
	if labels[LabelManaged] != "true" || !ExecRoles[labels[LabelRole]] {
		return fmt.Errorf("container %s is not a keel-managed workload", name)
	}
	return nil
}

// ClampTermSize는 터미널 크기를 1..1000으로 맞춘다(0이면 80x24).
func ClampTermSize(cols, rows int) (int, int) {
	if cols <= 0 {
		cols = 80
	}
	if rows <= 0 {
		rows = 24
	}
	return min(cols, 1000), min(rows, 1000)
}

// DockerSocket은 Engine API unix socket 경로다. DOCKER_HOST가 unix://면 그것을 쓰고, 다른 scheme이면 오류다.
func DockerSocket() (string, error) {
	h := os.Getenv("DOCKER_HOST")
	if h == "" {
		return "/var/run/docker.sock", nil
	}
	if p, ok := strings.CutPrefix(h, "unix://"); ok && p != "" {
		return p, nil
	}
	return "", errors.New("exec는 unix socket DOCKER_HOST만 지원합니다")
}

// engine은 docker Engine API를 unix socket으로 부르는 최소 클라이언트다(docker CLI에는 pty가 필요해서 exec만 이것으로 한다).
type engine struct {
	sock string
}

func newEngine() (*engine, error) {
	s, err := DockerSocket()
	if err != nil {
		return nil, err
	}
	return &engine{sock: s}, nil
}

func (e *engine) dial(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "unix", e.sock)
}

func (e *engine) client() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) { return e.dial(ctx) },
	}}
}

// do는 JSON 요청을 보내고 2xx면 out에 디코드한다.
func (e *engine) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		b, _ := json.Marshal(in)
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://docker"+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == 404 {
		return ErrNoContainer
	}
	if resp.StatusCode >= 300 {
		var m struct {
			Message string `json:"message"`
		}
		json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&m)
		return fmt.Errorf("docker %s %s: %d %s", method, path, resp.StatusCode, m.Message)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// Exec는 Engine API로 TTY exec를 만들고 hijack한 연결을 돌려준다.
// aven이 만든 워크로드 컨테이너(keel.managed=true, role app/cron)만 허용한다.
func (d *Docker) Exec(ctx context.Context, container string, cmd []string, cols, rows int) (ExecSession, error) {
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
	cols, rows = ClampTermSize(cols, rows)
	var created struct {
		ID string `json:"Id"`
	}
	err = e.do(ctx, "POST", "/containers/"+url.PathEscape(container)+"/exec", map[string]any{
		"AttachStdin": true, "AttachStdout": true, "AttachStderr": true, "Tty": true,
		"Cmd": cmd, "Env": []string{"TERM=xterm-256color"}, "ConsoleSize": []int{rows, cols},
	}, &created)
	if err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, errors.New("docker exec: no exec id")
	}
	conn, err := e.dial(ctx)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]any{"Detach": false, "Tty": true, "ConsoleSize": []int{rows, cols}})
	req, _ := http.NewRequest("POST", "http://docker/exec/"+url.PathEscape(created.ID)+"/start", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	// 101(upgrade) 또는 옛 엔진의 200(raw stream). 그 밖은 오류 본문이다.
	if resp.StatusCode != http.StatusSwitchingProtocols && resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		conn.Close()
		return nil, fmt.Errorf("docker exec start: %d %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	conn.SetDeadline(time.Time{})
	return &engineExec{e: e, id: created.ID, conn: conn, r: br}, nil
}

var _ Execer = (*Docker)(nil)

type engineExec struct {
	e    *engine
	id   string
	conn net.Conn
	r    *bufio.Reader
	once sync.Once
}

func (x *engineExec) Read(p []byte) (int, error)  { return x.r.Read(p) }
func (x *engineExec) Write(p []byte) (int, error) { return x.conn.Write(p) }

func (x *engineExec) Close() error {
	var err error
	x.once.Do(func() {
		if cw, ok := x.conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite() // stdin EOF를 먼저 알린다
		}
		err = x.conn.Close()
	})
	return err
}

func (x *engineExec) Resize(ctx context.Context, cols, rows int) error {
	cols, rows = ClampTermSize(cols, rows)
	return x.e.do(ctx, "POST", fmt.Sprintf("/exec/%s/resize?h=%d&w=%d", url.PathEscape(x.id), rows, cols), nil, nil)
}

func (x *engineExec) Wait(ctx context.Context) (int, error) {
	for i := 0; i < 50; i++ {
		var st struct {
			Running  bool `json:"Running"`
			ExitCode int  `json:"ExitCode"`
		}
		if err := x.e.do(ctx, "GET", "/exec/"+url.PathEscape(x.id)+"/json", nil, &st); err != nil {
			return -1, err
		}
		if !st.Running {
			return st.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return -1, nil
}
