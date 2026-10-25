package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// ---- service shell (exec) ----
//
// GET /api/services/{sid}/instances/{eid}/exec?replica=&cmd=&cols=&rows= (WebSocket)
// binary frame = stdin/stdout 바이트, text frame = JSON 제어 메시지:
//   client → server {type:"resize", cols, rows}
//   server → client {type:"exit", code} | {type:"error", message}
// 입력한 명령은 기록하지 않는다(이벤트에는 누가 어느 replica에서 열었는지만 남는다).

const (
	maxExecPerUser = 8
	execIdleLimit  = 30 * time.Minute
	execMaxFrame   = 64 << 10
	execMaxCmd     = 4096
)

// DefaultShell은 cmd를 주지 않았을 때 실행하는 명령이다(bash가 있으면 bash, 없으면 sh).
var DefaultShell = []string{"sh", "-lc", "command -v bash >/dev/null && exec bash || exec sh"}

// execer는 노드의 컨테이너에 exec 세션을 연다(deploy.Manager, 테스트에서는 가짜).
type execer interface {
	Exec(ctx context.Context, nodeID, container string, cmd []string, cols, rows int) (node.ExecSession, error)
}

type execLimiter struct {
	mu sync.Mutex
	n  map[string]int
}

func (l *execLimiter) acquire(user string, limit int) (func(), bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.n == nil {
		l.n = map[string]int{}
	}
	if l.n[user] >= limit {
		return nil, false
	}
	l.n[user]++
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			l.n[user]--
			l.mu.Unlock()
		})
	}, true
}

func (s *Server) execBackend() execer {
	if s.execImpl != nil {
		return s.execImpl
	}
	return s.Deploy
}

// wsSameOrigin은 WebSocket upgrade(GET)의 CSRF 검사다. requireAuth는 GET에 origin 검사를 하지 않고,
// 브라우저는 WebSocket에 Sec-Fetch-Site를 믿을 만하게 붙이지 않지만 Origin은 항상 붙인다.
// 그래서 Origin이 있으면 Host와 같아야 하고, Sec-Fetch-Site가 cross-site/same-site면 거부한다. 둘 다 없으면 브라우저가 아니다(CLI).
func wsSameOrigin(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return false
	}
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	u, err := url.Parse(o)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return false
	}
	return strings.EqualFold(u.Host, r.Host)
}

// ExecCommand는 cmd 쿼리를 실행할 argv로 바꾼다. 비면 DefaultShell, 있으면 sh -lc <cmd>.
func ExecCommand(cmd string) ([]string, error) {
	if strings.TrimSpace(cmd) == "" {
		return append([]string(nil), DefaultShell...), nil
	}
	if len(cmd) > execMaxCmd || strings.ContainsRune(cmd, 0) {
		return nil, errors.New("cmd가 너무 길거나 허용되지 않는 문자가 있습니다")
	}
	return []string{"sh", "-lc", cmd}, nil
}

// execControl은 client → server 제어 메시지다.
type execControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

// ParseExecControl은 text frame을 검사한다. 지금은 resize만 있다.
func ParseExecControl(b []byte) (execControl, error) {
	var c execControl
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	if c.Type != "resize" {
		return c, fmt.Errorf("unknown control %q", c.Type)
	}
	if c.Cols < 1 || c.Cols > 1000 || c.Rows < 1 || c.Rows > 1000 {
		return c, errors.New("cols/rows must be 1..1000")
	}
	return c, nil
}

// ExecExitFrame은 server → client 종료 메시지다.
func ExecExitFrame(code int) []byte {
	b, _ := json.Marshal(map[string]any{"type": "exit", "code": code})
	return b
}

func execErrorFrame(msg string) []byte {
	b, _ := json.Marshal(map[string]any{"type": "error", "message": msg})
	return b
}

func atoiDefault(s string, def int) int {
	if n, err := strconv.Atoi(s); err == nil {
		return n
	}
	return def
}

// execUser는 동시 셸 제한 키(사용자 id)와 감사 기록용 사용자 표시다.
func (s *Server) execUser(r *http.Request) (key, who string) {
	p := principalFrom(r)
	if p == nil { // 핸들러를 직접 부르는 테스트
		return "admin", "admin"
	}
	return p.User.ID, p.display()
}

// execTarget은 인스턴스의 현재 배포와 replica(컨테이너 이름)를 고른다. replica가 비면 첫 번째다.
func (s *Server) execTarget(ctx context.Context, in *store.Instance, replica string) (*store.Deployment, string, int, string) {
	if in.CurrentDeploymentID == nil {
		return nil, "", 409, "실행 중인 배포가 없습니다"
	}
	d, err := s.Store.GetDeployment(ctx, *in.CurrentDeploymentID)
	if err != nil {
		return nil, "", 409, "실행 중인 배포가 없습니다"
	}
	if d.Status.Terminal() && d.Status != store.DepLive && d.Status != store.DepCrashed {
		return nil, "", 409, "실행 중인 배포가 없습니다"
	}
	all := d.AllContainers()
	if len(all) == 0 {
		return nil, "", 409, "실행 중인 replica가 없습니다(cron 서비스는 실행 중일 때만 셸을 열 수 있습니다)"
	}
	if replica == "" {
		return d, all[0], 0, ""
	}
	for _, c := range all {
		if c == replica {
			return d, c, 0, ""
		}
	}
	return nil, "", 404, "replica를 찾을 수 없습니다"
}

func (s *Server) execShell(w http.ResponseWriter, r *http.Request) {
	if !wsSameOrigin(r) {
		writeErr(w, 403, "cross-origin request rejected")
		return
	}
	c, ok := s.loadInstance(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	cmd, err := ExecCommand(q.Get("cmd"))
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	cols, rows := node.ClampTermSize(atoiDefault(q.Get("cols"), 80), atoiDefault(q.Get("rows"), 24))
	d, replica, code, msg := s.execTarget(r.Context(), c.in, q.Get("replica"))
	if d == nil {
		writeErr(w, code, msg)
		return
	}
	key, who := s.execUser(r)
	release, ok := s.execs.acquire(key, maxExecPerUser)
	if !ok {
		writeErr(w, 429, fmt.Sprintf("동시에 열 수 있는 셸은 %d개까지입니다", maxExecPerUser))
		return
	}
	defer release()
	// Accept는 Origin이 있으면 Host와 같은지 한 번 더 확인한다(OriginPatterns 없음).
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(execMaxFrame)
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	sess, err := s.execBackend().Exec(ctx, d.NodeOf(replica), replica, cmd, cols, rows)
	if err != nil {
		s.Log.Warn("exec start", "replica", replica, "err", err)
		conn.Write(ctx, websocket.MessageText, execErrorFrame("셸을 열지 못했습니다: "+err.Error()))
		conn.Close(websocket.StatusInternalError, "exec failed")
		return
	}
	defer sess.Close()
	s.Events.Emit(ctx, c.p.ID, &c.env.ID, &c.svc.ID, "service.exec",
		fmt.Sprintf("%s (%s): %s이 %s replica에서 셸을 열었습니다", c.svc.Name, c.env.Name, who, replica))
	idle := s.execIdle
	if idle <= 0 {
		idle = execIdleLimit
	}
	execPump(ctx, conn, sess, idle)
}

// execPump는 세션과 WebSocket을 잇는다. 세션 출력이 끝나면 exit 메시지를 보내고, 클라이언트가 끊거나 idle 동안
// 양쪽 모두 조용하면 세션을 닫는다.
func execPump(ctx context.Context, conn *websocket.Conn, sess node.ExecSession, idle time.Duration) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var last atomic.Int64
	touch := func() { last.Store(time.Now().UnixNano()) }
	touch()

	go func() { // idle 감시
		tick := min(idle/4, time.Minute)
		t := time.NewTicker(max(tick, 10*time.Millisecond))
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if time.Since(time.Unix(0, last.Load())) > idle {
					conn.Write(ctx, websocket.MessageText, execErrorFrame("유휴 시간이 지나 셸을 닫았습니다"))
					conn.Close(websocket.StatusNormalClosure, "idle timeout")
					cancel()
					return
				}
			}
		}
	}()

	go func() { // 출력 → client
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := sess.Read(buf)
			if n > 0 {
				touch()
				if werr := conn.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				break
			}
		}
		if ctx.Err() != nil {
			return
		}
		wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
		code, err := sess.Wait(wctx)
		wcancel()
		if err != nil {
			conn.Write(ctx, websocket.MessageText, execErrorFrame(err.Error()))
		}
		conn.Write(ctx, websocket.MessageText, ExecExitFrame(code))
		conn.Close(websocket.StatusNormalClosure, "exited")
	}()

	for { // client → 입력/제어
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		touch()
		switch typ {
		case websocket.MessageBinary:
			if _, err := sess.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			ctl, err := ParseExecControl(data)
			if err != nil {
				continue
			}
			rctx, rc := context.WithTimeout(ctx, 10*time.Second)
			sess.Resize(rctx, ctl.Cols, ctl.Rows)
			rc()
		}
	}
}
