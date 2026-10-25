package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/netip"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

func TestMatchWildcards(t *testing.T) {
	for _, tc := range []struct {
		pats []string
		kind string
		want bool
	}{
		{[]string{"deploy.*"}, "deploy.failed", true},
		{[]string{"deploy.*"}, "deploy.backup.failed", true},
		{[]string{"deploy.*"}, "deployx.failed", false},
		{[]string{"deploy.*"}, "volume.limit_exceeded", false},
		{[]string{"*"}, "alert.firing", true},
		{[]string{"*.failed"}, "backup.failed", true},
		{[]string{"*.failed"}, "backup.done", false},
		{[]string{"alert.firing"}, "alert.firing", true},
		{[]string{"alert.firing"}, "alert.resolved", false},
		{nil, "deploy.failed", false},
		{[]string{"volume.*", "deploy.failed"}, "deploy.failed", true},
	} {
		if got := Match(tc.pats, tc.kind); got != tc.want {
			t.Errorf("Match(%v, %s) = %v", tc.pats, tc.kind, got)
		}
	}
	for p, ok := range map[string]bool{"deploy.*": true, "*": true, "alert.firing": true, "": false, "a b": false, "[x": false, "Deploy.*": false, "a/b": false} {
		if ValidPattern(p) != ok {
			t.Errorf("ValidPattern(%q) != %v", p, ok)
		}
	}
}

func TestPublicAddr(t *testing.T) {
	for s, want := range map[string]bool{
		"93.184.216.34": true, "2606:4700::1111": true, "1.1.1.1": true,
		"127.0.0.1": false, "10.1.2.3": false, "172.16.0.1": false, "192.168.1.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fe80::1": false, "fd00::1": false, "::ffff:127.0.0.1": false,
		"::ffff:10.0.0.1": false, "224.0.0.1": false, "255.255.255.255": false, "64:ff9b::a00:1": false, "::": false,
	} {
		if got := PublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("PublicAddr(%s) = %v", s, got)
		}
	}
}

func TestGuardRejectsPrivateDestinations(t *testing.T) {
	ctx := context.Background()
	g := &Guard{Lookup: func(_ context.Context, host string) ([]netip.Addr, error) {
		switch host {
		case "mixed.test": // 공인 주소와 사설 주소를 함께 돌려주는 이름도 거부한다
			return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("10.0.0.5")}, nil
		case "public.test":
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
	for _, h := range []string{"127.0.0.1", "169.254.169.254", "::1", "localhost.test", "mixed.test"} {
		if _, err := g.Resolve(ctx, h); !errors.Is(err, ErrBlockedAddress) {
			t.Errorf("Resolve(%s): %v", h, err)
		}
	}
	if a, err := g.Resolve(ctx, "public.test"); err != nil || len(a) != 1 {
		t.Errorf("public: %v %v", a, err)
	}
	// 실제 연결 직전 소켓 주소 검사(Control)
	if err := g.control("tcp4", "127.0.0.1:80", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("control loopback: %v", err)
	}
	if err := g.control("tcp6", "[fd00::1]:443", nil); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("control ULA: %v", err)
	}
	if err := g.control("tcp4", "93.184.216.34:443", nil); err != nil {
		t.Errorf("control public: %v", err)
	}
	if err := (&Guard{AllowPrivate: true}).control("tcp4", "127.0.0.1:80", nil); err != nil {
		t.Errorf("allow private: %v", err)
	}
	// httptest 서버(127.0.0.1)로는 보내지 않는다.
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	res := PostJSON(ctx, (&Guard{}).Client(5*time.Second), srv.URL, []byte(`{}`), nil, []time.Duration{time.Millisecond})
	if !errors.Is(res.Err, ErrBlockedAddress) || res.Attempts != 1 || hits.Load() != 0 {
		t.Errorf("loopback post: %+v hits=%d", res, hits.Load())
	}
}

// DNS rebinding: 미리 검사할 때는 공인 주소였다가 연결할 때 루프백으로 바뀌는 이름. 연결 직전 주소 검사가 막아야 한다.
func TestGuardDNSRebinding(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	var calls atomic.Int32
	g := &Guard{Lookup: func(context.Context, string) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}}
	ctx := context.Background()
	if _, err := g.Resolve(ctx, "rebind.test"); err != nil { // 앞선 검사(등록 시점 등)는 통과
		t.Fatal(err)
	}
	res := PostJSON(ctx, g.Client(5*time.Second), "http://rebind.test:"+port+"/hook", []byte(`{}`), nil, nil)
	if !errors.Is(res.Err, ErrBlockedAddress) || hits.Load() != 0 {
		t.Errorf("rebinding not blocked: %+v hits=%d", res, hits.Load())
	}
	// 이름 풀이가 공인 주소를 돌려줘도 실제 연결 주소가 사설이면 Control이 막는다(풀이 결과와 연결 주소가 다른 경우).
	d := &net.Dialer{Control: (&Guard{}).control, Timeout: time.Second}
	if _, err := d.DialContext(ctx, "tcp", "127.0.0.1:"+port); !errors.Is(err, ErrBlockedAddress) {
		t.Errorf("dialer control: %v", err)
	}
}

func TestSignature(t *testing.T) {
	body := []byte(`{"id":"evt_1"}`)
	sig := Sign("s3cret", body)
	if !strings.HasPrefix(sig, "sha256=") || len(sig) != 7+64 {
		t.Fatalf("sig: %s", sig)
	}
	if !Verify("s3cret", body, sig) || Verify("other", body, sig) || Verify("s3cret", []byte(`{"id":"evt_2"}`), sig) {
		t.Error("verify")
	}
	if Sign("key", []byte("The quick brown fox jumps over the lazy dog")) != "sha256=f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8" {
		t.Error("HMAC-SHA256 test vector")
	}
}

func TestPostRetriesWithBackoff(t *testing.T) {
	var n atomic.Int32
	var times []time.Time
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		times = append(times, time.Now())
		mu.Unlock()
		if n.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer srv.Close()
	client := (&Guard{AllowPrivate: true}).Client(5 * time.Second)
	res := PostJSON(context.Background(), client, srv.URL, []byte(`{}`), nil, []time.Duration{30 * time.Millisecond, 60 * time.Millisecond})
	if res.Err != nil || res.Attempts != 3 || res.StatusCode == nil || *res.StatusCode != 204 {
		t.Fatalf("result: %+v", res)
	}
	if gap1, gap2 := times[1].Sub(times[0]), times[2].Sub(times[1]); gap1 < 30*time.Millisecond || gap2 < 60*time.Millisecond {
		t.Errorf("backoff gaps: %v %v", gap1, gap2)
	}
	// 계속 실패하면 3번에서 멈춘다
	n.Store(-100)
	res = PostJSON(context.Background(), client, srv.URL, []byte(`{}`), nil, []time.Duration{time.Millisecond, time.Millisecond})
	if res.Err == nil || res.Attempts != 3 || *res.StatusCode != 503 || res.Err.Error() != "HTTP 503" {
		t.Errorf("exhausted: %+v", res)
	}
	// 4xx는 다시 보내지 않는다
	var bad atomic.Int32
	srv4 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { bad.Add(1); w.WriteHeader(404) }))
	defer srv4.Close()
	res = PostJSON(context.Background(), client, srv4.URL, []byte(`{}`), nil, []time.Duration{time.Millisecond, time.Millisecond})
	if res.Err == nil || res.Attempts != 1 || bad.Load() != 1 {
		t.Errorf("4xx: %+v hits=%d", res, bad.Load())
	}
	// 시도마다 제한 시간이 있다
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	res = PostJSON(context.Background(), (&Guard{AllowPrivate: true}).Client(50*time.Millisecond), slow.URL, []byte(`{}`), nil, []time.Duration{time.Millisecond})
	if res.Err == nil || res.Attempts != 2 {
		t.Errorf("timeout: %+v", res)
	}
}

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "k.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDispatchSignsAndLogsDeliveries(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	p := &store.Project{Name: "acme"}
	env, err := st.CreateProject(ctx, p)
	if err != nil {
		t.Fatal(err)
	}
	type got struct {
		sig  string
		body []byte
	}
	recv := make(chan got, 10)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recv <- got{r.Header.Get("X-Keel-Signature"), b}
	}))
	defer srv.Close()
	pid := p.ID
	hook := &store.NotificationChannel{ProjectID: &pid, Kind: store.ChannelWebhook, Name: "ci", Config: store.ChannelConfig{URL: srv.URL, Secret: "topsecret"}, Events: []string{"deploy.*"}, Enabled: true}
	ws := &store.NotificationChannel{Kind: store.ChannelSlack, Name: "all", Config: store.ChannelConfig{URL: srv.URL + "/slack"}, Events: []string{"alert.*"}, Enabled: true}
	off := &store.NotificationChannel{ProjectID: &pid, Kind: store.ChannelWebhook, Name: "off", Config: store.ChannelConfig{URL: srv.URL}, Events: []string{"*"}, Enabled: false}
	for _, c := range []*store.NotificationChannel{hook, ws, off} {
		if err := st.CreateChannel(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	dep := "dep_123"
	actor := "alice"
	e := &store.Event{ProjectID: p.ID, EnvironmentID: &env.ID, Kind: "deploy.failed", Message: "web 배포 실패", DeploymentID: &dep, Actor: &actor}
	st.CreateEvent(ctx, e)
	n := &Notifier{Store: st, Guard: &Guard{AllowPrivate: true}, Backoff: []time.Duration{}}
	n.Dispatch(e)
	select {
	case g := <-recv:
		if !Verify("topsecret", g.body, g.sig) {
			t.Errorf("signature %q does not match body", g.sig)
		}
		var pl Payload
		if err := json.Unmarshal(g.body, &pl); err != nil {
			t.Fatal(err)
		}
		if pl.ID != e.ID || pl.Kind != "deploy.failed" || pl.Project.Name != "acme" || pl.Environment == nil || pl.Environment.Name != "production" ||
			pl.DeploymentID == nil || *pl.DeploymentID != dep || pl.Actor == nil || *pl.Actor != "alice" || pl.Service != nil {
			t.Errorf("payload: %+v", pl)
		}
	default:
		t.Fatal("webhook not delivered")
	}
	if len(recv) != 0 {
		t.Errorf("unexpected extra deliveries: %d", len(recv))
	}
	ds, _ := st.ListDeliveries(ctx, hook.ID)
	if len(ds) != 1 || ds[0].Status != "ok" || ds[0].EventID != e.ID || ds[0].Attempts != 1 || *ds[0].StatusCode != 200 {
		t.Errorf("deliveries: %+v", ds)
	}
	// 작업 공간 Slack 채널은 alert.* 만
	a := &store.Event{ProjectID: p.ID, Kind: "alert.firing", Message: "cpu"}
	st.CreateEvent(ctx, a)
	n.Dispatch(a)
	g := <-recv
	if g.sig != "" || !strings.Contains(string(g.body), `"text":"[acme] alert.firing: cpu"`) {
		t.Errorf("slack: %s %s", g.sig, g.body)
	}
	// 전달 기록은 채널마다 최근 100개
	for i := 0; i < store.MaxDeliveries+5; i++ {
		st.AddDelivery(ctx, &store.Delivery{ChannelID: hook.ID, Status: "ok"})
	}
	if ds, _ := st.ListDeliveries(ctx, hook.ID); len(ds) != store.MaxDeliveries {
		t.Errorf("delivery log not trimmed: %d", len(ds))
	}
	// 채널 설정은 암호화되어 저장된다(박스가 있을 때) — 여기서는 왕복만 확인
	c, _ := st.GetChannel(ctx, hook.ID)
	if c.Config.Secret != "topsecret" || c.Config.URL != srv.URL {
		t.Errorf("config round trip: %+v", c.Config)
	}
}

func TestLegacyProjectWebhookStillDelivered(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	recv := make(chan []byte, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		recv <- b
	}))
	defer srv.Close()
	url := srv.URL
	p := &store.Project{Name: "old", NotifyWebhookURL: &url, NotifyEvents: []string{"deploy.success"}}
	if _, err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	n := &Notifier{Store: st, Guard: &Guard{AllowPrivate: true}, Backoff: []time.Duration{}}
	for _, k := range []string{"deploy.failed", "deploy.live"} {
		e := &store.Event{ProjectID: p.ID, Kind: k, Message: "m"}
		st.CreateEvent(ctx, e)
		n.Dispatch(e)
	}
	if len(recv) != 1 || !strings.Contains(string(<-recv), "deploy.success") {
		t.Error("legacy webhook: want exactly the deploy.live → deploy.success delivery")
	}
	// 사설 목적지 차단은 예전 webhook에도 적용된다
	n2 := &Notifier{Store: st, Guard: &Guard{}, Backoff: []time.Duration{}}
	e := &store.Event{ProjectID: p.ID, Kind: "deploy.live", Message: "m"}
	st.CreateEvent(ctx, e)
	n2.Dispatch(e)
	if len(recv) != 0 {
		t.Error("legacy webhook reached a loopback address without KEEL_NOTIFY_ALLOW_PRIVATE")
	}
}

func TestFanoutRespectsVisibilityAndPreferences(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	p := &store.Project{Name: "acme"}
	if _, err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	mk := func(email, role string) *store.User {
		u := &store.User{Email: email, Name: email, Role: role}
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
		if err := st.SetWorkspaceMember(ctx, store.DefaultWorkspaceID, u.ID, role); err != nil {
			t.Fatal(err)
		}
		return u
	}
	admin := mk("a@example.com", store.RoleAdmin)
	member := mk("m@example.com", store.RoleMember)
	outsider := mk("o@example.com", store.RoleMember)
	quiet := mk("q@example.com", store.RoleAdmin)
	st.SetMember(ctx, p.ID, member.ID, store.RoleViewer)
	st.SetUserNotificationEvents(ctx, quiet.ID, []string{"volume.*"})
	n := &Notifier{Store: st}
	for _, k := range []string{"deploy.failed", "deploy.live", "alert.firing"} {
		e := &store.Event{ProjectID: p.ID, Kind: k, Message: k}
		st.CreateEvent(ctx, e)
		if err := n.Fanout(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	count := func(u *store.User) int {
		ns, unread, err := st.ListUserNotifications(ctx, u.ID, false, 50)
		if err != nil || len(ns) != unread {
			t.Fatalf("list: %v %d %v", ns, unread, err)
		}
		return len(ns)
	}
	if count(admin) != 2 || count(member) != 2 || count(outsider) != 0 || count(quiet) != 0 {
		t.Errorf("fanout: admin=%d member=%d outsider=%d quiet=%d", count(admin), count(member), count(outsider), count(quiet))
	}
	ns, _, _ := st.ListUserNotifications(ctx, admin.ID, false, 50)
	if n, _ := st.MarkNotificationsRead(ctx, admin.ID, []string{ns[0].ID}); n != 1 {
		t.Errorf("mark one: %d", n)
	}
	if n, _ := st.MarkNotificationsRead(ctx, member.ID, []string{ns[1].ID}); n != 0 {
		t.Error("marked someone else's notification")
	}
	if _, unread, _ := st.ListUserNotifications(ctx, admin.ID, true, 50); unread != 1 {
		t.Errorf("unread after mark: %d", unread)
	}
	st.MarkNotificationsRead(ctx, admin.ID, nil)
	if _, unread, _ := st.ListUserNotifications(ctx, admin.ID, true, 50); unread != 0 {
		t.Errorf("unread after mark all: %d", unread)
	}
}

// ---- SMTP ----

type fakeSMTP struct {
	addr string
	mu   sync.Mutex
	from string
	rcpt []string
	data string
	auth string
}

// startFakeSMTP는 최소한의 SMTP 서버(EHLO/AUTH PLAIN/MAIL/RCPT/DATA/QUIT)다.
func startFakeSMTP(t *testing.T) *fakeSMTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeSMTP{addr: ln.Addr().String()}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return f
}

func (f *fakeSMTP) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	say := func(s string) { io.WriteString(c, s+"\r\n") }
	say("220 fake ESMTP")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		cmd := strings.ToUpper(line)
		switch {
		case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
			say("250-fake")
			say("250 AUTH PLAIN")
		case strings.HasPrefix(cmd, "AUTH PLAIN"):
			f.mu.Lock()
			f.auth = strings.TrimSpace(line[len("AUTH PLAIN"):])
			f.mu.Unlock()
			say("235 ok")
		case strings.HasPrefix(cmd, "MAIL FROM:"):
			f.mu.Lock()
			f.from = line[len("MAIL FROM:"):]
			f.mu.Unlock()
			say("250 ok")
		case strings.HasPrefix(cmd, "RCPT TO:"):
			f.mu.Lock()
			f.rcpt = append(f.rcpt, line[len("RCPT TO:"):])
			f.mu.Unlock()
			say("250 ok")
		case cmd == "DATA":
			say("354 go")
			var b strings.Builder
			for {
				l, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if l == ".\r\n" {
					break
				}
				b.WriteString(l)
			}
			f.mu.Lock()
			f.data = b.String()
			f.mu.Unlock()
			say("250 queued")
		case cmd == "QUIT":
			say("221 bye")
			return
		default:
			say("250 ok")
		}
	}
}

func TestBuildMessageFormatting(t *testing.T) {
	msg, err := BuildMessage("aven <aven@example.com>", []string{"a@example.com", "b@example.com"}, "[aven] acme · deploy.failed", "배포가 실패했습니다\n두 번째 줄", time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	m, err := mail.ReadMessage(strings.NewReader(string(msg)))
	if err != nil {
		t.Fatal(err)
	}
	subj, _ := new(mime.WordDecoder).DecodeHeader(m.Header.Get("Subject"))
	if subj != "[aven] acme · deploy.failed" || m.Header.Get("To") != "a@example.com, b@example.com" ||
		m.Header.Get("Content-Type") != "text/plain; charset=utf-8" || m.Header.Get("Content-Transfer-Encoding") != "base64" ||
		!strings.HasSuffix(m.Header.Get("Message-ID"), "@example.com>") || m.Header.Get("Date") != "Thu, 02 Jan 2025 03:04:05 +0000" {
		t.Errorf("headers: %v (subject %q)", m.Header, subj)
	}
	raw, _ := io.ReadAll(m.Body)
	body, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	if err != nil || string(body) != "배포가 실패했습니다\r\n두 번째 줄" {
		t.Errorf("body: %q %v", body, err)
	}
	for _, line := range strings.Split(string(msg), "\r\n") {
		if len(line) > 78 {
			t.Errorf("line too long: %q", line)
		}
	}
	// 헤더 주입 방지
	if _, err := BuildMessage("aven@example.com", []string{"a@example.com\r\nBcc: x@evil.test"}, "s", "b", time.Now()); err == nil {
		t.Error("recipient with CRLF accepted")
	}
	if _, err := BuildMessage("aven@example.com", []string{"a@example.com"}, "s\r\nBcc: x@evil.test", "b", time.Now()); err == nil {
		t.Error("subject with CRLF accepted")
	}
}

func TestMailerSendsViaSMTP(t *testing.T) {
	f := startFakeSMTP(t)
	host, port, _ := net.SplitHostPort(f.addr)
	env := map[string]string{"KEEL_SMTP_HOST": host, "KEEL_SMTP_PORT": port, "KEEL_SMTP_FROM": "aven <noreply@example.com>", "KEEL_SMTP_TLS": "none",
		"KEEL_SMTP_USERNAME": "user", "KEEL_SMTP_PASSWORD": "pw"}
	m, err := MailerFromEnv(func(k string) string { return env[k] })
	if err != nil || m == nil || m.Port == 0 {
		t.Fatalf("mailer: %+v %v", m, err)
	}
	pl := &Payload{ID: "evt_1", Kind: "alert.firing", Message: "CPU 사용률이 높습니다", Project: Ref{ID: "prj_1", Name: "acme"},
		Service: &Ref{ID: "svc_1", Name: "web"}, CreatedAt: time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)}
	subject, body := EmailFor(pl, "https://aven.example.com")
	if err := m.Send(context.Background(), []string{"ops@example.com"}, subject, body); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.from != "<noreply@example.com>" || len(f.rcpt) != 1 || f.rcpt[0] != "<ops@example.com>" {
		t.Errorf("envelope: %q %v", f.from, f.rcpt)
	}
	if a, _ := base64.StdEncoding.DecodeString(f.auth); string(a) != "\x00user\x00pw" {
		t.Errorf("auth: %q", a)
	}
	msg, err := mail.ReadMessage(strings.NewReader(f.data))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(msg.Body)
	text, _ := base64.StdEncoding.DecodeString(strings.ReplaceAll(string(raw), "\r\n", ""))
	for _, want := range []string{"CPU 사용률이 높습니다", "프로젝트: acme", "서비스: web", "이벤트: alert.firing", "https://aven.example.com/?project=prj_1"} {
		if !strings.Contains(string(text), want) {
			t.Errorf("mail body missing %q:\n%s", want, text)
		}
	}
	// STARTTLS를 요구하는데 서버가 지원하지 않으면 평문으로 보내지 않는다
	m.TLS = "starttls"
	if err := m.Send(context.Background(), []string{"ops@example.com"}, "s", "b"); err == nil || !strings.Contains(err.Error(), "STARTTLS") {
		t.Errorf("starttls downgrade: %v", err)
	}
	for _, bad := range []map[string]string{{"KEEL_SMTP_HOST": "x"}, {"KEEL_SMTP_HOST": "x", "KEEL_SMTP_FROM": "a@b.c", "KEEL_SMTP_TLS": "ssl"}, {"KEEL_SMTP_HOST": "x", "KEEL_SMTP_FROM": "a@b.c", "KEEL_SMTP_PORT": "0"}} {
		if _, err := MailerFromEnv(func(k string) string { return bad[k] }); err == nil {
			t.Errorf("bad env accepted: %v", bad)
		}
	}
	if m, err := MailerFromEnv(func(string) string { return "" }); m != nil || err != nil {
		t.Error("no host should mean no mailer")
	}
}

func TestEmailChannelDelivery(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	p := &store.Project{Name: "acme"}
	st.CreateProject(ctx, p)
	f := startFakeSMTP(t)
	host, port, _ := net.SplitHostPort(f.addr)
	env := map[string]string{"KEEL_SMTP_HOST": host, "KEEL_SMTP_PORT": port, "KEEL_SMTP_FROM": "noreply@example.com", "KEEL_SMTP_TLS": "none"}
	m, _ := MailerFromEnv(func(k string) string { return env[k] })
	ch := &store.NotificationChannel{Kind: store.ChannelEmail, Name: "ops", Config: store.ChannelConfig{Recipients: []string{"ops@example.com"}}, Events: []string{"*"}, Enabled: true}
	st.CreateChannel(ctx, ch)
	n := &Notifier{Store: st, Mailer: m}
	res := n.Test(ctx, ch, p, "bob")
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	ds, _ := st.ListDeliveries(ctx, ch.ID)
	if len(ds) != 1 || ds[0].Status != "ok" || ds[0].EventKind != "notification.test" {
		t.Errorf("deliveries: %+v", ds)
	}
	n.Mailer = nil
	if res := n.Test(ctx, ch, p, ""); res.Err == nil {
		t.Error("email without SMTP should fail")
	}
}
