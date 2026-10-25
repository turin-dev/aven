package api

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/notify"
	"github.com/turin-dev/aven/internal/store"
)

// sseClient는 알림 스트림을 열고 읽은 줄을 채널로 넘긴다(본문이 끝나면 채널을 닫는다).
type sseClient struct {
	lines chan string
	resp  *http.Response
}

func openInboxStream(t *testing.T, base, cookie string) *sseClient {
	t.Helper()
	rq, _ := http.NewRequest("GET", base+"/api/notifications/stream", nil)
	rq.AddCookie(&http.Cookie{Name: cookieName, Value: cookie})
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("stream: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	c := &sseClient{lines: make(chan string, 64), resp: resp}
	go func() {
		defer close(c.lines)
		br := bufio.NewReader(resp.Body)
		for {
			l, err := br.ReadString('\n')
			if l = strings.TrimRight(l, "\r\n"); l != "" {
				c.lines <- l
			}
			if err != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	return c
}

// next는 pred에 맞는 줄을 기다린다. 스트림이 닫히면 ("", false)다.
func (c *sseClient) next(t *testing.T, d time.Duration, pred func(string) bool) (string, bool) {
	t.Helper()
	timeout := time.After(d)
	for {
		select {
		case l, ok := <-c.lines:
			if !ok {
				return "", false
			}
			if pred(l) {
				return l, true
			}
		case <-timeout:
			return "", false
		}
	}
}

// none은 d 동안 pred에 맞는 줄이 오지 않았는지 본다.
func (c *sseClient) none(d time.Duration, pred func(string) bool) bool {
	timeout := time.After(d)
	for {
		select {
		case l, ok := <-c.lines:
			if !ok {
				return true
			}
			if pred(l) {
				return false
			}
		case <-timeout:
			return true
		}
	}
}

func isData(l string) bool {
	return strings.HasPrefix(l, "data: ") && strings.Contains(l, `"event_id"`)
}

func waitSubs(t *testing.T, hub *notify.InboxHub, uid string, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if hub.Subscribers(uid) == n {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("subscribers of %s: %d, want %d", uid, hub.Subscribers(uid), n)
}

func TestNotificationStreamDeliveryAndIsolation(t *testing.T) {
	oldHB, oldRC := inboxHeartbeat, inboxRecheck
	inboxHeartbeat, inboxRecheck = 80*time.Millisecond, time.Hour
	t.Cleanup(func() { inboxHeartbeat, inboxRecheck = oldHB, oldRC })

	ctx := context.Background()
	st := testStore(t)
	n := &notify.Notifier{Store: st}
	s := &Server{Store: st, Notifier: n, AdminPassword: "password123", SessionKey: make([]byte, 32), Log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)

	pa, pb := &store.Project{Name: "alpha"}, &store.Project{Name: "beta"}
	for _, p := range []*store.Project{pa, pb} {
		if _, err := st.CreateProject(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	alice := testUser(t, st, "alice@example.com", store.RoleMember)
	bob := testUser(t, st, "bob@example.com", store.RoleMember)
	if err := st.SetMember(ctx, pa.ID, alice.ID, store.RoleViewer); err != nil {
		t.Fatal(err)
	}
	if err := st.SetMember(ctx, pb.ID, bob.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	aliceC, bobC := testSessionCookie(t, s, alice), testSessionCookie(t, s, bob)
	sa, sb := openInboxStream(t, srv.URL, aliceC), openInboxStream(t, srv.URL, bobC)
	hub := n.Inbox()
	waitSubs(t, hub, alice.ID, 1)
	waitSubs(t, hub, bob.ID, 1)

	fire := func(p *store.Project, msg string) {
		e := &store.Event{ProjectID: p.ID, Kind: "deploy.failed", Message: msg}
		if err := st.CreateEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
		if err := n.Fanout(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	fire(pa, "alpha-failed")
	l, ok := sa.next(t, 3*time.Second, isData)
	if !ok {
		t.Fatal("alice did not receive her notification")
	}
	var it notify.InboxItem
	if err := json.Unmarshal([]byte(strings.TrimPrefix(l, "data: ")), &it); err != nil || it.ProjectID != pa.ID || it.Message != "alpha-failed" || it.Kind != "deploy.failed" {
		t.Errorf("alice item: %s (%v)", l, err)
	}
	if !sb.none(300*time.Millisecond, isData) {
		t.Error("bob received a notification for a project he cannot see")
	}
	fire(pb, "beta-failed")
	if l, ok := sb.next(t, 3*time.Second, isData); !ok || !strings.Contains(l, "beta-failed") {
		t.Errorf("bob: %q %v", l, ok)
	}
	if !sa.none(300*time.Millisecond, isData) {
		t.Error("alice received bob's notification")
	}
	// heartbeat 주석
	if _, ok := sa.next(t, 2*time.Second, func(l string) bool { return l == ": ping" }); !ok {
		t.Error("no heartbeat")
	}

	// 로그아웃하면 그 세션의 스트림은 revoked를 보내고 닫힌다. 다른 사용자 스트림은 그대로다.
	rq, _ := http.NewRequest("POST", srv.URL+"/api/auth/logout", nil)
	rq.AddCookie(&http.Cookie{Name: cookieName, Value: aliceC})
	resp, err := http.DefaultClient.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("logout: %d", resp.StatusCode)
	}
	if _, ok := sa.next(t, 3*time.Second, func(l string) bool { return l == "event: revoked" }); !ok {
		t.Error("alice stream was not revoked after logout")
	}
	if _, ok := sa.next(t, 3*time.Second, func(string) bool { return false }); ok {
		t.Error("alice stream still open")
	}
	waitSubs(t, hub, alice.ID, 0)
	if hub.Subscribers(bob.ID) != 1 {
		t.Error("bob's stream closed by alice's logout")
	}

	// 다른 세션에서 세션을 끊어도 닫힌다.
	other := testSessionCookie(t, s, bob)
	bobSes, err := st.GetSessionByHash(ctx, hashSecret(bobC))
	if err != nil {
		t.Fatal(err)
	}
	if w := req(s.Handler(), "DELETE", "/api/auth/sessions/"+bobSes.ID, "", withCookie(other)); w.Code != 204 {
		t.Fatalf("delete session: %d %s", w.Code, w.Body)
	}
	if _, ok := sb.next(t, 3*time.Second, func(l string) bool { return l == "event: revoked" }); !ok {
		t.Error("bob stream was not revoked after session delete")
	}

	// 인증 없이는 401
	if w := req(s.Handler(), "GET", "/api/notifications/stream", "", nil); w.Code != 401 {
		t.Errorf("anonymous stream: %d", w.Code)
	}
}

func TestInboxHubIsolation(t *testing.T) {
	var h notify.InboxHub
	a, cancelA := h.Subscribe("u1")
	b, cancelB := h.Subscribe("u2")
	defer cancelB()
	h.Publish("u1", notify.InboxItem{EventID: "e1"})
	select {
	case it := <-a:
		if it.EventID != "e1" {
			t.Errorf("got %v", it)
		}
	default:
		t.Error("u1 got nothing")
	}
	select {
	case it := <-b:
		t.Errorf("u2 got %v", it)
	default:
	}
	// 가득 찬 구독자 때문에 Publish가 막히지 않는다.
	for i := 0; i < 100; i++ {
		h.Publish("u1", notify.InboxItem{EventID: "x"})
	}
	cancelA()
	cancelA()
	if h.Subscribers("u1") != 0 {
		t.Error("cancel did not unsubscribe")
	}
	k := h.Kicked()
	h.Kick()
	select {
	case <-k:
	default:
		t.Error("kick did not close the channel")
	}
}
