package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/turin-dev/aven/internal/node"
	"github.com/turin-dev/aven/internal/store"
)

// fakeSess는 입력을 그대로 출력으로 돌려준다. "exit\r"을 받으면 종료 코드 3으로 끝난다.
type fakeSess struct {
	pr      *io.PipeReader
	pw      *io.PipeWriter
	mu      sync.Mutex
	resizes [][2]int
	closed  bool
}

func newFakeSess() *fakeSess {
	pr, pw := io.Pipe()
	return &fakeSess{pr: pr, pw: pw}
}

func (f *fakeSess) Read(p []byte) (int, error) { return f.pr.Read(p) }
func (f *fakeSess) Write(p []byte) (int, error) {
	if bytes.Contains(p, []byte("exit\r")) {
		f.pw.Close()
		return len(p), nil
	}
	return f.pw.Write(p)
}
func (f *fakeSess) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return f.pw.Close()
}
func (f *fakeSess) Resize(_ context.Context, c, r int) error {
	f.mu.Lock()
	f.resizes = append(f.resizes, [2]int{c, r})
	f.mu.Unlock()
	return nil
}
func (f *fakeSess) Wait(context.Context) (int, error) { return 3, nil }

type fakeExecer struct {
	mu    sync.Mutex
	calls []string
	sess  []*fakeSess
}

func (f *fakeExecer) Exec(_ context.Context, nodeID, container string, cmd []string, cols, rows int) (node.ExecSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"node": nodeID, "c": container, "cmd": cmd, "cols": cols, "rows": rows})
	f.calls = append(f.calls, string(b))
	s := newFakeSess()
	f.sess = append(f.sess, s)
	return s, nil
}

type execEnv struct {
	f    *ghFixture
	ts   *httptest.Server
	fake *fakeExecer
	url  string
}

func newExecEnv(t *testing.T) *execEnv {
	f := newGHFixture(t)
	fake := &fakeExecer{}
	f.srv.execImpl = fake
	f.srv.APIToken = "test-token"
	f.srv.SessionKey = []byte("0123456789abcdef0123456789abcdef")
	in, _ := f.st.GetInstanceFor(f.ctx, f.web.ID, f.prod.ID)
	d := &store.Deployment{ServiceID: f.web.ID, EnvironmentID: f.prod.ID, InstanceID: in.ID, Status: store.DepLive, Trigger: "manual",
		Containers: []string{"keel-web-a", "keel-web-b"}}
	if err := f.st.CreateDeployment(f.ctx, d); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetInstanceStatus(f.ctx, in.ID, "online", &d.ID); err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(f.srv.Handler())
	t.Cleanup(ts.Close)
	return &execEnv{f: f, ts: ts, fake: fake, url: "ws" + strings.TrimPrefix(ts.URL, "http") + "/api/services/" + f.web.ID + "/instances/" + f.prod.ID + "/exec"}
}

func bearer() http.Header { return http.Header{"Authorization": []string{"Bearer test-token"}} }

func TestExecShellSession(t *testing.T) {
	e := newExecEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, e.url+"?replica=keel-web-b&cols=100&rows=30", &websocket.DialOptions{HTTPHeader: bearer()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte("echo hi\r")); err != nil {
		t.Fatal(err)
	}
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageBinary || string(data) != "echo hi\r" {
		t.Fatalf("echo: %v %v %q", err, typ, data)
	}
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`))
	conn.Write(ctx, websocket.MessageText, []byte(`{"type":"resize","cols":0,"rows":40}`)) // 무시된다
	conn.Write(ctx, websocket.MessageBinary, []byte("exit\r"))
	var exit map[string]any
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no exit frame: %v", err)
		}
		if typ == websocket.MessageText {
			json.Unmarshal(data, &exit)
			break
		}
	}
	if exit["type"] != "exit" || exit["code"] != float64(3) {
		t.Errorf("exit frame: %v", exit)
	}
	e.fake.mu.Lock()
	call, sess := e.fake.calls[0], e.fake.sess[0]
	e.fake.mu.Unlock()
	if !strings.Contains(call, `"c":"keel-web-b"`) || !strings.Contains(call, `"cols":100`) || !strings.Contains(call, `"rows":30`) ||
		!strings.Contains(call, `"node":"node_local"`) || !strings.Contains(call, "exec bash") {
		t.Errorf("exec call: %s", call)
	}
	sess.mu.Lock()
	if len(sess.resizes) != 1 || sess.resizes[0] != [2]int{120, 40} {
		t.Errorf("resizes: %v", sess.resizes)
	}
	sess.mu.Unlock()
	// 감사 이벤트: 누가/어느 replica인지만 남고 입력은 남지 않는다.
	evs, _ := e.f.st.ListEvents(e.f.ctx, e.f.p.ID, "", 10)
	found := false
	for _, ev := range evs {
		if ev.Kind == "service.exec" {
			found = true
			if !strings.Contains(ev.Message, "keel-web-b") || !strings.Contains(ev.Message, "API token") || strings.Contains(ev.Message, "echo") {
				t.Errorf("event message: %s", ev.Message)
			}
		}
	}
	if !found {
		t.Error("service.exec event missing")
	}
}

func TestExecShellRejects(t *testing.T) {
	e := newExecEnv(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	status := func(url string, h http.Header) int {
		c, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: h})
		if err == nil {
			c.CloseNow()
			return 101
		}
		if resp == nil {
			t.Fatalf("%s: %v", url, err)
		}
		return resp.StatusCode
	}
	if got := status(e.url, nil); got != 401 {
		t.Errorf("no auth: %d", got)
	}
	if got := status(e.url+"?replica=keel-other", bearer()); got != 404 {
		t.Errorf("unknown replica: %d", got)
	}
	if got := status(e.url+"?cmd="+strings.Repeat("x", 5000), bearer()); got != 400 {
		t.Errorf("long cmd: %d", got)
	}
	// 쿠키 인증 + 다른 origin은 거부(CSRF/CSWSH)
	cookie := cookieName + "=" + testSessionCookie(t, e.f.srv, testUser(t, e.f.st, "owner@example.com", store.RoleOwner))
	evil := http.Header{"Cookie": []string{cookie}, "Origin": []string{"https://evil.example"}}
	if got := status(e.url, evil); got != 403 {
		t.Errorf("cross-origin cookie: %d", got)
	}
	site := http.Header{"Cookie": []string{cookie}, "Sec-Fetch-Site": []string{"same-site"}}
	if got := status(e.url, site); got != 403 {
		t.Errorf("same-site sibling: %d", got)
	}
	host := strings.TrimPrefix(e.ts.URL, "http://")
	same := http.Header{"Cookie": []string{cookie}, "Origin": []string{"http://" + host}}
	if got := status(e.url, same); got != 101 {
		t.Errorf("same-origin cookie: %d", got)
	}
	// 배포가 없으면 409
	other := "ws" + strings.TrimPrefix(e.ts.URL, "http") + "/api/services/" + e.f.other.ID + "/instances/" + e.f.prod.ID + "/exec"
	if got := status(other, bearer()); got != 409 {
		t.Errorf("no deployment: %d", got)
	}
}

func TestExecShellLimitAndIdle(t *testing.T) {
	e := newExecEnv(t)
	e.f.srv.execIdle = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, e.url, &websocket.DialOptions{HTTPHeader: bearer()})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	typ, data, err := conn.Read(ctx)
	if err != nil || typ != websocket.MessageText || !strings.Contains(string(data), `"error"`) {
		t.Fatalf("idle frame: %v %q", err, data)
	}
	if _, _, err := conn.Read(ctx); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Errorf("idle close: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	e.fake.mu.Lock()
	s := e.fake.sess[0]
	e.fake.mu.Unlock()
	s.mu.Lock()
	if !s.closed {
		t.Error("session not closed after idle timeout")
	}
	s.mu.Unlock()

	var l execLimiter
	var rels []func()
	for i := 0; i < maxExecPerUser; i++ {
		r, ok := l.acquire("admin", maxExecPerUser)
		if !ok {
			t.Fatal("limit too early")
		}
		rels = append(rels, r)
	}
	if _, ok := l.acquire("admin", maxExecPerUser); ok {
		t.Error("limit not enforced")
	}
	rels[0]()
	rels[0]() // 두 번 불러도 한 번만 반납
	if _, ok := l.acquire("admin", maxExecPerUser); !ok {
		t.Error("release did not free a slot")
	}
	if _, ok := l.acquire("admin", maxExecPerUser); ok {
		t.Error("double release freed two slots")
	}
}

func TestExecFraming(t *testing.T) {
	if c, err := ParseExecControl([]byte(`{"type":"resize","cols":80,"rows":24}`)); err != nil || c.Cols != 80 || c.Rows != 24 {
		t.Errorf("resize: %v %+v", err, c)
	}
	for _, bad := range []string{`{"type":"resize","cols":0,"rows":1}`, `{"type":"resize","cols":1,"rows":1001}`, `{"type":"kill"}`, `nope`} {
		if _, err := ParseExecControl([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
	var m map[string]any
	json.Unmarshal(ExecExitFrame(127), &m)
	if m["type"] != "exit" || m["code"] != float64(127) {
		t.Errorf("exit frame: %v", m)
	}
	if cmd, _ := ExecCommand(""); strings.Join(cmd, " ") != strings.Join(DefaultShell, " ") {
		t.Errorf("default cmd: %v", cmd)
	}
	if cmd, _ := ExecCommand("ls -la"); len(cmd) != 3 || cmd[2] != "ls -la" {
		t.Errorf("custom cmd: %v", cmd)
	}
	if _, err := ExecCommand("a\x00b"); err == nil {
		t.Error("NUL accepted")
	}
	r := httptest.NewRequest("GET", "http://aven.test/x", nil)
	if !wsSameOrigin(r) {
		t.Error("no origin (CLI) rejected")
	}
	r.Header.Set("Origin", "http://aven.test")
	if !wsSameOrigin(r) {
		t.Error("same origin rejected")
	}
	for _, o := range []string{"null", "https://evil.test", "file://", "http://aven.test.evil"} {
		r.Header.Set("Origin", o)
		if wsSameOrigin(r) {
			t.Errorf("origin %q accepted", o)
		}
	}
}
