package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/turin-dev/aven/internal/store"
)

// An open project event stream closes once the caller loses access to the project.
func TestEventStreamClosesWhenAccessIsRevoked(t *testing.T) {
	old := inboxRecheck
	inboxRecheck = 30 * time.Millisecond
	t.Cleanup(func() { inboxRecheck = old })
	f, h := authServer(t)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	alice := testUser(t, f.st, "alice@example.com", store.RoleMember)
	if err := f.st.SetMember(f.ctx, f.p.ID, alice.ID, store.RoleMember); err != nil {
		t.Fatal(err)
	}
	rq, _ := http.NewRequest("GET", srv.URL+"/api/projects/"+f.p.ID+"/events/stream", nil)
	rq.AddCookie(&http.Cookie{Name: cookieName, Value: testSessionCookie(t, f.srv, alice)})
	resp, err := http.DefaultClient.Do(rq)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("stream: %v %v", resp, err)
	}
	c := &sseClient{lines: make(chan string, 64), resp: resp}
	go func() {
		defer close(c.lines)
		buf := make([]byte, 4096)
		for {
			if _, err := resp.Body.Read(buf); err != nil {
				return
			}
			c.lines <- "chunk"
		}
	}()
	t.Cleanup(func() { resp.Body.Close() })
	// Still a member: the stream stays open.
	if closed := waitClosed(c, 200*time.Millisecond); closed {
		t.Fatal("stream closed while the user is still a member")
	}
	if err := f.st.RemoveMember(f.ctx, f.p.ID, alice.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RemoveWorkspaceMember(f.ctx, store.DefaultWorkspaceID, alice.ID); err != nil {
		t.Logf("remove workspace member: %v", err)
	}
	if !waitClosed(c, 3*time.Second) {
		t.Fatal("stream still open after the membership was removed")
	}
}

func waitClosed(c *sseClient, d time.Duration) bool {
	timeout := time.After(d)
	for {
		select {
		case _, ok := <-c.lines:
			if !ok {
				return true
			}
		case <-timeout:
			return false
		}
	}
}
